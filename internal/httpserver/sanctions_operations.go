package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Server) handleInternalSanctionOutbox() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		// Response deadlines are case maintenance, not email delivery. They must
		// continue to expire tokens and reopen investigations while either email
		// kill switch is active.
		if _, err := s.DB.Exec(ctx, `WITH expired AS (
			UPDATE sanction_response_requests SET status='expired',closed_at=now()
			WHERE status='pending' AND due_at<=now() RETURNING case_id,access_token_id
		), revoked_tokens AS (
			UPDATE sanction_case_access_tokens token SET revoked_at=COALESCE(token.revoked_at,now())
			FROM expired WHERE token.id=expired.access_token_id RETURNING token.id
		), resumed_cases AS (
			UPDATE sanction_cases cases SET status='investigating',updated_at=now()
			FROM (SELECT DISTINCT case_id FROM expired) due
			WHERE cases.id=due.case_id AND cases.status='response_pending' RETURNING cases.id
		), cancelled_notices AS (
			UPDATE sanction_notification_outbox outbox SET processed_at=now()
			FROM expired WHERE outbox.case_id=expired.case_id AND outbox.message_kind IN ('response_request','response_reminder') AND outbox.processed_at IS NULL
			RETURNING outbox.id
		) INSERT INTO sanction_case_events(case_id,event_type,actor_type,reason)
		SELECT DISTINCT case_id,'response_overdue','system','Seven-day club response link expired; investigation remains open and no adverse inference was made' FROM expired`); err != nil {
			http.Error(w, "response deadline maintenance is unavailable", http.StatusServiceUnavailable)
			return
		}
		if sanctionsEmailDisabled() {
			http.Error(w, "sanctions email sending is disabled in this environment", http.StatusServiceUnavailable)
			return
		}
		var globalEnabled bool
		if err := s.DB.QueryRow(ctx, `SELECT enabled FROM sanction_automation_settings WHERE source_type='_global'`).Scan(&globalEnabled); err != nil || !globalEnabled {
			http.Error(w, "sanctions automation kill switch is active", 503)
			return
		}
		// A global advisory lock avoids duplicate SMTP sends by overlapping workers.
		lockConn, err := s.DB.Acquire(ctx)
		if err != nil {
			http.Error(w, "outbox lock unavailable", 503)
			return
		}
		var locked bool
		_ = lockConn.QueryRow(ctx, `SELECT pg_try_advisory_lock(83002)`).Scan(&locked)
		if !locked {
			lockConn.Release()
			http.Error(w, "outbox worker already running", 409)
			return
		}
		defer func() {
			unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer unlockCancel()
			_, _ = lockConn.Exec(unlockCtx, `SELECT pg_advisory_unlock(83002)`)
			lockConn.Release()
		}()
		rows, err := s.DB.Query(ctx, `SELECT o.id,o.message_kind,o.recipient,o.subject,o.body,o.attachment_manifest,cr.pdf_bytes,cr.pdf_sha256,cr.pdf_storage_key
			FROM sanction_notification_outbox o JOIN sanction_cases c ON c.id=o.case_id LEFT JOIN sanction_correspondence_revisions cr ON cr.id=o.correspondence_revision_id
			WHERE o.processed_at IS NULL AND o.revoked_at IS NULL AND o.available_at<=now()
			  AND (c.source_type<>'ineligible_player' OR $1::boolean)
			  AND (
				o.message_kind NOT IN ('response_request','response_reminder')
				OR EXISTS(
					SELECT 1
					FROM sanction_response_requests response
					JOIN sanction_case_access_tokens token
					  ON token.id=response.access_token_id AND token.case_id=response.case_id
					WHERE response.case_id=o.case_id
					  AND (
						(o.message_kind='response_request' AND response.correspondence_revision_id=o.correspondence_revision_id
						 AND (response.status='queued' OR (response.status='pending' AND response.due_at>now() AND token.revoked_at IS NULL AND token.expires_at>now())))
						OR
						(o.message_kind='response_reminder' AND response.reminder_correspondence_revision_id=o.correspondence_revision_id
						 AND response.status='pending' AND response.due_at>now() AND token.revoked_at IS NULL AND token.expires_at>now())
					  )
				)
			  )
			ORDER BY o.id LIMIT 50`, ineligibleOutboundEmailEnabled())
		if err != nil {
			http.Error(w, "outbox unavailable", 500)
			return
		}
		type msg struct {
			id                       int64
			kind, to, subject, body  string
			manifest, pdf            []byte
			pdfSHA256, pdfStorageKey *string
		}
		pending := []msg{}
		for rows.Next() {
			var m msg
			if rows.Scan(&m.id, &m.kind, &m.to, &m.subject, &m.body, &m.manifest, &m.pdf, &m.pdfSHA256, &m.pdfStorageKey) == nil {
				pending = append(pending, m)
			}
		}
		rows.Close()
		sent, failed := 0, 0
		mailer := email.NewFromEnv()
		for _, m := range pending {
			messageID := fmt.Sprintf("<sanction-outbox-%d@gmcl.co.uk>", m.id)
			var attachments []email.Attachment
			var sendErr error
			if len(m.pdf) > 0 {
				actual := fmt.Sprintf("%x", sha256.Sum256(m.pdf))
				if m.pdfSHA256 == nil || !strings.EqualFold(actual, strings.TrimSpace(*m.pdfSHA256)) {
					sendErr = fmt.Errorf("approved outcome PDF checksum mismatch")
				} else {
					filename := "GMCL-outcome.pdf"
					var manifest []struct {
						Filename    string `json:"filename"`
						ContentType string `json:"content_type"`
					}
					if json.Unmarshal(m.manifest, &manifest) == nil && len(manifest) > 0 && strings.TrimSpace(manifest[0].Filename) != "" {
						filename = filepath.Base(manifest[0].Filename)
					} else if m.pdfStorageKey != nil && strings.TrimSpace(*m.pdfStorageKey) != "" {
						filename = filepath.Base(*m.pdfStorageKey)
					}
					attachments = []email.Attachment{{Filename: filename, ContentType: "application/pdf", Data: m.pdf}}
				}
			}

			// A response notice is revalidated under row locks immediately before
			// SMTP. Manual responses, intake refreshes and expiry maintenance must
			// not race a stale request or reminder into the club's mailbox.
			responseNotice := m.kind == "response_request" || m.kind == "response_reminder"
			var tx pgx.Tx
			var responseRequestID int64
			var responseRequestStatus string
			if sendErr == nil && responseNotice {
				tx, err = s.DB.Begin(ctx)
				if err != nil {
					failed++
					continue
				}
				err = tx.QueryRow(ctx, `SELECT request.id,request.status
					FROM sanction_notification_outbox outbox
					JOIN sanction_cases cases ON cases.id=outbox.case_id
					JOIN sanction_response_requests request ON request.case_id=outbox.case_id
					JOIN sanction_case_access_tokens token
					  ON token.id=request.access_token_id AND token.case_id=request.case_id
					WHERE outbox.id=$1 AND outbox.processed_at IS NULL AND outbox.revoked_at IS NULL AND outbox.available_at<=now()
					  AND token.revoked_at IS NULL
					  AND (
						(outbox.message_kind='response_request'
						 AND request.correspondence_revision_id=outbox.correspondence_revision_id
						 AND (
							(request.status='queued' AND request.reminder_correspondence_revision_id IS NOT NULL
							 AND cases.status='response_pending')
							OR
							(request.status='pending' AND request.due_at>now() AND token.expires_at>now()
							 AND cases.status='response_pending')
						 ))
						OR
						(outbox.message_kind='response_reminder'
						 AND request.reminder_correspondence_revision_id=outbox.correspondence_revision_id
						 AND request.status='pending' AND request.due_at>now() AND token.expires_at>now()
						 AND cases.status='response_pending')
					  )
					ORDER BY request.id DESC
					LIMIT 1
					FOR UPDATE OF outbox,request,token,cases`, m.id).Scan(&responseRequestID, &responseRequestStatus)
				if err != nil {
					_ = tx.Rollback(ctx)
					if !errors.Is(err, pgx.ErrNoRows) {
						failed++
					}
					continue
				}
			}

			var attempt int
			if tx != nil {
				err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt_number),0)+1 FROM sanction_notification_attempts WHERE outbox_id=$1`, m.id).Scan(&attempt)
			} else {
				err = s.DB.QueryRow(ctx, `SELECT COALESCE(MAX(attempt_number),0)+1 FROM sanction_notification_attempts WHERE outbox_id=$1`, m.id).Scan(&attempt)
			}
			if err != nil {
				if tx != nil {
					_ = tx.Rollback(ctx)
				}
				failed++
				continue
			}
			if sendErr == nil {
				sendErr = mailer.SendSanctionSnapshot(m.to, m.subject, m.body, messageID, attachments)
			}
			if sendErr != nil {
				failed++
				if tx != nil {
					_, err = tx.Exec(ctx, `INSERT INTO sanction_notification_attempts(outbox_id,attempt_number,status,provider_message_id,error_message) VALUES($1,$2,'failed',$3,$4) ON CONFLICT DO NOTHING`, m.id, attempt, messageID, sendErr.Error())
					if err == nil {
						err = tx.Commit(ctx)
					} else {
						_ = tx.Rollback(ctx)
					}
				} else {
					_, _ = s.DB.Exec(ctx, `INSERT INTO sanction_notification_attempts(outbox_id,attempt_number,status,provider_message_id,error_message) VALUES($1,$2,'failed',$3,$4) ON CONFLICT DO NOTHING`, m.id, attempt, messageID, sendErr.Error())
				}
				continue
			}
			if tx == nil {
				tx, err = s.DB.Begin(ctx)
			}
			if err != nil {
				failed++
				continue
			}
			_, err = tx.Exec(ctx, `INSERT INTO sanction_notification_attempts(outbox_id,attempt_number,status,provider_message_id) VALUES($1,$2,'sent',$3)`, m.id, attempt, messageID)
			if err == nil {
				var tag pgconn.CommandTag
				tag, err = tx.Exec(ctx, `UPDATE sanction_notification_outbox SET processed_at=now() WHERE id=$1 AND processed_at IS NULL`, m.id)
				if err == nil && tag.RowsAffected() != 1 {
					err = fmt.Errorf("sanction outbox message was no longer pending")
				}
			}

			if err == nil && shouldActivateResponseWindow(responseRequestStatus) {
				var deliveredAt time.Time
				err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&deliveredAt)
				deliveredAt = deliveredAt.In(s.LondonLoc)
				reminderAt, dueAt := responseDeliveryDeadlines(deliveredAt)
				var reminderOutboxID int64
				if err == nil {
					err = tx.QueryRow(ctx, `INSERT INTO sanction_notification_outbox(
						case_id,policy_version_id,correspondence_revision_id,message_kind,idempotency_key,
						recipient,subject,body,attachment_manifest,available_at)
					SELECT request.case_id,initial.policy_version_id,reminder.id,'response_reminder',
					       format('case:%s:response-reminder:%s',request.case_id,reminder.id),
					       NULLIF(BTRIM(reminder.recipients->>0),''),reminder.subject,reminder.body,
					       reminder.attachment_manifest,$3
					FROM sanction_response_requests request
					JOIN sanction_notification_outbox initial ON initial.id=$2 AND initial.case_id=request.case_id
					JOIN sanction_correspondence_revisions reminder
					  ON reminder.id=request.reminder_correspondence_revision_id AND reminder.case_id=request.case_id
					WHERE request.id=$1 AND request.status='queued'
					RETURNING id`, responseRequestID, m.id, reminderAt).Scan(&reminderOutboxID)
				}
				if err == nil {
					var tag pgconn.CommandTag
					tag, err = tx.Exec(ctx, `UPDATE sanction_case_access_tokens token
						SET expires_at=$2
						FROM sanction_response_requests request
						WHERE request.id=$1 AND token.id=request.access_token_id
						  AND token.case_id=request.case_id AND token.revoked_at IS NULL`, responseRequestID, dueAt)
					if err == nil && tag.RowsAffected() != 1 {
						err = fmt.Errorf("queued response token was no longer activatable")
					}
				}
				if err == nil {
					var tag pgconn.CommandTag
					tag, err = tx.Exec(ctx, `UPDATE sanction_response_requests
						SET status='pending',delivered_at=$2,reminder_due_at=$3,due_at=$4,reminder_queued_at=$2
						WHERE id=$1 AND status='queued'`, responseRequestID, deliveredAt, reminderAt, dueAt)
					if err == nil && tag.RowsAffected() != 1 {
						err = fmt.Errorf("queued response request was no longer activatable")
					}
				}
				if err == nil {
					var tag pgconn.CommandTag
					tag, err = tx.Exec(ctx, `UPDATE sanction_cases cases
						SET updated_at=$2
						FROM sanction_response_requests request
						WHERE request.id=$1 AND cases.id=request.case_id
						  AND cases.status='response_pending'`, responseRequestID, deliveredAt)
					if err == nil && tag.RowsAffected() != 1 {
						err = fmt.Errorf("case was no longer ready for a club response")
					}
				}
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,after_data)
						SELECT case_id,'response_requested','system','Sanctions outbox',
						       'Initial response request delivered; seven-day response window activated and one day-five reminder queued',
						       jsonb_build_object('response_request_id',$1::bigint,'initial_outbox_id',$2::bigint,
						                          'reminder_outbox_id',$3::bigint,'delivered_at',$4::timestamptz,
						                          'reminder_due_at',$5::timestamptz,'due_at',$6::timestamptz)
						FROM sanction_response_requests WHERE id=$1`, responseRequestID, m.id, reminderOutboxID, deliveredAt, reminderAt, dueAt)
				}
			}

			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			if err != nil {
				failed++
			} else {
				sent++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"selected": len(pending), "sent": sent, "failed": failed})
	}
}

