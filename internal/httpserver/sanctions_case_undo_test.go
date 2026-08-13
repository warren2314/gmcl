package httpserver

import (
	"strings"
	"testing"
)

func TestAdminUndoCaseOpeningHTML(t *testing.T) {
	html := adminUndoCaseOpeningHTML(42, `token"value`, "ineligible_player", "investigating", false)
	for _, want := range []string{"/admin/cases/42/undo-opening", "Undo opening and return report", "pending review", "token&quot;value"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in %s", want, html)
		}
	}
	for _, input := range []struct {
		source, status string
		test           bool
	}{
		{"manual", "investigating", false},
		{"ineligible_player", "approved", false},
		{"ineligible_player", "investigating", true},
	} {
		if got := adminUndoCaseOpeningHTML(42, "csrf", input.source, input.status, input.test); got != "" {
			t.Fatalf("unexpected undo control for %+v: %s", input, got)
		}
	}
}
