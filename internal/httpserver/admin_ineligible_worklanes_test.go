package httpserver

import (
	"os"
	"strings"
	"testing"
)

func TestIneligibleQueueStatusCardsKeepTheOriginalGrid(t *testing.T) {
	counts := ineligibleDashboardCounts{
		NewIntakes: 2, AwaitingSelection: 3, HiddenReports: 198, ActiveCases: 4,
		ResponsesDue: 3, ResponsesOverdue: 5, RecentReplies: 1, AwaitingDecision: 0,
		AwaitingDenverSignoff: 7, PlayCricketPointsTasks: 2, DeliveryExceptions: 0, ClosedCases: 8,
	}
	cards := ineligibleQueueStatusCards(counts)
	// Twelve figures, eleven cards: Denver's two are one.
	want := []string{
		"Visible queue", "Not yet selected", "Hidden reports", "Under investigation",
		"Responses due", "Responses overdue", "New replies", "Awaiting decision",
		"Awaiting Denver final sign-off", "Delivery exceptions", "Closed cases",
	}
	if len(cards) != len(want) {
		t.Fatalf("queue status shows %d cards, want %d", len(cards), len(want))
	}
	for i, label := range want {
		if cards[i].Label != label {
			t.Fatalf("card %d is %q, want %q - the grid order changed", i, cards[i].Label, label)
		}
	}
	if cards[5].Href != "/admin/cases?group=responses_overdue#cases" {
		t.Fatalf("responses overdue links to %q, want the matching overdue case group", cards[5].Href)
	}
}

func TestIneligibleQueueStatusShowsDenverAsOneCard(t *testing.T) {
	counts := ineligibleDashboardCounts{AwaitingDenverSignoff: 7, PlayCricketPointsTasks: 2}
	denverCards := 0
	for _, card := range ineligibleQueueStatusCards(counts) {
		if !strings.Contains(card.Label, "Denver") {
			continue
		}
		denverCards++
		if card.Count != 7 {
			t.Fatalf("Denver card counts %d cases, want 7", card.Count)
		}
		if !strings.Contains(card.Note, "League points awaiting Denver") || !strings.Contains(card.Note, "2") {
			t.Fatalf("Denver card note %q does not carry the open league-points work", card.Note)
		}
		if card.NoteHref != "/admin/cases/tasks" {
			t.Fatalf("Denver league-points note links to %q, want the task list", card.NoteHref)
		}
	}
	if denverCards != 1 {
		t.Fatalf("queue status renders %d Denver cards, want exactly one", denverCards)
	}
}

func TestIneligibleDenverCardHidesEmptyPointsNote(t *testing.T) {
	for _, card := range ineligibleQueueStatusCards(ineligibleDashboardCounts{AwaitingDenverSignoff: 1}) {
		if strings.Contains(card.Label, "Denver") && card.Note != "" {
			t.Fatalf("Denver card still shows a league-points note (%q) when none is open", card.Note)
		}
	}
}

func TestIneligibleDashboardKeepsItsOriginalLayout(t *testing.T) {
	source := ineligibleDashboardSource(t)
	// The page was restored after the reworked layout was rolled back. These
	// are the pieces that rework had moved or replaced.
	for _, want := range []string{
		`<summary class="card-header fw-semibold">More filters and queue status</summary>`,
		`Import, choose the reports to progress, then work from that selected list.`,
		`<a class="btn btn-warning" href="/admin/ineligible/training/new">Create training report</a>`,
		`<a class="btn btn-outline-secondary" href="/admin/ineligible">Refresh</a>`,
		`aria-label="Choose work queue"`,
		`row-cols-2 row-cols-md-3 row-cols-xl-5`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("the original ineligible dashboard layout is missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"s.writeAdminPersonalWork(w, r)",
		"writeIneligibleTodayLane",
		"writeIneligibleTotalsLane",
		"Team queue",
		"Queue totals, filters and import routes",
	} {
		if strings.Contains(source, unwanted) {
			t.Fatalf("the rolled-back layout is still present: %q", unwanted)
		}
	}
	// The queue tabs sit inside the collapsible section again, above the grid.
	tabs := strings.Index(source, `aria-label="Choose work queue"`)
	grid := strings.Index(source, `row-cols-2 row-cols-md-3 row-cols-xl-5`)
	details := strings.Index(source, `More filters and queue status`)
	if details < 0 || tabs < details || grid < tabs {
		t.Fatalf("tabs and grid must sit inside the collapsible section: details=%d tabs=%d grid=%d", details, tabs, grid)
	}
}

func TestIneligibleDashboardStillReachesBulkClose(t *testing.T) {
	// Bulk close is a separate feature, not part of the layout that was rolled
	// back, so the page must still offer a way in.
	if !strings.Contains(ineligibleDashboardSource(t), `href="/admin/cases/close-batch">Close historic cases`) {
		t.Fatal("the dashboard no longer links to the bulk close page")
	}
}

func TestEveryAdminSeesTheSameIneligibleDashboard(t *testing.T) {
	// The focused sign-off page was removed: no administrator gets a different
	// version of this route any more.
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
