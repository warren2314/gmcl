package sanctions

import (
	"strings"
	"testing"
)

func TestOutcomeDraftAudienceAndKinds(t *testing.T) {
	for _, audience := range []string{"offending_club", "reporting_club", "official"} {
		if !validOutcomeAudience(audience) {
			t.Fatalf("expected %q to be a valid audience", audience)
		}
		if kind := outcomeMessageKind(audience, false); kind == "" {
			t.Fatalf("expected a message kind for %q", audience)
		}
		if kind := outcomeMessageKind(audience, true); kind != "no_action_outcome" {
			t.Fatalf("no-action kind = %q", kind)
		}
	}
	for _, audience := range []string{"", "reporter", "offending-and-reporting"} {
		if validOutcomeAudience(audience) {
			t.Fatalf("expected %q to be rejected", audience)
		}
	}
}

func TestOutcomeIsNoActionRequiresOnlyNoActionEffects(t *testing.T) {
	if outcomeIsNoAction(nil) {
		t.Fatal("an empty bundle is not a no-action decision")
	}
	if !outcomeIsNoAction([]approvedOutcomeEffect{{typeName: "no_action"}}) {
		t.Fatal("a no-action-only bundle should be recognised")
	}
	if outcomeIsNoAction([]approvedOutcomeEffect{{typeName: "no_action"}, {typeName: "warning"}}) {
		t.Fatal("a mixed bundle must not be treated as no action")
	}
}

func TestRenderedOutcomeDraftsContainEveryRequiredSection(t *testing.T) {
	rendered := renderOutcomeCommunications(outcomeRenderData{
		reference:     "GMCL-2026-0042",
		sourceType:    "ineligible_player",
		offendingClub: "Offending CC",
		offendingTeam: "4th XI",
		reportingClub: "Reporting CC",
		subject:       "Case outcome",
		findings:      "The player was not eligible for the fixture.",
		rule:          "Rule 3.5",
		effectSummary: "- Warning - Example Player",
		appeal:        "Appeal within seven days.",
		signatoryName: "Denver Thornton",
	})
	for audience, body := range map[string]string{
		"offending_club": rendered.offending,
		"reporting_club": rendered.reporting,
		"official":       rendered.official,
	} {
		if err := validateOutcomeDraftCompleteness(audience, body); err != nil {
			t.Errorf("generated %s draft is incomplete: %v", audience, err)
		}
	}
	for _, want := range []string{"Dear Club Official,", "The League officials have approved the decision for case GMCL-2026-0042", "Offending team:\nOffending 4th XI", "Regards,\n\nDenver Thornton\n\nGMCL Disciplinary Officer"} {
		if !strings.Contains(rendered.offending, want) {
			t.Fatalf("offending-club version is missing %q:\n%s", want, rendered.offending)
		}
	}
	if !strings.Contains(rendered.official, "Offending team: Offending 4th XI") {
		t.Fatalf("official version does not identify the specific team:\n%s", rendered.official)
	}
}

func TestOutcomeDraftCompletenessRejectsMissingEmptyDuplicateAndReorderedSections(t *testing.T) {
	offending := "Dear Club Secretary,\n\nFindings:\nConfirmed finding.\n\nRule determination:\nRule 3.5\n\nDecision and sanctions:\nWarning.\n\nAppeal instructions:\nAppeal within seven days.\n\nRegards,\nGMCL"
	offendingEmptyFindings := strings.Replace(offending, "Findings:\nConfirmed finding.", "Findings:", 1)
	offendingEmptyAppeal := strings.Replace(offending, "Appeal instructions:\nAppeal within seven days.", "Appeal instructions:", 1)
	offendingReordered := strings.Replace(offending,
		"Findings:\nConfirmed finding.\n\nRule determination:\nRule 3.5",
		"Rule determination:\nRule 3.5\n\nFindings:\nConfirmed finding.", 1)
	official := "Approved league outcome record\n\nCase: GMCL-2026-0042\nSource: ineligible_player\nOffending club: Offending CC\nReporting club: Reporting CC\n\nFindings:\nConfirmed finding.\n\nRule determination:\nRule 3.5\n\nDecision and sanctions:\nWarning.\n\nAppeal instructions:\nAppeal within seven days."

	tests := []struct {
		name, audience, body string
	}{
		{"missing section", "offending_club", strings.Replace(offending, "Rule determination:", "Rule:", 1)},
		{"empty findings", "offending_club", offendingEmptyFindings},
		{"signature is not appeal content", "offending_club", offendingEmptyAppeal},
		{"duplicate section", "offending_club", offending + "\n\nFindings:\nAnother finding."},
		{"reordered sections", "offending_club", offendingReordered},
		{"empty official inline field", "official", strings.Replace(official, "Source: ineligible_player", "Source:", 1)},
		{"empty official final block", "official", strings.Replace(official, "Decision and sanctions:\nWarning.", "Decision and sanctions:", 1)},
		{"empty official appeal", "official", strings.Replace(official, "Appeal instructions:\nAppeal within seven days.", "Appeal instructions:", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateOutcomeDraftCompleteness(test.audience, test.body); err == nil {
				t.Fatalf("expected incomplete %s draft to be rejected", test.audience)
			}
		})
	}
}

