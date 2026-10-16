package httpserver

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func ineligibleWorkspaceView(values url.Values) string {
	switch values.Get("view") {
	case "reports", "cases", "history":
		return values.Get("view")
	}
	// Retain existing report/bookmark URLs, while the clean workspace URL opens
	// casework. Ownership alone must never force a case list through intake links.
	for _, key := range []string{"state", "origin", "worklist", "pending_selection", "reporting_club", "offending_club", "player", "assignee", "age", "sort"} {
		if values.Get(key) != "" {
			return "reports"
		}
	}
	return "cases"
}

func (s *Server) handleAdminIneligibleDashboard() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ineligibleWorkspaceView(r.URL.Query()) == "reports" {
			s.requireAdminPermission("sanctions_triage")(s.handleAdminIneligibleReports()).ServeHTTP(w, r)
			return
		}
		s.handleAdminIneligibleCaseWorkspace().ServeHTTP(w, r)
	}
}

func (s *Server) handleAdminIneligibleCaseWorkspace() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "administrator identity required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		filter := parseIneligibleCaseFilter(r.URL.Query())
		access, accessErr := s.ineligibleCaseAccessForAdmin(ctx, *actor.ID)
		var items []ineligibleCaseWorkRow
		var total int64
		dataErr := accessErr
		if dataErr == nil {
			items, total, dataErr = s.loadIneligibleCaseWork(ctx, filter, access)
		}
		if dataErr != nil {
			slog.Error("load ineligible case workspace", "error", dataErr, "admin_id", *actor.ID)
		} else {
			lastPage := int((total + ineligibleCasePageSize - 1) / ineligibleCasePageSize)
			if lastPage < 1 {
				lastPage = 1
			}
			if filter.Page > lastPage {
				http.Redirect(w, r, ineligibleCaseFilterURL(filter, lastPage), http.StatusSeeOther)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if dataErr != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		pageHead(w, "Ineligible players")
		s.writeAdminWorkspaceNav(w, r)
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 py-4 admin-workspace"><header class="workspace-page-heading"><div><h1 class="h2 mb-1">Ineligible players</h1><p class="text-muted mb-0">Review incoming reports and move each case to its next action.</p></div><a class="btn btn-outline-secondary" href="/admin/dashboard">Home and My work</a></header>`)
		canTriage := s.adminHasPermission(ctx, *actor.ID, "sanctions_triage")
		writeIneligibleWorkspaceTabs(w, filter.View, canTriage, r.URL.Query())
		writeIneligibleCaseFilters(w, filter)
		if dataErr != nil {
			fmt.Fprint(w, `<section class="alert alert-warning" role="alert"><strong>Casework and its count are unavailable.</strong> Refresh to try again. No zero count has been substituted.</section>`)
		} else {
			writeIneligibleCaseResults(w, items, total, filter, access, s.LondonLoc, time.Now())
		}
		fmt.Fprint(w, `<details class="card mt-4"><summary class="card-header fw-semibold">Follow-up work and other administration</summary><div class="card-body"><p>Delivery problems and open tasks can remain after an outcome is issued. They are separate from active-case totals.</p><div class="d-flex flex-wrap gap-2">`)
		delivery := ineligibleCaseFilter{View: "cases", Scope: "all", Group: "delivery_exceptions", Training: filter.Training}
		fmt.Fprintf(w, `<a class="btn btn-outline-secondary" href="%s">Cases with delivery problems</a>`, escapeHTML(ineligibleCaseFilterURL(delivery, 1)))
		if access.CanInvestigate {
			fmt.Fprint(w, `<a class="btn btn-outline-secondary" href="/admin/cases/tasks?live=1">Open follow-up tasks</a><a class="btn btn-outline-secondary" href="/admin/cases/close-batch">Close historic cases</a>`)
		}
		if canTriage {
			fmt.Fprint(w, `<a class="btn btn-outline-secondary" href="/admin/ineligible?view=reports">Report triage</a><a class="btn btn-outline-warning" href="/admin/ineligible/training/new">Create training report</a>`)
		}
		training := filter
		training.Training, training.Page = !filter.Training, 1
		trainingLabel := "View training cases"
		if filter.Training {
			trainingLabel = "Return to live cases"
		}
		fmt.Fprintf(w, `<a class="btn btn-outline-secondary" href="%s">%s</a></div></div></details></main>`, escapeHTML(ineligibleCaseFilterURL(training, 1)), trainingLabel)
		pageFooter(w)
	}
}

