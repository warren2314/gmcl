package email

import (
	"encoding/base64"
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

func TestBuildMessageWithAttachmentCreatesMultipartSnapshot(t *testing.T) {
	client := &Client{fromHeader: "GMCL <webmaster@gmcl.co.uk>"}
	pdf := []byte("%PDF-1.4 outcome letter")
	for _, test := range []struct {
		name      string
		filename  string
		forbidden string
	}{
		{name: "windows", filename: `C:\private\IP-2026-0001.pdf`, forbidden: `C:\private`},
		{name: "posix", filename: "/srv/private/IP-2026-0001.pdf", forbidden: "/srv/private"},
		{name: "UNC", filename: `\\fileserver\private\IP-2026-0001.pdf`, forbidden: "fileserver"},
		{name: "mixed", filename: `C:\private/nested\IP-2026-0001.pdf`, forbidden: "nested"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, err := client.buildMessage(
				"club@example.com",
				"Case IP-2026-0001 outcome",
				"The approved finding is attached.",
				[]Attachment{{Filename: test.filename, ContentType: "application/pdf", Data: pdf}},
			)
			if err != nil {
				t.Fatal(err)
			}
			got := string(message)
			for _, want := range []string{
				"Content-Type: multipart/mixed",
				"Content-Type: text/html; charset=UTF-8",
				"Content-Type: application/pdf",
				"Content-Disposition: attachment; filename=IP-2026-0001.pdf",
				base64.StdEncoding.EncodeToString(pdf),
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("message does not contain %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, test.forbidden) {
				t.Fatalf("attachment storage path leaked into message: %s", got)
			}
		})
	}
}

func TestBuildMessageRejectsInjectedAttachmentFilename(t *testing.T) {
	client := &Client{fromHeader: "webmaster@gmcl.co.uk"}
	_, err := client.buildMessage("club@example.com", "Outcome", "Body", []Attachment{{
		Filename: "private\r\n/Bcc: attacker@example.com/outcome.pdf", ContentType: "application/pdf", Data: []byte("pdf"),
	}})
	if err == nil {
		t.Fatal("injected attachment filename was accepted")
	}
}

func TestBuildMessageSafelyQuotesAttachmentFilename(t *testing.T) {
	client := &Client{fromHeader: "webmaster@gmcl.co.uk"}
	message, err := client.buildMessage("club@example.com", "Outcome", "Body", []Attachment{{
		Filename: `C:\private\outcome"; x=evil.pdf`, ContentType: "application/pdf", Data: []byte("pdf"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(message)
	if !strings.Contains(got, `filename="outcome\"; x=evil.pdf"`) {
		t.Fatalf("attachment filename was not safely quoted: %s", got)
	}
	if strings.Contains(got, `filename=outcome"; x=evil.pdf`) {
		t.Fatalf("attachment filename escaped its MIME parameter: %s", got)
	}
}

func TestBuildMessageWithoutAttachmentsRemainsHTML(t *testing.T) {
	client := &Client{fromHeader: "webmaster@gmcl.co.uk"}
	message, err := client.buildMessage("club@example.com", "Subject", "Hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(message)
	if !strings.Contains(got, "Content-Type: text/html; charset=UTF-8") {
		t.Fatalf("HTML content type missing: %s", got)
	}
	if strings.Contains(got, "multipart/mixed") {
		t.Fatalf("unexpected multipart wrapper: %s", got)
	}
}

func TestMessageHeadersRejectHeaderInjection(t *testing.T) {
	client := &Client{fromHeader: "webmaster@gmcl.co.uk"}
	for _, test := range []struct {
		name, to, subject string
	}{
		{name: "recipient", to: "club@example.com\r\nBcc: attacker@example.com", subject: "Outcome"},
		{name: "subject", to: "club@example.com", subject: "Outcome\r\nBcc: attacker@example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.messageHeaders(test.to, test.subject); err == nil {
				t.Fatal("header injection was accepted")
			}
		})
	}
}

func TestBuildMessageRejectsInjectedAttachmentContentType(t *testing.T) {
	client := &Client{fromHeader: "webmaster@gmcl.co.uk"}
	_, err := client.buildMessage("club@example.com", "Outcome", "Body", []Attachment{{
		Filename: "outcome.pdf", ContentType: "application/pdf\r\nBcc: attacker@example.com", Data: []byte("pdf"),
	}})
	if err == nil {
		t.Fatal("injected attachment content type was accepted")
	}
}

func TestBuildMessageIncludesStableMessageID(t *testing.T) {
	client := &Client{fromHeader: "webmaster@gmcl.co.uk"}
	message, err := client.buildMessageWithID("club@example.com", "Outcome", "Body", nil, "<sanction-outbox-42@gmcl.co.uk>")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), "Message-ID: <sanction-outbox-42@gmcl.co.uk>\r\n") {
		t.Fatalf("stable Message-ID missing: %s", message)
	}
}
