package sanctions

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
)

func TestRestoreDenverFinalSignOffMigration(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.New(ctx, os.Getenv("TEST_DB_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	migration, err := os.ReadFile("../../migrations/0090_restore_denver_final_sign_off.sql")
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	tests := []struct {
		username string
		active   bool
		linked   bool
		want     bool
		id       int32
		email    string
	}{
		{username: " DeNvEr ", active: true, linked: true, want: true},
		{username: " DeNvErThOrNtOn ", active: true, linked: true, want: true},
		{username: "  denver  ", active: false, linked: true},
		{username: "   denver   ", active: true, linked: false},
		{username: fmt.Sprintf("stuart-%d", stamp), active: true, linked: true},
	}
	for i := range tests {
		tc := &tests[i]
		tc.id = int32(1_000_000_000 + stamp%100_000_000 + int64(i))
		tc.email = fmt.Sprintf("final-issuer-%d-%d@example.invalid", stamp, i)
		if _, err := tx.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,email,role,is_active) VALUES($1,$2,'test'::bytea,$3,'admin',$4)`, tc.id, tc.username, tc.email, tc.active); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'sanctions_publish')`, tc.id); err != nil {
			t.Fatal(err)
		}
		if tc.linked {
			if _, err := tx.Exec(ctx, `INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES('play_cricket','Migration regression',$1)`, " "+strings.ToUpper(tc.email)+" "); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The repair must be safe to rerun and must not nominate copy recipients,
	// inactive accounts or accounts without a matching Play-Cricket entry.
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	var caseID, decisionID int64
	if err := tx.QueryRow(ctx, `INSERT INTO sanction_cases(source_type,status) VALUES('ineligible_player','approved') RETURNING id`).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,status,public_reason) VALUES($1,1,'proposed','Migration regression') RETURNING id`, caseID).Scan(&decisionID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := queueFinalSignOffNotification(ctx, tx, caseID, decisionID); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range tests {
		var allowed, granted bool
		var queued int
		if err := tx.QueryRow(ctx, `SELECT sanction_ineligible_final_issuer($1),EXISTS(SELECT 1 FROM admin_user_permissions WHERE admin_user_id=$1 AND permission='sanctions_ineligible_issue'),(SELECT COUNT(*) FROM sanction_notification_outbox WHERE case_id=$2 AND recipient=$3 AND message_kind='final_sign_off_request')`, tc.id, caseID, tc.email).Scan(&allowed, &granted, &queued); err != nil {
			t.Fatal(err)
		}
		wantQueued := 0
		if tc.want {
			wantQueued = 1
		}
		if allowed != tc.want || granted != tc.want || queued != wantQueued {
			t.Errorf("%q: eligible=%v permission=%v requests=%d; want eligible=%v requests=%d", tc.username, allowed, granted, queued, tc.want, wantQueued)
		}
	}
}
