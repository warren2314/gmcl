package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cricket-ground-feedback/internal/middleware"
	"github.com/jackc/pgx/v5"
)

// closeBatchMaxCases bounds one batch so a single transaction stays short and
// an accidental "select everything" cannot close a whole season in one click.
const closeBatchMaxCases = 200

// closeBatchListLimit bounds how many candidate cases the page renders.
const closeBatchListLimit = 300

var closeBatchClosableStatuses = []string{"submitted", "triage", "investigating", "response_pending", "decision_proposed"}

func closeBatchStatusClosable(status string) bool {
	for _, candidate := range closeBatchClosableStatuses {
		if candidate == status {
			return true
		}
	}
	return false
}

type closeBatchFilters struct {
	OpenedBefore string
	Source       string
}

type closeBatchCandidate struct {
	ID          int64
	Reference   string
	Source      string
	Status      string
	Player      string
	Club        string
	Team        string
	OpenedAt    time.Time
	UpdatedAt   time.Time
	Unassigned  bool
	HasDecision bool
	EmailSent   bool
}

func parseCloseBatchFilters(values url.Values) closeBatchFilters {
	filters := closeBatchFilters{
		OpenedBefore: strings.TrimSpace(values.Get("opened_before")),
		Source:       strings.TrimSpace(values.Get("source")),
	}
	if _, err := time.Parse("2006-01-02", filters.OpenedBefore); err != nil {
		filters.OpenedBefore = ""
	}
	if filters.Source != "ineligible_player" && filters.Source != "other" {
		filters.Source = ""
	}
	return filters
}

func closeBatchWhereSQL(filters closeBatchFilters, adminID int32) (string, []any) {
	args := []any{adminID}
	where := []string{
		"NOT cases.is_test",
		"NOT EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=cases.id AND training.event_type='case_training_designated')",
		"cases.status IN ('" + strings.Join(closeBatchClosableStatuses, "','") + "')",
	}
	if filters.OpenedBefore != "" {
		args = append(args, filters.OpenedBefore)
		// Cast through text so the parameter is described as text and a Go
		// string always encodes cleanly; parseCloseBatchFilters has already
		// checked the value is a real YYYY-MM-DD date.
		where = append(where, fmt.Sprintf("cases.created_at < $%d::text::date", len(args)))
	}
	switch filters.Source {
	case "ineligible_player":
		where = append(where, "cases.source_type='ineligible_player'")
	case "other":
		where = append(where, "cases.source_type<>'ineligible_player'")
	}
	return strings.Join(where, " AND "), args
}

