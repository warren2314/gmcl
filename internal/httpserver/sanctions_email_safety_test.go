package httpserver

import "testing"

func TestSanctionsEmailDisabled(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"false", false},
		{"0", false},
		{"true", true},
		{" YES ", true},
		{"On", true},
		{"1", true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("SANCTIONS_EMAIL_DISABLED", tt.value)
			if got := sanctionsEmailDisabled(); got != tt.want {
				t.Fatalf("sanctionsEmailDisabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
