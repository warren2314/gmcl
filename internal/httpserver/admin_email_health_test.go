package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestDecodeSESWebhookConfigurationSetEvent(t *testing.T) {
	body := []byte(`{
		"Type":"Notification",
		"MessageId":"sns-123",
		"TopicArn":"arn:aws:sns:eu-west-2:123456789012:gmcl-ses-events",
		"Message":"{\"eventType\":\"Bounce\",\"mail\":{\"messageId\":\"ses-456\",\"destination\":[\"captain@example.com\"]},\"bounce\":{\"bounceType\":\"Transient\",\"bounceSubType\":\"MailboxFull\",\"bouncedRecipients\":[{\"emailAddress\":\"captain@example.com\",\"diagnosticCode\":\"smtp; 452 mailbox full\"}]}}"
	}`)

	env, event, mode, err := decodeSESWebhook(body, http.Header{})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mode != "sns_wrapped" || env.MessageID != "sns-123" || sesEventType(event) != "bounce" {
		t.Fatalf("unexpected decoded event: mode=%q env=%+v event=%+v", mode, env, event)
	}
	if event.Bounce.BounceType != "Transient" || event.Bounce.BouncedRecipients[0].EmailAddress != "captain@example.com" {
		t.Fatalf("soft bounce details not decoded: %+v", event.Bounce)
	}
}

func TestDecodeSESWebhookRawLegacyNotification(t *testing.T) {
	header := http.Header{}
	header.Set("x-amz-sns-message-id", "sns-raw-1")
	header.Set("x-amz-sns-topic-arn", "arn:aws:sns:eu-west-2:123456789012:gmcl-ses-events")
	body := []byte(`{"notificationType":"Delivery","mail":{"messageId":"ses-1","destination":["captain@example.com"]},"delivery":{"recipients":["captain@example.com"]}}`)

	env, event, mode, err := decodeSESWebhook(body, header)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mode != "sns_raw" || env.MessageID != "sns-raw-1" || sesEventType(event) != "delivery" {
		t.Fatalf("unexpected raw event: mode=%q env=%+v event=%+v", mode, env, event)
	}
}

func TestValidSNSSubscribeURL(t *testing.T) {
	valid := "https://sns.eu-west-2.amazonaws.com/?Action=ConfirmSubscription&TopicArn=arn%3Aaws%3Asns%3Aeu-west-2%3A123%3Atopic&Token=abc"
	if !validSNSSubscribeURL(valid) {
		t.Fatal("expected Amazon SNS confirmation URL to be accepted")
	}
	for _, raw := range []string{
		"http://sns.eu-west-2.amazonaws.com/?Action=ConfirmSubscription",
		"https://example.com/?Action=ConfirmSubscription",
		"https://sns.eu-west-2.amazonaws.com/?Action=DeleteTopic",
	} {
		if validSNSSubscribeURL(raw) {
			t.Fatalf("unsafe confirmation URL accepted: %s", raw)
		}
	}
}

func TestDecodeSESWebhookRejectsMissingEventType(t *testing.T) {
	_, _, _, err := decodeSESWebhook([]byte(`{"mail":{"messageId":"ses-1"}}`), http.Header{})
	if err == nil || !strings.Contains(err.Error(), "no eventType") {
		t.Fatalf("expected missing event type error, got %v", err)
	}
}

func TestValidSESSNSWebhookTokenSupportsRotation(t *testing.T) {
	t.Setenv("SES_SNS_WEBHOOK_TOKEN", "current-token")
	t.Setenv("SES_SNS_WEBHOOK_TOKEN_NEXT", "replacement-token")

	if !validSESSNSWebhookToken("current-token") {
		t.Fatal("current webhook token should remain valid during rotation")
	}
	if !validSESSNSWebhookToken("replacement-token") {
		t.Fatal("replacement webhook token should be valid during rotation")
	}
	if validSESSNSWebhookToken("wrong-token") {
		t.Fatal("unexpected webhook token accepted")
	}
}

func TestValidSESSNSWebhookTokenCanBeUnconfigured(t *testing.T) {
	t.Setenv("SES_SNS_WEBHOOK_TOKEN", "")
	t.Setenv("SES_SNS_WEBHOOK_TOKEN_NEXT", "")

	if !validSESSNSWebhookToken("") {
		t.Fatal("unconfigured webhook should preserve existing open behaviour")
	}
}
