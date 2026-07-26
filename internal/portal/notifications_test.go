package portal

import (
	"strings"
	"testing"
	"time"
)

func TestSecurityNotificationTemplateAllowlist(t *testing.T) {
	tests := map[string]string{
		"portal.invitation.redeemed":     NotificationTemplateAccountActivated,
		"portal.role_assignment.revoked": NotificationTemplateAccessRevoked,
	}
	for eventType, expected := range tests {
		template, ok := securityNotificationTemplate(eventType)
		if !ok || template != expected {
			t.Fatalf("template for %q = %q, %v", eventType, template, ok)
		}
	}
	if template, ok := securityNotificationTemplate("portal.case.message"); ok || template != "" {
		t.Fatalf("case event reached security notification allowlist: %q, %v", template, ok)
	}
}

func TestNotificationRetryDelayIsBounded(t *testing.T) {
	expected := []time.Duration{
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
	}
	for index, want := range expected {
		if got := notificationRetryDelay(index + 1); got != want {
			t.Fatalf("retry %d delay = %v, want %v", index+1, got, want)
		}
	}
	if got := notificationRetryDelay(100); got != 32*time.Minute {
		t.Fatalf("bounded retry delay = %v", got)
	}
}

func TestTruncateNotificationError(t *testing.T) {
	if got := truncateNotificationError("  "); got != "delivery failed" {
		t.Fatalf("empty delivery error = %q", got)
	}
	value := strings.Repeat("é", 1001)
	if got := truncateNotificationError(value); len([]rune(got)) != 1000 {
		t.Fatalf("truncated error length = %d", len([]rune(got)))
	}
}
