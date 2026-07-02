package httpserver

import (
	"database/sql"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func TestParseSanctionsExportWeekRange(t *testing.T) {
	from, to, err := parseSanctionsExportWeekRange(url.Values{
		"week_from": {"5"},
		"week_to":   {"10"},
	})
	if err != nil {
		t.Fatalf("parseSanctionsExportWeekRange returned error: %v", err)
	}
	if from != 5 || to != 10 {
		t.Fatalf("range = %d-%d, want 5-10", from, to)
	}
}

func TestParseSanctionsExportWeekRangeRejectsInvalidRanges(t *testing.T) {
	tests := []url.Values{
		{},
		{"week_from": {"0"}, "week_to": {"10"}},
		{"week_from": {"11"}, "week_to": {"10"}},
		{"week_from": {"x"}, "week_to": {"10"}},
	}
	for _, q := range tests {
		if _, _, err := parseSanctionsExportWeekRange(q); err == nil {
			t.Fatalf("parseSanctionsExportWeekRange(%v) returned nil error", q)
		}
	}
}

func TestSanctionsExportRecordIncludesVisibleSanctionFields(t *testing.T) {
	loc := time.FixedZone("GMT", 0)
	row := sanctionsExportRow{
		ID:              42,
		Season:          "2026",
		Week:            7,
		MatchDate:       time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
		Club:            "Worsley CC",
		Team:            "1st XI",
		Colour:          "red",
		Reason:          "non_submission",
		Notes:           "Second missed report",
		Status:          "served",
		EmailStatus:     "sent",
		PointsDeduction: sql.NullInt32{Int32: 2, Valid: true},
		IssuedAt:        time.Date(2026, 6, 27, 19, 13, 0, 0, time.UTC),
		IssuedBy:        "admin",
		EmailSentAt:     sql.NullTime{Time: time.Date(2026, 6, 27, 19, 20, 0, 0, time.UTC), Valid: true},
	}

	got := sanctionsExportRecord(row, loc)
	want := []string{
		"42",
		"2026",
		"7",
		"2026-06-13",
		"Worsley CC",
		"1st XI",
		"Red Card",
		"Non-submission",
		"Second missed report",
		"Served",
		"Sent",
		"2",
		"admin",
		"",
		"",
		"",
		"",
		"2026-06-27 19:20",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanctionsExportRecord() = %#v, want %#v", got, want)
	}
}

func TestSanctionsExportHeaderUsesMatchDateInsteadOfProcessedDate(t *testing.T) {
	got := sanctionsExportHeader()
	want := []string{
		"Sanction ID",
		"Season",
		"Week",
		"Match date",
		"Club",
		"Team",
		"Card",
		"Reason",
		"Notes",
		"Status",
		"Email",
		"Points deduction",
		"Processed by",
		"Resolved",
		"Resolved by",
		"Email approved by",
		"Email approved",
		"Email sent",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanctionsExportHeader() = %#v, want %#v", got, want)
	}
}

func TestSanctionsExportFilename(t *testing.T) {
	got := sanctionsExportFilename(
		sanctionsExportFilter{SeasonID: 3, WeekFrom: 5, WeekTo: 10},
		time.Date(2026, 6, 29, 12, 30, 5, 0, time.UTC),
	)
	want := "gmcl-sanctions-season-3-weeks-05-10-20260629-123005.csv"
	if got != want {
		t.Fatalf("sanctionsExportFilename() = %q, want %q", got, want)
	}
}
