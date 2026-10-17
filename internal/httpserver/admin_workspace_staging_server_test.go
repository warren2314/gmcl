package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

// Opt-in local walkthrough only. This starts the actual authenticated handlers
// against a newly migrated disposable schema, without constructing NewServer
// or starting any scheduler, importer, email worker or external API client.
// Password-only login is explicit fixture configuration, not a 2FA test.
func TestAdminWorkspaceStagingWalkthroughServer(t *testing.T) {
	if os.Getenv("GMCL_STAGING_WALKTHROUGH") != "1" {
		t.Skip("GMCL_STAGING_WALKTHROUGH is not enabled")
	}
	directory, err := filepath.Abs(strings.TrimSpace(os.Getenv("GMCL_STAGING_DIR")))
	if err != nil || strings.TrimSpace(os.Getenv("GMCL_STAGING_DIR")) == "" {
		t.Fatal("an explicit task-local GMCL_STAGING_DIR is required")
	}
	assetDirectory := strings.TrimSpace(os.Getenv("GMCL_STAGING_ASSET_DIR"))
	assets := map[string]string{
		bootstrapCSS: "bootstrap-5.3.3.min.css", bootstrapJS: "bootstrap-5.3.3.bundle.min.js",
		htmxJS: "htmx-1.9.12.min.js", chartJS: "chart-4.4.3.umd.min.js",
	}
	for _, name := range assets {
		info, err := os.Stat(filepath.Join(assetDirectory, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("pinned local staging asset %s is required: %v", name, err)
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(directory, "walkthrough.lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("another walkthrough may own this directory: %v", err)
	}
	_ = lock.Close()
	t.Cleanup(func() { _ = os.Remove(lockPath) })
	stopPath := filepath.Join(directory, "stop")
	if err := os.Remove(stopPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"ADMIN_SESSION_SECRET": "synthetic-local-staging-secret-no-production-use",
		"DISABLE_2FA":          "1", "SUPER_ADMIN_EMAILS": "", "ENABLE_HSTS": "0",
		"SANCTIONS_EMAIL_DISABLED": "true", "INELIGIBLE_OUTBOUND_EMAIL_ENABLED": "false",
		"SMTP_HOST": "", "SMTP_USERNAME": "", "SMTP_PASSWORD": "", "EMAIL_OVERRIDE": "",
		"INELIGIBLE_IMPORT_ENABLED": "false", "INELIGIBLE_BOOTSTRAP_IMPORT_ENABLED": "false",
		"RULES_ASSISTANT_ENABLED": "false", "OPENAI_API_KEY": "",
		"PLAY_CRICKET_API_BASE_URL": "", "PLAY_CRICKET_API_KEY": "", "PLAY_CRICKET_API_AUTH_HEADER": "",
		"SANCTIONS_EVIDENCE_DIR": filepath.Join(directory, "evidence"),
		"INELIGIBLE_UPLOAD_DIR":  filepath.Join(directory, "uploads"),
	} {
		t.Setenv(name, value)
	}
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	seedAdminWorkspaceHandlerCases(t, ctx, pool)
	seedAdminWorkspaceStagingActions(t, ctx, pool)
	const password = "GMCL-Synthetic-Walkthrough-2026!"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET password_hash=$1 WHERE id BETWEEN 90007 AND 90015`, hash); err != nil {
		t.Fatal(err)
	}
	// Migration-owned directory entries remain historical, but no active
	// recipient in this fixture may address an actual person's mailbox.
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `
		UPDATE sanction_club_contacts SET active=false WHERE email NOT LIKE '%@example.invalid';
		INSERT INTO sanction_intakes(id,origin,external_key,state,latest_revision,reporting_club_text,offending_club_text,team_text,player_text,fixture_date) VALUES
			(992001,'native_form','staging-new-report','new',1,'Synthetic Reporting Club','Synthetic Offending Club','Synthetic First XI','Synthetic New Report Player','2026-07-04'),
			(992002,'native_form','staging-reviewing-report','reviewing',1,'Synthetic Reporting Club','Synthetic Offending Club','Synthetic Second XI','Synthetic Reviewing Player','2026-07-05'),
			(992003,'native_form','staging-historical-report','ignored',1,'Synthetic Reporting Club','Synthetic Offending Club','Synthetic First XI','Synthetic Historical Report Player','2026-06-01');
		INSERT INTO sanction_intake_revisions(intake_id,revision,raw_data,raw_sha256)
			SELECT id,1,jsonb_build_object('player',player_text,'reason','Synthetic allegation for local walkthrough only'),repeat('e',64)
			FROM sanction_intakes WHERE id BETWEEN 992001 AND 992003;
		INSERT INTO sanction_cases(id,reference,source_type,status,season_id,club_id,team_id,assigned_admin_id,player_name,match_date,private_summary)
			VALUES(911003,'STAGING-TRAINING','ineligible_player','investigating',90001,90001,90001,90007,'Synthetic Training Player','2026-07-05','Synthetic training case for local walkthrough');
		INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,after_data)
			VALUES(911003,'case_training_designated','system','Synthetic staging fixture','Synthetic training case for local walkthrough',
				'{"training_case":true,"real_email_enabled":false}');
	`)
	// Verify the live browser seed through the actual workspace query. A
	// manual private-link test is a different source and would be excluded.
	for _, training := range []bool{false, true} {
		query, args := buildIneligibleCaseQuery(ineligibleCaseFilter{View: "cases", Scope: "all", Query: "STAGING-TRAINING", Training: training}, ineligibleCaseAccess{AdminID: 90007, CanInvestigate: true}, true)
		var count int
		want := 0
		if training {
			want = 1
		}
		if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil || count != want {
			t.Fatalf("synthetic training fixture query training=%v count=%d want=%d error=%v", training, count, want, err)
		}
	}
	var unsafeRecipients, migrations int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM sanction_recipient_directory WHERE active AND email NOT LIKE '%@example.invalid')+
		(SELECT count(*) FROM sanction_club_contacts WHERE active AND email NOT LIKE '%@example.invalid')`).Scan(&unsafeRecipients); err != nil || unsafeRecipients != 0 {
		t.Fatalf("fixture has non-synthetic active recipients: %d, error=%v", unsafeRecipients, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: pool, LondonLoc: london}
	router := chi.NewRouter()
	router.Use(middleware.SecurityHeaders())
	router.Get("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin/login", http.StatusSeeOther) })
	repo := filepath.Join("..", "..")
	router.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(repo, "static")))))
	router.Handle("/images/*", http.StripPrefix("/images/", http.FileServer(http.Dir(filepath.Join(repo, "images")))))
	router.Handle("/__staging_assets/*", http.StripPrefix("/__staging_assets/", http.FileServer(http.Dir(assetDirectory))))
	router.Get("/manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(repo, "static", "manifest.webmanifest"))
	})
	router.Get("/service-worker.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(repo, "static", "service-worker.js"))
	})
	router.Mount("/admin", server.adminRouter())
	wrapped := adminWorkspaceStagingHandler(router, assets)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(wrapped)
	_ = httpServer.Listener.Close()
	httpServer.Listener = listener
	httpServer.Start()
	t.Cleanup(httpServer.Close)
	port := listener.Addr().(*net.TCPAddr).Port
	localURL := fmt.Sprintf("http://localhost:%d", port)
	t.Setenv("PUBLIC_BASE_URL", localURL)
	// Exercise the actual password verifier and session issuer before publishing
	// the ready manifest. Secure-cookie browser behavior is checked separately.
	verifyAdminWorkspaceStagingLogin(t, httpServer.URL, password)
	minutes := 20
	if configured, err := strconv.Atoi(os.Getenv("GMCL_STAGING_MINUTES")); err == nil && configured >= 1 && configured <= 30 {
		minutes = configured
	}
	started := time.Now().UTC()
	manifest := map[string]any{
		"status": "ready", "url": localURL, "transport_url": httpServer.URL,
		"login_url": localURL + "/admin/login", "password": password,
		"users": []map[string]string{
			{"username": "synthetic_owner", "purpose": "triage, case ownership, reply review"},
			{"username": "synthetic_reviewer", "purpose": "action-flow owner and designated approval"},
			{"username": "synthetic_final_issuer", "purpose": "separate final sign-off and issue"},
			{"username": "synthetic_triage_only", "purpose": "report and case workspace access"},
			{"username": "synthetic_no_grants", "purpose": "permission denial checks"},
			{"username": "synthetic_approval_only", "purpose": "approval read context without investigator controls"},
		},
		"schema": schema, "migrations": migrations, "pid": os.Getpid(), "started_at": started,
		"expires_at": started.Add(time.Duration(minutes) * time.Minute), "stop_file": stopPath,
		"authentication": "real bcrypt/session/CSRF; fixture-only password login, no email 2FA",
		"outbound":       "no workers; email disabled; active recipient mailboxes .invalid; POST allowlist",
		"case_ids":       []int{910001, 910002, 910003, 910004, 910005, 910006, 910008, 911001, 911002, 911003},
		"reset":          "stop this process and start a new harness to obtain a fresh migrated schema",
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	writeManifest := func() {
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err == nil {
			err = os.WriteFile(manifestPath, data, 0600)
		}
		if err != nil {
			t.Errorf("write walkthrough manifest: %v", err)
		}
	}
	writeManifest()
	t.Logf("Local synthetic walkthrough ready at %s; %d actual migrations; manifest %s", localURL, migrations, manifestPath)
	timer := time.NewTimer(time.Duration(minutes) * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case <-timer.C:
			manifest["stop_reason"] = "bounded walkthrough window expired"
			waiting = false
		case <-ticker.C:
			if _, err := os.Stat(stopPath); err == nil {
				manifest["stop_reason"] = "task-local stop file"
				waiting = false
			}
		}
	}
	httpServer.Close()
	manifest["status"] = "stopped"
	manifest["stopped_at"] = time.Now().UTC()
	writeManifest()
}

