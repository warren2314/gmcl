package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func adminCloseCaseNoActionHTML(caseID int64, csrf, status string, hasProposed bool, assignedAdminID, currentAdminID *int32) string {
	if !map[string]bool{"submitted": true, "triage": true, "investigating": true, "response_pending": true, "decision_proposed": true}[status] || !sameAdminAssignment(assignedAdminID, currentAdminID) {
		return ""
	}
	return fmt.Sprintf(`<form method="POST" action="/admin/cases/%d/close-no-action" class="card mb-3 border-success"><input type="hidden" name="csrf_token" value="%s"><div class="card-header"><strong>Close with no action</strong></div><div class="card-body"><p class="small">Use this when the investigation is complete and no sanction or outcome letter is required. The case goes straight to <strong>Closed</strong>; its history and evidence remain available.</p><label class="form-label">Reason for taking no action</label><textarea class="form-control" name="reason" required minlength="5" maxlength="2000" rows="3"></textarea><div class="form-check mt-3"><input class="form-check-input" type="checkbox" name="confirm" value="yes" id="confirm-close-no-action" required><label class="form-check-label" for="confirm-close-no-action">I confirm that no sanction, approval request or outcome letter is required.</label></div><div class="form-text mt-2">Any pending response link, reminder, unsent email or open follow-up task will be cancelled.</div></div><div class="card-footer"><button class="btn btn-outline-success">Close case with no action</button></div></form>`, caseID, escapeHTML(csrf))
}

func (s *Server) handleAdminCaseCloseNoAction() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID <= 0 {
			http.NotFound(w, r)
			return
		}
		if err = r.ParseForm(); err != nil {
			http.Error(w, "invalid close request", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if r.FormValue("confirm") != "yes" || utf8.RuneCountInString(reason) < 5 || utf8.RuneCountInString(reason) > 2000 {
			http.Error(w, "confirm no action and provide a reason of 5 to 2,000 characters", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}

		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			http.Error(w, "could not close case", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var locked bool
		if err = tx.QueryRow(r.Context(), `SELECT pg_try_advisory_xact_lock($1)`, sanctionsOutboxAdvisoryLock).Scan(&locked); err != nil || !locked {
			http.Error(w, "email delivery is busy; wait a moment and try again", http.StatusConflict)
			return
		}

		var reference, status string
		var assignedAdminID *int32
		var hasDecision bool
		if err = tx.QueryRow(r.Context(), `SELECT cases.reference,cases.status,cases.assigned_admin_id,
			EXISTS(SELECT 1 FROM sanction_decision_revisions decision WHERE decision.case_id=cases.id)
			FROM sanction_cases cases WHERE cases.id=$1 FOR UPDATE`, caseID).Scan(&reference, &status, &assignedAdminID, &hasDecision); err != nil {
			http.NotFound(w, r)
			return
		}
		if !map[string]bool{"submitted": true, "triage": true, "investigating": true, "response_pending": true, "decision_proposed": true}[status] {
			http.Error(w, "this case is not open for no-action closure", http.StatusConflict)
			return
		}
		if !sameAdminAssignment(assignedAdminID, actor.ID) {
			http.Error(w, "only the assigned case owner can close this case with no action", http.StatusForbidden)
			return
		}

		if _, err = tx.Exec(r.Context(), `UPDATE sanction_case_access_tokens token SET revoked_at=COALESCE(token.revoked_at,now()),last_used_at=now()
			WHERE token.id IN (SELECT request.access_token_id FROM sanction_response_requests request WHERE request.case_id=$1 AND request.status IN ('queued','pending'))`, caseID); err != nil {
			http.Error(w, "could not cancel case access", http.StatusInternalServerError)
			return
		}
		cancelledRequests, revokedMessages, cancelledTasks := int64(0), int64(0), int64(0)
		result, err := tx.Exec(r.Context(), `UPDATE sanction_response_requests SET status='cancelled',closed_at=COALESCE(closed_at,now()) WHERE case_id=$1 AND status IN ('queued','pending')`, caseID)
		if err == nil {
			cancelledRequests = result.RowsAffected()
			result, err = tx.Exec(r.Context(), `UPDATE sanction_notification_outbox SET processed_at=now(),revoked_at=now(),revoked_by_admin_id=$2,revocation_reason=$3 WHERE case_id=$1 AND processed_at IS NULL AND revoked_at IS NULL`, caseID, *actor.ID, reason)
		}
		if err == nil {
			revokedMessages = result.RowsAffected()
			result, err = tx.Exec(r.Context(), `UPDATE sanction_follow_up_tasks SET status='cancelled',current_note=CONCAT_WS(E'\n',NULLIF(current_note,''),$2),updated_at=now() WHERE case_id=$1 AND status IN ('open','in_progress')`, caseID, "Cancelled because the case was closed with no action: "+reason)
		}
		if err == nil {
			cancelledTasks = result.RowsAffected()
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status='closed',public_status='unpublished',closed_at=now(),updated_at=now() WHERE id=$1`, caseID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,before_data,after_data,metadata)
				VALUES($1,'case_closed_no_action','admin',$2,$3,$4,$5,jsonb_build_object('status',$6::text),jsonb_build_object('status','closed','public_status','unpublished'),jsonb_build_object('cancelled_response_requests',$7::bigint,'revoked_outbox_messages',$8::bigint,'cancelled_follow_up_tasks',$9::bigint))`, caseID, *actor.ID, actor.Label, reason, actor.RequestID, status, cancelledRequests, revokedMessages, cancelledTasks)
		}
		if err != nil {
			http.Error(w, "could not close case", http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "could not close case", http.StatusInternalServerError)
			return
		}
		message := url.QueryEscape(reference + " closed with no action")
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", caseID, message), http.StatusSeeOther)
	}
}
