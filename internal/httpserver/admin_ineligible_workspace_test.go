package httpserver

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func workspaceLinks(markup string) []*url.URL {
	var result []*url.URL
	for _, match := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(markup, -1) {
		if link, err := url.Parse(html.UnescapeString(match[1])); err == nil {
			result = append(result, link)
		}
	}
	return result
}

func TestIneligibleWorkspaceBookmarksKeepTheirRecordType(t *testing.T) {
	for _, test := range []struct{ query, want string }{
		{"", "cases"}, {"scope=mine", "cases"}, {"group=new_replies", "cases"},
		{"state=open&scope=all", "reports"}, {"worklist=deferred", "reports"},
		{"origin=google_form&pending_selection=1", "reports"},
		{"view=cases&state=open", "cases"}, {"view=history&state=all", "history"},
		{"view=reports&q=Example", "reports"},
	} {
		values, _ := url.ParseQuery(test.query)
		if got := ineligibleWorkspaceView(values); got != test.want {
			t.Errorf("%q opens %q, want %q", test.query, got, test.want)
		}
	}
}

func TestIneligibleWorkspaceFiltersValidateAndRoundTrip(t *testing.T) {
	values, _ := url.ParseQuery("view=history&scope=mine&reply_status=unreviewed&q=+Example+%26+Club+&training=1&fixture_from=2026-08-01&fixture_to=2026-07-01&page=2")
	filter := parseIneligibleCaseFilter(values)
	if filter.Query != "Example & Club" || filter.Group != "new_replies" || filter.FixtureFrom != "2026-07-01" || filter.FixtureTo != "2026-08-01" || filter.Page != 2 {
		t.Fatalf("valid filters lost context: %+v", filter)
	}
	link, err := url.Parse(ineligibleCaseFilterURL(filter, 3))
	if err != nil {
		t.Fatal(err)
	}
	got := parseIneligibleCaseFilter(link.Query())
	filter.Page = 3
	if got != filter {
		t.Fatalf("pagination changed population: got %+v want %+v", got, filter)
	}
	invalid, _ := url.ParseQuery("view=unknown&scope=unknown&group=unknown&case_status=unknown&fixture_from=bad&page=1000001")
	got = parseIneligibleCaseFilter(invalid)
	if got.View != "cases" || got.Scope != "all" || got.Group != "" || got.CaseStatus != "" || got.FixtureFrom != "" || got.Page != 1 {
		t.Fatalf("invalid filters not normalised: %+v", got)
	}
}

func TestIneligibleWorkspaceTabsPreserveSharedFiltersWithoutHidingNewReports(t *testing.T) {
	current := url.Values{
		"q": {"Example & Club"}, "fixture_from": {"2026-07-01"}, "fixture_to": {"2026-08-01"},
		"training": {"1"}, "scope": {"mine"}, "page": {"9"}, "group": {"new_replies"},
	}
	var out bytes.Buffer
	writeIneligibleWorkspaceTabs(&out, "cases", true, current)
	links := workspaceLinks(out.String())
	if len(links) != 3 {
		t.Fatalf("got %d workspace views, want 3", len(links))
	}
	for _, link := range links {
		q := link.Query()
		if q.Get("q") != "Example & Club" || q.Get("fixture_from") != "2026-07-01" || q.Get("fixture_to") != "2026-08-01" || q.Get("training") != "1" || q.Get("page") != "" || q.Get("group") != "" {
			t.Errorf("view link loses or carries incompatible filters: %s", link)
		}
		if q.Get("view") == "reports" && q.Get("scope") != "all" {
			t.Errorf("report triage must include unassigned arrivals: %s", link)
		}
	}
	out.Reset()
	writeIneligibleWorkspaceTabs(&out, "cases", false)
	if strings.Contains(out.String(), "Reports to review") {
		t.Fatal("triage entrance exposed without triage capability")
	}
}

func TestIneligibleWorkspaceRowsKeepCaseUnitsAndReturnContext(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	due := now.Add(24 * time.Hour)
	items := []ineligibleCaseWorkRow{
		{ID: 12, Reference: "GMCL-12", Player: "<script>bad()</script>", Club: "Example & Club", Team: "First XI", Season: "2026", Owner: "Owner", Status: "response_pending", RequestStatus: "pending", DueAt: &due},
		{ID: 13, Reference: "GMCL-13", Player: "Example Player", Status: "investigating", IsOwner: true, ReplyUnreviewed: true, OpenTasks: 2},
	}
	filter := ineligibleCaseFilter{View: "cases", Scope: "mine", Query: "Example & Club", Training: true, Page: 2}
	var out bytes.Buffer
	writeIneligibleCaseResults(&out, items, 61, filter, ineligibleCaseAccess{CanInvestigate: true}, time.UTC, now)
	markup := out.String()
	for _, want := range []string{"61 matching cases", "Page 2 of 2", "Each row is one case", "training and test cases", "Recorded deadline:", "2 open follow-up task(s)", "&lt;script&gt;bad()&lt;/script&gt;", "Review the club reply"} {
		if !strings.Contains(markup, want) {
			t.Errorf("rendered results missing %q", want)
		}
	}
	if strings.Contains(markup, "<script>bad()") {
		t.Fatal("case data rendered as executable markup")
	}
	var exportFound, returnFound bool
	for _, link := range workspaceLinks(markup) {
		if link.Path == "/admin/ineligible/export.csv" {
			exportFound = link.Query().Get("scope") == "mine" && link.Query().Get("q") == filter.Query && link.Query().Get("training") == "1" && link.Query().Get("page") == ""
		}
		if link.Path == "/admin/cases/13" {
			returnFound = link.Query().Get("return_to") == ineligibleCaseFilterURL(filter, 2)
		}
	}
	if !exportFound || !returnFound {
		t.Fatalf("matching export or case return context lost: export=%v return=%v", exportFound, returnFound)
	}
}

