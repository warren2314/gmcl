package httpserver

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"github.com/go-chi/chi/v5"
)

type juniorFixtureSeed struct {
	Admin    junior.Principal
	SeasonID int64
	Clubs    []int32
	Teams    []int64
}

func seedJuniorFixtureProgramme(t *testing.T, ctx context.Context, pool *db.Pool) juniorFixtureSeed {
	t.Helper()
	admin, a, b := seedJuniorAccessAdminClub(t, ctx, pool)
	seed := juniorFixtureSeed{Admin: junior.Principal{AdminID: admin}, Clubs: []int32{a, b}}
	var third int32
	if err := pool.QueryRow(ctx, `INSERT INTO clubs(name) VALUES('Synthetic Junior Club C') RETURNING id`).Scan(&third); err != nil {
		t.Fatal(err)
	}
	seed.Clubs = append(seed.Clubs, third)
	if err := pool.QueryRow(ctx, `INSERT INTO junior_seasons(name,start_date,end_date) VALUES('Synthetic Junior 2027','2027-04-01','2027-09-30') RETURNING id`).Scan(&seed.SeasonID); err != nil {
		t.Fatal(err)
	}
	for i, club := range seed.Clubs {
		if _, err := pool.Exec(ctx, `INSERT INTO junior_club_seasons(club_id,season_id) VALUES($1,$2)`, club, seed.SeasonID); err != nil {
			t.Fatal(err)
		}
		var team int64
		if err := pool.QueryRow(ctx, `INSERT INTO junior_teams(club_id,name) VALUES($1,$2) RETURNING id`, club, fmt.Sprintf("Synthetic U13 %d", i+1)).Scan(&team); err != nil {
			t.Fatal(err)
		}
		seed.Teams = append(seed.Teams, team)
	}
	return seed
}
func juniorCompetitionRevision(t *testing.T, ctx context.Context, svc *junior.Service, p junior.Principal, id int64) int64 {
	t.Helper()
	c, err := svc.GetCompetition(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	return c.Revision
}

func TestJuniorFixturePublicationAndConcessionReviewPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-fixture-test-secret")
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	svc := junior.NewService(pool)
	p := seed.Admin
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	staffRequest := func(path string) *httptest.ResponseRecorder {
		return adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", path, nil)
	}
	comp, err := svc.CreateCompetition(ctx, p, junior.Competition{SeasonID: seed.SeasonID, Name: "Synthetic U13 once", AgeGroup: "U13", Division: "North", Format: "once", StartDate: "2027-05-01", EndDate: "2027-05-31", DefaultStartTime: "18:00"})
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 { return juniorCompetitionRevision(t, ctx, svc, p, comp) }
	for _, team := range seed.Teams {
		if err = svc.SaveCompetitionEntry(ctx, p, comp, revision(), team, true); err != nil {
			t.Fatal(err)
		}
	}
	constraints := junior.FixtureConstraints{PlayingDates: []string{"2027-05-01", "2027-05-08", "2027-05-15"}, ReserveDates: []string{"2027-05-22"}, DurationMinutes: 60}
	for i, club := range seed.Clubs {
		constraints.Grounds = append(constraints.Grounds, junior.GroundConstraint{ClubID: club, Name: fmt.Sprintf("Synthetic Ground %d", i+1), Capacity: 1})
	}
	if err = svc.SaveFixtureConstraints(ctx, p, comp, revision(), constraints); err != nil {
		t.Fatal(err)
	}
	if err = svc.GenerateFixtures(ctx, p, comp, revision()); err != nil {
		t.Fatal(err)
	}
	if err = svc.GenerateFixtures(ctx, p, comp, revision()); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("silent draft replacement: %v", err)
	}
	drafts, err := svc.ListFixtures(ctx, p, comp, false)
	if err != nil || len(drafts) != 3 {
		t.Fatalf("draft cardinality=%d err=%v", len(drafts), err)
	}
	if _, err = svc.PublishFixtures(ctx, p, comp, revision(), false, ""); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("unacknowledged coverage gap: %v", err)
	}
	constraints.Unavailable = []junior.UnavailableDate{{ClubID: drafts[0].HomeClubID, Date: drafts[0].MatchDate}}
	if err = svc.SaveFixtureConstraints(ctx, p, comp, revision(), constraints); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PublishFixtures(ctx, p, comp, revision(), true, "Checked synthetic bookings"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("unavailable club published: %v", err)
	}
	constraints.Unavailable = nil
	if err = svc.SaveFixtureConstraints(ctx, p, comp, revision(), constraints); err != nil {
		t.Fatal(err)
	}
	publication, err := svc.PublishFixtures(ctx, p, comp, revision(), true, "Checked all external/senior bookings and configured synthetic grounds")
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := svc.Dashboard(ctx, p, seed.SeasonID)
	if err != nil || dashboard.UnpublishedCompetitions != 0 {
		t.Fatalf("published schedule remained draft on Home: %+v %v", dashboard, err)
	}
	draftQueue := staffRequest(fmt.Sprintf("/admin/juniors/competitions?queue=draft&season_id=%d", seed.SeasonID))
	assertAdminWorkspaceHandlerHTML(t, draftQueue, []string{"0 matching competitions."}, []string{">Synthetic U13 once</a>"})
	officialPage := staffRequest(fmt.Sprintf("/admin/juniors/fixtures?competition_id=%d", comp))
	assertAdminWorkspaceHandlerHTML(t, officialPage, []string{"3 fixtures.", "Official junior fixtures"}, nil)
	captureAdminWorkspaceHandlerHTML(t, "junior-official-fixtures", officialPage.Body.String())
	if _, err = svc.PublishFixtures(ctx, p, comp, revision()-1, true, "Stale action"); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("stale publication accepted: %v", err)
	}
	var messages, recipients, attempts int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM junior_messages WHERE kind='fixture_publication'),(SELECT count(*) FROM junior_message_recipients),(SELECT count(*) FROM junior_message_attempts)`).Scan(&messages, &recipients, &attempts); err != nil || messages != 3 || recipients != 0 || attempts != 0 {
		t.Fatalf("publication recipient-gap evidence: messages=%d recipients=%d attempts=%d err=%v", messages, recipients, attempts, err)
	}
	messageItems, err := svc.Messages(ctx, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messageItems {
		if m.Kind == "fixture_publication" && m.Status != "needs_recipients" {
			t.Fatalf("missing recipients reported delivered: %+v", m)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_publication_fixtures SET ground_name='changed' WHERE publication_id=$1`, publication); err == nil {
		t.Fatal("official snapshot was mutable")
	}
	grant := seedJuniorAccess(t, pool, seed.Clubs[0], p.AdminID, "lead-a@example.invalid")
	own, err := svc.ListFixtures(ctx, grant.Principal, comp, false)
	if err != nil || len(own) != 2 {
		t.Fatalf("club published scope=%d err=%v", len(own), err)
	}
	for _, f := range own {
		if f.PublicationID != publication || (f.HomeClubID != seed.Clubs[0] && f.AwayClubID != seed.Clubs[0]) {
			t.Fatal("club scope leaked draft/opponents' fixtures")
		}
	}
	if err = svc.GenerateFixtures(ctx, grant.Principal, comp, revision()); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("lead generated fixtures: %v", err)
	}
	preview := junior.Principal{AdminID: p.AdminID, ClubID: seed.Clubs[0], ReadOnly: true}
	if err = svc.SaveDraftFixture(ctx, preview, comp, revision(), drafts[0]); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("preview wrote fixture: %v", err)
	}
	first := drafts[0]
	initial := []byte(fmt.Sprintf("fixture_id,outcome,evidence\n%d,unknown,No verified result\n%d,abandoned,Rain\n%d,played,Completed\n", drafts[0].ID, drafts[1].ID, drafts[2].ID))
	summary, err := svc.ImportFixtureResultsCSV(ctx, p, comp, "Synthetic reviewed CSV", initial)
	if err != nil || summary.Candidates != 0 {
		t.Fatalf("unknown/rain/played inferred concession: %+v %v", summary, err)
	}
	source := []byte(fmt.Sprintf("fixture_id,outcome,evidence\n%d,conceded_home,Explicit source says home conceded\n", first.ID))
	summary, err = svc.ImportFixtureResultsCSV(ctx, p, comp, "Synthetic reviewed CSV", source)
	if err != nil || summary.Candidates != 1 {
		t.Fatalf("indicator intake: %+v %v", summary, err)
	}
	replay, err := svc.ImportFixtureResultsCSV(ctx, p, comp, "Synthetic reviewed CSV", source)
	if err != nil || !replay.Replayed || replay.ImportID != summary.ImportID {
		t.Fatalf("replay duplicated source: %+v %v", replay, err)
	}
	candidates, err := svc.ListConcessions(ctx, p, junior.ConcessionFilter{CompetitionID: comp})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidate population: %+v %v", candidates, err)
	}
	candidate := candidates[0]
	if err = svc.ReviewConcession(ctx, p, candidate.ID, candidate.Revision, "confirm", candidate.HomeTeamID, 0, "Verified conceding home side against source"); err != nil {
		t.Fatal(err)
	}
	if err = svc.ReviewConcession(ctx, p, candidate.ID, candidate.Revision, "dismiss", 0, 0, "Stale request"); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("stale concession review accepted: %v", err)
	}
	candidates, err = svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "confirmed", ClubID: candidate.HomeClubID})
	if err != nil || len(candidates) != 1 || !candidates[0].Confirmed {
		t.Fatalf("confirmed attribution: %+v %v", candidates, err)
	}
	opposing, err := svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "confirmed", ClubID: candidate.AwayClubID})
	if err != nil || len(opposing) != 0 {
		t.Fatalf("opponent charged concession: %+v %v", opposing, err)
	}
	if err = svc.ReviewConcession(ctx, p, candidate.ID, candidates[0].Revision, "request_information", 0, candidate.HomeClubID, "Please clarify the later corrected source"); err != nil {
		t.Fatal(err)
	}
	corrected := []byte(fmt.Sprintf("fixture_id,outcome,evidence\n%d,played,Corrected source says match played\n", first.ID))
	if _, err = svc.ImportFixtureResultsCSV(ctx, p, comp, "Synthetic reviewed CSV", corrected); err != nil {
		t.Fatal(err)
	}
	candidates, err = svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "source_changed"})
	if err != nil || len(candidates) != 1 || !candidates[0].Confirmed {
		t.Fatalf("correction erased confirmed history: %+v %v", candidates, err)
	}
	dashboard, err = svc.Dashboard(ctx, p, seed.SeasonID)
	reviewItems, listErr := svc.ListConcessions(ctx, p, junior.ConcessionFilter{SeasonID: seed.SeasonID, State: "review"})
	if err != nil || listErr != nil || dashboard.ConcessionReview != int64(len(reviewItems)) || dashboard.ConfirmedConcessions != 1 {
		t.Fatalf("review/confirmed reconciliation dashboard=%+v list=%+v err=%v/%v", dashboard, reviewItems, err, listErr)
	}
	reviewPath := fmt.Sprintf("/admin/juniors/concessions?queue=review&season_id=%d", seed.SeasonID)
	reviewPage := staffRequest(reviewPath)
	assertAdminWorkspaceHandlerHTML(t, reviewPage, []string{"1 matching records", "Last confirmed conceding side", "Corrected source says match played"}, []string{">Confirm concession</button>"})
	export := staffRequest(reviewPath + "&export=csv")
	csvRows, csvErr := csv.NewReader(strings.NewReader(export.Body.String())).ReadAll()
	if export.Code != 200 || csvErr != nil || len(csvRows) != 2 {
		t.Fatalf("concession matching export: status=%d rows=%d err=%v", export.Code, len(csvRows), csvErr)
	}
	captureAdminWorkspaceHandlerHTML(t, "junior-concession-source-review", reviewPage.Body.String())
	candidate = candidates[0]
	if err = svc.ReviewConcession(ctx, p, candidate.ID, candidate.Revision, "confirm", candidate.HomeTeamID, 0, "Incorrect reconfirmation"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("played source reconfirmed: %v", err)
	}
	if err = svc.ReviewConcession(ctx, p, candidate.ID, candidate.Revision, "dismiss", 0, 0, "Correction verified: the match was played"); err != nil {
		t.Fatal(err)
	}
	confirmed, err := svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "confirmed"})
	if err != nil || len(confirmed) != 0 {
		t.Fatalf("dismissed correction remained in current total: %+v %v", confirmed, err)
	}
	history, err := svc.ConcessionHistory(ctx, p, candidate.ID)
	if err != nil || len(history) != 3 || history[2].Action != "confirm" {
		t.Fatalf("immutable decision history lost: %+v %v", history, err)
	}
	sameEvidenceNewFile := []byte(fmt.Sprintf("fixture_id,outcome,evidence\n%d,played,Corrected source says match played\n%d,unknown,Still no verified result\n", first.ID, drafts[1].ID))
	if _, err = svc.ImportFixtureResultsCSV(ctx, p, comp, "Synthetic reviewed CSV", sameEvidenceNewFile); err != nil {
		t.Fatal(err)
	}
	dismissed, err := svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "dismissed"})
	if err != nil || len(dismissed) != 1 {
		t.Fatalf("unchanged result reopened dismissal: %+v %v", dismissed, err)
	}
	before, err := svc.ListResultObservations(ctx, p, comp)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []byte(fmt.Sprintf("fixture_id,outcome,evidence\n%d,possible_concession,Reviewed indicator\n999999999,unknown,Not mapped\n", first.ID))
	if _, err = svc.ImportFixtureResultsCSV(ctx, p, comp, "Synthetic reviewed CSV", invalid); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("unmapped row accepted: %v", err)
	}
	after, err := svc.ListResultObservations(ctx, p, comp)
	if err != nil || len(after) != len(before) {
		t.Fatal("invalid multirow import partially committed")
	}
	// Draft correction remains private and cannot rewrite the match date attached to old result evidence.
	first.MatchDate = "2027-05-22"
	if err = svc.SaveDraftFixture(ctx, p, comp, revision(), first); err != nil {
		t.Fatal(err)
	}
	dashboard, err = svc.Dashboard(ctx, p, seed.SeasonID)
	if err != nil || dashboard.UnpublishedCompetitions != 1 {
		t.Fatalf("edited draft missing on Home: %+v %v", dashboard, err)
	}
	draftQueue = staffRequest(fmt.Sprintf("/admin/juniors/competitions?queue=draft&season_id=%d", seed.SeasonID))
	assertAdminWorkspaceHandlerHTML(t, draftQueue, []string{"1 matching competitions.", ">Synthetic U13 once</a>"}, nil)
	dismissed, err = svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "dismissed"})
	if err != nil || dismissed[0].MatchDate != drafts[0].MatchDate {
		t.Fatalf("draft edit rewrote official result context: %+v %v", dismissed, err)
	}
	if err = svc.RetireDraftFixture(ctx, p, comp, revision(), first.ID, "Remove evidenced fixture"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("evidenced fixture silently removed: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_result_observations SET evidence='changed' WHERE id=$1`, candidate.ObservationID); err == nil {
		t.Fatal("result observation was mutable")
	}
	correctionPublication, err := svc.PublishFixtures(ctx, p, comp, revision(), true, "Reviewed fixture date correction and external/senior booking checks")
	if err != nil || correctionPublication == publication {
		t.Fatalf("correction publication: %d %v", correctionPublication, err)
	}
	versions, err := svc.FixturePublications(ctx, p, comp)
	if err != nil || len(versions) != 2 || versions[0].ID != correctionPublication || !versions[0].Current || versions[1].Current {
		t.Fatalf("publication history: %+v %v", versions, err)
	}
	prior, err := svc.PublicationFixtures(ctx, p, comp, publication)
	if err != nil || len(prior) != 3 || prior[0].MatchDate != drafts[0].MatchDate {
		t.Fatalf("older publication changed: %+v %v", prior, err)
	}
	clubVersions, err := svc.FixturePublications(ctx, grant.Principal, comp)
	if err != nil || len(clubVersions) != 2 || clubVersions[0].FixtureCount != 2 || clubVersions[0].Reason != "" {
		t.Fatalf("club publication history scope/reason: %+v %v", clubVersions, err)
	}
	priorPage := staffRequest(fmt.Sprintf("/admin/juniors/fixtures?competition_id=%d&publication_id=%d", comp, publication))
	assertAdminWorkspaceHandlerHTML(t, priorPage, []string{"Showing immutable publication", "3 fixtures."}, nil)
	dismissed, err = svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "dismissed"})
	if err != nil || dismissed[0].MatchDate != drafts[0].MatchDate {
		t.Fatalf("published correction rewrote old observation context: %+v %v", dismissed, err)
	}
	// A second competition cannot publish over a team already booked by the first.
	second, err := svc.CreateCompetition(ctx, p, junior.Competition{SeasonID: seed.SeasonID, Name: "Synthetic U13 custom clash", AgeGroup: "U13", Format: "custom", StartDate: "2027-05-01", EndDate: "2027-05-31", DefaultStartTime: "18:00"})
	if err != nil {
		t.Fatal(err)
	}
	secondRevision := func() int64 { return juniorCompetitionRevision(t, ctx, svc, p, second) }
	for _, team := range []int64{drafts[0].HomeTeamID, drafts[0].AwayTeamID} {
		if err = svc.SaveCompetitionEntry(ctx, p, second, secondRevision(), team, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.SaveFixtureConstraints(ctx, p, second, secondRevision(), constraints); err != nil {
		t.Fatal(err)
	}
	clash := first
	clash.ID = 0
	if err = svc.SaveDraftFixture(ctx, p, second, secondRevision(), clash); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PublishFixtures(ctx, p, second, secondRevision(), true, "Checked senior bookings"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("cross-competition double booking accepted: %v", err)
	}
	// Successful staff review returns to the same season and queue after removing a selected-record filter.
	postPath := fmt.Sprintf("/admin/juniors/concessions?queue=review&season_id=%d&candidate_id=%d", seed.SeasonID, candidate.ID)
	current, err := svc.ListConcessions(ctx, p, junior.ConcessionFilter{State: "dismissed"})
	if err != nil {
		t.Fatal(err)
	}
	post := adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", postPath, url.Values{"csrf_token": {"synthetic-csrf-token"}, "candidate_id": {fmt.Sprint(candidate.ID)}, "revision": {fmt.Sprint(current[0].Revision)}, "action": {"request_information"}, "information_club_id": {fmt.Sprint(candidate.HomeClubID)}, "reason": {"Clarify this synthetic corrected source"}})
	redirect, parseErr := url.Parse(post.Header().Get("Location"))
	if post.Code != 303 || parseErr != nil || redirect.Query().Get("season_id") != fmt.Sprint(seed.SeasonID) || redirect.Query().Get("queue") != "review" || redirect.Query().Has("candidate_id") {
		t.Fatalf("review return context lost: status=%d location=%s body=%s", post.Code, post.Header().Get("Location"), post.Body.String())
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_message_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("test started delivery: attempts=%d err=%v", attempts, err)
	}
	filteredFixtures := staffRequest(fmt.Sprintf("/admin/juniors/fixtures?publication_id=%d&season_id=%d&division=North&age_group=U13&date_from=2027-05-01&date_to=2027-05-01&export=csv", publication, seed.SeasonID))
	fixtureRows, err := csv.NewReader(strings.NewReader(filteredFixtures.Body.String())).ReadAll()
	if filteredFixtures.Code != 200 || err != nil || len(fixtureRows) != 2 || fixtureRows[1][13] != "North" {
		t.Fatalf("division/date fixture export: rows=%v status=%d err=%v", fixtureRows, filteredFixtures.Code, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_seasons SET state='closed' WHERE id=$1`, seed.SeasonID); err != nil {
		t.Fatal(err)
	}
	closedRevision := revision()
	for name, write := range map[string]func() error{"entry": func() error { return svc.SaveCompetitionEntry(ctx, p, comp, closedRevision, seed.Teams[0], true) }, "constraints": func() error { return svc.SaveFixtureConstraints(ctx, p, comp, closedRevision, constraints) }, "generate": func() error { return svc.GenerateFixtures(ctx, p, comp, closedRevision, true) }, "draft": func() error { return svc.SaveDraftFixture(ctx, p, comp, closedRevision, first) }, "publish": func() error {
		_, err := svc.PublishFixtures(ctx, p, comp, closedRevision, true, "Closed correction")
		return err
	}, "results": func() error {
		_, err := svc.ImportFixtureResultsCSV(ctx, p, comp, "Closed synthetic source", source)
		return err
	}, "review": func() error {
		return svc.ReviewConcession(ctx, p, candidate.ID, 1, "dismiss", 0, 0, "Closed season review")
	}} {
		if err = write(); !errors.Is(err, junior.ErrValidation) || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("closed season %s accepted or wrong gate: %v", name, err)
		}
	}
	closedDetail := staffRequest(fmt.Sprintf("/admin/juniors/competitions/%d", comp))
	assertAdminWorkspaceHandlerHTML(t, closedDetail, []string{"junior season is closed", "Retained draft schedule"}, []string{"Generate draft", "Publish official fixtures", "Add manual fixture"})
	closedResults := staffRequest(fmt.Sprintf("/admin/juniors/results?competition_id=%d", comp))
	assertAdminWorkspaceHandlerHTML(t, closedResults, []string{"junior season is closed", "Immutable observation history"}, []string{"Validate and import results"})
	closedConcessions := staffRequest(fmt.Sprintf("/admin/juniors/concessions?division=North&season_id=%d", seed.SeasonID))
	assertAdminWorkspaceHandlerHTML(t, closedConcessions, []string{"review history is read-only", "1 matching records"}, []string{"Request information</button>"})
}
