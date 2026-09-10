package sanctions

import (
	"strings"
	"testing"
	"time"
)

func TestScheduledRedAwardUsesTargetSeasonEscalationWithoutMatchCap(t *testing.T) {
	for _, test := range []struct{ before, count, points, after int }{
		{0, 3, 6, 3}, {3, 2, 9, 5}, {1, 1, 2, 2},
	} {
		got, err := calculateScheduledRed(Policy{MaxRedsPerMatch: 1}, LedgerState{TeamRedCount: test.before, ClubRedCount: test.before, MatchRedCount: 9}, test.count)
		if err != nil || got.Suppressed || got.PointsDeduction != test.points || got.TeamRedCountAfter != test.after || got.EffectType != "scheduled_red" {
			t.Fatalf("before=%d count=%d: calculation=%+v error=%v", test.before, test.count, got, err)
		}
	}
	ordinary, err := Calculate(Policy{MaxRedsPerMatch: 1}, LedgerState{MatchRedCount: 1}, CardRequest{Kind: "direct_red"})
	if err != nil || !ordinary.Suppressed {
		t.Fatal("ordinary direct reds must retain the per-match cap")
	}
}

func TestFutureCardFieldsDistinguishDefiniteAndConditionalAwards(t *testing.T) {
	season := int32(2027)
	start := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	points := -6
	for _, test := range []struct {
		name   string
		effect DecisionEffectRequest
		ok     bool
	}{
		{"definite three", DecisionEffectRequest{EffectType: "scheduled_red", TargetSeasonID: &season, StartsAt: &start, RedCardCount: 3}, true},
		{"conditional three", DecisionEffectRequest{EffectType: "suspended_red", TargetSeasonID: &season, StartsAt: &start, RedCardCount: 3, Trigger: "Further registration breach"}, true},
		{"legacy suspended", DecisionEffectRequest{EffectType: "suspended_red"}, true},
		{"missing season", DecisionEffectRequest{EffectType: "scheduled_red", StartsAt: &start, RedCardCount: 3}, false},
		{"missing date", DecisionEffectRequest{EffectType: "scheduled_red", TargetSeasonID: &season, RedCardCount: 3}, false},
		{"missing count", DecisionEffectRequest{EffectType: "scheduled_red", TargetSeasonID: &season, StartsAt: &start}, false},
		{"definite with condition", DecisionEffectRequest{EffectType: "scheduled_red", TargetSeasonID: &season, StartsAt: &start, RedCardCount: 3, Trigger: "Maybe"}, false},
		{"definite marked rescindable", DecisionEffectRequest{EffectType: "scheduled_red", TargetSeasonID: &season, StartsAt: &start, RedCardCount: 3, Rescindable: true}, false},
		{"conditional missing condition", DecisionEffectRequest{EffectType: "suspended_red", TargetSeasonID: &season, StartsAt: &start, RedCardCount: 3}, false},
		{"manual points on card", DecisionEffectRequest{EffectType: "scheduled_red", TargetSeasonID: &season, StartsAt: &start, RedCardCount: 3, Points: &points}, false},
		{"oversized award", DecisionEffectRequest{EffectType: "scheduled_red", TargetSeasonID: &season, StartsAt: &start, RedCardCount: 101}, false},
		{"ordinary card count", DecisionEffectRequest{EffectType: "red_card", RedCardCount: 3}, false},
		{"points redirected to future season", DecisionEffectRequest{EffectType: "points_adjustment", TargetSeasonID: &season, Points: &points}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDecisionEffectFields(test.effect)
			if (err == nil) != test.ok {
				t.Fatalf("error=%v want valid=%v", err, test.ok)
			}
		})
	}
}

func TestTargetCardDatesStayInsideLaterSeason(t *testing.T) {
	start := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 9, 30, 0, 0, 0, 0, time.UTC)
	prior := start.AddDate(-1, 0, 0)
	for _, date := range []time.Time{start, end} {
		if err := validateTargetCardDates(DecisionEffectRequest{StartsAt: &date}, start, end, prior); err != nil {
			t.Fatal(err)
		}
	}
	for _, date := range []time.Time{start.AddDate(0, 0, -1), end.AddDate(0, 0, 1)} {
		if err := validateTargetCardDates(DecisionEffectRequest{StartsAt: &date}, start, end, prior); err == nil {
			t.Fatal("out-of-season date accepted")
		}
	}
	if err := validateTargetCardDates(DecisionEffectRequest{StartsAt: &start}, start, end, start); err == nil {
		t.Fatal("original season accepted")
	}
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	firstDayBST := time.Date(2027, 4, 1, 0, 0, 0, 0, london)
	if err := validateTargetCardDates(DecisionEffectRequest{StartsAt: &firstDayBST}, start, end, prior); err != nil {
		t.Fatalf("first season day at BST midnight: %v", err)
	}
	previousDayBST := firstDayBST.AddDate(0, 0, -1)
	if err := validateTargetCardDates(DecisionEffectRequest{StartsAt: &previousDayBST}, start, end, prior); err == nil {
		t.Fatal("previous civil date accepted")
	}
}

func TestFutureAwardOutcomePreservesCountSeasonDateAndCondition(t *testing.T) {
	start := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	points := 6
	got := approvedEffectSummary([]approvedOutcomeEffect{
		{typeName: "scheduled_red", subjectType: "team", teamName: "1st XI", redCardCount: 3, targetYear: 2027, startsAt: &start, points: &points},
		{typeName: "suspended_red", subjectType: "team", teamName: "2nd XI", redCardCount: 2, targetYear: 2027, startsAt: &start, trigger: "Further registration breach"},
	})
	for _, want := range []string{"Definite award of 3 red cards", "6 card-system points", "target season 2027", "effective 1 April 2027", "apply the card-system deduction once", "2 suspended red cards", "no cards or points applied unless separately activated", "Further registration breach"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}