func TestIneligibleWorkspaceUnavailableDoesNotBecomeZero(t *testing.T) {
	var out bytes.Buffer
	writeIneligibleReportLeagueSummary(&out, ineligibleDashboardCounts{}, errors.New("unavailable"))
	if !strings.Contains(out.String(), "League totals unavailable") || strings.Contains(out.String(), "0 active cases") {
		t.Fatalf("failed metric masquerades as zero: %s", out.String())
	}
}

func TestIneligibleWorkspaceTriageOnlyDoesNotOfferForbiddenCaseLinks(t *testing.T) {
	var out bytes.Buffer
	writeIneligibleCaseResults(&out, []ineligibleCaseWorkRow{{ID: 12, Reference: "GMCL-12", Status: "investigating"}}, 1,
		ineligibleCaseFilter{View: "cases", Scope: "all", Page: 1}, ineligibleCaseAccess{}, time.UTC, time.Now())
	if strings.Contains(out.String(), `href="/admin/cases/12`) || !strings.Contains(out.String(), "Case detail requires investigation or approval access") {
		t.Fatalf("triage-only workspace offers an inaccessible case: %s", out.String())
	}
}

func TestIneligibleWorkspaceReportSearchAndTrainingSurviveDateControls(t *testing.T) {
	filter := ineligibleQueueFilters{State: "open", Scope: "all", Worklist: "visible", Sort: "newest", Search: "Club & Player", TrainingOnly: true, FixtureFrom: "2026-07-01"}
	var out bytes.Buffer
	writeIneligibleFixtureDateControls(&out, filter)
	for _, want := range []string{`name="view" value="reports"`, `name="q" value="Club &amp; Player"`, `name="training" value="1"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("date controls lose %q", want)
		}
	}
	for _, raw := range []string{ineligibleClearFixtureDatesURL(filter), ineligibleQueueTabURL(filter, "all", "all", "all")} {
		link, _ := url.Parse(raw)
		if link.Query().Get("q") != filter.Search || link.Query().Get("training") != "1" || link.Query().Get("view") != "reports" {
			t.Errorf("report navigation loses search/training: %s", raw)
		}
	}
}

func TestIneligibleWorkspaceResetKeepsTrainingView(t *testing.T) {
	var out bytes.Buffer
	writeIneligibleCaseFilters(&out, ineligibleCaseFilter{View: "history", Scope: "mine", Query: "Example", Training: true})
	for _, link := range workspaceLinks(out.String()) {
		if link.Path == "/admin/ineligible" {
			if link.Query().Get("training") != "1" || link.Query().Get("view") != "history" || link.Query().Get("q") != "" || link.Query().Get("scope") != "all" {
				t.Fatalf("reset switches to live work or retains search/ownership: %s", link)
			}
			return
		}
	}
	t.Fatal("reset link missing")
}

// Optional offline rendering uses the production HTML helpers and local styles,
// with invented records only. It never starts the app or its background jobs.
func TestIneligibleWorkspaceVisualFixture(t *testing.T) {
	dir := os.Getenv("GMCL_UI_FIXTURE_DIR")
	if dir == "" {
		t.Skip("optional visual fixture output not requested")
	}
	t.Setenv("GMCL_UI_ASSET_DIR", dir)
	now := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	due := now.Add(24 * time.Hour)
	items := []ineligibleCaseWorkRow{
		{ID: 12, Reference: "GMCL-2026-012", Player: "Example Player", Club: "Example Cricket Club", Team: "First XI", Season: "2026", Owner: "Case Owner", Status: "investigating", IsOwner: true, ReplyUnreviewed: true, ReplyAt: &now, OpenTasks: 1},
		{ID: 13, Reference: "GMCL-2026-013", Player: "Another Player", Club: "Another Cricket Club", Team: "Second XI", Season: "2026", Owner: "Case Owner", Status: "response_pending", RequestStatus: "pending", DueAt: &due},
		{ID: 14, Reference: "GMCL-2026-014", Player: "Example Player", Club: "Example Cricket Club", Team: "First XI", Season: "2026", Owner: "Other Owner", Status: "decision_proposed", HasProposed: true, SentForApproval: true},
		{ID: 15, Reference: "GMCL-2026-015", Club: "Example Cricket Club", Team: "Third XI", Season: "2026", Owner: "Other Owner", Status: "approved", DeliveryException: true},
	}
	filter := ineligibleCaseFilter{View: "cases", Scope: "all", Page: 1}
	var out bytes.Buffer
	pageHead(&out, "Ineligible players")
	writeAdminNav(&out, "", "/admin/ineligible", "admin")
	fmt.Fprint(&out, `<main class="container-fluid px-3 px-lg-4 py-4 admin-workspace"><header class="workspace-page-heading"><div><h1 class="h2 mb-1">Ineligible players</h1><p class="text-muted mb-0">Review incoming reports and move each case to its next action.</p></div><a class="btn btn-outline-secondary" href="/admin/dashboard">Home and My work</a></header>`)
	writeIneligibleWorkspaceTabs(&out, "cases", true)
	writeIneligibleCaseFilters(&out, filter)
	writeIneligibleCaseResults(&out, items, 4, filter, ineligibleCaseAccess{CanInvestigate: true, CanPropose: true, CanApprove: true}, time.UTC, now)
	fmt.Fprint(&out, "</main>")
	pageFooter(&out)
	markup := offlineHomePreviewHTML(t, out.String())
	if err := os.WriteFile(filepath.Join(dir, "ineligible-cases.html"), []byte(markup), 0600); err != nil {
		t.Fatal(err)
	}
}
