package httpserver

import (
	"os"
	"strings"
	"testing"
)

func TestCaseCorrectionCastsSummaryParametersInsideJSONAuditSnapshot(t *testing.T) {
	source, err := os.ReadFile("sanctions_cases.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{"$5::text", "$6::text", "$7::text", "$8::text"} {
		if !strings.Contains(text, want) {
			t.Fatalf("case correction audit insert is missing %s; PostgreSQL cannot infer an uncast jsonb_build_object parameter", want)
		}
	}
}
