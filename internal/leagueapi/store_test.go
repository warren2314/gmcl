package leagueapi

import (
	"context"
	"os"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
)

// setupStoreTestDB connects to a test database. All tests in this file are skipped
// when TEST_DB_DSN is not set (i.e. in plain `go test` without Docker).
func setupStoreTestDB(t *testing.T) *db.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set – skipping DB tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	return pool
}

// ---------------------------------------------------------------------------
// UpsertFixtureRow
// ---------------------------------------------------------------------------

func TestUpsertFixtureRow_SingleGMCLFixture(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	d := MatchDetail{
		MatchID:       "90001",
		LeagueID:      "5501",
		CompetitionID: "88101",
		MatchDate:     "05/04/2025",
		GroundName:    "Heaton Mersey CC Ground",
		HomeTeamName:  "Heaton Mersey CC - 1st XI",
		HomeTeamID:    "10011",
		HomeClubName:  "Heaton Mersey CC",
		HomeClubID:    "2001",
		AwayTeamName:  "Denton St Lawrence CC - 1st XI",
		AwayTeamID:    "10021",
		AwayClubName:  "Denton St Lawrence CC",
		AwayClubID:    "2002",
		Umpire1Name:   "R. Patel",
		Umpire2Name:   "S. Khan",
	}

	if err := UpsertFixtureRow(ctx, pool, nil, d); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Verify it is in the table.
	var storedUmpire1, storedUmpire2 string
	var storedMatchDate string
	err := pool.QueryRow(ctx, `
		SELECT umpire_1_name, umpire_2_name, match_date::text
		FROM league_fixtures WHERE play_cricket_match_id = 90001
	`).Scan(&storedUmpire1, &storedUmpire2, &storedMatchDate)
	if err != nil {
		t.Fatalf("query after upsert: %v", err)
	}
	if storedUmpire1 != "R. Patel" {
		t.Errorf("umpire1: got %q", storedUmpire1)
	}
	if storedUmpire2 != "S. Khan" {
		t.Errorf("umpire2: got %q", storedUmpire2)
	}
	if storedMatchDate != "2025-04-05" {
		t.Errorf("match_date: got %q", storedMatchDate)
	}

	// Cleanup.
	pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id = 90001`)
}

func TestUpsertFixtureRow_IdempotentUpdate(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	d := MatchDetail{
		MatchID:      "90002",
		MatchDate:    "05/04/2025",
		HomeTeamID:   "10031",
		AwayTeamID:   "10041",
		Umpire1Name:  "T. Briggs",
		Umpire2Name:  "D. Walsh",
	}

	// First insert.
	if err := UpsertFixtureRow(ctx, pool, nil, d); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Update umpire2 (umpire appointed after initial sync).
	d.Umpire2Name = "J. Clarke"
	if err := UpsertFixtureRow(ctx, pool, nil, d); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var umpire2 string
	pool.QueryRow(ctx, `SELECT umpire_2_name FROM league_fixtures WHERE play_cricket_match_id = 90002`).Scan(&umpire2)
	if umpire2 != "J. Clarke" {
		t.Errorf("umpire2 after update: got %q", umpire2)
	}

	// Cleanup.
	pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id = 90002`)
}

func TestUpsertFixtureRow_MissingMatchID(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	d := MatchDetail{MatchDate: "05/04/2025"}
	if err := UpsertFixtureRow(ctx, pool, nil, d); err == nil {
		t.Fatal("expected error for missing match_id")
	}
}

func TestUpsertFixtureRow_InvalidMatchDate(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	d := MatchDetail{MatchID: "99999", MatchDate: "not-a-date"}
	if err := UpsertFixtureRow(ctx, pool, nil, d); err == nil {
		t.Fatal("expected error for invalid match_date")
	}
}

// ---------------------------------------------------------------------------
// UpsertFixtureBatch
// ---------------------------------------------------------------------------

func TestUpsertFixtureBatch_MultipleGMCLFixtures(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	r, err := ParseMatchDetailsJSON([]byte(gmclSaturdayJSON))
	if err != nil {
		t.Fatal(err)
	}

	// Use high match IDs to avoid colliding with real data.
	for i := range r.MatchDetails {
		r.MatchDetails[i].MatchID = "99" + r.MatchDetails[i].MatchID
	}

	if err := UpsertFixtureBatch(ctx, pool, nil, r.MatchDetails); err != nil {
		t.Fatalf("batch upsert: %v", err)
	}

	var count int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM league_fixtures
		WHERE play_cricket_match_id IN (9990001, 9990002, 9990003)
	`).Scan(&count)
	if count != 3 {
		t.Errorf("want 3 rows, got %d", count)
	}

	// Cleanup.
	pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id IN (9990001, 9990002, 9990003)`)
}

func TestUpsertFixtureBatch_EmptySlice(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	if err := UpsertFixtureBatch(ctx, pool, nil, nil); err != nil {
		t.Fatalf("empty batch should not error: %v", err)
	}
}

