package httpserver

import (
	"strings"
	"testing"
)

func TestParseApprovedIneligibleReopenForm(t *testing.T) {
	caseID, reason, err := parseApprovedIneligibleReopenForm("42", "  Google revision 3 changed the fixture date.  ")
	if err != nil || caseID != 42 || reason != "Google revision 3 changed the fixture date." {
		t.Fatalf("parse result = (%d, %q, %v)", caseID, reason, err)
	}
	for _, test := range []struct{ caseID, reason string }{
		{"", "reason"}, {"0", "reason"}, {"not-a-number", "reason"}, {"42", ""}, {"42", strings.Repeat("x", 4001)},
	} {
		if _, _, err := parseApprovedIneligibleReopenForm(test.caseID, test.reason); err == nil {
			t.Errorf("expected case=%q reason length=%d to fail", test.caseID, len(test.reason))
		}
	}
}

func TestAdminIneligibleReopenFormExplainsSafeWorkflow(t *testing.T) {
	html := adminIneligibleReopenFormHTML(42, `csrf-<token>`)
	for _, required := range []string{
		`action="/admin/cases/42/reopen-source-change"`,
		`name="reason"`,
		`required`,
		`before publication or delivery`,
		`merge the latest intake revision`,
		`fresh proposal`,
		`different authorised administrator`,
		`csrf-&lt;token&gt;`,
	} {
		if !strings.Contains(html, required) {
			t.Errorf("reopen form is missing %q", required)
		}
	}
}