func responseDeliveryDeadlines(deliveredAt time.Time) (reminderAt, dueAt time.Time) {
	return deliveredAt.AddDate(0, 0, 5), deliveredAt.AddDate(0, 0, 7)
}

func shouldActivateResponseWindow(requestStatus string) bool {
	return requestStatus == "queued"
}

func (s *Server) handleAdminSanctionAutomation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.DB.Query(r.Context(), `SELECT source_type,mode,enabled,clean_cycles,last_reconciled_at,activated_at FROM sanction_automation_settings ORDER BY source_type`)
		if err != nil {
			http.Error(w, "settings unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions automation")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container py-4" style="max-width:900px"><h1 class="h2">Sanctions automation safety</h1><p class="text-muted">Only deterministic sources can become automatic. Three reconciled clean cycles and an explicit super-admin change are required.</p><div class="row g-3">`)
		for rows.Next() {
			var source, mode string
			var enabled bool
			var cycles int
			var reconciled, activated *time.Time
			if rows.Scan(&source, &mode, &enabled, &cycles, &reconciled, &activated) != nil {
				continue
			}
			checked := ""
			if enabled {
				checked = " checked"
			}
			automaticOption := fmt.Sprintf(`<option value="automatic"%s>Automatic</option>`, selectedMode(mode, "automatic"))
			if source == "ineligible_player" {
				automaticOption = `<option value="automatic" disabled>Automatic unavailable for investigation decisions</option>`
			}
			fmt.Fprintf(w, `<div class="col-md-6"><form method="POST" action="/admin/cases/automation" class="card h-100"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="source_type" value="%s"><div class="card-header"><strong>%s</strong></div><div class="card-body"><p>Mode: <strong>%s</strong><br>Clean cycles: <strong>%d / 3</strong></p><select class="form-select mb-2" name="mode"><option value="shadow"%s>Shadow</option><option value="manual"%s>Manual approval</option>%s</select><label class="form-check mb-2"><input class="form-check-input" type="checkbox" name="enabled" value="yes"%s> <span class="form-check-label">Enabled (clear to activate kill switch)</span></label><label class="form-label">Reason</label><input class="form-control" name="reason" required></div><div class="card-footer d-flex gap-2"><button class="btn btn-primary" name="action" value="update">Save</button>`, csrf, escapeHTML(source), escapeHTML(source), escapeHTML(mode), cycles, selectedMode(mode, "shadow"), selectedMode(mode, "manual"), automaticOption, checked)
			if source != "_global" && source != "ineligible_player" {
				fmt.Fprint(w, `<button class="btn btn-outline-success" name="action" value="clean_cycle">Record clean cycle</button>`)
			}
			fmt.Fprint(w, `</div></form></div>`)
		}
		fmt.Fprint(w, `</div></main>`)
		pageFooter(w)
	}
}

