package httpserver

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAdminSubmissionsExportRecord(t *testing.T) {
	row := adminSubmissionSearchRow{
		ID:          123,
		Club:        "Radcliffe CC",
		Team:        "1st XI",
		Captain:     "Alex Captain",
		Week:        9,
		MatchDate:   time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC),
		SubmittedAt: time.Date(2026, 6, 21, 18, 45, 0, 0, time.UTC),
	}

	got := adminSubmissionsExportRecord(row)
	want := []string{
		"123",
		"Radcliffe CC",
		"1st XI",
		"Alex Captain",
		"9",
		"2026-06-20",
		"2026-06-21 18:45",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adminSubmissionsExportRecord() = %#v, want %#v", got, want)
	}
}

func TestAdminSubmissionsExportHeader(t *testing.T) {
	got := adminSubmissionsExportHeader()
	want := []string{
		"Submission ID",
		"Club",
		"Team",
		"Captain",
		"Week",
		"Match date",
		"Submitted",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adminSubmissionsExportHeader() = %#v, want %#v", got, want)
	}
}

func TestAdminSubmissionsExportFilename(t *testing.T) {
	got := adminSubmissionsExportFilename(
		"Radcliffe CC",
		time.Date(2026, 7, 2, 10, 11, 12, 0, time.UTC),
	)
	want := "gmcl-submissions-radcliffe-cc-20260702-101112.csv"
	if got != want {
		t.Fatalf("adminSubmissionsExportFilename() = %q, want %q", got, want)
	}
}

func TestAdminSubmissionsExportButtonPreservesClubFilter(t *testing.T) {
	got := adminSubmissionsExportButton("Radcliffe CC")
	if !strings.Contains(got, `/admin/submissions/export.csv?club=Radcliffe+CC`) {
		t.Fatalf("export button did not preserve club filter: %s", got)
	}
}
