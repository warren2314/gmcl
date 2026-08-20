package sanctions

import (
	"strings"
	"testing"
)

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

func TestYellowConversionCarriesRemainder(t *testing.T) {
	got, err := Calculate(Policy{}, LedgerState{YellowBalance: 4, TeamRedCount: 1}, CardRequest{Kind: "yellow"})
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectType != "red_card" || got.YellowBalanceAfter != 2 || got.ConsumedYellowCount != 3 {
		t.Fatalf("unexpected carried balance: %+v", got)
	}
}

func TestDirectRedUsesOrdinalPoints(t *testing.T) {
	got, _ := Calculate(Policy{}, LedgerState{TeamRedCount: 1}, CardRequest{Kind: "direct_red"})
	if got.EffectType != "red_card" || got.PointsDeduction != 2 || got.TeamRedCountAfter != 2 {
		t.Fatalf("unexpected calculation: %+v", got)
	}
	if !strings.Contains(got.Explanation, "adds 1 direct red card") || !strings.Contains(got.Explanation, "before this case: 1") || !strings.Contains(got.Explanation, "points to deduct: 2") {
		t.Fatalf("unexpected calculation: %+v", got)
	}
}

func TestAuthoritativeCardBalanceDoesNotAddLegacyDuplicate(t *testing.T) {
	tests := []struct {
		name             string
		ledger           int
		hasLedgerEntries bool
		legacy           int
		want             int
	}{
		{name: "canonical ledger replaces duplicate legacy projection", ledger: 1, hasLedgerEntries: true, legacy: 1, want: 1},
		{name: "canonical reversal remains authoritative", ledger: 0, hasLedgerEntries: true, legacy: 1, want: 0},
		{name: "legacy is opening-balance fallback", ledger: 0, hasLedgerEntries: false, legacy: 2, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authoritativeCardBalance(test.ledger, test.hasLedgerEntries, test.legacy); got != test.want {
				t.Fatalf("authoritativeCardBalance() = %d, want %d", got, test.want)
			}
		})
	}
}
func TestSuspendedRedDoesNotCountUntilActivation(t *testing.T) {
	got, _ := Calculate(Policy{}, LedgerState{TeamRedCount: 1}, CardRequest{Kind: "suspended_red"})
	if got.Status != "suspended" || got.TeamRedCountAfter != 1 || got.PointsDeduction != 0 {
		t.Fatalf("unexpected calculation: %+v", got)
	}
}

func TestSuspendedRedActivationUsesCurrentOrdinal(t *testing.T) {
	got, err := Calculate(Policy{}, LedgerState{TeamRedCount: 2, ClubRedCount: 2}, CardRequest{Kind: "activate_suspended_red"})
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectType != "red_card" || got.PointsDeduction != 3 || !got.CreateBoardReviewTask {
		t.Fatalf("unexpected activation: %+v", got)
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
