package httpserver

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAdminCaseResponseWindowShowsAuditedEarlierDateCorrection(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, time.August, 29, 10, 15, 0, 0, london)
	reminder, due := responseDeliveryDeadlines(started)
	html := adminCaseResponseWindowHTML(77, "token", adminCaseResponseWindowView{
		ID:            12,
		Status:        "pending",
		DeliveredAt:   &started,
		ReminderDueAt: &reminder,
		DueAt:         &due,
	}, "response_pending", london)
	for _, want := range []string{
		"Club response clock",
		"Response due",
		"29 Aug 2026 10:15",
		"03 Sep 2026 10:15",
		"05 Sep 2026 10:15",
		`action="/admin/cases/77/response-window/correct"`,
		`name="started_at"`,
		"The date can only move earlier",
		"moves to <strong>Response overdue</strong> immediately",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("response-window panel missing %q: %s", want, html)
		}
	}
}

func TestAdminCaseExpiredResponseWindowHasNoCorrectionForm(t *testing.T) {
	due := time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC)
	html := adminCaseResponseWindowHTML(77, "token", adminCaseResponseWindowView{ID: 12, Status: "expired", DueAt: &due}, "investigating", time.UTC)
	if !strings.Contains(html, "Response overdue") {
		t.Fatalf("expired response window is not labelled overdue: %s", html)
	}
	if strings.Contains(html, "/response-window/correct") {
		t.Fatalf("expired response window still offers correction: %s", html)
	}
}

func TestParseAdminResponseWindowStartUsesLondonWallClock(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseAdminResponseWindowStart("2026-08-24T09:16", london)
	if err != nil {
		t.Fatal(err)
	}
	if got.Location() != london || got.Hour() != 9 || got.UTC().Hour() != 8 {
		t.Fatalf("parsed response-window start = %v, want 09:16 Europe/London during BST", got)
	}
	if _, err := parseAdminResponseWindowStart("24/08/2026 09:16", london); err == nil {
		t.Fatal("invalid browser datetime value was accepted")
	}
}

func TestResponseWindowCorrectionMigrationPreservesAuditAndImmutability(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0085_audited_response_window_corrections.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(string(raw)), " "))
	for _, want := range []string{
		"add column if not exists window_corrected_at timestamptz",
		"add column if not exists window_corrected_by_admin_id integer",
		"add column if not exists window_correction_reason text",
		"new.delivered_at>=old.delivered_at",
		"delivered response window correction is invalid",
		"terminal response request is immutable",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("response-window correction migration missing %q", want)
		}
	}
}

func TestResponseWindowCorrectionImmediatelyExpiresPastDeadline(t *testing.T) {
	raw, err := os.ReadFile("sanctions_response_window_correction.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"madeOverdue := !newDueAt.After(correctedAt)",
		"SET status='expired',closed_at=$2",
		"revoked_at=CASE WHEN $3::boolean",
		"'response_overdue'",
		`nextStatus = "investigating"`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("overdue correction flow missing %q", want)
		}
	}
}
