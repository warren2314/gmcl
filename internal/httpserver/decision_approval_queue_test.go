package httpserver

import (
	"os"
	"strings"
	"testing"
)

func TestDecisionApprovalDashboardMatchesApprovalRules(t *testing.T) {
	raw, err := os.ReadFile("admin_personal_dashboard.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"cases.source_type='ineligible_player' OR cases.proposed_by_admin_id IS DISTINCT FROM $1",
		"decision_sent_for_approval",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("decision dashboard routing is missing %q", required)
		}
	}
}

func TestSanctionOutboxRevokesStaleDecisionApprovalAlerts(t *testing.T) {
	raw, err := os.ReadFile("sanctions_operations.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"message_kind='decision_approval_request'",
		"cases.status<>'decision_proposed'",
		"decision_sent_for_approval",
		"revoked_at=now()",
		"Case is no longer awaiting independent decision approval",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("stale approval-alert cleanup is missing %q", required)
		}
	}
}
