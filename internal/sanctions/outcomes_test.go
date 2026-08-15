package sanctions

import (
	"strings"
	"testing"
	"time"
)

func TestApprovedEffectSummaryUsesMappedTeamForTeamEffects(t *testing.T) {
	points := -2
	got := approvedEffectSummary([]approvedOutcomeEffect{{
		typeName: "points_adjustment", subjectType: "team",
		playerName: "Case Player", teamName: "Second XI", points: &points,
	}})
	want := "- Points adjustment - Second XI (-2 league-table points)"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestApprovedEffectSummaryOmitsBanStyleDatesForCards(t *testing.T) {
	starts := time.Date(2026, time.August, 2, 0, 0, 0, 0, time.UTC)
	ends := time.Date(2026, time.August, 14, 0, 0, 0, 0, time.UTC)
	got := approvedEffectSummary([]approvedOutcomeEffect{{
		typeName: "red_card", playerName: "Warren Phillips", startsAt: &starts, endsAt: &ends,
	}})
	if strings.Contains(got, "effective") || strings.Contains(got, "14 August") {
		t.Fatalf("card summary contains misleading ban-style dates: %q", got)
	}
}

func TestApprovedEffectSummaryKeepsDatesForBans(t *testing.T) {
	starts := time.Date(2026, time.August, 2, 0, 0, 0, 0, time.UTC)
	ends := time.Date(2026, time.August, 14, 0, 0, 0, 0, time.UTC)
	got := approvedEffectSummary([]approvedOutcomeEffect{{
		typeName: "player_ban", playerName: "Example Player", startsAt: &starts, endsAt: &ends,
	}})
	if !strings.Contains(got, "effective 2 August 2026 to 14 August 2026") {
		t.Fatalf("ban summary lost its date range: %q", got)
	}
}

func TestCanonicalOutcomeRecipientRequiresPlainAddress(t *testing.T) {
	if got, err := canonicalOutcomeRecipient("OFFICIAL@example.org"); err != nil || got != "official@example.org" {
		t.Fatalf("canonical recipient = %q, %v", got, err)
	}
	for _, invalid := range []string{"Name <official@example.org>", "one@example.org, two@example.org", "bad\r\nBcc:x@example.org"} {
		if _, err := canonicalOutcomeRecipient(invalid); err == nil {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}

func TestAppendUniqueOutcomeRecipientAddsReporterAndDeduplicates(t *testing.T) {
	seen := map[string]bool{}
	recipients, err := appendUniqueOutcomeRecipient(nil, seen, "REPORTER@example.test")
	if err != nil {
		t.Fatal(err)
	}
	recipients, err = appendUniqueOutcomeRecipient(recipients, seen, "reporter@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0] != "reporter@example.test" {
		t.Fatalf("recipients = %#v, want one canonical reporter address", recipients)
	}
	if _, err = appendUniqueOutcomeRecipient(recipients, seen, "Name <reporter@example.test>"); err == nil {
		t.Fatal("display-name reporter address was accepted")
	}
}
func TestOutcomeContainsPrivateIdentity(t *testing.T) {
	if !outcomeContainsPrivateIdentity("Findings reported by Example CC", "Example CC") {
		t.Fatal("reporting-club identity was not detected")
	}
	if outcomeContainsPrivateIdentity("Dear Club Secretary", "Sec") {
		t.Fatal("short role must not create a false identity match")
	}
}

func TestContainsPrivateIdentitySplitsLegacyNameAndRole(t *testing.T) {
	for _, body := range []string{
		"The allegation was submitted by Jane Smith.",
		"The reporter was the Club Welfare Officer.",
		"Contact 07700 900 123 for details.",
	} {
		if !ContainsPrivateIdentity(body, "Jane Smith, Club Welfare Officer", "07700-900-123") {
			t.Fatalf("private legacy reporter detail was not detected in %q", body)
		}
	}
}

func TestContainsPrivateIdentityDoesNotTreatGenericRoleAsPerson(t *testing.T) {
	if ContainsPrivateIdentity("The ineligible player finding is confirmed.", "Jane Smith, Player", "Player") {
		t.Fatal("generic role word created a false reporter-identity match")
	}
	if !ContainsPrivateIdentity("The report was submitted by Jane Smith.", "Jane Smith, Player") {
		t.Fatal("reporter's name was not protected when the combined role was generic")
	}
}

func TestContainsPrivateIdentityCoversShortNamesAndLegacySeparators(t *testing.T) {
	for _, value := range []string{"Jo Li / Secretary", "Jo Li (Secretary)", "Jo Li-Secretary"} {
		if !ContainsPrivateIdentity("The report was submitted by Jo Li.", value) {
			t.Errorf("short reporter name was not protected for %q", value)
		}
	}
	if !ContainsPrivateIdentity("The reporter was the Club Secretary.", "Jo Li / Club Secretary") {
		t.Fatal("contextual reporter role disclosure was not detected")
	}
	if ContainsPrivateIdentity("The club secretary will receive the final decision.", "Jo Li / Club Secretary") {
		t.Fatal("ordinary recipient-role wording created a false identity disclosure")
	}
}

func TestContainsPrivateIdentityCoversContextualReporterRolePhrases(t *testing.T) {
	for _, body := range []string{
		"The Club Captain lodged the complaint.",
		"The complaint was raised by the Club Captain.",
		"The allegation was made by the Club Captain.",
		"The Club Captain reported the matter.",
	} {
		if !ContainsPrivateIdentity(body, "Jo Li / Club Captain") {
			t.Errorf("contextual reporter role was not protected in %q", body)
		}
	}
	if ContainsPrivateIdentity("The club captain may submit an appeal for the offending club.", "Jo Li / Club Captain") {
		t.Fatal("ordinary offending-club role wording created a false identity disclosure")
	}
}
