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
