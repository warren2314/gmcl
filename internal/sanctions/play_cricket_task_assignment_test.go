package sanctions

import (
	"os"
	"strings"
	"testing"
)

func TestLeaguePointsApprovalDoesNotRequireNamedAdministrator(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	approvalSource := string(source)
	for _, forbidden := range []string{
		"LOWER(username)='denverthornton'",
		"active Denver administrator account is required",
	} {
		if strings.Contains(approvalSource, forbidden) {
			t.Fatalf("league-points approval still depends on named administrator: %q", forbidden)
		}
	}
	for _, required := range []string{
		"recipient.recipient_role='play_cricket'",
		"LOWER(BTRIM(admin.email))=LOWER(BTRIM(recipient.email))",
		"errors.Is(err, pgx.ErrNoRows)",
		"playCricketAdminID",
	} {
		if !strings.Contains(approvalSource, required) {
			t.Fatalf("league-points follow-up assignment is missing %q", required)
		}
	}
}

func TestIneligibleFinalIssueRequiresPlayCricketSignOffAccount(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	publishSource := string(source)
	for _, required := range []string{
		`if sourceType == "ineligible_player"`,
		"recipient.recipient_role='play_cricket'",
		"only Denver's active Play-Cricket account can give final sign-off",
	} {
		if !strings.Contains(publishSource, required) {
			t.Fatalf("ineligible-player final issue guard is missing %q", required)
		}
	}
}
