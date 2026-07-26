package portal

import (
	"testing"
	"time"
)

func TestReportObligationStatus(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	beforeDeadline := now.Add(time.Hour)
	onTime := now.Add(-2 * time.Hour)
	late := now.Add(2 * time.Hour)

	tests := []struct {
		name       string
		obligation ReportObligation
		want       string
	}{
		{
			name: "submitted",
			obligation: ReportObligation{
				DeadlineAt:  beforeDeadline,
				SubmittedAt: &onTime,
			},
			want: "submitted",
		},
		{
			name: "late",
			obligation: ReportObligation{
				DeadlineAt:  beforeDeadline,
				SubmittedAt: &late,
			},
			want: "late",
		},
		{
			name: "exempt",
			obligation: ReportObligation{
				DeadlineAt: now.Add(-time.Hour),
				Exempt:     true,
			},
			want: "exempt",
		},
		{
			name: "missed",
			obligation: ReportObligation{
				DeadlineAt: now.Add(-time.Hour),
			},
			want: "missed",
		},
		{
			name: "due",
			obligation: ReportObligation{
				DeadlineAt: beforeDeadline,
			},
			want: "due",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := reportObligationStatus(test.obligation, now); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}
