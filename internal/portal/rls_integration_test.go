package portal

import (
	"context"
	"os"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPortalRLSIsolatesClubTenants(t *testing.T) {
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
	store, err := NewStore(pool, DefaultSessionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeSecurity(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setSystemContext(ctx, tx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1804289383, 9048)`); err != nil {
		t.Fatal(err)
	}
	var clubA int32
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(id), 0) + 1001 FROM clubs
	`).Scan(&clubA); err != nil {
		t.Fatalf("allocate club IDs: %v", err)
	}
	clubB := clubA + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO clubs (id, name, short_name)
		VALUES ($1, $2, 'RLSA')
	`, clubA, "Portal RLS A "+suffix); err != nil {
		t.Fatalf("insert club A: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO clubs (id, name, short_name)
		VALUES ($1, $2, 'RLSB')
	`, clubB, "Portal RLS B "+suffix); err != nil {
		t.Fatalf("insert club B: %v", err)
	}

	userA := uuid.New()
	userB := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO portal_users (id, display_name, status)
		VALUES ($1, 'Portal RLS User A', 'active'),
		       ($2, 'Portal RLS User B', 'active')
	`, userA, userB); err != nil {
		t.Fatalf("insert portal users: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO portal_club_memberships (
			user_id, club_id, status, approved_at
		)
		VALUES ($1, $2, 'active', now()),
		       ($3, $4, 'active', now())
	`, userA, clubA, userB, clubB); err != nil {
		t.Fatalf("insert portal memberships: %v", err)
	}

	if err := store.applyRuntimeRole(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := setTenantContext(ctx, tx, userA, clubA); err != nil {
		t.Fatal(err)
	}
	var visibleCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM portal_club_memberships
	`).Scan(&visibleCount); err != nil {
		t.Fatalf("count tenant memberships: %v", err)
	}
	if visibleCount != 1 {
		t.Fatalf("tenant A saw %d memberships, want exactly 1", visibleCount)
	}

	var foreignCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM portal_club_memberships WHERE club_id = $1
	`, clubB).Scan(&foreignCount); err != nil {
		t.Fatalf("query foreign tenant: %v", err)
	}
	if foreignCount != 0 {
		t.Fatalf("tenant A saw %d tenant B memberships", foreignCount)
	}

	if _, err := tx.Exec(ctx, "SAVEPOINT rls_write_test"); err != nil {
		t.Fatal(err)
	}
	_, writeErr := tx.Exec(ctx, `
		INSERT INTO portal_club_memberships (user_id, club_id, status)
		VALUES ($1, $2, 'active')
	`, userA, clubB)
	if writeErr == nil {
		t.Fatal("tenant context unexpectedly wrote a membership")
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT rls_write_test"); err != nil {
		t.Fatalf("restore after expected RLS error: %v", err)
	}
}

func TestPortalAuditEventsCannotBeMutated(t *testing.T) {
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
	store, err := NewStore(pool, DefaultSessionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeSecurity(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.applyRuntimeRole(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := setSystemContext(ctx, tx); err != nil {
		t.Fatal(err)
	}

	correlationID := "test-" + uuid.NewString()
	if err := store.appendAuditTx(ctx, tx, AuditEvent{
		ActorKind:     "system",
		Action:        "test.created",
		TargetType:    "test",
		Outcome:       "success",
		CorrelationID: correlationID,
	}); err != nil {
		t.Fatalf("append audit event: %v", err)
	}
	var eventID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM portal_audit_events
		WHERE correlation_id = $1
	`, correlationID).Scan(&eventID); err != nil {
		t.Fatalf("load appended audit event: %v", err)
	}

	if _, err := tx.Exec(ctx, "SAVEPOINT audit_mutation_test"); err != nil {
		t.Fatal(err)
	}
	_, mutationErr := tx.Exec(ctx, `
		UPDATE portal_audit_events SET action = 'test.changed' WHERE id = $1
	`, eventID)
	if mutationErr == nil {
		t.Fatal("audit mutation unexpectedly succeeded")
	}
	// The exact PostgreSQL error differs depending on whether RLS or the
	// append-only trigger rejects first; either is a safe rejection.
	t.Logf("audit mutation rejected as expected: %v", mutationErr)
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT audit_mutation_test"); err != nil {
		t.Fatalf("restore after expected audit error: %v", err)
	}
}
