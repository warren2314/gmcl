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
