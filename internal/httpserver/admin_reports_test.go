package httpserver

import (
	"testing"
	"time"
)

func TestParseReportWeekPeriod(t *testing.T) {
	tests := []struct {
		name     string
		period   string
		wantYear int
		wantWeek int
		wantOK   bool
	}{
		{name: "standard", period: "2026-W10", wantYear: 2026, wantWeek: 10, wantOK: true},
		{name: "lowercase", period: "2026-w10", wantYear: 2026, wantWeek: 10, wantOK: true},
		{name: "stored executive label", period: "2026-W09 Executive", wantYear: 2026, wantWeek: 9, wantOK: true},
		{name: "missing week", period: "2026", wantOK: false},
		{name: "invalid week", period: "2026-W00", wantOK: false},
		{name: "auto", period: "Auto", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotYear, gotWeek, gotOK := parseReportWeekPeriod(tt.period)
			if gotOK != tt.wantOK {
				t.Fatalf("ok: got %v want %v", gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotYear != tt.wantYear || gotWeek != tt.wantWeek {
				t.Fatalf("period: got %d-W%02d want %d-W%02d", gotYear, gotWeek, tt.wantYear, tt.wantWeek)
			}
		})
	}
}

func TestExtractWeekNumHandlesExecutiveLabel(t *testing.T) {
	if got := extractWeekNum("2026-W09 Executive"); got != 9 {
		t.Fatalf("week: got %d want 9", got)
	}
}

func TestIsAutoAIExecutivePeriod(t *testing.T) {
	for _, period := range []string{"", "Auto", "latest", "latest completed", "  AUTO  "} {
		if !isAutoAIExecutivePeriod(period) {
			t.Fatalf("%q should be treated as auto", period)
		}
	}
	if isAutoAIExecutivePeriod("2026-W10") {
		t.Fatal("explicit week should not be treated as auto")
	}
}

func TestIsAutoAISeasonToDatePeriod(t *testing.T) {
	for _, period := range []string{"", "Auto", "today", "now", "season to date", "season-to-date", "  AUTO  "} {
		if !isAutoAISeasonToDatePeriod(period) {
			t.Fatalf("%q should be treated as auto", period)
		}
	}
	if isAutoAISeasonToDatePeriod("2026-07-09") {
		t.Fatal("explicit date should not be treated as auto")
	}
}

func TestParseAISeasonToDateAsOfDate(t *testing.T) {
	loc := time.UTC
	want := time.Date(2026, 7, 9, 0, 0, 0, 0, loc)
	for _, period := range []string{"2026-07-09", "2026 season-to-date to 2026-07-09"} {
		t.Run(period, func(t *testing.T) {
			got, err := parseAISeasonToDateAsOfDate(period, loc)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if !got.Equal(want) {
				t.Fatalf("date: got %s want %s", got, want)
			}
		})
	}
}

func TestParseAISeasonToDateAsOfDateAutoAndInvalid(t *testing.T) {
	got, err := parseAISeasonToDateAsOfDate("Auto", time.UTC)
	if err != nil {
		t.Fatalf("auto should not error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("auto date: got %s want zero", got)
	}
	if _, err := parseAISeasonToDateAsOfDate("2026-W10", time.UTC); err == nil {
		t.Fatal("weekly period should not parse as a season-to-date date")
	}
}
