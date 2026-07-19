package httpserver

import (
	"strings"
	"testing"

	"cricket-ground-feedback/internal/starred"
)

func sampleStarredCandidate() starred.Candidate {
	return starred.Candidate{
		ClubName: "Example CC", ClubKey: "example", PlayerID: 12345,
		PlayerName: "Alex Player", PlayerKey: "alexplayer",
		FirstXILeague: 6, AllLeague: 10, Percentage: 0.6,
	}
}

func TestStarredCandidateKeyIsStableAndSeasonSpecific(t *testing.T) {
	candidate := sampleStarredCandidate()
	first := starredCandidateKey(2026, candidate)
	if first == "" || first != starredCandidateKey(2026, candidate) {
		t.Fatalf("candidate key is not stable: %q", first)
	}
	if first == starredCandidateKey(2027, candidate) {
		t.Fatal("candidate key must be season-specific")
	}
	candidate.PlayerID++
	if first == starredCandidateKey(2026, candidate) {
		t.Fatal("different Play-Cricket identities must have different keys")
	}
}

func TestStarredCandidateActionsOfferReviewAndAccept(t *testing.T) {
	html := starredCandidateActionsHTML(sampleStarredCandidate(), "token", 2026)
	for _, want := range []string{"Review games", "Accept / close", "/candidates/accept", "q=12345", `name="csrf_token" value="token"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("candidate actions do not contain %q: %s", want, html)
		}
	}
}

func TestOutstandingCandidateCountExcludesAcceptedDecision(t *testing.T) {
	accepted := sampleStarredCandidate()
	pending := sampleStarredCandidate()
	pending.PlayerID = 67890
	pending.PlayerName = "Pending Player"
	starredAlready := sampleStarredCandidate()
	starredAlready.PlayerID = 99999
	starredAlready.AlreadyStarred = true
	states := map[string]starredCandidateReviewState{
		starredCandidateKey(2026, accepted): {ID: 1, Status: "accepted"},
	}
	if got := countOutstandingUnstarredCandidates(2026, []starred.Candidate{accepted, pending, starredAlready}, states); got != 1 {
		t.Fatalf("outstanding count=%d want 1", got)
	}
}
