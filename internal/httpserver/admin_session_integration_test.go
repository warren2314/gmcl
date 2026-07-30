package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestAdminSessionRevalidatesLiveAccountState(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := uuid.NewString()
	username := "admin-session-" + suffix
	email := username + "@example.test"
	var adminID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO admin_users (
			username, password_hash, email, is_active, role
		)
		VALUES ($1, decode('00', 'hex'), $2, TRUE, 'super_admin')
		RETURNING id
	`, username, email).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		if _, err := pool.Exec(
			cleanupCtx,
			`DELETE FROM admin_users WHERE id = $1`,
			adminID,
		); err != nil {
			t.Errorf("delete test admin: %v", err)
		}
	}()

	t.Setenv("ADMIN_SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	now := time.Now()
	cookieRecorder := httptest.NewRecorder()
	if err := setAdminSessionCookie(cookieRecorder, &adminSession{
		AdminID:  adminID,
		Expiry:   now.Add(time.Hour).Unix(),
		Name:     username,
		Role:     "super_admin",
		Aud:      "adm",
		JTI:      uuid.NewString(),
		IssuedAt: now.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	cookies := cookieRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}
	sessionCookie := cookies[0]
	server := &Server{DB: pool}

	newRequest := func() *http.Request {
		request := httptest.NewRequest(
			http.MethodGet,
			"/admin/session-integration-test",
			nil,
		)
		request.AddCookie(sessionCookie)
		return request
	}
	liveRole := ""
	adminHandler := server.requireAdmin()(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			liveRole = adminRoleForRequest(r)
			w.WriteHeader(http.StatusNoContent)
		},
	))

	activeRecorder := httptest.NewRecorder()
	adminHandler.ServeHTTP(activeRecorder, newRequest())
	if activeRecorder.Code != http.StatusNoContent {
		t.Fatalf(
			"active admin status = %d, body = %q",
			activeRecorder.Code,
			activeRecorder.Body.String(),
		)
	}
	if liveRole != "super_admin" {
		t.Fatalf("active admin live role = %q, want super_admin", liveRole)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE admin_users SET role = 'admin' WHERE id = $1`,
		adminID,
	); err != nil {
		t.Fatal(err)
	}

	liveRole = ""
	demotedRecorder := httptest.NewRecorder()
	adminHandler.ServeHTTP(demotedRecorder, newRequest())
	if demotedRecorder.Code != http.StatusNoContent {
		t.Fatalf(
			"demoted admin status = %d, body = %q",
			demotedRecorder.Code,
			demotedRecorder.Body.String(),
		)
	}
	if liveRole != "admin" {
		t.Fatalf("demoted admin live role = %q, want admin", liveRole)
	}

	superAdminRecorder := httptest.NewRecorder()
	server.requireAdminRole("super_admin")(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	)).ServeHTTP(superAdminRecorder, newRequest())
	if superAdminRecorder.Code != http.StatusForbidden {
		t.Fatalf(
			"demoted super-admin route status = %d, want %d; body = %q",
			superAdminRecorder.Code,
			http.StatusForbidden,
			superAdminRecorder.Body.String(),
		)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE admin_users SET is_active = FALSE WHERE id = $1`,
		adminID,
	); err != nil {
		t.Fatal(err)
	}

	inactiveRecorder := httptest.NewRecorder()
	adminHandler.ServeHTTP(inactiveRecorder, newRequest())
	if inactiveRecorder.Code != http.StatusSeeOther {
		t.Fatalf(
			"inactive admin status = %d, want %d; body = %q",
			inactiveRecorder.Code,
			http.StatusSeeOther,
			inactiveRecorder.Body.String(),
		)
	}
	if location := inactiveRecorder.Header().Get("Location"); location != "/admin/login" {
		t.Fatalf("inactive admin redirect = %q, want /admin/login", location)
	}
	cleared := false
	for _, cookie := range inactiveRecorder.Result().Cookies() {
		if cookie.Name == adminSessionCookie &&
			cookie.Value == "" &&
			cookie.MaxAge < 0 {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Fatalf(
			"inactive admin did not receive a cleared %s cookie: %q",
			adminSessionCookie,
			inactiveRecorder.Header().Values("Set-Cookie"),
		)
	}
}

func TestAdminUserMutationRevalidatesActorInsideTransaction(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := uuid.NewString()
	var actorID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO admin_users (
			username, password_hash, email, is_active, role
		)
		VALUES ($1, decode('00', 'hex'), $2, TRUE, 'super_admin')
		RETURNING id
	`, "mutation-actor-"+suffix,
		"mutation-actor-"+suffix+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer deleteAdminSessionTestUser(t, pool, actorID)

	var targetID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO admin_users (
			username, password_hash, email, is_active, role
		)
		VALUES ($1, decode('00', 'hex'), $2, TRUE, 'admin')
		RETURNING id
	`, "mutation-target-"+suffix,
		"mutation-target-"+suffix+"@example.test").Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	defer deleteAdminSessionTestUser(t, pool, targetID)

	t.Setenv("ADMIN_SESSION_SECRET", strings.Repeat("m", 32))
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	now := time.Now()
	cookieRecorder := httptest.NewRecorder()
	if err := setAdminSessionCookie(cookieRecorder, &adminSession{
		AdminID:  actorID,
		Expiry:   now.Add(time.Hour).Unix(),
		Name:     "mutation-actor-" + suffix,
		Role:     "super_admin",
		Aud:      "adm",
		JTI:      uuid.NewString(),
		IssuedAt: now.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	cookies := cookieRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}
	staleRequest := httptest.NewRequest(
		http.MethodPost,
		"/admin/users/mutation-integration-test",
		nil,
	)
	staleRequest.AddCookie(cookies[0])
	staleSession, err := getAdminSessionFromRequest(staleRequest)
	if err != nil {
		t.Fatal(err)
	}
	if staleSession.Role != "super_admin" {
		t.Fatalf("signed session role = %q, want super_admin", staleSession.Role)
	}

	server := &Server{DB: pool}
	if err := server.withAdminUserMutationTx(
		ctx,
		staleSession.AdminID,
		&targetID,
		func(tx pgx.Tx) error {
			tag, err := tx.Exec(
				ctx,
				`UPDATE admin_users SET is_active = FALSE WHERE id = $1`,
				targetID,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errAdminUserNotFound
			}
			return nil
		},
	); err != nil {
		t.Fatalf("active super-admin mutation: %v", err)
	}

	var targetActive bool
	if err := pool.QueryRow(
		ctx,
		`SELECT is_active FROM admin_users WHERE id = $1`,
		targetID,
	).Scan(&targetActive); err != nil {
		t.Fatal(err)
	}
	if targetActive {
		t.Fatal("active super-admin mutation did not deactivate target")
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE admin_users SET role = 'admin' WHERE id = $1`,
		actorID,
	); err != nil {
		t.Fatal(err)
	}
	if staleSession.Role != "super_admin" {
		t.Fatalf(
			"signed session unexpectedly changed after DB demotion: %q",
			staleSession.Role,
		)
	}

	mutationCalled := false
	err = server.withAdminUserMutationTx(
		ctx,
		staleSession.AdminID,
		&targetID,
		func(tx pgx.Tx) error {
			mutationCalled = true
			_, err := tx.Exec(
				ctx,
				`UPDATE admin_users SET is_active = TRUE WHERE id = $1`,
				targetID,
			)
			return err
		},
	)
	if !errors.Is(err, errAdminUserMutationForbidden) {
		t.Fatalf(
			"demoted actor mutation error = %v, want %v",
			err,
			errAdminUserMutationForbidden,
		)
	}
	if mutationCalled {
		t.Fatal("mutation callback ran for demoted actor")
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT is_active FROM admin_users WHERE id = $1`,
		targetID,
	).Scan(&targetActive); err != nil {
		t.Fatal(err)
	}
	if targetActive {
		t.Fatal("target changed after demoted actor mutation was rejected")
	}
}

func deleteAdminSessionTestUser(t *testing.T, pool *db.Pool, adminID int32) {
	t.Helper()
	cleanupCtx, cleanupCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cleanupCancel()
	if _, err := pool.Exec(
		cleanupCtx,
		`DELETE FROM admin_users WHERE id = $1`,
		adminID,
	); err != nil {
		t.Errorf("delete test admin %d: %v", adminID, err)
	}
}
