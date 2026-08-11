package starred

import (
	"context"
	"os"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
)

func setupStoreTestDB(t *testing.T) *db.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set - skipping DB tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	return pool
}

func TestPendingMatchesIncludeAugustAndExcludeSyntheticFixtureIDs(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()
	const (
		seasonYear       = 2026
		realMatchID      = int64(910000000000000001)
		syntheticMatchID = -realMatchID
	)

	_, _ = pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id IN ($1,$2)`, realMatchID, syntheticMatchID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM league_fixtures WHERE play_cricket_match_id IN ($1,$2)`, realMatchID, syntheticMatchID)
		pool.Close()
	})

	before, err := PendingMatchCount(ctx, pool, seasonYear)
	if err != nil {
		t.Fatalf("pending count before insert: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO league_fixtures(
			play_cricket_match_id,match_date,home_club_name,away_club_name,
			home_team_name,away_team_name,payload
		) VALUES
			($1,'2026-08-08','Real Home CC','Real Away CC','Real Home CC - 1st XI','Real Away CC - 1st XI','{}'::jsonb),
			($2,'2026-08-08','Synthetic Home CC','Synthetic Away CC','Synthetic Home CC - 1st XI','Synthetic Away CC - 1st XI','{"pitch_fixture_source":"play_cricket_ground_xlsx"}'::jsonb)
	`, realMatchID, syntheticMatchID)
	if err != nil {
		t.Fatalf("insert fixtures: %v", err)
	}

	ids, err := PendingMatchIDs(ctx, pool, seasonYear, 100)
	if err != nil {
		t.Fatalf("pending match IDs: %v", err)
	}
	if !containsMatchID(ids, realMatchID) {
		t.Errorf("real Play-Cricket fixture %d was not pending", realMatchID)
	}
	if containsMatchID(ids, syntheticMatchID) {
		t.Errorf("synthetic fixture %d must not be pending", syntheticMatchID)
	}

	after, err := PendingMatchCount(ctx, pool, seasonYear)
	if err != nil {
		t.Fatalf("pending count after insert: %v", err)
	}
	if after != before+1 {
		t.Errorf("pending count changed by %d, want 1", after-before)
	}
}

func containsMatchID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
