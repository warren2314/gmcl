package httpserver

import (
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"cricket-ground-feedback/internal/sanctions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const responseLinkPlaceholder = "{{RESPONSE_LINK}}"

type responseDraftView struct {
	id, revision  int64
	kind          string
	subject, body string
	exists        bool
}

func (s *Server) handleAdminCaseOutcomeDraftSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := parsePositiveCaseID(chi.URLParam(r, "id"))
		if err != nil || r.ParseForm() != nil {
			http.Error(w, "invalid draft request", http.StatusBadRequest)
			return
		}
		audience := chi.URLParam(r, "audience")
		_, err = sanctions.NewService(s.DB).SaveOutcomeDraft(r.Context(), caseID, audience, r.FormValue("subject"), r.FormValue("body"), adminActor(r))
		if err != nil {
			http.Error(w, "draft was not saved: "+err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?draft_saved=%s", caseID, audience), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminCaseResponseDraftSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := parsePositiveCaseID(chi.URLParam(r, "id"))
		if err != nil || r.ParseForm() != nil {
			http.Error(w, "invalid draft request", http.StatusBadRequest)
			return
		}
		kind := chi.URLParam(r, "kind")
		if kind != "response_request" && kind != "response_reminder" {
			http.Error(w, "invalid response draft kind", http.StatusBadRequest)
			return
		}
		subject := strings.TrimSpace(r.FormValue("subject"))
		body := strings.TrimSpace(r.FormValue("body"))
		if subject == "" || len(subject) > 300 || body == "" || len(body) > 30000 {
			http.Error(w, "subject and body are required and must fit the correspondence limits", http.StatusBadRequest)
			return
		}
		if !validResponseDraftBody(body) {
			http.Error(w, "the body must contain exactly one "+responseLinkPlaceholder+" placeholder", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "administrator identity is required", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "draft was not saved", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var status, publicSummary, sourceType string
		var offendingClubID *int32
		err = tx.QueryRow(r.Context(), `SELECT status,club_id,COALESCE(public_summary,''),source_type FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).
			Scan(&status, &offendingClubID, &publicSummary, &sourceType)
		if err != nil || !map[string]bool{"submitted": true, "triage": true, "investigating": true}[status] {
			http.Error(w, "response drafts can only be saved before a decision is proposed", http.StatusConflict)
			return
		}
		allegedRule, ruleErr := loadCaseAllegedRule(r.Context(), tx, caseID)
		if ruleErr != nil {
			http.Error(w, "record the published rule alleged in this investigation before saving correspondence", http.StatusConflict)
			return
		}
		allegedRuleParagraph := allegedRuleCorrespondenceParagraph(allegedRule)
		if validationErr := validateResponseDraftContent(kind, body, publicSummary, allegedRuleParagraph); validationErr != nil {
			http.Error(w, validationErr.Error(), http.StatusBadRequest)
			return
		}
		privacyValues, queryErr := sanctions.CaseReporterIdentityValues(r.Context(), tx, caseID)
		if queryErr != nil {
			http.Error(w, "draft privacy could not be checked", http.StatusInternalServerError)
			return
		}
		reportingAliases, queryErr := sanctions.CaseReportingClubIdentityValues(r.Context(), tx, caseID, offendingClubID)
		if queryErr != nil {
			http.Error(w, "draft privacy could not be checked", http.StatusInternalServerError)
			return
		}
		privacyValues = append(privacyValues, reportingAliases...)
		if sanctions.ContainsPrivateIdentity(subject+"\n"+body, privacyValues...) {
			http.Error(w, "draft contains reporter or reporting-club identity", http.StatusBadRequest)
			return
		}
		var draftID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_correspondence_revisions(case_id,supersedes_id,message_kind,audience,revision,status,subject,body,created_by_admin_id)
			VALUES($1,(SELECT id FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind=$2 AND audience='offending_club' ORDER BY revision DESC,id DESC LIMIT 1),
			$2,'offending_club',(SELECT COALESCE(MAX(revision),0)+1 FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind=$2 AND audience='offending_club'),'draft',$3,$4,$5)
			RETURNING id`, caseID, kind, subject, body, actorIDAny(actor)).Scan(&draftID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data)
				VALUES($1,'correspondence_draft_saved','admin',$2,$3,$4,$5,jsonb_build_object('correspondence_revision_id',$6::bigint,'message_kind',$7::text))`,
				caseID, actorIDAny(actor), actor.Label, "Saved an append-only "+strings.ReplaceAll(kind, "_", " ")+" draft", actor.RequestID, draftID, kind)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "draft was not saved", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?draft_saved=%s#contact-club", caseID, kind), http.StatusSeeOther)
	}
}

// handleAdminCaseResponseDraftTest queues a clearly marked copy of the saved
// initial-email draft to the signed-in administrator. It creates no response
// token, contacts no club and does not change the case status.
func (s *Server) handleAdminCaseResponseDraftTest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := parsePositiveCaseID(chi.URLParam(r, "id"))
		if err != nil || r.ParseForm() != nil || r.FormValue("confirm") != "yes" {
			http.Error(w, "confirm the test email recipient", http.StatusBadRequest)
			return
		}
		if sanctionsEmailDisabled() {
			http.Error(w, "outbound sanctions email is disabled", http.StatusServiceUnavailable)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "administrator identity is required", http.StatusUnauthorized)
			return
		}
		var sourceType string
		if err = s.DB.QueryRow(r.Context(), `SELECT source_type FROM sanction_cases WHERE id=$1`, caseID).Scan(&sourceType); err != nil {
			http.NotFound(w, r)
			return
		}
		if sourceType == "ineligible_player" && !ineligibleOutboundEmailEnabled() {
			http.Error(w, "outbound ineligible-player email is disabled", http.StatusServiceUnavailable)
			return
		}
		var recipient, subject, body string
		if err = s.DB.QueryRow(r.Context(), `SELECT COALESCE(email,'') FROM admin_users WHERE id=$1 AND is_active`, *actor.ID).Scan(&recipient); err != nil || strings.TrimSpace(recipient) == "" {
			http.Error(w, "your active administrator account needs an email address", http.StatusConflict)
			return
		}
		parsed, parseErr := mail.ParseAddress(strings.TrimSpace(recipient))
		if parseErr != nil || parsed.Address == "" || !strings.EqualFold(parsed.Address, strings.TrimSpace(recipient)) {
			http.Error(w, "your administrator email address is invalid", http.StatusConflict)
			return
		}
		recipient = strings.ToLower(parsed.Address)
		if err = s.DB.QueryRow(r.Context(), `SELECT subject,body FROM sanction_correspondence_revisions
			WHERE case_id=$1 AND message_kind='response_request' AND audience='offending_club' AND status='draft'
			ORDER BY revision DESC,id DESC LIMIT 1`, caseID).Scan(&subject, &body); err != nil {
			http.Error(w, "save the initial email before sending a test copy", http.StatusConflict)
			return
		}
		testSubject := "[TEST ONLY - NO CLUB CONTACT] " + subject
		testBody := "TEST COPY FOR WORKFLOW CHECKING\nNo club was contacted and this message contains no live response link.\n\n" +
			strings.Replace(body, responseLinkPlaceholder, "[TEST ONLY - secure response link would be inserted here]", 1)
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "test email could not be queued", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		idempotencyKey := fmt.Sprintf("case:%d:response-request-test:admin:%d:%d", caseID, *actor.ID, time.Now().UnixNano())
		var outboxID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_notification_outbox(case_id,message_kind,idempotency_key,recipient,subject,body)
			VALUES($1,'response_request_test',$2,$3,$4,$5) RETURNING id`, caseID, idempotencyKey, recipient, testSubject, testBody).Scan(&outboxID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data)
				VALUES($1,'response_request_test_queued','admin',$2,$3,$4,$5,jsonb_build_object('outbox_id',$6::bigint,'recipient',$7::text))`,
				caseID, *actor.ID, actor.Label, "Queued a TEST ONLY copy of the saved initial email to the signed-in administrator; no club was contacted", actor.RequestID, outboxID, recipient)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "test email could not be queued", http.StatusInternalServerError)
			return
		}
		message := url.QueryEscape("Test email queued to " + recipient + ". No club was contacted and the case status was not changed.")
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s#contact-club", caseID, message), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminCaseResponseDraftPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := parsePositiveCaseID(chi.URLParam(r, "id"))
		kind := chi.URLParam(r, "kind")
		if err != nil || (kind != "response_request" && kind != "response_reminder") {
			http.Error(w, "invalid draft preview", http.StatusBadRequest)
			return
		}
		var subject, body string
		err = s.DB.QueryRow(r.Context(), `SELECT subject,body FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind=$2 AND audience='offending_club' AND status='draft' ORDER BY revision DESC,id DESC LIMIT 1`, caseID, kind).Scan(&subject, &body)
		if err != nil {
			http.Error(w, "save this draft before previewing it", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Correspondence draft preview")
		writeAdminNav(w, "", r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:800px"><div class="alert alert-warning">Preview only — no email has been queued. The secure response URL is generated only by the explicit send operation.</div><h1 class="h4">%s</h1><p><strong>Subject:</strong> %s</p><pre class="border rounded bg-light p-3" style="white-space:pre-wrap">%s</pre></main>`, escapeHTML(strings.ReplaceAll(kind, "_", " ")), escapeHTML(subject), escapeHTML(strings.Replace(body, responseLinkPlaceholder, "[secure response link generated on send]", 1)))
		pageFooter(w)
	}
}

func (s *Server) writeAdminOutcomeDraftForms(w http.ResponseWriter, r *http.Request, caseID int64, csrf string) {
	service := sanctions.NewService(s.DB)
	fmt.Fprint(w, `<section class="card mb-4"><div class="card-header">Emails awaiting independent approval</div><div class="card-body"><p class="small text-muted">These are the exact audience versions under review. Dave or Warren can approve another administrator's work; the approval button saves and locks them together but does not send anything. Denver issues them at final sign-off.</p>`)
	for _, audience := range []string{"offending_club", "reporting_club", "official"} {
		draft, err := service.OutcomeDraft(r.Context(), caseID, audience)
		if err != nil {
			fmt.Fprintf(w, `<div class="alert alert-warning">%s draft unavailable: %s</div>`, escapeHTML(strings.ReplaceAll(audience, "_", " ")), escapeHTML(err.Error()))
			continue
		}
		badge := `<span class="badge text-bg-warning">default — not saved</span>`
		if draft.Exists {
			badge = fmt.Sprintf(`<span class="badge text-bg-success">saved revision %d</span>`, draft.Revision)
		}
		readonly := ` readonly aria-readonly="true"`
		if !draft.Exists {
			badge = `<span class="badge text-bg-secondary">saved when approved</span>`
		}
		explanation := ""
		fmt.Fprintf(w, `<section class="border rounded p-3 mb-3"><div class="d-flex justify-content-between"><strong>%s</strong>%s</div>%s<label class="form-label mt-2">Subject</label><input class="form-control" value="%s"%s><label class="form-label mt-2">Body</label><textarea class="form-control font-monospace" rows="12"%s>%s</textarea><div class="mt-2"><a class="btn btn-sm btn-outline-secondary" target="_blank" rel="noopener" href="/admin/cases/%d/outcome-preview?audience=%s">Preview PDF</a></div></section>`, escapeHTML(strings.ReplaceAll(audience, "_", " ")), badge, explanation, escapeHTML(draft.Subject), readonly, readonly, escapeHTML(draft.Body), caseID, audience)
	}
	fmt.Fprint(w, `</div></section>`)
}

func outcomeDraftIsReadOnly(audience string) bool {
	return audience == "offending_club" || audience == "reporting_club" || audience == "official"
}

func defaultAdminResponseDraftViews(ref, teamName, publicSummary, allegedRuleParagraph string) map[string]responseDraftView {
	return map[string]responseDraftView{
		"response_request": {
			kind:    "response_request",
			subject: "Please respond: player eligibility query for " + teamName + " (" + ref + ")",
			body: "Dear Club Secretary,\n\nGMCL is reviewing a possible player eligibility issue involving " + teamName + ".\n\n" +
				"What we are asking about:\n\n" + publicSummary + "\n\n" +
				"Rule being checked:\n\n" + allegedRuleParagraph +
				"\n\nNo decision has been made. Before GMCL reaches a decision, please tell us:\n" +
				"- what happened and why the player appeared in this match;\n" +
				"- whether the club believes the player was eligible, and why; and\n" +
				"- any relevant permission, exemption or other circumstances we should consider.\n\n" +
				"You can also upload supporting evidence. Please respond using this secure link:\n" +
				responseLinkPlaceholder + "\n\nPlease reply within seven days of this email being delivered. " +
				"If no response is received, the investigation may continue using the available information, but no adverse decision is made automatically because the club did not reply.\n\n" +
				"Case reference: " + ref + "\n\nRegards,\nGreater Manchester Cricket League",
		},
		"response_reminder": {
			kind:    "response_reminder",
			subject: "Reminder: player eligibility response due (" + ref + ")",
			body: "Dear Club Secretary,\n\nThis is a reminder about GMCL's player eligibility query for " + teamName +
				". Your response for case " + ref + " is due in two days.\n\n" +
				"No decision has been made. Please use the secure link to explain what happened, why the player appeared and any permission, exemption or evidence GMCL should consider:\n" +
				responseLinkPlaceholder + "\n\nIf you have already responded, no further action is needed. No adverse decision is made automatically if the deadline passes.\n\n" +
				"Regards,\nGreater Manchester Cricket League",
		},
	}
}

func defaultAdminReportedCaseResponseDraftViews(ref, teamName, publicSummary, allegedRuleParagraph string) map[string]responseDraftView {
	return map[string]responseDraftView{
		"response_request": {
			kind:    "response_request",
			subject: "Please respond: GMCL case concerning " + teamName + " (" + ref + ")",
			body: "Dear Club Secretary,\n\nGMCL is reviewing a reported matter concerning " + teamName + ".\n\n" +
				"What we are asking about:\n\n" + publicSummary + "\n\n" +
				"Rule being checked:\n\n" + allegedRuleParagraph +
				"\n\nNo decision has been made. Before GMCL reaches a decision, please tell us:\n" +
				"- what happened and the club's response to the reported facts;\n" +
				"- whether the club believes the cited rule applies, and why; and\n" +
				"- any relevant context or supporting evidence we should consider.\n\n" +
				"Please respond using this secure link:\n" + responseLinkPlaceholder +
				"\n\nPlease reply within seven days of this email being delivered. If no response is received, the investigation may continue using the available information, but no adverse decision is made automatically because the club did not reply.\n\n" +
				"Case reference: " + ref + "\n\nRegards,\nGreater Manchester Cricket League",
		},
		"response_reminder": {
			kind:    "response_reminder",
			subject: "Reminder: GMCL case response due (" + ref + ")",
			body: "Dear Club Secretary,\n\nThis is a reminder about GMCL's reported case concerning " + teamName +
				". Your response for case " + ref + " is due in two days.\n\n" +
				"No decision has been made. Please use the secure link to respond to the reported facts and cited rule, and provide any context or evidence GMCL should consider:\n" + responseLinkPlaceholder +
				"\n\nIf you have already responded, no further action is needed. No adverse decision is made automatically if the deadline passes.\n\nRegards,\nGreater Manchester Cricket League",
		},
	}
}
func adminClubResponseStepsHTML() string {
	return `<section class="card mb-3 border-primary" id="contact-club"><div class="card-header"><strong>Next action: contact the club for its explanation</strong></div><div class="card-body"><p class="mb-3">No email is sent merely by opening this case. Complete these three steps in order:</p><ol class="mb-0"><li class="mb-2"><strong>Review and save the initial email.</strong> Select <strong>Save initial email</strong>; saving does not contact the club.</li><li class="mb-2"><strong>Review and save the reminder.</strong> Select <strong>Save reminder</strong>; it is prepared now but is not sent now.</li><li><strong>Select Send initial email to club.</strong> This is the only button in this section that contacts the club. The reminder is sent only later if it is still needed.</li></ol></div></section>`
}

func (s *Server) writeAdminResponseDraftForms(w http.ResponseWriter, r *http.Request, caseID int64, csrf, ref, teamName, publicSummary, sourceType string) {
	if responseSummaryIsPlaceholder(publicSummary) {
		fmt.Fprint(w, "<div class=\"alert alert-danger\"><strong>The allegation wording is not ready.</strong> Use Correct case summary to replace the placeholder with reviewed, reporter-safe facts before preparing or sending the response request.</div>")
		return
	}
	allegedRule, err := loadCaseAllegedRule(r.Context(), s.DB, caseID)
	if err != nil {
		fmt.Fprint(w, `<div class="card mb-3 border-warning"><div class="card-body"><strong>Correspondence is waiting for the alleged rule.</strong><p class="small text-muted mb-0">Record the published GMCL rule under investigation before creating the offending-club response request.</p></div></div>`)
		return
	}
	allegedRuleParagraph := allegedRuleCorrespondenceParagraph(allegedRule)
	defaults := defaultAdminResponseDraftViews(ref, teamName, publicSummary, allegedRuleParagraph)
	if sourceType != "ineligible_player" {
		defaults = defaultAdminReportedCaseResponseDraftViews(ref, teamName, publicSummary, allegedRuleParagraph)
	}
	fmt.Fprint(w, adminClubResponseStepsHTML())
	if saved := strings.TrimSpace(r.URL.Query().Get("draft_saved")); saved == "response_request" {
		fmt.Fprint(w, `<div class="alert alert-success"><strong>Initial email saved.</strong> Now save the reminder below.</div>`)
	} else if saved == "response_reminder" {
		fmt.Fprint(w, `<div class="alert alert-success"><strong>Reminder saved.</strong> If both steps show a green saved badge, the initial email can now be sent.</div>`)
	}
	savedKinds := map[string]bool{}
	for _, kind := range []string{"response_request", "response_reminder"} {
		view := defaults[kind]
		var stored responseDraftView
		err := s.DB.QueryRow(r.Context(), `SELECT id,revision,subject,body FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind=$2 AND audience='offending_club' AND status='draft' ORDER BY revision DESC,id DESC LIMIT 1`, caseID, kind).
			Scan(&stored.id, &stored.revision, &stored.subject, &stored.body)
		if err == nil && validateResponseDraftContent(kind, stored.body, publicSummary, allegedRuleParagraph) == nil {
			view = stored
			view.kind, view.exists = kind, true
			savedKinds[kind] = true
		}
		badge := `<span class="badge text-bg-warning">default — not saved</span>`
		if view.exists {
			badge = fmt.Sprintf(`<span class="badge text-bg-success">saved revision %d</span>`, view.revision)
		}
		preview := ""
		if view.exists {
			preview = fmt.Sprintf(`<a class="btn btn-sm btn-outline-secondary" target="_blank" rel="noopener" href="/admin/cases/%d/response-drafts/%s/preview">Preview saved draft</a>`, caseID, kind)
		}
		stepTitle := "1. Review and save the initial email"
		buttonText := "Save initial email"
		if kind == "response_reminder" {
			stepTitle = "2. Review and save the reminder"
			buttonText = "Save reminder"
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/response-drafts/%s" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header d-flex justify-content-between gap-2"><strong>%s</strong>%s</div><div class="card-body"><label class="form-label">Subject</label><input class="form-control" name="subject" maxlength="300" required value="%s"><label class="form-label mt-2">Body</label><textarea class="form-control font-monospace" name="body" rows="12" maxlength="30000" required>%s</textarea><div class="form-text">Keep exactly one %s placeholder. You may add other fixture dates and occurrence details around the recorded allegation. Saving this wording does not contact the club.</div></div><div class="card-footer d-flex gap-2"><button class="btn btn-primary">%s</button>%s</div></form>`, caseID, kind, escapeHTML(csrf), stepTitle, badge, escapeHTML(view.subject), escapeHTML(view.body), responseLinkPlaceholder, buttonText, preview)
		if kind == "response_request" && view.exists && !sanctionsEmailDisabled() && (sourceType != "ineligible_player" || ineligibleOutboundEmailEnabled()) {
			actor := adminActor(r)
			adminEmail := ""
			if actor.ID != nil {
				_ = s.DB.QueryRow(r.Context(), `SELECT COALESCE(email,'') FROM admin_users WHERE id=$1 AND is_active`, *actor.ID).Scan(&adminEmail)
			}
			if strings.TrimSpace(adminEmail) != "" {
				fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/response-drafts/response_request/test" class="alert alert-secondary"><input type="hidden" name="csrf_token" value="%s"><p class="mb-2"><strong>Optional safe test:</strong> send a clearly marked test copy to your administrator email <strong>%s</strong>. It contains no live response link, contacts no club and does not change the case.</p><label class="form-check mb-2"><input class="form-check-input" type="checkbox" name="confirm" value="yes" required> <span class="form-check-label">I understand this sends case wording to my own administrator email only.</span></label><button class="btn btn-sm btn-outline-secondary">Send TEST copy to me</button></form>`, caseID, escapeHTML(csrf), escapeHTML(adminEmail))
			}
		}
	}
	officialEmail := ""
	_ = s.DB.QueryRow(r.Context(), `SELECT contact.email FROM sanction_cases c
		JOIN sanction_club_contacts contact ON contact.club_id=c.club_id
			AND contact.contact_type='official_mailbox' AND contact.active AND contact.verified_at IS NOT NULL
		WHERE c.id=$1 ORDER BY contact.verified_at DESC NULLS LAST,contact.id DESC LIMIT 1`, caseID).Scan(&officialEmail)
	draftsReady := savedKinds["response_request"] && savedKinds["response_reminder"]
	disabled := sanctionsEmailDisabled() || (sourceType == "ineligible_player" && !ineligibleOutboundEmailEnabled()) || strings.TrimSpace(officialEmail) == "" || !draftsReady
	if disabled {
		reason := "Outbound sanctions email is disabled. Drafts remain editable and safe to preview."
		if !draftsReady {
			reason = "Save both the initial email and the reminder above. The send button is enabled only when both show a green saved badge."
		} else if strings.TrimSpace(officialEmail) == "" {
			reason = "No verified official mailbox is recorded for the offending club. Add or verify it in Recipients before sending."
		}
		fmt.Fprintf(w, `<div class="card mb-3 border-warning"><div class="card-header">3. Send the initial email</div><div class="card-body"><button class="btn btn-primary" disabled>Send initial email to club</button><div class="form-text">%s</div></div></div>`, escapeHTML(reason))
		return
	}
	fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/request-response" class="card mb-3 border-primary"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">3. Send the initial email</div><div class="card-body"><p><strong>To:</strong> %s</p><p>This sends only the initial email now. It securely locks both saved messages, creates the club's response link and queues the first email to the verified official mailbox.</p><p class="small text-muted">The seven-day response period starts when the email is delivered. The saved reminder is sent on day five only if the club has not responded.</p><button class="btn btn-primary">Send initial email to club</button></div></form>`, caseID, escapeHTML(csrf), escapeHTML(officialEmail))
}

