package httpserver

import (
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/starred"
)

func sampleStarredBreach() starred.Breach {
	return starred.Breach{
		ListType: "A",
		Appearance: starred.Appearance{
			MatchID:         7458963,
			MatchDate:       time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
			ClubName:        "Example CC",
			ClubKey:         "example",
			TeamName:        "Example CC 2nd XI",
			CompetitionName: "Division Two",
			PlayerID:        12345,
			PlayerName:      "Alex Player",
			PlayerKey:       "alexplayer",
		},
	}
}

func TestStarredFindingKeyIsStableAndPlayerSpecific(t *testing.T) {
	breach := sampleStarredBreach()
	first := starredFindingKey(breach)
	if first == "" || first != starredFindingKey(breach) {
		t.Fatalf("finding key is not stable: %q", first)
	}
	breach.Appearance.PlayerID++
	if first == starredFindingKey(breach) {
		t.Fatal("different players in a match must have different finding keys")
	}
}

func TestStarredFindingActionsRequireSeparateDraftAndApproval(t *testing.T) {
	breach := sampleStarredBreach()
	pending := starredFindingActionsHTML(breach, starredFindingState{}, "token", 2026)
	for _, want := range []string{"Accept / close", "Not accepted", "/findings/escalate"} {
		if !strings.Contains(pending, want) {
			t.Fatalf("pending actions do not contain %q: %s", want, pending)
		}
	}
	draft := starredFindingActionsHTML(breach, starredFindingState{ID: 42, Status: "draft"}, "token", 2026)
	if !strings.Contains(draft, "/findings/42") || strings.Contains(draft, "approve") {
		t.Fatalf("draft state should link to review without approving inline: %s", draft)
	}
}

func TestStarredFindingDraftIncludesOffenceAndScorecardEvidence(t *testing.T) {
	breach := sampleStarredBreach()
	subject, body := starredFindingDraft(breach, starredCaptain{Name: "Casey Captain"}, "Example CC — 2nd XI:\n- Alex Player")
	if !strings.Contains(subject, "Example CC") {
		t.Fatalf("subject does not identify the club: %s", subject)
	}
	for _, want := range []string{"Dear Casey Captain", "Rule 3.5", "List A", "Potential offence", "23 May 2026", "7458963", "Alex Player", "Scorecard evidence"} {
		if !strings.Contains(body, want) {
			t.Fatalf("letter does not contain %q:\n%s", want, body)
		}
	}
}
