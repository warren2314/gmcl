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
