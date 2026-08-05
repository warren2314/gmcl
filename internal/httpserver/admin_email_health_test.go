package httpserver

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
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

func TestValidSESSNSWebhookTokenFailsClosedWhenUnconfigured(t *testing.T) {
	t.Setenv("SES_SNS_WEBHOOK_TOKEN", "")
	t.Setenv("SES_SNS_WEBHOOK_TOKEN_NEXT", "")

	if validSESSNSWebhookToken("") {
		t.Fatal("unconfigured webhook token must fail closed")
	}
}

func TestCanonicalSNSNotification(t *testing.T) {
	canonical, err := canonicalSNSMessage(snsEnvelope{
		Type: "Notification", Message: "payload", MessageID: "message-1",
		Timestamp: "2026-08-04T03:30:00.000Z", TopicARN: "arn:aws:sns:eu-west-2:123:gmcl", Subject: "subject",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Message\npayload\nMessageId\nmessage-1\nSubject\nsubject\nTimestamp\n2026-08-04T03:30:00.000Z\nTopicArn\narn:aws:sns:eu-west-2:123:gmcl\nType\nNotification\n"
	if canonical != want {
		t.Fatalf("canonical message:\n%s\nwant:\n%s", canonical, want)
	}
}

func TestVerifySNSCanonicalSignatureSupportsProtocolVersions(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	canonical := "Message\npayload\nMessageId\nmessage-1\n"
	for _, version := range []string{"1", "2"} {
		t.Run("version_"+version, func(t *testing.T) {
			var hash crypto.Hash
			var digest []byte
			if version == "1" {
				sum := sha1.Sum([]byte(canonical))
				hash, digest = crypto.SHA1, sum[:]
			} else {
				sum := sha256.Sum256([]byte(canonical))
				hash, digest = crypto.SHA256, sum[:]
			}
			signature, signErr := rsa.SignPKCS1v15(rand.Reader, privateKey, hash, digest)
			if signErr != nil {
				t.Fatal(signErr)
			}
			if err := verifySNSCanonicalSignature(&privateKey.PublicKey, version, canonical, signature); err != nil {
				t.Fatalf("verify legitimate SNS version %s signature: %v", version, err)
			}
			if err := verifySNSCanonicalSignature(&privateKey.PublicKey, version, canonical+"tampered", signature); err == nil {
				t.Fatalf("tampered SNS version %s message was accepted", version)
			}
		})
	}
	if err := verifySNSCanonicalSignature(&privateKey.PublicKey, "3", canonical, []byte("signature")); err == nil {
		t.Fatal("unsupported SNS signature version was accepted")
	}
}

func TestValidateSNSWebhookEnvelopeRequiresConfiguredTopic(t *testing.T) {
	t.Setenv("SES_SNS_TOPIC_ARN", "")
	if err := validateSNSWebhookEnvelope(context.Background(), snsEnvelope{}, "sns_raw"); err == nil {
		t.Fatal("missing topic ARN should fail closed")
	}
	t.Setenv("SES_SNS_TOPIC_ARN", "arn:aws:sns:eu-west-2:123:gmcl")
	t.Setenv("SES_SNS_ALLOW_RAW", "1")
	if err := validateSNSWebhookEnvelope(context.Background(), snsEnvelope{TopicARN: "arn:aws:sns:eu-west-2:999:attacker"}, "sns_raw"); err == nil {
		t.Fatal("unexpected topic ARN should be rejected")
	}
}

func TestSanctionAttemptStatusForSESEvent(t *testing.T) {
	for eventType, want := range map[string]string{
		"bounce": "bounced", "complaint": "complained", "reject": "failed", "rendering_failure": "failed", "delivery": "",
	} {
		if got := sanctionAttemptStatusForSESEvent(eventType); got != want {
			t.Fatalf("status for %s = %q, want %q", eventType, got, want)
		}
	}
}
