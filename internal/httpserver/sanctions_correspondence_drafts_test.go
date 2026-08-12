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

func TestDefaultAdminResponseDraftViewsExposeTemplates(t *testing.T) {
	views := defaultAdminResponseDraftViews(
		"GMCL-2026-0169",
		"Example CC First XI",
		"An eligibility concern was reported.",
		"Alleged rule under investigation: Rule 3.5 - Player eligibility",
	)
	request := views["response_request"]
	for _, required := range []string{"GMCL-2026-0169", "Example CC First XI", "eligibility concern", "Rule 3.5", responseLinkPlaceholder, "seven days", "No decision has been made", "what happened and why the player appeared"} {
		if !strings.Contains(request.subject+"\n"+request.body, required) {
			t.Fatalf("response request template does not contain %q", required)
		}
	}
	reminder := views["response_reminder"]
	for _, required := range []string{"GMCL-2026-0169", responseLinkPlaceholder, "due in two days", "No adverse decision is made automatically"} {
		if !strings.Contains(reminder.subject+"\n"+reminder.body, required) {
			t.Fatalf("response reminder template does not contain %q", required)
		}
	}
}

func TestAdminClubResponseStepsExplainWhenEmailIsSent(t *testing.T) {
	html := adminClubResponseStepsHTML()
	for _, required := range []string{
		`id="contact-club"`,
		"Next action: contact the club for its explanation",
		"No email is sent merely by opening this case",
		"Review and save the initial email",
		"Review and save the reminder",
		"Send initial email to club",
		"The reminder is sent only later",
		"This is the only button in this section that contacts the club",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("club response steps do not contain %q", required)
		}
	}
}

func TestAdminDecisionEffectsHTMLCollapsesOptionalEffects(t *testing.T) {
	html := adminDecisionEffectsHTML([]adminDecisionSubject{{id: 42, label: "Player - Example Name"}})
	if !strings.Contains(html, "Primary effect") {
		t.Fatal("primary effect is not shown")
	}
	if got := strings.Count(html, "<details"); got != 4 {
		t.Fatalf("optional effect count = %d, want 4", got)
	}
	if got := strings.Count(html, `name="effect_type"`); got != 5 {
		t.Fatalf("effect input count = %d, want 5", got)
	}
	if strings.Contains(html, "col-md-2") {
		t.Fatal("effect form still contains cramped two-column-width controls")
	}
}

func TestPendingOutcomeEmailTemplatesShowEveryDecisionSection(t *testing.T) {
	for _, audience := range []string{"offending_club", "reporting_club", "official"} {
		subject, body := pendingOutcomeEmailTemplate("GMCL-2026-0169", audience)
		if !strings.Contains(subject, "GMCL-2026-0169") || !strings.Contains(body, "GMCL-2026-0169") {
			t.Fatalf("%s template does not contain the case reference", audience)
		}
		if !strings.Contains(body, "Findings:") || !strings.Contains(body, "Rule determination:") {
			t.Fatalf("%s template omits core outcome sections", audience)
		}
		if !strings.Contains(body, "[Added after the decision is proposed]") {
			t.Fatalf("%s template does not explain its pending content", audience)
		}
	}
}
