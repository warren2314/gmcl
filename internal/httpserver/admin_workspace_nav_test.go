package httpserver

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

var workspaceNavProtectedLinks = map[string]string{
	"/admin/ineligible":         "sanctions_triage",
	"/admin/ineligible/rollout": "sanctions_triage",
	"/admin/cases":              "sanctions_triage",
	"/admin/cases/new":          "sanctions_triage",
	"/admin/cases/imports":      "sanctions_import",
	"/admin/cases/tasks":        "sanctions_investigate",
	"/admin/cases/recipients":   "sanctions_publish",
	"/admin/cases/automation":   "sanctions_automation",
	"/admin/rankings/umpires":   "view_umpire_feedback",
}

func assertWorkspaceNavGrants(t *testing.T, html string, role string, permissions map[string]bool) {
	t.Helper()
	for href, permission := range workspaceNavProtectedLinks {
		want := role == "super_admin" || permissions[permission]
		if got := strings.Contains(html, `href="`+href+`"`); got != want {
			t.Errorf("nav href=%s present=%v, permission=%s allowed=%v", href, got, permission, want)
		}
	}
	wantAssigned := role == "super_admin" || permissions["sanctions_triage"] || permissions["sanctions_investigate"] || permissions["sanctions_approve"]
	if got := strings.Contains(html, `href="/admin/cases/mine/ineligible"`); got != wantAssigned {
		t.Errorf("personal index present=%v, want=%v", got, wantAssigned)
	}
	for _, href := range []string{"/admin/dashboard", "/admin/compliance", "/admin/fixtures", "/admin/submissions", "/admin/reports/exec", "/admin/sanctions", "/admin/change-password"} {
		if !strings.Contains(html, `href="`+href+`"`) {
			t.Errorf("existing permitted destination disappeared: %s", href)
		}
	}
	if !strings.Contains(html, `>Home</a>`) || !strings.Contains(html, `action="/admin/logout"`) {
		t.Error("workspace Home or logout navigation disappeared")
	}
}

func TestAdminWorkspaceNavMatchesExistingRouteGrants(t *testing.T) {
	for _, test := range []struct {
		name, role string
		grants     []string
	}{
		{"no grants", "admin", nil},
		{"triage only", "admin", []string{"sanctions_triage"}},
		{"investigator only", "admin", []string{"sanctions_investigate"}},
		{"approver only", "admin", []string{"sanctions_approve"}},
		{"publisher only", "admin", []string{"sanctions_publish"}},
		{"import and automation", "admin", []string{"sanctions_import", "sanctions_automation"}},
		{"umpire feedback", "admin", []string{"view_umpire_feedback"}},
		{"super admin", "super_admin", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			permissions := map[string]bool{}
			for _, grant := range test.grants {
				permissions[grant] = true
			}
			var output strings.Builder
			writeAdminNavWithPermissions(&output, "fixture-csrf", "/admin/dashboard", test.role, permissions)
			assertWorkspaceNavGrants(t, output.String(), test.role, permissions)
			if !strings.Contains(output.String(), `name="csrf_token" value="fixture-csrf"`) {
				t.Error("logout lost its CSRF token")
			}
		})
	}
}

func TestAdminWorkspaceAuthenticatedHomeNavPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-workspace-nav-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	seedAdminWorkspaceHandlerCases(t, ctx, pool)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `INSERT INTO admin_users(id,username,password_hash,email,role,is_active,force_password_change) VALUES
		(90116,'synthetic_publish_only','test'::bytea,'publish-only@example.invalid','admin',true,false);
		INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES(90116,'sanctions_publish')`)
	server := &Server{DB: pool, LondonLoc: time.UTC}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	for _, test := range []struct {
		name    string
		adminID int32
		grants  []string
	}{
		{"no sanctions access", 90012, nil},
		{"triage only", 90011, []string{"sanctions_triage"}},
		{"investigator", 90007, []string{"sanctions_triage", "sanctions_investigate", "sanctions_propose"}},
		{"approver only", 90015, []string{"sanctions_approve"}},
		{"publisher only", 90116, []string{"sanctions_publish"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := adminWorkspaceHandlerRequest(t, router, test.adminID, http.MethodGet, "/admin/dashboard", nil)
			assertAdminWorkspaceHandlerHTML(t, response, []string{"<h1 class=\"h3 mb-1\">Home</h1>"}, nil)
			html := response.Body.String()
			start := strings.Index(html, `<nav class="navbar`)
			if start < 0 {
				t.Fatal("actual Home navbar missing")
			}
			end := strings.Index(html[start:], "</nav>")
			if end < 0 {
				t.Fatal("actual Home navbar is incomplete")
			}
			permissions := map[string]bool{}
			for _, grant := range test.grants {
				permissions[grant] = true
			}
			assertWorkspaceNavGrants(t, html[start:start+end], "admin", permissions)
			if !permissions["sanctions_triage"] {
				denied := adminWorkspaceHandlerRequest(t, router, test.adminID, http.MethodGet, "/admin/ineligible", nil)
				if denied.Code != http.StatusForbidden {
					t.Errorf("navbar changed existing triage gate: status=%d", denied.Code)
				}
			}
		})
	}
}
