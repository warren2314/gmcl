package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/portal"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) handleAdminPortalCasesGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		cases, err := s.PortalStore.ListAdminMessageCases(ctx)
		if err != nil {
			slog.Error("list portal cases", "error", err)
			http.Error(w, "could not load portal cases", http.StatusInternalServerError)
			return
		}
		requests, err := s.PortalStore.ListAdminOperationalRequests(ctx)
		if err != nil {
			slog.Error("list portal requests", "error", err)
			http.Error(w, "could not load portal requests", http.StatusInternalServerError)
			return
		}
		contacts, err := s.PortalStore.ListAdminClubContacts(ctx)
		if err != nil {
			slog.Error("list portal contacts", "error", err)
			http.Error(w, "could not load portal contacts", http.StatusInternalServerError)
			return
		}
		csrf := adminPortalCSRF(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Club portal work queue")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-wrap justify-content-between align-items-start gap-3 mb-4"><div><p class="text-uppercase text-muted small mb-1">Club operations</p><h1 class="h2">Portal work queue</h1><p class="text-muted">Club-visible correspondence is kept separate from internal GMCL notes. Email remains the official record during the pilot.</p></div><a class="btn btn-outline-primary" href="/admin/portal">Pilot controls</a></div>`)
		renderAdminPortalOperationStatus(w, r.URL.Query().Get("status"))
		fmt.Fprint(w, `<section class="card shadow-sm mb-4"><div class="card-header"><strong>Communication cases</strong></div><div class="table-responsive"><table class="table table-striped align-middle mb-0"><thead><tr><th>Club</th><th>Subject</th><th>Category</th><th>Status</th><th>Messages</th><th>Updated</th></tr></thead><tbody>`)
		if len(cases) == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-muted">No communication cases.</td></tr>`)
		}
		for _, item := range cases {
			fmt.Fprintf(w, `<tr><td>%s</td><td><a href="/admin/portal/cases/%s">%s</a>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%s</td></tr>`,
				escapeHTML(item.ClubName), item.ID, escapeHTML(item.Subject),
				portalPriorityBadge(item.Priority), escapeHTML(humanPortalMessageCategory(item.Category)),
				portalCaseStatusBadge(item.Status), item.MessageCount,
				escapeHTML(portalLocalTime(item.UpdatedAt, s.LondonLoc)),
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section>
<section class="card shadow-sm mb-4"><div class="card-header"><strong>Operational reviews</strong></div><div class="table-responsive"><table class="table table-striped align-middle mb-0"><thead><tr><th>Club</th><th>Request</th><th>Status</th><th>Reference</th><th>Human decision</th></tr></thead><tbody>`)
		if len(requests) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-muted">No operational reviews.</td></tr>`)
		}
		for _, item := range requests {
			caseLink := ""
			if item.CaseID != nil {
				caseLink = fmt.Sprintf(` <a href="/admin/portal/cases/%s">Open case</a>`, *item.CaseID)
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td><strong>%s</strong><div class="small text-muted">%s</div>%s</td><td>%s</td><td>%s</td><td><form method="post" action="/admin/portal/requests/%s/review" class="row g-2"><input type="hidden" name="csrf_token" value="%s"><div class="col-md-3"><select class="form-select form-select-sm" name="status"><option value="under_review">Under review</option><option value="awaiting_club">Awaiting club</option><option value="approved">Approved</option><option value="rejected">Rejected</option><option value="completed">Completed</option></select></div><div class="col-md-3"><input class="form-control form-control-sm" name="rule_release" maxlength="200" placeholder="Rule release/version"></div><div class="col-md-4"><input class="form-control form-control-sm" name="review_note" maxlength="2000" placeholder="Decision note"></div><div class="col-md-2"><button class="btn btn-sm btn-primary" type="submit">Record</button></div></form></td></tr>`,
				escapeHTML(item.ClubName), escapeHTML(item.Title), escapeHTML(string(item.Type)),
				caseLink, escapeHTML(item.Status), escapeHTML(item.ExternalReference),
				item.ID, escapeHTML(csrf),
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section>
<section class="card shadow-sm"><div class="card-header"><strong>Club contact verification</strong></div><div class="table-responsive"><table class="table table-striped align-middle mb-0"><thead><tr><th>Club</th><th>Role</th><th>Contact</th><th>Evidence reference</th><th>Status/action</th></tr></thead><tbody>`)
		if len(contacts) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-muted">No club contact versions.</td></tr>`)
		}
		for _, item := range contacts {
			action := escapeHTML(item.Status)
			if item.Status == "pending" {
				action = fmt.Sprintf(`<form method="post" action="/admin/portal/contacts/%s/review" class="d-flex gap-2"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-sm btn-success" name="decision" value="approve" type="submit">Verify</button><button class="btn btn-sm btn-outline-danger" name="decision" value="reject" type="submit">Reject</button></form>`, item.ID, escapeHTML(csrf))
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s<div class="small">%s · %s</div></td><td>%s</td><td>%s</td></tr>`,
				escapeHTML(item.ClubName), escapeHTML(humanPortalContactRole(item.RoleKey)),
				escapeHTML(item.DisplayName), escapeHTML(item.Email), escapeHTML(item.Phone),
				escapeHTML(item.EvidenceReference), action,
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPortalCaseGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		detail, err := s.PortalStore.LoadAdminMessageCase(ctx, caseID)
		if err != nil {
			http.Error(w, "case not found", http.StatusNotFound)
			return
		}
		csrf := adminPortalCSRF(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, detail.Subject)
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container pb-5"><p><a href="/admin/portal/cases">← Work queue</a></p><div class="d-flex flex-wrap justify-content-between gap-3"><div><h1 class="h3">%s%s</h1><p>%s · %s</p></div><div>%s</div></div>`,
			escapeHTML(detail.Subject), portalPriorityBadge(detail.Priority),
			escapeHTML(detail.ClubName), escapeHTML(humanPortalMessageCategory(detail.Category)),
			portalCaseStatusBadge(detail.Status),
		)
		renderAdminPortalOperationStatus(w, r.URL.Query().Get("status"))
		fmt.Fprint(w, `<div class="row g-4"><div class="col-lg-8"><section class="card shadow-sm"><div class="card-header"><strong>Club-visible correspondence</strong></div><div class="card-body">`)
		for _, message := range detail.Messages {
			retry := ""
			if message.EmailStatus != "sent" {
				retry = fmt.Sprintf(`<form method="post" action="/admin/portal/messages/%s/retry-email" class="mt-2"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-sm btn-outline-warning" type="submit">Retry official email copy</button></form>`, message.ID, escapeHTML(csrf))
			}
			fmt.Fprintf(w, `<article class="border rounded p-3 mb-3"><div class="d-flex justify-content-between"><strong>%s</strong><span class="small text-muted">%s · email %s</span></div><div class="mt-2" style="white-space:pre-wrap">%s</div>%s</article>`,
				escapeHTML(message.AuthorLabel), escapeHTML(portalLocalTime(message.CreatedAt, s.LondonLoc)),
				escapeHTML(message.EmailStatus), escapeHTML(message.Body), retry,
			)
		}
		fmt.Fprintf(w, `<form method="post" action="/admin/portal/cases/%s/reply"><input type="hidden" name="csrf_token" value="%s"><label class="form-label" for="reply">Reply visible to the club</label><textarea class="form-control" id="reply" name="body" rows="5" maxlength="10000" required></textarea><div class="form-text">This is also emailed to the verified case creator.</div><button class="btn btn-primary mt-3" type="submit">Send reply</button></form></div></section></div>`,
			detail.ID, escapeHTML(csrf),
		)
		fmt.Fprintf(w, `<div class="col-lg-4"><section class="card shadow-sm mb-4"><div class="card-header"><strong>Workflow</strong></div><div class="card-body"><form method="post" action="/admin/portal/cases/%s/update"><input type="hidden" name="csrf_token" value="%s"><label class="form-label">Status</label><select class="form-select mb-3" name="status">`,
			detail.ID, escapeHTML(csrf),
		)
		for _, status := range []string{"new", "awaiting_gmcl", "awaiting_club", "in_progress", "resolved", "closed", "reopened"} {
			selected := ""
			if status == detail.Status {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, status, selected, escapeHTML(strings.ReplaceAll(status, "_", " ")))
		}
		fmt.Fprint(w, `</select><label class="form-label">Priority</label><select class="form-select mb-3" name="priority">`)
		for _, priority := range []string{"normal", "urgent"} {
			selected := ""
			if priority == detail.Priority {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, priority, selected, priority)
		}
		deadline := ""
		if detail.DeadlineAt != nil {
			deadline = detail.DeadlineAt.In(s.LondonLoc).Format("2006-01-02")
		}
		fmt.Fprintf(w, `</select><label class="form-label">Deadline</label><input class="form-control mb-3" type="date" name="deadline" value="%s"><label class="form-label">Assign admin ID</label><input class="form-control mb-3" type="number" min="1" name="assigned_admin_id" value="%d"><button class="btn btn-outline-primary" type="submit">Update workflow</button></form></div></section>`,
			escapeHTML(deadline), adminIDForRequest(r),
		)
		fmt.Fprintf(w, `<section class="card shadow-sm border-warning"><div class="card-header"><strong>Internal GMCL notes</strong></div><div class="card-body"><div class="alert alert-warning small">Never returned by club repositories or portal responses.</div>`)
		for _, note := range detail.InternalNotes {
			fmt.Fprintf(w, `<article class="border rounded p-2 mb-2"><strong>%s</strong><div class="small text-muted">%s</div><div style="white-space:pre-wrap">%s</div></article>`,
				escapeHTML(note.AuthorLabel), escapeHTML(portalLocalTime(note.CreatedAt, s.LondonLoc)), escapeHTML(note.Body),
			)
		}
		fmt.Fprintf(w, `<form method="post" action="/admin/portal/cases/%s/notes"><input type="hidden" name="csrf_token" value="%s"><textarea class="form-control" name="body" rows="4" maxlength="10000" required></textarea><button class="btn btn-warning mt-2" type="submit">Add internal note</button></form></div></section></div></div></main>`,
			detail.ID, escapeHTML(csrf),
		)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPortalCaseReply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		detail, err := s.PortalStore.LoadAdminMessageCase(ctx, caseID)
		if err != nil {
			http.Error(w, "case not found", http.StatusNotFound)
			return
		}
		messageID, err := s.PortalStore.AddAdminCaseMessage(ctx, caseID, adminIDForRequest(r), r.FormValue("body"), requestID(r))
		if err != nil {
			http.Error(w, "could not add reply", http.StatusBadRequest)
			return
		}
		status := "replied"
		if detail.CreatedByEmail != "" {
			body := fmt.Sprintf("GMCL replied to portal case %s:\n\n%s\n\nOpen the portal: %s/portal/messages/%s", detail.Subject, strings.TrimSpace(r.FormValue("body")), publicBaseURL(r), caseID)
			if err := email.NewFromEnv().SendSensitive(detail.CreatedByEmail, "GMCL portal: "+detail.Subject, body); err != nil {
				status = "email-pending"
				_ = s.PortalStore.MarkMessageEmailDelivery(ctx, messageID, false, err.Error())
			} else {
				_ = s.PortalStore.MarkMessageEmailDelivery(ctx, messageID, true, "")
			}
		}
		http.Redirect(w, r, "/admin/portal/cases/"+caseID.String()+"?status="+status, http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalCaseNote() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, err := s.PortalStore.AddInternalCaseNote(ctx, caseID, adminIDForRequest(r), r.FormValue("body"), requestID(r)); err != nil {
			http.Error(w, "could not add internal note", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal/cases/"+caseID.String()+"?status=note-added", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalMessageEmailRetry() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		messageID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid message", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		delivery, err := s.PortalStore.LoadAdminMessageDeliveryContext(ctx, messageID)
		if err != nil {
			http.Error(w, "message not found", http.StatusNotFound)
			return
		}
		recipient := portalGMCLCommsEmail()
		subject := "GMCL portal: " + delivery.Subject
		body := delivery.Body + "\n\nOpen the case: " + publicBaseURL(r) + "/admin/portal/cases/" + delivery.CaseID.String()
		if delivery.AuthorKind == "gmcl_admin" {
			recipient = delivery.CreatedByEmail
			body = delivery.Body + "\n\nOpen the portal: " + publicBaseURL(r) + "/portal/messages/" + delivery.CaseID.String()
		}
		if recipient == "" {
			http.Error(w, "no verified email recipient is available", http.StatusConflict)
			return
		}
		status := "replied"
		if err := email.NewFromEnv().SendSensitive(recipient, subject, body); err != nil {
			status = "email-pending"
			_ = s.PortalStore.MarkMessageEmailDelivery(ctx, messageID, false, err.Error())
		} else {
			_ = s.PortalStore.MarkMessageEmailDelivery(ctx, messageID, true, "")
		}
		http.Redirect(w, r, "/admin/portal/cases/"+delivery.CaseID.String()+"?status="+status, http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalCaseUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		var assigned *int32
		if raw := strings.TrimSpace(r.FormValue("assigned_admin_id")); raw != "" {
			value, parseErr := strconv.ParseInt(raw, 10, 32)
			if parseErr != nil || value <= 0 {
				http.Error(w, "invalid assignee", http.StatusBadRequest)
				return
			}
			converted := int32(value)
			assigned = &converted
		}
		deadline, err := optionalDateFormValue(r.FormValue("deadline"), s.LondonLoc)
		if err != nil {
			http.Error(w, "invalid deadline", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := s.PortalStore.UpdateAdminCase(ctx, caseID, adminIDForRequest(r), portal.AdminCaseUpdate{
			Status: r.FormValue("status"), Priority: r.FormValue("priority"),
			AssignedAdminID: assigned, DeadlineAt: deadline,
		}, requestID(r)); err != nil {
			http.Error(w, "could not update case", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal/cases/"+caseID.String()+"?status=case-updated", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalRequestReview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := s.PortalStore.ReviewOperationalRequest(ctx, id, adminIDForRequest(r), r.FormValue("status"), r.FormValue("review_note"), r.FormValue("rule_release"), requestID(r)); err != nil {
			http.Error(w, "could not review request", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal/cases?status=request-reviewed", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalContactReview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid contact", http.StatusBadRequest)
			return
		}
		decision := strings.TrimSpace(r.FormValue("decision"))
		if decision != "approve" && decision != "reject" {
			http.Error(w, "invalid decision", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := s.PortalStore.VerifyClubContact(ctx, id, adminIDForRequest(r), decision == "approve", requestID(r)); err != nil {
			http.Error(w, "could not review contact", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal/cases?status=contact-reviewed", http.StatusSeeOther)
	}
}

func adminPortalCSRF(r *http.Request) string {
	if cookie, err := r.Cookie(middleware.CSRFCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func renderAdminPortalOperationStatus(w http.ResponseWriter, status string) {
	messages := map[string]string{
		"request-reviewed": "The human review decision was recorded.",
		"contact-reviewed": "The contact verification decision was recorded.",
		"replied":          "The reply was saved and emailed.",
		"email-pending":    "The reply was saved, but its email copy needs an operator retry.",
		"note-added":       "The internal GMCL note was added.",
		"case-updated":     "The case workflow was updated.",
	}
	if message := messages[status]; message != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(message))
	}
}

func sortedPayloadKeys(payload map[string]string) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
