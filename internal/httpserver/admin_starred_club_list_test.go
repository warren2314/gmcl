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
