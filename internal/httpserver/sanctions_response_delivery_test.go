package httpserver

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestResponseDeliveryDeadlinesUseCalendarDays(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	delivered := time.Date(2026, time.October, 23, 10, 30, 0, 0, london)
	reminderAt, dueAt := responseDeliveryDeadlines(delivered)

	if reminderAt.Day() != 28 || reminderAt.Hour() != delivered.Hour() || reminderAt.Location() != london {
		t.Fatalf("reminder deadline = %v, want five London calendar days at the same local time", reminderAt)
	}
	if dueAt.Day() != 30 || dueAt.Hour() != delivered.Hour() || dueAt.Location() != london {
		t.Fatalf("response deadline = %v, want seven London calendar days at the same local time", dueAt)
	}
	if reminderAt.Sub(delivered) != 121*time.Hour || dueAt.Sub(delivered) != 169*time.Hour {
		t.Fatalf("deadlines did not preserve local time across the autumn DST change: reminder=%v due=%v", reminderAt.Sub(delivered), dueAt.Sub(delivered))
	}
}

func TestOnlyQueuedInitialNoticeActivatesResponseWindow(t *testing.T) {
	if !shouldActivateResponseWindow("queued") {
		t.Fatal("successful delivery of the queued initial notice must activate the response window")
	}
	for _, status := range []string{"pending", "responded", "expired", "cancelled"} {
		if shouldActivateResponseWindow(status) {
			t.Fatalf("%s notice delivery would reset an established response deadline", status)
		}
	}
}

func TestResponseDeliveryWindowMigrationGuardsQueuedLifecycle(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0064_response_delivery_window.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(string(raw)), " "))
	for _, required := range []string{
		"add column if not exists delivered_at timestamptz",
		"add column if not exists reminder_correspondence_revision_id bigint",
		"alter column reminder_due_at drop not null",
		"alter column due_at drop not null",
		"status in ('queued','pending')",
		"foreign key(reminder_correspondence_revision_id,case_id)",
		"references sanction_correspondence_revisions(id,case_id)",
		"old.status='queued'",
		"new.status='pending'",
		"terminal response request is immutable",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("response-window migration is missing %q", required)
		}
	}
}

func TestResponseRequestCreationQueuesOnlyInitialNotice(t *testing.T) {
	raw, err := os.ReadFile("sanctions_cases.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(raw), "func (s *Server) handleAdminCaseRequestResponse()")
	if start < 0 {
		t.Fatal("could not find response-request handler")
	}
	end := strings.Index(string(raw)[start:], "func (s *Server) handleAdminCases()")
	if end < 0 {
		t.Fatal("could not isolate response-request handler")
	}
	handler := string(raw)[start : start+end]
	if got := strings.Count(handler, "INSERT INTO sanction_notification_outbox"); got != 1 {
		t.Fatalf("request creation queues %d outbox rows, want only the initial notice", got)
	}
	for _, required := range []string{"'response_request'", "'queued'", "reminder_correspondence_revision_id"} {
		if !strings.Contains(handler, required) {
			t.Errorf("response-request handler is missing %q", required)
		}
	}
	for _, forbidden := range []string{"now()+interval '5 days'", "now()+interval '7 days'"} {
		if strings.Contains(handler, forbidden) {
			t.Errorf("request creation starts a response deadline before delivery: %q", forbidden)
		}
	}
}

func TestOutboxWorkerLocksAndActivatesQueuedResponseRequest(t *testing.T) {
	raw, err := os.ReadFile("sanctions_operations.go")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(raw)), " ")
	for _, required := range []string{
		"FOR UPDATE OF outbox,request,token,cases",
		"SET status='pending',delivered_at=$2,reminder_due_at=$3,due_at=$4,reminder_queued_at=$2",
		"JOIN sanction_correspondence_revisions reminder ON reminder.id=request.reminder_correspondence_revision_id",
		"shouldActivateResponseWindow(responseRequestStatus)",
		"day-five reminder scheduled (not sent)",
		"response_reminder_sent",
		"Day-five club response reminder delivered",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("outbox delivery flow is missing %q", required)
		}
	}
}
