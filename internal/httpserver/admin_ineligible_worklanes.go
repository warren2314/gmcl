package httpserver

// ineligibleQueueStatusCard is one clickable number in the queue-status grid.
// Note and NoteHref are optional renderer fields; this grid does not display
// a separate league-points note beneath the Denver sign-off card.
type ineligibleQueueStatusCard struct {
	Label    string
	Count    int64
	Href     string
	Accent   string
	Note     string
	NoteHref string
}

// ineligibleQueueStatusCards preserves the twelve-card queue-status grid.
// Denver's card counts cases awaiting final sign-off only. League-points
// follow-up tasks remain available on their task page, not as a loose link
// beneath this card.
func ineligibleQueueStatusCards(counts ineligibleDashboardCounts) []ineligibleQueueStatusCard {
	denver := ineligibleQueueStatusCard{
		Label:  "Awaiting Denver final sign-off",
		Count:  counts.AwaitingDenverSignoff,
		Accent: "border-danger",
		Href:   "/admin/cases?group=awaiting_denver#cases",
	}
	return []ineligibleQueueStatusCard{
		{Label: "Visible queue", Count: counts.NewIntakes, Accent: "border-primary", Href: "/admin/ineligible?live=1&scope=all&state=open&worklist=visible"},
		{Label: "Not yet selected", Count: counts.AwaitingSelection, Accent: "border-warning", Href: "/admin/ineligible?live=1&origin=google_form&pending_selection=1&scope=all&state=open&worklist=all"},
		{Label: "Hidden reports", Count: counts.HiddenReports, Accent: "border-secondary", Href: "/admin/ineligible?live=1&scope=all&state=open&worklist=deferred"},
		{Label: "Live cases", Count: counts.LiveCases, Accent: "border-success", Href: "/admin/cases?group=live#cases"},
		{Label: "Under investigation", Count: counts.ActiveCases, Accent: "border-primary", Href: "/admin/cases?group=investigating#cases"},
		{Label: "Responses due", Count: counts.ResponsesDue, Accent: "border-warning", Href: "/admin/cases?group=responses_due#cases"},
		{Label: "Responses overdue", Count: counts.ResponsesOverdue, Accent: "border-danger", Href: "/admin/cases?group=responses_overdue#cases"},
		{Label: "New replies", Count: counts.RecentReplies, Accent: "border-info", Href: ineligibleNewRepliesHref(counts)},
		{Label: "Awaiting decision", Count: counts.AwaitingDecision, Accent: "border-primary", Href: "/admin/cases?group=awaiting_decision#cases"},
		denver,
		{Label: "Delivery exceptions", Count: counts.DeliveryExceptions, Accent: "border-danger", Href: "/admin/cases?group=delivery_exceptions#cases"},
		{Label: "Closed cases", Count: counts.ClosedCases, Accent: "border-success", Href: "/admin/cases?group=closed#cases"},
	}
}
