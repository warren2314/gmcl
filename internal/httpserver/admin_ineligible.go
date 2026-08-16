package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	ineligibledomain "cricket-ground-feedback/internal/ineligible"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	ineligibleDefaultAssigneeEnv   = "INELIGIBLE_DEFAULT_ASSIGNEE_USERNAME"
	ineligibleManualSyncTimeout    = 2 * time.Minute
	ineligibleManualSyncWriteGrace = 30 * time.Second
)

type ineligibleQueueFilters struct {
	State         string
	Origin        string
	ReportingClub string
	OffendingClub string
	Team          string
	Player        string
	Assignee      string
	CaseStatus    string
	ReplyStatus   string
	Age           string
	Scope         string
	Worklist      string
	Sort          string
	FixtureFrom   string
	FixtureTo     string
}

type ineligibleQueueRow struct {
	ID                 int64
	Origin             string
	IsTraining         bool
	ExternalKey        string
	State              string
	ReportingClubText  string
	OffendingClubText  string
	TeamText           string
	PlayerText         string
	FixtureDate        *time.Time
	ExternalCreatedAt  *time.Time
	CaseID             *int64
	CaseReference      string
	CaseStatus         string
	Assignee           string
	WorklistVisibility string
	WorklistBatchID    int64
	AttachmentCount    int64
	ReplyCount         int64
	LatestReplyAt      *time.Time
	ReplyNeedsReview   bool
}

type ineligibleDashboardCounts struct {
	NewIntakes         int64
	AwaitingSelection  int64
	HiddenReports      int64
	ActiveCases        int64
	ResponsesDue       int64
	ResponsesOverdue   int64
	RecentReplies      int64
	AwaitingDecision   int64
	DenverPointsTasks  int64
	DeliveryExceptions int64
	ClosedCases        int64
	RecentReplyCaseID  int64
}

type ineligibleSyncHealth struct {
	ID          int64
	Origin      string
	Status      string
	RowsSeen    int
	RowsNew     int
	RowsChanged int
	RowsErrored int
	Error       string
	StartedAt   time.Time
	CompletedAt *time.Time
}

type ineligibleClubOption struct {
	ID   int32
	Name string
}

type ineligibleTeamOption struct {
	ID       int32
	ClubName string
	TeamName string
}

type ineligibleCaseOption struct {
	ID        int64
	Reference string
	Status    string
	Club      string
	Team      string
}

type ineligibleCaseLinkView struct {
	CaseID       int64
	Reference    string
	Relationship string
	Reason       string
	Status       string
	Club         string
	Team         string
	Player       string
	Assignee     string
	CreatedAt    time.Time
}

func parseIneligibleQueueFilters(values url.Values) ineligibleQueueFilters {
	filter := ineligibleQueueFilters{
		State:         strings.TrimSpace(values.Get("state")),
		Origin:        strings.TrimSpace(values.Get("origin")),
		ReportingClub: strings.TrimSpace(values.Get("reporting_club")),
		OffendingClub: strings.TrimSpace(values.Get("offending_club")),
		Team:          strings.TrimSpace(values.Get("team")),
		Player:        strings.TrimSpace(values.Get("player")),
		Assignee:      strings.TrimSpace(values.Get("assignee")),
		CaseStatus:    strings.TrimSpace(values.Get("case_status")),
		ReplyStatus:   strings.TrimSpace(values.Get("reply_status")),
		Age:           strings.TrimSpace(values.Get("age")),
		Scope:         strings.TrimSpace(values.Get("scope")),
		Worklist:      strings.TrimSpace(values.Get("worklist")),
		Sort:          strings.TrimSpace(values.Get("sort")),
		FixtureFrom:   parseIneligibleFixtureDateFilter(values.Get("fixture_from")),
		FixtureTo:     parseIneligibleFixtureDateFilter(values.Get("fixture_to")),
	}
	if filter.State == "" {
		filter.State = "open"
	}
	if !map[string]bool{"open": true, "all": true, "new": true, "reviewing": true, "linked": true, "duplicate": true, "ignored": true, "exception": true}[filter.State] {
		filter.State = "open"
	}
	if !map[string]bool{"": true, "google_form": true, "native_form": true, "starred_player": true, "tracker_backfill": true}[filter.Origin] {
		filter.Origin = ""
	}
	if !map[string]bool{"": true, "submitted": true, "triage": true, "investigating": true, "response_pending": true, "decision_proposed": true, "approved": true, "published": true, "appealed": true, "closed": true, "rejected": true, "withdrawn": true}[filter.CaseStatus] {
		filter.CaseStatus = ""
	}
	if !map[string]bool{"": true, "unreviewed": true, "received": true}[filter.ReplyStatus] {
		filter.ReplyStatus = ""
	}
	if !map[string]bool{"": true, "2d": true, "7d": true, "14d": true, "30d": true}[filter.Age] {
		filter.Age = ""
	}
	if !map[string]bool{"visible": true, "deferred": true, "all": true}[filter.Worklist] {
		filter.Worklist = "visible"
	}
	if filter.Scope == "" {
		// New reports do not have a case assignee yet, so "My work" hides the
		// very items staff need in order to raise a case. Start with all work;
		// the narrower ownership view remains available under manager controls.
		filter.Scope = "all"
	}
	if filter.Scope != "mine" && filter.Scope != "all" {
		filter.Scope = "all"
	}
	if filter.Sort == "" {
		filter.Sort = "newest"
	}
	if !map[string]bool{"newest": true, "oldest": true, "fixture_newest": true, "fixture_oldest": true}[filter.Sort] {
		filter.Sort = "newest"
	}
	if filter.FixtureFrom != "" && filter.FixtureTo != "" && filter.FixtureFrom > filter.FixtureTo {
		filter.FixtureFrom, filter.FixtureTo = filter.FixtureTo, filter.FixtureFrom
	}
	return filter
}

func parseIneligibleFixtureDateFilter(value string) string {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.Format("2006-01-02")
}

func ineligibleQueueTabURL(filter ineligibleQueueFilters, scope, state, worklist string) string {
	values := url.Values{
		"scope":    {scope},
		"state":    {state},
		"worklist": {worklist},
		"sort":     {filter.Sort},
	}
	if filter.FixtureFrom != "" {
		values.Set("fixture_from", filter.FixtureFrom)
	}
	if filter.FixtureTo != "" {
		values.Set("fixture_to", filter.FixtureTo)
	}
	return "/admin/ineligible?" + values.Encode()
}

func ineligibleClearFixtureDatesURL(filter ineligibleQueueFilters) string {
	values := url.Values{}
	for key, value := range map[string]string{
		"state":          filter.State,
		"origin":         filter.Origin,
		"reporting_club": filter.ReportingClub,
		"offending_club": filter.OffendingClub,
		"team":           filter.Team,
		"player":         filter.Player,
		"assignee":       filter.Assignee,
		"case_status":    filter.CaseStatus,
		"reply_status":   filter.ReplyStatus,
		"age":            filter.Age,
		"scope":          filter.Scope,
		"worklist":       filter.Worklist,
		"sort":           filter.Sort,
	} {
		if value != "" {
			values.Set(key, value)
		}
	}
	return "/admin/ineligible?" + values.Encode()
}

func buildIneligibleQueueQuery(filter ineligibleQueueFilters) (string, []any) {
	return buildIneligibleQueueQueryForAdmin(filter, nil)
}

func buildIneligibleQueueQueryForAdmin(filter ineligibleQueueFilters, adminID *int32) (string, []any) {
	where := []string{"TRUE"}
	args := []any{}
	add := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	switch filter.State {
	case "", "open":
		where = append(where, "i.state IN ('new','reviewing','exception')")
	case "all":
	default:
		where = append(where, "i.state="+add(filter.State))
	}
	switch filter.Worklist {
	case "deferred":
		where = append(where, "COALESCE(worklist.visibility,'visible')='deferred' AND c.id IS NULL")
	case "all":
	default:
		where = append(where, "(COALESCE(worklist.visibility,'visible')='visible' OR c.id IS NOT NULL)")
	}
	if filter.Origin != "" {
		where = append(where, "i.origin="+add(filter.Origin))
	}
	if filter.ReportingClub != "" {
		where = append(where, "i.reporting_club_text ILIKE "+add("%"+filter.ReportingClub+"%"))
	}
	if filter.OffendingClub != "" {
		where = append(where, "i.offending_club_text ILIKE "+add("%"+filter.OffendingClub+"%"))
	}
	if filter.Team != "" {
		where = append(where, "i.team_text ILIKE "+add("%"+filter.Team+"%"))
	}
	if filter.Player != "" {
		where = append(where, "i.player_text ILIKE "+add("%"+filter.Player+"%"))
	}
	if filter.Assignee != "" {
		where = append(where, "a.username ILIKE "+add("%"+filter.Assignee+"%"))
	}
	if filter.CaseStatus != "" {
		where = append(where, "c.status="+add(filter.CaseStatus))
	}
	switch filter.ReplyStatus {
	case "unreviewed":
		where = append(where, "latest_reply.needs_review")
	case "received":
		where = append(where, "latest_reply.id IS NOT NULL")
	}
	if filter.Age != "" {
		days := map[string]int{"2d": 2, "7d": 7, "14d": 14, "30d": 30}[filter.Age]
		where = append(where, fmt.Sprintf("COALESCE(i.external_created_at,i.created_at) < now() - interval '%d days'", days))
	}
	if filter.FixtureFrom != "" {
		where = append(where, "i.fixture_date >= "+add(filter.FixtureFrom)+"::date")
	}
	if filter.FixtureTo != "" {
		where = append(where, "i.fixture_date <= "+add(filter.FixtureTo)+"::date")
	}
	if filter.Scope == "mine" && adminID != nil {
		where = append(where, "c.assigned_admin_id="+add(*adminID))
	}
	orderBy := "COALESCE(i.external_created_at,i.created_at) DESC,i.id DESC"
	switch filter.Sort {
	case "oldest":
		orderBy = "COALESCE(i.external_created_at,i.created_at) ASC,i.id ASC"
	case "fixture_newest":
		orderBy = "i.fixture_date DESC NULLS LAST,COALESCE(i.external_created_at,i.created_at) DESC,i.id DESC"
	case "fixture_oldest":
		orderBy = "i.fixture_date ASC NULLS LAST,COALESCE(i.external_created_at,i.created_at) ASC,i.id ASC"
	}
	if filter.ReplyStatus != "" {
		orderBy = "latest_reply.created_at DESC NULLS LAST,i.id DESC"
	}
	query := `
		SELECT i.id,i.origin,i.external_key,i.state,i.is_training,COALESCE(i.reporting_club_text,''),
		       COALESCE(i.offending_club_text,''),COALESCE(i.team_text,''),COALESCE(i.player_text,''),
		       i.fixture_date,i.external_created_at,c.id,COALESCE(c.reference,''),COALESCE(c.status,''),
		       COALESCE(a.username,''),
		       COALESCE(worklist.visibility,'visible'),COALESCE(worklist.batch_id,0),
		       (SELECT COUNT(*) FROM sanction_intake_attachments attachment WHERE attachment.intake_id=i.id),
		       COALESCE(latest_reply.reply_count,0),latest_reply.created_at,
		       COALESCE(latest_reply.needs_review,false)
		FROM sanction_intakes i
		LEFT JOIN LATERAL (
			SELECT l.case_id
			FROM sanction_intake_effective_case_links l
			WHERE l.intake_id=i.id
			ORDER BY CASE l.relationship WHEN 'primary' THEN 0 WHEN 'split' THEN 1 ELSE 2 END,l.id DESC
			LIMIT 1
		) latest_link ON TRUE
		LEFT JOIN sanction_cases c ON c.id=latest_link.case_id
		LEFT JOIN LATERAL (
			SELECT response.id,response.created_at,
			       (SELECT COUNT(*) FROM sanction_case_events all_replies
			        WHERE all_replies.case_id=c.id
			          AND all_replies.event_type IN ('party_response','external_response_recorded')) AS reply_count,
			       NOT EXISTS(
			        SELECT 1 FROM sanction_case_events reviewed
			        WHERE reviewed.case_id=c.id AND reviewed.event_type='response_reviewed'
			          AND reviewed.metadata->>'response_event_id'=response.id::text) AS needs_review
			FROM sanction_case_events response
			WHERE response.case_id=c.id AND response.event_type IN ('party_response','external_response_recorded')
			ORDER BY response.id DESC LIMIT 1
		) latest_reply ON TRUE
		LEFT JOIN admin_users a ON a.id=c.assigned_admin_id
		LEFT JOIN sanction_intake_worklist_current worklist ON worklist.intake_id=i.id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY ` + orderBy + `
		LIMIT 2000`
	return query, args
}

