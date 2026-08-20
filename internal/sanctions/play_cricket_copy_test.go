package sanctions

import "testing"

func TestPlayCricketHelpCopyRecipientIsCanonicalAndDeduplicated(t *testing.T) {
	seen := map[string]bool{"secretary@droylsden.example": true}
	recipients := []string{"secretary@droylsden.example"}
	var err error
	recipients, err = appendUniqueOutcomeRecipient(recipients, seen, PlayCricketHelpCopyRecipient)
	if err != nil {
		t.Fatal(err)
	}
	recipients, err = appendUniqueOutcomeRecipient(recipients, seen, "PLAYCRICKETHELP@GTRMCRCRICKET.CO.UK")
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 2 || recipients[1] != PlayCricketHelpCopyRecipient {
		t.Fatalf("offending-club recipients = %#v", recipients)
	}
}

func TestIneligibleOutcomeDoesNotEmailJoepRoutes(t *testing.T) {
	if shouldCopyPlayCricketHelpOnOffendingOutcome("ineligible_player") {
		t.Fatal("ineligible-player offending-club outcome still copies Play-Cricket Help")
	}
	if !shouldCopyPlayCricketHelpOnOffendingOutcome("manual") {
		t.Fatal("unrelated sanction outcome unexpectedly lost the configured copy")
	}
	for _, recipient := range []string{joepIneligibleOutcomeRecipient, PlayCricketHelpCopyRecipient} {
		if shouldIncludeReporterOutcomeRecipient("ineligible_player", recipient) {
			t.Fatalf("ineligible-player reporting outcome still includes %s", recipient)
		}
		if !shouldIncludeReporterOutcomeRecipient("manual", recipient) {
			t.Fatalf("unrelated sanction outcome unexpectedly excludes %s", recipient)
		}
	}
	if !shouldIncludeReporterOutcomeRecipient("ineligible_player", "club-reporter@example.test") {
		t.Fatal("unrelated ineligible-player reporter was excluded")
	}
}
