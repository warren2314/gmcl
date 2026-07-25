package httpserver

import "testing"

func TestReportProgressCompletionRateUsesSatisfiedRequirements(t *testing.T) {
	progress := reportProgress{
		Expected:  20,
		Submitted: 16,
		Exempt:    2,
		Satisfied: 18,
		Missing:   2,
	}
	if got := progress.completionRate(); got != 90 {
		t.Fatalf("completion rate: got %.1f, want 90.0", got)
	}
}

func TestReportProgressCompletionRateHandlesNoFixtures(t *testing.T) {
	if got := (reportProgress{}).completionRate(); got != 0 {
		t.Fatalf("completion rate: got %.1f, want 0", got)
	}
}

func TestClubReportProgressCompletionRateIncludesExemptions(t *testing.T) {
	progress := clubReportProgress{
		Expected:  12,
		Submitted: 8,
		Exempt:    1,
		Satisfied: 9,
		Missing:   3,
	}
	if got := progress.completionRate(); got != 75 {
		t.Fatalf("completion rate: got %.1f, want 75.0", got)
	}
}

func TestClubReportProgressCompletionRateHandlesNoFixtures(t *testing.T) {
	if got := (clubReportProgress{}).completionRate(); got != 0 {
		t.Fatalf("completion rate: got %.1f, want 0", got)
	}
}
