package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestJuniorTeamEntryApprovalPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-team-entry-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	svc := junior.NewService(pool)
	p := seed.Admin
	grant := seedJuniorAccess(t, pool, seed.Clubs[0], p.AdminID, "entries@example.invalid")
	if _, err := pool.Exec(ctx, `UPDATE junior_teams SET name='U13 A' WHERE id=$1`, seed.Teams[0]); err != nil {
		t.Fatal(err)
	}
	comps := []int64{}
	for _, age := range []string{"U13", "U15"} {
		id, err := svc.CreateCompetition(ctx, p, junior.Competition{SeasonID: seed.SeasonID, Name: age + " League", AgeGroup: age, Division: "North", Format: "once", StartDate: "2027-05-01", EndDate: "2027-06-01", DefaultStartTime: "18:00"})
		if err != nil {
			t.Fatal(err)
		}
		comps = append(comps, id)
	}
	publish := func(title string) junior.FormAssignment {
		id, err := svc.PublishForm(ctx, p, junior.FormInput{SeasonID: seed.SeasonID, Title: title, DueDate: time.Now().AddDate(0, 0, 7), ClubIDs: seed.Clubs[:1], Fields: []junior.FormField{{Key: "entries", Label: "Your teams", Type: "team_entries", Required: true}}})
		if err != nil {
			t.Fatal(err)
		}
		as, err := svc.FormAssignments(ctx, p, id)
		if err != nil || len(as) != 1 {
			t.Fatalf("assignments: %v %v", as, err)
		}
		return as[0]
	}
	a := publish("Team entries")
	values := map[string]string{"entries": `[{"name":" U13   A ","age_group":"U13"},{"name":"B","age_group":"U15"}]`}
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	clubPath := fmt.Sprintf("/junior/forms/%d", a.ID)
	clubPage := juniorPortalTestRequest(t, router, grant, "GET", clubPath, nil)
	assertAdminWorkspaceHandlerHTML(t, clubPage, []string{"Team 1 name", "Team 1 age group", "Submit official response"}, nil)
	clubPost := juniorPortalTestRequest(t, router, grant, "POST", clubPath, url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"submit"}, "draft_revision": {"0"}, "team_entries_0_name": {" U13   A "}, "team_entries_0_age": {"U13"}, "team_entries_1_name": {"B"}, "team_entries_1_age": {"U15"}})
	if clubPost.Code != 303 {
		t.Fatalf("club entry submission: %d %s", clubPost.Code, clubPost.Body.String())
	}
	current, err := svc.FormAssignment(ctx, p, a.ID)
	if err != nil || current.Status != "submitted" {
		t.Fatalf("review must be mandatory: %v %v", current, err)
	}
	submission := current.SubmissionID
	if err = svc.ReviewForm(ctx, p, a.ID, submission, "complete", ""); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("generic review bypass: %v", err)
	}
	path := fmt.Sprintf("/admin/juniors/forms/assignments/%d/team-entry", a.ID)
	page := adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", path, nil)
	assertAdminWorkspaceHandlerHTML(t, page, []string{"Preview team allocation", "U13 A", "U15 B"}, []string{"Approve teams and allocate leagues"})
	if w := adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", path, url.Values{"action": {"preview"}}); w.Code != 403 {
		t.Fatalf("approval CSRF: %d", w.Code)
	}
	plan, err := svc.PreviewTeamEntry(ctx, p, a.ID, submission, comps)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].TeamID != seed.Teams[0] || plan.Items[1].TeamID != 0 {
		t.Fatalf("reuse/create plan: %#v", plan.Items)
	}
	post := url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"preview"}, "submission_id": {fmt.Sprint(submission)}, "competition_0": {fmt.Sprint(comps[0])}, "competition_1": {fmt.Sprint(comps[1])}}
	previewPage := adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", path, post)
	assertAdminWorkspaceHandlerHTML(t, previewPage, []string{"Approval preview", "Reuse existing team and allocate", "Create team and allocate", "Approve teams and allocate leagues"}, nil)
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_teams WHERE club_id=$1`, seed.Clubs[0]).Scan(&n); err != nil || n != 1 {
		t.Fatalf("preview mutated teams: %d %v", n, err)
	}
	if err = svc.ApproveTeamEntry(ctx, grant.Principal, a.ID, submission, comps, plan.Hash); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("club approved: %v", err)
	}
	if err = svc.ApproveTeamEntry(ctx, p, a.ID, submission, []int64{comps[1], comps[0]}, plan.Hash); err == nil {
		t.Fatal("wrong age allocation accepted")
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id=$1`, comps[0]); err != nil {
		t.Fatal(err)
	}
	if err = svc.ApproveTeamEntry(ctx, p, a.ID, submission, comps, plan.Hash); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("stale preview accepted: %v", err)
	}
	plan, err = svc.PreviewTeamEntry(ctx, p, a.ID, submission, comps)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.ApproveTeamEntry(ctx, p, a.ID, submission, comps, plan.Hash) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent approval: %v", e)
		}
	}
	post.Set("action", "approve")
	post.Set("preview_hash", plan.Hash)
	if w := adminWorkspaceHandlerRequest(t, router, p.AdminID, "POST", path, post); w.Code != 303 {
		t.Fatalf("HTTP approval replay: %d %s", w.Code, w.Body.String())
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_teams WHERE club_id=$1`, seed.Clubs[0]).Scan(&n); err != nil || n != 2 {
		t.Fatalf("duplicate teams: %d %v", n, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_team_entry_approvals WHERE submission_id=$1`, submission).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate approval receipt: %d %v", n, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_competition_entries`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("duplicate allocations: %d %v", n, err)
	}
	if _, err = svc.SaveTeam(ctx, p, junior.TeamInput{ClubID: seed.Clubs[0], Name: "  u15   b  "}); !errors.Is(err, junior.ErrConflict) {
		t.Fatalf("manual spacing duplicate accepted: %v", err)
	}
	page = adminWorkspaceHandlerRequest(t, router, p.AdminID, "GET", juniorFormLink(p, a.ID), nil)
	assertAdminWorkspaceHandlerHTML(t, page, []string{"Completed", "Approved team allocation receipt", "U15 B"}, []string{"Mark completed"})
	// A second form/version with the same teams cannot recreate team or entry records.
	b := publish("Reconfirmed team entries")
	sub2, err := svc.SaveFormResponse(ctx, grant.Principal, b.ID, 0, values, true)
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := svc.PreviewTeamEntry(ctx, p, b.ID, sub2, comps)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.ApproveTeamEntry(ctx, p, b.ID, sub2, comps, plan2.Hash); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_teams WHERE club_id=$1`, seed.Clubs[0]).Scan(&n); err != nil || n != 2 {
		t.Fatalf("resubmission duplicated teams: %d %v", n, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_competition_entries`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("resubmission duplicated entries: %d %v", n, err)
	}
	c := publish("Pending participation check")
	sub3, err := svc.SaveFormResponse(ctx, grant.Principal, c.ID, 0, values, true)
	if err != nil {
		t.Fatal(err)
	}
	plan3, err := svc.PreviewTeamEntry(ctx, p, c.ID, sub3, comps)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.SetClubInactive(ctx, p, seed.Clubs[0], true, "Inactive after preview for regression"); err != nil {
		t.Fatal(err)
	}
	if err = svc.ApproveTeamEntry(ctx, p, c.ID, sub3, comps, plan3.Hash); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("inactive participation approved: %v", err)
	}
	if err = svc.SetClubInactive(ctx, p, seed.Clubs[0], false, "Reactivated for closed season regression"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_seasons SET state='closed' WHERE id=$1`, seed.SeasonID); err != nil {
		t.Fatal(err)
	}
	if err = svc.ApproveTeamEntry(ctx, p, c.ID, sub3, comps, plan3.Hash); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("closed season approved: %v", err)
	}
}
