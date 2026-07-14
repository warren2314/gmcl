package httpserver

import (
	"testing"
	"time"

	"cricket-ground-feedback/internal/starred"
)

func TestBuildStarredPlayerReviewRowsCalculatesPercentagesAndSignals(t *testing.T) {
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	periods := []starred.Period{
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "A", PlayerName: "Red Player", PlayerKey: "red", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "B", PlayerName: "Green Player", PlayerKey: "green", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	apps := []starred.Appearance{
		{MatchID: 1, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 1, PlayerName: "Green Player", PlayerKey: "green"},
		{MatchID: 2, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 2, PlayerName: "Green Player", PlayerKey: "green"},
		{MatchID: 3, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 3, PlayerName: "Red Player", PlayerKey: "red"},
		{MatchID: 4, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 1, PlayerName: "Unstarred Player", PlayerKey: "unstarred"},
		{MatchID: 5, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Sunday", TeamLevel: 1, PlayerName: "Red Player", PlayerKey: "red"},
	}
	rows := buildStarredPlayerReviewRows(periods, apps, nil, cutoff, map[string]string{"alpha": "Premier 1"}, "Premier 1", "", 50, 25)
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3: %#v", len(rows), rows)
	}
	byPlayer := make(map[string]starredPlayerReviewRow)
	for _, row := range rows {
		byPlayer[row.PlayerName] = row
	}
	if byPlayer["Green Player"].FirstPct != 50 || byPlayer["Green Player"].Signal != "green" {
		t.Fatalf("green row=%#v", byPlayer["Green Player"])
	}
	if byPlayer["Red Player"].Total != 1 || byPlayer["Red Player"].Signal != "red" {
		t.Fatalf("red row=%#v", byPlayer["Red Player"])
	}
	if byPlayer["Unstarred Player"].Signal != "neutral" {
		t.Fatalf("unstarred row should remain neutral: %#v", byPlayer["Unstarred Player"])
	}
}

func TestStarredRetentionSignalUsesAdjustableThresholds(t *testing.T) {
	if got := starredRetentionSignal("B", 39.9, 60, 30); got != "orange" {
		t.Fatalf("signal=%q want orange", got)
	}
	if got := starredRetentionSignal("A", 29.9, 60, 30); got != "red" {
		t.Fatalf("signal=%q want red", got)
	}
}
