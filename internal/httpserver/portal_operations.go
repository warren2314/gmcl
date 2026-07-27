package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/portal"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type portalOperationalField struct {
	Name        string
	Label       string
	Placeholder string
	Kind        string
	Required    bool
}

type portalOperationalModule struct {
	Path        string
	Title       string
	Eyebrow     string
	Description string
	RequestType portal.ModuleRequestType
	Fields      []portalOperationalField
	SafetyNote  string
}

var (
	portalOperationalModuleCorrection = portalOperationalModule{
		Path:        "/portal/club-profile",
		Title:       "Data correction requests",
		Eyebrow:     "Club self-service",
		Description: "Request a correction without overwriting the existing GMCL source record.",
		RequestType: portal.ModuleRequestCorrection,
		Fields: []portalOperationalField{
			{Name: "record_type", Label: "Record type", Placeholder: "Contact, team, report, sanction or other", Required: true},
			{Name: "record_reference", Label: "Source record reference", Placeholder: "Existing reference or URL", Required: true},
			{Name: "requested_change", Label: "Requested correction", Kind: "textarea", Required: true},
		},
		SafetyNote: "Submitted official records remain unchanged. GMCL review creates a new correction decision and audit record.",
	}
	portalOperationalModuleStarred = portalOperationalModule{
		Path:        "/portal/starred-players",
		Title:       "Starred-player reviews",
		Eyebrow:     "Versioned compliance",
		Description: "Submit a potential correction, exemption or review against the published season/rule release.",
		RequestType: portal.ModuleRequestStarred,
		Fields: []portalOperationalField{
			{Name: "player_name", Label: "Player name", Required: true},
			{Name: "season", Label: "Season", Placeholder: "2026", Required: true},
			{Name: "rule_release", Label: "Published rule/list release", Placeholder: "Rule 3.5 / published list date", Required: true},
			{Name: "requested_outcome", Label: "Requested outcome", Kind: "textarea", Required: true},
		},
		SafetyNote: "Every automated or club-submitted finding remains a potential issue until a named GMCL reviewer decides it.",
	}
	portalOperationalModuleJunior = portalOperationalModule{
		Path:        "/portal/junior-administration",
		Title:       "Junior administration",
		Eyebrow:     "Verified adult recipients only",
		Description: "Send or respond to ordinary junior competition administration through verified adult club roles.",
		RequestType: portal.ModuleRequestJunior,
		Fields: []portalOperationalField{
			{Name: "adult_recipient_role", Label: "Adult recipient role", Placeholder: "Junior secretary or club secretary", Required: true},
			{Name: "competition_or_team", Label: "Competition/team reference", Required: true},
			{Name: "requested_action", Label: "Administrative request or acknowledgement", Kind: "textarea", Required: true},
		},
		SafetyNote: "Do not enter child contact details, medical information or safeguarding content. Safeguarding uses a separate restricted route and is not enabled here.",
	}
	portalOperationalModuleIdentity = portalOperationalModule{
		Path:        "/portal/player-identity",
		Title:       "Player identity reconciliation",
		Eyebrow:     "Externally referenced",
		Description: "Record an ambiguity or mapping request against an available external player reference.",
		RequestType: portal.ModuleRequestIdentity,
		Fields: []portalOperationalField{
			{Name: "player_name", Label: "Player name", Required: true},
			{Name: "play_cricket_member_id", Label: "Play-Cricket member ID", Placeholder: "If available"},
			{Name: "identity_issue", Label: "Reconciliation issue", Kind: "textarea", Required: true},
		},
		SafetyNote: "No photographs, biometric matching or bulk player data are collected. API photo access and redistribution remain unconfirmed.",
	}
	portalOperationalModuleRegistration = portalOperationalModule{
		Path:        "/portal/registration",
		Title:       "Registration handoff",
		Eyebrow:     "Guided external process",
		Description: "Create a tracked handoff while Play-Cricket and the published GMCL forms remain authoritative.",
		RequestType: portal.ModuleRequestRegistration,
		Fields: []portalOperationalField{
			{Name: "applicant_reference", Label: "Applicant/player reference", Required: true},
			{Name: "registration_route", Label: "Registration route", Placeholder: "New player, transfer, overseas or other", Required: true},
			{Name: "former_club_email_status", Label: "Former-club direct email status", Placeholder: "Required / sent / not applicable", Required: true},
			{Name: "external_form_reference", Label: "External form or Play-Cricket reference"},
		},
		SafetyNote: "The portal does not write to Play-Cricket. Direct former-club email remains required until Rule 3.1 is amended.",
	}
)

