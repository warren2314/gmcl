package httpserver

import (
	"strings"
	"testing"
)

func TestEveryAudienceOutcomeDraftIsReadOnly(t *testing.T) {
	for _, audience := range []string{"offending_club", "reporting_club", "official"} {
		if !outcomeDraftIsReadOnly(audience) {
			t.Fatalf("%s outcome draft must be generated read-only", audience)
		}
	}
	for _, audience := range []string{"", "reporter", "league"} {
		if outcomeDraftIsReadOnly(audience) {
			t.Fatalf("invalid %s audience was treated as an outcome draft", audience)
		}
	}
}

func TestResponseRequestDraftRequiresCurrentAllegationLinkAndSevenDayWindow(t *testing.T) {
	allegation := "Player A appeared for First XI while subject to an active registration restriction."
	rule := "Alleged rule under investigation: Rule 3.5 - Player eligibility"
	body := "Dear Club Secretary,\n\nPlease respond to this allegation:\n\n" + allegation + "\n\n" + rule + "\n\nUse the secure link:\n" + responseLinkPlaceholder + "\n\nThe secure response window lasts seven days.\n\nRegards"
	if err := validateResponseDraftContent("response_request", body, allegation, rule); err != nil {
		t.Fatalf("editable valid request was rejected: %v", err)
	}
	for name, invalid := range map[string]string{
		"missing link":         strings.ReplaceAll(body, responseLinkPlaceholder, "https://example.invalid"),
		"missing window":       strings.ReplaceAll(body, "seven days", "the stated period"),
		"stale allegation":     strings.ReplaceAll(body, allegation, allegation+" Previous wording."),
		"duplicate allegation": strings.Replace(body, allegation, allegation+"\n\n"+allegation, 1),
		"missing alleged rule": strings.ReplaceAll(body, rule+"\n\n", ""),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateResponseDraftContent("response_request", invalid, allegation, rule); err == nil {
				t.Fatal("invalid response request was accepted")
			}
		})
	}
}

func TestCorrectedAllegationForcesNewResponseRequestDraft(t *testing.T) {
	oldAllegation := "Player A was ineligible."
	correctedAllegation := "Player A was ineligible under Rule 5."
	body := "Allegation:\n\n" + oldAllegation + "\n\n" + responseLinkPlaceholder + "\n\nPlease respond within seven days."
	if err := validateResponseDraftContent("response_request", body, oldAllegation, ""); err != nil {
		t.Fatalf("original request was not valid: %v", err)
	}
	if err := validateResponseDraftContent("response_request", body, correctedAllegation, ""); err == nil {
		t.Fatal("a request containing stale allegation wording remained queueable")
	}
}

func TestResponseReminderRequiresTwoDayContextAndNoAutomaticAdverseDecision(t *testing.T) {
	body := "Dear Club Secretary,\n\nYour response is due in two days. No adverse decision is made automatically if the deadline passes.\n\n" + responseLinkPlaceholder
	if err := validateResponseDraftContent("response_reminder", body, "unused", ""); err != nil {
		t.Fatalf("valid reminder was rejected: %v", err)
	}
	for name, invalid := range map[string]string{
		"missing link":      strings.ReplaceAll(body, responseLinkPlaceholder, "secure portal"),
		"missing two days":  strings.ReplaceAll(body, "due in two days", "due shortly"),
		"missing safeguard": strings.ReplaceAll(body, "No adverse decision is made automatically if the deadline passes.", "The investigation will continue."),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateResponseDraftContent("response_reminder", invalid, "unused", ""); err == nil {
				t.Fatal("invalid response reminder was accepted")
			}
		})
	}
}
