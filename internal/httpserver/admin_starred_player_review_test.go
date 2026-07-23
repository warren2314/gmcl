package httpserver

import (
	"net/http"
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
		{MatchID: 1, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 2, PlayerName: "Green Player", PlayerKey: "green"},
		{MatchID: 2, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 2, PlayerName: "Green Player", PlayerKey: "green"},
		{MatchID: 6, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Sunday", TeamLevel: 3, PlayerName: "Green Player", PlayerKey: "green"},
		{MatchID: 7, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Sunday", TeamLevel: 4, PlayerName: "Green Player", PlayerKey: "green"},
		{MatchID: 3, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 3, PlayerName: "Red Player", PlayerKey: "red"},
		{MatchID: 4, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 1, PlayerName: "Unstarred Player", PlayerKey: "unstarred"},
		{MatchID: 5, ClubName: "Alpha CC", ClubKey: "alpha", PlayingDay: "Sunday", TeamLevel: 2, PlayerName: "Red Player", PlayerKey: "red"},
	}
	rows := buildStarredPlayerReviewRows(periods, apps, nil, cutoff, map[string]string{"alpha": "Premier 1"}, []string{"Premier 1"}, "", 50, 25)
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3: %#v", len(rows), rows)
	}
	byPlayer := make(map[string]starredPlayerReviewRow)
	for _, row := range rows {
		byPlayer[row.PlayerName] = row
	}
	if byPlayer["Green Player"].FirstPct != 0 || byPlayer["Green Player"].RulePct != 50 || byPlayer["Green Player"].Signal != "green" {
		t.Fatalf("green row=%#v", byPlayer["Green Player"])
	}
	if byPlayer["Red Player"].Total != 2 || byPlayer["Red Player"].Counts[2] != 1 || byPlayer["Red Player"].Signal != "red" {
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

func TestBuildStarredPlayerReviewRowsUsesTeamFixturesAndDateCutoff(t *testing.T) {
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	periods := []starred.Period{
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "A", PlayerName: "Occasional Player", PlayerKey: "occasional", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	apps := []starred.Appearance{
		{MatchID: 1, MatchDate: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC), ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 1, PlayerName: "Occasional Player", PlayerKey: "occasional"},
		{MatchID: 1, MatchDate: time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC), ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 1, PlayerName: "Regular Player", PlayerKey: "regular"},
		{MatchID: 2, MatchDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 1, PlayerName: "Regular Player", PlayerKey: "regular"},
		{MatchID: 3, MatchDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 1, PlayerName: "Regular Player", PlayerKey: "regular"},
		{MatchID: 4, MatchDate: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 1, PlayerName: "Occasional Player", PlayerKey: "occasional"},
	}
	rows := buildStarredPlayerReviewRows(periods, apps, nil, cutoff, map[string]string{"alpha": "Premier 1"}, []string{"Premier 1"}, "", 50, 25)
	byPlayer := make(map[string]starredPlayerReviewRow)
	for _, row := range rows {
		byPlayer[row.PlayerName] = row
	}
	occasional := byPlayer["Occasional Player"]
	if occasional.Counts[1] != 1 || occasional.TeamGames[1] != 3 || occasional.RulePct < 33.3 || occasional.RulePct > 33.4 {
		t.Fatalf("occasional row should be 1 of 3 team fixtures through cutoff: %#v", occasional)
	}
	if occasional.Signal != "orange" {
		t.Fatalf("signal=%q want orange for 33.3%%", occasional.Signal)
	}
	regular := byPlayer["Regular Player"]
	if regular.FirstPct != 100 || regular.ListType != "" {
		t.Fatalf("unstarred regular player should be 3 of 3 first-XI fixtures: %#v", regular)
	}
}

func TestStarredPlayerReviewCutoffUsesRequestedEarlierDate(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "/admin/starred-players?review_date=2026-06-30", nil)
	if err != nil {
		t.Fatal(err)
	}
	maximum := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	got := starredPlayerReviewCutoff(request, 2026, maximum)
	if got.Format("2006-01-02") != "2026-06-30" {
		t.Fatalf("cutoff=%s want 2026-06-30", got)
	}
}

func TestStarredReviewSelectedDivisionsAcceptsMultipleUniqueValues(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "/admin/starred-players?division=Premier+1&division=Premier+2&division=Premier+1", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := starredReviewSelectedDivisions(request)
	if len(got) != 2 || got[0] != "Premier 1" || got[1] != "Premier 2" {
		t.Fatalf("selected divisions=%#v want Premier 1 and Premier 2", got)
	}
}

func TestBuildStarredPlayerReviewRowsIncludesEverySelectedDivision(t *testing.T) {
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	apps := []starred.Appearance{
		{MatchID: 1, MatchDate: cutoff, ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 1, PlayerName: "Alpha Player", PlayerKey: "alpha-player"},
		{MatchID: 2, MatchDate: cutoff, ClubName: "Beta CC", ClubKey: "beta", TeamLevel: 1, PlayerName: "Beta Player", PlayerKey: "beta-player"},
		{MatchID: 3, MatchDate: cutoff, ClubName: "Gamma CC", ClubKey: "gamma", TeamLevel: 1, PlayerName: "Gamma Player", PlayerKey: "gamma-player"},
	}
	divisions := map[string]string{"alpha": "Premier 1", "beta": "Premier 2", "gamma": "Championship"}
	rows := buildStarredPlayerReviewRows(nil, apps, nil, cutoff, divisions, []string{"Premier 1", "Premier 2"}, "", 50, 25)
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2 selected divisions: %#v", len(rows), rows)
	}
	gotClubs := map[string]bool{}
	for _, row := range rows {
		gotClubs[row.ClubKey] = true
	}
	if !gotClubs["alpha"] || !gotClubs["beta"] || gotClubs["gamma"] {
		t.Fatalf("selected clubs=%#v want alpha and beta only", gotClubs)
	}
}
