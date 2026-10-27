package httpserver

import (
	"context"
	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/juniordemo"
	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/migrate"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestJuniorDemoRefusesNonDemoDatabasePostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	handler, err := NewJuniorDemoHandler(ctx, pool, JuniorDemoOptions{BaseURL: "https://demo.example.invalid", ExpiresAt: time.Now().Add(time.Hour)})
	if err == nil || handler != nil {
		t.Fatal("demo handler accepted an application/test database")
	}
	if _, err = juniordemo.Seed(ctx, pool, "synthetic-demo-password-not-a-live-credential"); err == nil {
		t.Fatal("demo seed accepted a database outside its dedicated name")
	}
	var touched bool
	if err = pool.QueryRow(ctx, `SELECT to_regclass('junior_demo_state') IS NOT NULL`).Scan(&touched); err != nil || touched {
		t.Fatalf("refused demo still changed database: %v %v", touched, err)
	}
}

func TestJuniorDemoPopulatedJourneyPostgres(t *testing.T) {
	dsn := os.Getenv("GMCL_JUNIOR_DEMO_REHEARSAL_DSN")
	if dsn == "" {
		t.Skip("dedicated populated demo rehearsal not requested")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	basePool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer basePool.Close()
	var name string
	if err = basePool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil || name != "gmcl_junior_demo" {
		t.Fatal("dedicated demo rehearsal database required")
	}
	if _, err = basePool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"demo_rehearsal_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err = basePool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	native, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	pool := &db.Pool{Pool: native}
	defer func() {
		pool.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := basePool.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	for k, v := range map[string]string{"JUNIOR_PORTAL_ENABLED": "true", "JUNIOR_OUTBOUND_EMAIL_ENABLED": "false", "SMTP_HOST": "", "EMAIL_OVERRIDE": "", "PUBLIC_BASE_URL": "https://demo.example.invalid", "ADMIN_SESSION_SECRET": "synthetic-demo-rehearsal-session-secret", "DISABLE_2FA": "1", "SUPER_ADMIN_EMAILS": "", "JUNIOR_UPLOAD_DIR": t.TempDir()} {
		t.Setenv(k, v)
	}
	if err = migrate.Run(ctx, pool, os.DirFS(filepath.Join("..", "..")), "migrations"); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-demo-rehearsal-password-only"
	seed, err := juniordemo.Seed(ctx, pool, password)
	if err != nil {
		t.Fatal(err)
	}
	again, err := juniordemo.Seed(ctx, pool, password)
	if err != nil || again.AdminID != seed.AdminID || again.SeasonID != seed.SeasonID {
		t.Fatalf("restart changed demo identities: %+v %v", again, err)
	}
	handler, err := NewJuniorDemoHandler(ctx, pool, JuniorDemoOptions{BaseURL: "https://demo.example.invalid", AssetDir: filepath.Join("..", ".."), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]*http.Cookie{}
	request := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		if method == http.MethodPost {
			if form == nil {
				form = url.Values{}
			}
			if c := cookies[middleware.CSRFCookieName]; c != nil {
				form.Set("csrf_token", c.Value)
			}
		}
		r := httptest.NewRequest(method, "https://demo.example.invalid"+path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		for _, c := range w.Result().Cookies() {
			cookies[c.Name] = c
		}
		return w
	}
	if w := request("GET", "/admin/demo-guide", nil); w.Code != 303 {
		t.Fatalf("demo guide missing staff authentication: %d", w.Code)
	}
	if w := request("GET", "/admin/cases", nil); w.Code != 404 {
		t.Fatalf("demo exposed non-junior operation: %d", w.Code)
	}
	if w := request("GET", "/admin/login", nil); w.Code != 200 {
		t.Fatalf("demo login: %d", w.Code)
	}
	if w := request("POST", "/admin/login", url.Values{"username": {"joe-demo"}, "password": {password}}); w.Code != 303 && w.Code != 302 {
		t.Fatalf("normal staff login: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/admin/juniors", "/admin/juniors/contacts", fmt.Sprintf("/admin/juniors/clubs/%d?season_id=%d", seed.Clubs[0], seed.SeasonID), "/admin/juniors/competitions", "/admin/juniors/concessions", "/admin/juniors/messages"} {
		w := request("GET", path, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "DEMONSTRATION") {
			t.Fatalf("populated demo page %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request("GET", "/junior/login", nil); w.Code != 200 {
		t.Fatal("club login unavailable")
	}
	if w := request("POST", "/junior/login", url.Values{"email": {"lead1@demo.example.invalid"}}); w.Code != 200 {
		t.Fatal("club login request failed")
	}
	inbox := request("GET", "/admin/demo-guide", nil)
	match := regexp.MustCompile(`href="(https://demo\.example\.invalid/junior/access\?token=[^"]+)"`).FindStringSubmatch(inbox.Body.String())
	if len(match) != 2 {
		t.Fatal("fictional sign-in email missing from simulated inbox")
	}
	link, err := url.Parse(html.UnescapeString(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	if w := request("GET", link.RequestURI(), nil); w.Code != 200 {
		t.Fatal("one-use sign-in confirmation unavailable")
	}
	if w := request("POST", "/junior/access", url.Values{"token": {link.Query().Get("token")}}); w.Code != 303 {
		t.Fatalf("normal club token redemption: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", "/junior", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Oakfield") {
		t.Fatalf("club home: %d %s", w.Code, w.Body.String())
	}
	var assignment int64
	if err = pool.QueryRow(ctx, `SELECT id FROM junior_form_assignments WHERE form_id=$1 AND club_id=$2`, seed.FormID, seed.Clubs[0]).Scan(&assignment); err != nil {
		t.Fatal(err)
	}
	formPath := fmt.Sprintf("/junior/forms/%d", assignment)
	if w := request("POST", formPath, url.Values{"action": {"submit"}, "draft_revision": {"0"}, "answer_confirmed": {"yes"}, "answer_notes": {"Fictional club submission during rehearsal"}}); w.Code != 303 {
		t.Fatalf("actual club submission: %d %s", w.Code, w.Body.String())
	}
	var submission int64
	if err = pool.QueryRow(ctx, `SELECT latest_submission_id FROM junior_form_assignments WHERE id=$1`, assignment).Scan(&submission); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", fmt.Sprintf("/admin/juniors/forms/assignments/%d", assignment), url.Values{"action": {"complete"}, "submission_id": {fmt.Sprint(submission)}, "reason": {"Rehearsal checked"}}); w.Code != 303 {
		t.Fatalf("actual staff review: %d %s", w.Code, w.Body.String())
	}
	state, err := junior.NewService(pool).ClubWorkspace(ctx, junior.Principal{AdminID: seed.AdminID}, seed.Clubs[0], seed.SeasonID)
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for _, f := range state.Forms {
		if f.ID == assignment && f.Status == "completed" {
			completed = true
		}
	}
	if !completed {
		t.Fatal("form journey did not reach completed")
	}
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_message_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("demo attempted real outgoing transport: %d %v", attempts, err)
	}
}
