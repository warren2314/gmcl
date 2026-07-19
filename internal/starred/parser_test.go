package starred

import (
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/leagueapi"
)

func TestParsePublishedCSV(t *testing.T) {
	csv := `Club Name,Alpha CC,Beta CC,xx
,Two teams,No form submitted,
List A1,A1-Jane Smith (Pro),A1-Sam Jones,
List A2,A2-John Brown,,
Club Name,Alpha CC,Beta CC,
List B Required?,Large List B,No List B,
List B1,B1-Alice Green,,
Number of Starred Players Submitted,3,1,
Amendment 1,John Brown replaced by Jon Browne (12/06/2026),,
`
	s, err := ParsePublishedCSV(strings.NewReader(csv), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 4 {
		t.Fatalf("entries=%d want 4", len(s.Entries))
	}
	if len(s.Amendments) != 1 {
		t.Fatalf("amendments=%d want 1", len(s.Amendments))
	}
	if s.Entries[0].PlayerName != "Jane Smith" || len(s.Entries[0].Tags) != 1 {
		t.Fatalf("unexpected parsed player: %#v", s.Entries[0])
	}
	if s.Amendments[0].Incoming != "Jon Browne" {
		t.Fatalf("unexpected amendment: %#v", s.Amendments[0])
	}
	if !s.Clubs[1].NoForm || s.Clubs[0].SubmittedCount != 3 {
		t.Fatalf("club metadata not retained: %#v", s.Clubs)
	}
}

func TestBuildPeriodsAppliesFuzzyAmendment(t *testing.T) {
	d := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	s := Snapshot{SeasonYear: 2026, Entries: []Entry{{SeasonYear: 2026, ClubName: "Alpha CC", ClubKey: "alpha", ListType: "B", PlayerName: "Shoaib Hussainkhail", PlayerKey: NormalizeName("Shoaib Hussainkhail")}}, Amendments: []Amendment{{SeasonYear: 2026, ClubName: "Alpha CC", ClubKey: "alpha", Sequence: 1, Date: &d, Outgoing: "Shoaib Hussainkhill", OutgoingKey: NormalizeName("Shoaib Hussainkhill"), Incoming: "Alfie Hurd", IncomingKey: NormalizeName("Alfie Hurd"), RawValue: "Shoaib Hussainkhill replaced by Alfie Hurd (12/06/2026)", Status: "parsed"}}}
	periods, issues := BuildPeriods(s, time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC))
	if len(issues) != 0 {
		t.Fatalf("issues=%#v", issues)
	}
	if len(periods) != 2 || periods[0].ValidTo == nil || periods[1].PlayerName != "Alfie Hurd" {
		t.Fatalf("periods=%#v", periods)
	}
}

func TestBuildPeriodsDeduplicatesPlayerRepeatedInTwoSourceSlots(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	entry := Entry{SeasonYear: 2026, ClubName: "Bolton Deane & Derby CC", ClubKey: "boltondeaneandderby", ListType: "B", PlayerName: "Safwan Patel", PlayerKey: NormalizeName("Safwan Patel")}
	duplicate := entry
	entry.Slot = 1
	duplicate.Slot = 7

	periods, issues := BuildPeriods(Snapshot{SeasonYear: 2026, Entries: []Entry{entry, duplicate}}, start)
	if len(issues) != 0 {
		t.Fatalf("issues=%#v", issues)
	}
	if len(periods) != 1 {
		t.Fatalf("periods=%d want 1: %#v", len(periods), periods)
	}
}

