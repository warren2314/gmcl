package httpserver

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
)

func TestAdminCloseCaseNoActionHTML(t *testing.T) {
	owner, other := int32(7), int32(8)
	html := adminCloseCaseNoActionHTML(42, `token"value`, "investigating", false, &owner, &owner)
	for _, want := range []string{"/admin/cases/42/close-no-action", "Close case with no action", "goes straight to", "token&quot;value", "no sanction, approval request or outcome letter"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in %s", want, html)
		}
	}
	for _, test := range []struct {
		status      string
		hasProposed bool
		assigned    *int32
		actor       *int32
	}{
		{status: "approved", assigned: &owner, actor: &owner},
		{status: "investigating", assigned: &other, actor: &owner},
		{status: "investigating", assigned: nil, actor: &owner},
	} {
		if got := adminCloseCaseNoActionHTML(42, "csrf", test.status, test.hasProposed, test.assigned, test.actor); got != "" {
			t.Fatalf("unexpected close control for %+v: %s", test, got)
		}
	}
	for _, test := range []struct {
		status      string
		hasProposed bool
	}{
		{status: "decision_proposed", hasProposed: true},
		{status: "investigating", hasProposed: true},
	} {
		if got := adminCloseCaseNoActionHTML(42, "csrf", test.status, test.hasProposed, &owner, &owner); got == "" {
			t.Fatalf("missing close control for %+v", test)
		}
	}
}

func TestCloseCaseNoActionErrorMessageIdentifiesFailedStage(t *testing.T) {
	message := closeCaseNoActionErrorMessage("cancel_response_request", "request-123")
	for _, want := range []string{"case was not changed", "response window", "request-123"} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in %q", want, message)
		}
	}
}

func TestCloseCaseNoActionErrorMessageDoesNotRequireRequestID(t *testing.T) {
	message := closeCaseNoActionErrorMessage("unexpected", " ")
	if !strings.Contains(message, "case was not changed") {
		t.Fatalf("unexpected message %q", message)
	}
	if strings.Contains(message, "support reference") {
		t.Fatalf("unexpected empty support reference in %q", message)
	}
}

func TestCloseCaseFollowUpCancellationBindsNoteAsText(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	var caseID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_cases(source_type,status,public_summary)
		VALUES('manual','investigating','Follow-up cancellation parameter test') RETURNING id`).Scan(&caseID); err != nil {
		t.Fatalf("insert case: %v", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,current_note)
		VALUES($1,'investigation_support','Existing note')`, caseID); err != nil {
		t.Fatalf("insert follow-up task: %v", err)
	}

	note := "Cancelled because the case was closed with no action: duplicate identity confirmed"
	result, err := tx.Exec(ctx, cancelOpenCaseFollowUpTasksSQL, caseID, note)
	if err != nil {
		t.Fatalf("cancel follow-up task with bound note: %v", err)
	}
	if got := result.RowsAffected(); got != 1 {
		t.Fatalf("cancelled tasks = %d, want 1", got)
	}
	var status, currentNote string
	if err = tx.QueryRow(ctx, `SELECT status,current_note FROM sanction_follow_up_tasks WHERE case_id=$1`, caseID).Scan(&status, &currentNote); err != nil {
		t.Fatalf("load cancelled task: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", status)
	}
	if want := "Existing note\n" + note; currentNote != want {
		t.Fatalf("current note = %q, want %q", currentNote, want)
	}
}
