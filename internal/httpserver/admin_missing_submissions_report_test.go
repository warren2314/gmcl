package httpserver

import "testing"

func TestMissingSubmissionCardLabel(t *testing.T) {
	tests := map[string]string{
		"yellow_card":   "Yellow card",
		"red_card":      "Red card",
		"suspended_red": "Suspended red card",
		"":              "Not staged",
	}
	for effect, want := range tests {
		if got := missingSubmissionCardLabel(effect); got != want {
			t.Fatalf("missingSubmissionCardLabel(%q)=%q want %q", effect, got, want)
		}
	}
}
