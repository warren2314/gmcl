package httpserver

import (
	"os"
	"strings"
	"testing"
)

func TestAllegedRuleManualFallbackIsProminentAndHawkQueryStaysInActiveRelease(t *testing.T) {
	raw, err := os.ReadFile("sanctions_case_rules.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		`href="#record-alleged-rule"`,
		`id="record-alleged-rule"`,
		"Next action: %s alleged rule",
		"You do not need HawkAI to complete this step",
		`class="btn btn-primary">Save reviewed rule`,
		"HawkAI is unavailable. Continue with Record alleged rule below",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("manual alleged-rule fallback does not contain %q", required)
		}
	}
	queryStart := strings.Index(source, "WHERE release.status='active' AND NULLIF(BTRIM(chunk.rule_reference),'') IS NOT NULL")
	queryEnd := strings.Index(source[queryStart:], "ORDER BY chunk.ordinal,chunk.id")
	if queryStart < 0 || queryEnd < 1 {
		t.Fatal("could not isolate HawkAI eligibility query")
	}
	query := source[queryStart : queryStart+queryEnd]
	if !strings.Contains(query, "AND (chunk.heading_path ILIKE '%ineligible%'") {
		t.Fatal("eligibility predicates are not grouped under the active-release condition")
	}
	if !strings.Contains(query, "chunk.content ILIKE '%registration%'))") {
		t.Fatal("eligibility predicate group does not close before ordering")
	}
}
