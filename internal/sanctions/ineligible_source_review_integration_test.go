package sanctions

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Temporary tables on a single test-only connection exercise the same source
// review definition through both its detail service and correlated list form.
func TestApprovedIneligibleSourceReviewMatchesDetailAndListPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DB_DSN"))
	if dsn == "" {
		t.Skip("TEST_DB_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TEMP TABLE sanction_cases(id bigint PRIMARY KEY,source_type text NOT NULL DEFAULT 'ineligible_player',
			status text NOT NULL DEFAULT 'approved',public_status text NOT NULL DEFAULT 'unpublished',published_at timestamptz);
		CREATE TEMP TABLE sanction_intakes(id bigint PRIMARY KEY,state text NOT NULL DEFAULT 'linked',latest_revision integer DEFAULT 1);
		CREATE TEMP TABLE sanction_intake_revisions(id bigint PRIMARY KEY,intake_id bigint,revision integer);
		CREATE TEMP TABLE sanction_intake_case_links(case_id bigint,intake_id bigint,relationship text NOT NULL DEFAULT 'primary');
		CREATE TEMP TABLE sanction_case_intake_merge_resolutions(id bigint PRIMARY KEY,case_id bigint,intake_id bigint,relationship text,revision_id bigint);
		INSERT INTO sanction_cases(id) SELECT generate_series(1,12);
		INSERT INTO sanction_intakes(id) SELECT generate_series(1,12);
		INSERT INTO sanction_intake_revisions(id,intake_id,revision) SELECT 100+id,id,1 FROM sanction_intakes;
		INSERT INTO sanction_intake_case_links(case_id,intake_id) SELECT id,id FROM sanction_cases WHERE id<>9;
		INSERT INTO sanction_case_intake_merge_resolutions(id,case_id,intake_id,relationship,revision_id)
			SELECT id,id,id,'primary',100+id FROM sanction_cases WHERE id<>9;
		-- A new revision without a corresponding merge requires source review.
		UPDATE sanction_intakes SET latest_revision=2 WHERE id IN(2,12);
		INSERT INTO sanction_intake_revisions VALUES(202,2,2),(212,12,2);
		-- Duplicate links use their own intake state and relationship resolution.
		UPDATE sanction_intake_case_links SET relationship='duplicate' WHERE case_id IN(3,4);
		UPDATE sanction_case_intake_merge_resolutions SET relationship='duplicate' WHERE case_id IN(3,4);
		UPDATE sanction_intakes SET state='duplicate' WHERE id=4;
		-- Case state limits source review even when linked intake data is stale.
		UPDATE sanction_intakes SET state='exception' WHERE id IN(5,6,7,8,11);
		UPDATE sanction_cases SET source_type='manual' WHERE id=5;
		UPDATE sanction_cases SET status='investigating' WHERE id=6;
		UPDATE sanction_cases SET status='published',public_status='active' WHERE id=7;
		UPDATE sanction_cases SET published_at='2026-10-01 12:00:00+00' WHERE id=8;
		-- A different relationship's newer resolution cannot replace the primary one.
		INSERT INTO sanction_case_intake_merge_resolutions VALUES(310,10,10,'supporting',999);
		-- The latest resolution for a relationship controls, rather than any old match.
		INSERT INTO sanction_case_intake_merge_resolutions VALUES(312,12,12,'primary',212),(412,12,12,'primary',112);
	`)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{2, 3, 11, 12}
	rows, err := pool.Query(ctx, `SELECT c.id FROM sanction_cases c WHERE `+ApprovedIneligibleSourceReviewPredicate()+` ORDER BY c.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source review cases=%v, want %v", got, want)
	}
	service := NewService(&db.Pool{Pool: pool})
	for id := int64(1); id <= 12; id++ {
		needsReview, err := service.ApprovedIneligibleCaseNeedsReopen(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		wantReview := id == 2 || id == 3 || id == 11 || id == 12
		if needsReview != wantReview {
			t.Errorf("detail source-review state for case %d=%v, want %v", id, needsReview, wantReview)
		}
	}
}
