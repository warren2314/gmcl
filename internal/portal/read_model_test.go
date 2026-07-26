package portal

import "testing"

func TestReportSummaryDerivedValues(t *testing.T) {
	summary := ReportSummary{
		Expected:  10,
		Submitted: 7,
		Exempt:    1,
		Due:       1,
		Missed:    1,
		Late:      2,
	}
	if summary.Satisfied() != 8 {
		t.Fatalf("satisfied = %d", summary.Satisfied())
	}
	if summary.CompletionPercent() != 80 {
		t.Fatalf("completion = %.2f", summary.CompletionPercent())
	}
}

func TestReportSummaryZeroExpectedIsNotInventedCompliance(t *testing.T) {
	summary := ReportSummary{}
	if summary.CompletionPercent() != 0 {
		t.Fatalf("zero expected completion = %.2f", summary.CompletionPercent())
	}
}
