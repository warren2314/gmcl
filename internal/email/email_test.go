package email

import (
	"strings"
	"testing"
)

func TestToHTMLRendersBackupURLAsLink(t *testing.T) {
	html := toHTML("Primary:\nhttps://gmcl.co.uk/magic-link/confirm?token=abc\nBackup:\nBACKUP_URL:https://www.gmcl.co.uk/magic-link/confirm?token=abc")

	if !strings.Contains(html, ">Open link</a>") {
		t.Fatalf("primary button missing: %s", html)
	}
	if !strings.Contains(html, "<strong>Backup link:</strong>") {
		t.Fatalf("backup label missing: %s", html)
	}
	if !strings.Contains(html, `href="https://www.gmcl.co.uk/magic-link/confirm?token=abc"`) {
		t.Fatalf("backup href missing: %s", html)
	}
}
