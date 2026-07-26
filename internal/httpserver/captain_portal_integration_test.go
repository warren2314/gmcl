package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestCaptainEntryPortalPrefillValidatesClubTeamPair(t *testing.T) {
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

	clubID, teamID := seedCaptainPrefillPair(t, ctx, pool)
	server := &Server{DB: pool}
	request := httptest.NewRequest(
		http.MethodGet,
		"/?portal_club_id="+int32Text(clubID)+"&portal_team_id="+int32Text(teamID),
		nil,
	)
	prefill, ok := server.loadCaptainEntryPrefill(request)
	if !ok || prefill.ClubID != clubID || prefill.TeamID != teamID ||
		prefill.ClubName == "" {
		t.Fatalf("valid portal prefill = %#v, %v", prefill, ok)
	}

	wrongClubRequest := httptest.NewRequest(
		http.MethodGet,
		"/?portal_club_id="+int32Text(clubID+1)+"&portal_team_id="+int32Text(teamID),
		nil,
	)
	if prefill, ok := server.loadCaptainEntryPrefill(wrongClubRequest); ok {
		t.Fatalf("cross-club team prefill accepted: %#v", prefill)
	}
}

func seedCaptainPrefillPair(
	t *testing.T,
	ctx context.Context,
	pool *db.Pool,
) (int32, int32) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1804289383, 9148)`); err != nil {
		t.Fatal(err)
	}
	var clubID int32
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) + 4001 FROM clubs`).Scan(&clubID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO clubs (id, name, short_name)
		VALUES ($1, $2, 'PREF')
	`, clubID, "Portal Prefill "+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var teamID int32
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) + 4001 FROM teams`).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO teams (id, club_id, name, level, active)
		VALUES ($1, $2, 'Portal Prefill XI', 1, TRUE)
	`, teamID, clubID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return clubID, teamID
}

func int32Text(value int32) string {
	return strconv.FormatInt(int64(value), 10)
}
