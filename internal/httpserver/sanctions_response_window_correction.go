package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type adminCaseResponseWindowView struct {
	ID               int64
	Status           string
	DeliveredAt      *time.Time
	ReminderDueAt    *time.Time
	DueAt            *time.Time
	CorrectedAt      *time.Time
	CorrectionReason string
}

func (s *Server) loadAdminCaseResponseWindow(ctx context.Context, caseID int64) adminCaseResponseWindowView {
	var view adminCaseResponseWindowView
	_ = s.DB.QueryRow(ctx, `SELECT id,status,delivered_at,reminder_due_at,due_at,window_corrected_at,COALESCE(window_correction_reason,'')
		FROM sanction_response_requests WHERE case_id=$1 ORDER BY id DESC LIMIT 1`, caseID).
		Scan(&view.ID, &view.Status, &view.DeliveredAt, &view.ReminderDueAt, &view.DueAt, &view.CorrectedAt, &view.CorrectionReason)
	return view
}

func adminCaseResponseWindowHTML(caseID int64, csrf string, view adminCaseResponseWindowView, caseStatus string, loc *time.Location) string {
	if view.ID == 0 {
		return ""
	}
	if loc == nil {
		loc = time.Local
	}
	badgeClass := "text-bg-secondary"
	badgeLabel := strings.ReplaceAll(view.Status, "_", " ")
	switch view.Status {
	case "queued":
		badgeClass, badgeLabel = "text-bg-warning", "Waiting for email delivery"
	case "pending":
		badgeClass, badgeLabel = "text-bg-primary", "Response due"
		if view.DueAt != nil && !view.DueAt.After(time.Now()) {
			badgeClass, badgeLabel = "text-bg-danger", "Response overdue"
		}
	case "expired":
		badgeClass, badgeLabel = "text-bg-danger", "Response overdue"
	case "responded":
		badgeClass, badgeLabel = "text-bg-success", "Response received"
	case "cancelled":
		badgeLabel = "Response request cancelled"
	}

	formatTime := func(value *time.Time) string {
		if value == nil {
			return "Not set"
		}
		return value.In(loc).Format("02 Jan 2006 15:04")
	}
	var body strings.Builder
	fmt.Fprintf(&body, `<section class="card mb-4 border-primary" id="response-window"><div class="card-header d-flex flex-wrap justify-content-between gap-2"><strong>Club response clock</strong><span class="badge %s">%s</span></div><div class="card-body">`, badgeClass, escapeHTML(badgeLabel))
	if view.Status == "queued" {
		fmt.Fprint(&body, `<p class="mb-0">The initial email is queued. The seven-day clock will start only after successful delivery.</p>`)
	} else if view.DeliveredAt != nil {
		fmt.Fprintf(&body, `<dl class="row small mb-2"><dt class="col-sm-4">Clock started</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Day-five reminder</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Response deadline</dt><dd class="col-sm-8">%s</dd></dl>`, escapeHTML(formatTime(view.DeliveredAt)), escapeHTML(formatTime(view.ReminderDueAt)), escapeHTML(formatTime(view.DueAt)))
	}
	if view.CorrectedAt != nil {
		fmt.Fprintf(&body, `<div class="alert alert-light border small"><strong>Clock corrected %s.</strong> %s</div>`, escapeHTML(formatTime(view.CorrectedAt)), escapeHTML(view.CorrectionReason))
	}
	if view.Status == "pending" && caseStatus == "response_pending" && view.DeliveredAt != nil {
		currentValue := view.DeliveredAt.In(loc).Format("2006-01-02T15:04")
		fmt.Fprintf(&body, `<details class="border rounded p-3 mt-3"><summary class="fw-semibold">Correct an earlier initial-email date</summary><p class="small text-muted mt-3">Use this only when the club was first contacted outside this system before the recorded delivery above. The date can only move earlier. Day five, day seven and the secure-link expiry will be recalculated. If day seven has already passed, this case moves to <strong>Response overdue</strong> immediately.</p><form method="POST" action="/admin/cases/%d/response-window/correct"><input type="hidden" name="csrf_token" value="%s"><div class="mb-3"><label class="form-label" for="response-window-start">When was the initial email first sent?</label><input class="form-control" id="response-window-start" type="datetime-local" name="started_at" value="%s" required></div><div class="mb-3"><label class="form-label" for="response-window-reason">Why is the clock being corrected?</label><textarea class="form-control" id="response-window-reason" name="reason" rows="2" maxlength="2000" required placeholder="For example: Initial email was sent manually from the league mailbox on 24 August."></textarea></div><button class="btn btn-outline-warning" type="submit">Correct response clock</button></form></details>`, caseID, escapeHTML(csrf), escapeHTML(currentValue))
	}
	fmt.Fprint(&body, `</div></section>`)
	return body.String()
}

func parseAdminResponseWindowStart(value string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.Local
	}
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, loc); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("enter a valid initial-email date and time")
}

