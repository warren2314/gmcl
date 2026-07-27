package email

import (
	"strings"
	"testing"
)

func TestMessageHeadersIncludeConfiguredReplyTo(t *testing.T) {
	client := &Client{
		fromHeader: "GMCL <webmaster@gmcl.co.uk>",
		replyTo:    "GMCL Match Reports <matchreports@gtrmcrcricket.co.uk>",
	}
	headers, err := client.messageHeaders("captain@example.com", "Rule 3.5 review")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"From: GMCL <webmaster@gmcl.co.uk>\r\n",
		"To: captain@example.com\r\n",
		"Reply-To: \"GMCL Match Reports\" <matchreports@gtrmcrcricket.co.uk>\r\n",
	} {
		if !strings.Contains(headers, want) {
			t.Fatalf("headers do not contain %q: %s", want, headers)
		}
	}
}

func TestMessageHeadersRejectInvalidReplyTo(t *testing.T) {
	client := &Client{fromHeader: "webmaster@gmcl.co.uk", replyTo: "not an address"}
	if _, err := client.messageHeaders("captain@example.com", "Subject"); err == nil {
		t.Fatal("invalid reply-to address was accepted")
	}
}

func TestSendSensitiveRequiresSMTP(t *testing.T) {
	client := &Client{}
	if client.SensitiveDeliveryConfigured() {
		t.Fatal("empty SMTP client reported sensitive delivery as configured")
	}
	if err := client.SendSensitive(
		"official@example.org",
		"Portal invitation",
		"secret onboarding link",
	); err == nil {
		t.Fatal("sensitive email was allowed to fall back to development logging")
	}
	client.host = "smtp.example.org"
	if !client.SensitiveDeliveryConfigured() {
		t.Fatal("SMTP client did not report sensitive delivery as configured")
	}
}

func TestValidateSensitiveDeliveryConfig(t *testing.T) {
	t.Setenv("SMTP_HOST", "smtp.example")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_FROM", "GMCL Portal <portal@example.test>")
	t.Setenv("SMTP_USERNAME", "user")
	t.Setenv("SMTP_PASSWORD", "password")
	t.Setenv("SMTP_REPLY_TO", "support@example.test")
	if err := NewFromEnv().ValidateSensitiveDeliveryConfig(); err != nil {
		t.Fatalf("valid SMTP configuration rejected: %v", err)
	}
	t.Setenv("SMTP_HOST", " smtp.example ")
	t.Setenv("SMTP_PORT", " 587 ")
	if err := NewFromEnv().ValidateSensitiveDeliveryConfig(); err != nil {
		t.Fatalf("trimmed SMTP configuration rejected: %v", err)
	}

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"missing host", "SMTP_HOST", ""},
		{"invalid port", "SMTP_PORT", "not-a-port"},
		{"invalid from", "SMTP_FROM", "not-an-address"},
		{"partial auth", "SMTP_PASSWORD", ""},
		{"invalid reply-to", "SMTP_REPLY_TO", "not-an-address"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if err := NewFromEnv().ValidateSensitiveDeliveryConfig(); err == nil {
				t.Fatal("invalid SMTP configuration unexpectedly passed")
			}
		})
	}
}

func TestToHTMLRendersBackupURLAsLink(t *testing.T) {
	html := toHTML("Primary:\nBUTTON_URL:https://gmcl.co.uk/magic-link/confirm?token=abc\nBackup:\nBACKUP_URL:https://www.gmcl.co.uk/magic-link/confirm?token=abc\nACCESS_URL:https://gmcl.co.uk/access\nACCESS_CODE:abc")

	if !strings.Contains(html, ">Open secure form</a>") {
		t.Fatalf("primary button missing: %s", html)
	}
	if !strings.Contains(html, "<strong>Backup link:</strong>") {
		t.Fatalf("backup label missing: %s", html)
	}
	if !strings.Contains(html, `href="https://www.gmcl.co.uk/magic-link/confirm?token=abc"`) {
		t.Fatalf("backup href missing: %s", html)
	}
	if !strings.Contains(html, "<strong>Manual access page:</strong>") {
		t.Fatalf("manual access page missing: %s", html)
	}
	if !strings.Contains(html, "<strong>Access code:</strong>") || !strings.Contains(html, ">abc</pre>") {
		t.Fatalf("access code missing: %s", html)
	}
}
