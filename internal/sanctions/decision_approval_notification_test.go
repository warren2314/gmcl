package sanctions

import (
	"strings"
	"testing"
)

func TestDecisionApprovalNotificationTargetsCricketDirectorOncePerRevision(t *testing.T) {
	recipient, key, subject, body := decisionApprovalNotification(42, 107)
	if recipient != "cricketdirector@gtrmcrcricket.co.uk" {
		t.Fatalf("recipient = %q", recipient)
	}
	if key != "case:42:decision-approval-request:107" {
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
