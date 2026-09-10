package httpserver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestConfiguredFutureSeasonDoesNotCaptureCurrentReportsPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE TEMP TABLE seasons (
		id serial PRIMARY KEY, name text UNIQUE NOT NULL,
		start_date date NOT NULL, end_date date NOT NULL,
		is_archived boolean NOT NULL DEFAULT false);
		INSERT INTO seasons(name,start_date,end_date) VALUES
		('2026','2026-04-18','2026-09-12');`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/0088_configure_2027_season.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Reapplying configuration must not create another season.
	for i := 0; i < 2; i++ {
		if _, err = conn.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM seasons`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("season count=%d, error=%v", count, err)
	}
	for _, query := range []string{reportFallbackSeasonQuery, defaultSanctionsSeasonQuery} {
		for _, tc := range []struct {
			date string
			want int32
		}{
			{"2026-09-10", 1}, {"2026-12-31", 1}, {"2027-04-16", 1},
			{"2027-04-17", 2}, {"2027-09-11", 2}, {"2027-10-01", 2},
		} {
			var id int32
			if err = conn.QueryRow(ctx, query, tc.date).Scan(&id); err != nil || id != tc.want {
				t.Fatalf("date=%s: season=%d want=%d error=%v", tc.date, id, tc.want, err)
			}
		}
		var id int32
		if err = conn.QueryRow(ctx, query, "2025-01-01").Scan(&id); err != pgx.ErrNoRows {
			t.Fatalf("pre-season report should have no inferred season: %v", err)
		}
	}
	var id int32
	var name string
	if err = conn.QueryRow(ctx, captainFormSeasonsQuery, "2026-09-10").Scan(&id, &name); err != nil || id != 1 {
		t.Fatalf("captain settings default=%d error=%v", id, err)
	}
	var start, end time.Time
	if err = conn.QueryRow(ctx, `SELECT start_date,end_date FROM seasons WHERE name='2027' AND NOT is_archived`).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if start.Format("2006-01-02") != "2027-04-17" || end.Format("2006-01-02") != "2027-09-11" {
		t.Fatalf("unexpected 2027 bounds: %v to %v", start, end)
	}
	// Do not silently overwrite a season configured differently by an admin.
	if _, err = conn.Exec(ctx, `UPDATE seasons SET end_date='2027-09-18' WHERE name='2027'`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, string(migration)); err == nil {
		t.Fatal("expected conflicting configuration to be rejected")
	}
}
