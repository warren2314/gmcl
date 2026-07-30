package httpserver

import (
	"strings"
	"testing"
)

func TestStarredClubCorrectionEmailUsesClubCCAlias(t *testing.T) {
	got, err := starredClubCorrectionEmail("Wythenshawe CC", "Wythenshawe CC")
	if err != nil {
		t.Fatalf("starredClubCorrectionEmail returned an error: %v", err)
	}
	if want := "wythenshawecc@gtrmcrcricket.co.uk"; got != want {
		t.Fatalf("starredClubCorrectionEmail = %q, want %q", got, want)
	}
}

func TestStarredReplacementDraftTextIncludesReviewAndSafetyWording(t *testing.T) {
	player := starredPlayerReviewRow{ClubName: "Alpha CC", PlayerName: "Alex Player", ListType: "A", Total: 12, RuleGames: 2, RulePct: 16.7, Signal: "red"}
	captain := starredReplacementCaptain{Name: "Casey Captain", Email: "captain@example.test", Team: "1st XI"}
	subject, body := starredReplacementDraftText(player, captain)
	for _, want := range []string{"Alpha CC", "Alex Player"} {
		if !strings.Contains(subject, want) {
			t.Fatalf("subject %q does not contain %q", subject, want)
		}
	}
	for _, want := range []string{"Dear Casey Captain", "2", "12", "16.7%", "does not change the published starred-player list automatically"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body does not contain %q: %s", want, body)
		}
	}
}
