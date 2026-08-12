package rulesassistant

import (
	"strings"
	"testing"
)

func TestParseHTMLPreservesLivePenaltyRuleReferences(t *testing.T) {
	longHeading := "8.3.2.5. Offence - No league form or play-cricket registration, or defective or invalid league form or play-cricket registration for any other reason"
	if len(longHeading) < 130 {
		t.Fatalf("test heading length=%d, want at least 130", len(longHeading))
	}
	raw := `<html><title>Rules penalties section 3</title><body>WELCOME TO GMCL FOR YOUR MOBILE
<h2>8.3.2.4.6. Appealable ? - Not Appealable (Amended Feb 2026)</h2>
<p>` + longHeading + `</p>
<p>8.3.2.5.1. Card - Red</p>
<p>8.3.2.5.2. Breach - At end of game</p>
<p>8.3.2.5.3. Penalty Points - Part of the Card System</p>
<p>8.3.2.5.4. Additional Penalty -</p>
<p>8.3.2.5.5. Comments - None</p>
<p>8.3.2.5.6. Appealable ? - Not appealable</p>
Proud Sponsors</body></html>`

	doc := parseHTML("https://example.test/pages/rules-pen3", raw)
	expected := []string{
		"8.3.2.4.6",
		"8.3.2.5",
		"8.3.2.5.1",
		"8.3.2.5.2",
		"8.3.2.5.3",
		"8.3.2.5.4",
		"8.3.2.5.5",
		"8.3.2.5.6",
	}
	if strings.Join(doc.SourceRuleReferences, ",") != strings.Join(expected, ",") {
		t.Fatalf("source reference inventory=%v want %v", doc.SourceRuleReferences, expected)
	}
	for _, reference := range expected {
		found := false
		for _, chunk := range doc.Chunks {
			if chunk.RuleReference == reference {
				found = true
				if !strings.HasPrefix(chunk.Content, reference) {
					t.Errorf("chunk %s content starts %q", reference, chunk.Content)
				}
				break
			}
		}
		if !found {
			t.Errorf("rule %s is missing from chunks: %+v", reference, doc.Chunks)
		}
	}
}

func TestRuleReferenceParsingSupportsUnlimitedHierarchyDepth(t *testing.T) {
	const reference = "8.3.2.2.4.1.2.2.1"
	line := reference + ". Removal of the win from the defaulting team's record"
	if got := extractRuleReference(line); got != reference {
		t.Fatalf("extractRuleReference()=%q want %q", got, reference)
	}
	if got := lineStartRuleReference(line); got != reference {
		t.Fatalf("lineStartRuleReference()=%q want %q", got, reference)
	}
	for _, suffix := range []string{" Removal applies", ") Removal applies", ": Removal applies"} {
		input := reference + suffix
		if got := lineStartRuleReference(input); got != reference {
			t.Errorf("punctuated line-start reference in %q=%q want %q", input, got, reference)
		}
	}
	if got := lineStartRuleReference("Guidance refers to " + reference + ": inline only"); got != "" {
		t.Fatalf("inline punctuated reference=%q want empty", got)
	}

	raw := `<html><title>Deep rules</title><body>WELCOME TO GMCL FOR YOUR MOBILE
<p>` + line + `</p>
Proud Sponsors</body></html>`
	doc := parseHTML("https://example.test/pages/deep-rules", raw)
	if len(doc.Chunks) != 1 || doc.Chunks[0].RuleReference != reference {
		t.Fatalf("deep reference was truncated or omitted: %+v", doc.Chunks)
	}
}

func TestInlineRuleReferencesDoNotSplitOrRelabelChunks(t *testing.T) {
	filler := strings.Repeat("Registration evidence remains part of this allegation. ", 45)
	raw := `<html><title>Registration rules</title><body>WELCOME TO GMCL FOR YOUR MOBILE
<h2>8.3.2.5. Missing or invalid registration</h2>
<p>` + filler + `</p>
<p>See Rule 8.4 for the separate forfeiture fine; this is an inline cross-reference.</p>
<p>The same paragraph also refers to 8.3.2.5.4 without opening a new rule.</p>
<h2>8.3.2.6. Next registration offence</h2>
<p>This is enough text to preserve the following rule chunk.</p>
Proud Sponsors</body></html>`

	doc := parseHTML("https://example.test/pages/registration-rules", raw)
	foundContinuation := false
	for _, chunk := range doc.Chunks {
		if chunk.RuleReference == "8.4" || chunk.RuleReference == "8.3.2.5.4" {
			t.Fatalf("inline cross-reference became a chunk identity: %+v", chunk)
		}
		if strings.Contains(chunk.Content, "inline cross-reference") {
			foundContinuation = true
			if chunk.RuleReference != "8.3.2.5" {
				t.Fatalf("continuation reference=%q want 8.3.2.5", chunk.RuleReference)
			}
		}
	}
	if !foundContinuation {
		t.Fatalf("size-flushed continuation was not preserved: %+v", doc.Chunks)
	}
	for _, reference := range doc.SourceRuleReferences {
		if reference == "8.4" || reference == "8.3.2.5.4" {
			t.Fatalf("inline reference entered source inventory: %v", doc.SourceRuleReferences)
		}
	}
}

func TestMissingRuleReferenceOccurrencesCountsDuplicates(t *testing.T) {
	doc := parsedDocument{
		URL:                  "https://example.test/pages/rules",
		SourceRuleReferences: []string{"8.3.2.5", "8.3.2.5"},
		Chunks: []parsedChunk{
			{RuleReference: "8.3.2.5", Content: "8.3.2.5. Missing or invalid registration"},
			{RuleReference: "8.3.2.5", Content: "Continuation text after a size-based flush"},
		},
	}
	missing := missingRuleReferenceOccurrences(doc)
	if len(missing) != 1 || missing[0] != "8.3.2.5" {
		t.Fatalf("missing occurrences=%v want one 8.3.2.5", missing)
	}
}
