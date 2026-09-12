package sanctions

import (
	"strings"
	"testing"
)

func TestFinalSignOffNotificationExplainsDenverHandover(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://gmcl.example.test/")
	key, subject, body := finalSignOffNotification(42, 108, "GMCL-2026-0042")
	if key != "case:42:final-sign-off-request:108:recipient:" {
		t.Fatalf("key=%s", key)
	}
	for _, want := range []string{"ready for final sign-off", "Denver's final sign-off and issue", "emails and letters are locked", "https://gmcl.example.test/admin/cases/42", "https://gmcl.example.test/admin#my-decisions", "case owner, Dave and Warren do not need to act"} {
		if !strings.Contains(subject+"\n"+body, want) {
			t.Fatalf("notification missing %q", want)
		}
	}
}