func parsePositiveCaseID(value string) (int64, error) {
	var id int64
	_, err := fmt.Sscan(value, &id)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("invalid case id")
	}
	return id, nil
}

func validResponseDraftBody(body string) bool {
	return strings.Count(body, responseLinkPlaceholder) == 1
}

var responseDraftParagraphBreak = regexp.MustCompile(`\n[\t ]*\n+`)

func responseSummaryIsPlaceholder(value string) bool {
	normalized := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "."))
	return strings.EqualFold(normalized, "Report awaiting investigation")
}

func validateResponseDraftContent(kind, body, currentPublicAllegation, allegedRuleParagraph string) error {
	if !validResponseDraftBody(body) {
		return fmt.Errorf("the body must contain exactly one %s placeholder", responseLinkPlaceholder)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(body), " "))
	switch kind {
	case "response_request":
		if strings.TrimSpace(currentPublicAllegation) == "" {
			return fmt.Errorf("record the public allegation before saving a response request")
		}
		if responseSummaryIsPlaceholder(currentPublicAllegation) {
			return fmt.Errorf("replace the placeholder with reviewed, reporter-safe allegation facts before saving a response request")
		}
		if !containsCurrentResponseDraftAllegation(body, currentPublicAllegation) {
			return fmt.Errorf("the response request must contain the current public allegation once; extra fixture dates and occurrence details may be added around it")
		}
		if strings.TrimSpace(allegedRuleParagraph) != "" && !containsExactResponseDraftParagraph(body, allegedRuleParagraph) {
			return fmt.Errorf("the response request must contain the current alleged rule exactly as a standalone paragraph")
		}
		if !containsSevenDayWindow(normalized) {
			return fmt.Errorf("the response request must state that the secure response window lasts seven days")
		}
	case "response_reminder":
		if !containsTwoDayRemainingContext(normalized) {
			return fmt.Errorf("the response reminder must state that two days remain or that the response is due in two days")
		}
		if !containsNoAutomaticAdverseDecision(normalized) {
			return fmt.Errorf("the response reminder must explicitly state that no adverse decision is made automatically")
		}
	default:
		return fmt.Errorf("invalid response draft kind")
	}
	return nil
}