func (s *Server) loadCloseBatchCandidates(ctx context.Context, filters closeBatchFilters, adminID int32) ([]closeBatchCandidate, int64, error) {
	whereSQL, args := closeBatchWhereSQL(filters, adminID)
	rows, err := s.DB.Query(ctx, `SELECT cases.id,cases.reference,cases.source_type,cases.status,
		COALESCE(cases.player_name,''),COALESCE(club.name,''),COALESCE(team.name,''),
		cases.created_at,cases.updated_at,cases.assigned_admin_id IS NULL,
		EXISTS(SELECT 1 FROM sanction_decision_revisions decision WHERE decision.case_id=cases.id),
		EXISTS(SELECT 1 FROM sanction_notification_outbox message WHERE message.case_id=cases.id AND message.processed_at IS NOT NULL AND message.revoked_at IS NULL)
		FROM sanction_cases cases
		LEFT JOIN clubs club ON club.id=cases.club_id
		LEFT JOIN teams team ON team.id=cases.team_id
		WHERE `+whereSQL+` AND (cases.assigned_admin_id=$1 OR cases.assigned_admin_id IS NULL)
		ORDER BY cases.created_at,cases.id LIMIT `+strconv.Itoa(closeBatchListLimit), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	candidates := []closeBatchCandidate{}
	for rows.Next() {
		var item closeBatchCandidate
		if err = rows.Scan(&item.ID, &item.Reference, &item.Source, &item.Status, &item.Player, &item.Club, &item.Team,
			&item.OpenedAt, &item.UpdatedAt, &item.Unassigned, &item.HasDecision, &item.EmailSent); err != nil {
			return nil, 0, err
		}
		candidates = append(candidates, item)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	var ownedByOthers int64
	if err = s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_cases cases WHERE `+whereSQL+
		` AND cases.assigned_admin_id IS NOT NULL AND cases.assigned_admin_id<>$1`, args...).Scan(&ownedByOthers); err != nil {
		return nil, 0, err
	}
	return candidates, ownedByOthers, nil
}

func (s *Server) handleAdminCaseCloseBatch() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		filters := parseCloseBatchFilters(r.URL.Query())
		candidates, ownedByOthers, err := s.loadCloseBatchCandidates(ctx, filters, *actor.ID)
		if err != nil {
			slog.Error("load bulk close candidates", "admin_id", *actor.ID, "error", err)
			http.Error(w, "could not load cases to close", http.StatusInternalServerError)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Close historic cases")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 py-4"><div class="d-flex flex-column flex-lg-row justify-content-between gap-3 mb-4"><div><h1 class="h2 mb-1">Close historic cases</h1><p class="text-muted mb-0">Tick the cases that need no further action, give one shared reason, and close them together. Nothing is deleted and no email is sent.</p></div><div class="d-flex flex-wrap gap-2 align-self-lg-start"><a class="btn btn-outline-secondary" href="/admin/ineligible">Back to ineligible-player work</a><a class="btn btn-outline-secondary" href="/admin/cases/mine/ineligible">My cases</a></div></div>`)
		writeIneligibleFlash(w, r)
		writeCloseBatchFilters(w, filters)
		fmt.Fprintf(w, `<div class="alert alert-warning"><strong>This is the same action as "Close with no action" on a single case.</strong> Each case goes to <strong>Closed</strong>, any pending response link, reminder, unsent email and open follow-up task is cancelled, and your reason is written to every case history. Use it for historic work that was never finished; anything needing a sanction must go through the normal decision, approval and sign-off steps. Up to %d cases can be closed at once.</div>`, closeBatchMaxCases)
		if ownedByOthers > 0 {
			fmt.Fprintf(w, `<div class="alert alert-secondary">%d matching case(s) are assigned to another administrator and are not listed. Ask that owner to close them, or open the case and take it over first.</div>`, ownedByOthers)
		}
		if len(candidates) == 0 {
			fmt.Fprint(w, `<div class="alert alert-success mb-0">No open cases match these filters, so there is nothing to close.</div></main>`)
			pageFooter(w)
			return
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/close-batch" id="close-batch-form"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="opened_before" value="%s"><input type="hidden" name="source" value="%s"><section class="card shadow-sm mb-3">`, escapeHTML(csrf), escapeHTML(filters.OpenedBefore), escapeHTML(filters.Source))
		writeCloseBatchControls(w, len(candidates))
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0" id="close-batch-table"><thead><tr><th scope="col">Close</th><th scope="col">Case</th><th scope="col">Subject</th><th scope="col">Status</th><th scope="col">Opened</th></tr></thead><tbody>`)
		for _, item := range candidates {
			subject, subtitle := adminCaseListSubject(item.Player, item.Club, item.Team)
			notes := ""
			if item.Unassigned {
				notes += ` <span class="badge text-bg-light border">Unassigned - will be recorded as yours</span>`
			}
			if item.EmailSent {
				notes += ` <span class="badge text-bg-warning">A club has already been emailed</span>`
			}
			if item.HasDecision {
				notes += ` <span class="badge text-bg-warning">A decision was drafted</span>`
			}
			fmt.Fprintf(w, `<tr data-close-row data-opened="%s"><td data-label="Close"><input class="form-check-input" type="checkbox" name="case_id" value="%d" aria-label="Close case %s"></td><td data-label="Case"><a href="/admin/cases/%d" target="_blank" rel="noopener"><strong>%s</strong></a><div class="small text-muted">%s</div>%s</td><td data-label="Subject">%s<div class="small text-muted">%s</div></td><td data-label="Status">%s</td><td data-label="Opened">%s<div class="small text-muted">updated %s</div></td></tr>`,
				item.OpenedAt.Format("2006-01-02"), item.ID, escapeHTML(item.Reference), item.ID, escapeHTML(item.Reference),
				escapeHTML(strings.ReplaceAll(item.Source, "_", " ")), notes,
				escapeHTML(defaultString(subject, "Subject not recorded")), escapeHTML(subtitle),
				escapeHTML(caseStatusLabel(item.Status)),
				escapeHTML(item.OpenedAt.In(s.LondonLoc).Format("02 Jan 2006")),
				escapeHTML(item.UpdatedAt.In(s.LondonLoc).Format("02 Jan 2006")))
		}
		fmt.Fprintf(w, `</tbody></table></div>`)
		if len(candidates) == closeBatchListLimit {
			fmt.Fprintf(w, `<div class="card-footer small text-muted">Showing the %d oldest matching cases. Close these, or narrow the dates, to see the rest.</div>`, closeBatchListLimit)
		}
		fmt.Fprint(w, `</section><section class="card border-success"><div class="card-body"><label class="form-label fw-semibold" for="close-batch-reason">Reason recorded on every selected case</label><textarea class="form-control" id="close-batch-reason" name="reason" rows="3" required minlength="5" maxlength="2000" placeholder="For example: Historic 2024 report; no further action agreed at the January committee"></textarea><div class="form-text">Saved with your name and the exact list of cases closed.</div><div class="form-check mt-3"><input class="form-check-input" type="checkbox" name="confirm" value="yes" id="close-batch-confirm" required><label class="form-check-label" for="close-batch-confirm">I confirm that none of the selected cases needs a sanction, approval request or outcome letter.</label></div></div><div class="card-footer d-flex flex-column flex-sm-row justify-content-between gap-2"><span class="small text-muted align-self-sm-center">No email is sent and nothing is published.</span><button class="btn btn-success">Close selected cases</button></div></section></form>`)
		writeCloseBatchScript(w)
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func writeCloseBatchFilters(w io.Writer, filters closeBatchFilters) {
	sourceOptions := []struct{ Value, Label string }{
		{"", "Every case source"},
		{"ineligible_player", "Ineligible-player cases"},
		{"other", "All other sources"},
	}
	fmt.Fprint(w, `<form method="GET" action="/admin/cases/close-batch" class="border rounded bg-body-tertiary p-3 mb-3" aria-label="Choose which historic cases to list"><div class="row g-2 align-items-end"><div class="col-12 col-lg"><strong>Which cases?</strong><div class="small text-muted">Only open cases assigned to you, or to nobody, can be closed here.</div></div><div class="col-6 col-md-4 col-lg-3"><label class="form-label" for="close-batch-opened-before">Opened before</label>`)
	fmt.Fprintf(w, `<input class="form-control" id="close-batch-opened-before" type="date" name="opened_before" value="%s"></div><div class="col-6 col-md-4 col-lg-3"><label class="form-label" for="close-batch-source">Case source</label><select class="form-select" id="close-batch-source" name="source">`, escapeHTML(filters.OpenedBefore))
	for _, option := range sourceOptions {
		selected := ""
		if filters.Source == option.Value {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(option.Value), selected, escapeHTML(option.Label))
	}
	fmt.Fprint(w, `</select></div><div class="col-auto"><button class="btn btn-primary">Show cases</button></div>`)
	if filters.OpenedBefore != "" || filters.Source != "" {
		fmt.Fprint(w, `<div class="col-auto"><a class="btn btn-outline-secondary" href="/admin/cases/close-batch">Clear</a></div>`)
	}
	fmt.Fprint(w, `</div></form>`)
}

func writeCloseBatchControls(w io.Writer, total int) {
	fmt.Fprintf(w, `<div class="card-header"><div class="row g-2 align-items-center"><div class="col-lg"><strong>Open cases you can close</strong><div class="small text-muted" aria-live="polite"><span id="close-batch-shown">%d</span> shown &middot; <span id="close-batch-selected">0</span> selected</div></div><div class="col-lg-4"><label class="visually-hidden" for="close-batch-search">Search cases</label><input class="form-control" id="close-batch-search" type="search" placeholder="Search reference, player or club"></div><div class="col-auto"><button class="btn btn-sm btn-outline-primary" type="button" id="close-batch-select-shown">Select all shown</button></div><div class="col-auto"><button class="btn btn-sm btn-outline-secondary" type="button" id="close-batch-clear">Clear selection</button></div></div><div class="alert alert-secondary py-2 mt-3 mb-0" id="close-batch-no-results" hidden>No cases match this search. Clear it to see the full list.</div></div>`, total)
}

func writeCloseBatchScript(w io.Writer) {
	fmt.Fprintf(w, `<script>(function(){"use strict";var search=document.getElementById("close-batch-search");var rows=Array.prototype.slice.call(document.querySelectorAll("[data-close-row]"));var shown=document.getElementById("close-batch-shown");var selected=document.getElementById("close-batch-selected");var noResults=document.getElementById("close-batch-no-results");var form=document.getElementById("close-batch-form");var maximum=%d;function boxes(){return rows.map(function(row){return row.querySelector('input[type="checkbox"]');}).filter(Boolean);}function chosen(){return boxes().filter(function(box){return box.checked;});}function updateCount(){selected.textContent=chosen().length;}function applySearch(){var query=search.value.toLowerCase().trim();var visible=0;rows.forEach(function(row){var match=query===""||row.textContent.toLowerCase().indexOf(query)!==-1;row.hidden=!match;if(match){visible+=1;}});shown.textContent=visible;noResults.hidden=visible!==0;}search.addEventListener("input",applySearch);document.getElementById("close-batch-select-shown").addEventListener("click",function(){rows.forEach(function(row){var box=row.querySelector('input[type="checkbox"]');if(!row.hidden&&box){box.checked=true;}});updateCount();});document.getElementById("close-batch-clear").addEventListener("click",function(){boxes().forEach(function(box){box.checked=false;});updateCount();});boxes().forEach(function(box){box.addEventListener("change",updateCount);});form.addEventListener("submit",function(event){var count=chosen().length;if(count===0){event.preventDefault();window.alert("Tick at least one case to close.");return;}if(count>maximum){event.preventDefault();window.alert("Close at most "+maximum+" cases at a time. "+count+" are selected.");return;}if(!window.confirm("Close "+count+" case(s) with no action? This cannot be undone from this page.")){event.preventDefault();}});applySearch();updateCount();}());</script>`, closeBatchMaxCases)
}

func parseCloseBatchCaseIDs(values []string) []int64 {
	seen := map[int64]bool{}
	ids := []int64{}
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	// Locking in a stable order keeps concurrent batches from deadlocking.
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return ids
}

func (s *Server) handleAdminCaseCloseBatchApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid close request", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if r.FormValue("confirm") != "yes" || utf8.RuneCountInString(reason) < 5 || utf8.RuneCountInString(reason) > 2000 {
			http.Error(w, "confirm no action and provide a reason of 5 to 2,000 characters", http.StatusBadRequest)
			return
		}
		ids := parseCloseBatchCaseIDs(r.Form["case_id"])
		if len(ids) == 0 {
			redirectCloseBatch(w, r, "error", "Tick at least one case to close.")
			return
		}
		if len(ids) > closeBatchMaxCases {
			redirectCloseBatch(w, r, "error", fmt.Sprintf("Close at most %d cases at a time; %d were selected.", closeBatchMaxCases, len(ids)))
			return
		}

		// One batch must finish well inside the server write timeout, so a slow
		// database rolls the whole batch back instead of losing the response.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			s.failAdminCaseCloseBatch(w, r, actor.ID, "begin_transaction", err)
			return
		}
		defer tx.Rollback(ctx)
		var locked bool
		if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, sanctionsOutboxAdvisoryLock).Scan(&locked); err != nil {
			s.failAdminCaseCloseBatch(w, r, actor.ID, "lock_email_delivery", err)
			return
		}
		if !locked {
			redirectCloseBatch(w, r, "error", "Email delivery is busy. Wait a moment and try again; no case was changed.")
			return
		}

		closed, claimed := 0, 0
		skipped := []string{}
		for _, caseID := range ids {
			var reference, status string
			var assignedAdminID *int32
			err = tx.QueryRow(ctx, `SELECT cases.reference,cases.status,cases.assigned_admin_id
				FROM sanction_cases cases
				WHERE cases.id=$1 AND NOT cases.is_test
				  AND NOT EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=cases.id AND training.event_type='case_training_designated')
				FOR UPDATE`, caseID).Scan(&reference, &status, &assignedAdminID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					skipped = append(skipped, fmt.Sprintf("case %d (no longer available)", caseID))
					continue
				}
				s.failAdminCaseCloseBatch(w, r, actor.ID, "load_case", err)
				return
			}
			if !closeBatchStatusClosable(status) {
				skipped = append(skipped, reference+" ("+caseStatusLabel(status)+")")
				continue
			}
			if assignedAdminID != nil && !sameAdminAssignment(assignedAdminID, actor.ID) {
				skipped = append(skipped, reference+" (assigned to another administrator)")
				continue
			}
			if assignedAdminID == nil {
				if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,request_id)
					VALUES($1,'investigator_assigned','admin',$2,$3,'Taken over to close the case with no action',jsonb_build_object('assigned_admin_id',NULL::integer),jsonb_build_object('assigned_admin_id',$2::bigint),$4)`, caseID, *actor.ID, actor.Label, actor.RequestID); err != nil {
					s.failAdminCaseCloseBatch(w, r, actor.ID, "record_case_history", err)
					return
				}
				if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET assigned_admin_id=$2,updated_at=now() WHERE id=$1`, caseID, *actor.ID); err != nil {
					s.failAdminCaseCloseBatch(w, r, actor.ID, "update_case", err)
					return
				}
				claimed++
			}
			if stage, stepErr := closeSanctionCaseNoActionSteps(ctx, tx, caseID, status, *actor.ID, actor.Label, actor.RequestID, reason, len(ids)); stepErr != nil {
				s.failAdminCaseCloseBatch(w, r, actor.ID, stage, stepErr)
				return
			}
			closed++
		}
		if closed == 0 {
			redirectCloseBatch(w, r, "error", "No case was closed: "+closeBatchSkippedSummary(skipped))
			return
		}
		if err = tx.Commit(ctx); err != nil {
			s.failAdminCaseCloseBatch(w, r, actor.ID, "commit_transaction", err)
			return
		}
		auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		defer cancelAudit()
		s.audit(auditCtx, r, "admin", actor.ID, "sanction_cases_closed_no_action_batch", "sanction_case", nil, map[string]any{
			"requested": len(ids),
			"closed":    closed,
			"claimed":   claimed,
			"skipped":   len(skipped),
			"reason":    reason,
		})
		message := fmt.Sprintf("%d case(s) closed with no action", closed)
		if claimed > 0 {
			message += fmt.Sprintf("; %d unassigned case(s) recorded as yours first", claimed)
		}
		if len(skipped) > 0 {
			message += ". " + closeBatchSkippedSummary(skipped)
		}
		redirectCloseBatch(w, r, "success", message)
	}
}