func selectedMode(current, want string) string {
	if current == want {
		return " selected"
	}
	return ""
}

func (s *Server) handleAdminSanctionAutomationPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		source := r.FormValue("source_type")
		mode := r.FormValue("mode")
		reason := strings.TrimSpace(r.FormValue("reason"))
		action := r.FormValue("action")
		allowed := map[string]bool{"_global": true, "captain_report": true, "play_cricket": true, "ineligible_player": true}
		if !allowed[source] || reason == "" {
			http.Error(w, "valid source and reason required", 400)
			return
		}
		sess, _ := getAdminSessionFromRequest(r)
		if sess == nil || s.effectiveAdminRole(r.Context(), sess.AdminID) != "super_admin" {
			http.Error(w, "super-admin required", 403)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "update failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var oldMode string
		var oldEnabled bool
		var cycles int
		if tx.QueryRow(r.Context(), `SELECT mode,enabled,clean_cycles FROM sanction_automation_settings WHERE source_type=$1 FOR UPDATE`, source).Scan(&oldMode, &oldEnabled, &cycles) != nil {
			http.Error(w, "source not found", 404)
			return
		}
		newMode, newEnabled := oldMode, oldEnabled
		if action == "clean_cycle" {
			if source == "ineligible_player" {
				http.Error(w, "ineligible-player clean cycles are recorded only by successful scheduled daily reconciliations", http.StatusBadRequest)
				return
			}
			cycles++
		} else {
			if mode != "shadow" && mode != "manual" && mode != "automatic" {
				http.Error(w, "invalid mode", 400)
				return
			}
			if source == "_global" && mode == "automatic" {
				mode = "manual"
			}
			if source == "ineligible_player" && mode == "automatic" {
				http.Error(w, "ineligible-player decisions always require independent human approval", http.StatusBadRequest)
				return
			}
			if mode == "automatic" && cycles < 3 {
				http.Error(w, "three clean cycles are required before automatic mode", 400)
				return
			}
			newMode = mode
			newEnabled = r.FormValue("enabled") == "yes"
		}
		before, _ := json.Marshal(map[string]any{"mode": oldMode, "enabled": oldEnabled, "clean_cycles": func() int {
			if action == "clean_cycle" {
				return cycles - 1
			}
			return cycles
		}()})
		after, _ := json.Marshal(map[string]any{"mode": newMode, "enabled": newEnabled, "clean_cycles": cycles})
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_configuration_events(configuration_type,configuration_key,actor_admin_id,reason,before_data,after_data,request_id) VALUES('automation',$1,$2,$3,$4,$5,$6)`, source, sess.AdminID, reason, before, after, requestID(r))
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_automation_settings SET mode=$2,enabled=$3,clean_cycles=$4,last_reconciled_at=CASE WHEN $5='clean_cycle' THEN now() ELSE last_reconciled_at END,activated_by_admin_id=CASE WHEN $2='automatic' THEN $6 ELSE activated_by_admin_id END,activated_at=CASE WHEN $2='automatic' THEN now() ELSE activated_at END,updated_at=now() WHERE source_type=$1`, source, newMode, newEnabled, cycles, action, sess.AdminID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "update failed", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/automation", 303)
	}
}

