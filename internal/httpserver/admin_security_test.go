package httpserver

import "testing"

func TestInternalWorkerAuthConfigurationUsesEffectiveSecret(t *testing.T) {
	t.Setenv("INTERNAL_HMAC_SECRET", "legacy-value-that-is-not-used-by-the-verifier")
	t.Setenv("N8N_HMAC_SECRET", "")
	if internalWorkerAuthConfigured() {
		t.Fatal("legacy-only HMAC variable was reported as configured")
	}
	t.Setenv("N8N_HMAC_SECRET", "short")
	if internalWorkerAuthConfigured() {
		t.Fatal("weak HMAC secret was reported as configured")
	}
	t.Setenv("N8N_HMAC_SECRET", "01234567890123456789012345678901")
	if !internalWorkerAuthConfigured() {
		t.Fatal("effective 32-byte HMAC secret was not reported as configured")
	}
}

func TestSMTPSecurityDeliveryConfiguration(t *testing.T) {
	t.Setenv("SMTP_HOST", "")
	if smtpSecurityDeliveryConfigured() {
		t.Fatal("missing SMTP host was reported as configured")
	}
	t.Setenv("SMTP_HOST", "smtp.example")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_FROM", "GMCL <portal@example.test>")
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("SMTP_REPLY_TO", "")
	if !smtpSecurityDeliveryConfigured() {
		t.Fatal("valid unauthenticated SMTP relay was not reported as configured")
	}
}