// containsCurrentResponseDraftAllegation keeps the reviewed public allegation
// in the email while allowing an investigator to add related occurrences (for
// example, two more fixture dates) to the same paragraph. Whitespace entered by
// the browser is immaterial, but the allegation's words must remain unchanged
// and must occur exactly once so a corrected case summary invalidates old
// drafts.
func containsCurrentResponseDraftAllegation(body, allegation string) bool {
	normalizedBody := strings.Join(strings.Fields(body), " ")
	normalizedAllegation := strings.Join(strings.Fields(allegation), " ")
	if normalizedAllegation == "" {
		return false
	}
	return strings.Count(normalizedBody, normalizedAllegation) == 1
}

func containsExactResponseDraftParagraph(body, allegation string) bool {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	want := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(allegation, "\r\n", "\n"), "\r", "\n"))
	matches := 0
	for _, paragraph := range responseDraftParagraphBreak.Split(body, -1) {
		if strings.TrimSpace(paragraph) == want {
			matches++
		}
	}
	return matches == 1
}

func containsSevenDayWindow(normalized string) bool {
	for _, phrase := range []string{"seven-day", "7-day", "seven days", "7 days"} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func containsTwoDayRemainingContext(normalized string) bool {
	for _, phrase := range []string{
		"due in two days", "due in 2 days", "due within two days", "due within 2 days",
		"two days remain", "2 days remain", "two days remaining", "2 days remaining",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func containsNoAutomaticAdverseDecision(normalized string) bool {
	for _, phrase := range []string{
		"no automatic adverse decision",
		"no adverse decision is made automatically",
		"no adverse decision will be made automatically",
		"no adverse decision follows automatically",
		"no adverse decision will follow automatically",
		"no adverse decision is taken automatically",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}
