package sanctions

import (
	"strings"
	"testing"
)

func TestUnresolvedCardProposalErrorNamesBlockingCase(t *testing.T) {
	err := (&UnresolvedCardProposalError{
		TeamID:        235,
		TeamLabel:     "Woodhouses CC - 3rd XI",
		CaseID:        174,
		CaseReference: "GMCL-2026-001174",
	}).Error()
	for _, want := range []string{"Woodhouses CC - 3rd XI", "GMCL-2026-001174", "approve, reject, or correct"} {
		if !strings.Contains(err, want) {
			t.Fatalf("conflict error missing %q: %s", want, err)
		}
	}
}
