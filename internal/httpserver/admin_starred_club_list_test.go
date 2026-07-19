package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/starred"
)

func TestRedirectStarredAnchorPreservesReviewSection(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/admin/starred-players/mapping", nil)
	redirectStarredAnchor(recorder, request, 2026, "Confirmed", "", "identity-matches")
	location := recorder.Header().Get("Location")
	if !strings.HasSuffix(location, "#identity-matches") || !strings.Contains(location, "message=Confirmed") {
		t.Fatalf("redirect did not preserve section: %q", location)
	}
}

func TestActiveStarredPeriodsByClubUsesCutoffAndListOrder(t *testing.T) {
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	ended := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	periods := []starred.Period{
		{ClubKey: "alpha", ListType: "B", PlayerName: "Zed Player", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubKey: "beta", ListType: "A", PlayerName: "Other Club", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubKey: "alpha", ListType: "A", PlayerName: "Amy Player", ValidFrom: time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)},
		{ClubKey: "alpha", ListType: "A", PlayerName: "Replaced Player", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), ValidTo: &ended},
	}
	got := activeStarredPeriodsByClub(periods, cutoff, "alpha")
	if len(got) != 2 {
		t.Fatalf("active periods=%d want 2: %#v", len(got), got)
	}
	if got[0].ListType != "A" || got[0].PlayerName != "Amy Player" || got[1].ListType != "B" {
		t.Fatalf("unexpected club list order: %#v", got)
	}
}

func TestActiveUnmappedStarredPeriodsRemovesAcceptedPlayers(t *testing.T) {
	cutoff := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	periods := []starred.Period{
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "A", PlayerName: "Accepted Player", PlayerKey: "accepted", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "B", PlayerName: "Needs Match", PlayerKey: "needsmatch", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ClubName: "Alpha CC", ClubKey: "alpha", ListType: "B", PlayerName: "Needs Match", PlayerKey: "needsmatch", ValidFrom: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	mappings := []starred.IdentityMapping{{ClubKey: "alpha", StarredPlayerKey: "accepted", PlayerID: 99}}

	got := activeUnmappedStarredPeriods(periods, mappings, cutoff)
	if len(got) != 1 || got[0].PlayerKey != "needsmatch" {
		t.Fatalf("unmapped periods=%#v want only Needs Match", got)
	}
	sourceID := starredMappingSourceID(got[0])
	selected, ok := findUnmappedStarredPeriod(got, sourceID)
	if !ok || selected.PlayerName != "Needs Match" {
		t.Fatalf("source selection failed: %#v, %v", selected, ok)
	}
}

func TestSaturdayStarredClubDivisionsUsesFirstXICompetition(t *testing.T) {
	clubs := map[string]string{"alpha": "Alpha CC", "beta": "Beta CC", "gamma": "Gamma CC"}
	appearances := []starred.Appearance{
		{ClubKey: "alpha", PlayingDay: "Saturday", CompetitionType: "League", CompetitionName: "GMCL Premier League 1", TeamLevel: 1},
		{ClubKey: "alpha", PlayingDay: "Saturday", CompetitionType: "League", CompetitionName: "GMCL Division 3", TeamLevel: 2},
		{ClubKey: "beta", PlayingDay: "Saturday", CompetitionType: "League", CompetitionName: "GMCL Championship", TeamLevel: 1},
		{ClubKey: "beta", PlayingDay: "Sunday", CompetitionType: "League", CompetitionName: "Sunday Premier", TeamLevel: 1},
	}
	got := saturdayStarredClubDivisions(clubs, appearances, nil)
	want := []string{"Premier 1", "Championship", "Unassigned / no Saturday division"}
	if len(got) != len(want) {
		t.Fatalf("divisions=%d want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].Label != want[index] {
			t.Errorf("division %d=%q want %q", index, got[index].Label, want[index])
		}
	}
	if len(got[0].Clubs) != 1 || got[0].Clubs[0] != "alpha" {
		t.Fatalf("Alpha was not assigned by its 1st XI division: %#v", got[0])
	}
	overridden := saturdayStarredClubDivisions(clubs, appearances, map[string]string{"alpha": "Division 4"})
	foundOverride := false
	for _, division := range overridden {
		if division.Label == "Division 4" && len(division.Clubs) == 1 && division.Clubs[0] == "alpha" {
			foundOverride = true
		}
	}
	if !foundOverride {
		t.Fatal("manual division override did not replace the inferred assignment")
	}
}

func TestStarredSaturdayTeamCountsUsesMappedIdentityAndDeduplicates(t *testing.T) {
	periods := []starred.Period{
		{ClubKey: "alpha", PlayerKey: "amy", PlayerName: "Amy Player"},
		{ClubKey: "alpha", PlayerKey: "zed", PlayerName: "Zed Player"},
	}
	appearances := []starred.Appearance{
		{MatchID: 1, ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 1, PlayerID: 99, PlayerKey: "different-name"},
		{MatchID: 1, ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 1, PlayerID: 99, PlayerKey: "different-name"},
		{MatchID: 2, ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 1, PlayerID: 99, PlayerKey: "different-name"},
		{MatchID: 3, ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 2, PlayerID: 99, PlayerKey: "different-name"},
		{MatchID: 4, ClubKey: "alpha", PlayingDay: "Sunday", TeamLevel: 2, PlayerID: 99, PlayerKey: "different-name"},
		{MatchID: 5, ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 1, PlayerID: 100, PlayerKey: "amy"},
		{MatchID: 6, ClubKey: "alpha", PlayingDay: "Saturday", TeamLevel: 3, PlayerKey: "zed"},
	}
	mappings := []starred.IdentityMapping{{ClubKey: "alpha", StarredPlayerKey: "amy", PlayerID: 99}}
	got := starredSaturdayTeamCounts(periods, appearances, mappings)
	if got["alpha|amy"][1] != 2 || got["alpha|amy"][2] != 1 {
		t.Fatalf("mapped Amy counts=%#v want 1st=2, 2nd=1", got["alpha|amy"])
	}
	if got["alpha|zed"][3] != 1 {
		t.Fatalf("name-matched Zed counts=%#v want 3rd=1", got["alpha|zed"])
	}
}

func TestRemapStarredAppearanceClubsLinksImportedGamesToPublishedClub(t *testing.T) {
	appearances := []starred.Appearance{{ClubKey: "play-cricket-name", ClubName: "Play Cricket Name", PlayerName: "Player"}}
	got := remapStarredAppearanceClubs(appearances, map[string]string{"published-name": "play-cricket-name"}, map[string]string{"published-name": "Published Name CC"})
	if got[0].ClubKey != "published-name" || got[0].ClubName != "Published Name CC" {
		t.Fatalf("appearance was not remapped: %#v", got[0])
	}
	if appearances[0].ClubKey != "play-cricket-name" {
		t.Fatal("source appearances must not be mutated")
	}
}

func TestStarredAppearanceSearchPrefersMappedPlayerID(t *testing.T) {
	if got := starredAppearanceSearch("Safwan Patel", 6340202); got != "6340202" {
		t.Fatalf("search=%q want player ID", got)
	}
	if got := starredAppearanceSearch("Yahya Adia", 0); got != "Yahya Adia" {
		t.Fatalf("search=%q want published name", got)
	}
}
