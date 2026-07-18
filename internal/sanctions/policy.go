package sanctions

import "fmt"

// Policy is the effective-dated card policy loaded from sanction_policy_versions.
type Policy struct {
	YellowThreshold       int
	MaxRedsPerMatch       int
	ClubBoardRedThreshold int
}

func (p Policy) normalised() Policy {
	if p.YellowThreshold < 2 {
		p.YellowThreshold = 3
	}
	if p.MaxRedsPerMatch < 1 {
		p.MaxRedsPerMatch = 1
	}
	if p.ClubBoardRedThreshold < 1 {
		p.ClubBoardRedThreshold = 3
	}
	return p
}

type CardRequest struct {
	// Kind is yellow, direct_red, suspended_red, or activate_suspended_red.
	Kind        string
	Rescindable bool
}

type LedgerState struct {
	YellowBalance int
	TeamRedCount  int
	ClubRedCount  int
	MatchRedCount int
}

type Calculation struct {
	EffectType            string
	Status                string
	PointsDeduction       int
	YellowBalanceAfter    int
	TeamRedCountAfter     int
	ClubRedCountAfter     int
	ConsumedYellowCount   int
	CreateBoardReviewTask bool
	Suppressed            bool
	Explanation           string
}

// Calculate is the only card-colour/totting decision point. It is deliberately
// pure so scheduled, bulk, manual, import-preview, and correction paths use the
// same behaviour and can persist the explanation alongside the decision.
func Calculate(policy Policy, state LedgerState, request CardRequest) (Calculation, error) {
	policy = policy.normalised()
	result := Calculation{
		YellowBalanceAfter: state.YellowBalance,
		TeamRedCountAfter:  state.TeamRedCount,
		ClubRedCountAfter:  state.ClubRedCount,
		Status:             "active",
	}

	makeRed := func(effect string) Calculation {
		if state.MatchRedCount >= policy.MaxRedsPerMatch {
			result.Suppressed = true
			result.EffectType = "no_action"
			result.Status = "cancelled"
			result.Explanation = "The per-match red-card maximum has already been reached."
			return result
		}
		result.EffectType = effect
		result.TeamRedCountAfter++
		result.ClubRedCountAfter++
		result.PointsDeduction = result.TeamRedCountAfter
		result.CreateBoardReviewTask = state.ClubRedCount < policy.ClubBoardRedThreshold && result.ClubRedCountAfter >= policy.ClubBoardRedThreshold
		return result
	}

	switch request.Kind {
	case "yellow":
		if request.Rescindable {
			result.EffectType = "yellow_card"
			result.Status = "suspended"
			result.Explanation = fmt.Sprintf("Rescindable yellow recorded; it does not enter the %d-card balance unless the remedy condition is missed.", policy.YellowThreshold)
			return result, nil
		}
		if state.MatchRedCount >= policy.MaxRedsPerMatch && !request.Rescindable {
			result.Suppressed = true
			result.EffectType = "no_action"
			result.Status = "cancelled"
			result.Explanation = "A non-rescindable yellow is absorbed by the match red-card maximum."
			return result, nil
		}
		result.YellowBalanceAfter++
		if result.YellowBalanceAfter >= policy.YellowThreshold {
			result = makeRed("red_card")
			if !result.Suppressed {
				result.ConsumedYellowCount = policy.YellowThreshold
				result.YellowBalanceAfter = state.YellowBalance + 1 - policy.YellowThreshold
				result.Explanation = fmt.Sprintf("Yellow card %d converts to red card %d with a %d-point deduction.", policy.YellowThreshold, result.TeamRedCountAfter, result.PointsDeduction)
			}
			return result, nil
		}
		result.EffectType = "yellow_card"
		result.Explanation = fmt.Sprintf("Yellow card recorded; the team balance is now %d of %d.", result.YellowBalanceAfter, policy.YellowThreshold)
		return result, nil
	case "direct_red":
		result = makeRed("red_card")
		if !result.Suppressed {
			result.Explanation = fmt.Sprintf("Direct red card %d carries a %d-point card-system deduction.", result.TeamRedCountAfter, result.PointsDeduction)
		}
		return result, nil
	case "suspended_red":
		result.EffectType = "suspended_red"
		result.Status = "suspended"
		result.PointsDeduction = 0
		result.Explanation = "Suspended red recorded; it does not count until activated."
		return result, nil
	case "activate_suspended_red":
		result = makeRed("red_card")
		if !result.Suppressed {
			result.Explanation = fmt.Sprintf("Suspended red activated as red card %d with a %d-point deduction.", result.TeamRedCountAfter, result.PointsDeduction)
		}
		return result, nil
	default:
		return Calculation{}, fmt.Errorf("unsupported card request %q", request.Kind)
	}
}
