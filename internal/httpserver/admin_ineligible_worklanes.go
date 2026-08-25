package httpserver

import "fmt"

// ineligibleQueueStatusCard is one clickable number in the queue-status grid.
// Note carries a secondary figure that would otherwise need a tile of its own,
// so a single subject - Denver's sign-off work - is one card rather than two
// competing for attention.
type ineligibleQueueStatusCard struct {
	Label    string
	Count    int64
	Href     string
	Accent   string
	Note     string
	NoteHref string
}

// ineligibleQueueStatusCards is the queue-status grid, in its original order.
// The only departure is Denver: the cases awaiting his final sign-off and the
// open Play-Cricket league-points tasks are one card, because they are two
// different things (cases versus follow-up tasks, over different sources) and
// side by side they read as the same number twice.
func ineligibleQueueStatusCards(counts ineligibleDashboardCounts) []ineligibleQueueStatusCard {
	denver := ineligibleQueueStatusCard{
		Label:  "Awaiting Denver final sign-off",
		Count:  counts.AwaitingDenverSignoff,
		Accent: "border-danger",
		Href:   "/admin/cases?group=awaiting_denver#cases",
	}
	if counts.PlayCricketPointsTasks > 0 {
		denver.Note = fmt.Sprintf("League points awaiting Denver: %d", counts.PlayCricketPointsTasks)
		denver.NoteHref = "/admin/cases/tasks"
	}
	return []ineligibleQueueStatusCard{
		{Label: "Visible queue", Count: counts.NewIntakes, Accent: "border-primary", Href: "/admin/ineligible?scope=all&state=open&worklist=visible"},
		{Label: "Not yet selected", Count: counts.AwaitingSelection, Accent: "border-warning", Href: "/admin/ineligible/selection"},
		{Label: "Hidden reports", Count: counts.HiddenReports, Accent: "border-secondary", Href: "/admin/ineligible?scope=all&state=open&worklist=deferred"},
		{Label: "Under investigation", Count: counts.ActiveCases, Accent: "border-primary", Href: "/admin/cases?group=investigating#cases"},
		{Label: "Responses due", Count: counts.ResponsesDue, Accent: "border-warning", Href: "/admin/ineligible?scope=all&state=all&case_status=response_pending"},
		{Label: "Responses overdue", Count: counts.ResponsesOverdue, Accent: "border-danger", Href: "/admin/ineligible?scope=all&state=all&case_status=investigating"},
		{Label: "New replies", Count: counts.RecentReplies, Accent: "border-info", Href: ineligibleNewRepliesHref(counts)},
		{Label: "Awaiting decision", Count: counts.AwaitingDecision, Accent: "border-primary", Href: "/admin/cases?group=awaiting_decision#cases"},
		denver,
		{Label: "Delivery exceptions", Count: counts.DeliveryExceptions, Accent: "border-danger", Href: "/admin/cases"},
		{Label: "Closed cases", Count: counts.ClosedCases, Accent: "border-success", Href: "/admin/cases?group=closed#cases"},
	}
}