func TestEvaluateLeagueOnlyAndListRules(t *testing.T) {
	start := time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)
	periods := []Period{{SeasonYear: 2026, ClubName: "Alpha CC", ClubKey: "alpha", ListType: "A", PlayerName: "Jane Smith", PlayerKey: NormalizeName("Jane Smith"), ValidFrom: start}}
	apps := []Appearance{
		{MatchID: 1, SeasonYear: 2026, MatchDate: start, CompetitionType: "League", ClubName: "Alpha CC", ClubKey: "alpha", TeamName: "1st XI", TeamLevel: 1, PlayerID: 10, PlayerName: "Jane Smith", PlayerKey: NormalizeName("Jane Smith")},
		{MatchID: 2, SeasonYear: 2026, MatchDate: start.AddDate(0, 0, 7), CompetitionType: "Cup", ClubName: "Alpha CC", ClubKey: "alpha", TeamName: "2nd XI", TeamLevel: 2, PlayerID: 10, PlayerName: "Jane Smith", PlayerKey: NormalizeName("Jane Smith")},
		{MatchID: 3, SeasonYear: 2026, MatchDate: start, CompetitionType: "League", ClubName: "Alpha CC", ClubKey: "alpha", TeamName: "1st XI", TeamLevel: 1, PlayerID: 11, PlayerName: "New Player", PlayerKey: NormalizeName("New Player")},
		{MatchID: 4, SeasonYear: 2026, MatchDate: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), CompetitionType: "League", ClubName: "Alpha CC", ClubKey: "alpha", TeamName: "2nd XI", TeamLevel: 2, PlayerID: 10, PlayerName: "Jane Smith", PlayerKey: NormalizeName("Jane Smith")},
		{MatchID: 5, SeasonYear: 2026, MatchDate: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), CompetitionType: "League", ClubName: "Alpha CC", ClubKey: "alpha", TeamName: "1st XI", TeamLevel: 1, PlayerID: 12, PlayerName: "July Player", PlayerKey: NormalizeName("July Player")},
		{MatchID: 6, SeasonYear: 2026, MatchDate: start, CompetitionType: "League", ClubName: "Alpha CC", ClubKey: "alpha", TeamName: "Under 15", TeamLevel: 0, PlayerID: 13, PlayerName: "Junior Player", PlayerKey: NormalizeName("Junior Player")},
		{MatchID: 7, SeasonYear: 2026, MatchDate: start, CompetitionType: "League", CompetitionName: "GMCL Women's Premier League", ClubName: "Alpha CC", ClubKey: "alpha", TeamName: "Women's 2nd XI", TeamLevel: 2, PlayerID: 10, PlayerName: "Jane Smith", PlayerKey: NormalizeName("Jane Smith")},
	}
	e := Evaluate(periods, apps, nil, time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC))
	if len(e.Breaches) != 1 {
		t.Fatalf("breaches=%d want 1", len(e.Breaches))
	}
	if len(e.Candidates) != 2 {
		t.Fatalf("candidates=%d want 2", len(e.Candidates))
	}
	for _, c := range e.Candidates {
		if c.PlayerName == "Jane Smith" && c.AllLeague != 1 {
			t.Fatalf("cup was included in league denominator: %#v", c)
		}
	}
}

func TestReviewCutoffAdvancesUntilJuly31(t *testing.T) {
	current := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if got := ReviewCutoff(2026, current); !got.Equal(current) {
		t.Fatalf("current cutoff=%s want %s", got, current)
	}
	got := ReviewCutoff(2026, time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	want := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("cutoff=%s want %s", got, want)
	}
}

func TestIsWomensAppearanceUsesCompetitionClubAndTeamLabels(t *testing.T) {
	tests := []Appearance{
		{CompetitionName: "GMCL Women's Premier League"},
		{ClubName: "Example Ladies CC"},
		{TeamName: "Woman's 1st XI"},
		{CompetitionName: "Girls Development League"},
	}
	for _, appearance := range tests {
		if !IsWomensAppearance(appearance) {
			t.Errorf("expected women's appearance: %#v", appearance)
		}
	}
	if IsWomensAppearance(Appearance{CompetitionName: "GMCL Premier League 1", ClubName: "Example CC", TeamName: "1st XI"}) {
		t.Fatal("men's open-age appearance was incorrectly excluded")
	}
}

func TestNormalizeClubAliases(t *testing.T) {
	if NormalizeClub("Blackley CC, Lancs") != NormalizeClub("Blackley CC") {
		t.Fatal("expected Lancs suffix alias")
	}
	if NormalizeClub("Delph & Dobcross CC") != NormalizeClub("Delph and Dobcross CC") {
		t.Fatal("expected ampersand alias")
	}
}

func TestNonNilStringsUsesEmptyPostgresArrayForMissingTags(t *testing.T) {
	got := nonNilStrings(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v; want a non-nil empty slice", got)
	}
}

func TestDedupeScorecardPlayersMergesRepeatedTeamSheetRows(t *testing.T) {
	players := []leagueapi.ScorecardPlayer{
		{Position: 4, PlayerID: 123, PlayerName: "Jane Smith", Captain: true},
		{Position: 4, PlayerID: 123, PlayerName: " Jane Smith ", WicketKeeper: true},
		{Position: 5, PlayerID: 456, PlayerName: "Sam Jones"},
	}
	got := dedupeScorecardPlayers(players)
	if len(got) != 2 {
		t.Fatalf("got %d players; want 2: %#v", len(got), got)
	}
	if !got[0].Captain || !got[0].WicketKeeper {
		t.Fatalf("duplicate flags were not merged: %#v", got[0])
	}
}

