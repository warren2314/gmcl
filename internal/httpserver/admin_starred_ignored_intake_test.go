package httpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/starred"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCreateStarredCasePreservesRetiredIntakes(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// A single dedicated connection keeps temporary tables visible across the
	// service's own transactions, without touching other packages' test fixtures.
	config.MaxConns = 1
	config.MinConns = 1
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = time.Hour
	connectionPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer connectionPool.Close()
	pool := &db.Pool{Pool: connectionPool}
	server := &Server{DB: pool}
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// LIKE copies the current schema's columns, defaults and constraints. Give
	// every shadow table a private sequence so even failed INSERT/UPSERT attempts
	// leave the shared test database's sequences unchanged. PostgreSQL does not
	// copy foreign keys or triggers; these tests target the command's transaction
	// ordering and stop before the deliberate missing-fixture mapping exception.
	tables := []string{
		"sanction_intakes", "sanction_intake_revisions", "sanction_intake_sync_runs",
		"sanction_intake_case_links", "sanction_intake_events", "sanction_cases",
		"sanction_case_events", "sanction_decision_revisions", "sanction_effect_revisions",
	}
	exec(t, `CREATE TEMP SEQUENCE starred_intake_test_ids`)
	for _, table := range append(append([]string(nil), tables...), "league_fixtures") {
		name := pgx.Identifier{table}.Sanitize()
		publicName := pgx.Identifier{"public", table}.Sanitize()
		exec(t, "CREATE TEMP TABLE "+name+" (LIKE "+publicName+" INCLUDING ALL)")
		exec(t, "ALTER TABLE "+pgx.Identifier{"pg_temp", table}.Sanitize()+" ALTER COLUMN id SET DEFAULT nextval('pg_temp.starred_intake_test_ids'::regclass)")
	}
	snapshot := func(t *testing.T) map[string]string {
		t.Helper()
		result := make(map[string]string, len(tables))
		for _, table := range tables {
			var rows string
			query := "SELECT COALESCE(jsonb_agg(to_jsonb(record) ORDER BY record.id),'[]'::jsonb)::text FROM " + pgx.Identifier{"pg_temp", table}.Sanitize() + " record"
			if err := pool.QueryRow(ctx, query).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			result[table] = rows
		}
		return result
	}
	assertUnchanged := func(t *testing.T, before map[string]string) {
		t.Helper()
		for table, after := range snapshot(t) {
			if before[table] != after {
				t.Errorf("%s changed during a refused or idempotent request\nbefore: %s\nafter: %s", table, before[table], after)
			}
		}
	}
	newIntake := func(t *testing.T, state string, matchID int64) (starred.Breach, int64) {
		t.Helper()
		breach := starred.Breach{
			ListType: "A", StarredName: "Test Player", RuleReference: "3.5",
			Appearance: starred.Appearance{
				MatchID: matchID, SeasonYear: 2026,
				MatchDate: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
				ClubName:  "Intake Disposition Test Club", ClubKey: "intakedispositiontestclub",
				TeamName: "2nd XI", TeamLevel: 2, PlayerID: matchID + 100,
				PlayerName: "Test Player", PlayerKey: "testplayer", CompetitionType: "League",
			},
		}
		var intakeID int64
		if err := pool.QueryRow(ctx, `INSERT INTO sanction_intakes(origin,external_key,state,latest_revision,exception_message,updated_at)
			VALUES('starred_player',$1,$2,1,$3,'2026-09-06 12:00:00+00') RETURNING id`,
			starredFindingKey(breach), state, "Investigator's recorded disposition: "+state).Scan(&intakeID); err != nil {
			t.Fatal(err)
		}
		exec(t, `INSERT INTO sanction_intake_revisions(intake_id,revision,raw_data,raw_sha256)
			VALUES($1,1,'{"recorded":"original evidence"}',$2)`, intakeID, strings.Repeat("a", 64))
		if eventType := map[string]string{"ignored": "ignored", "duplicate": "marked_duplicate"}[state]; eventType != "" {
			exec(t, `INSERT INTO sanction_intake_events(intake_id,event_type,actor_label,reason,after_data)
				VALUES($1,$2,'Original investigator',$3,jsonb_build_object('state',$4::text))`,
				intakeID, eventType, "Original audited reason for "+state, state)
		}
		return breach, intakeID
	}
	for index, state := range []string{"ignored", "duplicate"} {
		t.Run(state+" refuses new case on repeated requests", func(t *testing.T) {
			breach, intakeID := newIntake(t, state, int64(880001+index))
			before := snapshot(t)
			for attempt := 0; attempt < 2; attempt++ {
				result, err := server.createStarredIneligibleCase(ctx, breach, nil, "Test investigator", "test-refused-intake")
				var disposition *starredCaseIntakeStateError
				if !errors.As(err, &disposition) || disposition.IntakeID != intakeID || disposition.State != state {
					t.Fatalf("attempt %d: expected %s intake refusal for %d, got result=%+v err=%v", attempt, state, intakeID, result, err)
				}
				if result.Created || result.CaseID != 0 {
					t.Fatalf("attempt %d created a case: %+v", attempt, result)
				}
				assertUnchanged(t, before)
			}
		})
		t.Run(state+" returns its linked closed case", func(t *testing.T) {
			breach, intakeID := newIntake(t, state, int64(880011+index))
			var caseID int64
			reference := fmt.Sprintf("TEST-CLOSED-%s", state)
			if err := pool.QueryRow(ctx, `INSERT INTO sanction_cases(reference,source_type,status,public_summary,closed_at)
				VALUES($1,'ineligible_player','closed','Already investigated',now()) RETURNING id`, reference).Scan(&caseID); err != nil {
				t.Fatal(err)
			}
			exec(t, `INSERT INTO sanction_intake_case_links(intake_id,case_id,relationship,reason) VALUES($1,$2,'primary','Original investigated case')`, intakeID, caseID)
			before := snapshot(t)
			for attempt := 0; attempt < 2; attempt++ {
				result, err := server.createStarredIneligibleCase(ctx, breach, nil, "Test investigator", "test-existing-case")
				want := starredCaseResult{IntakeID: intakeID, CaseID: caseID, Reference: reference}
				if err != nil || !reflect.DeepEqual(result, want) {
					t.Fatalf("attempt %d: result=%+v err=%v, want existing %+v", attempt, result, err, want)
				}
				assertUnchanged(t, before)
			}
		})
	}
	for index, state := range []string{"new", "reviewing"} {
		t.Run(state+" still reaches normal mapping", func(t *testing.T) {
			breach, intakeID := newIntake(t, state, int64(880021+index))
			before := snapshot(t)
			var initialRuns int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_intake_sync_runs`).Scan(&initialRuns); err != nil {
				t.Fatal(err)
			}
			result, err := server.createStarredIneligibleCase(ctx, breach, nil, "Test investigator", "test-normal-intake")
			var mapping *starredCaseExceptionError
			if !errors.As(err, &mapping) || !strings.Contains(err.Error(), "not present in the fixture cache") || result.IntakeID != intakeID {
				t.Fatalf("normal intake did not reach mapping: result=%+v err=%v", result, err)
			}
			var afterState, reason string
			var revision, revisionRows, runRows int
			if err := pool.QueryRow(ctx, `SELECT state,COALESCE(exception_message,''),latest_revision,
				(SELECT COUNT(*) FROM sanction_intake_revisions WHERE intake_id=$1),
				(SELECT COUNT(*) FROM sanction_intake_sync_runs)
				FROM sanction_intakes WHERE id=$1`, intakeID).Scan(&afterState, &reason, &revision, &revisionRows, &runRows); err != nil {
				t.Fatal(err)
			}
			if afterState != "exception" || !strings.Contains(reason, "not present in the fixture cache") || revision != 2 || revisionRows != 2 || runRows != initialRuns+1 {
				t.Fatalf("normal workflow state=%s reason=%s revision=%d revisionRows=%d runRows=%d", afterState, reason, revision, revisionRows, runRows)
			}
			after := snapshot(t)
			for _, table := range []string{"sanction_cases", "sanction_case_events", "sanction_intake_events", "sanction_intake_case_links", "sanction_decision_revisions", "sanction_effect_revisions"} {
				if after[table] != before[table] {
					t.Errorf("mapping exception unexpectedly changed %s", table)
				}
			}
		})
	}
}
