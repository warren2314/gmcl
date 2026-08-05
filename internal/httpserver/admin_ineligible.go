package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

const ineligibleDefaultAssigneeEnv = "INELIGIBLE_DEFAULT_ASSIGNEE_USERNAME"

type ineligibleQueueFilters struct {
	State         string
	Origin        string
	ReportingClub string
	OffendingClub string
	Team          string
	Player        string
	Assignee      string
	CaseStatus    string
	Age           string
}

type ineligibleQueueRow struct {
	ID                int64
	Origin            string
	ExternalKey       string
	State             string
	ReportingClubText string
	OffendingClubText string
	TeamText          string
	PlayerText        string
	FixtureDate       *time.Time
	ExternalCreatedAt *time.Time
	CaseID            *int64
	CaseReference     string
	CaseStatus        string
	Assignee          string
}

type ineligibleDashboardCounts struct {
	NewIntakes         int64
	ActiveCases        int64
	ResponsesDue       int64
	ResponsesOverdue   int64
	RecentReplies      int64
	AwaitingDecision   int64
	DenverPointsTasks  int64
	DeliveryExceptions int64
	ClosedCases        int64
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
		Age:           strings.TrimSpace(values.Get("age")),
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
	if !map[string]bool{"": true, "2d": true, "7d": true, "14d": true, "30d": true}[filter.Age] {
		filter.Age = ""
	}
	return filter
}

func buildIneligibleQueueQuery(filter ineligibleQueueFilters) (string, []any) {
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
	if filter.Age != "" {
		days := map[string]int{"2d": 2, "7d": 7, "14d": 14, "30d": 30}[filter.Age]
		where = append(where, fmt.Sprintf("COALESCE(i.external_created_at,i.created_at) < now() - interval '%d days'", days))
	}
	query := `
		SELECT i.id,i.origin,i.external_key,i.state,COALESCE(i.reporting_club_text,''),
		       COALESCE(i.offending_club_text,''),COALESCE(i.team_text,''),COALESCE(i.player_text,''),
		       i.fixture_date,i.external_created_at,c.id,COALESCE(c.reference,''),COALESCE(c.status,''),
		       COALESCE(a.username,'')
		FROM sanction_intakes i
		LEFT JOIN LATERAL (
			SELECT l.case_id
			FROM sanction_intake_case_links l
			WHERE l.intake_id=i.id
			ORDER BY CASE l.relationship WHEN 'primary' THEN 0 WHEN 'split' THEN 1 ELSE 2 END,l.id DESC
			LIMIT 1
		) latest_link ON TRUE
		LEFT JOIN sanction_cases c ON c.id=latest_link.case_id
		LEFT JOIN admin_users a ON a.id=c.assigned_admin_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY CASE i.state WHEN 'exception' THEN 0 WHEN 'new' THEN 1 WHEN 'reviewing' THEN 2 ELSE 3 END,
		         COALESCE(i.external_created_at,i.created_at) DESC,i.id DESC
		LIMIT 500`
	return query, args
}