func TestUpsertFixtureBatch_StopsOnFirstError(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	details := []MatchDetail{
		{MatchID: "9980001", MatchDate: "05/04/2025", HomeTeamID: "10011"},
		{MatchID: "",        MatchDate: "05/04/2025"}, // will fail — no match_id
		{MatchID: "9980002", MatchDate: "05/04/2025", HomeTeamID: "10021"},
	}
	err := UpsertFixtureBatch(ctx, pool, nil, details)
	if err == nil {
		t.Fatal("expected error for missing match_id in batch")
	}

	// Cleanup whatever may have been inserted before the failure.
	pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id IN (9980001, 9980002)`)
}

// ---------------------------------------------------------------------------
// LookupUmpirePrefill (needs a team with play_cricket_team_id set)
// ---------------------------------------------------------------------------

func TestLookupUmpirePrefill_NoFixture(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	// Team ID 0 won't exist; expect no prefill.
	_, _, ok := LookupUmpirePrefill(ctx, pool, 0, time.Now())
	if ok {
		t.Error("want ok=false for non-existent team")
	}
}

func TestLookupUmpirePrefill_NoPlayCricketID(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	// Find a seeded team that has no play_cricket_team_id (Test Club team).
	var teamID int32
	err := pool.QueryRow(ctx, `
		SELECT t.id FROM teams t
		JOIN clubs c ON c.id = t.club_id
		WHERE c.name = 'Test Club' AND (t.play_cricket_team_id IS NULL OR t.play_cricket_team_id = '')
		LIMIT 1
	`).Scan(&teamID)
	if err != nil || teamID == 0 {
		t.Skip("no team without play_cricket_team_id found in DB")
	}

	_, _, ok := LookupUmpirePrefill(ctx, pool, teamID, time.Now())
	if ok {
		t.Error("want ok=false when play_cricket_team_id is not set")
	}
}

func TestLookupUmpirePrefill_WithFixture(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	// Insert a test fixture for Heaton Mersey CC 1st XI (PCTeamID 10011).
	_, err := pool.Exec(ctx, `
		INSERT INTO league_fixtures (
			play_cricket_match_id, match_date,
			home_team_pc_id, away_team_pc_id,
			umpire_1_name, umpire_2_name,
			payload
		) VALUES (
			9970001, '2025-04-05',
			'10011', '10021',
			'R. Patel', 'S. Khan',
			'{}'
		) ON CONFLICT (play_cricket_match_id) DO UPDATE
			SET umpire_1_name = EXCLUDED.umpire_1_name,
			    umpire_2_name = EXCLUDED.umpire_2_name
	`)
	if err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id = 9970001`)
	})

	// Find a seeded team with PCTeamID = 10011.
	var teamID int32
	err = pool.QueryRow(ctx, `SELECT id FROM teams WHERE play_cricket_team_id = '10011' LIMIT 1`).Scan(&teamID)
	if err != nil || teamID == 0 {
		t.Skip("no team with play_cricket_team_id=10011 in DB (run seed first)")
	}

	matchDate := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	u1, u2, ok := LookupUmpirePrefill(ctx, pool, teamID, matchDate)
	if !ok {
		t.Fatal("want ok=true for team with matching fixture")
	}
	if u1 != "R. Patel" {
		t.Errorf("umpire1: got %q", u1)
	}
	if u2 != "S. Khan" {
		t.Errorf("umpire2: got %q", u2)
	}
}

func TestLookupUmpirePrefill_AsAwayTeam(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	// Denton St Lawrence (away, PCTeamID 10021) should also return umpires.
	_, err := pool.Exec(ctx, `
		INSERT INTO league_fixtures (
			play_cricket_match_id, match_date,
			home_team_pc_id, away_team_pc_id,
			umpire_1_name, umpire_2_name,
			payload
		) VALUES (
			9970002, '2025-04-05',
			'10011', '10021',
			'R. Patel', 'S. Khan',
			'{}'
		) ON CONFLICT (play_cricket_match_id) DO UPDATE
			SET umpire_1_name = EXCLUDED.umpire_1_name,
			    umpire_2_name = EXCLUDED.umpire_2_name
	`)
	if err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id = 9970002`)
	})

	var teamID int32
	err = pool.QueryRow(ctx, `SELECT id FROM teams WHERE play_cricket_team_id = '10021' LIMIT 1`).Scan(&teamID)
	if err != nil || teamID == 0 {
		t.Skip("no team with play_cricket_team_id=10021 in DB")
	}

	matchDate := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	u1, u2, ok := LookupUmpirePrefill(ctx, pool, teamID, matchDate)
	if !ok {
		t.Fatal("want ok=true for away team with matching fixture")
	}
	if u1 != "R. Patel" || u2 != "S. Khan" {
		t.Errorf("umpires: %q / %q", u1, u2)
	}
}

func TestLookupUmpirePrefill_WrongDate(t *testing.T) {
	pool := setupStoreTestDB(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		INSERT INTO league_fixtures (
			play_cricket_match_id, match_date,
			home_team_pc_id, away_team_pc_id,
			umpire_1_name, umpire_2_name,
			payload
		) VALUES (
			9970003, '2025-04-05',
			'10031', '10041',
			'T. Briggs', 'D. Walsh',
			'{}'
		) ON CONFLICT (play_cricket_match_id) DO UPDATE
			SET umpire_1_name = EXCLUDED.umpire_1_name,
			    umpire_2_name = EXCLUDED.umpire_2_name
	`)
	if err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM league_fixtures WHERE play_cricket_match_id = 9970003`)
	})

	var teamID int32
	err = pool.QueryRow(ctx, `SELECT id FROM teams WHERE play_cricket_team_id = '10031' LIMIT 1`).Scan(&teamID)
	if err != nil || teamID == 0 {
		t.Skip("no team with play_cricket_team_id=10031 in DB")
	}

	// Look up a different date — should return no result.
	wrongDate := time.Date(2025, 4, 12, 0, 0, 0, 0, time.UTC)
	_, _, ok := LookupUmpirePrefill(ctx, pool, teamID, wrongDate)
	if ok {
		t.Error("want ok=false when no fixture exists for the given date")
	}
}
