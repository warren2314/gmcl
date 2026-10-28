package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/url"
	"strings"
	"testing"
)

func TestJuniorGroundsWorkflowPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-grounds-test-secret")
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	svc := junior.NewService(pool)
	p := seed.Admin
	g := junior.Ground{Name: "Fictional Neutral Ground", Address: "1 Fictional Lane", Postcode: "zz11aa", Source: "Fictional test only", RulesConfirmed: true, Layouts: []junior.GroundLayout{{"U11": 2}, {"U13": 1}, {"U15": 1}}}
	gid, err := svc.SaveGround(ctx, p, g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SaveGround(ctx, p, g); err == nil {
		t.Fatal("duplicate physical venue accepted")
	}
	if _, err = svc.Grounds(ctx, junior.Principal{ClubID: seed.Clubs[0], ReadOnly: true}); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("club exposed grounds administration: %v", err)
	}
	comp := func(name, age string) int64 {
		t.Helper()
		id, e := svc.CreateCompetition(ctx, p, junior.Competition{SeasonID: seed.SeasonID, Name: name, AgeGroup: age, Format: "custom", StartDate: "2027-05-01", EndDate: "2027-06-30", DefaultStartTime: "18:00"})
		if e != nil {
			t.Fatal(e)
		}
		for _, team := range seed.Teams {
			if e = svc.SaveCompetitionEntry(ctx, p, id, juniorCompetitionRevision(t, ctx, svc, p, id), team, true); e != nil {
				t.Fatal(e)
			}
		}
		e = svc.SaveFixtureConstraints(ctx, p, id, juniorCompetitionRevision(t, ctx, svc, p, id), junior.FixtureConstraints{PlayingDates: []string{"2027-05-01", "2027-05-02"}, DurationMinutes: 60, Grounds: []junior.GroundConstraint{{ClubID: seed.Clubs[0], GroundID: gid}}})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	one, two := comp("Layout U13", "U13"), comp("Layout U15", "U15")
	save := func(id int64, f junior.Fixture) {
		t.Helper()
		if e := svc.SaveDraftFixture(ctx, p, id, juniorCompetitionRevision(t, ctx, svc, p, id), f); e != nil {
			t.Fatal(e)
		}
	}
	f := junior.Fixture{HomeTeamID: seed.Teams[0], AwayTeamID: seed.Teams[1], MatchDate: "2027-05-01", StartTime: "18:00", GroundID: gid}
	save(one, f)
	save(two, f)
	issues, err := svc.FixtureChecks(ctx, p, one)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range issues {
		found = found || i.Code == "ground_clash" && i.Blocking && strings.Contains(i.Message, "ZZ1 1AA")
	}
	if !found {
		t.Fatalf("cross-draft clash missing: %+v", issues)
	}
	if _, err = svc.PublishFixtures(ctx, p, one, juniorCompetitionRevision(t, ctx, svc, p, one), true, "checked"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("clashing publication: %v", err)
	}
	drafts, err := svc.ListFixtures(ctx, p, two, false)
	if err != nil {
		t.Fatal(err)
	}
	f.ID = drafts[0].ID
	f.MatchDate = "2027-05-02"
	save(two, f)
	rev := juniorCompetitionRevision(t, ctx, svc, p, one)
	gs, err := svc.Grounds(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	g = gs[0]
	g.RulesConfirmed = true
	g.Name = "Fictional Neutral Ground revised"
	if _, err = svc.SaveGround(ctx, p, g); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PublishFixtures(ctx, p, one, rev, true, "checked"); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("stale venue review published: %v", err)
	}
	pub, err := svc.PublishFixtures(ctx, p, one, juniorCompetitionRevision(t, ctx, svc, p, one), true, "checked fictional venue and external coverage")
	if err != nil {
		t.Fatal(err)
	}
	var frozenName, postcode string
	if err = pool.QueryRow(ctx, `SELECT ground_name,ground_postcode FROM junior_publication_fixtures WHERE publication_id=$1`, pub).Scan(&frozenName, &postcode); err != nil {
		t.Fatal(err)
	}
	if frozenName != g.Name || postcode != "ZZ1 1AA" {
		t.Fatalf("publication used stale venue: %q %q", frozenName, postcode)
	}
	gs, _ = svc.Grounds(ctx, p)
	g = gs[0]
	g.RulesConfirmed = true
	g.Name = "Later venue name"
	g.Postcode = "ZZ1 2BB"
	if _, err = svc.SaveGround(ctx, p, g); err != nil {
		t.Fatal(err)
	}
	official, err := svc.ListFixtures(ctx, p, one, true)
	if err != nil || official[0].GroundName != frozenName || official[0].GroundPostcode != postcode {
		t.Fatalf("publication changed: %+v %v", official, err)
	}
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	page := adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", "/admin/juniors/grounds", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Permitted layouts") {
		t.Fatalf("grounds page %d: %s", page.Code, page.Body.String())
	}
	page = adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", fmt.Sprintf("/admin/juniors/competitions/%d", one), nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `name="ground_id"`) {
		t.Fatalf("fixture venue select %d", page.Code)
	}
	// Missing confirmation is rejected server-side, not just by HTML validation.
	page = adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", "/admin/juniors/grounds", url.Values{"action": {"save_ground"}, "name": {"Bad"}, "address": {"2 Fictional Lane"}, "postcode": {"ZZ1 1AA"}, "source": {"test"}, "layout_0_U11": {"2"}})
	if page.Code < 400 {
		t.Fatalf("unconfirmed save accepted: %d", page.Code)
	}
}