func TestEveryOutcomeMustMatchGeneratedTemplate(t *testing.T) {
	subject := "GMCL case outcome GMCL-2026-0042"
	body := "Dear Club Secretary,\n\nConfirmed findings:\nFinding.\n\nRule determination:\nRule 3.5\n\nFinal outcome:\nNo action.\n\nRegards,\nGMCL"
	if !outcomeDraftMatchesGenerated(subject, strings.ReplaceAll(body, "\n", "\r\n"), subject, body) {
		t.Fatal("browser line-ending normalization should not make the generated reporting draft stale")
	}
	if outcomeDraftMatchesGenerated(subject, strings.Replace(body, "Finding.", "Different finding.", 1), subject, body) {
		t.Fatal("edited reporting-club findings matched the deterministic template")
	}
	if outcomeDraftMatchesGenerated("Changed subject", body, subject, body) {
		t.Fatal("edited outcome subject matched the deterministic template")
	}
}

func TestAudienceOutcomeSnapshotsRejectAnyDecisionBearingMutation(t *testing.T) {
	rendered := renderOutcomeCommunications(outcomeRenderData{
		reference:     "GMCL-2026-0042",
		sourceType:    "ineligible_player",
		offendingClub: "Offending CC",
		reportingClub: "Reporting CC",
		subject:       "GMCL case outcome GMCL-2026-0042",
		findings:      "The player was ineligible for the fixture.",
		rule:          "Rule 3.5",
		effectSummary: "- Points adjustment - First XI (-4 league-table points); effective 8 August 2026",
		appeal:        "Appeal within seven days.",
	})
	bodies := map[string]string{
		"offending_club": rendered.offending,
		"reporting_club": rendered.reporting,
		"official":       rendered.official,
	}
	for audience, body := range bodies {
		t.Run(audience, func(t *testing.T) {
			if !outcomeDraftMatchesGenerated(rendered.subject, strings.ReplaceAll(body, "\n", "\r\n"), rendered.subject, body) {
				t.Fatal("CRLF-only normalization should be accepted")
			}
			for _, replacement := range []struct{ old, new string }{
				{"GMCL-2026-0042", "GMCL-2026-9999"},
				{"ineligible_player", "discipline"},
				{"Offending CC", "Different CC"},
				{"Reporting CC", "Other CC"},
				{"The player was ineligible for the fixture.", "The player was eligible."},
				{"Rule 3.5", "Rule 9.9"},
				{"-4 league-table points", "-1 league-table point"},
				{"8 August 2026", "9 August 2026"},
				{"Appeal within seven days.", "No appeal is permitted."},
			} {
				if !strings.Contains(body, replacement.old) {
					continue
				}
				mutated := strings.Replace(body, replacement.old, replacement.new, 1)
				if outcomeDraftMatchesGenerated(rendered.subject, mutated, rendered.subject, body) {
					t.Fatalf("%s mutation %q was accepted", audience, replacement.old)
				}
			}
		})
	}
	if outcomeDraftMatchesGenerated("GMCL no-action outcome GMCL-2026-0042", rendered.reporting, rendered.subject, rendered.reporting) {
		t.Fatal("a no-action subject was accepted for a sanctions outcome")
	}
}
