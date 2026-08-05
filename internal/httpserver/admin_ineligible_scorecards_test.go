package httpserver

import (
	"errors"
	"testing"
	"time"

	"cricket-ground-feedback/internal/leagueapi"
)

func TestValidateScorecardForCase(t *testing.T) {
	date := time.Date(2026, time.August, 2, 0, 0, 0, 0, time.UTC)
	match := leagueapi.ScorecardMatch{
		MatchID:      "7458963",
		MatchDate:    "02/08/2026",
		HomeTeamID:   "111",
		AwayTeamID:   "222",
		HomeTeamName: "Home 1st XI",
		AwayTeamName: "Away 1st XI",
	}
	if err := validateScorecardForCase(match, 7458963, "222", date); err != nil {
		t.Fatalf("valid scorecard rejected: %v", err)
	}
	if err := validateScorecardForCase(match, 7458964, "222", date); err == nil {
		t.Fatal("different match ID was accepted")
	}
	if err := validateScorecardForCase(match, 7458963, "999", date); err == nil {
		t.Fatal("scorecard without mapped team was accepted")
	}
	wrongDate := date.AddDate(0, 0, 1)
	if err := validateScorecardForCase(match, 7458963, "222", wrongDate); err == nil {
		t.Fatal("scorecard on a different date was accepted")
	}
}

func TestScorecardCollectionMessage(t *testing.T) {
	if got := scorecardCollectionMessage(errScorecardFixtureMissing); got == "" {
		t.Fatal("missing fixture should produce an actionable message")
	}
	if got := scorecardCollectionMessage(errScorecardFixtureAmbiguous); got == "" {
		t.Fatal("ambiguous fixture should produce an actionable message")
	}
	if got := scorecardCollectionMessage(errors.New("upstream unavailable")); got == "" {
		t.Fatal("upstream error should produce a message")
	}
}
