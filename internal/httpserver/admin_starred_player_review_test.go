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

func TestBuildStarredPlayerReviewRowsCountsAutomaticallyLinkedClubScorecards(t *testing.T) {
	cutoff := time.Date(2026, 7, 27, 23, 59, 59, 0, time.UTC)
	periods := []starred.Period{
		{ClubName: "Flixton C&SC", ClubKey: "flixtoncandsc", ListType: "A", PlayerName: "James Lupton", PlayerKey: "jameslupton", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Micklehurst C&SC", ClubKey: "micklehurstcandsc", ListType: "A", PlayerName: "Tim Wood", PlayerKey: "timwood", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Swinton Moorside CC", ClubKey: "swintonmoorside", ListType: "A", PlayerName: "Alfie Harvey", PlayerKey: "alfieharvey", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Westleigh CC", ClubKey: "westleigh", ListType: "A", PlayerName: "Ethan Welch", PlayerKey: "ethanwelch", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	appearances := []starred.Appearance{
		{MatchID: 1, MatchDate: cutoff, ClubName: "Flixton CC", ClubKey: "flixton", TeamLevel: 1, PlayerName: "James Lupton", PlayerKey: "jameslupton"},
		{MatchID: 2, MatchDate: cutoff, ClubName: "Micklehurst Cricket & Social Club", ClubKey: "micklehurstcricketsocialclub", TeamLevel: 1, PlayerName: "Tim Wood", PlayerKey: "timwood"},
		{MatchID: 3, MatchDate: cutoff, ClubName: "Swinton Moorside CC, Salford", ClubKey: "swintonmoorsidesalford", TeamLevel: 1, PlayerName: "Alfie Harvey", PlayerKey: "alfieharvey"},
		{MatchID: 4, MatchDate: cutoff, ClubName: "Westleigh CC, Leigh", ClubKey: "westleighleigh", TeamLevel: 1, PlayerName: "Ethan Welch", PlayerKey: "ethanwelch"},
	}
	clubNames := activeStarredClubNames(periods, cutoff)
	appearances = remapStarredAppearanceClubs(appearances, nil, clubNames)
	rows := buildStarredPlayerReviewRows(periods, appearances, nil, cutoff, map[string]string{
		"flixtoncandsc":     "Championship",
		"micklehurstcandsc": "division 5 east",
		"swintonmoorside":   "Championship",
		"westleigh":         "Division 1",
	}, []string{"Championship", "division 5 east", "Division 1"}, "", 50, 25)

	byPlayer := make(map[string]starredPlayerReviewRow)
	for _, row := range rows {
		byPlayer[row.PlayerName] = row
	}
	if byPlayer["James Lupton"].Total != 1 || byPlayer["James Lupton"].ClubKey != "flixtoncandsc" {
		t.Fatalf("Flixton scorecard was not counted: %#v", byPlayer["James Lupton"])
	}
	if byPlayer["Tim Wood"].Total != 1 || byPlayer["Tim Wood"].ClubKey != "micklehurstcandsc" {
		t.Fatalf("Micklehurst scorecard was not counted: %#v", byPlayer["Tim Wood"])
	}
	if byPlayer["Alfie Harvey"].Total != 1 || byPlayer["Alfie Harvey"].ClubKey != "swintonmoorside" {
		t.Fatalf("Swinton scorecard was not counted: %#v", byPlayer["Alfie Harvey"])
	}
	if byPlayer["Ethan Welch"].Total != 1 || byPlayer["Ethan Welch"].ClubKey != "westleigh" {
		t.Fatalf("Westleigh scorecard was not counted: %#v", byPlayer["Ethan Welch"])
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

func TestStarredReviewSignalFilterAcceptsOnlyReviewSignals(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "/admin/starred-players?signal=RED", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := starredReviewSignalFilter(request); got != "red" {
		t.Fatalf("signal=%q want red", got)
	}
	request.URL.RawQuery = "signal=neutral"
	if got := starredReviewSignalFilter(request); got != "" {
		t.Fatalf("unsupported signal=%q want empty", got)
	}
}

func TestFilterStarredPlayerReviewRowsKeepsOnlySelectedSignal(t *testing.T) {
	rows := []starredPlayerReviewRow{
		{PlayerName: "Keep", Signal: "green"},
		{PlayerName: "Watch", Signal: "orange"},
		{PlayerName: "Review", Signal: "red"},
	}
	filtered := filterStarredPlayerReviewRows(rows, "red")
	if len(filtered) != 1 || filtered[0].PlayerName != "Review" {
		t.Fatalf("filtered rows=%#v want only removal review", filtered)
	}
	if all := filterStarredPlayerReviewRows(rows, ""); len(all) != len(rows) {
		t.Fatalf("unfiltered rows=%d want %d", len(all), len(rows))
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

func TestBuildStarredPlayerReviewRowsShowsReplacementAndHidesFormerStarredPlayer(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	replacedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	periods := []starred.Period{
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "B", PlayerName: "Former Player", PlayerKey: "former", ValidFrom: start, ValidTo: &replacedAt},
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "B", PlayerName: "Replacement Player", PlayerKey: "replacement", ValidFrom: replacedAt, SourceKind: "amendment", SourceSequence: 1},
	}
	apps := []starred.Appearance{
		{MatchID: 1, MatchDate: start, ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 1, PlayerName: "Former Player", PlayerKey: "former"},
		{MatchID: 2, MatchDate: replacedAt.AddDate(0, 0, 7), ClubName: "Alpha CC", ClubKey: "alpha", TeamLevel: 2, PlayerName: "Unstarred Player", PlayerKey: "unstarred"},
	}

	rows := buildStarredPlayerReviewRows(periods, apps, nil, cutoff, map[string]string{"alpha": "Premier 1"}, []string{"Premier 1"}, "", 50, 25)
	byPlayer := make(map[string]starredPlayerReviewRow)
	for _, row := range rows {
		byPlayer[row.PlayerName] = row
	}
	if _, exists := byPlayer["Former Player"]; exists {
		t.Fatalf("former starred player should be hidden after replacement: %#v", rows)
	}
	if replacement, exists := byPlayer["Replacement Player"]; !exists || replacement.ListType != "B" {
		t.Fatalf("replacement should be shown on List B: %#v", rows)
	}
	if _, exists := byPlayer["Unstarred Player"]; !exists {
		t.Fatalf("ordinary unstarred candidates must remain visible: %#v", rows)
	}
}
