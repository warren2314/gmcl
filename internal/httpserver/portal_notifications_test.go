package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/portal"

	"github.com/google/uuid"
)

func TestRenderPortalSecurityNotificationUsesAllowlistedPayload(t *testing.T) {
	account := portal.PendingNotification{
		ID:          uuid.New(),
		TemplateKey: portal.NotificationTemplateAccountActivated,
		Payload: map[string]any{
			"club_name":     "Example & Cricket Club",
			"internal_note": "must never appear",
			"token":         "must never appear",
		},
	}
	subject, body, err := renderPortalSecurityNotification(
		account,
		"https://portal.example.test/",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subject, "account is active") ||
		!strings.Contains(body, "Example & Cricket Club") ||
		!strings.Contains(body, "https://portal.example.test/portal/sessions") {
		t.Fatalf("unexpected account notification: %q / %q", subject, body)
	}
	for _, forbidden := range []string{"must never appear", "internal_note", "token"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account notification leaked %q: %s", forbidden, body)
		}
	}

	revoked := portal.PendingNotification{
		ID:          uuid.New(),
		TemplateKey: portal.NotificationTemplateAccessRevoked,
		Payload: map[string]any{
			"club_name": "Example Cricket Club",
			"role":      string(portal.RoleClubPrimaryAdmin),
			"reason":    "private administrative reason",
		},
	}
	subject, body, err = renderPortalSecurityNotification(
		revoked,
		"https://portal.example.test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subject, "access changed") ||
		!strings.Contains(body, "Club Primary Administrator") ||
		strings.Contains(body, "private administrative reason") {
		t.Fatalf("unexpected revocation notification: %q / %q", subject, body)
	}
}

func TestRenderPortalSecurityNotificationRejectsUnknownTemplate(t *testing.T) {
	_, _, err := renderPortalSecurityNotification(portal.PendingNotification{
		TemplateKey: "case_message",
		Payload: map[string]any{
			"club_name": "Example Cricket Club",
		},
	}, "https://portal.example.test")
	if err == nil {
		t.Fatal("unknown portal notification template was accepted")
	}
	_, _, err = renderPortalSecurityNotification(portal.PendingNotification{
		TemplateKey: portal.NotificationTemplateAccountActivated,
		Payload: map[string]any{
			"club_name": "Example Cricket Club",
		},
	}, "http://portal.example.test")
	if err == nil {
		t.Fatal("non-HTTPS portal notification URL was accepted")
	}
}

func TestRenderAdminPortalNotificationHealthEscapesErrors(t *testing.T) {
	output := httptest.NewRecorder()
	oldest := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	renderAdminPortalNotificationHealth(
		output,
		portal.NotificationDeliveryHealth{
			UnpublishedEvents: 1,
			OutboxDeadLetter:  1,
			Pending:           2,
			Retrying:          3,
			Sending:           4,
			Sent:              5,
			DeadLetter:        2,
			OldestReadyAt:     &oldest,
			LatestError:       `<script>alert("delivery")</script>`,
		},
		false,
		time.UTC,
	)
	body := output.Body.String()
	for _, expected := range []string{
		"SMTP unavailable",
		"Operator action required",
		"Unpublished events",
		"Dead letter",
		"&lt;script&gt;",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("notification health omitted %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Fatalf("notification health did not escape the delivery error: %s", body)
	}
}
