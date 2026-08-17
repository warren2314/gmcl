package sanctions

import (
	"os"
	"strings"
	"testing"
)

func TestApprovalQueriesKeepDecisionRevisionParameterNumeric(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	approvalSource := string(source)
	if strings.Contains(approvalSource, "metadata->>'decision_revision_id'=$2::text") {
		t.Fatal("decision revision ID is still forced to PostgreSQL text, which pgx cannot encode from int64")
	}
	for _, want := range []string{
		"(metadata->>'decision_revision_id')::bigint=$2",
		"(required.metadata->>'decision_revision_id')::bigint=$2",
	} {
		if !strings.Contains(approvalSource, want) {
			t.Fatalf("approval query is missing numeric decision revision comparison %q", want)
		}
	}
}
