package sanctions

import (
	"os"
	"strings"
	"testing"
)

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
	for _, recipient := range []string{joepIneligibleOutcomeRecipient} {
		if shouldIncludeReporterOutcomeRecipient("ineligible_player", recipient) {
			t.Fatalf("ineligible-player reporting outcome still includes %s", recipient)
		}
		if shouldIncludeReporterOutcomeRecipient("manual", recipient) {
			t.Fatalf("manual reporting outcome unexpectedly includes %s", recipient)
		}
	}
	if !shouldIncludeReporterOutcomeRecipient("manual", PlayCricketHelpCopyRecipient) {
		t.Fatal("unrelated sanction outcome unexpectedly excludes Play-Cricket copy")
	}
	if shouldIncludeReporterOutcomeRecipient("ineligible_player", PlayCricketHelpCopyRecipient) {
		t.Fatal("ineligible-player reporting outcome unexpectedly includes Play-Cricket copy")
	}
	if !shouldIncludeReporterOutcomeRecipient("manual", "club-reporter@example.test") {
		t.Fatal("unrelated manual reporter was excluded")
	}
}

func TestFinalIssueRefreshesPlayCricketRecipientsForPointOutcomes(t *testing.T) {
	raw, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"currentPlayCricketRecipients",
		"active AND recipient_role='play_cricket'",
		`item.audience == "official" && hasAnyPoints`,
		"appendUniqueOutcomeRecipient(item.recipients, seen, recipient)",
		"'queued',$3::jsonb",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("final issue does not refresh the Play-Cricket recipient snapshot: missing %q", required)
		}
	}
}

func TestStuartPointsEmailBackfillIsOfficialIdempotentAndLiveOnly(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0084_restore_stuart_play_cricket_outcomes.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ToLower(string(raw))
	for _, required := range []string{
		"'play_cricket','stuart russell','playcrickethelp@gtrmcrcricket.co.uk',true",
		"cases.source_type='ineligible_player'",
		"cases.status='published'",
		"not cases.is_test",
		"case_training_designated",
		"coalesce(effect.points,0)<>0",
		"outbox.message_kind='outcome_official'",
		"correspondence.audience='official'",
		"where not exists",
		"on conflict(idempotency_key) do nothing",
		"play_cricket_outcome_backfill_queued",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Stuart points-email repair is missing %q", required)
		}
	}
}
