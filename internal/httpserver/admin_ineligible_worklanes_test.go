package httpserver

import (
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestIneligibleReportSummarySeparatesCasesAndReports(t *testing.T) {
	out := httptest.NewRecorder()
	writeIneligibleReportLeagueSummary(out, ineligibleDashboardCounts{
		LiveCases: 9, NewIntakes: 2, HiddenReports: 198, AwaitingDenverSignoff: 7, PlayCricketPointsTasks: 4,
	}, nil)
	html := out.Body.String()
	for _, want := range []string{
		`href="/admin/ineligible?view=cases">9 active cases`,
		`href="/admin/ineligible?view=reports&amp;state=open&amp;worklist=visible">2 reports in the visible queue`,
		`href="/admin/ineligible?view=reports&amp;state=open&amp;worklist=deferred">198 deferred reports`,
		`These league totals are separate from the filtered report count.`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("report summary missing %q: %s", want, html)
		}
	}
	for _, unwanted := range []string{"Awaiting Denver", "League points", "209 total", "row-cols"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("report summary mixes unrelated work into report totals: %s", html)
		}
	}
}

func TestIneligibleReportSummaryShowsUnavailableCounts(t *testing.T) {
	out := httptest.NewRecorder()
	writeIneligibleReportLeagueSummary(out, ineligibleDashboardCounts{}, errors.New("query failed"))
	html := out.Body.String()
	if !strings.Contains(html, "League totals unavailable") || !strings.Contains(html, "report list below remains usable") {
		t.Fatalf("failed counts need a visible, usable fallback: %s", html)
	}
	if strings.Contains(html, "0 active cases") || strings.Contains(html, "0 reports") {
		t.Fatal("unavailable counts are represented as zero")
	}
}

func TestIneligibleWorkspaceTabsLinkDifferentRecordPopulations(t *testing.T) {
	out := httptest.NewRecorder()
	writeIneligibleWorkspaceTabs(out, "reports", true)
	html := out.Body.String()
	for _, want := range []string{
		`href="/admin/ineligible?view=reports" aria-current="page">Reports to review`,
		`href="/admin/ineligible?view=cases">Cases in progress`,
		`href="/admin/ineligible?view=history">Case history`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("workspace navigation missing %q: %s", want, html)
		}
	}
}

func TestIneligibleDashboardStillReachesBulkClose(t *testing.T) {
	if !strings.Contains(ineligibleDashboardSource(t), `href="/admin/cases/close-batch">Close historic cases`) {
		t.Fatal("the dashboard no longer links to the bulk close page")
	}
}

func TestEveryAdminSeesTheSameIneligibleDashboard(t *testing.T) {
	if _, err := os.Stat("admin_ineligible_signoff_view.go"); err == nil {
		t.Fatal("the separate sign-off view still exists")
	}
	source := ineligibleDashboardSource(t)
	for _, unwanted := range []string{"writeAdminIneligibleSignOffPage", "adminIsFinalSignOffAdmin", "ineligibleSignOffViewRequested"} {
		if strings.Contains(source, unwanted) {
			t.Fatalf("the dashboard still branches to a per-administrator view: %q", unwanted)
		}
	}
}

func TestAdminHomeStillShowsPersonalWork(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "s.writeAdminPersonalWork(w, r)") {
		t.Fatal("personal work is no longer shown on the Dashboard page")
	}
}