func TestJuniorGroundLegacyLinkAndGenerationPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	svc := junior.NewService(pool)
	p := seed.Admin
	makeComp := func(name, format string) int64 {
		t.Helper()
		id, e := svc.CreateCompetition(ctx, p, junior.Competition{SeasonID: seed.SeasonID, Name: name, AgeGroup: "U13", Format: format, StartDate: "2027-05-01", EndDate: "2027-05-31", DefaultStartTime: "18:00"})
		if e != nil {
			t.Fatal(e)
		}
		for _, team := range seed.Teams[:2] {
			if e = svc.SaveCompetitionEntry(ctx, p, id, juniorCompetitionRevision(t, ctx, svc, p, id), team, true); e != nil {
				t.Fatal(e)
			}
		}
		return id
	}
	legacy := makeComp("Legacy venue", "custom")
	constraints := junior.FixtureConstraints{DurationMinutes: 60, PlayingDates: []string{"2027-05-01", "2027-05-02"}, Grounds: []junior.GroundConstraint{{ClubID: seed.Clubs[0], Name: "Original venue label", Capacity: 1}}}
	if err := svc.SaveFixtureConstraints(ctx, p, legacy, juniorCompetitionRevision(t, ctx, svc, p, legacy), constraints); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveDraftFixture(ctx, p, legacy, juniorCompetitionRevision(t, ctx, svc, p, legacy), junior.Fixture{HomeTeamID: seed.Teams[0], AwayTeamID: seed.Teams[1], MatchDate: "2027-05-01", StartTime: "18:00", GroundName: "Original venue label"}); err != nil {
		t.Fatal(err)
	}
	pub, err := svc.PublishFixtures(ctx, p, legacy, juniorCompetitionRevision(t, ctx, svc, p, legacy), true, "verified legacy example")
	if err != nil {
		t.Fatal(err)
	}
	var before string
	var fixture int64
	if err = pool.QueryRow(ctx, `SELECT row_to_json(f)::text,fixture_id FROM junior_publication_fixtures f WHERE publication_id=$1`, pub).Scan(&before, &fixture); err != nil {
		t.Fatal(err)
	}
	gid, err := svc.SaveGround(ctx, p, junior.Ground{Name: "Saved neutral venue", Address: "1 Fictional Lane", Postcode: "ZZ1 1AA", Source: "Synthetic test only", RulesConfirmed: true, Layouts: []junior.GroundLayout{{"U13": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.LinkPublishedGround(ctx, p, pub, fixture, gid); err != nil {
		t.Fatal(err)
	}
	if err = svc.LinkPublishedGround(ctx, p, pub, fixture, gid); err != nil {
		t.Fatal(err)
	}
	var after string
	var links int
	if err = pool.QueryRow(ctx, `SELECT row_to_json(f)::text,(SELECT count(*) FROM junior_publication_ground_links WHERE publication_id=$1) FROM junior_publication_fixtures f WHERE publication_id=$1`, pub).Scan(&after, &links); err != nil || before != after || links != 1 {
		t.Fatalf("history or idempotence: %v links=%d", err, links)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM junior_publication_ground_links WHERE publication_id=$1`, pub); err == nil {
		t.Fatal("append-only link was deleted")
	}
	oldDrafts, err := svc.ListFixtures(ctx, p, legacy, false)
	if err != nil {
		t.Fatal(err)
	}
	f := oldDrafts[0]
	f.GroundID = gid
	if err = svc.SaveDraftFixture(ctx, p, legacy, juniorCompetitionRevision(t, ctx, svc, p, legacy), f); err != nil {
		t.Fatal(err)
	}
	generated := makeComp("Generated round", "once")
	constraints.Grounds = []junior.GroundConstraint{{ClubID: seed.Clubs[0], GroundID: gid}, {ClubID: seed.Clubs[1], GroundID: gid}}
	if err = svc.SaveFixtureConstraints(ctx, p, generated, juniorCompetitionRevision(t, ctx, svc, p, generated), constraints); err != nil {
		t.Fatal(err)
	}
	if err = svc.GenerateFixtures(ctx, p, generated, juniorCompetitionRevision(t, ctx, svc, p, generated)); err != nil {
		t.Fatal(err)
	}
	drafts, err := svc.ListFixtures(ctx, p, generated, false)
	if err != nil || len(drafts) != 1 || drafts[0].MatchDate != "2027-05-02" || drafts[0].GroundID != gid {
		t.Fatalf("generator did not avoid saved published/draft booking: %+v %v", drafts, err)
	}
}
