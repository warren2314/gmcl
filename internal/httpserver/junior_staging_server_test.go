package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// Explicitly opt-in, loopback-only rehearsal. No application constructor,
// worker, production configuration, provider or real email is used.
func TestJuniorStagingWalkthroughServer(t *testing.T) {
	if os.Getenv("GMCL_JUNIOR_WALKTHROUGH") != "1" {
		t.Skip("local junior walkthrough not requested")
	}
	dir := strings.TrimSpace(os.Getenv("GMCL_JUNIOR_STAGING_DIR"))
	if dir == "" {
		t.Fatal("explicit staging directory required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "stop")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"ADMIN_SESSION_SECRET": "synthetic-junior-local-staging-session-secret",
		"DISABLE_2FA":          "1", "SUPER_ADMIN_EMAILS": "", "JUNIOR_PORTAL_ENABLED": "true",
		"JUNIOR_OUTBOUND_EMAIL_ENABLED": "false", "SANCTIONS_EMAIL_DISABLED": "true",
		"INELIGIBLE_OUTBOUND_EMAIL_ENABLED": "false", "SMTP_HOST": "", "EMAIL_OVERRIDE": "",
		"JUNIOR_UPLOAD_DIR": filepath.Join(dir, "badges"), "PUBLIC_BASE_URL": "https://junior.example.invalid",
	} {
		t.Setenv(name, value)
	}
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	hash, err := auth.HashPassword("Junior-Local-Walkthrough-2026!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE admin_users SET password_hash=$2 WHERE id=$1`, seed.Admin.AdminID, hash); err != nil {
		t.Fatal(err)
	}
	svc := junior.NewService(pool)
	svc.AccessMailer = func(context.Context, string, string, string) error { return nil }
	grant := seedJuniorAccess(t, pool, seed.Clubs[0], seed.Admin.AdminID, "lead-a@example.invalid")
	for i, clubID := range seed.Clubs {
		_, err = svc.SaveContact(ctx, seed.Admin, junior.ContactInput{ClubID: clubID, SeasonID: seed.SeasonID, FullName: fmt.Sprintf("Synthetic Lead %d", i+1), Email: fmt.Sprintf("lead-%d@example.invalid", i+1), Role: "junior_lead", StartsOn: time.Now().AddDate(0, 0, -1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	formID, err := svc.PublishForm(ctx, seed.Admin, junior.FormInput{SeasonID: seed.SeasonID, Title: "2027 Junior entry confirmation", Description: "Synthetic rehearsal only. Confirm your club's adult contact and team entry.", DueDate: time.Now().AddDate(0, 0, 7), RequiresReview: true, ClubIDs: seed.Clubs, Fields: []junior.FormField{{Key: "confirmed", Label: "Entry confirmed?", Type: "yes_no", Required: true}, {Key: "notes", Label: "Notes for the league", Type: "long_text"}}})
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := svc.FormAssignments(ctx, grant.Principal, formID)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("club form seed: %v", err)
	}
	compID, err := svc.CreateCompetition(ctx, seed.Admin, junior.Competition{SeasonID: seed.SeasonID, Name: "Synthetic U13 league", AgeGroup: "U13", Format: "once", StartDate: "2027-05-01", EndDate: "2027-05-31", DefaultStartTime: "18:00"})
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 { return juniorCompetitionRevision(t, ctx, svc, seed.Admin, compID) }
	for _, teamID := range seed.Teams {
		if err = svc.SaveCompetitionEntry(ctx, seed.Admin, compID, revision(), teamID, true); err != nil {
			t.Fatal(err)
		}
	}
	constraints := junior.FixtureConstraints{PlayingDates: []string{"2027-05-01", "2027-05-08", "2027-05-15"}, DurationMinutes: 60}
	for i, clubID := range seed.Clubs {
		constraints.Grounds = append(constraints.Grounds, junior.GroundConstraint{ClubID: clubID, Name: fmt.Sprintf("Synthetic Ground %d", i+1), Capacity: 1})
	}
	if err = svc.SaveFixtureConstraints(ctx, seed.Admin, compID, revision(), constraints); err != nil {
		t.Fatal(err)
	}
	if err = svc.GenerateFixtures(ctx, seed.Admin, compID, revision()); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PublishFixtures(ctx, seed.Admin, compID, revision(), true, "Synthetic local rehearsal; no external senior bookings connected"); err != nil {
		t.Fatal(err)
	}
	london, _ := time.LoadLocation("Europe/London")
	server := &Server{DB: pool, LondonLoc: london, JuniorAccessMailer: svc.AccessMailer}
	router := chi.NewRouter()
	router.Use(middleware.SecurityHeaders())
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	repo := filepath.Join("..", "..")
	router.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(repo, "static")))))
	router.Handle("/images/*", http.StripPrefix("/images/", http.FileServer(http.Dir(filepath.Join(repo, "images")))))
	assetDir := strings.TrimSpace(os.Getenv("GMCL_STAGING_ASSET_DIR"))
	if assetDir == "" {
		t.Fatal("local pinned assets required")
	}
	assets := map[string]string{bootstrapCSS: "bootstrap-5.3.3.min.css", bootstrapJS: "bootstrap-5.3.3.bundle.min.js", htmxJS: "htmx-1.9.12.min.js", chartJS: "chart-4.4.3.umd.min.js"}
	for _, name := range assets {
		if _, err = os.Stat(filepath.Join(assetDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	router.Handle("/__staging_assets/*", http.StripPrefix("/__staging_assets/", http.FileServer(http.Dir(assetDir))))
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "localhost" && host != "127.0.0.1") {
			http.Error(w, "loopback staging only", 403)
			return
		}
		allowed := strings.HasPrefix(r.URL.Path, "/admin/juniors") || strings.HasPrefix(r.URL.Path, "/junior") || strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/images/") || strings.HasPrefix(r.URL.Path, "/__staging_assets/") || r.URL.Path == "/admin/login" || r.URL.Path == "/admin/logout"
		if !allowed {
			http.Error(w, "outside junior rehearsal", 403)
			return
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		for name, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		body := response.Body.String()
		if strings.Contains(response.Header().Get("Content-Type"), "text/html") {
			for remote, local := range assets {
				body = strings.ReplaceAll(body, remote, "/__staging_assets/"+local)
			}
			w.Header().Del("Content-Length")
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write([]byte(body))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(wrapped)
	_ = httpServer.Listener.Close()
	httpServer.Listener = listener
	httpServer.StartTLS()
	defer httpServer.Close()
	baseURL := fmt.Sprintf("https://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	manifest := map[string]any{"status": "ready", "url": baseURL, "username": "junior-access-fixture", "password": "Junior-Local-Walkthrough-2026!", "lead_token": grant.Token, "club_id": seed.Clubs[0], "season_id": seed.SeasonID, "form_id": formID, "assignment_id": assignments[0].ID, "competition_id": compID, "pid": os.Getpid(), "outbound": "disabled, injected in-memory fixture mail only"}
	write := func() {
		data, e := json.MarshalIndent(manifest, "", "  ")
		if e == nil {
			e = os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600)
		}
		if e != nil {
			t.Error(e)
		}
	}
	write()
	t.Logf("Junior local walkthrough ready: %s", baseURL)
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			manifest["status"] = "stopped"
			write()
			return
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(dir, "stop")); err == nil {
				manifest["status"] = "stopped"
				write()
				return
			}
		}
	}
}