func closeBatchSkippedSummary(skipped []string) string {
	if len(skipped) == 0 {
		return "Nothing was skipped."
	}
	shown := skipped
	suffix := ""
	if len(shown) > 5 {
		shown = shown[:5]
		suffix = fmt.Sprintf(" and %d more", len(skipped)-5)
	}
	return fmt.Sprintf("%d case(s) were skipped because they changed or are not yours: %s%s.", len(skipped), strings.Join(shown, "; "), suffix)
}

func (s *Server) failAdminCaseCloseBatch(w http.ResponseWriter, r *http.Request, actorID *int32, stage string, err error) {
	rid := requestID(r)
	slog.Error("close sanction cases in batch", "admin_id", actorID, "stage", stage, "request_id", rid, "error", err)
	auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancelAudit()
	s.audit(auditCtx, r, "admin", actorID, "sanction_cases_close_no_action_batch_failed", "sanction_case", nil, map[string]any{
		"stage":      stage,
		"request_id": rid,
		"error":      err.Error(),
	})
	redirectCloseBatch(w, r, "error", "No case was closed. "+closeCaseNoActionErrorMessage(stage, rid))
}

func redirectCloseBatch(w http.ResponseWriter, r *http.Request, key, message string) {
	values := url.Values{key: []string{message}}
	if before := strings.TrimSpace(r.FormValue("opened_before")); before != "" {
		values.Set("opened_before", before)
	}
	if source := strings.TrimSpace(r.FormValue("source")); source != "" {
		values.Set("source", source)
	}
	http.Redirect(w, r, "/admin/cases/close-batch?"+values.Encode(), http.StatusSeeOther)
}
