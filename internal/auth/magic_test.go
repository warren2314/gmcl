package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
)

func setupTestDB(t *testing.T) *db.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// Keep these integration tests self-contained. CI starts with a freshly
	// migrated database, so the foreign-key records used by the token tests do
	// not exist until the test creates them.
	if _, err := pool.Exec(ctx, `
		INSERT INTO seasons (id, name, start_date, end_date)
		VALUES (1, 'Magic token test season', '2025-01-01', '2025-12-31')
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO weeks (id, season_id, week_number, start_date, end_date)
		VALUES (1, 1, 1, '2025-01-01', '2025-01-07')
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO clubs (id, name)
		VALUES (1, 'Magic Token Test Club')
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO teams (id, club_id, name)
		VALUES (1, 1, 'Magic Token Test XI')
		ON CONFLICT (id) DO NOTHING;

		INSERT INTO captains (id, team_id, full_name, email, active_from)
		VALUES (1, 1, 'Test Captain', 'captain@example.test', '2025-01-01')
		ON CONFLICT (id) DO NOTHING;

		DELETE FROM magic_link_tokens WHERE captain_id = 1;
	`); err != nil {
		t.Fatalf("prepare test fixtures: %v", err)
	}
	return pool
}

func TestMagicTokenLifecycle(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	token, err := GenerateAndStoreMagicToken(ctx, pool, 1, 1, 1, time.Minute, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	mt, err := ConsumeMagicToken(ctx, pool, token)
	if err != nil {
		t.Fatalf("consume token: %v", err)
	}
	if mt.CaptainID != 1 || mt.SeasonID != 1 || mt.WeekID != 1 {
		t.Fatalf("unexpected token data: %+v", mt)
	}

	// second use should fail
	if _, err := ConsumeMagicToken(ctx, pool, token); err == nil {
		t.Fatalf("expected error on second consume")
	}
}

func TestMagicTokenRevocationKeepsCaptainAndDelegateLinksSeparate(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	expiresAt := time.Now().Add(time.Hour)

	captainToken, err := GenerateAndStoreMagicTokenWithRevocation(ctx, pool, 1, 1, 1, expiresAt, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("generate captain token: %v", err)
	}

	delegateToken, err := GenerateAndStoreMagicTokenWithDelegate(ctx, pool, 1, 1, 1, nil, expiresAt, "127.0.0.1", "test-agent", "Stand In", "standin@example.test")
	if err != nil {
		t.Fatalf("generate delegate token: %v", err)
	}

	if _, err := ValidateMagicToken(ctx, pool, captainToken); err != nil {
		t.Fatalf("captain token should remain valid after delegate invite: %v", err)
	}

	newCaptainToken, err := GenerateAndStoreMagicTokenWithRevocation(ctx, pool, 1, 1, 1, expiresAt, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("generate replacement captain token: %v", err)
	}

	if _, err := ValidateMagicToken(ctx, pool, captainToken); err == nil {
		t.Fatalf("old captain token should be invalidated by a newer captain token")
	}
	if _, err := ValidateMagicToken(ctx, pool, delegateToken); err != nil {
		t.Fatalf("delegate token should remain valid after captain link refresh: %v", err)
	}

	newDelegateToken, err := GenerateAndStoreMagicTokenWithDelegate(ctx, pool, 1, 1, 1, nil, expiresAt, "127.0.0.1", "test-agent", "Stand In", "standin@example.test")
	if err != nil {
		t.Fatalf("generate replacement delegate token: %v", err)
	}

	if _, err := ValidateMagicToken(ctx, pool, delegateToken); err == nil {
		t.Fatalf("old delegate token should be invalidated by a newer invite for the same delegate")
	}
	if _, err := ValidateMagicToken(ctx, pool, newCaptainToken); err != nil {
		t.Fatalf("current captain token should remain valid after delegate re-invite: %v", err)
	}
	if mt, err := ValidateMagicToken(ctx, pool, newDelegateToken); err != nil {
		t.Fatalf("new delegate token should be valid: %v", err)
	} else if mt.DelegateEmail != "standin@example.test" {
		t.Fatalf("unexpected delegate email: %q", mt.DelegateEmail)
	}
}
