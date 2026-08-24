package httpserver

import (
	"fmt"
	"io"
)

// ineligibleWorkTile is one clickable number on the ineligible-player
// dashboard. Note carries the secondary figure that used to need a whole tile
// of its own, so a single subject (for example Denver's sign-off work) is one
// tile rather than several competing for attention.
type ineligibleWorkTile struct {
	Label    string
	Count    int64
	Href     string
	Accent   string
	Note     string
	NoteHref string
}

// ineligibleTodayTiles are the only counts shown above the fold: the work the
// team can pick up right now. Everything else is a total, not a task, and
// lives under "Queue totals and history".
func ineligibleTodayTiles(counts ineligibleDashboardCounts) []ineligibleWorkTile {
	return []ineligibleWorkTile{
		{Label: "Reports to review", Count: counts.NewIntakes, Accent: "border-primary", Href: "/admin/ineligible?scope=all&state=open&worklist=visible#reports"},
		{Label: "Club replies to read", Count: counts.RecentReplies, Accent: "border-info", Href: ineligibleNewRepliesHref(counts)},
		{Label: "Responses overdue", Count: counts.ResponsesOverdue, Accent: "border-danger", Href: "/admin/ineligible?scope=all&state=all&case_status=investigating"},
		{Label: "Cases under investigation", Count: counts.ActiveCases, Accent: "border-secondary", Href: "/admin/cases?group=investigating#cases"},
	}
}

// ineligibleTotalsTiles are reference counts. Denver's two figures are one
// tile: the cases waiting for final sign-off, with the open Play-Cricket
// league-points tasks as its note.
func ineligibleTotalsTiles(counts ineligibleDashboardCounts) []ineligibleWorkTile {
	denver := ineligibleWorkTile{Label: "With Denver for final sign-off", Count: counts.AwaitingDenverSignoff, Accent: "border-warning", Href: "/admin/cases?group=awaiting_denver#cases"}
	if counts.PlayCricketPointsTasks > 0 {
		denver.Note = fmt.Sprintf("League points awaiting Denver: %d", counts.PlayCricketPointsTasks)
		denver.NoteHref = "/admin/cases/tasks"
	}
	return []ineligibleWorkTile{
		{Label: "Not yet selected", Count: counts.AwaitingSelection, Accent: "border-warning", Href: "/admin/ineligible/selection"},
		{Label: "Hidden reports", Count: counts.HiddenReports, Accent: "border-secondary", Href: "/admin/ineligible?scope=all&state=open&worklist=deferred"},
		{Label: "Responses still in date", Count: counts.ResponsesDue, Accent: "border-secondary", Href: "/admin/ineligible?scope=all&state=all&case_status=response_pending"},
		{Label: "Awaiting decision approval", Count: counts.AwaitingDecision, Accent: "border-secondary", Href: "/admin/cases?group=awaiting_decision#cases"},
		denver,
		{Label: "Delivery exceptions", Count: counts.DeliveryExceptions, Accent: "border-danger", Href: "/admin/cases"},
		{Label: "Closed cases", Count: counts.ClosedCases, Accent: "border-success", Href: "/admin/cases?group=closed#cases"},
	}
}

func writeIneligibleTiles(w io.Writer, tiles []ineligibleWorkTile) {
	fmt.Fprint(w, `<div class="row row-cols-2 row-cols-md-4 g-2 mb-3">`)
	for _, tile := range tiles {
		fmt.Fprintf(w, `<div class="col"><a class="border rounded d-block %s text-decoration-none text-body p-3" href="%s"><div class="h3 mb-0">%d</div><div class="small text-muted">%s</div></a>`, tile.Accent, escapeHTML(tile.Href), tile.Count, escapeHTML(tile.Label))
		switch {
		case tile.Note != "" && tile.NoteHref != "":
			fmt.Fprintf(w, `<div class="small mt-1"><a href="%s">%s</a></div>`, escapeHTML(tile.NoteHref), escapeHTML(tile.Note))
		case tile.Note != "":
			fmt.Fprintf(w, `<div class="small text-muted mt-1">%s</div>`, escapeHTML(tile.Note))
		}
		fmt.Fprint(w, `</div>`)
	}
	fmt.Fprint(w, `</div>`)
}

// writeIneligibleTodayLane is the band directly under each investigator's own
// work: the shared queue, in the order it is normally worked.
func writeIneligibleTodayLane(w io.Writer, counts ineligibleDashboardCounts) {
	fmt.Fprint(w, `<section class="card shadow-sm mb-4" aria-labelledby="ineligible-team-heading"><div class="card-header"><strong id="ineligible-team-heading">Team queue</strong><div class="small text-muted">Unassigned work for the whole panel. Your own cases are listed above.</div></div><div class="card-body pb-2">`)
	writeIneligibleTiles(w, ineligibleTodayTiles(counts))
	fmt.Fprint(w, `</div></section>`)
}

func writeIneligibleTotalsLane(w io.Writer, counts ineligibleDashboardCounts) {
	fmt.Fprint(w, `<h3 class="h6 text-muted text-uppercase">Queue totals and history</h3><p class="small text-muted">These are running totals, not a to-do list. Denver's sign-off and league-points work is one entry.</p>`)
	writeIneligibleTiles(w, ineligibleTotalsTiles(counts))
}

// writeIneligibleQueueTabs sits directly above the report table it filters, so
// the choice of queue and the list it produces are read together.
func writeIneligibleQueueTabs(w io.Writer, filter ineligibleQueueFilters) {
	mineClass, selectedClass, importedClass := "btn-outline-primary", "btn-outline-primary", "btn-outline-primary"
	if filter.Scope == "mine" {
		mineClass = "btn-primary"
	} else if filter.Worklist == "all" {
		importedClass = "btn-primary"
	} else if filter.Worklist == "visible" {
		selectedClass = "btn-primary"
	}
	fmt.Fprintf(w, `<nav class="btn-group mb-3" aria-label="Choose work queue"><a class="btn %s" href="%s">My assigned cases</a><a class="btn %s" href="%s">Selected reports</a><a class="btn %s" href="%s">Report history</a></nav>`,
		mineClass, escapeHTML(ineligibleQueueTabURL(filter, "mine", "all", "visible")),
		selectedClass, escapeHTML(ineligibleQueueTabURL(filter, "all", "open", "visible")),
		importedClass, escapeHTML(ineligibleQueueTabURL(filter, "all", "all", "all")))
}

// ineligibleNextReportAction names the single most useful starting point: the
// next selected report, or the queue itself when nothing is waiting.
func ineligibleNextReportAction(nextReportID int64) (string, string) {
	if nextReportID > 0 {
		return fmt.Sprintf("/admin/ineligible/%d", nextReportID), "Open next selected report"
	}
	return "/admin/ineligible?scope=all&state=open&worklist=visible#reports", "View reports"
}
