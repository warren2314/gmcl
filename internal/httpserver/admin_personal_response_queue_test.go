package httpserver

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPersonalResponseQueueKeepsFinishedCasesOutPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DB_DSN"))
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

	// Session-local tables exercise the exact dashboard query without changing
	// persistent cases, replies or schema. All cases deliberately share a name.
	if _, err := conn.Exec(ctx, `
		CREATE TEMP TABLE clubs (id integer PRIMARY KEY,name text);
		CREATE TEMP TABLE sanction_cases (
			id bigint PRIMARY KEY,reference text NOT NULL,
			player_name text NOT NULL DEFAULT 'Example Player',
			club_id integer NOT NULL DEFAULT 1,status text NOT NULL,
			assigned_admin_id integer NOT NULL DEFAULT 7,is_test boolean NOT NULL DEFAULT false
		);
		CREATE TEMP TABLE sanction_case_events (
			id bigint PRIMARY KEY,case_id bigint NOT NULL,event_type text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT '2026-09-10 12:00:00+00',
			metadata jsonb NOT NULL DEFAULT '{}'
		);
		INSERT INTO clubs VALUES(1,'Example Club');
		INSERT INTO sanction_cases(id,reference,status) VALUES
			(1,'CASE-1','investigating'),(2,'CASE-2','response_pending'),
			(3,'CASE-3','approved'),(4,'CASE-4','appealed'),
			(5,'CASE-5','published'),(6,'CASE-6','closed'),
			(7,'CASE-7','rejected'),(8,'CASE-8','withdrawn'),
			(9,'CASE-9','investigating'),(10,'CASE-10','investigating'),
			(11,'CASE-11','investigating'),(12,'CASE-12','investigating');
		UPDATE sanction_cases SET assigned_admin_id=8 WHERE id=10;
		UPDATE sanction_cases SET is_test=true WHERE id=11;
		INSERT INTO sanction_case_events(id,case_id,event_type)
			SELECT id,id,'party_response' FROM sanction_cases;
		INSERT INTO sanction_case_events(id,case_id,event_type,metadata) VALUES
			(90,9,'response_reviewed','{"response_event_id":"9"}'),
			(91,12,'case_training_designated','{}');
	`); err != nil {
		t.Fatal(err)
	}
	assertQueue := func(limit int, want []int64, wantTotal int64) {
		t.Helper()
		rows, err := conn.Query(ctx, personalResponsesAwaitingReviewQuery, int32(7), limit)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for rows.Next() {
			var item personalWorkResponse
			var total int64
			if err := rows.Scan(&item.CaseID, &item.Reference, &item.Player, &item.Club, &item.ReceivedAt, &total); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if total != wantTotal {
				rows.Close()
				t.Fatalf("response count=%d, want %d", total, wantTotal)
			}
			got = append(got, item.CaseID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("response cases=%v, want %v", got, want)
		}
	}
	assertQueue(20, []int64{4, 3, 2, 1}, 4)
	assertQueue(2, []int64{4, 3}, 4)
	if _, err := conn.Exec(ctx, `UPDATE sanction_cases SET status='closed' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	assertQueue(20, []int64{4, 3, 2}, 3)
	if _, err := conn.Exec(ctx, `INSERT INTO sanction_case_events(id,case_id,event_type,created_at)
		VALUES(93,1,'external_response_recorded','2026-09-10 12:02:00+00')`); err != nil {
		t.Fatal(err)
	}
	assertQueue(20, []int64{4, 3, 2}, 3)
	var status string
	var replyCount int
	if err := conn.QueryRow(ctx, `SELECT status,(SELECT COUNT(*) FROM sanction_case_events WHERE case_id=1)
		FROM sanction_cases WHERE id=1`).Scan(&status, &replyCount); err != nil {
		t.Fatal(err)
	}
	if status != "closed" || replyCount != 2 {
		t.Fatalf("history was changed: status=%q replies=%d", status, replyCount)
	}
	if _, err := conn.Exec(ctx, `UPDATE sanction_cases SET status='investigating' WHERE id=1;
		INSERT INTO sanction_case_events(id,case_id,event_type,created_at)
		VALUES(95,9,'external_response_recorded','2026-09-10 12:03:00+00')`); err != nil {
		t.Fatal(err)
	}
	assertQueue(20, []int64{9, 1, 4, 3, 2}, 5)
	if _, err := conn.Exec(ctx, `INSERT INTO sanction_case_events(id,case_id,event_type,metadata)
		VALUES(96,9,'response_reviewed','{"response_event_id":"95"}')`); err != nil {
		t.Fatal(err)
	}
	assertQueue(20, []int64{1, 4, 3, 2}, 4)
	if _, err := conn.Exec(ctx, `UPDATE sanction_cases SET status='closed' WHERE id IN (1,2,3,4)`); err != nil {
		t.Fatal(err)
	}
	assertQueue(20, nil, 0)
}