func (s *Server) handlePortalMessagesGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		cases, err := s.PortalStore.ListMessageCases(ctx, principal)
		if errors.Is(err, portal.ErrForbidden) {
			renderPortalModuleDisabled(w, r, principal, "Secure communications")
			return
		}
		if err != nil {
			http.Error(w, "could not load portal communications", http.StatusInternalServerError)
			return
		}
		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Communications")
		writePortalNav(w, csrf, principal, r.URL.Path)
		fmt.Fprint(w, `<main id="main-content" tabindex="-1" class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-wrap justify-content-between gap-3 align-items-start mb-4"><div>
<p class="text-uppercase text-muted small mb-1">Official communication in parallel with email</p>
<h1 class="h2">Cases and messages</h1>
<p class="text-muted">Track correspondence, replies, acknowledgements, owners and deadlines. Email remains the official record during the pilot.</p></div></div>`)
		renderPortalOperationStatus(w, r.URL.Query().Get("status"))
		fmt.Fprint(w, `<div class="row g-4"><div class="col-xl-5"><section class="card shadow-sm"><div class="card-header"><strong>Start a case</strong></div><div class="card-body">
<form method="post" action="/portal/messages" class="row g-3">
<input type="hidden" name="csrf_token" value="`+escapeHTML(csrf)+`">
<div class="col-12"><label class="form-label" for="message-category">Category</label><select class="form-select" id="message-category" name="category" required>
<option value="general">General</option><option value="compliance_sanctions">Compliance and sanctions</option>
<option value="fixtures">Fixtures</option><option value="registration">Registration</option>
<option value="starred_players">Starred players</option><option value="junior_administration">Junior administration</option>
</select></div>
<div class="col-12"><label class="form-label" for="message-subject">Subject</label><input class="form-control" id="message-subject" name="subject" maxlength="200" required></div>
<div class="col-12"><label class="form-label" for="message-priority">Priority</label><select class="form-select" id="message-priority" name="priority"><option value="normal">Normal</option><option value="urgent">Urgent</option></select></div>
<div class="col-12"><label class="form-label" for="message-body">Message</label><textarea class="form-control" id="message-body" name="body" rows="6" maxlength="10000" required></textarea></div>
<div class="col-12"><button class="btn btn-primary" type="submit">Send to GMCL</button></div>
</form></div></section></div><div class="col-xl-7"><section class="card shadow-sm"><div class="card-header"><strong>Club case history</strong></div><div class="table-responsive"><table class="table table-striped align-middle mb-0"><caption class="visually-hidden">Club communications</caption><thead><tr><th scope="col">Case</th><th scope="col">Category</th><th scope="col">Status</th><th scope="col">Updated</th></tr></thead><tbody>`)
		if len(cases) == 0 {
			fmt.Fprint(w, `<tr><td colspan="4" class="text-muted">No communication cases exist for this club.</td></tr>`)
		}
		for _, item := range cases {
			fmt.Fprintf(w, `<tr><td><a href="/portal/messages/%s">%s</a>%s<div class="small text-muted">%d message(s)</div></td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				item.ID,
				escapeHTML(item.Subject),
				portalPriorityBadge(item.Priority),
				item.MessageCount,
				escapeHTML(humanPortalMessageCategory(item.Category)),
				portalCaseStatusBadge(item.Status),
				portalLocalTime(item.UpdatedAt, s.LondonLoc),
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section></div></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) handlePortalMessageCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		category, ok := portal.ParseMessageCategory(r.FormValue("category"))
		if !ok {
			http.Error(w, "invalid communication category", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		caseID, messageID, err := s.PortalStore.CreateMessageCase(
			ctx,
			principal,
			portal.CreateMessageCaseRequest{
				Category: category,
				Subject:  r.FormValue("subject"),
				Body:     r.FormValue("body"),
				Priority: r.FormValue("priority"),
			},
			requestID(r),
		)
		if errors.Is(err, portal.ErrForbidden) {
			http.Error(w, "communication access denied", http.StatusForbidden)
			return
		}
		if err != nil {
			http.Error(w, "could not create communication case", http.StatusBadRequest)
			return
		}
		emailStatus := s.sendPortalCaseCopy(
			ctx,
			messageID,
			portalGMCLCommsEmail(),
			"New GMCL portal case: "+strings.TrimSpace(r.FormValue("subject")),
			fmt.Sprintf(
				"Club portal case %s\n\nCategory: %s\nPriority: %s\n\n%s\n\nReview: %s/admin/portal/cases/%s",
				caseID,
				category,
				strings.TrimSpace(r.FormValue("priority")),
				strings.TrimSpace(r.FormValue("body")),
				publicBaseURL(r),
				caseID,
			),
		)
		http.Redirect(
			w,
			r,
			"/portal/messages/"+caseID.String()+"?status="+emailStatus,
			http.StatusSeeOther,
		)
	}
}

func (s *Server) handlePortalMessageCaseGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "caseID")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		detail, err := s.PortalStore.LoadMessageCase(ctx, principal, caseID)
		if errors.Is(err, portal.ErrNotFound) || errors.Is(err, portal.ErrForbidden) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "could not load communication case", http.StatusInternalServerError)
			return
		}
		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, detail.Subject)
		writePortalNav(w, csrf, principal, "/portal/messages")
		fmt.Fprintf(w, `<main id="main-content" tabindex="-1" class="container pb-5">
<div class="mb-4"><a href="/portal/messages">&larr; All cases</a><p class="text-uppercase text-muted small mt-3 mb-1">%s</p><h1 class="h2">%s</h1>
<div class="d-flex flex-wrap gap-2">%s %s %s</div></div>`,
			escapeHTML(humanPortalMessageCategory(detail.Category)),
			escapeHTML(detail.Subject),
			portalCaseStatusBadge(detail.Status),
			portalPriorityBadge(detail.Priority),
			func() string {
				if detail.AcknowledgedAt != nil {
					return `<span class="badge text-bg-success">Acknowledged</span>`
				}
				return `<span class="badge text-bg-secondary">Not acknowledged</span>`
			}(),
		)
		renderPortalOperationStatus(w, r.URL.Query().Get("status"))
		fmt.Fprint(w, `<section class="card shadow-sm mb-4"><div class="card-header"><strong>Club-visible timeline</strong></div><div class="card-body">`)
		for _, message := range detail.Messages {
			style := "border-start border-4 border-secondary ps-3 mb-4"
			if message.AuthorKind == "gmcl_admin" {
				style = "border-start border-4 border-primary ps-3 mb-4"
			}
			fmt.Fprintf(w, `<article class="%s"><div class="d-flex justify-content-between gap-3"><strong>%s</strong><time class="small text-muted">%s</time></div><div class="mt-2" style="white-space:pre-wrap">%s</div><div class="small text-muted mt-2">Email copy: %s</div></article>`,
				style,
				escapeHTML(message.AuthorLabel),
				portalLocalTime(message.CreatedAt, s.LondonLoc),
				escapeHTML(message.Body),
				escapeHTML(message.EmailStatus),
			)
		}
		fmt.Fprint(w, `</div></section><div class="row g-4"><div class="col-lg-8"><section class="card shadow-sm"><div class="card-header"><strong>Reply</strong></div><div class="card-body">`)
		if detail.Status == "closed" {
			fmt.Fprint(w, `<p class="text-muted mb-0">This case is closed. Contact GMCL to request reopening.</p>`)
		} else if principal.Assignment != nil && portal.Authorize(
			*principal.Assignment,
			portal.PermissionMessagesReply,
			principal.Assignment.Scope,
			time.Now(),
		) {
			fmt.Fprintf(w, `<form method="post" action="/portal/messages/%s/reply"><input type="hidden" name="csrf_token" value="%s"><label class="form-label" for="case-reply">Message</label><textarea class="form-control" id="case-reply" name="body" rows="5" maxlength="10000" required></textarea><button class="btn btn-primary mt-3" type="submit">Send reply</button></form>`,
				detail.ID,
				escapeHTML(csrf),
			)
		} else {
			fmt.Fprint(w, `<p class="text-muted mb-0">Your current role can view this case but cannot reply.</p>`)
		}
		fmt.Fprintf(w, `</div></section></div><div class="col-lg-4"><section class="card shadow-sm"><div class="card-header"><strong>Case actions</strong></div><div class="card-body d-grid gap-3">
<form method="post" action="/portal/messages/%s/acknowledge"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-outline-primary w-100" type="submit">Acknowledge</button></form>
<form method="post" action="/portal/messages/%s/watch"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="watching" value="%t"><button class="btn btn-outline-secondary w-100" type="submit">%s</button></form>
</div></section></div></div></main>`,
			detail.ID,
			escapeHTML(csrf),
			detail.ID,
			escapeHTML(csrf),
			!detail.Watching,
			func() string {
				if detail.Watching {
					return "Stop watching"
				}
				return "Watch case"
			}(),
		)
		pageFooter(w)
	}
}

func (s *Server) handlePortalMessageReply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "caseID")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		messageID, err := s.PortalStore.ReplyMessageCase(
			ctx,
			principal,
			caseID,
			r.FormValue("body"),
			requestID(r),
		)
		if err != nil {
			http.Error(w, "could not send case reply", http.StatusBadRequest)
			return
		}
		status := s.sendPortalCaseCopy(
			ctx,
			messageID,
			portalGMCLCommsEmail(),
			"GMCL portal case reply: "+caseID.String(),
			fmt.Sprintf(
				"Club replied to portal case %s.\n\n%s\n\nReview: %s/admin/portal/cases/%s",
				caseID,
				strings.TrimSpace(r.FormValue("body")),
				publicBaseURL(r),
				caseID,
			),
		)
		http.Redirect(
			w,
			r,
			"/portal/messages/"+caseID.String()+"?status="+status,
			http.StatusSeeOther,
		)
	}
}

func (s *Server) handlePortalMessageAcknowledge() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "caseID")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.PortalStore.AcknowledgeMessageCase(
			ctx,
			principal,
			caseID,
			requestID(r),
		); err != nil {
			http.Error(w, "could not acknowledge case", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/portal/messages/"+caseID.String()+"?status=acknowledged", http.StatusSeeOther)
	}
}

func (s *Server) handlePortalMessageWatch() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "caseID")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		watching, err := strconv.ParseBool(strings.TrimSpace(r.FormValue("watching")))
		if err != nil {
			http.Error(w, "invalid watch state", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.PortalStore.SetCaseWatching(ctx, principal, caseID, watching); err != nil {
			http.Error(w, "could not update case watcher", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/portal/messages/"+caseID.String(), http.StatusSeeOther)
	}
}

func (s *Server) handlePortalClubProfileGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		contacts, err := s.PortalStore.ListClubContacts(ctx, principal)
		if errors.Is(err, portal.ErrForbidden) {
			renderPortalModuleDisabled(w, r, principal, "Club self-service")
			return
		}
		if err != nil {
			http.Error(w, "could not load club contacts", http.StatusInternalServerError)
			return
		}
		requests, err := s.PortalStore.ListOperationalRequests(
			ctx,
			principal,
			portal.ModuleRequestCorrection,
		)
		if err != nil && !errors.Is(err, portal.ErrForbidden) {
			http.Error(w, "could not load correction requests", http.StatusInternalServerError)
			return
		}
		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Club profile")
		writePortalNav(w, csrf, principal, r.URL.Path)
		fmt.Fprint(w, `<main id="main-content" tabindex="-1" class="container-fluid px-3 px-lg-4 pb-5">
<p class="text-uppercase text-muted small mb-1">Club self-service</p><h1 class="h2">Contacts and corrections</h1>
<p class="text-muted">Submit versioned contact details and source-record corrections for GMCL review. Existing official data is never overwritten by submission.</p>`)
		renderPortalOperationStatus(w, r.URL.Query().Get("status"))
		fmt.Fprint(w, `<div class="row g-4"><div class="col-xl-5"><section class="card shadow-sm"><div class="card-header"><strong>Submit club contact</strong></div><div class="card-body"><form method="post" action="/portal/club-profile/contacts" class="row g-3">
<input type="hidden" name="csrf_token" value="`+escapeHTML(csrf)+`">
<div class="col-12"><label class="form-label" for="contact-role">Role</label><select class="form-select" id="contact-role" name="role_key"><option value="primary_contact">Primary contact</option><option value="secretary">Secretary</option><option value="play_cricket_admin">Play-Cricket administrator</option><option value="junior_contact">Junior contact</option><option value="fixtures_contact">Fixtures contact</option><option value="registration_contact">Registration contact</option></select></div>
<div class="col-12"><label class="form-label" for="contact-name">Named adult</label><input class="form-control" id="contact-name" name="display_name" maxlength="200" required></div>
<div class="col-12"><label class="form-label" for="contact-email">Email</label><input class="form-control" id="contact-email" name="email" type="email" maxlength="320" required></div>
<div class="col-12"><label class="form-label" for="contact-phone">Phone (optional)</label><input class="form-control" id="contact-phone" name="phone" maxlength="50"></div>
<div class="col-12"><label class="form-label" for="contact-evidence">Evidence reference</label><input class="form-control" id="contact-evidence" name="evidence_reference" maxlength="500" required><div class="form-text">Reference the source and review date; do not upload copied personal documents.</div></div>
<div class="col-12"><button class="btn btn-primary" type="submit">Submit for verification</button></div></form></div></section></div>
<div class="col-xl-7"><section class="card shadow-sm"><div class="card-header"><strong>Versioned contacts</strong></div><div class="table-responsive"><table class="table table-striped mb-0"><thead><tr><th>Role</th><th>Name</th><th>Email</th><th>Status</th></tr></thead><tbody>`)
		if len(contacts) == 0 {
			fmt.Fprint(w, `<tr><td colspan="4" class="text-muted">No portal contact submissions exist.</td></tr>`)
		}
		for _, contact := range contacts {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				escapeHTML(humanPortalContactRole(contact.RoleKey)),
				escapeHTML(contact.DisplayName),
				escapeHTML(contact.Email),
				escapeHTML(contact.Status),
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section></div></div>`)
		renderPortalOperationalRequestSection(w, csrf, portalOperationalModuleCorrection, requests, s.LondonLoc)
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func (s *Server) handlePortalClubContactCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		_, err := s.PortalStore.CreateClubContact(
			ctx,
			principal,
			portal.CreateClubContactRequest{
				RoleKey:           r.FormValue("role_key"),
				DisplayName:       r.FormValue("display_name"),
				Email:             r.FormValue("email"),
				Phone:             r.FormValue("phone"),
				EvidenceReference: r.FormValue("evidence_reference"),
			},
			requestID(r),
		)
		if err != nil {
			http.Error(w, "could not submit club contact", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/portal/club-profile?status=contact-submitted", http.StatusSeeOther)
	}
}

func (s *Server) handlePortalOperationalModuleGet(
	definition portalOperationalModule,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		requests, err := s.PortalStore.ListOperationalRequests(
			ctx,
			principal,
			definition.RequestType,
		)
		if errors.Is(err, portal.ErrForbidden) {
			renderPortalModuleDisabled(w, r, principal, definition.Title)
			return
		}
		if err != nil {
			http.Error(w, "could not load portal module", http.StatusInternalServerError)
			return
		}
		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, definition.Title)
		writePortalNav(w, csrf, principal, r.URL.Path)
		fmt.Fprintf(w, `<main id="main-content" tabindex="-1" class="container pb-5"><p class="text-uppercase text-muted small mb-1">%s</p><h1 class="h2">%s</h1><p class="text-muted">%s</p>`,
			escapeHTML(definition.Eyebrow),
			escapeHTML(definition.Title),
			escapeHTML(definition.Description),
		)
		renderPortalOperationStatus(w, r.URL.Query().Get("status"))
		renderPortalOperationalRequestSection(w, csrf, definition, requests, s.LondonLoc)
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func (s *Server) handlePortalOperationalRequestCreate(
	definition portalOperationalModule,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		payload := make(map[string]string, len(definition.Fields))
		for _, field := range definition.Fields {
			value := strings.TrimSpace(r.FormValue(field.Name))
			if field.Required && value == "" {
				http.Error(w, field.Label+" is required", http.StatusBadRequest)
				return
			}
			payload[field.Name] = value
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		_, caseID, err := s.PortalStore.CreateOperationalRequest(
			ctx,
			principal,
			portal.CreateModuleRequest{
				Type:              definition.RequestType,
				Title:             r.FormValue("title"),
				ExternalReference: r.FormValue("external_reference"),
				Payload:           payload,
				Message:           r.FormValue("message"),
			},
			requestID(r),
		)
		if err != nil {
			http.Error(w, "could not submit portal request", http.StatusBadRequest)
			return
		}
		// The generated case is the common communications and audit trail for
		// every module request.
		detail, loadErr := s.PortalStore.LoadMessageCase(ctx, principal, caseID)
		status := "request-submitted"
		if loadErr == nil && len(detail.Messages) > 0 {
			status = s.sendPortalCaseCopy(
				ctx,
				detail.Messages[0].ID,
				portalGMCLCommsEmail(),
				"GMCL portal request: "+strings.TrimSpace(r.FormValue("title")),
				fmt.Sprintf(
					"Portal request type: %s\nReference: %s\n\n%s\n\nReview: %s/admin/portal/cases/%s",
					definition.RequestType,
					strings.TrimSpace(r.FormValue("external_reference")),
					strings.TrimSpace(r.FormValue("message")),
					publicBaseURL(r),
					caseID,
				),
			)
		}
		http.Redirect(w, r, definition.Path+"?status="+status, http.StatusSeeOther)
	}
}

func renderPortalOperationalRequestSection(
	w http.ResponseWriter,
	csrf string,
	definition portalOperationalModule,
	requests []portal.ModuleRequest,
	location *time.Location,
) {
	fmt.Fprintf(w, `<section class="card shadow-sm mt-4"><div class="card-header"><strong>Submit request</strong></div><div class="card-body"><div class="alert alert-info">%s</div><form method="post" action="%s" class="row g-3"><input type="hidden" name="csrf_token" value="%s">
<div class="col-md-8"><label class="form-label" for="module-title">Title</label><input class="form-control" id="module-title" name="title" maxlength="200" required></div>
<div class="col-md-4"><label class="form-label" for="module-reference">External reference</label><input class="form-control" id="module-reference" name="external_reference" maxlength="500"></div>`,
		escapeHTML(definition.SafetyNote),
		escapeHTML(definition.Path),
		escapeHTML(csrf),
	)
	for _, field := range definition.Fields {
		required := ""
		if field.Required {
			required = " required"
		}
		if field.Kind == "textarea" {
			fmt.Fprintf(w, `<div class="col-12"><label class="form-label" for="field-%s">%s</label><textarea class="form-control" id="field-%s" name="%s" rows="3" maxlength="1000"%s></textarea></div>`,
				escapeHTML(field.Name),
				escapeHTML(field.Label),
				escapeHTML(field.Name),
				escapeHTML(field.Name),
				required,
			)
		} else {
			fmt.Fprintf(w, `<div class="col-md-6"><label class="form-label" for="field-%s">%s</label><input class="form-control" id="field-%s" name="%s" maxlength="1000" placeholder="%s"%s></div>`,
				escapeHTML(field.Name),
				escapeHTML(field.Label),
				escapeHTML(field.Name),
				escapeHTML(field.Name),
				escapeHTML(field.Placeholder),
				required,
			)
		}
	}
	fmt.Fprint(w, `<div class="col-12"><label class="form-label" for="module-message">Supporting explanation</label><textarea class="form-control" id="module-message" name="message" rows="5" maxlength="10000" required></textarea></div><div class="col-12"><button class="btn btn-primary" type="submit">Submit for GMCL review</button></div></form></div></section>
<section class="card shadow-sm mt-4"><div class="card-header"><strong>Request history</strong></div><div class="table-responsive"><table class="table table-striped mb-0"><thead><tr><th>Request</th><th>Status</th><th>Rule/release</th><th>Updated</th></tr></thead><tbody>`)
	if len(requests) == 0 {
		fmt.Fprint(w, `<tr><td colspan="4" class="text-muted">No requests have been submitted.</td></tr>`)
	}
	for _, item := range requests {
		caseLink := ""
		if item.CaseID != nil {
			caseLink = fmt.Sprintf(` <a class="small" href="/portal/messages/%s">Open case</a>`, *item.CaseID)
		}
		rule := item.RuleRelease
		if rule == "" {
			rule = "Awaiting review"
		}
		fmt.Fprintf(w, `<tr><td>%s%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			escapeHTML(item.Title),
			caseLink,
			escapeHTML(item.Status),
			escapeHTML(rule),
			portalLocalTime(item.UpdatedAt, location),
		)
	}
	fmt.Fprint(w, `</tbody></table></div></section>`)
}

func (s *Server) handlePortalFixturePlanningGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		constraints, err := s.PortalStore.ListFixtureConstraints(ctx, principal)
		if errors.Is(err, portal.ErrForbidden) {
			renderPortalModuleDisabled(w, r, principal, "Fixture planning")
			return
		}
		if err != nil {
			http.Error(w, "could not load fixture constraints", http.StatusInternalServerError)
			return
		}
		scope, err := s.PortalStore.ResolveReadScope(ctx, principal, nil, nil)
		if err != nil {
			http.Error(w, "could not load fixture scope", http.StatusInternalServerError)
			return
		}
		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Fixture planning")
		writePortalNav(w, csrf, principal, r.URL.Path)
		fmt.Fprint(w, `<main id="main-content" tabindex="-1" class="container-fluid px-3 px-lg-4 pb-5"><p class="text-uppercase text-muted small mb-1">Constraint catalogue</p><h1 class="h2">Fixture planning</h1><p class="text-muted">Capture hard and soft constraints for a future isolated optimisation run. Nothing on this page publishes or changes a fixture.</p>`)
		renderPortalOperationStatus(w, r.URL.Query().Get("status"))
		fmt.Fprint(w, `<div class="row g-4"><div class="col-xl-5"><section class="card shadow-sm"><div class="card-header"><strong>Submit constraint</strong></div><div class="card-body"><form method="post" action="/portal/fixture-planning" class="row g-3"><input type="hidden" name="csrf_token" value="`+escapeHTML(csrf)+`">
<div class="col-12"><label class="form-label" for="constraint-team">Team</label><select class="form-select" id="constraint-team" name="team_id"><option value="">Club-wide</option>`)
		for _, team := range scope.Teams {
			fmt.Fprintf(w, `<option value="%d">%s</option>`, team.ID, escapeHTML(team.Name))
		}
		fmt.Fprint(w, `</select></div><div class="col-12"><label class="form-label" for="constraint-season">Season</label><select class="form-select" id="constraint-season" name="season_id">`)
		for _, season := range scope.Seasons {
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, season.ID, selectedAttr(season.ID == scope.SelectedSeasonID), escapeHTML(season.Name))
		}
		fmt.Fprint(w, `</select></div><div class="col-12"><label class="form-label" for="constraint-type">Constraint</label><select class="form-select" id="constraint-type" name="constraint_type"><option value="venue_unavailable">Venue unavailable</option><option value="team_unavailable">Team unavailable</option><option value="shared_ground">Shared ground</option><option value="travel_preference">Travel preference</option><option value="paired_team">Paired team</option><option value="date_preference">Date preference</option><option value="other">Other</option></select></div>
<div class="col-md-6"><label class="form-label" for="constraint-start">From</label><input class="form-control" id="constraint-start" type="date" name="starts_on"></div><div class="col-md-6"><label class="form-label" for="constraint-end">To</label><input class="form-control" id="constraint-end" type="date" name="ends_on"></div>
<div class="col-12"><label class="form-label" for="constraint-description">Description</label><textarea class="form-control" id="constraint-description" name="description" rows="4" maxlength="1000" required></textarea></div>
<div class="col-12 form-check ms-2"><input class="form-check-input" type="checkbox" id="hard-constraint" name="hard_constraint" value="true" checked><label class="form-check-label" for="hard-constraint">Hard constraint (candidate schedules must satisfy it)</label></div>
<div class="col-12"><button class="btn btn-primary" type="submit">Submit constraint</button></div></form></div></section></div><div class="col-xl-7"><section class="card shadow-sm"><div class="card-header"><strong>Constraint inventory</strong></div><div class="table-responsive"><table class="table table-striped mb-0"><thead><tr><th>Scope</th><th>Constraint</th><th>Strength</th><th>Status</th></tr></thead><tbody>`)
		if len(constraints) == 0 {
			fmt.Fprint(w, `<tr><td colspan="4" class="text-muted">No fixture constraints have been submitted.</td></tr>`)
		}
		for _, item := range constraints {
			scopeLabel := "Club-wide"
			if item.TeamName != "" {
				scopeLabel = item.TeamName
			}
			if item.SeasonName != "" {
				scopeLabel += " · " + item.SeasonName
			}
			strength := "Soft"
			if item.HardConstraint {
				strength = "Hard"
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td><strong>%s</strong><div class="small text-muted">%s</div></td><td>%s</td><td>%s</td></tr>`,
				escapeHTML(scopeLabel),
				escapeHTML(strings.ReplaceAll(item.ConstraintType, "_", " ")),
				escapeHTML(item.Description),
				strength,
				escapeHTML(item.Status),
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section><div class="alert alert-warning mt-4"><strong>No automatic publication.</strong> OR-Tools experimentation begins only after GMCL signs off the constraint catalogue and historical replay data. Every generated schedule requires manual review and controlled publication.</div></div></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) handlePortalFixtureConstraintCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		teamID, err := optionalInt32FormValue(r.FormValue("team_id"))
		if err != nil {
			http.Error(w, "invalid team", http.StatusBadRequest)
			return
		}
		seasonID, err := optionalInt32FormValue(r.FormValue("season_id"))
		if err != nil {
			http.Error(w, "invalid season", http.StatusBadRequest)
			return
		}
		startsOn, err := optionalDateFormValue(r.FormValue("starts_on"), s.LondonLoc)
		if err != nil {
			http.Error(w, "invalid start date", http.StatusBadRequest)
			return
		}
		endsOn, err := optionalDateFormValue(r.FormValue("ends_on"), s.LondonLoc)
		if err != nil {
			http.Error(w, "invalid end date", http.StatusBadRequest)
			return
		}
		hard := strings.EqualFold(strings.TrimSpace(r.FormValue("hard_constraint")), "true")
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		_, err = s.PortalStore.CreateFixtureConstraint(
			ctx,
			principal,
			portal.CreateFixtureConstraintRequest{
				TeamID:         teamID,
				SeasonID:       seasonID,
				ConstraintType: r.FormValue("constraint_type"),
				Description:    r.FormValue("description"),
				StartsOn:       startsOn,
				EndsOn:         endsOn,
				HardConstraint: hard,
			},
			requestID(r),
		)
		if err != nil {
			http.Error(w, "could not submit fixture constraint", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/portal/fixture-planning?status=constraint-submitted", http.StatusSeeOther)
	}
}

func (s *Server) sendPortalCaseCopy(
	ctx context.Context,
	messageID uuid.UUID,
	recipient string,
	subject string,
	body string,
) string {
	err := email.NewFromEnv().SendSensitive(recipient, subject, body)
	if err != nil {
		_ = s.PortalStore.MarkMessageEmailDelivery(ctx, messageID, false, err.Error())
		return "email-pending"
	}
	_ = s.PortalStore.MarkMessageEmailDelivery(ctx, messageID, true, "")
	return "sent"
}

func portalGMCLCommsEmail() string {
	if value := strings.TrimSpace(os.Getenv("PORTAL_GMCL_COMMS_EMAIL")); value != "" {
		return value
	}
	return "webmaster@gmcl.co.uk"
}

func renderPortalModuleDisabled(
	w http.ResponseWriter,
	r *http.Request,
	principal portal.Principal,
	module string,
) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	pageHead(w, module)
	writePortalNav(w, portalCSRFToken(r), principal, r.URL.Path)
	fmt.Fprintf(w, `<main id="main-content" tabindex="-1" class="container pb-5"><div class="alert alert-warning"><h1 class="h4">%s is not enabled</h1><p class="mb-0">A GMCL Super Administrator must enable this module for the selected club before it can be tested.</p></div></main>`,
		escapeHTML(module),
	)
	pageFooter(w)
}

func renderPortalOperationStatus(w http.ResponseWriter, status string) {
	switch strings.TrimSpace(status) {
	case "sent":
		fmt.Fprint(w, `<div class="alert alert-success">Saved and copied to the official GMCL email channel.</div>`)
	case "email-pending":
		fmt.Fprint(w, `<div class="alert alert-warning">Saved in the portal, but the official email copy needs an operator retry.</div>`)
	case "acknowledged":
		fmt.Fprint(w, `<div class="alert alert-success">The case has been acknowledged.</div>`)
	case "request-submitted":
		fmt.Fprint(w, `<div class="alert alert-success">The request was submitted for human review.</div>`)
	case "contact-submitted":
		fmt.Fprint(w, `<div class="alert alert-success">The contact version was submitted for verification.</div>`)
	case "constraint-submitted":
		fmt.Fprint(w, `<div class="alert alert-success">The constraint was added to the review catalogue. No fixture was changed.</div>`)
	}
}

func humanPortalMessageCategory(category portal.MessageCategory) string {
	switch category {
	case portal.MessageCategoryGeneral:
		return "General"
	case portal.MessageCategoryCompliance:
		return "Compliance and sanctions"
	case portal.MessageCategoryFixtures:
		return "Fixtures"
	case portal.MessageCategoryRegistration:
		return "Registration"
	case portal.MessageCategoryStarred:
		return "Starred players"
	case portal.MessageCategoryJunior:
		return "Junior administration"
	case portal.MessageCategoryContact:
		return "Contact or correction"
	case portal.MessageCategoryPlayerIdentity:
		return "Player identity"
	default:
		return "Other"
	}
}

func humanPortalContactRole(role string) string {
	switch role {
	case "primary_contact":
		return "Primary contact"
	case "secretary":
		return "Secretary"
	case "play_cricket_admin":
		return "Play-Cricket administrator"
	case "junior_contact":
		return "Junior contact"
	case "fixtures_contact":
		return "Fixtures contact"
	case "registration_contact":
		return "Registration contact"
	default:
		return "Other"
	}
}

func portalCaseStatusBadge(status string) string {
	className := "text-bg-secondary"
	switch status {
	case "new", "awaiting_gmcl", "reopened":
		className = "text-bg-warning"
	case "awaiting_club", "in_progress":
		className = "text-bg-primary"
	case "resolved":
		className = "text-bg-success"
	case "closed":
		className = "text-bg-dark"
	}
	return fmt.Sprintf(
		`<span class="badge %s">%s</span>`,
		className,
		escapeHTML(strings.ReplaceAll(status, "_", " ")),
	)
}

func portalPriorityBadge(priority string) string {
	if priority != "urgent" {
		return ""
	}
	return ` <span class="badge text-bg-danger">Urgent</span>`
}

func selectedAttr(selected bool) string {
	if selected {
		return ` selected`
	}
	return ""
}

func optionalInt32FormValue(value string) (*int32, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed <= 0 {
		return nil, fmt.Errorf("invalid identifier")
	}
	result := int32(parsed)
	return &result, nil
}

func optionalDateFormValue(value string, location *time.Location) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if location == nil {
		location = time.UTC
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, location)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
