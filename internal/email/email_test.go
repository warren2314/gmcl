package email

import (
	"strings"
	"testing"
)

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
