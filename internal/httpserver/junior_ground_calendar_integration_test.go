package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJuniorU9CalendarAndExternalBookingsPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-calendar-test-secret")
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	p := seed.Admin
	svc := junior.NewService(pool)
	gid, err := svc.SaveGround(ctx, p, junior.Ground{Name: "U9 Festival Ground", Address: "1 Fictional Road", Postcode: "ZZ1 1AA", Source: "Synthetic confirmed layouts", RulesConfirmed: true, Layouts: []junior.GroundLayout{{"U9": 3}, {"U11": 2}}})
	if err != nil {
		t.Fatal(err)
	}
	comp, err := svc.CreateCompetition(ctx, p, junior.Competition{SeasonID: seed.SeasonID, Name: "Rochdale U9", AgeGroup: "U9", Division: "Rochdale", Format: "custom", StartDate: "2027-05-01", EndDate: "2027-07-31", DefaultStartTime: "09:00"})
	if err != nil {
		t.Fatal(err)
	}
	teams := append([]int64{}, seed.Teams...)
	for i := 0; i < 3; i++ {
		var id int64
		if err = pool.QueryRow(ctx, `INSERT INTO junior_teams(club_id,name) VALUES($1,$2) RETURNING id`, seed.Clubs[i], fmt.Sprintf("U9 B %d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		teams = append(teams, id)
	}
	rev := func() int64 { return juniorCompetitionRevision(t, ctx, svc, p, comp) }
	for _, id := range teams {
		if err = svc.SaveCompetitionEntry(ctx, p, comp, rev(), id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.SaveFixtureConstraints(ctx, p, comp, rev(), junior.FixtureConstraints{PlayingDates: []string{"2027-05-02"}, DurationMinutes: 60, Grounds: []junior.GroundConstraint{{ClubID: seed.Clubs[0], GroundID: gid}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = svc.SaveDraftFixture(ctx, p, comp, rev(), junior.Fixture{HomeTeamID: teams[i], AwayTeamID: teams[i+3], MatchDate: "2027-05-02", StartTime: "09:00", GroundID: gid}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = svc.PublishFixtures(ctx, p, comp, rev(), true, "Synthetic external checks"); err != nil {
		t.Fatalf("three U9 fixtures must publish: %v", err)
	}
	oldRev := rev()
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	path := "/admin/juniors/ground-calendar"
	form := url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"external_booking"}, "ground_id": {fmt.Sprint(gid)}, "title": {"Women’s league fixture"}, "category": {"women"}, "match_date": {"2027-05-02"}, "start_time": {"09:30"}, "duration_minutes": {"120"}, "source": {"Synthetic booking confirmation"}}
	if w := adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", path, form); w.Code != 303 {
		t.Fatalf("save booking %d %s", w.Code, w.Body.String())
	}
	if _, err = svc.PublishFixtures(ctx, p, comp, oldRev, true, "stale check"); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("external booking did not invalidate review: %v", err)
	}
	if _, err = svc.PublishFixtures(ctx, p, comp, rev(), true, "cannot publish through women’s booking"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("publication ignored external booking: %v", err)
	}
	page := adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", path+fmt.Sprintf("?date=2027-05-02&ground_id=%d&competition_id=%d", gid, comp), nil)
	assertAdminWorkspaceHandlerHTML(t, page, []string{"3 × U9", "GROUND CLASH", "role=\"alert\"", "Women’s league fixture", "All bookings at the selected ground", "All fixtures in the selected division", "published"}, nil)
	writeJuniorPlanningSnapshot(t, "calendar", page)
	groundsPage := adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", "/admin/juniors/grounds", nil)
	writeJuniorPlanningSnapshot(t, "grounds", groundsPage)
	competitionPage := adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", fmt.Sprintf("/admin/juniors/competitions/%d", comp), nil)
	writeJuniorPlanningSnapshot(t, "competition", competitionPage)
	if w := adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", path+"?date=not-a-date", nil); w.Code != 400 {
		t.Fatalf("invalid date: %d", w.Code)
	}
	bad := url.Values{"action": {"external_booking"}}
	if w := adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", path, bad); w.Code != 403 {
		t.Fatalf("missing CSRF accepted: %d", w.Code)
	}
	var booking int64
	if err = pool.QueryRow(ctx, `SELECT id FROM junior_external_ground_bookings`).Scan(&booking); err != nil {
		t.Fatal(err)
	}
	if err = svc.CancelExternalGroundBooking(ctx, junior.Principal{ClubID: seed.Clubs[0], ReadOnly: true}, booking); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("club cancelled external booking: %v", err)
	}
	if _, _, err = svc.GroundBookings(ctx, junior.Principal{ClubID: seed.Clubs[0], ReadOnly: true}, 0); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("club exposed all ground bookings: %v", err)
	}
	form.Set("action", "cancel_booking")
	form.Set("booking_id", fmt.Sprint(booking))
	if w := adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", path, form); w.Code != 303 {
		t.Fatalf("cancel booking: %d %s", w.Code, w.Body.String())
	}
	if _, err = svc.PublishFixtures(ctx, p, comp, rev(), true, "Booking cancelled; confirmed availability"); err != nil {
		t.Fatalf("cancelled booking still blocks: %v", err)
	}
	// A draft relocation continues to reserve the official slot until republished.
	fixtures, err := svc.ListFixtures(ctx, p, comp, false)
	if err != nil {
		t.Fatal(err)
	}
	moved := fixtures[0]
	moved.StartTime = "11:00"
	if err = svc.SaveDraftFixture(ctx, p, comp, rev(), moved); err != nil {
		t.Fatal(err)
	}
	all, issues, err := svc.GroundBookings(ctx, p, seed.SeasonID)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		if issue.Code == "ground_clash" {
			t.Fatalf("draft/publication duplicate inflated capacity: %+v", issues)
		}
	}
	count := 0
	for _, f := range all {
		if f.ID == moved.ID {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("old/new reservations not retained: %d", count)
	}
}

// Optional synthetic handler snapshots for visual QA, never live club data.
func writeJuniorPlanningSnapshot(t *testing.T, name string, page *httptest.ResponseRecorder) {
	t.Helper()
	dir := os.Getenv("GMCL_JUNIOR_SNAPSHOTS_DIR")
	if dir == "" {
		return
	}
	if page.Code != 200 {
		t.Fatalf("snapshot %s status %d", name, page.Code)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(page.Body.String(), bootstrapCSS, "/bootstrap.css")
	if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
