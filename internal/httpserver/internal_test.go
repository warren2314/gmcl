package httpserver

import (
	"testing"
	"time"
)

func TestMostRecentWeekendDates_ThursdayRerun(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	sat, sun := mostRecentWeekendDates(time.Date(2026, time.May, 14, 12, 0, 0, 0, loc), loc)

	if got := sat.Format("2006-01-02"); got != "2026-05-09" {
		t.Fatalf("saturday: got %s", got)
	}
	if got := sun.Format("2006-01-02"); got != "2026-05-10" {
		t.Fatalf("sunday: got %s", got)
	}
}

func TestMostRecentWeekendDates_WednesdaySchedule(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	sat, sun := mostRecentWeekendDates(time.Date(2026, time.May, 13, 23, 59, 0, 0, loc), loc)

	if got := sat.Format("2006-01-02"); got != "2026-05-09" {
		t.Fatalf("saturday: got %s", got)
	}
	if got := sun.Format("2006-01-02"); got != "2026-05-10" {
		t.Fatalf("sunday: got %s", got)
	}
}
