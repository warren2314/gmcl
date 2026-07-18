package httpserver

import (
	"bytes"
	"strings"
	"testing"
)

func TestPublicNavigationExposesSanctionsAndCollapsesOnMobile(t *testing.T) {
	var out bytes.Buffer
	writeCaptainNav(&out)
	html := out.String()
	for _, want := range []string{`href="https://sanctions.gmcl.co.uk/"`, `Sanctions register`, `navbar-toggler`, `id="publicNav"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("public navigation missing %q", want)
		}
	}
}

func TestAdminNavigationExposesSanctionsWorkflow(t *testing.T) {
	var out bytes.Buffer
	writeAdminNav(&out, "csrf", "/admin/cases/imports", "super_admin")
	html := out.String()
	for _, want := range []string{"Sanctions", "Add card, ban, fine or points decision", "Import legacy bans &amp; cards", "Follow-up tasks", "View public register"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin navigation missing %q", want)
		}
	}
}
