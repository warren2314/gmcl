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
