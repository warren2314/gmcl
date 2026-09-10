package httpserver

import (
	"strings"
	"testing"
	"time"
)

func TestPublicScheduledAndSuspendedRedsRemainDistinct(t *testing.T) {
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	start := time.Date(2027, time.April, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, time.September, 30, 0, 0, 0, 0, time.UTC)
	if got := publicSanctionEffectLabel("scheduled_red", 3); got != "3 scheduled red cards" {
		t.Fatalf("scheduled card count: %q", got)
	}
	if got := publicSanctionEffectStatus("active", &start, nil, now); got != "Scheduled" {
		t.Fatalf("future definite award: %q", got)
	}
	if got := publicSanctionEffectStatus("active", &start, nil, start); got != "Active" {
		t.Fatalf("effective definite award: %q", got)
	}
	for _, date := range []time.Time{now, start, start.Add(24 * time.Hour)} {
		if got := publicSanctionEffectStatus("suspended", &start, &end, date); got != "Conditional — not applied" {
			t.Fatalf("conditional award on %s: %q", date, got)
		}
	}
	if got := publicSanctionEffectStatus("overturned", &start, &end, end.Add(time.Hour)); got != "overturned" {
		t.Fatalf("overturned award must retain outcome: %q", got)
	}
	if got := publicSanctionPoints("scheduled_red", 6); got != "6 points to deduct from the effective date" {
		t.Fatalf("scheduled points: %q", got)
	}
	if got := publicSanctionPoints("suspended_red", 0); !strings.Contains(got, "unless") {
		t.Fatalf("conditional points look immediate: %q", got)
	}
	if got := publicSanctionCondition("suspended_red", "Another red card in 2027"); !strings.Contains(got, "does not activate") || !strings.Contains(got, "Another red card in 2027") {
		t.Fatalf("conditional explanation incomplete: %q", got)
	}
	if got := publicSanctionCondition("scheduled_red", ""); got != "" {
		t.Fatalf("definite award labelled conditional: %q", got)
	}
}

func TestPublicPointsAdjustmentsExplainSignedValues(t *testing.T) {
	for _, example := range []struct {
		effect string
		points int
		want   string
	}{
		{"points_adjustment", -41, "41 points deducted"},
		{"points_adjustment", -6, "6 points deducted"},
		{"points_adjustment", 2, "2 points added"},
		{"red_card", 1, "1 point deducted"},
	} {
		if got := publicSanctionPoints(example.effect, example.points); got != example.want {
			t.Errorf("%s %d: got %q, want %q", example.effect, example.points, got, example.want)
		}
	}
	if got := publicEffectSubject("scheduled_red", "First XI", "Case player"); got != "First XI" {
		t.Fatalf("scheduled team award subject: %q", got)
	}
}

func TestSanctionsLookupIncludesScheduledCountSeasonAndConditionalTiming(t *testing.T) {
	line := sanctionRecordLine(sanctionRecordRow{
		Ref: "S-2027", Effect: "scheduled_red", RedCardCount: 3,
		Season: "2027", Status: "Scheduled", Date: "01 Apr 2027", Points: 6,
		Team: "First XI", Reason: "Meeting decision",
	}, true)
	for _, want := range []string{"3 scheduled red cards", "season 2027", "Scheduled", "01 Apr 2027", "6-point deduction", "First XI"} {
		if !strings.Contains(line, want) {
			t.Fatalf("scheduled lookup missing %q: %s", want, line)
		}
	}
	kinds, _ := sanctionKindFilter("Show our red cards")
	if rows := filterSanctionRowsByKind([]sanctionRecordRow{{Effect: "scheduled_red"}}, kinds); len(rows) != 1 {
		t.Fatal("scheduled red cards omitted from card lookup")
	}
	conditional := sanctionRecordLine(sanctionRecordRow{
		Effect: "suspended_red", RedCardCount: 2, Status: "Conditional — not applied",
		Season: "2027", Date: "01 Apr 2027", Condition: "Another offence in 2027",
	}, false)
	for _, want := range []string{"2 suspended red cards", "Conditional", "season 2027", "does not activate", "Another offence in 2027"} {
		if !strings.Contains(conditional, want) {
			t.Fatalf("conditional lookup missing %q: %s", want, conditional)
		}
	}
}