func (s *Server) handleAdminIneligibleDashboard() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		filter := parseIneligibleQueueFilters(r.URL.Query())
		var currentAdminID *int32
		if sess, sessionErr := getAdminSessionFromRequest(r); sessionErr == nil {
			currentAdminID = &sess.AdminID
		}
		query, args := buildIneligibleQueueQueryForAdmin(filter, currentAdminID)
		rows, err := s.DB.Query(ctx, query, args...)
		if err != nil {
			slog.Error("load ineligible intake queue", "error", err)
			http.Error(w, "could not load ineligible-player intake", http.StatusInternalServerError)
			return
		}
		queue := []ineligibleQueueRow{}
		for rows.Next() {
			var row ineligibleQueueRow
			if err := rows.Scan(&row.ID, &row.Origin, &row.ExternalKey, &row.State, &row.IsTraining, &row.ReportingClubText, &row.OffendingClubText, &row.TeamText, &row.PlayerText, &row.FixtureDate, &row.ExternalCreatedAt, &row.CaseID, &row.CaseReference, &row.CaseStatus, &row.Assignee, &row.WorklistVisibility, &row.WorklistBatchID, &row.AttachmentCount, &row.ReplyCount, &row.LatestReplyAt, &row.ReplyNeedsReview); err != nil {
				rows.Close()
				http.Error(w, "could not read ineligible-player intake", http.StatusInternalServerError)
				return
			}
			queue = append(queue, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			http.Error(w, "could not read ineligible-player intake", http.StatusInternalServerError)
			return
		}
		rows.Close()

		counts, _ := s.loadIneligibleDashboardCounts(ctx)
		syncHealth, hasSyncHealth := s.loadIneligibleSyncHealth(ctx)
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Ineligible-player work")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 py-4"><div class="d-flex flex-column flex-lg-row justify-content-between gap-3 mb-4"><div><h1 class="h2 mb-1">Ineligible-player cases</h1><p class="text-muted mb-0">Import, choose the reports to progress, then work from that selected list. New arrivals stay visible until they are next reviewed.</p></div><div class="d-flex flex-wrap gap-2 align-self-lg-start"><a class="btn btn-warning" href="/admin/ineligible/training/new">Create training report</a><a class="btn btn-primary" href="/admin/ineligible/selection">Change selected reports</a><a class="btn btn-outline-secondary" href="/admin/ineligible?worklist=deferred&amp;scope=all&amp;state=open">View hidden reports</a><a class="btn btn-outline-secondary" href="/admin/ineligible">Refresh</a><a class="btn btn-outline-primary" href="/admin/cases">All sanction cases</a></div></div>`)
		writeIneligibleFlash(w, r)
		var nextReportID int64
		if filter.Worklist == "visible" {
			nextReportID = nextIneligibleReportID(queue)
		}
		writeIneligibleStartRoutes(w, csrf, nextReportID)
		advancedOpen := ""
		if ineligibleQueueUsesAdvancedView(filter) {
			advancedOpen = " open"
		}
		fmt.Fprintf(w, `<details class="card mb-4"%s><summary class="card-header fw-semibold">More filters and queue status</summary><div class="card-body">`, advancedOpen)
		mineClass, selectedClass, importedClass := "btn-outline-primary", "btn-outline-primary", "btn-outline-primary"
		if filter.Scope == "mine" {
			mineClass = "btn-primary"
		} else if filter.Worklist == "all" {
			importedClass = "btn-primary"
		} else if filter.Worklist == "visible" {
			selectedClass = "btn-primary"
		}
		mineHref := ineligibleQueueTabURL(filter, "mine", "all", "visible")
		selectedHref := ineligibleQueueTabURL(filter, "all", "open", "visible")
		importedHref := ineligibleQueueTabURL(filter, "all", "all", "all")
		fmt.Fprintf(w, `<nav class="btn-group mb-3" aria-label="Choose work queue"><a class="btn %s" href="%s">My assigned cases</a><a class="btn %s" href="%s">Selected reports</a><a class="btn %s" href="%s">Report history</a></nav>`, mineClass, escapeHTML(mineHref), selectedClass, escapeHTML(selectedHref), importedClass, escapeHTML(importedHref))
		fmt.Fprint(w, `<div class="row row-cols-2 row-cols-md-3 row-cols-xl-5 g-2 mb-3">`)
		newRepliesHref := ineligibleNewRepliesHref(counts)
		for _, card := range []struct {
			Label string
			Count int64
			Class string
			Href  string
		}{
			{"Visible queue", counts.NewIntakes, "border-primary", "/admin/ineligible?scope=all&state=open&worklist=visible"},
			{"Not yet selected", counts.AwaitingSelection, "border-warning", "/admin/ineligible/selection"},
			{"Hidden reports", counts.HiddenReports, "border-secondary", "/admin/ineligible?scope=all&state=open&worklist=deferred"},
			{"Under investigation", counts.ActiveCases, "border-primary", "/admin/ineligible?scope=all&state=all&case_status=investigating"},
			{"Responses due", counts.ResponsesDue, "border-warning", "/admin/ineligible?scope=all&state=all&case_status=response_pending"},
			{"Responses overdue", counts.ResponsesOverdue, "border-danger", "/admin/ineligible?scope=all&state=all&case_status=investigating"},
			{"New replies", counts.RecentReplies, "border-info", newRepliesHref},
			{"Awaiting decision", counts.AwaitingDecision, "border-primary", "/admin/ineligible?scope=all&state=all&case_status=decision_proposed"},
			{"Denver points tasks", counts.DenverPointsTasks, "border-warning", "/admin/cases/tasks"},
			{"Delivery exceptions", counts.DeliveryExceptions, "border-danger", "/admin/cases"},
			{"Closed cases", counts.ClosedCases, "border-success", "/admin/ineligible?scope=all&state=all&case_status=closed"},
		} {
			fmt.Fprintf(w, `<div class="col"><a class="border rounded d-block h-100 %s text-decoration-none text-body p-2" href="%s"><div class="h4 mb-0">%d</div><div class="small text-muted">%s</div></a></div>`, card.Class, escapeHTML(card.Href), card.Count, escapeHTML(card.Label))
		}
		fmt.Fprint(w, `</div>`)
		if hasSyncHealth {
			completed := "still running"
			if syncHealth.CompletedAt != nil {
				completed = syncHealth.CompletedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")
			}
			alertClass := "alert-success"
			if syncHealth.Status == "failed" || syncHealth.Status == "partial" {
				alertClass = "alert-danger"
			} else if syncHealth.Status == "running" {
				alertClass = "alert-info"
			}
			fmt.Fprintf(w, `<section class="alert %s d-flex flex-column flex-lg-row justify-content-between gap-2 mb-3"><div><strong>Latest %s import: %s</strong><div class="small">Run %d; seen %d, new %d, changed %d, errors %d. Completed: %s.</div>`, alertClass, escapeHTML(strings.ReplaceAll(syncHealth.Origin, "_", " ")), escapeHTML(syncHealth.Status), syncHealth.ID, syncHealth.RowsSeen, syncHealth.RowsNew, syncHealth.RowsChanged, syncHealth.RowsErrored, escapeHTML(completed))
			fmt.Fprintf(w, `<div class="small mt-1"><a href="/admin/ineligible/selection?run_id=%d">Choose reports from this import</a></div>`, syncHealth.ID)
			if syncHealth.Error != "" {
				fmt.Fprintf(w, `<div class="small mt-1">%s</div>`, escapeHTML(syncHealth.Error))
			}
			fmt.Fprint(w, `</div><div class="small text-nowrap">Started `+escapeHTML(syncHealth.StartedAt.In(s.LondonLoc).Format("02 Jan 15:04"))+`</div></section>`)
		} else {
			fmt.Fprint(w, `<div class="alert alert-warning mb-3"><strong>No Google Form import has run.</strong> Configure the read-only source before using the import action.</div>`)
		}

		writeIneligibleFilters(w, filter)
		queueTitle := "Selected reports ready for review"
		queueHelp := "Open a selected report, check its details, then raise or link its case. New arrivals also stay visible until the next selection."
		switch {
		case filter.ReplyStatus == "unreviewed":
			queueTitle = "Replies awaiting review"
			queueHelp = "Each row below has a reply that still needs review. Open case jumps directly to the received reply."
		case filter.Scope == "mine" || filter.CaseStatus != "" || filter.ReplyStatus != "":
			queueTitle = "Work matching this view"
			queueHelp = "Open a report or case to continue from its current step."
		case filter.Worklist == "all" && filter.State == "all":
			queueTitle = "Report history"
			queueHelp = "This view includes open, selected, hidden, case-raised, duplicate and ignored reports. Up to 2,000 matching reports are shown; narrow the dates or filters if needed."
		case filter.Worklist == "deferred":
			queueTitle = "Hidden reports"
			queueHelp = "These reports are hidden from the normal queue, not deleted. Change the selection to restore one."
		case filter.Worklist == "all":
			queueTitle = "All imported reports"
			queueHelp = "This audit view shows selected, newly arrived and hidden reports."
		case filter.State == "all":
			queueTitle = "Work matching this view"
			queueHelp = "Open a report or case to continue from its current step."
		case filter.Origin == "google_form":
			queueTitle = "Google Form reports ready for review"
			queueHelp = "Open each imported report, check its details, then raise, link or resolve it."
		}
		fmt.Fprint(w, `</div></details>`)
		writeIneligibleFixtureDateControls(w, filter)
		fmt.Fprintf(w, `<section class="card shadow-sm" id="reports"><div class="card-header"><h2 class="h5 mb-1">%s</h2><p class="small text-muted mb-0">%s</p></div><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0"><thead><tr><th>Report</th><th>Fixture</th><th>Received</th><th>Status</th><th>Next step</th></tr></thead><tbody>`, escapeHTML(queueTitle), escapeHTML(queueHelp))
		for _, row := range queue {
			received := "-"
			if row.ExternalCreatedAt != nil {
				received = row.ExternalCreatedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")
			}
			fixture := ""
			if row.FixtureDate != nil {
				fixture = row.FixtureDate.Format("02 Jan 2006")
			}
			caseHTML := ineligibleCaseNextStepHTML(row, s.LondonLoc)
			rowClass := ""
			if row.IsTraining {
				rowClass = ` class="table-warning"`
			}
			if row.CaseID != nil {
				rowClass = ` class="table-success"`
				if row.ReplyNeedsReview {
					rowClass = ` class="table-info"`
				}
			}
			evidenceHTML := ""
			if row.AttachmentCount > 0 {
				label := "files"
				if row.AttachmentCount == 1 {
					label = "file"
				}
				evidenceHTML = fmt.Sprintf(`<div class="small mt-1"><span class="badge text-bg-light border">%d evidence %s</span></div>`, row.AttachmentCount, label)
			}

			visibilityHTML := ""
			if row.Origin == "google_form" && row.CaseID == nil {
				visibilityLabel := "Selected"
				visibilityClass := "text-bg-primary"
				if row.WorklistVisibility == "deferred" {
					visibilityLabel = "Hidden"
					visibilityClass = "text-bg-secondary"
				} else if row.WorklistBatchID == 0 {
					visibilityLabel = "New - not yet chosen"
					visibilityClass = "text-bg-warning"
				}
				if filter.Worklist != "visible" || row.WorklistBatchID == 0 {
					visibilityHTML = fmt.Sprintf(` <span class="badge %s">%s</span>`, visibilityClass, escapeHTML(visibilityLabel))
				}
			}
			if row.IsTraining {
				visibilityHTML += ` <span class="badge text-bg-warning">Training - real email enabled</span>`
			}
			fmt.Fprintf(w, `<tr%s><td data-label="Report"><a href="/admin/ineligible/%d"><strong>%s</strong></a><div class="small text-muted">%s / %s</div><div class="small text-muted">Reported by %s</div>%s</td><td data-label="Fixture">%s</td><td data-label="Received">%s<div class="small text-muted">%s</div></td><td data-label="Status">%s%s</td><td data-label="Next step">%s</td></tr>`, rowClass, row.ID, escapeHTML(row.PlayerText), escapeHTML(row.OffendingClubText), escapeHTML(row.TeamText), escapeHTML(row.ReportingClubText), evidenceHTML, escapeHTML(fixture), escapeHTML(received), escapeHTML(strings.ReplaceAll(row.Origin, "_", " ")), ineligibleStateBadge(row.State), visibilityHTML, caseHTML)
		}
		if len(queue) == 0 {
			writeIneligibleEmptyQueue(w, filter, queueTitle)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func ineligibleNewRepliesHref(counts ineligibleDashboardCounts) string {
	if counts.RecentReplies == 1 && counts.RecentReplyCaseID > 0 {
		return fmt.Sprintf("/admin/cases/%d#club-response", counts.RecentReplyCaseID)
	}
	return "/admin/ineligible?scope=all&state=all&worklist=all&reply_status=unreviewed#reports"
}
func ineligibleCaseNextStepHTML(row ineligibleQueueRow, loc *time.Location) string {
	if row.CaseID == nil {
		return fmt.Sprintf(`<a class="btn btn-sm btn-primary" href="/admin/ineligible/%d">Review report</a>`, row.ID)
	}
	if loc == nil {
		loc = time.Local
	}
	href := fmt.Sprintf("/admin/cases/%d", *row.CaseID)
	buttonClass := "btn-success"
	if row.ReplyNeedsReview {
		href += "#club-response"
		buttonClass = "btn-info"
	}
	result := fmt.Sprintf(`<a class="btn btn-sm %s" href="%s">Open case %s</a><div class="small text-success-emphasis mt-1">%s`, buttonClass, href, escapeHTML(row.CaseReference), escapeHTML(plainIneligibleStatus(row.CaseStatus)))
	if row.Assignee != "" {
		result += `; ` + escapeHTML(row.Assignee)
	}
	result += `</div>`
	if row.ReplyCount == 0 {
		return result
	}
	label := "Reply received"
	badgeClass := "text-bg-success"
	if row.ReplyNeedsReview {
		label = "Reply received - needs review"
		badgeClass = "text-bg-danger"
	}
	replyWord := "replies"
	if row.ReplyCount == 1 {
		replyWord = "reply"
	}
	received := ""
	if row.LatestReplyAt != nil {
		received = "Latest " + row.LatestReplyAt.In(loc).Format("02 Jan 15:04")
	}
	return result + fmt.Sprintf(`<div class="mt-2"><span class="badge %s">%s</span> <span class="badge text-bg-light border">%d %s total</span><div class="small text-muted">%s</div></div>`, badgeClass, escapeHTML(label), row.ReplyCount, replyWord, escapeHTML(received))
}

func writeIneligibleStartRoutes(w io.Writer, csrf string, nextReportID int64) {
	nextHref := "/admin/ineligible?scope=all&state=open&worklist=visible#reports"
	nextLabel := "View reports"
	if nextReportID > 0 {
		nextHref = fmt.Sprintf("/admin/ineligible/%d", nextReportID)
		nextLabel = "Open next selected report"
	}
	fmt.Fprint(w, `<section class="mb-4" aria-labelledby="ineligible-start-title"><h2 class="h4 mb-3" id="ineligible-start-title">What do you want to do?</h2><div class="row row-cols-1 row-cols-lg-3 g-3">`)
	fmt.Fprintf(w, `<div class="col"><article class="card h-100 border-primary"><div class="card-body d-flex flex-column"><div class="small text-primary fw-semibold mb-2">ROUTE 1</div><h3 class="h5">Raise one case</h3><p class="text-muted flex-grow-1">Open a report, check the pre-filled details, then select <strong>Raise case</strong>.</p><a class="btn btn-primary" href="%s">%s</a></div></article></div>`, escapeHTML(nextHref), escapeHTML(nextLabel))
	fmt.Fprintf(w, `<div class="col"><article class="card h-100"><form class="card-body d-flex flex-column" method="POST" action="/admin/ineligible/sync"><input type="hidden" name="csrf_token" value="%s"><div class="small text-primary fw-semibold mb-2">ROUTE 2</div><h3 class="h5">Import and choose reports</h3><p class="text-muted flex-grow-1">Import the Google Form, then tick the reports you have been asked to progress.</p><button class="btn btn-outline-primary">Import and choose reports</button></form></article></div>`, escapeHTML(csrf))
	fmt.Fprint(w, `<div class="col"><article class="card h-100"><div class="card-body d-flex flex-column"><div class="small text-primary fw-semibold mb-2">ROUTE 3</div><h3 class="h5">Import historical tracker</h3><p class="text-muted flex-grow-1">Upload the Excel tracker, check its matches, sign off, then apply history.</p><a class="btn btn-outline-primary" href="/admin/ineligible/backfill">Open tracker import</a></div></article></div></div><div class="alert alert-light border mt-3 mb-0"><strong>Safe by design:</strong> importing never sends an email or issues a sanction. A member of staff must deliberately raise each live case.</div></section>`)
}

func nextIneligibleReportID(queue []ineligibleQueueRow) int64 {
	for _, row := range queue {
		if row.CaseID == nil && (row.State == "new" || row.State == "reviewing" || row.State == "exception") {
			return row.ID
		}
	}
	return 0
}

func ineligibleQueueUsesAdvancedView(filter ineligibleQueueFilters) bool {
	return filter.State != "open" ||
		(filter.Origin != "" && filter.Origin != "google_form") ||
		filter.ReportingClub != "" ||
		filter.OffendingClub != "" ||
		filter.Team != "" ||
		filter.Player != "" ||
		filter.Assignee != "" ||
		filter.CaseStatus != "" ||
		filter.ReplyStatus != "" ||
		filter.Age != "" ||
		filter.Scope != "all" ||
		filter.Worklist != "visible" ||
		filter.Sort != "newest"
}

func (s *Server) loadIneligibleDashboardCounts(ctx context.Context) (ineligibleDashboardCounts, error) {
	var counts ineligibleDashboardCounts
	err := s.DB.QueryRow(ctx, `
		WITH live_intakes AS (SELECT * FROM sanction_intakes WHERE NOT is_training),
		live_cases AS (
			SELECT c.* FROM sanction_cases c WHERE NOT c.is_test
			AND NOT EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=c.id AND training.event_type='case_training_designated')
		)
		SELECT
		 (SELECT COUNT(*) FROM live_intakes intake LEFT JOIN sanction_intake_worklist_current worklist ON worklist.intake_id=intake.id WHERE intake.state IN ('new','reviewing','exception') AND (COALESCE(worklist.visibility,'visible')='visible' OR EXISTS(SELECT 1 FROM sanction_intake_effective_case_links link WHERE link.intake_id=intake.id))),
		 (SELECT COUNT(*) FROM live_intakes intake LEFT JOIN sanction_intake_worklist_current worklist ON worklist.intake_id=intake.id WHERE intake.origin='google_form' AND intake.state IN ('new','reviewing','exception') AND worklist.batch_id IS NULL AND NOT EXISTS(SELECT 1 FROM sanction_intake_effective_case_links link WHERE link.intake_id=intake.id)),
		 (SELECT COUNT(*) FROM live_intakes intake JOIN sanction_intake_worklist_current worklist ON worklist.intake_id=intake.id WHERE intake.state IN ('new','reviewing','exception') AND worklist.visibility='deferred' AND NOT EXISTS(SELECT 1 FROM sanction_intake_effective_case_links link WHERE link.intake_id=intake.id)),
		 (SELECT COUNT(*) FROM live_cases WHERE source_type='ineligible_player' AND status='investigating'),
		 (SELECT COUNT(*) FROM live_cases c JOIN LATERAL (
			SELECT rr.status,rr.due_at FROM sanction_response_requests rr WHERE rr.case_id=c.id ORDER BY rr.id DESC LIMIT 1
		 ) latest ON TRUE WHERE c.source_type='ineligible_player' AND latest.status='pending' AND latest.due_at>=now()),
		 (SELECT COUNT(*) FROM live_cases c JOIN LATERAL (
			SELECT rr.status,rr.due_at FROM sanction_response_requests rr WHERE rr.case_id=c.id ORDER BY rr.id DESC LIMIT 1
		 ) latest ON TRUE WHERE c.source_type='ineligible_player' AND c.status NOT IN ('closed','rejected','withdrawn','published')
			AND ((latest.status='pending' AND latest.due_at<now()) OR latest.status='expired')),
		 (SELECT COUNT(*) FROM live_cases c JOIN LATERAL (
			SELECT e.id,e.created_at FROM sanction_case_events e WHERE e.case_id=c.id AND e.event_type IN ('party_response','external_response_recorded') ORDER BY e.id DESC LIMIT 1
		 ) reply ON TRUE WHERE c.source_type='ineligible_player' AND NOT EXISTS (
			SELECT 1 FROM sanction_case_events reviewed WHERE reviewed.case_id=c.id AND reviewed.event_type='response_reviewed'
			  AND reviewed.metadata->>'response_event_id'=reply.id::text)),
		 (SELECT COUNT(*) FROM live_cases WHERE source_type='ineligible_player' AND status IN ('decision_proposed','approved')),
		 (SELECT COUNT(*) FROM sanction_follow_up_tasks t JOIN live_cases c ON c.id=t.case_id LEFT JOIN admin_users a ON a.id=t.assigned_admin_id WHERE c.source_type='ineligible_player' AND t.task_type='play_cricket_points' AND t.status IN ('open','in_progress') AND LOWER(COALESCE(a.username,''))='denverthornton'),
		 (SELECT COUNT(DISTINCT o.id) FROM sanction_notification_outbox o JOIN live_cases c ON c.id=o.case_id WHERE c.source_type='ineligible_player' AND o.revoked_at IS NULL AND EXISTS (
			SELECT 1 FROM sanction_notification_attempts latest WHERE latest.id=(SELECT attempt.id FROM sanction_notification_attempts attempt WHERE attempt.outbox_id=o.id ORDER BY attempt.attempt_number DESC,attempt.id DESC LIMIT 1) AND latest.status IN ('failed','bounced','complained'))),
		 (SELECT COUNT(*) FROM live_cases WHERE source_type='ineligible_player' AND status IN ('closed','rejected','withdrawn'))
	`).Scan(&counts.NewIntakes, &counts.AwaitingSelection, &counts.HiddenReports, &counts.ActiveCases, &counts.ResponsesDue, &counts.ResponsesOverdue, &counts.RecentReplies, &counts.AwaitingDecision, &counts.DenverPointsTasks, &counts.DeliveryExceptions, &counts.ClosedCases)
	if err == nil && counts.RecentReplies == 1 {
		_ = s.DB.QueryRow(ctx, `SELECT c.id FROM sanction_cases c JOIN LATERAL (
			SELECT event.id FROM sanction_case_events event
			WHERE event.case_id=c.id AND event.event_type IN ('party_response','external_response_recorded')
			ORDER BY event.id DESC LIMIT 1
		) reply ON TRUE
		WHERE c.source_type='ineligible_player' AND NOT EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=c.id AND training.event_type='case_training_designated')
		  AND NOT EXISTS (
			SELECT 1 FROM sanction_case_events reviewed
			WHERE reviewed.case_id=c.id AND reviewed.event_type='response_reviewed'
			  AND reviewed.metadata->>'response_event_id'=reply.id::text)
		LIMIT 1`).Scan(&counts.RecentReplyCaseID)
	}
	return counts, err
}

func (s *Server) loadIneligibleSyncHealth(ctx context.Context) (ineligibleSyncHealth, bool) {
	var result ineligibleSyncHealth
	err := s.DB.QueryRow(ctx, `SELECT id,origin,status,rows_seen,rows_new,rows_changed,rows_errored,COALESCE(error_message,''),started_at,completed_at FROM sanction_intake_sync_runs WHERE origin='google_form' ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&result.ID, &result.Origin, &result.Status, &result.RowsSeen, &result.RowsNew, &result.RowsChanged, &result.RowsErrored, &result.Error, &result.StartedAt, &result.CompletedAt)
	return result, err == nil
}

func writeIneligibleFixtureDateControls(w io.Writer, filter ineligibleQueueFilters) {
	fmt.Fprintf(w, `<form method="GET" action="/admin/ineligible" class="border rounded bg-body-tertiary p-3 mb-3" aria-label="Fixture date controls"><input type="hidden" name="scope" value="%s"><input type="hidden" name="state" value="%s"><input type="hidden" name="worklist" value="%s"><input type="hidden" name="origin" value="%s"><input type="hidden" name="reporting_club" value="%s"><input type="hidden" name="offending_club" value="%s"><input type="hidden" name="team" value="%s"><input type="hidden" name="player" value="%s"><input type="hidden" name="assignee" value="%s"><input type="hidden" name="case_status" value="%s"><input type="hidden" name="reply_status" value="%s"><input type="hidden" name="age" value="%s"><div class="row g-2 align-items-end"><div class="col-12 col-lg"><strong>Fixture dates</strong><div class="small text-muted">Choose a range or put the fixture column into date order.</div></div><div class="col-6 col-md-3 col-lg-2"><label class="form-label" for="fixture-from">From</label><input class="form-control" id="fixture-from" type="date" name="fixture_from" value="%s"></div><div class="col-6 col-md-3 col-lg-2"><label class="form-label" for="fixture-to">To</label><input class="form-control" id="fixture-to" type="date" name="fixture_to" value="%s"></div><div class="col-12 col-md-4 col-lg-3"><label class="form-label" for="fixture-order">Order</label><select class="form-select" id="fixture-order" name="sort">`, escapeHTML(filter.Scope), escapeHTML(filter.State), escapeHTML(filter.Worklist), escapeHTML(filter.Origin), escapeHTML(filter.ReportingClub), escapeHTML(filter.OffendingClub), escapeHTML(filter.Team), escapeHTML(filter.Player), escapeHTML(filter.Assignee), escapeHTML(filter.CaseStatus), escapeHTML(filter.ReplyStatus), escapeHTML(filter.Age), escapeHTML(filter.FixtureFrom), escapeHTML(filter.FixtureTo))
	writeIneligibleDateSortOptions(w, filter.Sort)
	fmt.Fprint(w, `</select></div><div class="col-auto"><button class="btn btn-primary">Apply dates</button></div>`)
	if filter.FixtureFrom != "" || filter.FixtureTo != "" {
		fmt.Fprintf(w, `<div class="col-auto"><a class="btn btn-outline-secondary" href="%s">Clear dates</a></div>`, escapeHTML(ineligibleClearFixtureDatesURL(filter)))
	}
	fmt.Fprint(w, `</div></form>`)
}

func writeIneligibleEmptyQueue(w io.Writer, filter ineligibleQueueFilters, queueTitle string) {
	if filter.FixtureFrom != "" || filter.FixtureTo != "" {
		fmt.Fprintf(w, `<tr><td colspan="5" class="text-center text-muted py-5">No reports match these fixture dates. <a href="%s">Clear dates</a> or choose a wider range.</td></tr>`, escapeHTML(ineligibleClearFixtureDatesURL(filter)))
		return
	}
	if filter.ReplyStatus == "unreviewed" {
		fmt.Fprint(w, `<tr><td colspan="5" class="text-center text-muted py-5">No club replies are waiting for review.</td></tr>`)
		return
	}
	emptyMessage := "No reports match this view. Open the manager controls above to clear filters."
	if filter.Worklist == "visible" {
		emptyMessage = "No reports are currently selected. Import and choose reports, or change the selected work list."
	}
	if queueTitle == "Work matching this view" {
		emptyMessage = "No work matches this view. Open the manager controls above to clear filters."
	}
	fmt.Fprintf(w, `<tr><td colspan="5" class="text-center text-muted py-5">%s</td></tr>`, escapeHTML(emptyMessage))
}

func writeIneligibleDateSortOptions(w io.Writer, selectedValue string) {
	for _, option := range []struct{ Value, Label string }{
		{"fixture_newest", "Newest fixture first"},
		{"fixture_oldest", "Oldest fixture first"},
		{"newest", "Newest report received"},
		{"oldest", "Oldest report received"},
	} {
		selected := ""
		if selectedValue == option.Value {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, option.Value, selected, option.Label)
	}
}

func writeIneligibleFilters(w http.ResponseWriter, filter ineligibleQueueFilters) {
	fmt.Fprintf(w, `<form method="GET" action="/admin/ineligible" class="border rounded p-3 mb-0"><input type="hidden" name="scope" value="%s"><input type="hidden" name="fixture_from" value="%s"><input type="hidden" name="fixture_to" value="%s"><div class="d-flex justify-content-between gap-3 mb-3"><div><strong>Find work</strong><div class="small text-muted">Use only the filters you need. Clear returns to selected reports.</div></div><span class="small text-muted">Current queue: %s</span></div><div class="row g-3"><div class="col-6 col-lg-2"><label class="form-label">Work list</label><select class="form-select" name="worklist">`, escapeHTML(filter.Scope), escapeHTML(filter.FixtureFrom), escapeHTML(filter.FixtureTo), escapeHTML(plainIneligibleWorklist(filter.Worklist)))
	for _, option := range []struct{ Value, Label string }{{"visible", "Selected reports"}, {"deferred", "Hidden reports"}, {"all", "All imported reports"}} {
		selected := ""
		if filter.Worklist == option.Value {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, option.Value, selected, option.Label)
	}
	fmt.Fprint(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Work status</label><select class="form-select" name="state">`)
	writeSelectedOptions(w, filter.State, []string{"open", "all", "new", "reviewing", "exception", "linked", "duplicate", "ignored"})
	fmt.Fprint(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Source</label><select class="form-select" name="origin"><option value="">All sources</option>`)
	writeSelectedOptions(w, filter.Origin, []string{"google_form", "native_form", "starred_player", "tracker_backfill"})
	fmt.Fprintf(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Reporting club</label><input class="form-control" name="reporting_club" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Offending club</label><input class="form-control" name="offending_club" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Team</label><input class="form-control" name="team" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Player</label><input class="form-control" name="player" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Assignee</label><input class="form-control" name="assignee" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Case status</label><select class="form-select" name="case_status"><option value="">All statuses</option>`, escapeHTML(filter.ReportingClub), escapeHTML(filter.OffendingClub), escapeHTML(filter.Team), escapeHTML(filter.Player), escapeHTML(filter.Assignee))
	writeSelectedOptions(w, filter.CaseStatus, []string{"submitted", "triage", "investigating", "response_pending", "decision_proposed", "approved", "published", "appealed", "closed", "rejected", "withdrawn"})
	fmt.Fprint(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Reply status</label><select class="form-select" name="reply_status"><option value="">All replies</option>`)
	writeSelectedOptions(w, filter.ReplyStatus, []string{"unreviewed", "received"})
	fmt.Fprint(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Older than</label><select class="form-select" name="age"><option value="">Any age</option>`)
	for _, option := range []struct{ Value, Label string }{{"2d", "2 days"}, {"7d", "7 days"}, {"14d", "14 days"}, {"30d", "30 days"}} {
		selected := ""
		if filter.Age == option.Value {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, option.Value, selected, option.Label)
	}
	fmt.Fprint(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Order</label><select class="form-select" name="sort">`)
	writeIneligibleDateSortOptions(w, filter.Sort)
	fmt.Fprintf(w, `</select></div><div class="col-12 col-lg-4 d-flex align-items-end gap-2"><button class="btn btn-primary">Apply filters</button><a class="btn btn-outline-secondary" href="/admin/ineligible?scope=%s&amp;worklist=visible">Clear</a></div></div></form>`, escapeHTML(filter.Scope))
}

func writeSelectedOptions(w http.ResponseWriter, selected string, values []string) {
	for _, value := range values {
		attr := ""
		if selected == value {
			attr = " selected"
		}
		label := strings.Title(strings.ReplaceAll(value, "_", " ")) //nolint:staticcheck -- labels are short ASCII workflow values.
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(value), attr, escapeHTML(label))
	}
}

func plainIneligibleWorklist(value string) string {
	switch value {
	case "deferred":
		return "Hidden reports"
	case "all":
		return "All imported reports"
	default:
		return "Selected reports"
	}
}

func ineligibleStateBadge(state string) string {
	class := map[string]string{"new": "text-bg-danger", "reviewing": "text-bg-primary", "linked": "text-bg-success", "duplicate": "text-bg-secondary", "ignored": "text-bg-secondary", "exception": "text-bg-warning"}[state]
	if class == "" {
		class = "text-bg-light"
	}
	return fmt.Sprintf(`<span class="badge %s">%s</span>`, class, escapeHTML(plainIneligibleStatus(state)))
}

func plainIneligibleStatus(value string) string {
	labels := map[string]string{
		"mine":                   "My work",
		"all":                    "All work",
		"open":                   "Needs attention",
		"new":                    "New report",
		"reviewing":              "Being reviewed",
		"linked":                 "Case raised",
		"duplicate":              "Duplicate",
		"ignored":                "No action needed",
		"exception":              "Import needs help",
		"submitted":              "Submitted",
		"triage":                 "Initial review",
		"investigating":          "Investigation in progress",
		"response_pending":       "Waiting for response",
		"decision_proposed":      "Decision ready for approval",
		"approved":               "Approved",
		"published":              "Published",
		"appealed":               "Appeal in progress",
		"closed":                 "Closed",
		"rejected":               "Rejected",
		"withdrawn":              "Withdrawn",
		"draft":                  "Draft",
		"locked":                 "Approved and locked",
		"queued":                 "Queued to send",
		"sent":                   "Sent",
		"response_request":       "Response request",
		"response_reminder":      "Response reminder",
		"outcome_offending_club": "Outcome",
		"outcome_reporting_club": "Outcome",
		"outcome_official":       "Outcome",
		"no_action_outcome":      "No-action outcome",
		"offending_club":         "Offending club",
		"reporting_club":         "Reporting club",
		"official":               "League official",
	}
	if label := labels[value]; label != "" {
		return label
	}
	return strings.Title(strings.ReplaceAll(value, "_", " ")) //nolint:staticcheck -- short workflow labels.
}

func writeIneligibleFlash(w http.ResponseWriter, r *http.Request) {
	if success := strings.TrimSpace(r.URL.Query().Get("success")); success != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(success))
	}
	if message := strings.TrimSpace(r.URL.Query().Get("error")); message != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(message))
	}
}

func (s *Server) handleAdminIneligibleCount() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var count int64
		if err := s.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM sanction_intakes intake LEFT JOIN sanction_intake_worklist_current worklist ON worklist.intake_id=intake.id WHERE intake.state IN ('new','reviewing','exception') AND (COALESCE(worklist.visibility,'visible')='visible' OR EXISTS(SELECT 1 FROM sanction_intake_effective_case_links link WHERE link.intake_id=intake.id))`).Scan(&count); err != nil {
			http.Error(w, "count unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]int64{"count": count})
	}
}

func (s *Server) handleAdminIneligibleSync() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		adminID := int64(*actor.ID)
		if err := setIneligibleSyncWriteDeadline(w, time.Now()); err != nil {
			slog.Warn("extend manual ineligible sync write deadline", "admin_id", adminID, "error", err)
		}
		ctx, cancel := context.WithTimeout(r.Context(), ineligibleManualSyncTimeout)
		defer cancel()
		summary, err := ineligibledomain.SyncFromEnv(ctx, s.DB, ineligibledomain.Trigger{Type: "admin", AdminID: &adminID})
		if err != nil {
			message := "Sync failed. Check sync health before retrying."
			switch {
			case errors.Is(err, ineligibledomain.ErrImportDisabled):
				message = "Import is disabled by INELIGIBLE_IMPORT_ENABLED."
			case errors.Is(err, ineligibledomain.ErrSyncInProgress):
				message = "Another ineligible-player sync is already running."
			case errors.Is(err, ineligibledomain.ErrBackfillPrerequisite):
				message = "Manual staging is blocked until bootstrap mode is enabled or the named tracker reconciliation is signed off and applied."
			}
			slog.Error("manual ineligible intake sync", "admin_id", adminID, "run_id", summary.RunID, "error", err)
			redirectIneligibleDashboard(w, r, "error", message)
			return
		}
		message := fmt.Sprintf("Google Form import %d completed: %d seen, %d added, %d changed, %d errors.", summary.RunID, summary.Seen, summary.New, summary.Changed, summary.Errors)
		values := url.Values{"run_id": []string{strconv.FormatInt(summary.RunID, 10)}, "success": []string{message}}
		http.Redirect(w, r, "/admin/ineligible/selection?"+values.Encode(), http.StatusSeeOther)
	}
}

func setIneligibleSyncWriteDeadline(w http.ResponseWriter, now time.Time) error {
	return http.NewResponseController(w).SetWriteDeadline(now.Add(ineligibleManualSyncTimeout + ineligibleManualSyncWriteGrace))
}

func (s *Server) handleAdminIneligibleDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intakeID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || intakeID <= 0 {
			http.NotFound(w, r)
			return
		}
		var origin, externalKey, state, reportingText, offendingText, teamText, playerText, exception string
		var fixtureDate, externalCreatedAt *time.Time
		var isTraining bool
		var revision int
		var rawJSON []byte
		err = s.DB.QueryRow(r.Context(), `
			SELECT i.origin,i.external_key,i.state,i.is_training,COALESCE(i.reporting_club_text,''),COALESCE(i.offending_club_text,''),
			       COALESCE(i.team_text,''),COALESCE(i.player_text,''),i.fixture_date,i.external_created_at,
			       COALESCE(i.exception_message,''),COALESCE(rev.revision,0),COALESCE(rev.raw_data,'{}'::jsonb)
			FROM sanction_intakes i
			LEFT JOIN LATERAL (SELECT r.revision,r.raw_data FROM sanction_intake_revisions r WHERE r.intake_id=i.id ORDER BY r.revision DESC LIMIT 1) rev ON TRUE
			WHERE i.id=$1`, intakeID).Scan(&origin, &externalKey, &state, &isTraining, &reportingText, &offendingText, &teamText, &playerText, &fixtureDate, &externalCreatedAt, &exception, &revision, &rawJSON)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		clubs, teams, cases, err := s.loadIneligibleMappingOptions(r.Context())
		if err != nil {
			http.Error(w, "could not load mapping options", http.StatusInternalServerError)
			return
		}
		links, _ := s.loadIneligibleCaseLinks(r.Context(), intakeID)
		var activeLink *ineligibleCaseLinkView
		for index := range links {
			if links[index].Relationship != "duplicate" {
				activeLink = &links[index]
				break
			}
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Ineligible intake "+strconv.FormatInt(intakeID, 10))
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:1180px"><a class="btn btn-sm btn-outline-secondary mb-3" href="/admin/ineligible">Back to intake queue</a>`)
		writeIneligibleFlash(w, r)
		fmt.Fprintf(w, `<div class="d-flex flex-column flex-md-row justify-content-between gap-2 mb-4"><div><h1 class="h2 mb-1">Intake %d</h1><p class="text-muted mb-0">%s / %s / revision %d</p></div><div>%s</div></div>`, intakeID, escapeHTML(strings.ReplaceAll(origin, "_", " ")), escapeHTML(externalKey), revision, ineligibleStateBadge(state))
		if isTraining {
			fmt.Fprint(w, `<div class="alert alert-warning"><strong>Training report - real email enabled.</strong> It is excluded from live workload totals. Raising the case opens the normal investigation, Denver approval and email workflow. <a class="alert-link" href="/admin/ineligible/training/new">Create another training report</a>.</div>`)
		}
		if exception != "" {
			fmt.Fprintf(w, `<div class="alert alert-warning"><strong>Import exception:</strong> %s</div>`, escapeHTML(exception))
		}
		fmt.Fprint(w, `<div class="row g-4"><div class="col-lg-7">`)
		fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header fw-semibold">Reported details</div><div class="card-body"><dl class="row mb-0"><dt class="col-sm-4">Reporting club</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Offending club</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Team</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Player</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Fixture date</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Received</dt><dd class="col-sm-8">%s</dd></dl></div></section>`, escapeHTML(reportingText), escapeHTML(offendingText), escapeHTML(teamText), escapeHTML(playerText), escapeHTML(formatOptionalDate(fixtureDate)), escapeHTML(formatOptionalDateTime(externalCreatedAt, s.LondonLoc)))
		pretty := prettyJSON(rawJSON)
		fmt.Fprintf(w, `<details class="card mb-4"><summary class="card-header fw-semibold">Original submitted data <span class="small text-muted">(audit copy)</span></summary><div class="card-body"><p class="small text-muted">This saved copy cannot be edited, so there is always a reliable record of what was originally received.</p><pre class="small mb-0" style="white-space:pre-wrap;overflow-wrap:anywhere">%s</pre></div></details>`, escapeHTML(pretty))
		fmt.Fprint(w, nativeIntakeEvidenceLinks(rawJSON, intakeID, revision))
		s.writeIneligibleIntakeAttachments(w, r.Context(), intakeID)
		if len(links) > 0 {
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-header fw-semibold">Linked case records</div><div class="card-body">`)
			for _, link := range links {
				fmt.Fprintf(w, `<article class="border rounded p-3 mb-3"><strong>%s</strong><div class="small text-muted mb-2">%s; %s; %s / %s`, escapeHTML(link.Reference), escapeHTML(link.Relationship), escapeHTML(plainIneligibleStatus(link.Status)), escapeHTML(link.Club), escapeHTML(link.Team))
				if link.Player != "" {
					fmt.Fprint(w, `; `+escapeHTML(link.Player))
				}
				fmt.Fprint(w, `</div>`)
				if link.Reason != "" {
					fmt.Fprint(w, `<p class="small">`+escapeHTML(link.Reason)+`</p>`)
				}
				buttonClass, buttonLabel := ineligibleCaseLinkAction(link.Relationship)
				fmt.Fprintf(w, `<a class="%s" href="/admin/cases/%d">%s</a></article>`, buttonClass, link.CaseID, buttonLabel)
			}
			fmt.Fprint(w, `</div></section>`)
		}
		writeIneligibleIntakeEvents(w, s, r.Context(), intakeID)
		fmt.Fprint(w, `</div><aside class="col-lg-5">`)
		if activeLink == nil && state != "duplicate" && state != "ignored" {
			writeIneligibleCreateCaseForm(w, csrf, intakeID, reportingText, playerText, fixtureDate, rawJSON, clubs, teams, false)
			fmt.Fprint(w, `<details class="card mb-4"><summary class="card-header fw-semibold">Other outcomes</summary><div class="card-body"><p class="small text-muted">Use these only when this report belongs to existing work or should not become a new case.</p>`)
			writeIneligibleLinkCaseForm(w, csrf, intakeID, reportingText, clubs, teams, cases)
			writeIneligibleResolutionForms(w, csrf, intakeID, state, reportingText, clubs, cases, false)
			fmt.Fprint(w, `</div></details>`)
		} else if activeLink != nil {
			fmt.Fprintf(w, `<section class="card border-success mb-4"><div class="card-body"><h2 class="h5 text-success">Case %s is ready</h2><p>Continue the investigation, correspondence and decision on the case page.</p><a class="btn btn-success w-100" href="/admin/cases/%d">Open case</a></div></section>`, escapeHTML(activeLink.Reference), activeLink.CaseID)
			fmt.Fprint(w, `<details class="card mb-4"><summary class="card-header fw-semibold">Advanced case links</summary><div class="card-body"><p class="small text-muted">Only use this if the report genuinely needs another or split investigation.</p>`)
			writeIneligibleCreateCaseForm(w, csrf, intakeID, reportingText, playerText, fixtureDate, rawJSON, clubs, teams, true)
			writeIneligibleLinkCaseForm(w, csrf, intakeID, reportingText, clubs, teams, cases)
			fmt.Fprint(w, `</div></details>`)
		} else {
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h5">Report resolved</h2><p class="mb-0">This report is recorded as duplicate or no action. Its audit history remains below.</p></div></section>`)
		}
		fmt.Fprint(w, `</aside></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) writeIneligibleIntakeAttachments(w http.ResponseWriter, ctx context.Context, intakeID int64) {
	rows, err := s.DB.Query(ctx, `SELECT attachment.id,revision.revision,attachment.original_filename,attachment.content_type,attachment.size_bytes,attachment.sha256,attachment.observed_at
		FROM sanction_intake_attachments attachment
		JOIN sanction_intake_revisions revision ON revision.id=attachment.revision_id AND revision.intake_id=attachment.intake_id
		WHERE attachment.intake_id=$1 ORDER BY revision.revision,attachment.id`, intakeID)
	if err != nil {
		return
	}
	defer rows.Close()
	var content strings.Builder
	count := 0
	for rows.Next() {
		var attachmentID, size int64
		var revision int
		var name, media, digest string
		var observed time.Time
		if rows.Scan(&attachmentID, &revision, &name, &media, &size, &digest, &observed) != nil {
			continue
		}
		count++
		attachmentURL := fmt.Sprintf("/admin/ineligible/%d/attachments/%d", intakeID, attachmentID)
		metadata := fmt.Sprintf(`Source revision %d &middot; %s &middot; %d bytes &middot; file fingerprint %s &middot; retained %s`, revision, escapeHTML(media), size, escapeHTML(digest[:minInt(12, len(digest))]), escapeHTML(observed.In(s.LondonLoc).Format("02 Jan 2006 15:04")))
		switch ineligibleAttachmentPreviewKind(media) {
		case "image":
			fmt.Fprintf(&content, `<div class="col"><article class="border rounded h-100 overflow-hidden"><a href="%s?preview=1" target="_blank" rel="noopener noreferrer"><img src="%s?preview=1" alt="Preview of %s" loading="lazy" decoding="async" style="display:block;width:100%%;height:220px;object-fit:contain;background:#f8f9fa"></a><div class="p-3"><div class="fw-semibold text-break">%s</div><div class="small text-muted mt-1">%s</div><a class="btn btn-sm btn-outline-secondary mt-2" href="%s">Download original</a></div></article></div>`, attachmentURL, attachmentURL, escapeHTML(name), escapeHTML(name), metadata, attachmentURL)
			continue
		case "video":
			fmt.Fprintf(&content, `<div class="col"><article class="border rounded h-100 overflow-hidden"><video controls preload="metadata" playsinline style="display:block;width:100%%;height:220px;background:#000"><source src="%s?preview=1" type="video/mp4">Your browser does not support MP4 video.</video><div class="p-3"><div class="fw-semibold text-break">%s</div><div class="small text-muted mt-1">%s</div><a class="btn btn-sm btn-outline-secondary mt-2" href="%s">Download original</a></div></article></div>`, attachmentURL, escapeHTML(name), metadata, attachmentURL)
			continue
		}
		fmt.Fprintf(&content, `<div class="col"><article class="border rounded h-100 p-3"><a class="fw-semibold text-break" href="%s">%s</a><div class="small text-muted mt-1">%s</div></article></div>`, attachmentURL, escapeHTML(name), metadata)
	}
	if count > 0 {
		fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header fw-semibold">Original Google Drive evidence <span class="badge text-bg-secondary ms-1">%d</span></div><div class="card-body"><p class="small text-muted">These saved originals cannot be edited. Add a reviewed copy if information must be hidden before sharing.</p><div class="row row-cols-1 row-cols-md-2 g-3">%s</div></div></section>`, count, content.String())
	}
}