func parseIntOrNil(v string) *int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func (s *Server) handleAdminSanctionTasks() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		includeClosed := r.URL.Query().Get("archive") == "1"
		where := `t.status IN ('open','in_progress')`
		if includeClosed {
			where = `TRUE`
		}
		rows, err := s.DB.Query(r.Context(), `SELECT t.id,c.id,c.reference,t.task_type,t.status,COALESCE(t.current_note,''),t.due_at,t.created_at,COALESCE(a.username,'') FROM sanction_follow_up_tasks t JOIN sanction_cases c ON c.id=t.case_id LEFT JOIN admin_users a ON a.id=t.assigned_admin_id WHERE `+where+` ORDER BY CASE t.status WHEN 'open' THEN 0 WHEN 'in_progress' THEN 1 ELSE 2 END,t.due_at NULLS LAST,t.id`)
		if err != nil {
			http.Error(w, "tasks unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions follow-up tasks")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container py-4" style="max-width:1000px"><div class="d-flex flex-column flex-sm-row justify-content-between gap-2 mb-3"><div><h1 class="h2">Sanctions follow-up tasks</h1><p class="text-muted mb-0">Points deductions, fine recovery, Board intervention, suspension reviews, appeals and ban expiry.</p></div><a class="btn btn-outline-secondary align-self-sm-start" href="/admin/cases">Back to cases</a></div><form method="GET" class="mb-3"><label class="form-check"><input class="form-check-input" type="checkbox" name="archive" value="1"`)
		if includeClosed {
			fmt.Fprint(w, ` checked`)
		}
		fmt.Fprint(w, ` onchange="this.form.submit()"> <span class="form-check-label">Include completed and cancelled tasks</span></label></form><div class="row g-3">`)
		count := 0
		for rows.Next() {
			var taskID, caseID int64
			var ref, taskType, status, note, assigned string
			var due *time.Time
			var created time.Time
			if rows.Scan(&taskID, &caseID, &ref, &taskType, &status, &note, &due, &created, &assigned) != nil {
				continue
			}
			count++
			dueLabel := "No due date"
			if due != nil {
				dueLabel = due.In(s.LondonLoc).Format("02 Jan 2006 15:04")
			}
			fmt.Fprintf(w, `<div class="col-12"><article class="card"><div class="card-header d-flex flex-wrap justify-content-between gap-2"><div><a href="/admin/cases/%d"><strong>%s</strong></a> <span class="badge text-bg-secondary">%s</span></div><span>%s</span></div><form method="POST" action="/admin/cases/tasks/%d"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-3"><label class="form-label">Status</label><select class="form-select" name="status"><option value="open"%s>Open</option><option value="in_progress"%s>In progress</option><option value="complete"%s>Complete</option><option value="cancelled"%s>Cancelled</option></select></div><div class="col-md-4"><label class="form-label">Operational note</label><input class="form-control" name="note" value="%s"></div><div class="col-md-5"><label class="form-label">Reason for change</label><input class="form-control" name="reason" required placeholder="Why is this task changing?"></div></div><div class="card-footer d-flex flex-column flex-sm-row justify-content-between gap-2"><small class="text-muted">Due: %s · Assigned: %s · Created: %s</small><button class="btn btn-primary">Record task update</button></div></form></article></div>`, caseID, escapeHTML(ref), escapeHTML(strings.ReplaceAll(taskType, "_", " ")), escapeHTML(status), taskID, csrf, selectedMode(status, "open"), selectedMode(status, "in_progress"), selectedMode(status, "complete"), selectedMode(status, "cancelled"), escapeHTML(note), escapeHTML(dueLabel), escapeHTML(defaultString(assigned, "unassigned")), created.In(s.LondonLoc).Format("02 Jan 2006"))
		}
		if count == 0 {
			fmt.Fprint(w, `<div class="col-12"><div class="alert alert-success">There are no follow-up tasks in this view.</div></div>`)
		}
		fmt.Fprint(w, `</div></main>`)
		pageFooter(w)
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *Server) handleAdminSanctionTaskUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
		if err != nil || taskID < 1 || r.ParseForm() != nil {
			http.Error(w, "invalid task", 400)
			return
		}
		status := strings.TrimSpace(r.FormValue("status"))
		reason := strings.TrimSpace(r.FormValue("reason"))
		note := strings.TrimSpace(r.FormValue("note"))
		if !map[string]bool{"open": true, "in_progress": true, "complete": true, "cancelled": true}[status] || reason == "" {
			http.Error(w, "valid status and reason are required", 400)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", 401)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "task update failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var beforeData []byte
		var taskType string
		var assignedAdminID *int32
		if tx.QueryRow(r.Context(), `SELECT to_jsonb(t),task_type,assigned_admin_id FROM sanction_follow_up_tasks t WHERE id=$1 FOR UPDATE`, taskID).Scan(&beforeData, &taskType, &assignedAdminID) != nil {
			http.NotFound(w, r)
			return
		}
		if taskType == "play_cricket_points" && (assignedAdminID == nil || *assignedAdminID != *actor.ID) {
			http.Error(w, "only the assigned Play-Cricket administrator can update this points task", http.StatusForbidden)
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE sanction_follow_up_tasks SET status=$2,current_note=$3,assigned_admin_id=COALESCE(assigned_admin_id,$4),updated_at=now() WHERE id=$1`, taskID, status, nullIfEmptyHTTP(note), *actor.ID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_follow_up_task_events(task_id,actor_admin_id,actor_label,reason,before_data,after_data,request_id) SELECT id,$2,$3,$4,$5::jsonb,to_jsonb(t),$6 FROM sanction_follow_up_tasks t WHERE id=$1`, taskID, *actor.ID, actor.Label, reason, string(beforeData), actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "task update failed", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/tasks", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminSanctionRecipients() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.DB.Query(r.Context(), `SELECT recipient_role,name,email,active FROM sanction_recipient_directory ORDER BY recipient_role,name`)
		if err != nil {
			http.Error(w, "recipients unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanction recipients")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><h1 class="h2">Sanction notice recipients</h1><p class="text-muted">Captains are resolved automatically. These role addresses receive notices when the versioned policy calls for them.</p><form method="POST" action="/admin/cases/recipients" class="card mb-4"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-3"><select class="form-select" name="recipient_role"><option value="executive">Executive</option><option value="discipline">Discipline</option><option value="finance">Finance</option><option value="play_cricket">Play-Cricket</option><option value="other">Other</option></select></div><div class="col-md-3"><input class="form-control" name="name" required placeholder="Name"></div><div class="col-md-3"><input class="form-control" type="email" name="email" required placeholder="Email"></div><div class="col-md-3"><input class="form-control" name="reason" required placeholder="Reason for change"></div></div><div class="card-footer"><button class="btn btn-primary">Add or reactivate</button></div></form><table class="table"><thead><tr><th>Role</th><th>Name</th><th>Email</th><th>Status</th></tr></thead><tbody>`, csrf)
		for rows.Next() {
			var role, name, emailAddr string
			var active bool
			if rows.Scan(&role, &name, &emailAddr, &active) == nil {
				status := "inactive"
				if active {
					status = "active"
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, escapeHTML(role), escapeHTML(name), escapeHTML(emailAddr), status)
			}
		}
		fmt.Fprint(w, `</tbody></table><hr class="my-5"><h2 class="h3">Official club mailboxes</h2><p class="text-muted">Response requests and club outcomes are blocked until an administrator verifies the exact official mailbox. Suggested aliases are never active automatically.</p>`)
		clubRows, clubErr := s.DB.Query(r.Context(), `SELECT c.id,c.name,COALESCE(contact.email,''),COALESCE(contact.active,FALSE),contact.verified_at
			FROM clubs c LEFT JOIN LATERAL (SELECT email,active,verified_at FROM sanction_club_contacts WHERE club_id=c.id ORDER BY active DESC,verified_at DESC NULLS LAST,id DESC LIMIT 1) contact ON TRUE
			ORDER BY c.name`)
		if clubErr == nil {
			defer clubRows.Close()
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/club-contacts" class="card mb-4"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-4"><label class="form-label">Club</label><select class="form-select" name="club_id" required><option value="">Select club</option>`, csrf)
			type clubContactRow struct {
				id       int32
				name     string
				email    string
				active   bool
				verified *time.Time
			}
			var contacts []clubContactRow
			for clubRows.Next() {
				var contact clubContactRow
				if clubRows.Scan(&contact.id, &contact.name, &contact.email, &contact.active, &contact.verified) == nil {
					contacts = append(contacts, contact)
					fmt.Fprintf(w, `<option value="%d">%s</option>`, contact.id, escapeHTML(contact.name))
				}
			}
			fmt.Fprint(w, `</select></div><div class="col-md-4"><label class="form-label">Verified official email</label><input class="form-control" type="email" name="email" required></div><div class="col-md-4"><label class="form-label">Audit reason</label><input class="form-control" name="reason" required placeholder="How was this address verified?"></div></div><div class="card-footer"><button class="btn btn-primary">Verify and activate mailbox</button></div></form><div class="table-responsive"><table class="table"><thead><tr><th>Club</th><th>Current / suggested mailbox</th><th>Status</th></tr></thead><tbody>`)
			for _, contact := range contacts {
				status := "unresolved"
				if contact.active && contact.verified != nil {
					status = "verified " + contact.verified.In(s.LondonLoc).Format("02 Jan 2006")
				} else if contact.email != "" {
					status = "suggested only"
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`, escapeHTML(contact.name), escapeHTML(contact.email), escapeHTML(status))
			}
			fmt.Fprint(w, `</tbody></table></div>`)
		}
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSanctionRecipientDirectoryPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		role := r.FormValue("recipient_role")
		name := strings.TrimSpace(r.FormValue("name"))
		emailAddr := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		reason := strings.TrimSpace(r.FormValue("reason"))
		allowed := map[string]bool{"executive": true, "discipline": true, "finance": true, "play_cricket": true, "other": true}
		parsed, parseErr := mail.ParseAddress(emailAddr)
		if !allowed[role] || name == "" || parseErr != nil || parsed.Address == "" || !strings.EqualFold(parsed.Address, emailAddr) || reason == "" {
			http.Error(w, "role, name, email and reason are required", 400)
			return
		}
		sess, _ := getAdminSessionFromRequest(r)
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "save failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var before []byte
		var oldName string
		var oldActive bool
		if tx.QueryRow(r.Context(), `SELECT name,active FROM sanction_recipient_directory WHERE recipient_role=$1 AND email=$2`, role, emailAddr).Scan(&oldName, &oldActive) == nil {
			before, _ = json.Marshal(map[string]any{"name": oldName, "active": oldActive})
		}
		after, _ := json.Marshal(map[string]any{"name": name, "email": emailAddr, "role": role, "active": true})
		var admin any
		if sess != nil {
			admin = sess.AdminID
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_configuration_events(configuration_type,configuration_key,actor_admin_id,reason,before_data,after_data,request_id) VALUES('recipient',$1,$2,$3,$4,$5,$6)`, role+":"+emailAddr, admin, reason, before, after, requestID(r))
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES($1,$2,$3) ON CONFLICT(recipient_role,email) DO UPDATE SET name=EXCLUDED.name,active=TRUE`, role, name, emailAddr)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "save failed", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/recipients", 303)
	}
}

func (s *Server) handleAdminSanctionClubContactPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		clubID, err := strconv.ParseInt(r.FormValue("club_id"), 10, 32)
		emailAddress := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		reason := strings.TrimSpace(r.FormValue("reason"))
		parsed, parseErr := mail.ParseAddress(emailAddress)
		if err != nil || clubID < 1 || parseErr != nil || !strings.EqualFold(parsed.Address, emailAddress) || reason == "" {
			http.Error(w, "club, verified official email and audit reason are required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "club contact update failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var clubName string
		if err = tx.QueryRow(r.Context(), `SELECT name FROM clubs WHERE id=$1 FOR UPDATE`, clubID).Scan(&clubName); err != nil {
			http.Error(w, "club not found", http.StatusNotFound)
			return
		}
		var before []byte
		_ = tx.QueryRow(r.Context(), `SELECT to_jsonb(contact) FROM sanction_club_contacts contact WHERE club_id=$1 AND contact_type='official_mailbox' AND active FOR UPDATE`, clubID).Scan(&before)
		if _, err = tx.Exec(r.Context(), `UPDATE sanction_club_contacts SET active=FALSE,updated_at=now() WHERE club_id=$1 AND contact_type='official_mailbox' AND active`, clubID); err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_club_contacts(club_id,contact_type,name,email,active,verified_at,created_by_admin_id)
				VALUES($1,'official_mailbox',$2,$3,TRUE,now(),$4)`, clubID, clubName, emailAddress, *actor.ID)
		}
		if err == nil {
			after, _ := json.Marshal(map[string]any{"club_id": clubID, "club": clubName, "email": emailAddress, "active": true, "verified": true})
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_configuration_events(configuration_type,configuration_key,actor_admin_id,reason,before_data,after_data,request_id)
				VALUES('club_contact',$1,$2,$3,$4,$5,$6)`, fmt.Sprintf("club:%d", clubID), *actor.ID, reason, before, after, actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "club contact update failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/cases/recipients", http.StatusSeeOther)
	}
}
