package httpserver

import (
	"testing"
	"time"

	"cricket-ground-feedback/internal/starred"
)

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

func TestSaturdayStarredClubDivisionsUsesFirstXICompetition(t *testing.T) {
	clubs := map[string]string{"alpha": "Alpha CC", "beta": "Beta CC", "gamma": "Gamma CC"}
	appearances := []starred.Appearance{
		{ClubKey: "alpha", PlayingDay: "Saturday", CompetitionType: "League", CompetitionName: "GMCL Premier League 1", TeamLevel: 1},
		{ClubKey: "alpha", PlayingDay: "Saturday", CompetitionType: "League", CompetitionName: "GMCL Division 3", TeamLevel: 2},
		{ClubKey: "beta", PlayingDay: "Saturday", CompetitionType: "League", CompetitionName: "GMCL Championship", TeamLevel: 1},
		{ClubKey: "beta", PlayingDay: "Sunday", CompetitionType: "League", CompetitionName: "Sunday Premier", TeamLevel: 1},
	}
	got := saturdayStarredClubDivisions(clubs, appearances)
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
}