func ineligibleAttachmentPreviewKind(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(contentType))
	}
	switch strings.ToLower(mediaType) {
	case "image/gif", "image/jpeg", "image/png", "image/webp":
		return "image"
	case "video/mp4":
		return "video"
	default:
		return ""
	}
}

func ineligibleAttachmentDisposition(contentType string, preview bool) string {
	if preview && ineligibleAttachmentPreviewKind(contentType) != "" {
		return "inline"
	}
	return "attachment"
}

func (s *Server) handleAdminIneligibleAttachmentDownload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intakeID, intakeErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		attachmentID, attachmentErr := strconv.ParseInt(chi.URLParam(r, "attachmentID"), 10, 64)
		if intakeErr != nil || attachmentErr != nil || intakeID < 1 || attachmentID < 1 {
			http.NotFound(w, r)
			return
		}
		var name, media, storageKey, expectedDigest string
		var expectedSize int64
		var observed time.Time
		if err := s.DB.QueryRow(r.Context(), `SELECT original_filename,content_type,storage_key,size_bytes,sha256,observed_at
			FROM sanction_intake_attachments WHERE id=$1 AND intake_id=$2`, attachmentID, intakeID).Scan(&name, &media, &storageKey, &expectedSize, &expectedDigest, &observed); err != nil {
			http.NotFound(w, r)
			return
		}
		baseDirectory := strings.TrimSpace(os.Getenv("INELIGIBLE_UPLOAD_DIR"))
		if baseDirectory == "" {
			baseDirectory = "/app/data/ineligible-uploads"
		}
		data, err := readIneligibleRetainedUpload(baseDirectory, storageKey)
		if errors.Is(err, errInvalidIneligibleRetainedUploadPath) {
			http.Error(w, "retained evidence provenance is invalid", http.StatusInternalServerError)
			return
		}
		if err != nil {
			http.Error(w, "retained evidence is unavailable", http.StatusInternalServerError)
			return
		}
		actualDigest := fmt.Sprintf("%x", sha256.Sum256(data))
		if int64(len(data)) != expectedSize || !strings.EqualFold(actualDigest, expectedDigest) {
			http.Error(w, "retained evidence failed checksum verification", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", media)
		disposition := ineligibleAttachmentDisposition(media, r.URL.Query().Get("preview") == "1")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename=%q`, disposition, strings.ReplaceAll(filepath.Base(name), `"`, "")))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, filepath.Base(name), observed, bytes.NewReader(data))
	}
}

