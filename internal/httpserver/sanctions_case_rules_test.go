package httpserver

import "testing"

func TestNormalizeRuleReference(t *testing.T) {
	for input, want := range map[string]string{
		"3.5": "3.5", " Rule 3.5 ": "3.5", "rule 4.2.1": "4.2.1",
	} {
		if got := normalizeRuleReference(input); got != want {
			t.Fatalf("normalizeRuleReference(%q)=%q want %q", input, got, want)
		}
	}
}

func TestAllegedRuleCorrespondenceParagraph(t *testing.T) {
	rule := caseAllegedRule{Reference: "3.5", Heading: "Player eligibility"}
	rule.URL = "https://www.gtrmcrcricket.co.uk/pages/rules-3-5"
	want := "Alleged rule under investigation: Rule 3.5 - Player eligibility\nPublished source: https://www.gtrmcrcricket.co.uk/pages/rules-3-5"
	if got := allegedRuleCorrespondenceParagraph(rule); got != want {
		t.Fatalf("paragraph=%q want %q", got, want)
	}
}
