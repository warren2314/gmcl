package httpserver

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

func juniorPortalTestRequest(t *testing.T, router http.Handler, grant junior.SessionGrant, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://junior.example.invalid"+path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: juniorSessionCookie, Value: grant.Token})
	r.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "synthetic-csrf-token"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func TestJuniorFormsFirstRunHasActionableSeasonSetupPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-first-run-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	admin, club, _ := seedJuniorAccessAdminClub(t, ctx, pool)
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	for _, path := range []string{"/admin/juniors/forms", "/admin/juniors/forms/new", "/admin/juniors/forms?status=outstanding", "/admin/juniors/forms?status=overdue", "/admin/juniors/forms?status=review"} {
		page := adminWorkspaceHandlerRequest(t, router, admin, "GET", path, nil)
		assertAdminWorkspaceHandlerHTML(t, page, []string{"Configure the first junior season", `href="/admin/juniors/clubs"`}, []string{"season_id=0", "Create official form"})
	}
	for _, path := range []string{"/admin/juniors/fixtures", "/admin/juniors/concessions", fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=forms", club), fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=fixtures", club), fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=concessions", club)} {
		page := adminWorkspaceHandlerRequest(t, router, admin, "GET", path, nil)
		assertAdminWorkspaceHandlerHTML(t, page, nil, []string{"season_id=0"})
	}
}
func TestJuniorFormHTTPWorkflowPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("JUNIOR_PORTAL_ENABLED", "true")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-form-test-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	svc := junior.NewService(pool)
	grant := seedJuniorAccess(t, pool, seed.Clubs[0], seed.Admin.AdminID, "form-lead-a@example.invalid")
	server := &Server{DB: pool, LondonLoc: juniorLondon()}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	staffRequest := func(method, path string, values url.Values) *httptest.ResponseRecorder {
		return adminWorkspaceHandlerRequest(t, router, seed.Admin.AdminID, method, path, values)
	}
	builder := staffRequest("GET", fmt.Sprintf("/admin/juniors/forms/new?season_id=%d", seed.SeasonID), nil)
	assertAdminWorkspaceHandlerHTML(t, builder, []string{"Publish official form", "Question 10", "Target clubs", "Answer type"}, []string{"JSON"})
	values := url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"publish"}, "season_id": {strconv.FormatInt(seed.SeasonID, 10)}, "title": {"Synthetic official form"}, "description": {"Synthetic instructions"}, "due_date": {"2027-05-01"}, "requires_review": {"1"}, "field_label_1": {"Official answer"}, "field_type_1": {"text"}, "field_required_1": {"1"}, "field_label_2": {"Contact email"}, "field_type_2": {"email"}}
	values.Set("due_date", time.Now().AddDate(0, 0, -1).In(juniorLondon()).Format("2006-01-02"))
	for _, club := range seed.Clubs {
		values.Add("club_id", strconv.FormatInt(int64(club), 10))
	}
	published := staffRequest("POST", "/admin/juniors/forms/new", values)
	if published.Code != 303 {
		t.Fatalf("publish handler: %d %s", published.Code, published.Body.String())
	}
	location, err := url.Parse(published.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	formID, err := strconv.ParseInt(strings.TrimPrefix(location.Path, "/admin/juniors/forms/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := svc.FormAssignments(ctx, seed.Admin, formID)
	if err != nil || len(assignments) != 3 {
		t.Fatalf("assignments: %+v %v", assignments, err)
	}
	assertQueues := func() {
		t.Helper()
		dashboard, err := svc.Dashboard(ctx, seed.Admin, seed.SeasonID)
		if err != nil {
			t.Fatal(err)
		}
		for status, want := range map[string]int64{"outstanding": dashboard.OutstandingForms, "overdue": dashboard.OverdueForms, "review": dashboard.ReviewForms} {
			path := fmt.Sprintf("/admin/juniors/forms?season_id=%d&status=%s", seed.SeasonID, status)
			page := staffRequest("GET", path, nil)
			assertAdminWorkspaceHandlerHTML(t, page, []string{fmt.Sprintf("%d matching club assignments.", want)}, nil)
			export := staffRequest("GET", path+"&export=csv", nil)
			rows, err := csv.NewReader(strings.NewReader(export.Body.String())).ReadAll()
			if export.Code != 200 || err != nil || int64(len(rows)) != 1+want*2 {
				t.Fatalf("%s card/list/export mismatch count=%d rows=%d status=%d err=%v", status, want, len(rows), export.Code, err)
			}
		}
	}
	assertQueues()
	var own, other junior.FormAssignment
	for _, a := range assignments {
		if a.ClubID == seed.Clubs[0] {
			own = a
		} else {
			other = a
		}
	}
	path := fmt.Sprintf("/junior/forms/%d", own.ID)
	cross := juniorPortalTestRequest(t, router, grant, "GET", fmt.Sprintf("/junior/forms/%d", other.ID), nil)
	if cross.Code != 403 {
		t.Fatalf("cross-club response leak: %d", cross.Code)
	}
	noCSRF := juniorPortalTestRequest(t, router, grant, "POST", path, url.Values{"action": {"save"}, "draft_revision": {"0"}})
	if noCSRF.Code != 403 {
		t.Fatalf("club CSRF bypass: %d", noCSRF.Code)
	}
	draft := url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"save"}, "draft_revision": {"0"}, "answer_question_1": {"Gathering answer"}, "answer_question_2": {""}}
	saved := juniorPortalTestRequest(t, router, grant, "POST", path, draft)
	if saved.Code != 303 {
		t.Fatalf("draft handler: %d %s", saved.Code, saved.Body.String())
	}
	stale := juniorPortalTestRequest(t, router, grant, "POST", path, draft)
	if stale.Code != 409 {
		t.Fatalf("stale draft overwrite: %d %s", stale.Code, stale.Body.String())
	}
	missing := url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"submit"}, "draft_revision": {"1"}, "answer_question_1": {""}, "answer_question_2": {""}}
	if w := juniorPortalTestRequest(t, router, grant, "POST", path, missing); w.Code != 400 {
		t.Fatalf("required answer omitted: %d %s", w.Code, w.Body.String())
	}
	draft.Set("action", "submit")
	draft.Set("draft_revision", "1")
	draft.Set("answer_question_1", "Original submitted answer")
	draft.Set("answer_question_2", "form-lead-a@example.invalid")
	submitted := juniorPortalTestRequest(t, router, grant, "POST", path, draft)
	if submitted.Code != 303 {
		t.Fatalf("submit: %d %s", submitted.Code, submitted.Body.String())
	}
	own, err = svc.FormAssignment(ctx, grant.Principal, own.ID)
	if err != nil || own.Status != "submitted" || own.SubmissionID == 0 {
		t.Fatalf("receipt: %+v %v", own, err)
	}
	firstReceipt := own.SubmissionID
	assertQueues()
	repeated := juniorPortalTestRequest(t, router, grant, "POST", path, draft)
	if repeated.Code != 303 {
		t.Fatalf("repeated submit: %d %s", repeated.Code, repeated.Body.String())
	}
	history, err := svc.FormSubmissionHistory(ctx, grant.Principal, own.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("repeated submission duplicated receipt: %+v %v", history, err)
	}
	receiptPage := juniorPortalTestRequest(t, router, grant, "GET", path, nil)
	assertAdminWorkspaceHandlerHTML(t, receiptPage, []string{"Submitted response history", "Original submitted answer", fmt.Sprintf("Receipt %d", firstReceipt)}, []string{"Submit official response", "Save draft"})
	reviewPath := fmt.Sprintf("/admin/juniors/forms/assignments/%d", own.ID)
	returned := staffRequest("POST", reviewPath, url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"return"}, "submission_id": {strconv.FormatInt(firstReceipt, 10)}, "reason": {"Please clarify this synthetic answer"}})
	if returned.Code != 303 {
		t.Fatalf("return review: %d %s", returned.Code, returned.Body.String())
	}
	draft.Set("draft_revision", "2")
	draft.Set("answer_question_1", "Revised submitted answer")
	revised := juniorPortalTestRequest(t, router, grant, "POST", path, draft)
	if revised.Code != 303 {
		t.Fatalf("resubmit: %d %s", revised.Code, revised.Body.String())
	}
	own, err = svc.FormAssignment(ctx, grant.Principal, own.ID)
	if err != nil {
		t.Fatal(err)
	}
	badReview := staffRequest("POST", reviewPath, url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"complete"}, "submission_id": {strconv.FormatInt(firstReceipt, 10)}})
	if badReview.Code != 409 {
		t.Fatalf("stale review completed wrong submission: %d", badReview.Code)
	}
	complete := staffRequest("POST", reviewPath, url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"complete"}, "submission_id": {strconv.FormatInt(own.SubmissionID, 10)}, "reason": {"Verified revised answer"}})
	if complete.Code != 303 {
		t.Fatalf("complete: %d %s", complete.Code, complete.Body.String())
	}
	historyPage := juniorPortalTestRequest(t, router, grant, "GET", path, nil)
	assertAdminWorkspaceHandlerHTML(t, historyPage, []string{"Completed", "Original submitted answer", "Revised submitted answer", "Verified revised answer"}, []string{"Submit official response", "Save draft"})
	filtered := staffRequest("GET", fmt.Sprintf("/admin/juniors/forms/%d?status=completed&q=Synthetic+Junior+Club+A&export=csv", formID), nil)
	if filtered.Code != 200 {
		t.Fatalf("response export: %d %s", filtered.Code, filtered.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(filtered.Body.String())).ReadAll()
	if err != nil || len(rows) != 3 || rows[1][0] != strconv.FormatInt(own.ID, 10) || rows[1][9] != "Revised submitted answer" {
		t.Fatalf("matching export rows=%v err=%v", rows, err)
	}
	reminder := staffRequest("POST", fmt.Sprintf("/admin/juniors/forms/%d", formID), url.Values{"csrf_token": {"synthetic-csrf-token"}, "action": {"remind"}})
	if reminder.Code != 303 {
		t.Fatalf("remind: %d %s", reminder.Code, reminder.Body.String())
	}
	var completedReminders, attempts int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM junior_messages WHERE kind='form_reminder' AND record_id=$1),(SELECT count(*) FROM junior_message_attempts)`, own.ID).Scan(&completedReminders, &attempts); err != nil || completedReminders != 0 || attempts != 0 {
		t.Fatalf("completed club reminded or real transport attempted: reminders=%d attempts=%d err=%v", completedReminders, attempts, err)
	}
	forms, err := svc.ListForms(ctx, seed.Admin, seed.SeasonID)
	if err != nil || len(forms) != 1 {
		t.Fatalf("form summary: %+v %v", forms, err)
	}
	values.Set("current_version_id", strconv.FormatInt(forms[0].VersionID, 10))
	values.Set("title", "Synthetic new version")
	versioned := staffRequest("POST", fmt.Sprintf("/admin/juniors/forms/%d", formID), values)
	if versioned.Code != 303 {
		t.Fatalf("new version: %d %s", versioned.Code, versioned.Body.String())
	}
	ownAfter, err := svc.FormAssignment(ctx, grant.Principal, own.ID)
	if err != nil || ownAfter.Status != "completed" || ownAfter.Title != "Synthetic official form" {
		t.Fatalf("new version rewrote completed response: %+v %v", ownAfter, err)
	}
	otherAfter, err := svc.FormAssignment(ctx, seed.Admin, other.ID)
	if err != nil || otherAfter.Status != "superseded" {
		t.Fatalf("outstanding previous version not superseded: %+v %v", otherAfter, err)
	}
	clubList := juniorPortalTestRequest(t, router, grant, "GET", "/junior/forms", nil)
	assertAdminWorkspaceHandlerHTML(t, clubList, []string{"Synthetic official form", "Synthetic new version", "Completed"}, []string{"Synthetic Junior Club B"})
	all, err := svc.FormAssignments(ctx, seed.Admin, formID)
	if err != nil {
		t.Fatal(err)
	}
	assertQueues()
	var newest junior.FormAssignment
	for _, a := range all {
		if a.ClubID == seed.Clubs[0] && a.VersionID != own.VersionID {
			newest = a
		}
	}
	preview := staffRequest("GET", fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=forms&assignment_id=%d", seed.Clubs[0], newest.ID), nil)
	assertAdminWorkspaceHandlerHTML(t, preview, []string{"read-only staff preview", "Current draft — read-only"}, []string{"Submit official response", "Save draft", "Mark completed"})
	if _, err = svc.SaveFormResponse(ctx, junior.Principal{AdminID: seed.Admin.AdminID, ClubID: seed.Clubs[0], ReadOnly: true}, newest.ID, 0, map[string]string{}, false); !errors.Is(err, junior.ErrForbidden) {
		t.Fatalf("staff preview submitted club response: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE junior_form_submissions SET values='{}' WHERE id=$1`, firstReceipt); err == nil {
		t.Fatal("submitted form response mutable")
	}
	captureAdminWorkspaceHandlerHTML(t, "junior-form-builder", builder.Body.String())
	captureAdminWorkspaceHandlerHTML(t, "junior-form-club-receipt", historyPage.Body.String())
	captureAdminWorkspaceHandlerHTML(t, "junior-form-club-list", clubList.Body.String())
}