var errInvalidIneligibleRetainedUploadPath = errors.New("invalid retained upload path")

func readIneligibleRetainedUpload(baseDirectory, storageKey string) ([]byte, error) {
	baseDirectory = strings.TrimSpace(baseDirectory)
	relative := filepath.Clean(filepath.FromSlash(storageKey))
	if baseDirectory == "" || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errInvalidIneligibleRetainedUploadPath
	}
	root, err := os.OpenRoot(baseDirectory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(relative)
}

func (s *Server) loadIneligibleMappingOptions(ctx context.Context) ([]ineligibleClubOption, []ineligibleTeamOption, []ineligibleCaseOption, error) {
	clubs := []ineligibleClubOption{}
	clubRows, err := s.DB.Query(ctx, `SELECT id,name FROM clubs ORDER BY name`)
	if err != nil {
		return nil, nil, nil, err
	}
	for clubRows.Next() {
		var option ineligibleClubOption
		if err := clubRows.Scan(&option.ID, &option.Name); err != nil {
			clubRows.Close()
			return nil, nil, nil, err
		}
		clubs = append(clubs, option)
	}
	clubRows.Close()
	teams := []ineligibleTeamOption{}
	teamRows, err := s.DB.Query(ctx, `SELECT t.id,c.name,t.name FROM teams t JOIN clubs c ON c.id=t.club_id WHERE t.active ORDER BY c.name,t.name`)
	if err != nil {
		return nil, nil, nil, err
	}
	for teamRows.Next() {
		var option ineligibleTeamOption
		if err := teamRows.Scan(&option.ID, &option.ClubName, &option.TeamName); err != nil {
			teamRows.Close()
			return nil, nil, nil, err
		}
		teams = append(teams, option)
	}
	teamRows.Close()
	cases := []ineligibleCaseOption{}
	caseRows, err := s.DB.Query(ctx, `SELECT c.id,c.reference,c.status,COALESCE(cl.name,''),COALESCE(t.name,'') FROM sanction_cases c LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id WHERE c.source_type='ineligible_player' ORDER BY c.created_at DESC LIMIT 250`)
	if err != nil {
		return nil, nil, nil, err
	}
	for caseRows.Next() {
		var option ineligibleCaseOption
		if err := caseRows.Scan(&option.ID, &option.Reference, &option.Status, &option.Club, &option.Team); err != nil {
			caseRows.Close()
			return nil, nil, nil, err
		}
		cases = append(cases, option)
	}
	caseRows.Close()
	return clubs, teams, cases, nil
}

