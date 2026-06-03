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

func TestLeagueFixtureOperationalSyncDatesIncludeFollowingSunday(t *testing.T) {
	targets := []time.Time{
		time.Date(2026, time.May, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.May, 31, 0, 0, 0, 0, time.UTC),
	}

	dates := leagueFixtureOperationalSyncDates(targets, 8)

	if len(dates) != 10 {
		t.Fatalf("date count: got %d", len(dates))
	}
	if got := dates[0].Format("2006-01-02"); got != "2026-05-30" {
		t.Fatalf("first date: got %s", got)
	}
	if got := dates[len(dates)-1].Format("2006-01-02"); got != "2026-06-08" {
		t.Fatalf("last date: got %s", got)
	}

	foundComingSunday := false
	for _, d := range dates {
		if d.Format("2006-01-02") == "2026-06-07" {
			foundComingSunday = true
			break
		}
	}
	if !foundComingSunday {
		t.Fatal("expected sync window to include coming Sunday 2026-06-07")
	}
}

func TestLeagueFixtureSyncUsesDateTemplate(t *testing.T) {
	if !leagueFixtureSyncUsesDateTemplate("/api/matches?match_date={date}") {
		t.Fatal("expected date template to be detected")
	}
	if leagueFixtureSyncUsesDateTemplate("/api/v2/matches.json?site_id={siteId}&season={season}") {
		t.Fatal("season template should not be treated as date-filtered")
	}
}

func TestLeagueFixtureSyncYearsSortedUnique(t *testing.T) {
	dates := []time.Time{
		time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.June, 7, 0, 0, 0, 0, time.UTC),
	}

	years := leagueFixtureSyncYears(dates)
	if len(years) != 2 || years[0] != 2026 || years[1] != 2027 {
		t.Fatalf("unexpected years: %v", years)
	}
}

func TestBuildSanctionEmailYellowUsesNonSubmissionWording(t *testing.T) {
	matchDate := time.Date(2026, time.May, 10, 0, 0, 0, 0, time.UTC)

	subject, body := buildSanctionEmail("yellow", "Example CC", "Example CC - 1st XI", matchDate, 0)

	if subject != "GMCL Notice of Yellow Card - Example CC, Example CC - 1st XI" {
		t.Fatalf("subject: got %q", subject)
	}
	for _, want := range []string{
		"Notification of issue of yellow card for non-submission of captain's report.",
		"Required by: 13 May 2026 at 23:59",
		"Received: Not received by the deadline",
		"The 3rd penalty will be a red card with a 1 point deduction.",
		"This sanction is non-appealable.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestBuildSanctionEmailRedUsesPenaltyWording(t *testing.T) {
	matchDate := time.Date(2026, time.May, 9, 0, 0, 0, 0, time.UTC)

	subject, body := buildSanctionEmail("red", "Example CC", "Example CC - 1st XI", matchDate, 2)

	if subject != "GMCL Notice of Red Card - Example CC, Example CC - 1st XI" {
		t.Fatalf("subject: got %q", subject)
	}
	for _, want := range []string{
		"Notification of issue of red card for non-submission of captain's report.",
		"Required by: 13 May 2026 at 23:59",
		"Received: Not received by the deadline",
		"this is the 6th yellow card penalty",
		"Points deduction: 2 point(s)",
		"This sanction is non-appealable.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}
