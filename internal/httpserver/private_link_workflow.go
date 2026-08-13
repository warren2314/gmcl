package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const privateLinkTestResponse = "GMCL LINK TEST COMPLETE"

func (s *Server) handleAdminPrivateLinkTestCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sanctionsEmailDisabled() {
			http.Error(w, "sanctions email is disabled", http.StatusServiceUnavailable)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "administrator identity is required", http.StatusUnauthorized)
			return
		}
		var recipient string
		if err := s.DB.QueryRow(r.Context(), `SELECT COALESCE(email,'') FROM admin_users WHERE id=$1 AND is_active`, *actor.ID).Scan(&recipient); err != nil {
			http.Error(w, "active administrator email is required", http.StatusConflict)
			return
		}
		parsed, err := mail.ParseAddress(strings.TrimSpace(recipient))
		if err != nil || parsed.Address == "" || !strings.EqualFold(parsed.Address, strings.TrimSpace(recipient)) {
			http.Error(w, "administrator email is invalid", http.StatusConflict)
			return
		}
		recipient = strings.ToLower(parsed.Address)

		raw, hash, err := newPublicToken()
		if err != nil {
			http.Error(w, "could not create test token", http.StatusInternalServerError)
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "could not create test", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())

		var caseID, partyID, tokenID int64
		var reference string
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_cases(
			source_type,status,public_status,public_summary,private_summary,reporter_name,
			reporter_email,reporter_verified_at,assigned_admin_id,is_test)
			VALUES('manual','response_pending','unpublished',
			'Private end-to-end response-link test. This is not a real allegation.',
			'Synthetic case created solely to verify delivery, link opening and response submission.',
			$1,$2,now(),$3,TRUE) RETURNING id,reference`, actor.Label, recipient, *actor.ID).Scan(&caseID, &reference)
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_parties(case_id,party_type,name,email,relationship)
				VALUES($1,'league','GMCL administrator link test',$2,'representative') RETURNING id`, caseID, recipient).Scan(&partyID)
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_access_tokens(case_id,party_id,token_hash,purpose,expires_at)
				VALUES($1,$2,$3,'respond',now()) RETURNING id`, caseID, partyID, hash).Scan(&tokenID)
		}

		link := sanctionsBaseURL() + "/sanctions/case/respond?token=" + raw
		subject := "[PRIVATE LINK TEST] GMCL secure response journey (" + reference + ")"
		body := "Dear GMCL Administrator,\n\nThis is a private end-to-end test. No club has been contacted and this case cannot be published.\n\nWhat is being tested:\n- email delivery;\n- the secure response link;\n- token validation; and\n- response submission back into the case.\n\nUse the secure link below, enter exactly " + privateLinkTestResponse + ", then submit:\n" + link + "\n\nCase reference: " + reference + "\n\nRegards,\nGreater Manchester Cricket League"
		reminderSubject := "[PRIVATE LINK TEST] Reminder for " + reference
		reminderBody := "This is a private test reminder. No club has been contacted.\n\n" + link
		recipients, _ := json.Marshal([]string{recipient})
		var requestCorrespondenceID, reminderCorrespondenceID int64
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_correspondence_revisions(
				case_id,message_kind,audience,revision,status,recipients,subject,body,created_by_admin_id)
				VALUES($1,'response_request','offending_club',1,'queued',$2,$3,$4,$5) RETURNING id`,
				caseID, recipients, subject, body, *actor.ID).Scan(&requestCorrespondenceID)
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_correspondence_revisions(
				case_id,message_kind,audience,revision,status,recipients,subject,body,created_by_admin_id)
				VALUES($1,'response_reminder','offending_club',1,'queued',$2,$3,$4,$5) RETURNING id`,
				caseID, recipients, reminderSubject, reminderBody, *actor.ID).Scan(&reminderCorrespondenceID)
		}
		var policyID *int64
		_ = tx.QueryRow(r.Context(), `SELECT id FROM sanction_notification_policy_versions WHERE active AND source_type='*' AND event_type='decision_published' ORDER BY version DESC LIMIT 1`).Scan(&policyID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_notification_outbox(
				case_id,policy_version_id,correspondence_revision_id,message_kind,idempotency_key,recipient,subject,body)
				VALUES($1,$2,$3,'response_request',$4,$5,$6,$7)`, caseID, policyID, requestCorrespondenceID,
				fmt.Sprintf("private-link-test:%d:%d", caseID, requestCorrespondenceID), recipient, subject, body)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_response_requests(
				case_id,party_id,access_token_id,correspondence_revision_id,reminder_correspondence_revision_id,status,allegation_snapshot)
				VALUES($1,$2,$3,$4,$5,'queued',$6)`, caseID, partyID, tokenID, requestCorrespondenceID,
				reminderCorrespondenceID, "PRIVATE TEST — submit: "+privateLinkTestResponse)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(
				case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data)
				VALUES($1,'private_link_test_created','admin',$2,$3,$4,$5,
				jsonb_build_object('recipient',$6::text,'publicly_visible',false))`, caseID, *actor.ID, actor.Label,
				"Created a synthetic end-to-end response-link test; no club was contacted", actor.RequestID, recipient)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not create private link test", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/link-tests/%d", caseID), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPrivateLinkTestStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID < 1 {
			http.NotFound(w, r)
			return
		}
		var reference, caseStatus, requestStatus, recipient, deliveryStatus, deliveryError, response string
		err = s.DB.QueryRow(r.Context(), `SELECT c.reference,c.status,request.status,outbox.recipient,
			COALESCE(attempt.status,CASE WHEN outbox.processed_at IS NULL THEN 'queued' ELSE 'processed' END),
			COALESCE(attempt.error_message,''),COALESCE((SELECT event.reason FROM sanction_case_events event
				WHERE event.case_id=c.id AND event.event_type='party_response' ORDER BY event.id DESC LIMIT 1),'')
			FROM sanction_cases c
			JOIN sanction_response_requests request ON request.case_id=c.id
			JOIN sanction_notification_outbox outbox ON outbox.case_id=c.id AND outbox.message_kind='response_request'
			LEFT JOIN LATERAL (SELECT status,error_message FROM sanction_notification_attempts
				WHERE outbox_id=outbox.id ORDER BY attempt_number DESC,id DESC LIMIT 1) attempt ON TRUE
			WHERE c.id=$1 AND c.is_test`, caseID).Scan(&reference, &caseStatus, &requestStatus, &recipient, &deliveryStatus, &deliveryError, &response)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		complete := requestStatus == "responded" && response == privateLinkTestResponse
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Private link test "+reference)
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:800px"><a href="/admin/cases" class="btn btn-sm btn-outline-secondary mb-3">Back to cases</a><div class="alert alert-warning"><strong>Private synthetic case.</strong> It is hidden from normal case lists, cannot be published and has contacted no club.</div><h1 class="h3">Secure-link test %s</h1>`, escapeHTML(reference))
		if complete {
			fmt.Fprint(w, `<div class="alert alert-success"><strong>End-to-end test passed.</strong> The email was processed, its one-time link opened successfully and the exact test response was stored.</div>`)
		} else {
			fmt.Fprintf(w, `<div class="card"><div class="card-body"><p><strong>Sent only to:</strong> %s</p><p><strong>Delivery:</strong> %s</p><p><strong>Link state:</strong> %s</p><p>Open the email, select <strong>Respond securely</strong>, enter <code>%s</code>, and submit it. Then refresh this page.</p>%s</div></div>`, escapeHTML(recipient), escapeHTML(deliveryStatus), escapeHTML(requestStatus), privateLinkTestResponse, testDeliveryErrorHTML(deliveryError))
		}
		fmt.Fprintf(w, `<p class="mt-3 small text-muted">Internal case state: %s. This page contains no reusable token.</p></main>`, escapeHTML(caseStatus))
		pageFooter(w)
	}
}

func testDeliveryErrorHTML(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	return `<div class="alert alert-danger"><strong>Delivery error:</strong> ` + escapeHTML(message) + `</div>`
}
