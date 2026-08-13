package httpserver

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const sanctionsOutboxAdvisoryLock int64 = 83002

func adminUndoCaseOpeningHTML(caseID int64, csrf, source, status string, isTest bool) string {
	if source != "ineligible_player" || isTest || !map[string]bool{
		"submitted": true, "triage": true, "investigating": true, "response_pending": true,
	}[status] {
		return ""
	}
	return fmt.Sprintf(`<form method="POST" action="/admin/cases/%d/undo-opening" class="card mb-3 border-warning"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Opened this case by mistake?</div><div class="card-body"><p class="small">Return the original report to <strong>pending review</strong> and remove this case from active work. Nothing is deleted.</p><label class="form-label">Reason</label><textarea class="form-control" name="reason" required minlength="5" maxlength="1000" rows="2" placeholder="For example: Opened the wrong report"></textarea><div class="form-check mt-3"><input class="form-check-input" type="checkbox" name="confirm" value="yes" id="confirm-undo-opening" required><label class="form-check-label" for="confirm-undo-opening">I understand the case will be retired and the report returned to pending review.</label></div><div class="form-text mt-2">This is blocked if a club email was delivered, a response received, or a decision recorded.</div></div><div class="card-footer"><button class="btn btn-outline-warning">Undo opening and return report</button></div></form>`, caseID, escapeHTML(csrf))
}

func (s *Server) handleAdminCaseUndoOpening() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID <= 0 {
			http.NotFound(w, r)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if r.FormValue("confirm") != "yes" || utf8.RuneCountInString(reason) < 5 || utf8.RuneCountInString(reason) > 1000 {
			http.Error(w, "confirm the undo and provide a reason of 5 to 1,000 characters", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}

		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			http.Error(w, "could not undo case opening", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var locked bool
		if err = tx.QueryRow(r.Context(), `SELECT pg_try_advisory_xact_lock($1)`, sanctionsOutboxAdvisoryLock).Scan(&locked); err != nil || !locked {
			http.Error(w, "email delivery is busy; wait a moment and try again", http.StatusConflict)
			return
		}

		var reference, source, status string
		var isTest bool
		if err = tx.QueryRow(r.Context(), `SELECT reference,source_type,status,is_test FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&reference, &source, &status, &isTest); err != nil {
			http.NotFound(w, r)
			return
		}
		if source != "ineligible_player" || isTest || !map[string]bool{"submitted": true, "triage": true, "investigating": true, "response_pending": true}[status] {
			http.Error(w, "this case cannot be returned to the intake queue", http.StatusConflict)
			return
		}

		rows, err := tx.Query(r.Context(), `SELECT link.id,link.intake_id,link.relationship
			FROM sanction_intake_case_links link
			WHERE link.case_id=$1 AND link.relationship<>'duplicate'
			  AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=link.case_id AND event.event_type='case_opening_undone')
			ORDER BY link.id FOR UPDATE OF link`, caseID)
		if err != nil {
			http.Error(w, "could not inspect the source report", http.StatusInternalServerError)
			return
		}
		var linkID, intakeID int64
		var relationship string
		linkCount := 0
		for rows.Next() {
			linkCount++
			if err = rows.Scan(&linkID, &intakeID, &relationship); err != nil {
				break
			}
		}
		rows.Close()
		if err != nil || linkCount != 1 || relationship != "primary" {
			http.Error(w, "this case has merged or ambiguous source reports and cannot be undone automatically", http.StatusConflict)
			return
		}

		var committed bool
		err = tx.QueryRow(r.Context(), `SELECT
			EXISTS(SELECT 1 FROM sanction_decision_revisions WHERE case_id=$1)
			OR EXISTS(SELECT 1 FROM sanctions WHERE case_id=$1)
			OR EXISTS(SELECT 1 FROM sanction_follow_up_tasks WHERE case_id=$1)
			OR EXISTS(SELECT 1 FROM sanction_response_requests WHERE case_id=$1 AND status IN ('responded','expired'))
			OR EXISTS(SELECT 1 FROM sanction_case_events WHERE case_id=$1 AND event_type IN ('party_response','external_response_recorded'))
			OR EXISTS(
				SELECT 1 FROM sanction_notification_outbox outbox
				WHERE outbox.case_id=$1 AND outbox.message_kind<>'response_request_test'
				  AND (outbox.processed_at IS NOT NULL OR EXISTS(SELECT 1 FROM sanction_notification_attempts attempt WHERE attempt.outbox_id=outbox.id))
			)`, caseID).Scan(&committed)
		if err != nil {
			http.Error(w, "could not check case activity", http.StatusInternalServerError)
			return
		}
		if committed {
			http.Error(w, "this case has already progressed or contacted a club and cannot be returned to pending", http.StatusConflict)
			return
		}

		var cancelledRequests, revokedMessages int64
		result, err := tx.Exec(r.Context(), `UPDATE sanction_case_access_tokens token SET revoked_at=COALESCE(token.revoked_at,now()),last_used_at=now()
			WHERE token.id IN (SELECT request.access_token_id FROM sanction_response_requests request WHERE request.case_id=$1 AND request.status IN ('queued','pending'))`, caseID)
		if err == nil {
			_ = result
			result, err = tx.Exec(r.Context(), `UPDATE sanction_response_requests SET status='cancelled',closed_at=COALESCE(closed_at,now()) WHERE case_id=$1 AND status IN ('queued','pending')`, caseID)
			cancelledRequests = result.RowsAffected()
		}
		if err == nil {
			result, err = tx.Exec(r.Context(), `UPDATE sanction_notification_outbox SET processed_at=now(),revoked_at=now(),revoked_by_admin_id=$2,revocation_reason=$3 WHERE case_id=$1 AND message_kind<>'response_request_test' AND processed_at IS NULL AND revoked_at IS NULL`, caseID, *actor.ID, reason)
			revokedMessages = result.RowsAffected()
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status='withdrawn',public_status='unpublished',closed_at=now(),updated_at=now() WHERE id=$1`, caseID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,before_data,after_data,metadata)
				VALUES($1,'case_opening_undone','admin',$2,$3,$4,$5,jsonb_build_object('status',$6::text),jsonb_build_object('status','withdrawn'),jsonb_build_object('intake_id',$7::bigint,'link_id',$8::bigint,'cancelled_response_requests',$9::bigint,'revoked_outbox_messages',$10::bigint))`, caseID, *actor.ID, actor.Label, reason, actor.RequestID, status, intakeID, linkID, cancelledRequests, revokedMessages)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_intakes SET state='reviewing',exception_message=NULL,updated_at=now() WHERE id=$1`, intakeID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_intake_events(intake_id,event_type,actor_admin_id,actor_label,reason,before_data,after_data,request_id)
				VALUES($1,'case_opening_undone',$2,$3,$4,jsonb_build_object('state','linked','case_id',$5::bigint,'reference',$6::text),jsonb_build_object('state','reviewing','retired_case_id',$5::bigint),$7)`, intakeID, *actor.ID, actor.Label, reason, caseID, reference, actor.RequestID)
		}
		if err != nil {
			slog.Error("undo ineligible case opening", "case_id", caseID, "intake_id", intakeID, "error", err)
			http.Error(w, "could not return the report to pending review", http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "could not return the report to pending review", http.StatusInternalServerError)
			return
		}
		message := url.QueryEscape(reference + " retired; report returned to pending review")
		http.Redirect(w, r, fmt.Sprintf("/admin/ineligible/%d?success=%s", intakeID, message), http.StatusSeeOther)
	}
}
