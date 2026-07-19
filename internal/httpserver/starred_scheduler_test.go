package httpserver

import (
	"testing"
	"time"
)

func TestStarredWeeklySyncWindowEndsAfterJuly31(t *testing.T) {
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
		{time.Date(2026, time.July, 31, 23, 59, 59, 0, loc), true},
		{time.Date(2026, time.August, 3, 3, 0, 0, 0, loc), true},
		{time.Date(2026, time.August, 8, 0, 0, 0, 0, loc), false},
	} {
		if got := starredWeeklySyncWindowActive(test.at, loc); got != test.active {
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
