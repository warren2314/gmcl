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
			PlayingDay:      "Saturday",
			PlayerID:        12345,
			PlayerName:      "Alex Player",
			PlayerKey:       "alexplayer",
		},
	}
}

func TestGroupStarredBreachesByDayAndDivisionOrder(t *testing.T) {
	makeBreach := func(day, competition string) starred.Breach {
		breach := sampleStarredBreach()
		breach.Appearance.PlayingDay = day
		breach.Appearance.CompetitionName = competition
		return breach
	}
	groups := groupStarredBreaches([]starred.Breach{
		makeBreach("Sunday", "GMCL Sunday Division 1"),
		makeBreach("Saturday", "GMCL Championship"),
		makeBreach("Saturday", "GMCL Premier League 2"),
		makeBreach("Saturday", "GMCL Premier League 1"),
		makeBreach("Saturday", "GMCL Premier League 1"),
	})
	want := []string{
		"Saturday — Premier 1",
		"Saturday — Premier 2",
		"Saturday — Championship",
		"Sunday — Division 1",
	}
	if len(groups) != len(want) {
		t.Fatalf("groups=%d want %d: %#v", len(groups), len(want), groups)
	}
	for index, group := range groups {
		got := group.Day + " — " + group.Division
		if got != want[index] {
			t.Errorf("group %d=%q want %q", index, got, want[index])
		}
	}
	if len(groups[0].Breaches) != 2 {
		t.Fatalf("Premier 1 findings=%d want 2", len(groups[0].Breaches))
	}
	if starredDivisionRank("Division 10") <= starredDivisionRank("Division 2") {
		t.Fatal("numbered divisions must be sorted numerically")
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