func (s *Server) handleAdminIneligibleDashboard() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		filter := parseIneligibleQueueFilters(r.URL.Query())
		query, args := buildIneligibleQueueQuery(filter)
		rows, err := s.DB.Query(ctx, query, args...)
		if err != nil {
			slog.Error("load ineligible intake queue", "error", err)
			http.Error(w, "could not load ineligible-player intake", http.StatusInternalServerError)
			return
		}
		queue := []ineligibleQueueRow{}
		for rows.Next() {
			var row ineligibleQueueRow
			if err := rows.Scan(&row.ID, &row.Origin, &row.ExternalKey, &row.State, &row.ReportingClubText, &row.OffendingClubText, &row.TeamText, &row.PlayerText, &row.FixtureDate, &row.ExternalCreatedAt, &row.CaseID, &row.CaseReference, &row.CaseStatus, &row.Assignee); err != nil {
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
		pageHead(w, "Ineligible-player intake")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container-fluid px-3 px-lg-4 py-4"><div class="d-flex flex-column flex-lg-row justify-content-between gap-3 mb-4"><div><h1 class="h2 mb-1">Ineligible-player intake</h1><p class="text-muted mb-0">Private intake, mapping, investigation and decision hand-off. No triage action sends an email or issues a sanction.</p></div><div class="d-flex flex-wrap gap-2 align-self-lg-start"><form method="POST" action="/admin/ineligible/sync"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-primary">Sync now</button></form><a class="btn btn-outline-warning" href="/admin/ineligible/backfill">2026 tracker backfill</a><a class="btn btn-outline-secondary" href="/admin/ineligible">Refresh</a><a class="btn btn-outline-primary" href="/admin/cases">All sanction cases</a></div></div>`, escapeHTML(csrf))
		writeIneligibleFlash(w, r)
		fmt.Fprint(w, `<div class="row row-cols-2 row-cols-md-3 row-cols-xl-5 g-3 mb-4">`)
		for _, card := range []struct {
			Label string
			Count int64
			Class string
			Href  string
		}{
			{"New / exceptions", counts.NewIntakes, "border-danger", "/admin/ineligible?state=open"},
			{"Active investigations", counts.ActiveCases, "border-primary", "/admin/ineligible?state=all&case_status=investigating"},
			{"Responses due", counts.ResponsesDue, "border-warning", "/admin/ineligible?state=all&case_status=response_pending"},
			{"Responses overdue", counts.ResponsesOverdue, "border-danger", "/admin/ineligible?state=all&case_status=investigating"},
			{"New replies", counts.RecentReplies, "border-info", "/admin/ineligible?state=all&case_status=investigating"},
			{"Awaiting decision", counts.AwaitingDecision, "border-primary", "/admin/ineligible?state=all&case_status=decision_proposed"},
			{"Denver points tasks", counts.DenverPointsTasks, "border-warning", "/admin/cases/tasks"},
			{"Delivery exceptions", counts.DeliveryExceptions, "border-danger", "/admin/cases"},
			{"Closed cases", counts.ClosedCases, "border-success", "/admin/ineligible?state=all&case_status=closed"},
		} {
			fmt.Fprintf(w, `<div class="col"><a class="card h-100 %s text-decoration-none text-body" href="%s"><div class="card-body py-3"><div class="display-6 fw-semibold">%d</div><div class="small text-muted">%s</div></div></a></div>`, card.Class, escapeHTML(card.Href), card.Count, escapeHTML(card.Label))
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
			fmt.Fprintf(w, `<section class="alert %s d-flex flex-column flex-lg-row justify-content-between gap-2"><div><strong>Latest %s sync: %s</strong><div class="small">Run %d; seen %d, new %d, changed %d, errors %d. Completed: %s.</div>`, alertClass, escapeHTML(strings.ReplaceAll(syncHealth.Origin, "_", " ")), escapeHTML(syncHealth.Status), syncHealth.ID, syncHealth.RowsSeen, syncHealth.RowsNew, syncHealth.RowsChanged, syncHealth.RowsErrored, escapeHTML(completed))
			if syncHealth.Error != "" {
				fmt.Fprintf(w, `<div class="small mt-1">%s</div>`, escapeHTML(syncHealth.Error))
			}
			fmt.Fprint(w, `</div><div class="small text-nowrap">Started `+escapeHTML(syncHealth.StartedAt.In(s.LondonLoc).Format("02 Jan 15:04"))+`</div></section>`)
		} else {
			fmt.Fprint(w, `<div class="alert alert-warning"><strong>No intake sync has run.</strong> Configure the read-only source and run the protected importer before enabling live intake.</div>`)
		}

		writeIneligibleFilters(w, filter)
		fmt.Fprint(w, `<section class="card shadow-sm"><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0"><thead><tr><th>Received</th><th>Source</th><th>Reporting club</th><th>Offending club / team</th><th>Player / fixture</th><th>State</th><th>Case</th></tr></thead><tbody>`)
		for _, row := range queue {
			received := "-"
			if row.ExternalCreatedAt != nil {
				received = row.ExternalCreatedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")
			}
			fixture := ""
			if row.FixtureDate != nil {
				fixture = row.FixtureDate.Format("02 Jan 2006")
			}
			caseHTML := `<span class="text-muted">Not mapped</span>`
			if row.CaseID != nil {
				caseHTML = fmt.Sprintf(`<a href="/admin/cases/%d"><strong>%s</strong></a><div class="small text-muted">%s`, *row.CaseID, escapeHTML(row.CaseReference), escapeHTML(row.CaseStatus))
				if row.Assignee != "" {
					caseHTML += `; ` + escapeHTML(row.Assignee)
				}
				caseHTML += `</div>`
			}
			fmt.Fprintf(w, `<tr><td data-label="Received"><a href="/admin/ineligible/%d">%s</a><div class="small text-muted">%s</div></td><td data-label="Source">%s</td><td data-label="Reporting club">%s</td><td data-label="Offending club / team"><strong>%s</strong><div class="small text-muted">%s</div></td><td data-label="Player / fixture"><strong>%s</strong><div class="small text-muted">%s</div></td><td data-label="State">%s</td><td data-label="Case">%s</td></tr>`, row.ID, escapeHTML(received), escapeHTML(row.ExternalKey), escapeHTML(strings.ReplaceAll(row.Origin, "_", " ")), escapeHTML(row.ReportingClubText), escapeHTML(row.OffendingClubText), escapeHTML(row.TeamText), escapeHTML(row.PlayerText), escapeHTML(fixture), ineligibleStateBadge(row.State), caseHTML)
		}
		if len(queue) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-5">No intake records match these filters.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func (s *Server) loadIneligibleDashboardCounts(ctx context.Context) (ineligibleDashboardCounts, error) {
	var counts ineligibleDashboardCounts
	err := s.DB.QueryRow(ctx, `
		SELECT
		 (SELECT COUNT(*) FROM sanction_intakes WHERE state IN ('new','reviewing','exception')),
		 (SELECT COUNT(*) FROM sanction_cases WHERE source_type='ineligible_player' AND status IN ('submitted','triage','investigating','response_pending')),
		 (SELECT COUNT(*) FROM sanction_cases c JOIN LATERAL (
			SELECT rr.status,rr.due_at FROM sanction_response_requests rr WHERE rr.case_id=c.id ORDER BY rr.id DESC LIMIT 1
		 ) latest ON TRUE WHERE c.source_type='ineligible_player' AND latest.status='pending' AND latest.due_at>=now()),
		 (SELECT COUNT(*) FROM sanction_cases c JOIN LATERAL (
			SELECT rr.status,rr.due_at FROM sanction_response_requests rr WHERE rr.case_id=c.id ORDER BY rr.id DESC LIMIT 1
		 ) latest ON TRUE WHERE c.source_type='ineligible_player' AND c.status NOT IN ('closed','rejected','withdrawn','published')
			AND ((latest.status='pending' AND latest.due_at<now()) OR latest.status='expired')),
		 (SELECT COUNT(*) FROM sanction_cases c JOIN LATERAL (
			SELECT e.id,e.created_at FROM sanction_case_events e WHERE e.case_id=c.id AND e.event_type IN ('party_response','external_response_recorded') ORDER BY e.id DESC LIMIT 1
		 ) reply ON TRUE WHERE c.source_type='ineligible_player' AND NOT EXISTS (
			SELECT 1 FROM sanction_case_events reviewed WHERE reviewed.case_id=c.id AND reviewed.event_type='response_reviewed'
			  AND reviewed.metadata->>'response_event_id'=reply.id::text)),
		 (SELECT COUNT(*) FROM sanction_cases WHERE source_type='ineligible_player' AND status IN ('decision_proposed','approved')),
		 (SELECT COUNT(*) FROM sanction_follow_up_tasks t JOIN sanction_cases c ON c.id=t.case_id LEFT JOIN admin_users a ON a.id=t.assigned_admin_id WHERE c.source_type='ineligible_player' AND t.task_type='play_cricket_points' AND t.status IN ('open','in_progress') AND LOWER(COALESCE(a.username,''))='denverthornton'),
		 (SELECT COUNT(DISTINCT o.id) FROM sanction_notification_outbox o JOIN sanction_cases c ON c.id=o.case_id WHERE c.source_type='ineligible_player' AND o.revoked_at IS NULL AND EXISTS (
			SELECT 1 FROM sanction_notification_attempts latest WHERE latest.id=(SELECT attempt.id FROM sanction_notification_attempts attempt WHERE attempt.outbox_id=o.id ORDER BY attempt.attempt_number DESC,attempt.id DESC LIMIT 1) AND latest.status IN ('failed','bounced','complained'))),
		 (SELECT COUNT(*) FROM sanction_cases WHERE source_type='ineligible_player' AND status IN ('closed','rejected','withdrawn'))
	`).Scan(&counts.NewIntakes, &counts.ActiveCases, &counts.ResponsesDue, &counts.ResponsesOverdue, &counts.RecentReplies, &counts.AwaitingDecision, &counts.DenverPointsTasks, &counts.DeliveryExceptions, &counts.ClosedCases)
	return counts, err
}

func (s *Server) loadIneligibleSyncHealth(ctx context.Context) (ineligibleSyncHealth, bool) {
	var result ineligibleSyncHealth
	err := s.DB.QueryRow(ctx, `SELECT id,origin,status,rows_seen,rows_new,rows_changed,rows_errored,COALESCE(error_message,''),started_at,completed_at FROM sanction_intake_sync_runs WHERE origin='google_form' ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&result.ID, &result.Origin, &result.Status, &result.RowsSeen, &result.RowsNew, &result.RowsChanged, &result.RowsErrored, &result.Error, &result.StartedAt, &result.CompletedAt)
	return result, err == nil
}

func writeIneligibleFilters(w http.ResponseWriter, filter ineligibleQueueFilters) {
	fmt.Fprint(w, `<form method="GET" action="/admin/ineligible" class="card card-body mb-4"><div class="row g-3"><div class="col-6 col-lg-2"><label class="form-label">Queue state</label><select class="form-select" name="state">`)
	writeSelectedOptions(w, filter.State, []string{"open", "all", "new", "reviewing", "exception", "linked", "duplicate", "ignored"})
	fmt.Fprint(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Source</label><select class="form-select" name="origin"><option value="">All sources</option>`)
	writeSelectedOptions(w, filter.Origin, []string{"google_form", "native_form", "starred_player", "tracker_backfill"})
	fmt.Fprintf(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Reporting club</label><input class="form-control" name="reporting_club" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Offending club</label><input class="form-control" name="offending_club" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Team</label><input class="form-control" name="team" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Player</label><input class="form-control" name="player" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Assignee</label><input class="form-control" name="assignee" value="%s"></div><div class="col-6 col-lg-2"><label class="form-label">Case status</label><select class="form-select" name="case_status"><option value="">All statuses</option>`, escapeHTML(filter.ReportingClub), escapeHTML(filter.OffendingClub), escapeHTML(filter.Team), escapeHTML(filter.Player), escapeHTML(filter.Assignee))
	writeSelectedOptions(w, filter.CaseStatus, []string{"submitted", "triage", "investigating", "response_pending", "decision_proposed", "approved", "published", "appealed", "closed", "rejected", "withdrawn"})
	fmt.Fprint(w, `</select></div><div class="col-6 col-lg-2"><label class="form-label">Older than</label><select class="form-select" name="age"><option value="">Any age</option>`)
	for _, option := range []struct{ Value, Label string }{{"2d", "2 days"}, {"7d", "7 days"}, {"14d", "14 days"}, {"30d", "30 days"}} {
		selected := ""
		if filter.Age == option.Value {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, option.Value, selected, option.Label)
	}
	fmt.Fprint(w, `</select></div><div class="col-12 col-lg-4 d-flex align-items-end gap-2"><button class="btn btn-primary">Apply filters</button><a class="btn btn-outline-secondary" href="/admin/ineligible">Clear</a></div></div></form>`)
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

func ineligibleStateBadge(state string) string {
	class := map[string]string{"new": "text-bg-danger", "reviewing": "text-bg-primary", "linked": "text-bg-success", "duplicate": "text-bg-secondary", "ignored": "text-bg-secondary", "exception": "text-bg-warning"}[state]
	if class == "" {
		class = "text-bg-light"
	}
	return fmt.Sprintf(`<span class="badge %s">%s</span>`, class, escapeHTML(state))
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
		if err := s.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM sanction_intakes WHERE state IN ('new','reviewing','exception')`).Scan(&count); err != nil {
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
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		summary, err := ineligibledomain.SyncFromEnv(ctx, s.DB, ineligibledomain.Trigger{Type: "admin", AdminID: &adminID})
		if err != nil {
			message := "Sync failed. Check sync health before retrying."
			switch {
			case errors.Is(err, ineligibledomain.ErrImportDisabled):
				message = "Import is disabled by INELIGIBLE_IMPORT_ENABLED."
			case errors.Is(err, ineligibledomain.ErrSyncInProgress):
				message = "Another ineligible-player sync is already running."
			}
			slog.Error("manual ineligible intake sync", "admin_id", adminID, "run_id", summary.RunID, "error", err)
			redirectIneligibleDashboard(w, r, "error", message)
			return
		}
		message := fmt.Sprintf("Sync %d completed: %d seen, %d new, %d changed, %d errors.", summary.RunID, summary.Seen, summary.New, summary.Changed, summary.Errors)
		redirectIneligibleDashboard(w, r, "success", message)
	}
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
		var revision int
		var rawJSON []byte
		err = s.DB.QueryRow(r.Context(), `
			SELECT i.origin,i.external_key,i.state,COALESCE(i.reporting_club_text,''),COALESCE(i.offending_club_text,''),
			       COALESCE(i.team_text,''),COALESCE(i.player_text,''),i.fixture_date,i.external_created_at,
			       COALESCE(i.exception_message,''),COALESCE(rev.revision,0),COALESCE(rev.raw_data,'{}'::jsonb)
			FROM sanction_intakes i
			LEFT JOIN LATERAL (SELECT r.revision,r.raw_data FROM sanction_intake_revisions r WHERE r.intake_id=i.id ORDER BY r.revision DESC LIMIT 1) rev ON TRUE
			WHERE i.id=$1`, intakeID).Scan(&origin, &externalKey, &state, &reportingText, &offendingText, &teamText, &playerText, &fixtureDate, &externalCreatedAt, &exception, &revision, &rawJSON)
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
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Ineligible intake "+strconv.FormatInt(intakeID, 10))
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:1180px"><a class="btn btn-sm btn-outline-secondary mb-3" href="/admin/ineligible">Back to intake queue</a>`)
		writeIneligibleFlash(w, r)
		fmt.Fprintf(w, `<div class="d-flex flex-column flex-md-row justify-content-between gap-2 mb-4"><div><h1 class="h2 mb-1">Intake %d</h1><p class="text-muted mb-0">%s / %s / revision %d</p></div><div>%s</div></div>`, intakeID, escapeHTML(strings.ReplaceAll(origin, "_", " ")), escapeHTML(externalKey), revision, ineligibleStateBadge(state))
		if exception != "" {
			fmt.Fprintf(w, `<div class="alert alert-warning"><strong>Import exception:</strong> %s</div>`, escapeHTML(exception))
		}
		fmt.Fprint(w, `<div class="row g-4"><div class="col-lg-7">`)
		fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header fw-semibold">Reported details</div><div class="card-body"><dl class="row mb-0"><dt class="col-sm-4">Reporting club</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Offending club</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Team</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Player</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Fixture date</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Received</dt><dd class="col-sm-8">%s</dd></dl></div></section>`, escapeHTML(reportingText), escapeHTML(offendingText), escapeHTML(teamText), escapeHTML(playerText), escapeHTML(formatOptionalDate(fixtureDate)), escapeHTML(formatOptionalDateTime(externalCreatedAt, s.LondonLoc)))
		pretty := prettyJSON(rawJSON)
		fmt.Fprintf(w, `<details class="card mb-4"><summary class="card-header fw-semibold">Immutable source revision</summary><div class="card-body"><pre class="small mb-0" style="white-space:pre-wrap;overflow-wrap:anywhere">%s</pre></div></details>`, escapeHTML(pretty))
		fmt.Fprint(w, nativeIntakeEvidenceLinks(rawJSON, intakeID, revision))
		s.writeIneligibleIntakeAttachments(w, r.Context(), intakeID)
		fmt.Fprint(w, `<section class="card mb-4"><div class="card-header fw-semibold">Linked cases and investigation activity</div><div class="card-body">`)
		if len(links) == 0 {
			fmt.Fprint(w, `<p class="text-muted mb-0">No sanction case has been linked.</p>`)
		}
		for _, link := range links {
			fmt.Fprintf(w, `<article class="border rounded p-3 mb-3"><div class="d-flex justify-content-between gap-2"><div><a href="/admin/cases/%d"><strong>%s</strong></a><div class="small text-muted">%s; %s; %s / %s`, link.CaseID, escapeHTML(link.Reference), escapeHTML(link.Relationship), escapeHTML(link.Status), escapeHTML(link.Club), escapeHTML(link.Team))
			if link.Player != "" {
				fmt.Fprint(w, `; `+escapeHTML(link.Player))
			}
			fmt.Fprint(w, `</div></div><span class="small text-muted">`+escapeHTML(link.Assignee)+`</span></div>`)
			if link.Reason != "" {
				fmt.Fprint(w, `<p class="small mt-2 mb-0">`+escapeHTML(link.Reason)+`</p>`)
			}
			fmt.Fprintf(w, `<div class="row g-3 mt-1"><div class="col-md-6"><form method="POST" action="/admin/cases/%d/investigation-note"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="return_to" value="/admin/ineligible/%d"><label class="form-label small fw-semibold">Immutable investigation note</label><textarea class="form-control form-control-sm" name="note" rows="3" maxlength="10000" required></textarea><button class="btn btn-sm btn-outline-primary mt-2">Record note</button></form></div><div class="col-md-6"><form method="POST" action="/admin/cases/%d/manual-response" enctype="multipart/form-data"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="return_to" value="/admin/ineligible/%d"><label class="form-label small fw-semibold">External club response</label><select class="form-select form-select-sm mb-2" name="channel" required><option value="email">Email</option><option value="phone">Phone</option><option value="meeting">Meeting</option><option value="other">Other</option></select><input class="form-control form-control-sm mb-2" name="respondent" placeholder="Respondent name / mailbox" maxlength="300"><textarea class="form-control form-control-sm" name="response" rows="3" maxlength="20000" required></textarea><label class="form-label small mt-2 mb-1">Attachment (PDF, JPEG, PNG, WebP, or text; max 10 MB)</label><input class="form-control form-control-sm" type="file" name="evidence" accept="application/pdf,image/jpeg,image/png,image/webp,text/plain"><button class="btn btn-sm btn-outline-success mt-2">Record response</button></form></div></div></article>`, link.CaseID, csrf, intakeID, link.CaseID, csrf, intakeID)
		}
		fmt.Fprint(w, `</div></section>`)
		writeIneligibleIntakeEvents(w, s, r.Context(), intakeID)
		fmt.Fprint(w, `</div><aside class="col-lg-5">`)
		if state != "duplicate" && state != "ignored" {
			writeIneligibleCreateCaseForm(w, csrf, intakeID, reportingText, playerText, fixtureDate, rawJSON, clubs, teams, len(links) > 0)
			writeIneligibleLinkCaseForm(w, csrf, intakeID, reportingText, clubs, teams, cases)
		}
		hasActiveCaseLink := false
		for _, link := range links {
			if link.Relationship != "duplicate" {
				hasActiveCaseLink = true
				break
			}
		}
		writeIneligibleResolutionForms(w, csrf, intakeID, state, reportingText, clubs, cases, hasActiveCaseLink)
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
		fmt.Fprintf(&content, `<li class="list-group-item"><a href="/admin/ineligible/%d/attachments/%d">%s</a><div class="small text-muted">Source revision %d · %s · %d bytes · SHA-256 %s · retained %s</div></li>`, intakeID, attachmentID, escapeHTML(name), revision, escapeHTML(media), size, escapeHTML(digest[:minInt(12, len(digest))]), escapeHTML(observed.In(s.LondonLoc).Format("02 Jan 2006 15:04")))
	}
	if count > 0 {
		fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header fw-semibold">Immutable Google Drive evidence</div><ul class="list-group list-group-flush">%s</ul></section>`, content.String())
	}
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
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, strings.ReplaceAll(filepath.Base(name), `"`, "")))
		w.Header().Set("Cache-Control", "no-store")
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

