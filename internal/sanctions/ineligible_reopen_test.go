package sanctions

import (
	"errors"
	"testing"
	"time"
)

func TestValidateIneligibleReopenState(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name                             string
		sourceType, status, publicStatus string
		publishedAt                      *time.Time
		stale                            bool
		want                             error
	}{
		{"eligible", "ineligible_player", "approved", "unpublished", nil, true, nil},
		{"other source", "discipline", "approved", "unpublished", nil, true, ErrIneligibleReopenNotAllowed},
		{"not approved", "ineligible_player", "investigating", "unpublished", nil, true, ErrIneligibleReopenNotAllowed},
		{"public", "ineligible_player", "approved", "active", nil, true, ErrIneligibleReopenNotAllowed},
		{"published timestamp", "ineligible_player", "approved", "unpublished", &now, true, ErrIneligibleReopenNotAllowed},
		{"source current", "ineligible_player", "approved", "unpublished", nil, false, ErrIneligibleReopenNoSourceChange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateIneligibleReopenState(test.sourceType, test.status, test.publicStatus, test.publishedAt, test.stale)
			if !errors.Is(err, test.want) {
				t.Fatalf("validateIneligibleReopenState() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestIneligibleReopenRecognisesOnlyOutcomeMessageKinds(t *testing.T) {
	for _, kind := range []string{"outcome_offending_club", "outcome_reporting_club", "outcome_official", "no_action_outcome"} {
		if !isOutcomeMessageKind(kind) {
			t.Errorf("%q should be an outcome message kind", kind)
		}
	}
	for _, kind := range []string{"", "response_request", "response_reminder", "decision_published"} {
		if isOutcomeMessageKind(kind) {
			t.Errorf("%q must not be treated as an outcome message kind", kind)
		}
	}
}

func TestOutcomeDeliveryPreventsIneligibleReopen(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name                   string
		processedAt, revokedAt *time.Time
		uncertainOrSent        bool
		want                   bool
	}{
		{"never attempted", nil, nil, false, false},
		{"failed only", nil, nil, false, false},
		{"sending or sent attempt", nil, nil, true, true},
		{"processed", &now, nil, false, true},
		{"already revoked", &now, &now, false, false},
		{"revoked but later delivery evidence exists", &now, &now, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outcomeDeliveryPreventsReopen(test.processedAt, test.revokedAt, test.uncertainOrSent); got != test.want {
				t.Fatalf("outcomeDeliveryPreventsReopen() = %v, want %v", got, test.want)
			}
		})
	}
}
