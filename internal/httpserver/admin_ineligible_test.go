package httpserver

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseIneligibleQueueFiltersDefaultsAndRejectsUnknownValues(t *testing.T) {
	got := parseIneligibleQueueFilters(url.Values{
		"state":       {"not-a-state"},
		"origin":      {"untrusted"},
		"case_status": {"made-up"},
		"age":         {"500 years"},
		"player":      {"  A Player  "},
	})
	if got.State != "open" || got.Origin != "" || got.CaseStatus != "" || got.Age != "" {
		t.Fatalf("unexpected normalised filters: %#v", got)
	}
	if got.Player != "A Player" {
		t.Fatalf("player filter was not trimmed: %q", got.Player)
	}
}

func TestBuildIneligibleQueueQueryUsesArgumentsForUserInput(t *testing.T) {
	filter := ineligibleQueueFilters{
		State:         "linked",
		Origin:        "google_form",
		ReportingClub: "Reporting CC",
		OffendingClub: "Offending CC",
		Team:          "1st XI",
		Player:        "A Player",
		Assignee:      "hussan",
		CaseStatus:    "investigating",
		Age:           "7d",
	}
	query, args := buildIneligibleQueueQuery(filter)
	for _, value := range []string{"Reporting CC", "Offending CC", "1st XI", "A Player", "hussan"} {
		if strings.Contains(query, value) {
			t.Fatalf("query interpolates user input %q: %s", value, query)
		}
	}
	want := []any{"linked", "google_form", "%Reporting CC%", "%Offending CC%", "%1st XI%", "%A Player%", "%hussan%", "investigating"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("query args = %#v, want %#v", args, want)
	}
	if !strings.Contains(query, "interval '7 days'") {
		t.Fatalf("validated age filter missing from query: %s", query)
	}
}

func TestSourceStringFieldReadsExactGoogleFormHeaders(t *testing.T) {
	raw := []byte(`{
		"Email address":"reporter@example.com",
		"Name of defaulting player as shown on scorecard":"Alex Player",
		"Reason you believe the player is ineligible":"Not registered before the deadline",
		"Your Club":"Reporting CC",
		"Your Name & Role at Club/League":"Robin Reporter - Secretary",
		"Your Preferred tel no":"07123 456789"
	}`)
	tests := map[string]string{
		"email":  sourceStringField(raw, "Email address"),
		"player": sourceStringField(raw, "Name of defaulting player as shown on scorecard"),
		"reason": sourceStringField(raw, "Reason you believe the player is ineligible"),
		"club":   sourceStringField(raw, "Your Club"),
		"person": sourceStringField(raw, "Your Name & Role at Club/League"),
		"phone":  sourceStringField(raw, "Your Preferred tel no"),
	}
	want := map[string]string{
		"email":  "reporter@example.com",
		"player": "Alex Player",
		"reason": "Not registered before the deadline",
		"club":   "Reporting CC",
		"person": "Robin Reporter - Secretary",
		"phone":  "07123 456789",
	}
	if !reflect.DeepEqual(tests, want) {
		t.Fatalf("source fields = %#v, want %#v", tests, want)
	}
}

func TestIneligibleDefaultPublicSummaryIncludesFixture(t *testing.T) {
	date := time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC)
	got := ineligibleDefaultPublicSummary("Alex Player", &date)
	for _, want := range []string{"Alex Player", "04 August 2026"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q does not contain %q", got, want)
		}
	}
}
