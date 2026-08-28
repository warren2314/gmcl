package httpserver

import (
	"errors"
	"strings"
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

func TestScorecardResultHTMLShowsOutcomeBeneficiaryAndPoints(t *testing.T) {
	match := leagueapi.ScorecardMatch{
		HomeClubName: "Alpha CC", HomeTeamName: "2nd XI", HomeTeamID: "10",
		AwayClubName: "Beta CC", AwayTeamName: "3rd XI", AwayTeamID: "20",
		Result: "W", ResultDescription: "Beta CC - 3rd XI - Win 20pts", ResultAppliedTo: "20",
		Points: []leagueapi.ScorecardPoints{
			{TeamID: "10", GamePoints: "0", BonusPointsBatting: "3"},
			{TeamID: "20", GamePoints: "20", BonusPointsBowling: "1", PenaltyPoints: "0"},
		},
	}
	html := scorecardResultHTML(match)
	for _, want := range []string{
		"Recorded match result",
		"Beta CC - 3rd XI - Win 20pts",
		"Result applied to: <strong>Beta CC - 3rd XI</strong>",
		"Alpha CC - 2nd XI",
		"game 20; bowling bonus 1; penalty 0",
		"whether the offending team benefited",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("result HTML missing %q: %s", want, html)
		}
	}
}

func TestScorecardResultHTMLOmitsEmptyResult(t *testing.T) {
	if got := scorecardResultHTML(leagueapi.ScorecardMatch{}); got != "" {
		t.Fatalf("empty result should not render a result panel: %s", got)
	}
}

func TestScorecardDisplayTeamLabelsIncludeClubAndXI(t *testing.T) {
	home, away := scorecardDisplayTeamLabels(leagueapi.ScorecardMatch{
		HomeClubName: "Micklehurst Cricket & Social Club", HomeTeamName: "3rd XI", HomeTeamID: "10",
		AwayClubName: "Glodwick CC", AwayTeamName: "3rd XI", AwayTeamID: "20",
	})
	if home != "Micklehurst Cricket & Social Club - 3rd XI" || away != "Glodwick CC - 3rd XI" {
		t.Fatalf("scorecard labels = %q v %q", home, away)
	}
}

func TestScorecardPointsReviewRequiresExplicitDecisionCheck(t *testing.T) {
	match := leagueapi.ScorecardMatch{
		HomeClubName: "Glodwick CC", HomeTeamName: "3rd XI", HomeTeamID: "235",
		AwayClubName: "Example CC", AwayTeamName: "3rd XI", AwayTeamID: "999",
		Points: []leagueapi.ScorecardPoints{
			{TeamID: "235", GamePoints: "5", PenaltyPoints: "0"},
			{TeamID: "999", GamePoints: "0"},
		},
	}
	review, ok := scorecardPointsReviewForTeam(match, "235")
	if !ok {
		t.Fatal("positive offending-team points did not require a review")
	}
	html := scorecardPointsReviewHTML(review)
	for _, want := range []string{
		"Glodwick CC - 3rd XI",
		"game 5; penalty 0",
		"A red-card deduction does not remove these match points.",
		`name="league_points_reviewed"`,
		"required",
		"League-table points adjustment",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("points review HTML missing %q: %s", want, html)
		}
	}
}

func TestScorecardPointsReviewIgnoresTeamWithNoAward(t *testing.T) {
	match := leagueapi.ScorecardMatch{Points: []leagueapi.ScorecardPoints{{
		TeamID: "235", GamePoints: "0", BonusPointsBatting: "0", PenaltyPoints: "1",
	}}}
	if _, ok := scorecardPointsReviewForTeam(match, "235"); ok {
		t.Fatal("zero awarded points should not require the forfeiture review")
	}
}
