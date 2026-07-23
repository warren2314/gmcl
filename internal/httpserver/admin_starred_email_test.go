package httpserver

import (
	"strings"
	"testing"
	"time"
)

func TestStarredClubEmailUsesNormalisedClubKeyAndLeagueDomain(t *testing.T) {
	got, err := starredClubEmail("clifton", "Clifton CC, Lancs")
	if err != nil {
		t.Fatal(err)
	}
	if got != "clifton@gtrmcrcricket.co.uk" {
		t.Fatalf("club email=%q", got)
	}
}

func TestStarredClubEmailFallsBackToClubName(t *testing.T) {
	got, err := starredClubEmail("", "Edgworth CC")
	if err != nil {
		t.Fatal(err)
	}
	if got != "edgworth@gtrmcrcricket.co.uk" {
		t.Fatalf("club email=%q", got)
	}
}

func TestStarredCandidateRequestEmailIncludesEvidence(t *testing.T) {
	row := starredPlayerReviewRow{
		ClubName: "Edgworth CC", PlayerName: "Alex Player",
		Counts: map[int]int{1: 6}, TeamGames: map[int]int{1: 11}, FirstPct: 54.5,
	}
	subject, body := starredCandidateRequestEmail(row, time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC))
	for _, want := range []string{"Alex Player", "Edgworth CC", "30 June 2026", "6", "11", "54.5%", "docs.google.com/forms", "gtrmcrcricket.co.uk/pages/rules-3-5", "review should be reconsidered"} {
		if !strings.Contains(subject+"\n"+body, want) {
			t.Fatalf("request email missing %q:\n%s\n%s", want, subject, body)
		}
	}
}