func TestSuggestMappingsRecognisesCommonPlayCricketNameVariants(t *testing.T) {
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	periods := []Period{
		{ClubName: "Bolton Deane & Derby CC", ClubKey: "deane", PlayerName: "Firdaush Bahja", PlayerKey: NormalizeName("Firdaush Bahja"), ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Bolton Deane & Derby CC", ClubKey: "deane", PlayerName: "Sarfraz N Patel", PlayerKey: NormalizeName("Sarfraz N Patel"), ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Bolton Deane & Derby CC", ClubKey: "deane", PlayerName: "Haroon Rawat", PlayerKey: NormalizeName("Haroon Rawat"), ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	appearances := []Appearance{
		{ClubKey: "deane", PlayerID: 1, PlayerName: "Firdaush Daud Mohmed Bhaja", PlayerKey: NormalizeName("Firdaush Daud Mohmed Bhaja")},
		{ClubKey: "deane", PlayerID: 2, PlayerName: "Sarfraz Nawaz Patel", PlayerKey: NormalizeName("Sarfraz Nawaz Patel")},
		{ClubKey: "deane", PlayerID: 3, PlayerName: "Safvan Patel", PlayerKey: NormalizeName("Safvan Patel")},
		{ClubKey: "deane", PlayerID: 4, PlayerName: "Hr Rawat", PlayerKey: NormalizeName("Hr Rawat")},
	}

	suggestions := SuggestMappings(periods, appearances, nil, cutoff)
	got := make(map[string]string)
	for _, suggestion := range suggestions {
		got[suggestion.StarredName] = suggestion.CandidateName
	}
	want := map[string]string{
		"Firdaush Bahja":  "Firdaush Daud Mohmed Bhaja",
		"Sarfraz N Patel": "Sarfraz Nawaz Patel",
		"Haroon Rawat":    "Hr Rawat",
	}
	for source, candidate := range want {
		if got[source] != candidate {
			t.Errorf("suggestion for %q=%q want %q; all=%#v", source, got[source], candidate, got)
		}
	}
}

func TestConfirmedMappingRemovesIdentitySuggestion(t *testing.T) {
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	periods := []Period{{ClubName: "Example CC", ClubKey: "example", PlayerName: "Jon Phillips", PlayerKey: NormalizeName("Jon Phillips"), ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}}
	appearances := []Appearance{{ClubKey: "example", PlayerID: 44, PlayerName: "John Phillips", PlayerKey: NormalizeName("John Phillips")}}
	mappings := []IdentityMapping{{ClubKey: "example", StarredPlayerKey: NormalizeName("Jon Phillips"), PlayerID: 44}}

	if got := SuggestMappings(periods, appearances, mappings, cutoff); len(got) != 0 {
		t.Fatalf("confirmed identity was still suggested: %#v", got)
	}
}

func TestSearchAppearanceIdentitiesSearchesAllNamesAndGroupsScorecards(t *testing.T) {
	first := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)
	appearances := []Appearance{
		{MatchID: 10, MatchDate: first, ClubName: "Alpha CC", TeamLevel: 1, PlayerID: 44, PlayerName: "J Phillips"},
		{MatchID: 10, MatchDate: first, ClubName: "Alpha CC", TeamLevel: 1, PlayerID: 44, PlayerName: "John Phillips"},
		{MatchID: 11, MatchDate: last, ClubName: "Beta CC", TeamLevel: 2, PlayerID: 44, PlayerName: "John Phillips"},
		{MatchID: 12, MatchDate: last, ClubName: "Beta Women", TeamLevel: 1, PlayerID: 55, PlayerName: "Jane Phillips"},
		{MatchID: 13, MatchDate: last, ClubName: "Gamma CC", TeamLevel: 0, PlayerID: 66, PlayerName: "Phil Phillips"},
	}

	got := SearchAppearanceIdentities(appearances, "phillips", 50)
	if len(got) != 1 {
		t.Fatalf("results=%d want 1: %#v", len(got), got)
	}
	if got[0].PlayerID != 44 || got[0].PlayerName != "John Phillips" || got[0].MatchCount != 2 {
		t.Fatalf("unexpected identity summary: %#v", got[0])
	}
	if strings.Join(got[0].ClubNames, ",") != "Alpha CC,Beta CC" || !got[0].FirstSeen.Equal(first) || !got[0].LastSeen.Equal(last) {
		t.Fatalf("unexpected identity evidence: %#v", got[0])
	}
	byID := SearchAppearanceIdentities(appearances, "44", 50)
	if len(byID) != 1 || byID[0].PlayerID != 44 {
		t.Fatalf("player ID search failed: %#v", byID)
	}
}