func writeIneligibleCreateCaseForm(w http.ResponseWriter, csrf string, intakeID int64, reportingText, playerText string, fixtureDate *time.Time, rawJSON []byte, clubs []ineligibleClubOption, teams []ineligibleTeamOption, split bool) {
	title := "Create investigation case"
	button := "Create case without sending email"
	if split {
		title = "Create split investigation case"
		button = "Create another split case"
	}
	defaultDate := ""
	if fixtureDate != nil {
		defaultDate = fixtureDate.Format("2006-01-02")
	}
	publicSummary := ineligibleDefaultPublicSummary(playerText, fixtureDate)
	privateSummary := sourceStringField(rawJSON, "Reason you believe the player is ineligible", "reason why the player is ineligible", "reason for report", "details", "reason", "allegation")
	fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header fw-semibold">%s</div><form method="POST" action="/admin/ineligible/%d/create-case"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><label class="form-label">Offending team</label><select class="form-select mb-3" name="team_id" required><option value="">Choose mapped club and team...</option>`, escapeHTML(title), intakeID, csrf)
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
	fmt.Fprintf(w, `</select><label class="form-label">Reporting club</label><select class="form-select mb-1" name="reporting_club_id"%s><option value="">%s</option>`, required, blankLabel)
	for _, club := range clubs {
		selected := ""
		if strings.EqualFold(strings.TrimSpace(reportingText), strings.TrimSpace(club.Name)) {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, selected, escapeHTML(club.Name))
	}
	fmt.Fprintf(w, `</select><div class="form-text mb-3">Reported as: %s. Leave league-origin selected for GMCL Official reports.</div><label class="form-label">Fixture date</label><input class="form-control mb-3" type="date" name="match_date" value="%s" required><label class="form-label">Player</label><input class="form-control mb-3" name="player_name" value="%s" maxlength="200" required><label class="form-label">Recorded findings / allegation</label><textarea class="form-control mb-3" name="public_summary" rows="3" maxlength="5000" required>%s</textarea><label class="form-label">Private investigation context</label><textarea class="form-control" name="private_summary" rows="3" maxlength="10000">%s</textarea></div><div class="card-footer"><button class="btn btn-danger">%s</button><div class="form-text mt-2">Assigned to the configured ineligible-player investigator. No notice is queued.</div></div></form></section>`, escapeHTML(reportingText), escapeHTML(defaultDate), escapeHTML(playerText), escapeHTML(publicSummary), escapeHTML(privateSummary), escapeHTML(button))
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
	fmt.Fprintf(w, `</select><div class="form-text mb-3">Reported as: %s. Only an exact GMCL Official intake may remain league-origin.</div><label class="form-label">Reason for link</label><textarea class="form-control" name="reason" rows="2" maxlength="2000" required></textarea></div><div class="card-footer"><button class="btn btn-outline-primary">Link and merge intake</button><div class="form-text mt-2">Adds subjects, reporting-club provenance and private retained evidence. No notice is queued.</div></div></form></section>`, escapeHTML(reportingText))
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
	fmt.Fprint(w, `<section class="card mb-4"><div class="card-header fw-semibold">Immutable triage history</div><ul class="list-group list-group-flush">`)
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
	fmt.Fprint(w, `</ul></section>`)
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
		defer tx.Rollback(r.Context())
		var state, origin, externalKey, reportingText, offendingText string
		var revisionID int64
		var rawJSON []byte
		err = tx.QueryRow(r.Context(), `SELECT i.state,i.origin,i.external_key,COALESCE(i.reporting_club_text,''),COALESCE(i.offending_club_text,''),rev.id,rev.raw_data FROM sanction_intakes i JOIN LATERAL (SELECT id,raw_data FROM sanction_intake_revisions WHERE intake_id=i.id ORDER BY revision DESC LIMIT 1) rev ON TRUE WHERE i.id=$1 FOR UPDATE OF i`, intakeID).Scan(&state, &origin, &externalKey, &reportingText, &offendingText, &revisionID, &rawJSON)
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
		reporterName := sourceStringField(rawJSON, "Your Name & Role at Club/League", "reporter name", "your name", "name")
		reporterEmail := sourceStringField(rawJSON, "reporter email", "email address", "email")
		// The current form deliberately combines the reporter's name and role in
		// one field. Keep that value intact in reporter_name instead of guessing
		// where a person's name ends and a club role begins.
		reporterRole := sourceStringField(rawJSON, "reporter role", "role within club", "your role at the club", "role")
		reporterPhone := sourceStringField(rawJSON, "Your Preferred tel no", "reporter phone", "contact number", "phone number", "telephone", "phone")
		playCricketMatchID := sourceInt64Field(rawJSON, "play-cricket match id", "play cricket match id")
		playCricketPlayerID := sourceInt64Field(rawJSON, "play-cricket player id", "play cricket player id")
		var existingLinks int
		_ = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM sanction_intake_case_links WHERE intake_id=$1`, intakeID).Scan(&existingLinks)
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
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,metadata,request_id) VALUES($1,'ineligible_intake_case_created','admin',$2,$3,$4,jsonb_build_object('reference',$5,'team_id',$6::bigint,'reporting_club_id',$7::integer,'assigned_admin_id',$8::integer),jsonb_build_object('intake_id',$9::bigint,'origin',$10,'external_key',$11),$12)`, caseID, *actor.ID, actor.Label, "Investigation created from private intake and assigned to "+assignee, reference, teamID, reportingClubID, assigneeID, intakeID, origin, externalKey, actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'case_created',$2,$3,$4,jsonb_build_object('state',$5),jsonb_build_object('state','linked','case_id',$6::bigint,'reference',$7,'relationship',$8,'offending_club_id',$9::integer,'reporting_club_id',$10::integer,'team_id',$11::bigint),$12)`, intakeID, *actor.ID, actor.Label, "Created "+reference+" without sending correspondence", state, caseID, reference, relationship, clubID, reportingClubID, teamID, actor.RequestID)
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
		redirectIneligible(w, r, intakeID, "success", reference+" created and assigned to "+assignee+"; no email was sent")
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
		_ = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM sanction_intake_case_links WHERE intake_id=$1`, intakeID).Scan(&linkCount)
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
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,$2,$3,$4,$5,jsonb_build_object('state',$6),jsonb_build_object('state','linked','case_id',$7::bigint,'reference',$8,'relationship',$9,'team_id',$10::bigint,'reporting_club_id',$11::integer,'revision_id',$12::bigint),$13)`, intakeID, eventType, *actor.ID, actor.Label, reason, state, caseID, reference, relationship, teamID, reportingClubID, revisionID, actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id) VALUES($1,$2,'admin',$3,$4,$5,jsonb_build_object('intake_id',$6::bigint,'origin',$7,'relationship',$8,'team_id',$9::bigint,'reporting_club_id',$10::integer,'revision_id',$11::bigint),$12)`, caseID, "ineligible_intake_"+eventType, *actor.ID, actor.Label, reason, intakeID, origin, relationship, teamID, reportingClubID, revisionID, actor.RequestID)
		}
		if err == nil {
			err = projectIneligibleIntakeMergeState(r.Context(), tx, intakeID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not link intake", http.StatusInternalServerError)
			return
		}
		redirectIneligible(w, r, intakeID, "success", "Intake linked and merged into "+reference+"; no email was sent")
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
			EXISTS(SELECT 1 FROM sanction_intake_case_links link WHERE link.intake_id=intake.id AND link.relationship<>'duplicate')
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
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'marked_duplicate',$2,$3,$4,jsonb_build_object('state',$5),jsonb_build_object('state','duplicate','case_id',$6::bigint,'reference',$7,'reporting_club_id',$8::integer,'league_origin',$9::boolean),$10)`, intakeID, *actor.ID, actor.Label, reason, state, caseID, reference, reportingClubID, leagueOrigin, actor.RequestID)
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
			EXISTS(SELECT 1 FROM sanction_intake_case_links link WHERE link.intake_id=intake.id AND link.relationship<>'duplicate')
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
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'ignored',$2,$3,$4,jsonb_build_object('state',$5),jsonb_build_object('state','ignored'),$6)`, intakeID, *actor.ID, actor.Label, reason, state, actor.RequestID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_intakes SET state='ignored',updated_at=now(),exception_message=NULL WHERE id=$1`, intakeID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not ignore intake", http.StatusInternalServerError)
			return
		}
		redirectIneligible(w, r, intakeID, "success", "Intake ignored with an immutable audit reason")
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
		redirectCaseActivity(w, r, caseID, "Latest club response marked reviewed")
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
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,metadata,request_id) VALUES($1,'external_response_recorded','admin',$2,$3,$4,jsonb_build_object('status',$5),jsonb_build_object('status',$6),jsonb_build_object('channel',$7,'respondent',$8,'late_response',$9::boolean),$10) RETURNING id`, caseID, *actor.ID, actor.Label, response, currentStatus, nextStatus, channel, respondent, lateResponse, actor.RequestID).Scan(&eventID)
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
