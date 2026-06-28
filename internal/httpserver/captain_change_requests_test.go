package httpserver

import (
	"testing"
	"time"
)

func TestParseCaptainChangeDatesPermanent(t *testing.T) {
	from, to, err := parseCaptainChangeDates(captainChangePermanent, "2026-07-01", "")
	if err != nil {
		t.Fatalf("parseCaptainChangeDates returned error: %v", err)
	}
	if from.Format("2006-01-02") != "2026-07-01" {
		t.Fatalf("unexpected from date: %s", from.Format("2006-01-02"))
	}
	if to != nil {
		t.Fatalf("permanent change should not return an end date")
	}
}

func TestParseCaptainChangeDatesTemporary(t *testing.T) {
	from, to, err := parseCaptainChangeDates(captainChangeTemporary, "2026-07-01", "2026-07-14")
	if err != nil {
		t.Fatalf("parseCaptainChangeDates returned error: %v", err)
	}
	if from.Format("2006-01-02") != "2026-07-01" {
		t.Fatalf("unexpected from date: %s", from.Format("2006-01-02"))
	}
	if to == nil || to.Format("2006-01-02") != "2026-07-14" {
		t.Fatalf("unexpected to date: %v", to)
	}
}

func TestParseCaptainChangeDatesRejectsInvalidTemporaryRange(t *testing.T) {
	if _, _, err := parseCaptainChangeDates(captainChangeTemporary, "2026-07-14", "2026-07-01"); err == nil {
		t.Fatal("expected invalid temporary range to fail")
	}
}

func TestCaptainChangeActiveOnDate(t *testing.T) {
	from := mustDate(t, "2026-07-01")
	to := mustDate(t, "2026-07-14")

	tests := map[string]bool{
		"2026-06-30": false,
		"2026-07-01": true,
		"2026-07-10": true,
		"2026-07-14": true,
		"2026-07-15": false,
	}
	for day, want := range tests {
		if got := captainChangeActiveOnDate(from, &to, mustDate(t, day)); got != want {
			t.Fatalf("captainChangeActiveOnDate(%s) = %v, want %v", day, got, want)
		}
	}
}

func mustDate(t *testing.T, raw string) time.Time {
	t.Helper()
	out, err := time.Parse("2006-01-02", raw)
	if err != nil {
		t.Fatalf("parse date %q: %v", raw, err)
	}
	return out
}
