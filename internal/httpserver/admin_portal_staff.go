package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/portal"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type portalStaffAccessContextKey struct{}

func (s *Server) requirePortalStaff() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adminID := adminIDForRequest(r)
			if adminID <= 0 {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			access, err := s.PortalStore.LoadStaffAccess(ctx, adminID)
			if err != nil || !access.HasPortalStaffAccess() {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(
				w,
				r.WithContext(context.WithValue(
					r.Context(),
					portalStaffAccessContextKey{},
					access,
				)),
			)
		})
	}
}

func portalStaffAccessForRequest(r *http.Request) (portal.StaffAccess, bool) {
	access, ok := r.Context().Value(portalStaffAccessContextKey{}).(portal.StaffAccess)
	return access, ok && access.HasPortalStaffAccess()
}

func (s *Server) handleAdminPortalStaffGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		admins, err := s.PortalStore.ListAdminCaseAssignees(ctx)
		if err != nil {
			http.Error(w, "could not load administrators", http.StatusInternalServerError)
			return
		}
		clubs, err := s.PortalStore.ListPilotClubs(ctx)
		if err != nil {
			http.Error(w, "could not load clubs", http.StatusInternalServerError)
			return
		}
		competitions, err := s.PortalStore.ListStaffCompetitions(ctx)
		if err != nil {
			http.Error(w, "could not load competitions", http.StatusInternalServerError)
			return
		}
		assignments, err := s.PortalStore.ListStaffAssignments(ctx)
		if err != nil {
			http.Error(w, "could not load staff assignments", http.StatusInternalServerError)
			return
		}
		csrf := adminPortalCSRF(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Portal staff assignments")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-column flex-lg-row justify-content-between align-items-lg-start gap-3 mb-4"><div><p class="text-uppercase text-muted small mb-1">Club operations</p><h1 class="h2">Portal staff roles and scopes</h1><p class="text-muted mb-0">Assign named GMCL administrators as Club Liaison Officers or Junior Administrators. A blank scope applies to all clubs; otherwise choose one club or one competition.</p></div><div class="d-grid d-sm-flex flex-wrap gap-2 flex-shrink-0"><a class="btn btn-outline-secondary" href="/admin/portal">&larr; Pilot controls</a><a class="btn btn-primary px-4" href="/admin/portal/messages/new">Compose club message</a></div></div>`)
		renderAdminPortalStaffStatus(w, r.URL.Query().Get("status"))
		fmt.Fprintf(w, `<section class="card shadow-sm mb-4"><div class="card-header"><strong>Add staff assignment</strong></div><form method="post" action="/admin/portal/staff/assignments"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><div class="row g-3"><div class="col-md-4"><label class="form-label">Administrator</label><select class="form-select" name="admin_user_id" required><option value="">Choose administrator</option>`, escapeHTML(csrf))
		for _, admin := range admins {
			label := admin.Name
			if admin.Email != "" && !strings.EqualFold(admin.Name, admin.Email) {
				label += " · " + admin.Email
			}
			fmt.Fprintf(w, `<option value="%d">%s</option>`, admin.ID, escapeHTML(label))
		}
		fmt.Fprint(w, `</select></div><div class="col-md-3"><label class="form-label">Staff role</label><select class="form-select" name="role_key" required><option value="club_liaison_officer">Club Liaison Officer</option><option value="junior_administrator">Junior Administrator</option></select></div><div class="col-md-5"><label class="form-label">Club scope (optional)</label><select class="form-select" name="club_id"><option value="">All clubs or competition scope</option>`)
		for _, club := range clubs {
			fmt.Fprintf(w, `<option value="%d">%s</option>`, club.ID, escapeHTML(club.Name))
		}
		fmt.Fprint(w, `</select></div><div class="col-md-5"><label class="form-label">Competition scope (optional)</label><select class="form-select" name="competition_id"><option value="">No competition restriction</option>`)
		for _, competition := range competitions {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, competition.ID, escapeHTML(competition.Name))
		}
		fmt.Fprint(w, `</select><div class="form-text">Choose either a club or a competition, not both.</div></div><div class="col-md-7"><label class="form-label">Reason</label><input class="form-control" name="grant_reason" maxlength="500" required placeholder="Appointment authority, season or operational responsibility"></div></div></div><div class="card-footer d-flex flex-column flex-sm-row justify-content-between align-items-sm-center gap-2"><span class="text-muted small">Access takes effect immediately and is recorded in the audit trail.</span><button class="btn btn-primary px-4" type="submit">Assign staff role</button></div></form></section>
<section class="card shadow-sm"><div class="card-header"><strong>Staff assignments</strong></div><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0"><thead><tr><th>Administrator</th><th>Role</th><th>Scope</th><th>Status</th><th>Reason</th><th class="text-end">Actions</th></tr></thead><tbody>`)
		if len(assignments) == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-muted">No portal staff assignments exist.</td></tr>`)
		}
		for _, assignment := range assignments {
			scope := "All clubs"
			if assignment.ClubID != nil {
				scope = assignment.ClubName
			} else if assignment.CompetitionID != nil {
				scope = "Competition: " + assignment.CompetitionName
			}
			action := ""
			if assignment.Status == "active" {
				revokeID := "revoke-" + assignment.ID.String()
				action = fmt.Sprintf(`<div class="text-md-end"><button class="btn btn-sm btn-outline-danger text-nowrap" type="button" data-bs-toggle="collapse" data-bs-target="#%s" aria-expanded="false" aria-controls="%s">Remove access</button></div><div class="collapse mt-2" id="%s"><form method="post" action="/admin/portal/staff/assignments/%s/revoke" class="border rounded-3 bg-body-tertiary p-2 text-start"><input type="hidden" name="csrf_token" value="%s"><label class="form-label small mb-1" for="%s-reason">Reason for removal</label><input id="%s-reason" class="form-control form-control-sm mb-2" name="reason" maxlength="500" required placeholder="Why is this access being removed?"><button class="btn btn-sm btn-danger w-100" type="submit">Confirm removal</button></form></div>`,
					revokeID,
					revokeID,
					revokeID,
					assignment.ID,
					escapeHTML(csrf),
					revokeID,
					revokeID,
				)
			}
			statusClass := "text-bg-secondary"
			if assignment.Status == "active" {
				statusClass = "text-bg-success"
			}
			fmt.Fprintf(w, `<tr><td data-label="Administrator"><strong>%s</strong><div class="small text-muted">%s</div></td><td data-label="Role">%s</td><td data-label="Scope">%s</td><td data-label="Status"><span class="badge rounded-pill %s">%s</span></td><td data-label="Reason">%s</td><td data-label="Actions" style="min-width:15rem">%s</td></tr>`,
				escapeHTML(assignment.AdminName),
				escapeHTML(assignment.AdminEmail),
				escapeHTML(portal.StaffRoleLabel(assignment.Role)),
				escapeHTML(scope),
				statusClass,
				escapeHTML(assignment.Status),
				escapeHTML(assignment.GrantReason),
				action,
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPortalStaffAssignmentCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID64, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("admin_user_id")), 10, 32)
		if err != nil || adminID64 <= 0 {
			http.Error(w, "invalid administrator", http.StatusBadRequest)
			return
		}
		role, ok := portal.ParseStaffRoleKey(r.FormValue("role_key"))
		if !ok || role == portal.StaffRoleSuperAdministrator {
			http.Error(w, "invalid staff role", http.StatusBadRequest)
			return
		}
		var clubID *int32
		if raw := strings.TrimSpace(r.FormValue("club_id")); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || value <= 0 {
				http.Error(w, "invalid club scope", http.StatusBadRequest)
				return
			}
			converted := int32(value)
			clubID = &converted
		}
		var competitionID *uuid.UUID
		if raw := strings.TrimSpace(r.FormValue("competition_id")); raw != "" {
			value, err := uuid.Parse(raw)
			if err != nil {
				http.Error(w, "invalid competition scope", http.StatusBadRequest)
				return
			}
			competitionID = &value
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if _, err := s.PortalStore.CreateStaffAssignment(ctx, portal.StaffAssignmentRequest{
			AdminUserID:   int32(adminID64),
			Role:          role,
			ClubID:        clubID,
			CompetitionID: competitionID,
			GrantReason:   r.FormValue("grant_reason"),
			GrantedBy:     adminIDForRequest(r),
		}); err != nil {
			slog.Warn("create portal staff assignment failed", "error", err)
			http.Error(w, "could not create staff assignment", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal/staff?status=assignment-created", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalStaffAssignmentRevoke() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assignmentID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid staff assignment", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.PortalStore.RevokeStaffAssignment(
			ctx,
			assignmentID,
			adminIDForRequest(r),
			r.FormValue("reason"),
		); err != nil {
			http.Error(w, "could not revoke staff assignment", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal/staff?status=assignment-revoked", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalMessageNewGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		access, ok := portalStaffAccessForRequest(r)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		allClubs, err := s.PortalStore.ListPilotClubs(ctx)
		if err != nil {
			http.Error(w, "could not load clubs", http.StatusInternalServerError)
			return
		}
		competitions, err := s.PortalStore.ListStaffCompetitions(ctx)
		if err != nil {
			http.Error(w, "could not load competitions", http.StatusInternalServerError)
			return
		}
		campaigns, err := s.PortalStore.ListStaffCampaigns(ctx, access)
		if err != nil {
			http.Error(w, "could not load campaigns", http.StatusInternalServerError)
			return
		}
		csrf := adminPortalCSRF(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "New club message")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 pb-5"><div class="d-flex flex-wrap justify-content-between gap-3 mb-4"><div><p class="text-uppercase text-muted small mb-1">Secure communication</p><h1 class="h2">New message to clubs</h1><p class="text-muted mb-0">Each selected club receives a private case in its own inbox and an official email copy to verified adult contacts.</p></div><a class="btn btn-outline-primary" href="/admin/portal/cases">Open work queue</a></div>`)
		renderAdminPortalCampaignStatus(w, r.URL.Query().Get("status"), r.URL.Query().Get("message"))
		fmt.Fprintf(w, `<section class="card shadow-sm mb-4"><div class="card-body"><form method="post" action="/admin/portal/messages" class="row g-3"><input type="hidden" name="csrf_token" value="%s"><div class="col-md-5"><label class="form-label">Category</label><select class="form-select" name="category" required>`, escapeHTML(csrf))
		if staffCanComposeGeneral(access) {
			for _, category := range []portal.MessageCategory{
				portal.MessageCategoryGeneral,
				portal.MessageCategoryCompliance,
				portal.MessageCategoryFixtures,
				portal.MessageCategoryRegistration,
				portal.MessageCategoryStarred,
				portal.MessageCategoryContact,
				portal.MessageCategoryPlayerIdentity,
			} {
				fmt.Fprintf(w, `<option value="%s">%s</option>`, category, escapeHTML(humanPortalMessageCategory(category)))
			}
		}
		if access.SuperAdmin || staffHasRole(access, portal.StaffRoleJuniorAdministrator) ||
			staffHasRole(access, portal.StaffRoleClubLiaison) {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, portal.MessageCategoryJunior, escapeHTML(humanPortalMessageCategory(portal.MessageCategoryJunior)))
		}
		fmt.Fprint(w, `</select></div><div class="col-md-4"><label class="form-label">Verified recipient role</label><select class="form-select" name="recipient_role" required>`)
		for _, role := range []portal.RecipientRoleKey{
			portal.RecipientPrimaryContact,
			portal.RecipientSecretary,
			portal.RecipientJuniorContact,
			portal.RecipientFixturesContact,
			portal.RecipientRegistration,
			portal.RecipientPlayCricketAdmin,
		} {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, role, escapeHTML(portal.RecipientRoleLabel(role)))
		}
		fmt.Fprint(w, `</select><div class="form-text">If that appointment has no verified address, delivery falls back to an active primary administrator or secretary.</div></div><div class="col-md-3"><label class="form-label">Priority</label><select class="form-select" name="priority"><option value="normal">Normal</option><option value="urgent">Urgent</option></select></div><div class="col-md-5"><label class="form-label">Competition context (optional)</label><select class="form-select" name="competition_id"><option value="">No competition context</option>`)
		for _, competition := range competitions {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, competition.ID, escapeHTML(competition.Name))
		}
		fmt.Fprint(w, `</select></div><div class="col-md-7"><label class="form-label">Select clubs</label><select class="form-select" name="club_id" multiple size="10" required aria-describedby="club-selection-help">`)
		clubCount := 0
		for _, club := range allClubs {
			if !club.PortalAccess || !club.SecureMessaging ||
				!staffMaySelectClub(access, club.ID) {
				continue
			}
			clubCount++
			fmt.Fprintf(w, `<option value="%d">%s</option>`, club.ID, escapeHTML(club.Name))
		}
		fmt.Fprint(w, `</select><div class="form-text" id="club-selection-help">Hold Ctrl (Windows) or Command (Mac) to select more than one club. Only clubs within your active scope are accepted.</div></div><div class="col-12"><label class="form-label">Subject</label><input class="form-control" name="subject" maxlength="200" required></div><div class="col-12"><label class="form-label">Message</label><textarea class="form-control" name="body" rows="8" maxlength="10000" required></textarea></div><div class="col-12"><div class="alert alert-info small">The portal and email identify you by name and staff role. SES sends from the verified GMCL address; it does not impersonate your personal mailbox.</div><button class="btn btn-primary" type="submit">Create cases and send</button></div></form></div></section>`)
		if clubCount == 0 {
			fmt.Fprint(w, `<div class="alert alert-warning">No secure-messaging clubs are currently available within your assignment scope.</div>`)
		}
		fmt.Fprint(w, `<section class="card shadow-sm"><div class="card-header"><strong>Recent campaigns</strong></div><div class="table-responsive"><table class="table table-striped align-middle mb-0"><thead><tr><th>Sent</th><th>Subject</th><th>Sender</th><th>Targets</th><th>Email result</th><th>Acknowledged</th><th>Club replies</th></tr></thead><tbody>`)
		if len(campaigns) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-muted">No staff campaigns have been sent.</td></tr>`)
		}
		for _, campaign := range campaigns {
			fmt.Fprintf(w, `<tr><td>%s</td><td><strong>%s</strong><div class="small text-muted">%s · %s</div></td><td>%s<div class="small text-muted">%s</div></td><td>%d</td><td>%s · %d sent · %d failed</td><td>%d/%d</td><td>%d</td></tr>`,
				escapeHTML(portalLocalTime(campaign.CreatedAt, s.LondonLoc)),
				escapeHTML(campaign.Subject),
				escapeHTML(humanPortalMessageCategory(campaign.Category)),
				escapeHTML(portal.RecipientRoleLabel(campaign.RecipientRole)),
				escapeHTML(campaign.SenderName),
				escapeHTML(portal.StaffRoleLabel(campaign.SenderRole)),
				campaign.TargetCount,
				escapeHTML(campaign.Status),
				campaign.SentTargetCount,
				campaign.FailedTargetCount,
				campaign.AcknowledgedCount,
				campaign.TargetCount,
				campaign.ClubReplyCount,
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPortalMessageCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid message form", http.StatusBadRequest)
			return
		}
		category := portal.MessageCategory(strings.ToLower(strings.TrimSpace(r.FormValue("category"))))
		recipientRole, ok := portal.ParseRecipientRoleKey(r.FormValue("recipient_role"))
		if !ok {
			http.Error(w, "invalid recipient role", http.StatusBadRequest)
			return
		}
		var competitionID *uuid.UUID
		if raw := strings.TrimSpace(r.FormValue("competition_id")); raw != "" {
			value, err := uuid.Parse(raw)
			if err != nil {
				http.Error(w, "invalid competition", http.StatusBadRequest)
				return
			}
			competitionID = &value
		}
		var clubIDs []int32
		for _, raw := range r.Form["club_id"] {
			value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
			if err != nil || value <= 0 {
				http.Error(w, "invalid club selection", http.StatusBadRequest)
				return
			}
			clubIDs = append(clubIDs, int32(value))
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		result, err := s.PortalStore.CreateStaffCampaign(
			ctx,
			adminIDForRequest(r),
			portal.StaffCampaignRequest{
				Category:      category,
				RecipientRole: recipientRole,
				CompetitionID: competitionID,
				ClubIDs:       clubIDs,
				Subject:       r.FormValue("subject"),
				Body:          r.FormValue("body"),
				Priority:      r.FormValue("priority"),
				CorrelationID: requestID(r),
			},
		)
		if err != nil {
			slog.Warn("create portal staff campaign failed", "error", err)
			http.Redirect(
				w,
				r,
				"/admin/portal/messages/new?status=campaign-invalid&message="+
					url.QueryEscape(err.Error()),
				http.StatusSeeOther,
			)
			return
		}

		mailer := email.NewFromEnv()
		failed := 0
		for _, delivery := range result.Deliveries {
			body := fmt.Sprintf(`This GMCL Club Operations Portal message was sent by %s (%s).

Club: %s
Recipient role: %s

%s

Open the private club case:
%s/portal/messages/%s

You can reply or acknowledge in the portal. Email remains the official GMCL communication record during the pilot.`,
				delivery.SenderName,
				portal.StaffRoleLabel(delivery.SenderRole),
				delivery.ClubName,
				portal.RecipientRoleLabel(delivery.RecipientRole),
				delivery.Body,
				publicBaseURL(r),
				delivery.CaseID,
			)
			sendErr := mailer.SendSensitive(
				delivery.RecipientEmail,
				"GMCL: "+delivery.Subject,
				body,
			)
			if sendErr != nil {
				failed++
			}
			deliveryError := ""
			if sendErr != nil {
				deliveryError = sendErr.Error()
			}
			if markErr := s.PortalStore.CompleteCampaignDelivery(
				ctx,
				delivery.ID,
				sendErr == nil,
				deliveryError,
			); markErr != nil {
				slog.Error("record portal campaign delivery failed",
					"delivery_id", delivery.ID, "error", markErr)
				failed++
			}
		}
		status := "campaign-sent"
		if failed > 0 {
			status = "campaign-partially-failed"
		}
		http.Redirect(
			w,
			r,
			"/admin/portal/messages/new?status="+status,
			http.StatusSeeOther,
		)
	}
}

func staffHasRole(access portal.StaffAccess, role portal.StaffRoleKey) bool {
	for _, assignment := range access.Assignments {
		if assignment.Role == role {
			return true
		}
	}
	return false
}

func staffCanComposeGeneral(access portal.StaffAccess) bool {
	return access.SuperAdmin || staffHasRole(access, portal.StaffRoleClubLiaison)
}

func staffMaySelectClub(access portal.StaffAccess, clubID int32) bool {
	if access.SuperAdmin {
		return true
	}
	for _, assignment := range access.Assignments {
		if assignment.ClubID == nil || *assignment.ClubID == clubID {
			return true
		}
	}
	return false
}

func renderAdminPortalStaffStatus(w http.ResponseWriter, status string) {
	switch status {
	case "assignment-created":
		fmt.Fprint(w, `<div class="alert alert-success">The portal staff role and scope were assigned.</div>`)
	case "assignment-revoked":
		fmt.Fprint(w, `<div class="alert alert-success">The portal staff assignment was revoked immediately.</div>`)
	}
}

func renderAdminPortalCampaignStatus(w http.ResponseWriter, status string, message string) {
	switch status {
	case "campaign-sent":
		fmt.Fprint(w, `<div class="alert alert-success">A private case was created for every selected club and all official email copies were sent.</div>`)
	case "campaign-partially-failed":
		fmt.Fprint(w, `<div class="alert alert-warning">Every selected club has its portal case, but at least one email copy failed. The failure is recorded for retry.</div>`)
	case "campaign-invalid":
		if strings.TrimSpace(message) == "" {
			message = "The campaign could not be created."
		}
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(message))
	}
}
