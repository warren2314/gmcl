package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/portal"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPortalAccountActivityRendersAllowlistedProjection(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" || os.Getenv("TEST_DB_DISPOSABLE") != "1" {
		t.Skip("disposable TEST_DB_DSN not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := portal.NewStore(pool, portal.DefaultSessionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeSecurity(ctx); err != nil {
		t.Fatal(err)
	}

	var clubID int32
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(id), 0) + 6001 FROM clubs
	`).Scan(&clubID); err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	membershipID := uuid.New()
	assignmentID := uuid.New()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO clubs (id, name, short_name)
		VALUES ($1, $2, 'ACTV')
	`, clubID, "Portal Activity "+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO portal_users (id, display_name, status)
		VALUES ($1, 'Activity Test User', 'active')
	`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO portal_club_memberships (
			id, user_id, club_id, status, approved_at
		)
		VALUES ($1, $2, $3, 'active', now())
	`, membershipID, userID, clubID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO portal_role_assignments (
			id, membership_id, user_id, club_id, role_key, status, grant_reason
		)
		VALUES (
			$1, $2, $3, $4, 'club_primary_admin', 'active',
			'synthetic account activity integration test'
		)
	`, assignmentID, membershipID, userID, clubID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO portal_club_features (
			club_id, feature_key, enabled, enabled_at, notes
		)
		VALUES (
			$1, 'portal_access', TRUE, now(),
			'synthetic account activity integration test'
		)
	`, clubID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	principal, _, err := store.CreateSession(ctx, userID, portal.ClientDetails{
		IPAddress: "192.0.2.50",
		UserAgent: "GMCL Activity Integration Browser",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := store.ListActingContexts(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 || contexts[0].Assignment.ID != assignmentID {
		t.Fatalf("acting contexts = %#v", contexts)
	}
	selected, _, err := store.SelectActingContext(
		ctx,
		principal,
		assignmentID,
		portal.ClientDetails{
			IPAddress: "192.0.2.50",
			UserAgent: "GMCL Activity Integration Browser",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	server := &Server{
		DB:          pool,
		LondonLoc:   time.UTC,
		PortalStore: store,
	}
	request := httptest.NewRequest(http.MethodGet, "/portal/activity", nil)
	request = request.WithContext(context.WithValue(
		request.Context(),
		portalPrincipalContextKey{},
		selected,
	))
	recorder := httptest.NewRecorder()
	server.handlePortalActivity().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<h1 class="h2">Account activity</h1>`,
		`href="/portal/activity" aria-current="page">Activity</a>`,
		`<caption class="visually-hidden">`,
		`<th scope="col">Activity</th>`,
		"Signed in",
		"Club role selected",
		"Club Primary Administrator",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("activity page omitted %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{
		"portal.session.created",
		"portal.context.selected",
		"192.0.2.50",
		"GMCL Activity Integration Browser",
		selected.SessionID.String(),
		"target_id",
		"metadata",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("activity page disclosed %q: %s", forbidden, body)
		}
	}
}
