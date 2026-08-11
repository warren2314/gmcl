package httpserver

import (
	"testing"
	"time"
)

func TestStarredWeeklySyncFallbackWindowCoversPlayingSeason(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		at     time.Time
		active bool
	}{
		{time.Date(2026, time.March, 31, 23, 59, 0, 0, loc), false},
		{time.Date(2026, time.April, 1, 0, 0, 0, 0, loc), true},
		{time.Date(2026, time.August, 10, 3, 0, 0, 0, loc), true},
		{time.Date(2026, time.October, 31, 23, 59, 59, 0, loc), true},
		{time.Date(2026, time.November, 2, 23, 59, 59, 0, loc), true},
		{time.Date(2026, time.November, 3, 0, 0, 0, 0, loc), false},
	} {
		if got := starredWeeklySyncWindowActive(test.at, loc); got != test.active {
			t.Errorf("active at %s=%v want %v", test.at, got, test.active)
		}
	}
}

func TestStarredWeeklySyncWindowUsesConfiguredSeasonAndFinalCatchupMonday(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	window := buildStarredWeeklyWindow(
		time.Date(2026, time.April, 18, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC),
		loc,
		true,
	)
	if !window.Configured {
		t.Fatal("configured season window should be marked configured")
	}
	if got := window.SeasonStart; !got.Equal(time.Date(2026, time.April, 18, 0, 0, 0, 0, loc)) {
		t.Fatalf("season start=%s", got)
	}
	if got := window.CatchupEnd; !got.Equal(time.Date(2026, time.September, 21, 23, 59, 59, 0, loc)) {
		t.Fatalf("catch-up end=%s", got)
	}
	for _, test := range []struct {
		at     time.Time
		active bool
	}{
		{time.Date(2026, time.April, 17, 23, 59, 59, 0, loc), false},
		{time.Date(2026, time.April, 18, 0, 0, 0, 0, loc), true},
		{time.Date(2026, time.August, 10, 3, 0, 0, 0, loc), true},
		{time.Date(2026, time.September, 20, 23, 59, 59, 0, loc), true},
		{time.Date(2026, time.September, 21, 3, 0, 0, 0, loc), true},
		{time.Date(2026, time.September, 21, 23, 59, 59, 0, loc), true},
		{time.Date(2026, time.September, 22, 0, 0, 0, 0, loc), false},
	} {
		if got := window.Active(test.at); got != test.active {
			t.Errorf("active at %s=%v want %v", test.at, got, test.active)
		}
	}
}

func TestNextStarredWeeklySyncIsMondayAtThreeLondonTime(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	sunday := time.Date(2026, time.July, 19, 12, 0, 0, 0, loc)
	want := time.Date(2026, time.July, 20, 3, 0, 0, 0, loc)
	if got := nextStarredWeeklySync(sunday, loc); !got.Equal(want) {
		t.Fatalf("next sync=%s want %s", got, want)
	}
	mondayBefore := time.Date(2026, time.July, 20, 2, 59, 0, 0, loc)
	if got := nextStarredWeeklySync(mondayBefore, loc); !got.Equal(want) {
		t.Fatalf("same-day sync=%s want %s", got, want)
	}
	mondayAfter := time.Date(2026, time.July, 20, 3, 1, 0, 0, loc)
	wantNextWeek := time.Date(2026, time.July, 27, 3, 0, 0, 0, loc)
	if got := nextStarredWeeklySync(mondayAfter, loc); !got.Equal(wantNextWeek) {
		t.Fatalf("following sync=%s want %s", got, wantNextWeek)
	}
}

func TestStarredWeeklySyncEnabledFromEnvironment(t *testing.T) {
	t.Setenv("STARRED_WEEKLY_SYNC_ENABLED", "true")
	if !starredWeeklySyncEnabled() {
		t.Fatal("weekly sync should be enabled")
	}
	t.Setenv("STARRED_WEEKLY_SYNC_ENABLED", "false")
	if starredWeeklySyncEnabled() {
		t.Fatal("weekly sync should be disabled")
	}
}