func (s *Server) loadIneligibleCaseLinks(ctx context.Context, intakeID int64) ([]ineligibleCaseLinkView, error) {
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.reference,l.relationship,COALESCE(l.reason,''),c.status,COALESCE(cl.name,''),COALESCE(t.name,''),COALESCE(c.player_name,''),COALESCE(a.username,''),l.created_at FROM sanction_intake_case_links l JOIN sanction_cases c ON c.id=l.case_id LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id LEFT JOIN admin_users a ON a.id=c.assigned_admin_id WHERE l.intake_id=$1 ORDER BY l.id`, intakeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []ineligibleCaseLinkView{}
	for rows.Next() {
		var item ineligibleCaseLinkView
		if err := rows.Scan(&item.CaseID, &item.Reference, &item.Relationship, &item.Reason, &item.Status, &item.Club, &item.Team, &item.Player, &item.Assignee, &item.CreatedAt); err != nil {
			return nil, err
		}
		links = append(links, item)
	}
	return links, rows.Err()
}

func ineligibleCaseLinkAction(relationship string) (className, label string) {
	if relationship == "duplicate" {
		return "btn btn-outline-secondary", "Open related case"
	}
	return "btn btn-success", "Open case and continue"
}

func writeIneligibleCreateCaseForm(w http.ResponseWriter, csrf string, intakeID int64, reportingText, playerText string, fixtureDate *time.Time, rawJSON []byte, clubs []ineligibleClubOption, teams []ineligibleTeamOption, split bool) {
	title := "Raise this case"
	button := "Raise case"
	if split {
		title = "Raise a split case"
		button = "Raise another case"
	}
	defaultDate := ""
	if fixtureDate != nil {
		defaultDate = fixtureDate.Format("2006-01-02")
	}
	publicSummary := ineligibleDefaultPublicSummary(playerText, fixtureDate)
	privateSummary := sourceStringField(rawJSON, "Reason you believe the player is ineligible", "reason why the player is ineligible", "reason for report", "details", "reason", "allegation")
	fmt.Fprintf(w, `<section class="card border-primary mb-4"><div class="card-header fw-semibold">%s</div><form method="POST" action="/admin/ineligible/%d/create-case"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><p class="small text-muted">Check the four items below. Most details are already filled from the report.</p><label class="form-label fw-semibold">1. Offending team</label><select class="form-select mb-3" name="team_id" required><option value="">Choose the club and team...</option>`, escapeHTML(title), intakeID, csrf)
	for _, team := range teams {
		fmt.Fprintf(w, `<option value="%d">%s - %s</option>`, team.ID, escapeHTML(team.ClubName), escapeHTML(team.TeamName))
	}
	leagueOrigin := reportingText == "GMCL Official"
	required := " required"
	blankLabel := "Select the reporting club..."
	if leagueOrigin {
		required = ""
		blankLabel = "League-origin / GMCL Official"
	}
	fmt.Fprintf(w, `</select><label class="form-label fw-semibold">2. Reporting club</label><select class="form-select mb-1" name="reporting_club_id"%s><option value="">%s</option>`, required, blankLabel)
	for _, club := range clubs {
		selected := ""
		if strings.EqualFold(strings.TrimSpace(reportingText), strings.TrimSpace(club.Name)) {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, selected, escapeHTML(club.Name))
	}
	fmt.Fprintf(w, `</select><div class="form-text mb-3">Reported as: %s. Use league-origin only for a GMCL Official report.</div><div class="row g-3 mb-3"><div class="col-md-6"><label class="form-label fw-semibold">3. Fixture date</label><input class="form-control" type="date" name="match_date" value="%s" required></div><div class="col-md-6"><label class="form-label fw-semibold">4. Player</label><input class="form-control" name="player_name" value="%s" maxlength="200" required></div></div><details class="border rounded"><summary class="p-3 fw-semibold">Review case wording <span class="small text-muted">(usually no change)</span></summary><div class="px-3 pb-3"><label class="form-label">Recorded allegation</label><textarea class="form-control mb-3" name="public_summary" rows="3" maxlength="5000" required>%s</textarea><label class="form-label">Private investigation context</label><textarea class="form-control" name="private_summary" rows="3" maxlength="10000">%s</textarea></div></details></div><div class="card-footer"><button class="btn btn-primary w-100">%s</button><div class="form-text mt-2">This opens the investigation only. It does not email either club or decide the outcome.</div></div></form></section>`, escapeHTML(reportingText), escapeHTML(defaultDate), escapeHTML(playerText), escapeHTML(publicSummary), escapeHTML(privateSummary), escapeHTML(button))
}

func writeIneligibleLinkCaseForm(w http.ResponseWriter, csrf string, intakeID int64, reportingText string, clubs []ineligibleClubOption, teams []ineligibleTeamOption, cases []ineligibleCaseOption) {
	fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header fw-semibold">Link to an existing case</div><form method="POST" action="/admin/ineligible/%d/link-case"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><select class="form-select mb-3" name="case_id" required><option value="">Choose case...</option>`, intakeID, csrf)
	writeIneligibleCaseOptions(w, cases)
	fmt.Fprint(w, `</select><label class="form-label">Mapped subject team</label><select class="form-select mb-3" name="team_id" required><option value="">Choose mapped club and team...</option>`)
	for _, team := range teams {
		fmt.Fprintf(w, `<option value="%d">%s - %s</option>`, team.ID, escapeHTML(team.ClubName), escapeHTML(team.TeamName))
	}
	leagueOrigin := reportingText == "GMCL Official"
	required := ` required`
	blankLabel := "Select the reporting club..."
	if leagueOrigin {
		required = ""
		blankLabel = "League-origin / GMCL Official"
	}
	fmt.Fprintf(w, `</select><label class="form-label">Reporting club</label><select class="form-select mb-1" name="reporting_club_id"%s><option value="">%s</option>`, required, blankLabel)
	for _, club := range clubs {
		selected := ""
		if strings.EqualFold(strings.TrimSpace(reportingText), strings.TrimSpace(club.Name)) {
			selected = ` selected`
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, selected, escapeHTML(club.Name))
	}
	fmt.Fprintf(w, `</select><div class="form-text mb-3">Reported as: %s. Only an exact GMCL Official intake may remain league-origin.</div><label class="form-label">Reason for link</label><textarea class="form-control" name="reason" rows="2" maxlength="2000" required></textarea></div><div class="card-footer"><button class="btn btn-outline-primary">Link and merge intake</button><div class="form-text mt-2">Adds the player or team, reporting-club details and saved private evidence. No notice is queued.</div></div></form></section>`, escapeHTML(reportingText))
}

