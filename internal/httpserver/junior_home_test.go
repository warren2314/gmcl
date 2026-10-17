package httpserver

import (
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJuniorHomeEmptySetupAndUnavailablePostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-junior-home-test-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	admin, club, _ := seedJuniorAccessAdminClub(t, ctx, pool)
	s := &Server{DB: pool}
	router := chi.NewRouter()
	router.Mount("/admin", s.adminRouter())
	for _, path := range []string{"/admin/juniors", fmt.Sprintf("/admin/juniors/clubs/%d", club), fmt.Sprintf("/admin/juniors/clubs/%d/preview", club)} {
		response := adminWorkspaceHandlerRequest(t, router, admin, "GET", path, nil)
		if response.Code != 200 {
			t.Fatalf("fresh setup %s: %d %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "season_id=0") {
			t.Fatalf("fresh setup generated invalid season link: %s", path)
		}
	}
	var seasonID int64
	if err := pool.QueryRow(ctx, `INSERT INTO junior_seasons(name,start_date,end_date) VALUES('Test 2027','2027-04-01','2027-09-30') RETURNING id`).Scan(&seasonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TEMP TABLE junior_form_assignments(id bigint)`); err != nil {
		t.Fatal(err)
	}
	response := adminWorkspaceHandlerRequest(t, router, admin, "GET", fmt.Sprintf("/admin/juniors?season_id=%d", seasonID), nil)
	if response.Code != 500 || strings.Contains(response.Body.String(), "Needs attention") {
		t.Fatalf("failed totals looked like an empty dashboard: %d %s", response.Code, response.Body.String())
	}
}

func TestJuniorSeasonNavigationKeepsPreviewScope(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://example.invalid/admin/juniors/clubs/12/preview?view=forms&season_id=7&assignment_id=99", nil)
	got := juniorSeasonURL(r, "/admin/juniors/clubs/12/preview?view=contacts")
	if got != "/admin/juniors/clubs/12/preview?season_id=7&view=contacts" {
		t.Fatalf("season/preview link: %s", got)
	}
	if strings.Contains(got, "assignment_id") {
		t.Fatal("old record leaked into new destination")
	}
}