var adminWorkspaceStagingCasePost = regexp.MustCompile(`^/admin/cases/[0-9]+/(response-reviewed|investigation-note|manual-response|propose|send-for-approval|amend-decision|approve|reject|reopen-source-change|publish|close-no-action|tasks|assign-self|assign|outcome-drafts/(offending_club|reporting_club|official))$`)
var adminWorkspaceStagingTaskPost = regexp.MustCompile(`^/admin/cases/tasks/[0-9]+$`)
var adminWorkspaceStagingIntakePost = regexp.MustCompile(`^/admin/ineligible/[0-9]+/(link-case|ignore|duplicate)$`)

func adminWorkspaceStagingPostAllowed(path string) bool {
	return path == "/admin/login" || path == "/admin/logout" || adminWorkspaceStagingCasePost.MatchString(path) || adminWorkspaceStagingTaskPost.MatchString(path) || adminWorkspaceStagingIntakePost.MatchString(path)
}

func adminWorkspaceStagingHandler(next http.Handler, assets map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "localhost" && host != "127.0.0.1") {
			http.Error(w, "local walkthrough host required", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && (r.Method != http.MethodPost || !adminWorkspaceStagingPostAllowed(r.URL.Path)) {
			http.Error(w, "this operation is outside the local walkthrough", http.StatusForbidden)
			return
		}
		response := httptest.NewRecorder()
		next.ServeHTTP(response, r)
		for name, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		body := response.Body.Bytes()
		if strings.Contains(response.Header().Get("Content-Type"), "text/html") {
			html := string(body)
			for remote, local := range assets {
				html = strings.ReplaceAll(html, remote, "/__staging_assets/"+local)
			}
			body = []byte(html)
			w.Header().Del("Content-Length")
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(body)
	})
}