func writeIneligibleResolutionForms(w http.ResponseWriter, csrf string, intakeID int64, state, reportingText string, clubs []ineligibleClubOption, cases []ineligibleCaseOption, hasActiveCaseLink bool) {
	if state == "ignored" || state == "duplicate" || hasActiveCaseLink {
		return
	}
	fmt.Fprintf(w, `<section class="card border-secondary mb-4"><div class="card-header fw-semibold">Resolve without a new case</div><div class="card-body"><form method="POST" action="/admin/ineligible/%d/duplicate" class="mb-4"><input type="hidden" name="csrf_token" value="%s"><label class="form-label">Duplicate of case</label><select class="form-select mb-2" name="case_id" required><option value="">Choose case...</option>`, intakeID, csrf)
	writeIneligibleCaseOptions(w, cases)
	leagueOrigin := reportingText == "GMCL Official"
	required := ` required`
	blankLabel := "Select the reporting club..."
	if leagueOrigin {
		required = ""
		blankLabel = "League-origin / GMCL Official"
	}
	fmt.Fprintf(w, `</select><label class="form-label">Reporting club to receive the outcome</label><select class="form-select mb-2" name="reporting_club_id"%s><option value="">%s</option>`, required, blankLabel)
	for _, club := range clubs {
		selected := ""
		if strings.EqualFold(strings.TrimSpace(reportingText), strings.TrimSpace(club.Name)) {
			selected = ` selected`
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, selected, escapeHTML(club.Name))
	}
	fmt.Fprint(w, `</select><div class="form-text mb-2">A duplicate external report still receives the approved reporting-club findings and outcome.</div><label class="form-label">Audit reason</label><textarea class="form-control mb-2" name="reason" rows="2" maxlength="2000" required></textarea><button class="btn btn-sm btn-outline-secondary">Mark duplicate</button></form>`)
	fmt.Fprintf(w, `<form method="POST" action="/admin/ineligible/%d/ignore"><input type="hidden" name="csrf_token" value="%s"><label class="form-label">Reason to ignore / mark irrelevant</label><textarea class="form-control mb-2" name="reason" rows="2" maxlength="2000" required></textarea><button class="btn btn-sm btn-outline-danger">Ignore intake</button></form></div></section>`, intakeID, csrf)
}

func writeIneligibleCaseOptions(w http.ResponseWriter, cases []ineligibleCaseOption) {
	for _, item := range cases {
		fmt.Fprintf(w, `<option value="%d">%s - %s - %s / %s</option>`, item.ID, escapeHTML(item.Reference), escapeHTML(item.Status), escapeHTML(item.Club), escapeHTML(item.Team))
	}
}

func writeIneligibleIntakeEvents(w http.ResponseWriter, s *Server, ctx context.Context, intakeID int64) {
	rows, err := s.DB.Query(ctx, `SELECT event_type,COALESCE(actor_label,''),reason,created_at FROM sanction_intake_events WHERE intake_id=$1 ORDER BY id DESC`, intakeID)
	if err != nil {
		return
	}
	defer rows.Close()
	fmt.Fprint(w, `<details class="card mb-4"><summary class="card-header fw-semibold">Source and audit history</summary><div class="card-body py-2 small text-muted">Previous actions stay visible. Add a correction instead of changing an earlier entry.</div><ul class="list-group list-group-flush">`)
	count := 0
	for rows.Next() {
		var eventType, actor, reason string
		var created time.Time
		if rows.Scan(&eventType, &actor, &reason, &created) == nil {
			count++
			fmt.Fprintf(w, `<li class="list-group-item"><strong>%s</strong><div>%s</div><small class="text-muted">%s - %s</small></li>`, escapeHTML(strings.ReplaceAll(eventType, "_", " ")), escapeHTML(reason), escapeHTML(created.In(s.LondonLoc).Format("02 Jan 2006 15:04")), escapeHTML(actor))
		}
	}
	if count == 0 {
		fmt.Fprint(w, `<li class="list-group-item text-muted">No triage actions recorded.</li>`)
	}
	fmt.Fprint(w, `</ul></details>`)
}

func formatOptionalDate(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format("02 Jan 2006")
}

func formatOptionalDateTime(value *time.Time, location *time.Location) string {
	if value == nil {
		return "-"
	}
	return value.In(location).Format("02 Jan 2006 15:04")
}

func prettyJSON(raw []byte) string {
	var output bytes.Buffer
	if len(raw) > 0 && json.Indent(&output, raw, "", "  ") == nil {
		return output.String()
	}
	return string(raw)
}

func sourceFieldKey(value string) string {
	var output strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			output.WriteRune(r)
		}
	}
	return output.String()
}

func sourceStringField(raw []byte, aliases ...string) string {
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return ""
	}
	wanted := map[string]bool{}
	for _, alias := range aliases {
		wanted[sourceFieldKey(alias)] = true
	}
	for key, value := range values {
		if !wanted[sourceFieldKey(key)] {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case []any:
			parts := []string{}
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
			return strings.Join(parts, ", ")
		}
	}
	return ""
}

func sourceInt64Field(raw []byte, aliases ...string) *int64 {
	value := sourceStringField(raw, aliases...)
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return nil
	}
	return &parsed
}

func ineligibleDefaultPublicSummary(player string, fixtureDate *time.Time) string {
	player = strings.TrimSpace(player)
	if player == "" {
		player = "the reported player"
	}
	if fixtureDate == nil {
		return "Ineligible-player report concerning " + player + "."
	}
	return fmt.Sprintf("Ineligible-player report concerning %s in the fixture on %s.", player, fixtureDate.Format("02 January 2006"))
}

func (s *Server) configuredIneligibleAssignee(ctx context.Context) (int32, string, error) {
	username := strings.TrimSpace(os.Getenv(ineligibleDefaultAssigneeEnv))
	if username == "" {
		return 0, "", fmt.Errorf("%s is not configured", ineligibleDefaultAssigneeEnv)
	}
	var id int32
	var resolved string
	err := s.DB.QueryRow(ctx, `SELECT id,username FROM admin_users WHERE LOWER(username)=LOWER($1) AND is_active`, username).Scan(&id, &resolved)
	if err != nil {
		return 0, "", fmt.Errorf("configured investigator %q is missing or inactive", username)
	}
	return id, resolved, nil
}

