package sanctions

import (
	"strings"
	"testing"
)

func TestValidateDecisionEffectFieldsKeepsSanctionsDistinct(t *testing.T) {
	fine := int64(2500)
	points := -5
	tests := []struct {
		name   string
		effect DecisionEffectRequest
		ok     bool
	}{
		{"fine", DecisionEffectRequest{EffectType: "fine", AmountPence: &fine}, true},
		{"league points", DecisionEffectRequest{EffectType: "points_adjustment", Points: &points}, true},
		{"warning", DecisionEffectRequest{EffectType: "warning"}, true},
		{"fine plus points", DecisionEffectRequest{EffectType: "fine", AmountPence: &fine, Points: &points}, false},
		{"warning plus fine", DecisionEffectRequest{EffectType: "warning", AmountPence: &fine}, false},
		{"warning plus points", DecisionEffectRequest{EffectType: "warning", Points: &points}, false},
		{"points plus fine", DecisionEffectRequest{EffectType: "points_adjustment", Points: &points, AmountPence: &fine}, false},
		{"zero fine", DecisionEffectRequest{EffectType: "fine", AmountPence: new(int64)}, false},
		{"zero points", DecisionEffectRequest{EffectType: "points_adjustment", Points: new(int)}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDecisionEffectFields(test.effect)
			if (err == nil) != test.ok {
				t.Fatalf("error=%v, want ok=%v", err, test.ok)
			}
			if err != nil && strings.TrimSpace(err.Error()) == "" {
				t.Fatal("validation returned an empty error")
			}
		})
	}
}