func writeIneligibleWorkspaceTabs(w io.Writer, view string, canTriage bool, current ...url.Values) {
	fmt.Fprint(w, `<nav class="workspace-tabs" aria-label="Ineligible work views">`)
	for _, tab := range []struct{ view, label, href string }{
		{"reports", "Reports to review", "/admin/ineligible?view=reports"},
		{"cases", "Cases in progress", "/admin/ineligible?view=cases"},
		{"history", "Case history", "/admin/ineligible?view=history"},
	} {
		if tab.view == "reports" && !canTriage {
			continue
		}
		active := ""
		if view == tab.view {
			active = ` aria-current="page"`
		}
		href := tab.href
		if len(current) > 0 {
			values := url.Values{"view": {tab.view}}
			for _, key := range []string{"q", "fixture_from", "fixture_to", "training", "scope"} {
				if value := current[0].Get(key); value != "" {
					values.Set(key, value)
				}
			}
			if tab.view == "reports" || (values.Get("scope") == "action" && tab.view != "cases") {
				values.Set("scope", "all")
			}
			href = "/admin/ineligible?" + values.Encode()
		}
		fmt.Fprintf(w, `<a href="%s"%s>%s</a>`, escapeHTML(href), active, tab.label)
	}
	fmt.Fprint(w, `</nav>`)
}

func writeIneligibleCaseFilters(w io.Writer, filter ineligibleCaseFilter) {
	fmt.Fprintf(w, `<form method="GET" class="workspace-filters"><input type="hidden" name="view" value="%s">`, escapeHTML(filter.View))
	if filter.Training {
		fmt.Fprint(w, `<input type="hidden" name="training" value="1">`)
	}
	fmt.Fprint(w, `<label class="workspace-filter"><span>Case view</span><select class="form-select" name="scope">`)
	for _, choice := range []struct{ value, label string }{{"all", "All permitted cases"}, {"mine", "My assigned cases"}, {"action", "Needs my action"}} {
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, choice.value, selectedMode(filter.Scope, choice.value), choice.label)
	}
	fmt.Fprint(w, `</select></label><label class="workspace-filter"><span>Attention or stage</span><select class="form-select" name="group">`)
	for _, choice := range []struct{ value, label string }{
		{"", "All stages"}, {"new_replies", "Replies needing review"}, {"owner_review", "Owner notice review"},
		{"awaiting_decision", "Decision approval"}, {"awaiting_denver", "Final issue"},
		{"responses_due", "Awaiting club response"}, {"responses_overdue", "Overdue response"},
		{"delivery_exceptions", "Delivery problems (includes history)"}, {"investigating", "Investigation"},
	} {
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, choice.value, selectedMode(filter.Group, choice.value), choice.label)
	}
	fmt.Fprintf(w, `</select></label><label class="workspace-filter workspace-filter-search"><span>Search case, club, team, player or owner</span><input class="form-control" type="search" name="q" value="%s"></label><button class="btn btn-primary align-self-end">Apply filters</button><a class="btn btn-outline-secondary align-self-end" href="%s">Reset filters</a><details class="workspace-date-filters"><summary>Fixture dates and case status</summary><div class="d-flex flex-wrap gap-2 mt-2"><label class="workspace-filter"><span>Fixture from</span><input class="form-control" type="date" name="fixture_from" value="%s"></label><label class="workspace-filter"><span>Fixture to</span><input class="form-control" type="date" name="fixture_to" value="%s"></label><label class="workspace-filter"><span>Exact case status</span><select class="form-select" name="case_status"><option value="">Use selected view</option>`, escapeHTML(filter.Query), escapeHTML(ineligibleCaseFilterURL(ineligibleCaseFilter{View: filter.View, Scope: "all", Training: filter.Training}, 1)), escapeHTML(filter.FixtureFrom), escapeHTML(filter.FixtureTo))
	for _, status := range []string{"submitted", "triage", "investigating", "response_pending", "decision_proposed", "approved", "published", "appealed", "closed", "rejected", "withdrawn"} {
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, status, selectedMode(filter.CaseStatus, status), escapeHTML(caseStatusLabel(status)))
	}
	fmt.Fprint(w, `</select></label></div></details></form>`)
}

func ineligibleCaseScopeLabel(filter ineligibleCaseFilter) string {
	scope := map[string]string{"all": "all permitted cases", "mine": "assigned to you", "action": "needs your action"}[filter.Scope]
	if scope == "" {
		scope = "all permitted cases"
	}
	population := "live records"
	if filter.Training {
		population = "training and test cases"
	}
	view := "cases in progress"
	if filter.CaseStatus != "" {
		if isIneligibleCaseHistorical(filter.CaseStatus) {
			view = "case history"
		}
	} else if filter.View == "history" || filter.Group == "closed" {
		view = "case history"
	}
	if filter.Group == "delivery_exceptions" {
		if filter.CaseStatus == "" {
			view = "delivery follow-up (includes case history)"
		} else {
			view = "delivery follow-up · " + view
		}
	}
	return scope + " · " + view + " · " + population + " · all seasons"
}