func (s *Server) handleAdminIneligibleCreateCase() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intakeID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || intakeID <= 0 || r.ParseForm() != nil {
			http.Error(w, "invalid intake", http.StatusBadRequest)
			return
		}
		teamID, teamErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("team_id")), 10, 32)
		matchDate, dateErr := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue("match_date")))
		player := strings.TrimSpace(r.FormValue("player_name"))
		publicSummary := strings.TrimSpace(r.FormValue("public_summary"))
		privateSummary := strings.TrimSpace(r.FormValue("private_summary"))
		var reportingClubID *int32
		if value := strings.TrimSpace(r.FormValue("reporting_club_id")); value != "" {
			parsed, parseErr := strconv.ParseInt(value, 10, 32)
			if parseErr != nil || parsed <= 0 {
				http.Error(w, "invalid reporting club", http.StatusBadRequest)
				return
			}
			converted := int32(parsed)
			reportingClubID = &converted
		}
		if teamErr != nil || teamID <= 0 || dateErr != nil || player == "" || publicSummary == "" {
			http.Error(w, "offending team, fixture date, player and allegation are required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		assigneeID, assignee, err := s.configuredIneligibleAssignee(r.Context())
		if err != nil {
			slog.Warn("ineligible case creation blocked", "error", err)
			http.Error(w, "Case creation is blocked: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not create case", http.StatusInternalServerError)
			return
		}
		var isTraining bool
		defer tx.Rollback(r.Context())
		var state, origin, externalKey, reportingText, offendingText string
		var revisionID int64
		var rawJSON []byte
		err = tx.QueryRow(r.Context(), `SELECT i.state,i.origin,i.external_key,i.is_training,COALESCE(i.reporting_club_text,''),COALESCE(i.offending_club_text,''),rev.id,rev.raw_data FROM sanction_intakes i JOIN LATERAL (SELECT id,raw_data FROM sanction_intake_revisions WHERE intake_id=i.id ORDER BY revision DESC LIMIT 1) rev ON TRUE WHERE i.id=$1 FOR UPDATE OF i`, intakeID).Scan(&state, &origin, &externalKey, &isTraining, &reportingText, &offendingText, &revisionID, &rawJSON)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if state == "ignored" || state == "duplicate" {
			http.Error(w, "resolved intake cannot create a case", http.StatusConflict)
			return
		}
		leagueOrigin := reportingText == "GMCL Official"
		if reportingClubID == nil && !leagueOrigin {
			http.Error(w, "the reporting club must be mapped; only an exact GMCL Official report may be league-origin", http.StatusBadRequest)
			return
		}
		if reportingClubID != nil && leagueOrigin {
			http.Error(w, "GMCL Official reports must remain league-origin", http.StatusBadRequest)
			return
		}
		var clubID int32
		var offendingClubName, teamName string
		if err = tx.QueryRow(r.Context(), `SELECT t.club_id,c.name,t.name FROM teams t JOIN clubs c ON c.id=t.club_id WHERE t.id=$1 AND t.active`, teamID).Scan(&clubID, &offendingClubName, &teamName); err != nil {
			http.Error(w, "active offending team not found", http.StatusBadRequest)
			return
		}
		var reportingClubName string
		if reportingClubID != nil {
			if err = tx.QueryRow(r.Context(), `SELECT name FROM clubs WHERE id=$1`, *reportingClubID).Scan(&reportingClubName); err != nil {
				http.Error(w, "reporting club not found", http.StatusBadRequest)
				return
			}
		}
		var seasonID int32
		var weekID *int32
		var matchedWeek int32
		if tx.QueryRow(r.Context(), `SELECT season_id,id FROM weeks WHERE $1::date BETWEEN start_date AND end_date ORDER BY id DESC LIMIT 1`, matchDate).Scan(&seasonID, &matchedWeek) == nil {
			weekID = &matchedWeek
		} else if err = tx.QueryRow(r.Context(), `SELECT id FROM seasons WHERE $1::date BETWEEN start_date AND end_date ORDER BY id DESC LIMIT 1`, matchDate).Scan(&seasonID); err != nil {
			http.Error(w, "no season covers the selected fixture date", http.StatusBadRequest)
			return
		}
		reporterName := sourceStringField(rawJSON, "reporter name", "Your Name & Role at Club/League", "your name", "name")
		reporterEmail := sourceStringField(rawJSON, "reporter email", "email address", "email")
		// Google Form imports combine the reporter's name and role, while native
		// portal submissions retain separate verified name and role fields. Keep the
		// combined legacy value only when no separate reporter-name field exists.
		reporterRole := sourceStringField(rawJSON, "reporter role", "role within club", "your role at the club", "role")
		reporterPhone := sourceStringField(rawJSON, "Your Preferred tel no", "reporter phone", "contact number", "phone number", "telephone", "phone")
		playCricketMatchID := sourceInt64Field(rawJSON, "play-cricket match id", "play cricket match id")
		playCricketPlayerID := sourceInt64Field(rawJSON, "play-cricket player id", "play cricket player id")
		var existingLinks int
		_ = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM sanction_intake_effective_case_links WHERE intake_id=$1`, intakeID).Scan(&existingLinks)
		relationship := "primary"
		if existingLinks > 0 {
			relationship = "split"
		}
		var caseID int64
		var reference string
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_cases(source_type,status,season_id,week_id,club_id,team_id,player_name,match_date,play_cricket_match_id,public_summary,private_summary,reporter_name,reporter_email,reporter_role,reporter_phone,reporting_club_id,assigned_admin_id) VALUES('ineligible_player','investigating',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id,reference`, seasonID, weekID, clubID, teamID, player, matchDate, playCricketMatchID, publicSummary, nullIfEmptyHTTP(privateSummary), nullIfEmptyHTTP(reporterName), nullIfEmptyHTTP(reporterEmail), nullIfEmptyHTTP(reporterRole), nullIfEmptyHTTP(reporterPhone), reportingClubID, assigneeID).Scan(&caseID, &reference)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_case_links(intake_id,case_id,relationship,reason,created_by_admin_id) VALUES($1,$2,$3,$4,$5)`, intakeID, caseID, relationship, "Created from "+origin+" intake "+externalKey, *actor.ID)
		}
		if err == nil {
			err = mergeIneligibleIntakeIntoCase(r.Context(), tx, ineligibleIntakeMerge{
				CaseID: caseID, IntakeID: intakeID, RevisionID: revisionID, TeamID: teamID,
				PlayerName: player, PlayCricketPlayer: playCricketPlayerID, PlayCricketMatch: playCricketMatchID,
				OffendingClubName: offendingClubName,
				ReportingClubID:   reportingClubID, ReportingClubName: reportingClubName,
				LeagueOrigin: leagueOrigin, CreatedByAdminID: *actor.ID, Primary: true, Relationship: relationship,
			})
		}
		if err == nil && (reporterName != "" || reporterEmail != "") {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_parties(case_id,party_type,name,email) VALUES($1,'reporter',$2,$3)`, caseID, nullIfEmptyHTTP(reporterName), nullIfEmptyHTTP(reporterEmail))
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,metadata,request_id) VALUES($1,'ineligible_intake_case_created','admin',$2,$3,$4,jsonb_build_object('reference',$5::text,'team_id',$6::bigint,'reporting_club_id',$7::integer,'assigned_admin_id',$8::integer),jsonb_build_object('intake_id',$9::bigint,'origin',$10::text,'external_key',$11::text),$12)`, caseID, *actor.ID, actor.Label, "Investigation created from private intake and assigned to "+assignee, reference, teamID, reportingClubID, assigneeID, intakeID, origin, externalKey, actor.RequestID)
		}
		if err == nil && isTraining {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,metadata,request_id) VALUES($1,'case_training_designated','admin',$2,$3,'Created from a staff-authorised ineligible-player training report with real email enabled',jsonb_build_object('training_case',TRUE,'real_email_enabled',TRUE),jsonb_build_object('intake_id',$4::bigint),$5)`, caseID, *actor.ID, actor.Label, intakeID, actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'case_created',$2,$3,$4,jsonb_build_object('state',$5::text),jsonb_build_object('state','linked','case_id',$6::bigint,'reference',$7::text,'relationship',$8::text,'offending_club_id',$9::integer,'reporting_club_id',$10::integer,'team_id',$11::bigint),$12)`, intakeID, *actor.ID, actor.Label, "Created "+reference+" without sending correspondence", state, caseID, reference, relationship, clubID, reportingClubID, teamID, actor.RequestID)
		}
		if err == nil {
			err = projectIneligibleIntakeMergeState(r.Context(), tx, intakeID)
		}
		if err != nil {
			slog.Error("create ineligible case", "intake_id", intakeID, "error", err)
			http.Error(w, "could not create ineligible-player case", http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "could not create ineligible-player case", http.StatusInternalServerError)
			return
		}
		message := reference + " created and assigned to " + assignee + "; no email was sent"
		if result, collectErr := s.collectIneligibleScorecardEvidence(r.Context(), caseID, *actor.ID, actor.Label, actor.RequestID); collectErr == nil {
			message += fmt.Sprintf("; Play-Cricket scorecard %d was retained as private evidence", result.MatchID)
		} else {
			slog.Warn("automatic Play-Cricket scorecard collection", "case_id", caseID, "error", collectErr)
			message += "; scorecard evidence was not collected automatically (open the case to review or retry)"
		}
		redirectIneligible(w, r, intakeID, "success", message)
		_ = reportingText
		_ = offendingText
	}
}

func (s *Server) handleAdminIneligibleLinkCase() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intakeID, caseID, teamID, reportingClubID, reason, ok := ineligibleMergeLinkForm(r)
		if !ok {
			http.Error(w, "intake, case, mapped subject team and reason are required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not link intake", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var state, origin, reference, caseStatus, reportingText, playerText string
		var caseClubID int32
		var revisionID int64
		var rawJSON []byte
		if err = tx.QueryRow(r.Context(), `
			SELECT i.state,i.origin,COALESCE(i.reporting_club_text,''),COALESCE(i.player_text,''),r.id,r.raw_data
			FROM sanction_intakes i
			JOIN LATERAL (SELECT id,raw_data FROM sanction_intake_revisions WHERE intake_id=i.id ORDER BY revision DESC LIMIT 1) r ON TRUE
			WHERE i.id=$1 FOR UPDATE OF i
		`, intakeID).Scan(&state, &origin, &reportingText, &playerText, &revisionID, &rawJSON); err != nil {
			http.NotFound(w, r)
			return
		}
		if state == "ignored" || state == "duplicate" {
			http.Error(w, "resolved intake cannot be linked", http.StatusConflict)
			return
		}
		if err = tx.QueryRow(r.Context(), `SELECT reference,status,club_id FROM sanction_cases WHERE id=$1 AND source_type='ineligible_player' FOR UPDATE`, caseID).Scan(&reference, &caseStatus, &caseClubID); err != nil {
			http.Error(w, "case not found", http.StatusBadRequest)
			return
		}
		if !ineligibleCaseAcceptsIntake(caseStatus) {
			http.Error(w, "intakes can only be linked before a club response is requested or a decision is proposed", http.StatusConflict)
			return
		}
		leagueOrigin := reportingText == "GMCL Official"
		if reportingClubID == nil && !leagueOrigin {
			http.Error(w, "the reporting club must be mapped; only an exact GMCL Official report may be league-origin", http.StatusBadRequest)
			return
		}
		if reportingClubID != nil && leagueOrigin {
			http.Error(w, "GMCL Official reports must remain league-origin", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(playerText) == "" {
			http.Error(w, "the intake has no player subject and cannot be linked", http.StatusConflict)
			return
		}
		var mappedClubID int32
		var offendingClubName, offendingTeamName string
		if err = tx.QueryRow(r.Context(), `SELECT c.id,c.name,t.name FROM teams t JOIN clubs c ON c.id=t.club_id WHERE t.id=$1 AND t.active`, teamID).Scan(&mappedClubID, &offendingClubName, &offendingTeamName); err != nil {
			http.Error(w, "active mapped subject team not found", http.StatusBadRequest)
			return
		}
		if mappedClubID != caseClubID {
			http.Error(w, "a case may contain several players or teams only when they belong to the same offending club; split this intake into a separate case", http.StatusConflict)
			return
		}
		var reportingClubName string
		if reportingClubID != nil {
			if err = tx.QueryRow(r.Context(), `SELECT name FROM clubs WHERE id=$1`, *reportingClubID).Scan(&reportingClubName); err != nil {
				http.Error(w, "reporting club not found", http.StatusBadRequest)
				return
			}
		}
		playCricketMatchID := sourceInt64Field(rawJSON, "play-cricket match id", "play cricket match id")
		playCricketPlayerID := sourceInt64Field(rawJSON, "play-cricket player id", "play cricket player id")
		var linkCount int
		_ = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM sanction_intake_effective_case_links WHERE intake_id=$1`, intakeID).Scan(&linkCount)
		relationship := "primary"
		if linkCount > 0 {
			relationship = "supporting"
		}
		var linkID int64
		newLink := false
		err = tx.QueryRow(r.Context(), `SELECT id,relationship FROM sanction_intake_case_links WHERE intake_id=$1 AND case_id=$2 ORDER BY id LIMIT 1`, intakeID, caseID).Scan(&linkID, &relationship)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_intake_case_links(intake_id,case_id,relationship,reason,created_by_admin_id) VALUES($1,$2,$3,$4,$5) RETURNING id`, intakeID, caseID, relationship, reason, *actor.ID).Scan(&linkID)
			newLink = err == nil
		}
		if err != nil {
			http.Error(w, "could not retain the intake case link", http.StatusInternalServerError)
			return
		}
		err = mergeIneligibleIntakeIntoCase(r.Context(), tx, ineligibleIntakeMerge{
			CaseID: caseID, IntakeID: intakeID, RevisionID: revisionID, TeamID: teamID,
			PlayerName: playerText, PlayCricketPlayer: playCricketPlayerID, PlayCricketMatch: playCricketMatchID,
			OffendingClubName: offendingClubName,
			ReportingClubID:   reportingClubID, ReportingClubName: reportingClubName,
			LeagueOrigin: leagueOrigin, CreatedByAdminID: *actor.ID, Primary: relationship == "primary", Relationship: relationship,
		})
		eventType := "case_link_refreshed"
		if newLink {
			eventType = "case_linked"
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,$2,$3,$4,$5,jsonb_build_object('state',$6::text),jsonb_build_object('state','linked','case_id',$7::bigint,'reference',$8::text,'relationship',$9::text,'team_id',$10::bigint,'reporting_club_id',$11::integer,'revision_id',$12::bigint),$13)`, intakeID, eventType, *actor.ID, actor.Label, reason, state, caseID, reference, relationship, teamID, reportingClubID, revisionID, actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id) VALUES($1,$2,'admin',$3,$4,$5,jsonb_build_object('intake_id',$6::bigint,'origin',$7::text,'relationship',$8::text,'team_id',$9::bigint,'reporting_club_id',$10::integer,'revision_id',$11::bigint),$12)`, caseID, "ineligible_intake_"+eventType, *actor.ID, actor.Label, reason, intakeID, origin, relationship, teamID, reportingClubID, revisionID, actor.RequestID)
		}
		if err == nil {
			err = projectIneligibleIntakeMergeState(r.Context(), tx, intakeID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not link intake", http.StatusInternalServerError)
			return
		}
		message := "Intake linked and merged into " + reference + "; no email was sent"
		if result, collectErr := s.collectIneligibleScorecardEvidence(r.Context(), caseID, *actor.ID, actor.Label, actor.RequestID); collectErr == nil {
			message += fmt.Sprintf("; Play-Cricket scorecard %d was retained as private evidence", result.MatchID)
		} else {
			slog.Warn("automatic Play-Cricket scorecard collection after intake link", "case_id", caseID, "error", collectErr)
			message += "; scorecard evidence was not collected automatically (open the case to review or retry)"
		}
		redirectIneligible(w, r, intakeID, "success", message)
	}
}

func ineligibleCaseAcceptsIntake(status string) bool {
	return status == "submitted" || status == "triage" || status == "investigating"
}

func ineligibleMergeLinkForm(r *http.Request) (int64, int64, int64, *int32, string, bool) {
	intakeID, intakeErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if r.ParseForm() != nil {
		return 0, 0, 0, nil, "", false
	}
	caseID, caseErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("case_id")), 10, 64)
	teamID, teamErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("team_id")), 10, 64)
	var reportingClubID *int32
	if raw := strings.TrimSpace(r.FormValue("reporting_club_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed <= 0 {
			return 0, 0, 0, nil, "", false
		}
		value := int32(parsed)
		reportingClubID = &value
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	ok := intakeErr == nil && caseErr == nil && teamErr == nil && intakeID > 0 && caseID > 0 && teamID > 0 && reason != ""
	return intakeID, caseID, teamID, reportingClubID, reason, ok
}

func ineligibleLinkForm(r *http.Request) (int64, int64, string, bool) {
	intakeID, intakeErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if r.ParseForm() != nil {
		return 0, 0, "", false
	}
	caseID, caseErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("case_id")), 10, 64)
	reason := strings.TrimSpace(r.FormValue("reason"))
	return intakeID, caseID, reason, intakeErr == nil && caseErr == nil && intakeID > 0 && caseID > 0 && reason != ""
}

func ineligibleDuplicateForm(r *http.Request) (int64, int64, *int32, string, bool) {
	intakeID, caseID, reason, ok := ineligibleLinkForm(r)
	if !ok {
		return 0, 0, nil, "", false
	}
	var reportingClubID *int32
	if raw := strings.TrimSpace(r.FormValue("reporting_club_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed <= 0 {
			return 0, 0, nil, "", false
		}
		value := int32(parsed)
		reportingClubID = &value
	}
	return intakeID, caseID, reportingClubID, reason, true
}

func (s *Server) handleAdminIneligibleDuplicate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intakeID, caseID, reportingClubID, reason, ok := ineligibleDuplicateForm(r)
		if !ok {
			http.Error(w, "duplicate case and audit reason are required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not mark duplicate", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var state, reference, caseStatus, reportingText string
		var revisionID int64
		var hasActiveCaseLink bool
		if err = tx.QueryRow(r.Context(), `SELECT intake.state,COALESCE(intake.reporting_club_text,''),revision.id,
			EXISTS(SELECT 1 FROM sanction_intake_effective_case_links link WHERE link.intake_id=intake.id AND link.relationship<>'duplicate')
			FROM sanction_intakes intake
			JOIN LATERAL (SELECT id FROM sanction_intake_revisions WHERE intake_id=intake.id ORDER BY revision DESC LIMIT 1) revision ON TRUE
			WHERE intake.id=$1 FOR UPDATE OF intake`, intakeID).Scan(&state, &reportingText, &revisionID, &hasActiveCaseLink); err != nil {
			http.NotFound(w, r)
			return
		}
		if state == "duplicate" {
			redirectIneligible(w, r, intakeID, "success", "Intake was already marked duplicate")
			return
		}
		if state == "ignored" {
			http.Error(w, "ignored intake cannot be marked duplicate", http.StatusConflict)
			return
		}
		if hasActiveCaseLink {
			http.Error(w, "an intake already merged into an active case cannot be reclassified as duplicate", http.StatusConflict)
			return
		}
		var targetTeamID *int32
		if err = tx.QueryRow(r.Context(), `SELECT reference,status,team_id FROM sanction_cases WHERE id=$1 AND source_type='ineligible_player' FOR UPDATE`, caseID).Scan(&reference, &caseStatus, &targetTeamID); err != nil {
			http.Error(w, "duplicate case not found", http.StatusBadRequest)
			return
		}
		if !ineligibleCaseAcceptsIntake(caseStatus) {
			http.Error(w, "a duplicate reporter can only be attached before a club response is requested or a decision is proposed", http.StatusConflict)
			return
		}
		leagueOrigin := reportingText == "GMCL Official"
		if reportingClubID == nil && !leagueOrigin {
			http.Error(w, "the duplicate report's reporting club must be mapped", http.StatusBadRequest)
			return
		}
		if reportingClubID != nil && leagueOrigin {
			http.Error(w, "GMCL Official reports must remain league-origin", http.StatusBadRequest)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_case_links(intake_id,case_id,relationship,reason,created_by_admin_id) VALUES($1,$2,'duplicate',$3,$4) ON CONFLICT DO NOTHING`, intakeID, caseID, reason, *actor.ID)
		var reportingPartyID *int64
		var reportingClubName string
		if err == nil && leagueOrigin {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_parties(case_id,party_type,name,relationship)
				VALUES($1,'league','GMCL Official','league')
				ON CONFLICT(case_id,relationship) WHERE relationship='league' AND party_type='league' DO NOTHING`, caseID)
		}
		if err == nil && reportingClubID != nil {
			if err = tx.QueryRow(r.Context(), `SELECT name FROM clubs WHERE id=$1`, *reportingClubID).Scan(&reportingClubName); err == nil {
				var partyID int64
				_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_parties(case_id,party_type,name,club_id,relationship)
					VALUES($1,'club',$2,$3,'reporting_club')
					ON CONFLICT(case_id,club_id) WHERE relationship='reporting_club' AND club_id IS NOT NULL DO NOTHING`, caseID, reportingClubName, *reportingClubID)
				if err == nil {
					err = tx.QueryRow(r.Context(), `SELECT id FROM sanction_case_parties WHERE case_id=$1 AND club_id=$2 AND relationship='reporting_club'`, caseID, *reportingClubID).Scan(&partyID)
				}
				if err == nil {
					reportingPartyID = &partyID
					_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_reporting_club_intakes(case_id,club_id,intake_id,party_id,created_by_admin_id)
						VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, caseID, *reportingClubID, intakeID, partyID, *actor.ID)
				}
			}
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_intake_merge_resolutions(
				case_id,intake_id,revision_id,relationship,team_id,reporting_club_id,reporting_party_id,league_origin,created_by_admin_id
			) SELECT $1,$2,$3,'duplicate',$4,$5,$6,$7,$8
			WHERE NOT EXISTS(
				SELECT 1 FROM sanction_case_intake_merge_resolutions prior
				WHERE prior.id=(SELECT latest.id FROM sanction_case_intake_merge_resolutions latest
					WHERE latest.case_id=$1 AND latest.intake_id=$2 AND latest.relationship='duplicate' ORDER BY latest.id DESC LIMIT 1)
				  AND prior.revision_id=$3
				  AND prior.reporting_club_id IS NOT DISTINCT FROM $5::integer
				  AND prior.reporting_party_id IS NOT DISTINCT FROM $6::bigint
				  AND prior.league_origin=$7
			)`, caseID, intakeID, revisionID, targetTeamID, reportingClubID, reportingPartyID, leagueOrigin, *actor.ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'marked_duplicate',$2,$3,$4,jsonb_build_object('state',$5::text),jsonb_build_object('state','duplicate','case_id',$6::bigint,'reference',$7::text,'reporting_club_id',$8::integer,'league_origin',$9::boolean),$10)`, intakeID, *actor.ID, actor.Label, reason, state, caseID, reference, reportingClubID, leagueOrigin, actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id) VALUES($1,'duplicate_intake_linked','admin',$2,$3,$4,jsonb_build_object('intake_id',$5::bigint),$6)`, caseID, *actor.ID, actor.Label, reason, intakeID, actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_intakes SET state='duplicate',updated_at=now(),exception_message=NULL WHERE id=$1`, intakeID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not mark duplicate", http.StatusInternalServerError)
			return
		}
		redirectIneligible(w, r, intakeID, "success", "Intake marked duplicate of "+reference)
	}
}

