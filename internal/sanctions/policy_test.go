package sanctions

import "testing"

func TestThirdYellowConvertsToRed(t *testing.T) {
	got, err := Calculate(Policy{}, LedgerState{YellowBalance: 2, TeamRedCount: 1, ClubRedCount: 2}, CardRequest{Kind: "yellow"})
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectType != "red_card" || got.PointsDeduction != 2 || got.YellowBalanceAfter != 0 || !got.CreateBoardReviewTask {
		t.Fatalf("unexpected calculation: %+v", got)
	}
}

func TestYellowDoesNotConvertEarly(t *testing.T) {
	got, err := Calculate(Policy{}, LedgerState{YellowBalance: 1}, CardRequest{Kind: "yellow"})
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectType != "yellow_card" || got.YellowBalanceAfter != 2 {
		t.Fatalf("unexpected calculation: %+v", got)
	}
}

func TestDirectRedUsesOrdinalPoints(t *testing.T) {
	got, _ := Calculate(Policy{}, LedgerState{TeamRedCount: 2}, CardRequest{Kind: "direct_red"})
	if got.EffectType != "red_card" || got.PointsDeduction != 3 {
		t.Fatalf("unexpected calculation: %+v", got)
	}
}

func TestSuspendedRedDoesNotCountUntilActivation(t *testing.T) {
	got, _ := Calculate(Policy{}, LedgerState{TeamRedCount: 1}, CardRequest{Kind: "suspended_red"})
	if got.Status != "suspended" || got.TeamRedCountAfter != 1 || got.PointsDeduction != 0 {
		t.Fatalf("unexpected calculation: %+v", got)
	}
}

func TestPerMatchMaximum(t *testing.T) {
	got, _ := Calculate(Policy{}, LedgerState{MatchRedCount: 1}, CardRequest{Kind: "direct_red"})
	if !got.Suppressed || got.EffectType != "no_action" {
		t.Fatalf("unexpected calculation: %+v", got)
	}
}

func TestRescindableYellowSurvivesMatchRed(t *testing.T) {
	got, _ := Calculate(Policy{}, LedgerState{MatchRedCount: 1}, CardRequest{Kind: "yellow", Rescindable: true})
	if got.Suppressed || got.EffectType != "yellow_card" || got.Status != "suspended" {
		t.Fatalf("unexpected calculation: %+v", got)
	}
	if got.YellowBalanceAfter != 0 {
		t.Fatalf("rescindable yellow entered effective balance: %+v", got)
	}
}