func writeIneligibleCaseResults(w io.Writer, items []ineligibleCaseWorkRow, total int64, filter ineligibleCaseFilter, access ineligibleCaseAccess, loc *time.Location, now time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	values, _ := url.ParseQuery(strings.TrimPrefix(ineligibleCaseFilterURL(filter, 1), "/admin/ineligible?"))
	exportURL := "/admin/ineligible/export.csv?" + values.Encode()
	fmt.Fprintf(w, `<div class="workspace-results-meta"><div><strong>%d matching cases</strong> · %s<div class="small text-muted">Calculated %s (Europe/London). Each row is one case; attention flags overlap.</div></div><a class="btn btn-outline-secondary" href="%s">Export matching cases</a></div><section class="card"><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0"><thead><tr><th>Case and subject</th><th>Next stage</th><th>Whose action</th><th>Open work</th></tr></thead><tbody>`, total, escapeHTML(ineligibleCaseScopeLabel(filter)), now.In(loc).Format("02 Jan 2006 15:04"), escapeHTML(exportURL))
	for _, item := range items {
		action := selectAdminCaseNextAction(item.actionContext(access), now)
		returnURL := ineligibleCaseFilterURL(filter, filter.Page)
		caseURL := fmt.Sprintf("/admin/cases/%d?return_to=%s", item.ID, url.QueryEscape(returnURL))
		subject := defaultString(item.Player, "Club or team case")
		fixture := "Fixture not recorded"
		if item.MatchDate != nil {
			fixture = item.MatchDate.Format("02 Jan 2006")
		}
		reference := "<strong>" + escapeHTML(item.Reference) + "</strong>"
		if access.canOpenCase() {
			reference = fmt.Sprintf(`<a href="%s">%s</a>`, escapeHTML(caseURL), reference)
		}
		fmt.Fprintf(w, `<tr><td data-label="Case and subject">%s<div>%s</div><div class="small text-muted">%s · %s</div><div class="small text-muted">%s · %s</div></td><td data-label="Next stage"><strong>%s</strong>`, reference, escapeHTML(subject), escapeHTML(item.Club), escapeHTML(item.Team), escapeHTML(item.Season), escapeHTML(fixture), escapeHTML(action.Title))
		if item.ReplyAt != nil && item.ReplyUnreviewed && !isIneligibleCaseHistorical(item.Status) {
			fmt.Fprintf(w, `<div class="small text-muted">Reply received %s</div>`, item.ReplyAt.In(loc).Format("02 Jan 15:04"))
		}
		if item.RequestStatus == "pending" && item.DueAt != nil && item.Status == "response_pending" {
			fmt.Fprintf(w, `<div class="small text-muted">Recorded deadline: %s</div>`, item.DueAt.In(loc).Format("02 Jan 15:04"))
		}
		if item.DeliveryException {
			fmt.Fprint(w, `<div><span class="badge text-bg-warning">Delivery problem</span></div>`)
		}
		if item.OpenTasks > 0 {
			fmt.Fprintf(w, `<div class="small text-muted">%d open follow-up task(s)</div>`, item.OpenTasks)
		}
		fmt.Fprintf(w, `</td><td data-label="Whose action">%s<div class="small text-muted">Owner: %s</div></td><td data-label="Open work">`, escapeHTML(action.Responsible), escapeHTML(defaultString(item.Owner, "Unassigned")))
		if access.canOpenCase() {
			fmt.Fprintf(w, `<a class="btn btn-sm btn-outline-primary" href="%s%s">%s</a>`, escapeHTML(caseURL), escapeHTML(action.Href), escapeHTML(action.Label))
		} else {
			fmt.Fprint(w, `<span class="small text-muted">Case detail requires investigation or approval access.</span>`)
		}
		fmt.Fprint(w, "</td></tr>")
	}
	if len(items) == 0 {
		fmt.Fprint(w, `<tr><td colspan="4" class="empty-state py-4"><strong>No cases match these filters.</strong><span>Try another case view, clear the search, or check case history. Incoming reports have their own triage view.</span></td></tr>`)
	}
	fmt.Fprint(w, `</tbody></table></div></section>`)
	writeIneligiblePagination(w, filter.Page, total, ineligibleCasePageSize, func(page int) string { return ineligibleCaseFilterURL(filter, page) })
}

func isIneligibleCaseHistorical(status string) bool {
	return status == "published" || status == "closed" || status == "rejected" || status == "withdrawn"
}

