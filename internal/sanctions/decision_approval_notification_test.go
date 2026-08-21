package sanctions

import (
	"os"
	"strings"
	"testing"
)

func TestDecisionApprovalNotificationUsesPerRecipientKeyPrefix(t *testing.T) {
	key, subject, body := decisionApprovalNotification(42, 107)
	if key != "case:42:decision-approval-request:107:recipient:" {
		t.Fatalf("idempotency key = %q", key)
	}
	for _, want := range []string{"sanctions awaiting approval", "Awaiting decision", "approving or rejecting"} {
		if !strings.Contains(strings.ToLower(subject+"\n"+body), strings.ToLower(want)) {
			t.Fatalf("approval notification is missing %q:\n%s\n%s", want, subject, body)
		}
	}
	for _, sensitive := range []string{"GMCL-2026", "/admin/cases/", "Prepared by:"} {
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
		"NOT EXISTS(",
	} {
		if !strings.Contains(approvalSource, required) {
			t.Fatalf("peer-approval notification routing is missing %q", required)
		}
	}
}