func (s *Server) handleAdminIneligibleIgnore() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intakeID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || intakeID <= 0 || r.ParseForm() != nil {
			http.Error(w, "invalid intake", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" {
			http.Error(w, "audit reason is required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not ignore intake", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var state string
		var hasActiveCaseLink bool
		if err = tx.QueryRow(r.Context(), `SELECT intake.state,
			EXISTS(SELECT 1 FROM sanction_intake_effective_case_links link WHERE link.intake_id=intake.id AND link.relationship<>'duplicate')
			FROM sanction_intakes intake WHERE intake.id=$1 FOR UPDATE OF intake`, intakeID).Scan(&state, &hasActiveCaseLink); err != nil {
			http.NotFound(w, r)
			return
		}
		if state == "ignored" {
			redirectIneligible(w, r, intakeID, "success", "Intake was already ignored")
			return
		}
		if state == "duplicate" || state == "linked" || hasActiveCaseLink {
			http.Error(w, "linked or duplicate intake cannot be ignored", http.StatusConflict)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'ignored',$2,$3,$4,jsonb_build_object('state',$5::text),jsonb_build_object('state','ignored'),$6)`, intakeID, *actor.ID, actor.Label, reason, state, actor.RequestID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_intakes SET state='ignored',updated_at=now(),exception_message=NULL WHERE id=$1`, intakeID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not ignore intake", http.StatusInternalServerError)
			return
		}
		redirectIneligible(w, r, intakeID, "success", "Intake closed with a saved audit reason")
	}
}

func (s *Server) handleAdminCaseInvestigationNote() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID <= 0 || r.ParseForm() != nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		note := strings.TrimSpace(r.FormValue("note"))
		if note == "" {
			http.Error(w, "investigation note is required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		result, err := s.DB.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id) SELECT id,'investigation_note','admin',$2,$3,$4,$5 FROM sanction_cases WHERE id=$1`, caseID, *actor.ID, actor.Label, note, actor.RequestID)
		if err != nil || result.RowsAffected() != 1 {
			http.Error(w, "could not record investigation note", http.StatusInternalServerError)
			return
		}
		redirectCaseActivity(w, r, caseID, "Investigation note recorded")
	}
}

func (s *Server) handleAdminCaseResponseReviewed() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID <= 0 || r.ParseForm() != nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		note := strings.TrimSpace(r.FormValue("note"))
		if len([]rune(note)) > 2000 {
			http.Error(w, "review note is too long", http.StatusBadRequest)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not record response review", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var responseEventID int64
		if err = tx.QueryRow(r.Context(), `SELECT response.id
			FROM sanction_cases cases
			JOIN LATERAL (SELECT event.id FROM sanction_case_events event
				WHERE event.case_id=cases.id AND event.event_type IN ('party_response','external_response_recorded')
				ORDER BY event.id DESC LIMIT 1) response ON TRUE
			WHERE cases.id=$1 FOR UPDATE OF cases`, caseID).Scan(&responseEventID); err != nil {
			http.Error(w, "there is no response to review", http.StatusConflict)
			return
		}
		reason := "Latest club response reviewed"
		if note != "" {
			reason += ": " + note
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id)
			SELECT $1,'response_reviewed','admin',$2,$3,$4,jsonb_build_object('response_event_id',$5::bigint),$6
			WHERE NOT EXISTS(SELECT 1 FROM sanction_case_events prior WHERE prior.case_id=$1 AND prior.event_type='response_reviewed' AND prior.metadata->>'response_event_id'=$5::bigint::text)`,
			caseID, *actor.ID, actor.Label, reason, responseEventID, actor.RequestID)
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not record response review", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s#next-stage", caseID, url.QueryEscape("Latest club response marked reviewed")), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminCaseManualResponse() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID <= 0 {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(256<<10))
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "response or attachment is too large", http.StatusBadRequest)
			return
		}
		response := strings.TrimSpace(r.FormValue("response"))
		respondent := strings.TrimSpace(r.FormValue("respondent"))
		channel := strings.TrimSpace(r.FormValue("channel"))
		if !map[string]bool{"email": true, "phone": true, "meeting": true, "other": true}[channel] || response == "" {
			http.Error(w, "response and valid channel are required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not record response", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var currentStatus string
		if err = tx.QueryRow(r.Context(), `SELECT status FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&currentStatus); err != nil {
			http.NotFound(w, r)
			return
		}
		if !map[string]bool{"submitted": true, "triage": true, "investigating": true, "response_pending": true}[currentStatus] {
			http.Error(w, "an external investigation response cannot be added after a decision has been proposed", http.StatusConflict)
			return
		}
		var latestRequestStatus string
		_ = tx.QueryRow(r.Context(), `SELECT status FROM sanction_response_requests WHERE case_id=$1 ORDER BY requested_at DESC,id DESC LIMIT 1`, caseID).Scan(&latestRequestStatus)
		lateResponse := latestRequestStatus == "expired"
		nextStatus := currentStatus
		if currentStatus == "response_pending" {
			nextStatus = "investigating"
		}
		var eventID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,metadata,request_id) VALUES($1,'external_response_recorded','admin',$2,$3,$4,jsonb_build_object('status',$5::text),jsonb_build_object('status',$6::text),jsonb_build_object('channel',$7::text,'respondent',$8::text,'late_response',$9::boolean),$10) RETURNING id`, caseID, *actor.ID, actor.Label, response, currentStatus, nextStatus, channel, respondent, lateResponse, actor.RequestID).Scan(&eventID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_case_access_tokens SET revoked_at=COALESCE(revoked_at,now()),last_used_at=now()
				WHERE id IN (SELECT access_token_id FROM sanction_response_requests WHERE case_id=$1 AND status IN ('queued','pending','expired'))`, caseID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_response_requests SET status='responded',responded_at=COALESCE(responded_at,now()),closed_at=COALESCE(closed_at,now())
				WHERE id=(SELECT id FROM sanction_response_requests WHERE case_id=$1 AND status IN ('queued','pending','expired') ORDER BY requested_at DESC,id DESC LIMIT 1)`, caseID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_notification_outbox SET processed_at=now() WHERE case_id=$1 AND message_kind IN ('response_request','response_reminder') AND processed_at IS NULL`, caseID)
		}
		if err == nil && nextStatus != currentStatus {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status=$2,updated_at=now() WHERE id=$1`, caseID, nextStatus)
		}
		if err == nil {
			if file, header, fileErr := r.FormFile("evidence"); fileErr == nil {
				defer file.Close()
				err = storeCaseEvidence(r.Context(), tx, caseID, &eventID, "private", file, header, "admin", *actor.ID)
			}
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			slog.Error("record external sanction response", "case_id", caseID, "error", err)
			http.Error(w, "could not record response", http.StatusInternalServerError)
			return
		}
		redirectCaseActivity(w, r, caseID, "External response recorded")
	}
}

func redirectIneligible(w http.ResponseWriter, r *http.Request, intakeID int64, key, message string) {
	values := url.Values{key: []string{message}}
	http.Redirect(w, r, fmt.Sprintf("/admin/ineligible/%d?%s", intakeID, values.Encode()), http.StatusSeeOther)
}

func redirectIneligibleDashboard(w http.ResponseWriter, r *http.Request, key, message string) {
	http.Redirect(w, r, "/admin/ineligible?"+url.Values{key: []string{message}}.Encode(), http.StatusSeeOther)
}

func redirectCaseActivity(w http.ResponseWriter, r *http.Request, caseID int64, message string) {
	returnTo := strings.TrimSpace(r.FormValue("return_to"))
	if !strings.HasPrefix(returnTo, "/admin/ineligible/") {
		returnTo = fmt.Sprintf("/admin/cases/%d", caseID)
	}
	separator := "?"
	if strings.Contains(returnTo, "?") {
		separator = "&"
	}
	http.Redirect(w, r, returnTo+separator+url.Values{"success": []string{message}}.Encode(), http.StatusSeeOther)
}
