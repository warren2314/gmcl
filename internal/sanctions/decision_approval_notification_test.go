package sanctions

import (
	"os"
	"strings"
	"testing"
)

func TestDecisionApprovalNotificationUsesPerRecipientKeyPrefix(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://gmcl.example.test/")
	key, subject, body := decisionApprovalNotification(42, 107, "GMCL-2026-0042")
	if key != "case:42:decision-approval-request:107:recipient:" {
		t.Fatalf("idempotency key = %q", key)
	}
	for _, want := range []string{"GMCL-2026-0042 awaiting approval", "Case reference: GMCL-2026-0042", "Waiting for: Independent approval", "What to review:", "https://gmcl.example.test/admin/cases/42", "another authorised administrator may already have actioned it"} {
		if !strings.Contains(strings.ToLower(subject+"\n"+body), strings.ToLower(want)) {
			t.Fatalf("approval notification is missing %q:\n%s\n%s", want, subject, body)
		}
	}
	for _, sensitive := range []string{"Prepared by:", "Player name:", "Reporting club:"} {
		if strings.Contains(subject+"\n"+body, sensitive) {
			t.Fatalf("generic approval notification contains sensitive case detail %q", sensitive)
		}
	}
}

func TestDecisionApprovalRequestsGoToPeerApproversNotFinalIssuer(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	approvalSource := string(source)
	for _, required := range []string{
		"admin.id<>$6",
		"permission.permission='sanctions_approve'",
		"recipient.recipient_role='play_cricket'",
		"$7='ineligible_player' OR $8::integer IS DISTINCT FROM admin.id",
		"NOT EXISTS(",
	} {
		if !strings.Contains(approvalSource, required) {
			t.Fatalf("peer-approval notification routing is missing %q", required)
		}
	}
}
