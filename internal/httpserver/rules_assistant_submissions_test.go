package httpserver

import (
	"strings"
	"testing"
	"time"
)

func TestIsSubmissionLookupQuestionSeparatesStatusFromRulebook(t *testing.T) {
	statusQuestions := []string{
		"Has my report gone in?",
		"Did our submission go through?",
		"I never received the email link",
		"Why hasn't my sign-in link arrived? I requested an email",
		"What email address do you have for me?",
		"Did we submit our form on Saturday?",
	}
	for _, question := range statusQuestions {
		if !isSubmissionLookupQuestion(question) {
			t.Errorf("expected submission-status intent for %q", question)
		}
	}
	rulebookQuestions := []string{
		"When must match details be entered on Play-Cricket?", // eval question 13
		"What happens if we do not submit a report?",
		"What is the deadline for our match report?",
		"Do we get a fine for a late report?", // penalty phrasing stays with rules/sanctions
		"Tell me about the report rules",
		"Where must official league communications be sent?",
		"Can a captain email the umpire?",
	}
	for _, question := range rulebookQuestions {
		if isSubmissionLookupQuestion(question) {
			t.Errorf("expected rulebook routing for %q", question)
		}
	}
}

func TestMaskEmailKeepsFirstCharacterAndDomain(t *testing.T) {
	for input, want := range map[string]string{
		"warren@example.com": "w…@example.com",
		"a@b.co":             "a…@b.co",
		"":                   "",
		"not-an-email":       "not-an-email",
	} {
		if got := maskEmail(input); got != want {
			t.Errorf("maskEmail(%q)=%q want %q", input, got, want)
		}
	}
}

func submissionStatusFixture() captainSubmissionStatus {
	london, _ := time.LoadLocation("Europe/London")
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, london)
	used := time.Date(2026, 7, 18, 9, 14, 0, 0, london)
	return captainSubmissionStatus{
		CaptainName:    "Casey Captain",
		TeamName:       "Worsley 1st XI",
		ClubName:       "Worsley CC",
		PermanentEmail: "casey@example.com",
		EffectiveEmail: "override@example.com",
		OverrideActive: true,
		OverrideUntil:  "31 Jul 2026",
		Submissions: []captainSubmissionRecord{
			{MatchDate: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC), SubmittedAt: time.Date(2026, 7, 18, 9, 20, 0, 0, london)},
			{MatchDate: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), SubmittedAt: time.Date(2026, 7, 11, 19, 2, 0, 0, london)},
		},
		Tokens:       []captainLinkToken{{CreatedAt: time.Date(2026, 7, 17, 18, 2, 0, 0, london), ExpiresAt: time.Date(2026, 7, 19, 18, 2, 0, 0, london), UsedAt: &used}},
		LastLinkSend: &captainLinkSend{CreatedAt: time.Date(2026, 7, 17, 18, 2, 0, 0, london), SeasonName: "2026", WeekNumber: 12},
		Now:          now,
	}
}

func TestFormatCaptainSubmissionAnswerLeadsWithWhatWasAsked(t *testing.T) {
	status := submissionStatusFixture()
	answer, citations := formatCaptainSubmissionAnswer(status, "Has my report gone in?")
	if !strings.Contains(answer, "submitted on Sat 18 Jul 09:20") || !strings.Contains(answer, "18 July fixture") {
		t.Fatalf("submission status missing: %s", answer)
	}
	if strings.Index(answer, "match report") > strings.Index(answer, "sign-in link") {
		t.Fatalf("report question must lead with the submission section: %s", answer)
	}
	if strings.Contains(answer, "casey@example.com") || strings.Contains(answer, "override@example.com") {
		t.Fatalf("full email address leaked: %s", answer)
	}
	if !strings.Contains(answer, "o…@example.com") || !strings.Contains(answer, "override is active until 31 Jul 2026") {
		t.Fatalf("masked override detail missing: %s", answer)
	}
	if len(citations) != 2 {
		t.Fatalf("citations=%v", citations)
	}

	answer, _ = formatCaptainSubmissionAnswer(status, "Why did my email link not work?")
	if strings.Index(answer, "sign-in link") > strings.Index(answer, "match report") {
		t.Fatalf("link question must lead with the link section: %s", answer)
	}
	if !strings.Contains(answer, "used on Sat 18 Jul 09:14") {
		t.Fatalf("token usage explanation missing: %s", answer)
	}
}

func TestFormatCaptainSubmissionAnswerExplainsExpiryAndBounces(t *testing.T) {
	status := submissionStatusFixture()
	status.Submissions = nil
	status.OverrideActive = false
	status.Tokens[0].UsedAt = nil
	status.Tokens[0].ExpiresAt = status.Now.Add(-24 * time.Hour)
	status.EmailEvents = []captainEmailEvent{{At: status.Now.Add(-2 * time.Hour), Type: "bounce", Recipient: "override@example.com"}}
	answer, _ := formatCaptainSubmissionAnswer(status, "I never got my link email")
	for _, want := range []string{"No submitted match reports are recorded", "expired on", "without being used", "bounce", "o…@example.com"} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q: %s", want, answer)
		}
	}
}

func TestFormatCaptainSubmissionAnswerValidTokenState(t *testing.T) {
	status := submissionStatusFixture()
	status.Tokens[0].UsedAt = nil
	status.Tokens[0].ExpiresAt = status.Now.Add(24 * time.Hour)
	answer, _ := formatCaptainSubmissionAnswer(status, "Where is my link?")
	if !strings.Contains(answer, "still valid until") {
		t.Fatalf("valid token state missing: %s", answer)
	}
}

func TestHasSubmissionLookupTermsForAdminClubQuestions(t *testing.T) {
	// Admin questions carry no ownership marker; the club name is the scope.
	for _, question := range []string{"Has Woodley submitted their report?", "Why didn't the Worsley captain get their sign-in link email?"} {
		if !hasSubmissionLookupTerms(question) {
			t.Errorf("expected submission terms for %q", question)
		}
	}
	for _, question := range []string{"What is the deadline for Woodley's report?", "What happens if a report is late?", "When must match details be entered on Play-Cricket?"} {
		if hasSubmissionLookupTerms(question) {
			t.Errorf("expected rulebook routing for %q", question)
		}
	}
}

func TestFormatAdminSubmissionAnswerCoversEveryTeamState(t *testing.T) {
	withCaptain := submissionStatusFixture()
	captainless := captainSubmissionStatus{Now: withCaptain.Now}
	teams := []adminSubmissionTeam{
		{TeamName: "1st XI", HasCaptain: true, Status: withCaptain},
		{TeamName: "2nd XI", HasCaptain: false, Status: captainless},
	}
	answer, citations := formatAdminSubmissionAnswer("Worsley CC", teams, 1)
	for _, want := range []string{
		"Report and sign-in-link status for Worsley CC",
		"1st XI — captain Casey Captain (o…@example.com)",
		"latest submitted Sat 18 Jul 09:20",
		"2nd XI — no active captain on record",
		"Reports: none recorded.",
		"1 further team(s) not shown",
		"Email addresses are masked",
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("admin answer missing %q:\n%s", want, answer)
		}
	}
	if strings.Contains(answer, "override@example.com") || strings.Contains(answer, "casey@example.com") {
		t.Fatalf("full email leaked: %s", answer)
	}
	if len(citations) != 2 || !strings.Contains(citations[0]["url"].(string), "/admin/link-diagnostics?q=Worsley+CC") {
		t.Fatalf("citations=%v", citations)
	}
}
