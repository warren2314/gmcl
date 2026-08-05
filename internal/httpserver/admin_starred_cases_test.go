package httpserver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSelectStarredFixtureSideRequiresExactUnambiguousPlayCricketSide(t *testing.T) {
	breach := sampleStarredBreach()
	home := starredFixtureSide{
		Side: "home", TeamPCID: " 10011 ", ClubName: "Example Cricket Club", TeamName: "Example CC - 2nd XI",
	}
	away := starredFixtureSide{
		Side: "away", TeamPCID: "10021", ClubName: "Another CC", TeamName: "Another CC - 2nd XI",
	}
	selected, err := selectStarredFixtureSide(breach, home, away)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Side != "home" || selected.TeamPCID != "10011" {
		t.Fatalf("selected side = %#v, want exact home Play-Cricket team", selected)
	}

	selected, err = selectStarredFixtureSide(breach, away, home)
	if err != nil || selected.Side != "home" {
		t.Fatalf("selection must follow side identity rather than argument position: selected=%#v err=%v", selected, err)
	}

	_, err = selectStarredFixtureSide(breach, home, starredFixtureSide{
		Side: "away", TeamPCID: "10012", ClubName: home.ClubName, TeamName: home.TeamName,
	})
	var exception *starredCaseExceptionError
	if !errors.As(err, &exception) || !strings.Contains(err.Error(), "more than one possible side") {
		t.Fatalf("ambiguous fixture side error = %v", err)
	}

	_, err = selectStarredFixtureSide(breach, starredFixtureSide{
		Side: "home", TeamPCID: "10011", ClubName: home.ClubName, TeamName: "Example CC - 3rd XI",
	}, away)
	if !errors.As(err, &exception) || !strings.Contains(err.Error(), "does not have an exact side") {
		t.Fatalf("mismatched fixture team error = %v", err)
	}

	home.TeamPCID = ""
	_, err = selectStarredFixtureSide(breach, home, away)
	if !errors.As(err, &exception) || !strings.Contains(err.Error(), "has no team ID") {
		t.Fatalf("missing Play-Cricket team ID error = %v", err)
	}
}

func TestStarredCaseProvenanceIsStableAndPreservesSourceIDs(t *testing.T) {
	breach := sampleStarredBreach()
	breach.Appearance.SeasonYear = 2026
	breach.Appearance.TeamLevel = 2
	breach.Appearance.CompetitionType = "League"
	breach.Appearance.PlayingDay = "Saturday"
	breach.StarredName = "Alexander Player"

	raw, hash, err := starredCaseProvenance(breach)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, secondHash, err := starredCaseProvenance(breach)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(secondRaw) || hash != secondHash || len(hash) != 64 {
		t.Fatalf("provenance is not stable: hash=%q second=%q", hash, secondHash)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	match, _ := payload["match"].(map[string]any)
	player, _ := payload["player"].(map[string]any)
	list, _ := payload["list"].(map[string]any)
	evaluation, _ := payload["evaluation"].(map[string]any)
	if match["play_cricket_match_id"] != float64(breach.Appearance.MatchID) ||
		player["play_cricket_player_id"] != float64(breach.Appearance.PlayerID) ||
		list["type"] != "A" || evaluation["rule"] != "3.5" {
		t.Fatalf("source provenance lost required identifiers: %s", raw)
	}
	if scorecard, _ := match["scorecard_record"].(string); !strings.Contains(scorecard, "7458963") {
		t.Fatalf("scorecard provenance missing match ID: %s", raw)
	}
	if got := starredCaseSourceReference(breach); got != "play-cricket:match:7458963:player:12345" {
		t.Fatalf("source reference = %q", got)
	}
}
