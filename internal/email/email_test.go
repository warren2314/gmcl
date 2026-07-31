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

func TestCaptainReportClientUsesReportsMailbox(t *testing.T) {
	t.Setenv("SMTP_REPLY_TO", "joep@gtrmcrcricket.co.uk")
	t.Setenv("CAPTAIN_REPORT_REPLY_TO", "")

	headers, err := NewCaptainReportFromEnv().messageHeaders("captain@example.com", "Captain report reminder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(headers, "Reply-To: <reports@gtrmcrcricket.co.uk>\r\n") {
		t.Fatalf("captain report reply-to missing: %s", headers)
	}
	if strings.Contains(headers, "joep@gtrmcrcricket.co.uk") {
		t.Fatalf("captain report inherited the general reply-to: %s", headers)
	}
}

func TestCaptainReportClientAllowsDedicatedReplyToOverride(t *testing.T) {
	t.Setenv("CAPTAIN_REPORT_REPLY_TO", "GMCL Reports <captain-replies@example.com>")

	headers, err := NewCaptainReportFromEnv().messageHeaders("captain@example.com", "Captain report reminder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(headers, "Reply-To: \"GMCL Reports\" <captain-replies@example.com>\r\n") {
		t.Fatalf("dedicated captain report reply-to missing: %s", headers)
	}
}

func TestStarredPlayerClientUsesJoesMailbox(t *testing.T) {
	t.Setenv("SMTP_REPLY_TO", "webmaster@gmcl.co.uk")
	t.Setenv("STARRED_PLAYER_REPLY_TO", "")

	headers, err := NewStarredPlayerFromEnv().messageHeaders("club@example.com", "Rule 3.5 review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(headers, "Reply-To: <joep@gtrmcrcricket.co.uk>\r\n") {
		t.Fatalf("starred-player reply-to missing: %s", headers)
	}
	if strings.Contains(headers, "Reply-To: <webmaster@gmcl.co.uk>") {
		t.Fatalf("starred-player email inherited the general reply-to: %s", headers)
	}
}

func TestStarredPlayerClientAllowsDedicatedReplyToOverride(t *testing.T) {
	t.Setenv("STARRED_PLAYER_REPLY_TO", "Joe <joe-replies@example.com>")

	headers, err := NewStarredPlayerFromEnv().messageHeaders("club@example.com", "Rule 3.5 review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(headers, "Reply-To: \"Joe\" <joe-replies@example.com>\r\n") {
		t.Fatalf("dedicated starred-player reply-to missing: %s", headers)
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
