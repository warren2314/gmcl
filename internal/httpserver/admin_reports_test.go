package httpserver

import "testing"

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
