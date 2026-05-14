package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestResolveGenerateSanctionsDates_WeekendStartQuery(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/internal/generate-sanctions?weekend_start=2026-05-09", nil)

	sat, sun, source, err := resolveGenerateSanctionsDates(r, loc)
	if err != nil {
		t.Fatalf("resolve dates: %v", err)
	}
	if source != "weekend_start" {
		t.Fatalf("source: got %s", source)
	}
	if got := sat.Format("2006-01-02"); got != "2026-05-09" {
		t.Fatalf("saturday: got %s", got)
	}
	if got := sun.Format("2006-01-02"); got != "2026-05-10" {
		t.Fatalf("sunday: got %s", got)
	}
}

func TestResolveGenerateSanctionsDates_MatchDateBody(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	body := strings.NewReader(`{"match_date":"2026-05-10"}`)
	r := httptest.NewRequest(http.MethodPost, "/internal/generate-sanctions", body)

	sat, sun, source, err := resolveGenerateSanctionsDates(r, loc)
	if err != nil {
		t.Fatalf("resolve dates: %v", err)
	}
	if source != "match_date" {
		t.Fatalf("source: got %s", source)
	}
	if got := sat.Format("2006-01-02"); got != "2026-05-09" {
		t.Fatalf("saturday: got %s", got)
	}
	if got := sun.Format("2006-01-02"); got != "2026-05-10" {
		t.Fatalf("sunday: got %s", got)
	}
}

func TestResolveGenerateSanctionsDates_RejectsNonSaturdayWeekendStart(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/internal/generate-sanctions?weekend_start=2026-05-10", nil)

	if _, _, _, err := resolveGenerateSanctionsDates(r, loc); err == nil {
		t.Fatal("expected non-Saturday weekend_start to fail")
	}
}
