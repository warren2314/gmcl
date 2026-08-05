package httpserver

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

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
		var status, publicSummary string
		var offendingClubID *int32
		err = tx.QueryRow(r.Context(), `SELECT status,club_id,COALESCE(public_summary,'') FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).
			Scan(&status, &offendingClubID, &publicSummary)
		if err != nil || !map[string]bool{"submitted": true, "triage": true, "investigating": true}[status] {
			http.Error(w, "response drafts can only be saved before a decision is proposed", http.StatusConflict)
			return
		}
		if validationErr := validateResponseDraftContent(kind, body, publicSummary); validationErr != nil {
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
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?draft_saved=%s", caseID, kind), http.StatusSeeOther)
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
	fmt.Fprint(w, `<section class="card mb-4"><div class="card-header">Outcome correspondence drafts</div><div class="card-body"><p class="small text-muted">Save each required audience version before independent approval. Every save appends a revision; preview and save do not send email.</p>`)
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
		explanation := `<div class="alert alert-info py-2 mt-2 mb-0">This audience-safe version is generated from the proposed subject, findings, rule determination, effects and appeal instructions. Edit those decision fields before proposing; the rendered correspondence is read-only so it cannot contradict the decision or introduce private case material.</div>`
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/outcome-drafts/%s" class="border rounded p-3 mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="d-flex justify-content-between"><strong>%s</strong>%s</div>%s<label class="form-label mt-2">Subject</label><input class="form-control" name="subject" maxlength="300" required value="%s"%s><label class="form-label mt-2">Body</label><textarea class="form-control font-monospace" name="body" rows="12" maxlength="30000" required%s>%s</textarea><div class="d-flex gap-2 mt-2"><button class="btn btn-sm btn-primary">Save new immutable revision</button><a class="btn btn-sm btn-outline-secondary" target="_blank" rel="noopener" href="/admin/cases/%d/outcome-preview?audience=%s">Preview PDF</a></div></form>`, caseID, audience, escapeHTML(csrf), escapeHTML(strings.ReplaceAll(audience, "_", " ")), badge, explanation, escapeHTML(draft.Subject), readonly, readonly, escapeHTML(draft.Body), caseID, audience)
	}
	fmt.Fprint(w, `</div></section>`)
}

func outcomeDraftIsReadOnly(audience string) bool {
	return audience == "offending_club" || audience == "reporting_club" || audience == "official"
}

func (s *Server) writeAdminResponseDraftForms(w http.ResponseWriter, r *http.Request, caseID int64, csrf, ref, teamName, publicSummary, sourceType string) {
	defaults := map[string]responseDraftView{
		"response_request":  {kind: "response_request", subject: "Response requested for GMCL case " + ref, body: "Dear Club Secretary,\n\nThe GMCL requests an official response from " + teamName + " concerning the following allegation:\n\n" + publicSummary + "\n\nUse the secure link below to respond and upload any supporting evidence:\n" + responseLinkPlaceholder + "\n\nCase reference: " + ref + "\nThis secure link expires in seven days.\n\nRegards,\nGreater Manchester Cricket League"},
		"response_reminder": {kind: "response_reminder", subject: "Reminder: Response requested for GMCL case " + ref, body: "Dear Club Secretary,\n\nThis is the single reminder that the response for GMCL case " + ref + " is due in two days. No adverse decision is made automatically if the deadline passes.\n\n" + responseLinkPlaceholder + "\n\nRegards,\nGreater Manchester Cricket League"},
	}
	for _, kind := range []string{"response_request", "response_reminder"} {
		view := defaults[kind]
		var stored responseDraftView
		err := s.DB.QueryRow(r.Context(), `SELECT id,revision,subject,body FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind=$2 AND audience='offending_club' AND status='draft' ORDER BY revision DESC,id DESC LIMIT 1`, caseID, kind).
			Scan(&stored.id, &stored.revision, &stored.subject, &stored.body)
		if err == nil {
			view = stored
			view.kind, view.exists = kind, true
		}
		badge := `<span class="badge text-bg-warning">default — not saved</span>`
		if view.exists {
			badge = fmt.Sprintf(`<span class="badge text-bg-success">saved revision %d</span>`, view.revision)
		}
		preview := ""
		if view.exists {
			preview = fmt.Sprintf(`<a class="btn btn-sm btn-outline-secondary" target="_blank" rel="noopener" href="/admin/cases/%d/response-drafts/%s/preview">Preview saved draft</a>`, caseID, kind)
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/response-drafts/%s" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header d-flex justify-content-between"><span>%s draft</span>%s</div><div class="card-body"><label class="form-label">Subject</label><input class="form-control" name="subject" maxlength="300" required value="%s"><label class="form-label mt-2">Body</label><textarea class="form-control font-monospace" name="body" rows="9" maxlength="30000" required>%s</textarea><div class="form-text">Keep exactly one %s placeholder. Saving does not contact the club.</div></div><div class="card-footer d-flex gap-2"><button class="btn btn-outline-primary">Save new immutable revision</button>%s</div></form>`, caseID, kind, escapeHTML(csrf), escapeHTML(strings.ReplaceAll(kind, "_", " ")), badge, escapeHTML(view.subject), escapeHTML(view.body), responseLinkPlaceholder, preview)
	}
	disabled := sanctionsEmailDisabled() || (sourceType == "ineligible_player" && !ineligibleOutboundEmailEnabled())
	if disabled {
		fmt.Fprint(w, `<div class="card mb-3"><div class="card-body"><button class="btn btn-outline-danger" disabled>Send saved response request</button><div class="form-text">Outbound sanctions email is disabled. Drafts remain editable and safe to preview.</div></div></div>`)
		return
	}
	fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/request-response" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><p>Lock the latest saved request and reminder drafts, then queue only the initial notice to the verified offending-club mailbox. The seven-day response window and day-five reminder start only after the initial notice is delivered successfully.</p><button class="btn btn-outline-danger">Lock drafts and queue initial notice</button></div></form>`, caseID, escapeHTML(csrf))
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

func validateResponseDraftContent(kind, body, currentPublicAllegation string) error {
	if !validResponseDraftBody(body) {
		return fmt.Errorf("the body must contain exactly one %s placeholder", responseLinkPlaceholder)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(body), " "))
	switch kind {
	case "response_request":
		if strings.TrimSpace(currentPublicAllegation) == "" {
			return fmt.Errorf("record the public allegation before saving a response request")
		}
		if !containsExactResponseDraftParagraph(body, currentPublicAllegation) {
			return fmt.Errorf("the response request must contain the current public allegation exactly as a standalone paragraph")
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
