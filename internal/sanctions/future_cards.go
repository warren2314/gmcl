package sanctions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func isCardEffect(kind string) bool {
	return kind == "yellow_card" || kind == "red_card" || kind == "suspended_red" || kind == "scheduled_red"
}

func effectRedCardCount(effect DecisionEffectRequest) int {
	if effect.RedCardCount == 0 {
		return 1
	}
	return effect.RedCardCount
}

func leagueEffectDate(date time.Time) string {
	if london, err := time.LoadLocation("Europe/London"); err == nil {
		date = date.In(london)
	}
	return date.Format("2 January 2006")
}

func validateFutureCardFields(effect DecisionEffectRequest) error {
	future := effect.TargetSeasonID != nil
	if effect.RedCardCount < 0 || effect.RedCardCount > 100 {
		return errors.New("red-card count must be between 1 and 100")
	}
	if effect.EffectType != "scheduled_red" && effect.EffectType != "suspended_red" {
		if future || effect.RedCardCount != 0 {
			return errors.New("target season and red-card count are only available for scheduled or suspended red cards")
		}
		return nil
	}
	if effect.EffectType == "scheduled_red" {
		if effect.Rescindable {
			return errors.New("scheduled red cards are definite awards and cannot be rescindable")
		}
		if !future || effect.StartsAt == nil || effect.RedCardCount < 1 {
			return errors.New("scheduled red cards require a target season, effective date and card count")
		}
		if strings.TrimSpace(effect.Trigger) != "" {
			return errors.New("scheduled red cards are definite awards; use suspended red cards for a conditional sanction")
		}
	}
	if future && (effect.StartsAt == nil || *effect.TargetSeasonID <= 0) {
		return errors.New("a future-season card requires a valid target season and effective date")
	}
	if effect.EffectType == "suspended_red" && future && strings.TrimSpace(effect.Trigger) == "" {
		return errors.New("future suspended red cards require an explicit activation condition")
	}
	if effect.StartsAt != nil && effect.EndsAt != nil && effect.EndsAt.Before(*effect.StartsAt) {
		return errors.New("the end date cannot precede the effective date")
	}
	return nil
}

func validateTargetCardSeason(ctx context.Context, tx pgx.Tx, effect DecisionEffectRequest, caseSeasonID *int32) error {
	if effect.TargetSeasonID == nil {
		return nil
	}
	if caseSeasonID == nil {
		return errors.New("future-season cards require the case's original season to be mapped")
	}
	var start, end, caseStart time.Time
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT target.start_date,target.end_date,original.start_date,target.is_archived
		FROM seasons target CROSS JOIN seasons original WHERE target.id=$1 AND original.id=$2`,
		*effect.TargetSeasonID, *caseSeasonID).Scan(&start, &end, &caseStart, &archived); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("the target season must be configured before a future-season card can be saved")
		}
		return err
	}
	if archived {
		return errors.New("the target season is archived; select an available future season")
	}
	return validateTargetCardDates(effect, start, end, caseStart)
}

func validateTargetCardDates(effect DecisionEffectRequest, start, end, caseStart time.Time) error {
	if !start.After(caseStart) {
		return errors.New("select a season later than the case's original season")
	}
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		return fmt.Errorf("load league timezone: %w", err)
	}
	// Season bounds are PostgreSQL DATEs; compare the submitted league civil
	// date, not its UTC instant (BST midnight is the previous UTC day).
	if effect.StartsAt == nil {
		return errors.New("the effective date must fall within the selected target season")
	}
	date := effect.StartsAt.In(london).Format("2006-01-02")
	if date < start.Format("2006-01-02") || date > end.Format("2006-01-02") {
		return errors.New("the effective date must fall within the selected target season")
	}
	return nil
}

// A definite board award may cover several fixtures. Reserve every awarded red
// in its target season, independently of the original fixture's per-match cap.
// Future ordinary red cards therefore escalate from this approved opening count.
func calculateScheduledRed(policy Policy, state LedgerState, count int) (Calculation, error) {
	if count < 1 || count > 100 {
		return Calculation{}, errors.New("red-card count must be between 1 and 100")
	}
	policy = policy.normalised()
	after := state.TeamRedCount + count
	points := count*state.TeamRedCount + count*(count+1)/2
	return Calculation{
		EffectType: "scheduled_red", Status: "active", PointsDeduction: points,
		YellowBalanceAfter: state.YellowBalance, TeamRedCountAfter: after,
		ClubRedCountAfter:     state.ClubRedCount + count,
		CreateBoardReviewTask: state.ClubRedCount < policy.ClubBoardRedThreshold && state.ClubRedCount+count >= policy.ClubBoardRedThreshold,
		Explanation:           fmt.Sprintf("Definite award of %d red cards in the target season. Approved target-season reds before this award: %d; total after: %d. Deduct %d card-system points once on the effective date; do not add a second league-table adjustment for these cards.", count, state.TeamRedCount, after, points),
	}, nil
}
