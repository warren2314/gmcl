package httpserver

import (
	"strings"
	"testing"
)

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

func TestRankHawkRuleCandidatesUsesCaseFactsNotDocumentOrder(t *testing.T) {
	candidates := []caseHawkRuleCandidate{
		{RuleReference: "3.7.3.1", Heading: "Limited dispensation", Excerpt: "An advance request may be made for a normally ineligible player."},
		{RuleReference: "3.5", Heading: "Starred Players & Junior Exemptions", Excerpt: "Restrictions for starred players and junior exemptions."},
		{RuleReference: "3.8", Heading: "Disciplinary Regulations", Excerpt: "Penalties may apply for ineligible players."},
	}
	context := "Potential Rule 3.5 appearance. Revalidated starred-player finding for List A player."
	ranked := rankHawkRuleCandidates(candidates, context, "3.5")
	if got := ranked[0].RuleReference; got != "3.5" {
		t.Fatalf("top HawkAI candidate = %s, want 3.5", got)
	}
	if ranked[0].MatchReason == "" {
		t.Fatal("top HawkAI candidate has no case-specific explanation")
	}
}

func TestRankHawkRuleCandidatesCanPreferDispensationWhenCaseMentionsIt(t *testing.T) {
	candidates := []caseHawkRuleCandidate{
		{RuleReference: "3.5", Heading: "Starred Players", Excerpt: "Restrictions for starred players."},
		{RuleReference: "3.7.3.1", Heading: "Limited dispensation", Excerpt: "An advance request for dispensation may be made."},
	}
	ranked := rankHawkRuleCandidates(candidates, "The club says an advance dispensation request was approved.", "")
	if got := ranked[0].RuleReference; got != "3.7.3.1" {
		t.Fatalf("top HawkAI candidate = %s, want 3.7.3.1", got)
	}
}

func TestCaseAllegedRuleFormValuesKeepsSavedReviewReasonVisible(t *testing.T) {
	rule := caseAllegedRule{
		Reference: "8.3.2.5",
		Reason:    "The player appeared before completing the required registration.",
	}
	reference, reason := caseAllegedRuleFormValues(rule, caseHawkRuleSuggestion{
		SuggestedRuleReference: "3.5",
	})
	if reference != rule.Reference {
		t.Fatalf("reference=%q want %q", reference, rule.Reference)
	}
	if reason != rule.Reason {
		t.Fatalf("reason=%q want saved review reason %q", reason, rule.Reason)
	}
}

func TestCaseAllegedRuleFormValuesUsesHawkSuggestionOnlyForNewReview(t *testing.T) {
	reference, reason := caseAllegedRuleFormValues(caseAllegedRule{}, caseHawkRuleSuggestion{
		SuggestedRuleReference: "8.3.2.5",
	})
	if reference != "8.3.2.5" {
		t.Fatalf("reference=%q want HawkAI suggestion", reference)
	}
	if reason == "" {
		t.Fatal("new suggested rule should include review guidance")
	}
}
func TestHawkAppealGuidanceChecksPublishedRuleBeforeClaimingAppeal(t *testing.T) {
	tests := []struct {
		name, text, status, instructionPart string
	}{
		{"non-appealable", "The decision is final and no right of appeal exists.", "non_appealable", "not appealable"},
		{"appealable", "The club may appeal within seven days.", "appealable", "Any appeal"},
		{"review required", "A fine may be imposed.", "review_required", "must be confirmed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guidance := hawkAppealGuidanceForRule(caseAllegedRule{Reference: "8.3.2.5", Text: tt.text})
			if guidance.Status != tt.status || !strings.Contains(guidance.Instructions, tt.instructionPart) {
				t.Fatalf("guidance = %#v, want status %q containing %q", guidance, tt.status, tt.instructionPart)
			}
		})
	}
}

func TestHawkAppealValidationRejectsUnsupportedAppealClaim(t *testing.T) {
	guidance := hawkAppealGuidanceForRule(caseAllegedRule{Reference: "8.3.2.5", Text: "A fine may be imposed."})
	if err := validateHawkAppealInstructions(guidance, "Any appeal must be submitted within seven days."); err == nil {
		t.Fatal("unsupported appeal claim was accepted")
	}
}

func TestSplitAdditionalOutcomeRecipients(t *testing.T) {
	got := splitAdditionalOutcomeRecipients("stuart@example.org; gary@example.org\nother@example.org")
	if len(got) != 3 || got[0] != "stuart@example.org" || got[2] != "other@example.org" {
		t.Fatalf("recipients = %#v", got)
	}
}
