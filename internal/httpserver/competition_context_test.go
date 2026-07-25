package httpserver

import (
	"testing"
	"time"
)

func TestCompetitionDateUsesEuropeLondonAtBSTBoundary(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load Europe/London: %v", err)
	}

	// Still 24 July in UTC, but already 25 July in London during BST.
	now := time.Date(2026, time.July, 24, 23, 30, 0, 0, time.UTC)
	if got := competitionDate(now, london); got != "2026-07-25" {
		t.Fatalf("competition date: got %s, want 2026-07-25", got)
	}
}

func TestCompetitionDateUsesEuropeLondonAtGMTBoundary(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load Europe/London: %v", err)
	}

	now := time.Date(2026, time.December, 2, 0, 15, 0, 0, time.UTC)
	if got := competitionDate(now, london); got != "2026-12-02" {
		t.Fatalf("competition date: got %s, want 2026-12-02", got)
	}
}

func TestCompetitionWeekStatusLabelsDoNotRenumberWeeks(t *testing.T) {
	tests := map[competitionWeekStatus]string{
		competitionWeekActive:    "Current competition week",
		competitionWeekUpcoming:  "Next scheduled week",
		competitionWeekCompleted: "Most recently completed week",
	}
	for status, want := range tests {
		if got := competitionWeekStatusLabel(status); got != want {
			t.Errorf("status %q: got %q, want %q", status, got, want)
		}
	}
}
