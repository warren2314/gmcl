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

func TestStarredRemovalReviewEmailUsesRequestedWording(t *testing.T) {
	row := starredPlayerReviewRow{ClubName: "Edgworth CC", PlayerName: "Alex Player"}
	subject, body := starredRemovalReviewEmail(row)
	if subject != starredRemovalReviewSubject {
		t.Fatalf("subject=%q", subject)
	}
	for _, want := range []string{
		"Hi Edgworth CC,",
		"Alex Player has only participated in a limited number of matches",
		"removed from their current list/category",
		"provide details of any suitable replacement players for consideration",
		"Rule 3.5 review deadline is 31 July",
		"docs.google.com/forms",
		"review should be reconsidered",
		"gtrmcrcricket.co.uk/pages/rules-3-5",
		"Kind regards,\nGMCL",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("removal review email missing %q:\n%s", want, body)
		}
	}
}