func (s *Server) handleAdminCaseResponseWindowCorrect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID <= 0 {
			http.NotFound(w, r)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		if err = r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" || len(reason) > 2000 {
			http.Error(w, "a correction reason is required", http.StatusBadRequest)
			return
		}
		startedAt, err := parseAdminResponseWindowStart(r.FormValue("started_at"), s.LondonLoc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "could not correct the response clock", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())

		var requestID, tokenID, reminderCorrespondenceID int64
		var requestStatus, caseStatus string
		var deliveredAt, reminderDueAt, dueAt, correctedAt time.Time
		err = tx.QueryRow(r.Context(), `SELECT request.id,request.status,request.delivered_at,request.reminder_due_at,request.due_at,
			request.access_token_id,request.reminder_correspondence_revision_id,cases.status,clock_timestamp()
			FROM sanction_response_requests request JOIN sanction_cases cases ON cases.id=request.case_id
			WHERE request.case_id=$1 AND request.status='pending'
			ORDER BY request.id DESC LIMIT 1 FOR UPDATE OF request,cases`, caseID).
			Scan(&requestID, &requestStatus, &deliveredAt, &reminderDueAt, &dueAt, &tokenID, &reminderCorrespondenceID, &caseStatus, &correctedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "this case does not have an active delivered response clock", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "could not load the response clock", http.StatusInternalServerError)
			return
		}
		if requestStatus != "pending" || caseStatus != "response_pending" {
			http.Error(w, "the response clock can only be corrected while a club response is pending", http.StatusConflict)
			return
		}
		startedAt = startedAt.In(s.LondonLoc)
		if !startedAt.Before(deliveredAt.In(s.LondonLoc)) {
			http.Error(w, "the corrected initial-email time must be earlier than the recorded delivery", http.StatusBadRequest)
			return
		}
		newReminderDueAt, newDueAt := responseDeliveryDeadlines(startedAt)
		madeOverdue := !newDueAt.After(correctedAt)

		_, err = tx.Exec(r.Context(), `UPDATE sanction_response_requests
			SET delivered_at=$2,reminder_due_at=$3,due_at=$4,window_corrected_at=$5,
			    window_corrected_by_admin_id=$6,window_correction_reason=$7
			WHERE id=$1 AND status='pending'`, requestID, startedAt, newReminderDueAt, newDueAt, correctedAt, *actor.ID, reason)
		if err == nil {
			revocationReason := "Response clock corrected: " + reason
			replacementKey := fmt.Sprintf("case:%d:response-reminder:%d:window:%d", caseID, reminderCorrespondenceID, startedAt.Unix())
			_, err = tx.Exec(r.Context(), `WITH stopped AS (
				UPDATE sanction_notification_outbox outbox
				SET processed_at=$2,revoked_at=$2,revoked_by_admin_id=$3,revocation_reason=$4
				FROM sanction_response_requests request
				WHERE request.id=$1 AND outbox.case_id=request.case_id
				  AND outbox.correspondence_revision_id=request.reminder_correspondence_revision_id
				  AND outbox.message_kind='response_reminder' AND outbox.processed_at IS NULL AND outbox.revoked_at IS NULL
				RETURNING outbox.case_id,outbox.decision_revision_id,outbox.policy_version_id,outbox.correspondence_revision_id,
				          outbox.recipient,outbox.subject,outbox.body,outbox.attachment_manifest
			)
			INSERT INTO sanction_notification_outbox(case_id,decision_revision_id,policy_version_id,correspondence_revision_id,
				message_kind,idempotency_key,recipient,subject,body,attachment_manifest,available_at)
			SELECT case_id,decision_revision_id,policy_version_id,correspondence_revision_id,'response_reminder',$5,
			       recipient,subject,body,attachment_manifest,$6 FROM stopped WHERE $7::boolean
			ON CONFLICT(idempotency_key) DO NOTHING`, requestID, correctedAt, *actor.ID, revocationReason, replacementKey, newReminderDueAt, !madeOverdue)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_case_access_tokens SET expires_at=$2,
				revoked_at=CASE WHEN $3::boolean THEN COALESCE(revoked_at,$4) ELSE revoked_at END
				WHERE id=$1`, tokenID, newDueAt, madeOverdue, correctedAt)
		}
		if err == nil && madeOverdue {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_response_requests SET status='expired',closed_at=$2 WHERE id=$1 AND status='pending'`, requestID, correctedAt)
		}
		if err == nil {
			nextStatus := "response_pending"
			if madeOverdue {
				nextStatus = "investigating"
			}
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status=$2,updated_at=$3 WHERE id=$1 AND status='response_pending'`, caseID, nextStatus, correctedAt)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,before_data,after_data)
				VALUES($1,'response_window_corrected','admin',$2,$3,$4,$5,
				jsonb_build_object('response_request_id',$6::bigint,'delivered_at',$7::timestamptz,'reminder_due_at',$8::timestamptz,'due_at',$9::timestamptz,'case_status',$10::text),
				jsonb_build_object('response_request_id',$6::bigint,'delivered_at',$11::timestamptz,'reminder_due_at',$12::timestamptz,'due_at',$13::timestamptz,'case_status',$14::text,'made_overdue',$15::boolean))`,
				caseID, *actor.ID, actor.Label, reason, actor.RequestID, requestID, deliveredAt, reminderDueAt, dueAt, caseStatus, startedAt, newReminderDueAt, newDueAt, map[bool]string{true: "investigating", false: "response_pending"}[madeOverdue], madeOverdue)
		}
		if err == nil && madeOverdue {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data)
				VALUES($1,'response_overdue','admin',$2,$3,'Corrected initial-email date places the seven-day response deadline in the past; investigation remains open and no adverse inference was made',$4,
				jsonb_build_object('response_request_id',$5::bigint,'due_at',$6::timestamptz))`, caseID, *actor.ID, actor.Label, actor.RequestID, requestID, newDueAt)
		}
		if err != nil {
			slog.Error("correct sanction response clock", "case_id", caseID, "response_request_id", requestID, "error", err)
			http.Error(w, "could not correct the response clock", http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "could not correct the response clock", http.StatusInternalServerError)
			return
		}

		message := "Response clock corrected; reminder and deadline updated."
		if madeOverdue {
			message = "Response clock corrected. The seven-day deadline has passed, so the case is now in Response overdue."
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s#response-window", caseID, urlQueryEscape(message)), http.StatusSeeOther)
	}
}
