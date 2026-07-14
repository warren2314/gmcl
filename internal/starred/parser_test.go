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

func TestReviewCutoffStopsAtJune30(t *testing.T) {
	got := ReviewCutoff(2026, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))
	want := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("cutoff=%s want %s", got, want)
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
