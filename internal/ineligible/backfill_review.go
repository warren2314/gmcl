package ineligible

import (
	"fmt"
	"strings"
)

// BackfillReviewInput is a human interpretation only. Persisting it never
// changes an intake, case, effect, ledger or notification record.
type BackfillReviewInput struct {
	Disposition          string
	ReviewedIntakeID     int64
	ReviewedCaseState    string
	EffectsReviewStatus  string
	EffectInterpretation string
	ReviewReason         string
	ReviewerName         string
}

// ValidateBackfillReview enforces explicit state review and prevents tracker
// points/cards prose from being treated as a structured sanction.
func ValidateBackfillReview(requiresEffectReview bool, input BackfillReviewInput) error {
	input.Disposition = strings.TrimSpace(input.Disposition)
	input.ReviewedCaseState = strings.TrimSpace(input.ReviewedCaseState)
	input.EffectsReviewStatus = strings.TrimSpace(input.EffectsReviewStatus)
	input.EffectInterpretation = strings.TrimSpace(input.EffectInterpretation)
	input.ReviewReason = strings.TrimSpace(input.ReviewReason)
	input.ReviewerName = strings.TrimSpace(input.ReviewerName)

	switch input.Disposition {
	case "accept_match":
		if input.ReviewedIntakeID <= 0 {
			return fmt.Errorf("a staged Google-form intake is required for an accepted match")
		}
	case "leave_unmatched", "exclude_tracker_row":
		if input.ReviewedIntakeID != 0 {
			return fmt.Errorf("an unmatched or excluded row cannot select an intake")
		}
	default:
		return fmt.Errorf("choose a valid reconciliation disposition")
	}

	switch input.ReviewedCaseState {
	case "open", "closed", "needs_interpretation":
	default:
		return fmt.Errorf("choose the tracker row's reviewed open or closed state")
	}

	if requiresEffectReview {
		switch input.EffectsReviewStatus {
		case "pending_manual_interpretation":
		case "manually_interpreted", "confirmed_no_effect":
			if input.EffectInterpretation == "" {
				return fmt.Errorf("record the manual points/cards interpretation")
			}
		default:
			return fmt.Errorf("points/cards text requires an explicit manual-review status")
		}
	} else if input.EffectsReviewStatus != "not_applicable" {
		return fmt.Errorf("effect review must be not applicable when the tracker has no points/cards text")
	}

	if input.ReviewReason == "" {
		return fmt.Errorf("a reconciliation reason is required")
	}
	if len(input.ReviewReason) > 5000 {
		return fmt.Errorf("the reconciliation reason exceeds 5,000 characters")
	}
	if input.ReviewerName == "" {
		return fmt.Errorf("the reviewer name is required")
	}
	if len(input.ReviewerName) > 200 {
		return fmt.Errorf("the reviewer name exceeds 200 characters")
	}
	if len(input.EffectInterpretation) > 5000 {
		return fmt.Errorf("the manual effect interpretation exceeds 5,000 characters")
	}
	return nil
}

// BackfillSignoffReadiness is calculated from the latest immutable review for
// every staged row. Excluded rows still need a review but do not block on a
// case-state or effect interpretation.
type BackfillSignoffReadiness struct {
	RowsTotal               int `json:"rows_total"`
	RowsReviewed            int `json:"rows_reviewed"`
	RowsExcluded            int `json:"rows_excluded"`
	RowsNeedingStateReview  int `json:"rows_needing_state_review"`
	RowsNeedingEffectReview int `json:"rows_needing_effect_review"`
}

func (r BackfillSignoffReadiness) Validate() error {
	if r.RowsTotal <= 0 {
		return fmt.Errorf("the reconciliation run has no staged rows")
	}
	if r.RowsReviewed != r.RowsTotal {
		return fmt.Errorf("every staged row must have an explicit review before sign-off")
	}
	if r.RowsNeedingStateReview > 0 {
		return fmt.Errorf("%d non-excluded row(s) still need an explicit open/closed state", r.RowsNeedingStateReview)
	}
	if r.RowsNeedingEffectReview > 0 {
		return fmt.Errorf("%d non-excluded row(s) still need manual points/cards interpretation", r.RowsNeedingEffectReview)
	}
	return nil
}