func verifyAdminWorkspaceStagingLogin(t *testing.T, baseURL, password string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(baseURL + "/admin/login")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	var csrf *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == middleware.CSRFCookieName {
			csrf = cookie
		}
	}
	if response.StatusCode != http.StatusOK || csrf == nil || !csrf.Secure {
		t.Fatal("actual login did not supply its secure CSRF cookie")
	}
	form := url.Values{"username": {"synthetic_owner"}, "password": {password}, "csrf_token": {csrf.Value}}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/admin/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(csrf)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	var session *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == adminSessionCookie {
			session = cookie
		}
	}
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/admin/dashboard" || session == nil || !session.Secure || !session.HttpOnly {
		t.Fatalf("actual staging login failed: status=%d location=%q session=%v", response.StatusCode, response.Header.Get("Location"), session != nil)
	}
}

func TestAdminWorkspaceStagingMutationBoundary(t *testing.T) {
	for _, path := range []string{"/admin/login", "/admin/logout", "/admin/cases/911001/propose", "/admin/cases/911001/outcome-drafts/official", "/admin/cases/tasks/1", "/admin/ineligible/991001/link-case"} {
		if !adminWorkspaceStagingPostAllowed(path) {
			t.Errorf("local fixture action rejected: %s", path)
		}
	}
	for _, path := range []string{"/admin/reminders/send-date", "/admin/ineligible/sync", "/admin/cases/1/response-drafts/response_request/test", "/admin/cases/1/scorecard-evidence", "/admin/users/1/resend-invite", "/admin/cases/link-tests", "/admin/cases/1/notices/1/resend"} {
		if adminWorkspaceStagingPostAllowed(path) {
			t.Errorf("external-operation path accepted: %s", path)
		}
	}
}