func writeIneligiblePagination(w io.Writer, page int, total int64, pageSize int, href func(int) string) {
	if page < 1 {
		page = 1
	}
	pages := (total + int64(pageSize) - 1) / int64(pageSize)
	if pages < 1 {
		pages = 1
	}
	fmt.Fprintf(w, `<nav class="workspace-pagination" aria-label="Results pages"><span>Page %d of %d · %d total matching records</span><div class="d-flex gap-2">`, page, pages, total)
	if page > 1 {
		fmt.Fprintf(w, `<a class="btn btn-outline-secondary btn-sm" href="%s">Previous</a>`, escapeHTML(href(page-1)))
	}
	if int64(page) < pages {
		fmt.Fprintf(w, `<a class="btn btn-outline-secondary btn-sm" href="%s">Next</a>`, escapeHTML(href(page+1)))
	}
	fmt.Fprint(w, `</div></nav>`)
}

func buildIneligibleCaseExportQuery(filter ineligibleCaseFilter, access ineligibleCaseAccess) (string, []any) {
	query, args := buildIneligibleCaseQuery(filter, access, false)
	return query[:strings.LastIndex(query, " LIMIT $")], args[:len(args)-2]
}

func (s *Server) handleAdminIneligibleCaseExport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "administrator identity required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		access, err := s.ineligibleCaseAccessForAdmin(ctx, *actor.ID)
		if err != nil {
			http.Error(w, "case export unavailable", http.StatusServiceUnavailable)
			return
		}
		filter := parseIneligibleCaseFilter(r.URL.Query())
		query, args := buildIneligibleCaseExportQuery(filter, access)
		rows, err := s.DB.Query(ctx, query, args...)
		if err != nil {
			slog.Error("export ineligible cases", "error", err)
			http.Error(w, "case export unavailable", http.StatusServiceUnavailable)
			return
		}
		defer rows.Close()
		// Buffer before writing headers so a failed row/stream never masquerades
		// as a successfully completed export.
		var buffer strings.Builder
		writer := csv.NewWriter(&buffer)
		if err := writer.Write([]string{"Case", "Player", "Club", "Team", "Season", "Fixture date", "Next stage", "Responsible", "Owner", "Response deadline", "Delivery problem", "Open tasks", "Scope", "Calculated at"}); err != nil {
			http.Error(w, "case export unavailable", http.StatusInternalServerError)
			return
		}
		now := time.Now()
		loc := s.LondonLoc
		if loc == nil {
			loc = time.UTC
		}
		for rows.Next() {
			var item ineligibleCaseWorkRow
			if err := rows.Scan(&item.ID, &item.Reference, &item.Status, &item.Player, &item.Club, &item.Team, &item.Season, &item.Owner,
				&item.UpdatedAt, &item.MatchDate, &item.IsOwner, &item.HasProposed, &item.OwnerReviewRequired, &item.SentForApproval,
				&item.ReplyUnreviewed, &item.ReplyAt, &item.RequestStatus, &item.DeliveredAt, &item.ReminderDueAt, &item.DueAt,
				&item.DeliveryException, &item.OpenTasks, &item.SourceReviewRequired); err != nil {
				http.Error(w, "case export unavailable", http.StatusInternalServerError)
				return
			}
			action := selectAdminCaseNextAction(item.actionContext(access), now)
			date, deadline := "", ""
			if item.MatchDate != nil {
				date = item.MatchDate.Format("2006-01-02")
			}
			if item.Status == "response_pending" && item.RequestStatus == "pending" && item.DueAt != nil {
				deadline = item.DueAt.In(loc).Format(time.RFC3339)
			}
			fields := []string{item.Reference, item.Player, item.Club, item.Team, item.Season, date, action.Title, action.Responsible, item.Owner,
				deadline, strconv.FormatBool(item.DeliveryException), strconv.FormatInt(item.OpenTasks, 10), ineligibleCaseScopeLabel(filter), now.In(loc).Format(time.RFC3339)}
			for i := range fields {
				fields[i] = ineligibleCSVCell(fields[i])
			}
			if err := writer.Write(fields); err != nil {
				http.Error(w, "case export unavailable", http.StatusInternalServerError)
				return
			}
		}
		writer.Flush()
		if rows.Err() != nil || writer.Error() != nil {
			http.Error(w, "case export unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="gmcl-ineligible-cases.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, buffer.String())
	}
}

func ineligibleCSVCell(value string) string {
	trimmed := strings.TrimLeft(value, " ")
	first, _ := utf8.DecodeRuneInString(trimmed)
	if trimmed != "" && strings.ContainsRune("=+-@\t\r\n＝＋－＠", first) {
		return "'" + value
	}
	return value
}
