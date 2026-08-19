package httpserver

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestAuthoritativeIneligibleMailboxMigrationIsReviewSafe(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0083_authoritative_ineligible_club_mailboxes.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(raw))
	if got := strings.Count(text, "@gtrmcrcricket.co.uk"); got != 75 {
		t.Fatalf("authoritative mailbox count = %d, want 75", got)
	}
	emailPattern := regexp.MustCompile(`\('([^']+@gtrmcrcricket\.co\.uk)'\)`)
	seen := map[string]bool{}
	for _, match := range emailPattern.FindAllStringSubmatch(text, -1) {
		if seen[match[1]] {
			t.Fatalf("duplicate authoritative mailbox %q", match[1])
		}
		seen[match[1]] = true
	}
	if len(seen) != 75 {
		t.Fatalf("parsed %d authoritative mailboxes, want 75", len(seen))
	}
	for _, required := range []string{
		"match_status in ('pending','matched','unmatched','ambiguous')",
		"count(distinct candidate.club_id)",
		"having count(*)>1",
		"where authority.match_status='matched'",
		"sanction_configuration_events",
	} {
		if !strings.Contains(strings.Join(strings.Fields(text), " "), required) {
			t.Errorf("migration is missing safety guard %q", required)
		}
	}
}

func TestRecipientAdminSurfacesAndResolvesUnmatchedAuthorityRows(t *testing.T) {
	raw, err := os.ReadFile("sanctions_operations.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"Authoritative mailboxes needing admin review",
		"WHERE authority.match_status<>'matched'",
		"UPDATE sanction_club_mailbox_authority",
		"reviewed_by_admin_id",
		"candidate_club_ids=ARRAY[$1::integer]",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("recipient administration is missing %q", required)
		}
	}
}
