package email

import (
	"strings"
	"testing"
)

func TestSanctionEmailHTMLBrandsLinksAndRules(t *testing.T) {
	body := "Dear Club Secretary,\n\nRule being checked:\n\nAlleged rule under investigation: Rule 8.3.2.5 - Registration\nPublished source: https://www.gtrmcrcricket.co.uk/pages/rules-pen3\n\nPlease respond:\nhttps://gmcl.co.uk/sanctions/case/respond?token=abc&case=12\n\n- explain what happened;\n- attach evidence."
	got := sanctionEmailHTML("Please respond: case GMCL-2026-001174", body)
	for _, want := range []string{
		"Greater Manchester Cricket League",
		"background:#c41e3a",
		"Rule 8.3.2.5",
		`href="https://www.gtrmcrcricket.co.uk/pages/rules-pen3"`,
		">Respond securely</a>",
		`href="https://gmcl.co.uk/sanctions/case/respond?token=abc&amp;case=12"`,
		"<li",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("branded email does not contain %q:\n%s", want, got)
		}
	}
}

func TestSanctionEmailHTMLShowsTestBannerAndEscapesContent(t *testing.T) {
	got := sanctionEmailHTML("[TEST ONLY - NO CLUB CONTACT] <case>", "Rule 8.3.2.5 <script>alert(1)</script>\nhttps://example.com/path).")
	for _, want := range []string{"TEST EMAIL — NO CLUB HAS BEEN CONTACTED", "&lt;case&gt;", "&lt;script&gt;alert(1)&lt;/script&gt;", `href="https://example.com/path"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("branded test email does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Fatalf("untrusted HTML was not escaped: %s", got)
	}
}
