package ineligible

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func rolloutDay(t *testing.T, date string, successful bool) effectiveReconciliationDate {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	return effectiveReconciliationDate{Date: parsed, Successful: successful}
}

func TestCleanScheduledDateStreakRequiresDistinctAdjacentDates(t *testing.T) {
	count, dates := cleanScheduledDateStreak([]effectiveReconciliationDate{
		rolloutDay(t, "2026-08-04", true),
		rolloutDay(t, "2026-08-03", true),
		rolloutDay(t, "2026-08-02", true),
		rolloutDay(t, "2026-08-01", true),
	})
	if count != 3 || strings.Join(dates, ",") != "2026-08-04,2026-08-03,2026-08-02" {
		t.Fatalf("unexpected streak: count=%d dates=%v", count, dates)
	}

	count, _ = cleanScheduledDateStreak([]effectiveReconciliationDate{
		rolloutDay(t, "2026-08-04", true),
		rolloutDay(t, "2026-08-02", true),
	})
	if count != 1 {
		t.Fatalf("a missing scheduled date must break the streak, got %d", count)
	}
}

func TestCleanScheduledDateStreakFailureDominatesDate(t *testing.T) {
	count, _ := cleanScheduledDateStreak([]effectiveReconciliationDate{
		rolloutDay(t, "2026-08-04", false),
		rolloutDay(t, "2026-08-03", true),
		rolloutDay(t, "2026-08-02", true),
	})
	if count != 0 {
		t.Fatalf("failed or partial latest date must reset the streak, got %d", count)
	}
}

func TestRolloutGateAllowsOnlyExplicitAdminBootstrap(t *testing.T) {
	adminID := int64(9)
	pending := RolloutStatus{State: "pending"}
	if err := rolloutGateError(pending, Trigger{Type: "admin", AdminID: &adminID}, true); err != nil {
		t.Fatalf("explicit admin bootstrap was blocked: %v", err)
	}
	for _, trigger := range []Trigger{{Type: "n8n"}, {Type: "admin", AdminID: &adminID}, {Type: "system"}} {
		bootstrap := trigger.Type == "n8n"
		if err := rolloutGateError(pending, trigger, bootstrap); !errors.Is(err, ErrBackfillPrerequisite) {
			t.Fatalf("trigger %+v bootstrap=%t: got %v, want prerequisite error", trigger, bootstrap, err)
		}
	}

	applicationID := int64(42)
	retired := RolloutStatus{PrerequisiteApplicationID: &applicationID, State: "native_active_google_retired"}
	if err := rolloutGateError(retired, Trigger{Type: "n8n"}, false); !errors.Is(err, ErrGoogleImportRetired) {
		t.Fatalf("retired scheduler: got %v", err)
	}
	if err := rolloutGateError(retired, Trigger{Type: "admin", AdminID: &adminID}, false); err != nil {
		t.Fatalf("manual reconciliation should remain available after retirement: %v", err)
	}
}

func TestRolloutMigrationIsAppendOnlyAndFailureDominant(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0060_ineligible_rollout_gate.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"sanction_ineligible_scheduled_reconciliations",
		"sanction_ineligible_rollout_activations",
		"reject_immutable_sanction_change()",
		"google_grace_until = activated_at + interval '30 days'",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("rollout migration missing %q", required)
		}
	}
}
