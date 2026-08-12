package httpserver

import "testing"

func TestSanctionOutboxSchedulerEnabled(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", false}, {"false", false}, {"0", false}, {"true", true}, {" YES ", true}, {"1", true}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("SANCTION_OUTBOX_SCHEDULER_ENABLED", tc.value)
			if got := sanctionOutboxSchedulerEnabled(); got != tc.want {
				t.Fatalf("sanctionOutboxSchedulerEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
