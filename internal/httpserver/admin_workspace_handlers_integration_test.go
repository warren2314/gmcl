package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

// These are actual authenticated GET handlers, using an isolated schema built
// from the repository migrations. No NewServer, background worker, sender or
// successful mutation request is invoked. Invented records model workflow
// checkpoints; they do not claim to exercise the command transitions between
// those checkpoints or the deployed application's proxy/session configuration.
func TestAdminWorkspaceAuthenticatedCaseHandlersPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-handler-test-secret-never-used-outside-tests")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("SANCTIONS_EMAIL_DISABLED", "true")
	seedAdminWorkspaceHandlerCases(t, ctx, pool)
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: pool, LondonLoc: london}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())

	t.Run("existing authentication and route permissions", func(t *testing.T) {
		for _, tt := range []struct {
			name     string
			admin    int32
			want     int
			location string
		}{
			{"unauthenticated", 0, http.StatusSeeOther, "/admin/login"},
			{"no sanctions permission", 90012, http.StatusForbidden, ""},
			{"triage alone", 90011, http.StatusForbidden, ""},
			{"proposal alone", 90014, http.StatusForbidden, ""},
			{"forced password change", 90013, http.StatusSeeOther, "/admin/change-password"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				response := adminWorkspaceHandlerRequest(t, router, tt.admin, http.MethodGet, "/admin/cases/910001", nil)
				if response.Code != tt.want || response.Header().Get("Location") != tt.location {
					t.Fatalf("status=%d location=%q, want %d %q", response.Code, response.Header().Get("Location"), tt.want, tt.location)
				}
				if strings.Contains(response.Body.String(), "Synthetic private reply case") {
					t.Fatal("denied request disclosed case information")
				}
			})
		}
		// Both failures occur before the command handler: nothing is queued or
		// changed, even though the authenticated GET contains real action forms.
		withoutCSRF := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodPost, "/admin/cases/910001/response-reviewed", nil)
		if withoutCSRF.Code != http.StatusForbidden || !strings.Contains(withoutCSRF.Body.String(), "csrf validation failed") {
			t.Fatalf("missing CSRF request: %d %s", withoutCSRF.Code, withoutCSRF.Body.String())
		}
		withoutGrant := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodPost, "/admin/cases/910004/publish", url.Values{"csrf_token": {"synthetic-csrf-token"}})
		if withoutGrant.Code != http.StatusForbidden {
			t.Fatalf("owner without publish grant reached issue command: %d", withoutGrant.Code)
		}
	})

	t.Run("approval-only viewers retain context without forbidden controls", func(t *testing.T) {
		for _, tt := range []struct {
			id     int64
			want   []string
			absent []string
		}{
			{910001, []string{"Back to Home", "Awaiting reply review", "Synthetic evidence.txt", "Private source evidence"}, []string{`href="/admin/cases/910001/evidence/960001"`, `action="/admin/cases/910001/response-reviewed"`, `action="/admin/cases/910001/scorecard-evidence"`, `action="/admin/cases/910001/assign"`, `action="/admin/cases/910001/tasks"`, "share-with-offending-club"}},
			{910005, []string{"Original report details", "Synthetic corrected source allegation", "Review changed source evidence"}, []string{`href="/admin/ineligible/990001"`}},
			{910006, []string{"Case outcome recorded", "Synthetic follow-up evidence check", "Synthetic delivery failure"}, []string{`action="/admin/cases/910006/notices/980001/resend"`, `href="/admin/cases/tasks"`}},
			{910009, []string{"Awaiting case owner action", "No alleged rule recorded", "Case owner"}, []string{`action="/admin/cases/910009/alleged-rule"`, `action="/admin/cases/910009/hawk-rule-suggestion"`, `href="#record-alleged-rule"`, `action="/admin/cases/910009/scorecard-evidence"`}},
		} {
			response := adminWorkspaceHandlerRequest(t, router, 90015, http.MethodGet, fmt.Sprintf("/admin/cases/%d", tt.id), nil)
			assertAdminWorkspaceHandlerHTML(t, response, tt.want, tt.absent)
		}
		// The established outcome-preview route accepts approval access.
		approved := adminWorkspaceHandlerRequest(t, router, 90015, http.MethodGet, "/admin/cases/910004", nil)
		assertAdminWorkspaceHandlerHTML(t, approved, []string{"View offending-club PDF", "Synthetic locked outcome wording"}, nil)
		captureAdminWorkspaceHandlerHTML(t, "case-approval-only-read-context", approved.Body.String())
	})

	for _, tt := range []struct {
		name   string
		admin  int32
		id     int64
		want   []string
		absent []string
	}{
		{"owner-reply", 90007, 910001,
			[]string{"Review the club reply", "Synthetic reply awaiting owner review", `action="/admin/cases/910001/response-reviewed"`, "Synthetic private reply case", "Synthetic evidence.txt"},
			[]string{`action="/admin/cases/910001/propose"`, `action="/admin/cases/910001/approve"`}},
		{"owner-notice-review", 90007, 910002,
			[]string{"Owner notice review", "Penalty notices", `id="owner-outcome-submit"`, `action="/admin/cases/910002/send-for-approval"`, `name="reviewed_offending_club" value="920002:940021"`, `name="reviewed_reporting_club" value="920002:940022"`, `name="reviewed_official" value="920002:940023"`, "Synthetic saved offending notice"},
			[]string{`action="/admin/cases/910002/approve"`, "draft unavailable"}},
		{"designated-same-proposer", 90008, 910003,
			[]string{"Decision approval", "Owner review complete", `action="/admin/cases/910003/approve"`, "may approve a proposal they prepared", "Synthetic saved reporting notice"},
			[]string{`action="/admin/cases/910003/publish"`, `id="owner-outcome-submit"`, "draft unavailable"}},
		{"general-approver-not-designated", 90009, 910003,
			[]string{"Awaiting decision approval", "Your current access does not permit approval"},
			[]string{`action="/admin/cases/910003/approve"`, `action="/admin/cases/910003/reject"`}},
		{"final-issuer", 90010, 910004,
			[]string{"Final sign-off and issue", `action="/admin/cases/910004/publish"`, "Synthetic locked outcome wording", "Synthetic locked outcome subject", "View offending-club PDF"},
			[]string{`action="/admin/cases/910004/outcome-drafts/`, `action="/admin/cases/910004/approve"`}},
		{"approver-awaits-separate-issuer", 90008, 910004,
			[]string{"Awaiting final issue", "Synthetic locked outcome wording"},
			[]string{`action="/admin/cases/910004/publish"`}},
		{"changed-source", 90008, 910005,
			[]string{"Review changed source evidence", `action="/admin/cases/910005/reopen-source-change"`, "Synthetic corrected source allegation", "Original report details"},
			[]string{`action="/admin/cases/910005/publish"`}},
		{"issuer-awaits-source-review", 90010, 910005,
			[]string{"Review changed source evidence", `action="/admin/cases/910005/reopen-source-change"`},
			[]string{`action="/admin/cases/910005/publish"`}},
		{"completed-follow-up", 90007, 910006,
			[]string{"Case outcome recorded", "Synthetic late historical reply", "Synthetic delivery failure", "Synthetic follow-up evidence check", "Open supporting tasks", "Synthetic historical explanation"},
			[]string{"Review the club reply", `action="/admin/cases/910006/propose"`, `action="/admin/cases/910006/approve"`, `action="/admin/cases/910006/publish"`}},
		{"ordinary-same-proposer", 90009, 910007,
			[]string{"Awaiting decision approval"},
			[]string{`action="/admin/cases/910007/approve"`, `action="/admin/cases/910007/reject"`}},
		{"current-revision-owner-checkpoint", 90008, 910008,
			[]string{"Owner notice review", `id="owner-outcome-submit"`, `name="reviewed_offending_club" value="920082:0"`},
			[]string{`action="/admin/cases/910008/approve"`, "Owner review complete", "draft unavailable"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := adminWorkspaceHandlerRequest(t, router, tt.admin, http.MethodGet, fmt.Sprintf("/admin/cases/%d", tt.id), nil)
			assertAdminWorkspaceHandlerHTML(t, response, tt.want, tt.absent)
			if !strings.Contains(response.Body.String(), `name="csrf_token" value="synthetic-csrf-token"`) {
				t.Fatal("existing forms lost the middleware CSRF token")
			}
			captureAdminWorkspaceHandlerHTML(t, "case-"+tt.name, response.Body.String())
		})
	}

	t.Run("filtered results context survives following GET", func(t *testing.T) {
		destination := "/admin/ineligible?view=cases&scope=mine&q=Synthetic&page=2"
		response := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/cases/910001?return_to="+url.QueryEscape(destination), nil)
		assertAdminWorkspaceHandlerHTML(t, response, []string{"Back to results", `href="/admin/ineligible?view=cases&amp;scope=mine&amp;q=Synthetic&amp;page=2"`}, nil)
		request := httptest.NewRequest(http.MethodGet, "https://synthetic.invalid/admin/cases/910001?success=Recorded", nil)
		addAdminWorkspaceHandlerSession(t, request, 90007)
		request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "synthetic-csrf-token"})
		for _, cookie := range response.Result().Cookies() {
			if strings.HasPrefix(cookie.Name, "case_return_") {
				request.AddCookie(cookie)
			}
		}
		followed := httptest.NewRecorder()
		router.ServeHTTP(followed, request)
		assertAdminWorkspaceHandlerHTML(t, followed, []string{"Back to results", `href="/admin/ineligible?view=cases&amp;scope=mine&amp;q=Synthetic&amp;page=2"`}, nil)
	})

	t.Run("failed workflow read is unavailable not final issue", func(t *testing.T) {
		// The shadow table is connection-local and intentionally lacks the
		// column used by the real source-review predicate. No live table or
		// authorization function is changed to inject this read failure.
		var originalSearchPath string
		if err := pool.QueryRow(ctx, `SELECT current_setting('search_path')`).Scan(&originalSearchPath); err != nil {
			t.Fatal(err)
		}
		mustAdminWorkspaceFixtureExec(t, ctx, pool, `CREATE TEMP TABLE sanction_intake_case_links(intake_id bigint, relationship text)`)
		// Changing the explicit search path also invalidates cached prepared
		// query plans which previously resolved to the migrated table.
		mustAdminWorkspaceFixtureExec(t, ctx, pool, `SELECT set_config('search_path',$1,false)`, "pg_temp,"+originalSearchPath)
		defer func() {
			mustAdminWorkspaceFixtureExec(t, ctx, pool, `SELECT set_config('search_path',$1,false)`, originalSearchPath)
			mustAdminWorkspaceFixtureExec(t, ctx, pool, `DROP TABLE pg_temp.sanction_intake_case_links`)
		}()
		response := adminWorkspaceHandlerRequest(t, router, 90010, http.MethodGet, "/admin/cases/910004", nil)
		assertAdminWorkspaceHandlerHTML(t, response,
			[]string{"Next action unavailable", "Current decision actions are unavailable", "Synthetic locked outcome wording"},
			[]string{`action="/admin/cases/910004/publish"`, "Final sign-off and issue"})
		captureAdminWorkspaceHandlerHTML(t, "case-unavailable", response.Body.String())
	})

	t.Run("closed-season empty active work retains history and follow-up", func(t *testing.T) {
		mustAdminWorkspaceFixtureExec(t, ctx, pool, `UPDATE sanction_cases SET status='closed',closed_at=now() WHERE status NOT IN ('published','closed','rejected','withdrawn')`)
		active := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/ineligible?view=cases", nil)
		assertAdminWorkspaceHandlerHTML(t, active, []string{"0 matching cases", "No cases match these filters", "Case history"}, []string{"Casework and its count are unavailable", "SYNTHETIC-REPLY"})
		history := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/ineligible?view=history", nil)
		assertAdminWorkspaceHandlerHTML(t, history, []string{"SYNTHETIC-COMPLETED", "SYNTHETIC-REPLY", "Delivery problem", "1 open follow-up task(s)"}, []string{"Casework and its count are unavailable"})
		captureAdminWorkspaceHandlerHTML(t, "closed-season-active-cases", active.Body.String())
		captureAdminWorkspaceHandlerHTML(t, "closed-season-case-history", history.Body.String())
	})
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_notification_outbox WHERE processed_at IS NULL`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("GET/blocked POST requests unexpectedly queued messages: pending=%d error=%v", queued, err)
	}
}

func mustAdminWorkspaceFixtureExec(t *testing.T, ctx context.Context, pool *db.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, query, args...); err != nil {
		t.Fatalf("synthetic handler fixture SQL: %v", err)
	}
}

func addAdminWorkspaceHandlerSession(t *testing.T, request *http.Request, adminID int32) {
	t.Helper()
	if adminID == 0 {
		return
	}
	issued := httptest.NewRecorder()
	now := time.Now()
	if err := setAdminSessionCookie(issued, &adminSession{AdminID: adminID, Name: "Synthetic test administrator", Role: "admin", Aud: "adm", JTI: "synthetic-only", IssuedAt: now.Unix(), Expiry: now.Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range issued.Result().Cookies() {
		request.AddCookie(cookie)
	}
}

func adminWorkspaceHandlerRequest(t *testing.T, router http.Handler, adminID int32, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://synthetic.invalid"+path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "synthetic-csrf-token"})
	addAdminWorkspaceHandlerSession(t, request, adminID)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertAdminWorkspaceHandlerHTML(t *testing.T, response *httptest.ResponseRecorder, want, absent []string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("actual handler returned %d: %s", response.Code, response.Body.String())
	}
	for _, text := range want {
		if !strings.Contains(response.Body.String(), text) {
			t.Errorf("actual handler output missing %q", text)
		}
	}
	for _, text := range absent {
		if strings.Contains(response.Body.String(), text) {
			t.Errorf("actual handler output unexpectedly contains %q", text)
		}
	}
}

func captureAdminWorkspaceHandlerHTML(t *testing.T, name, body string) {
	t.Helper()
	directory := strings.TrimSpace(os.Getenv("GMCL_ADMIN_HANDLER_UI_DIR"))
	if directory == "" {
		return
	}
	// Retain only the production local hash-disclosure script after the
	// shared offline helper removes app/network scripts. Fixture forms are
	// inert and the banner distinguishes invented records from season data.
	var disclosure string
	for _, script := range regexp.MustCompile(`(?s)<script>.*?</script>`).FindAllString(body, -1) {
		if strings.Contains(script, "function reveal(hash)") && strings.Contains(script, "node.tagName==='DETAILS'") {
			disclosure = script
		}
	}
	body = offlineHomePreviewHTML(t, body)
	body = strings.Replace(body, "<body>", `<body><div class="alert alert-warning m-3">Synthetic fixture from the actual authenticated GET handler. Invented records; forms are disabled. No workers or external deliveries ran.</div>`, 1)
	body = strings.Replace(body, "</body>", disclosure+`<script>document.addEventListener('submit',function(event){event.preventDefault();});</script></body>`, 1)
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name+".html")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("Actual handler synthetic UI fixture: %s", path)
}
