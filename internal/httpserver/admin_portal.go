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
	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/portal"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) handleAdminPortalGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		clubs, err := s.PortalStore.ListPilotClubs(ctx)
		if err != nil {
			slog.Error("list portal pilot clubs", "error", err)
			http.Error(w, "could not load portal controls", http.StatusInternalServerError)
			return
		}
		invitations, err := s.PortalStore.ListRecentInvitations(ctx, 50)
		if err != nil {
			slog.Error("list portal invitations", "error", err)
			http.Error(w, "could not load portal invitations", http.StatusInternalServerError)
			return
		}
		assignments, err := s.PortalStore.ListActiveAssignments(ctx, 100)
		if err != nil {
			slog.Error("list active portal assignments", "error", err)
			http.Error(w, "could not load portal appointments", http.StatusInternalServerError)
			return
		}
		reconciliation, err := s.PortalStore.ListClubReconciliation(ctx)
		if err != nil {
			slog.Error("load portal club reconciliation", "error", err)
			http.Error(w, "could not load portal reconciliation", http.StatusInternalServerError)
			return
		}
		notificationHealth, err := s.PortalStore.LoadNotificationDeliveryHealth(ctx)
		if err != nil {
			slog.Error("load portal notification delivery health", "error", err)
			http.Error(w, "could not load portal notification health", http.StatusInternalServerError)
			return
		}
		csrf := ""
		if cookie, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrf = cookie.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Club portal pilot")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-wrap justify-content-between align-items-start gap-3 mb-4"><div>
<p class="text-uppercase text-muted small mb-1">Controlled rollout</p><h1 class="h2">Club portal pilot</h1>
<p class="text-muted">Approve named primary contacts, then enable individual clubs and modules. Existing captain and administrator access remains unchanged.</p></div><div class="d-flex flex-wrap gap-2"><a class="btn btn-primary" href="/admin/portal/onboarding">Onboard a club</a><a class="btn btn-outline-primary" href="/admin/portal/staff">Staff roles and scopes</a><a class="btn btn-outline-primary" href="/admin/portal/competitions">Competition contexts</a><a class="btn btn-outline-primary" href="/admin/portal/messages/new">New club message</a><a class="btn btn-outline-primary" href="/admin/portal/cases">Open portal work queue</a></div></div>`)
		renderAdminPortalStatus(w, r.URL.Query().Get("status"))
		if !s.PortalEnabled {
			fmt.Fprint(w, `<div class="alert alert-warning"><strong>Global portal routes are disabled.</strong> Set <code>CLUB_PORTAL_ENABLED=true</code> on the test server after migrations and OIDC configuration are verified.</div>`)
		}
		if s.PortalOIDC == nil || !s.PortalOIDC.Enabled() {
			fmt.Fprint(w, `<div class="alert alert-warning"><strong>Managed identity is not configured.</strong> Invitations cannot be issued until the test identity provider settings are present.</div>`)
		}

		renderAdminPortalNotificationHealth(
			w,
			notificationHealth,
			email.NewFromEnv().SensitiveDeliveryConfigured(),
			s.LondonLoc,
		)

		fmt.Fprint(w, `<div class="row g-4"><div class="col-xl-5">
<section class="card shadow-sm"><div class="card-header"><strong>Manual invitation (advanced)</strong></div><div class="card-body">
<div class="alert alert-light border small"><strong>Use the guided wizard for normal setup.</strong> It checks Cognito before sending the portal link. This form is retained for recovery and existing operator procedures.</div>
<p class="small text-muted">The approver must check an authoritative official-contact source. Email is used only for this single-use onboarding link; sign-in completes at the managed identity provider.</p>
<form method="post" action="/admin/portal/invitations" class="row g-3">
<input type="hidden" name="csrf_token" value="`+escapeHTML(csrf)+`">
<div class="col-12"><label class="form-label" for="portal-club">Club</label><select class="form-select" id="portal-club" name="club_id" required><option value="">Choose club</option>`)
		for _, club := range clubs {
			fmt.Fprintf(w, `<option value="%d">%s</option>`, club.ID, escapeHTML(club.Name))
		}
		fmt.Fprint(w, `</select></div>
<div class="col-12"><label class="form-label" for="portal-email">Named official email</label><input class="form-control" id="portal-email" name="email" type="email" autocomplete="off" required></div>
<div class="col-12"><label class="form-label" for="portal-role">Role</label><select class="form-select" id="portal-role" name="role" required>
<option value="club_primary_admin">Club Primary Administrator</option>
<option value="club_admin">Club Administrator</option>
<option value="club_secretary">Club Secretary</option>
<option value="read_only_club_user">Read-only Club User</option>
</select></div>
<div class="col-12"><label class="form-label" for="portal-evidence">Official-contact evidence reference</label><input class="form-control" id="portal-evidence" name="evidence_reference" maxlength="500" placeholder="Source, roster/version and verification date" required><div class="form-text">Record a reference, not copied personal documents.</div></div>
<div class="col-12"><button class="btn btn-primary" type="submit">Approve and send onboarding link</button></div>
</form></div></section></div>
<div class="col-xl-7"><section class="card shadow-sm"><div class="card-header"><strong>Pilot feature controls</strong></div><div class="card-body">
<form method="post" action="/admin/portal/features" class="row g-3">
<input type="hidden" name="csrf_token" value="`+escapeHTML(csrf)+`">
<div class="col-md-5"><label class="form-label" for="feature-club">Club</label><select class="form-select" id="feature-club" name="club_id" required><option value="">Choose club</option>`)
		for _, club := range clubs {
			fmt.Fprintf(w, `<option value="%d">%s</option>`, club.ID, escapeHTML(club.Name))
		}
		fmt.Fprint(w, `</select></div>
<div class="col-md-4"><label class="form-label" for="feature-key">Feature</label><select class="form-select" id="feature-key" name="feature_key" required>
<option value="portal_access">Portal access</option>
<option value="read_only_dashboard">Read-only dashboard</option>
<option value="secure_messaging">Secure communication</option>
<option value="club_self_service">Club self-service and starred players</option>
<option value="junior_administration">Junior administration</option>
<option value="player_identity">Player identity</option>
<option value="registration">Registration handoff</option>
<option value="fixture_optimisation">Fixture planning</option></select></div>
<div class="col-md-3"><label class="form-label" for="feature-state">State</label><select class="form-select" id="feature-state" name="enabled"><option value="true">Enabled</option><option value="false">Disabled</option></select></div>
<div class="col-12"><label class="form-label" for="feature-notes">Rollout note</label><input class="form-control" id="feature-notes" name="notes" maxlength="500" placeholder="Pilot approval, support owner or rollback reason"></div>
<div class="col-12"><button class="btn btn-primary" type="submit">Update feature</button></div></form>
<hr><div class="table-responsive"><table class="table table-sm align-middle"><thead><tr><th>Club</th><th>Portal</th><th>Dashboard</th><th>Comms</th><th>Self-service</th><th>Junior</th><th>Identity</th><th>Registration</th><th>Fixtures</th></tr></thead><tbody>`)
		for _, club := range clubs {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				escapeHTML(club.Name),
				portalFeatureBadge(club.PortalAccess),
				portalFeatureBadge(club.ReadOnlyDashboard),
				portalFeatureBadge(club.SecureMessaging),
				portalFeatureBadge(club.ClubSelfService),
				portalFeatureBadge(club.JuniorAdministration),
				portalFeatureBadge(club.PlayerIdentity),
				portalFeatureBadge(club.Registration),
				portalFeatureBadge(club.FixtureOptimisation),
			)
		}
		fmt.Fprint(w, `</tbody></table></div></div></section></div></div>
<section class="card shadow-sm mt-4"><div class="card-header"><strong>Pilot data reconciliation</strong></div>
<div class="card-body border-bottom"><p class="small text-muted mb-0">Review these source counts before enabling a club. “Mapped” means an active team has a non-empty Play-Cricket team identifier; it does not replace representative record sign-off.</p></div>
<div class="table-responsive"><table class="table table-sm align-middle mb-0"><thead><tr><th>Club</th><th>Team mapping</th><th>Captain contacts</th><th>Portal members / roles</th><th>Latest mapped fixture sync</th></tr></thead><tbody>`)
		if len(reconciliation) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-muted">No club reconciliation rows are available.</td></tr>`)
		}
		for _, summary := range reconciliation {
			mappingState := `<span class="badge text-bg-warning">Incomplete</span>`
			if summary.TeamMappingsComplete() {
				mappingState = `<span class="badge text-bg-success">Complete</span>`
			}
			lastSync := "Unavailable"
			if summary.LastFixtureSyncAt != nil {
				lastSync = portalLocalTime(*summary.LastFixtureSyncAt, s.LondonLoc)
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s <span class="small text-muted">%d/%d active teams</span></td><td>%d</td><td>%d / %d</td><td>%s</td></tr>`,
				escapeHTML(summary.ClubName),
				mappingState,
				summary.MappedActiveTeams,
				summary.ActiveTeams,
				summary.ActiveCaptainContacts,
				summary.ActiveMemberships,
				summary.ActiveAssignments,
				escapeHTML(lastSync),
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section>
<section class="card shadow-sm mt-4"><div class="card-header"><strong>Recent onboarding approvals</strong></div><div class="table-responsive"><table class="table table-striped mb-0"><thead><tr><th>Club</th><th>Named account</th><th>Role</th><th>Status</th><th>Approved</th><th>Expires</th><th></th></tr></thead><tbody>`)
		if len(invitations) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-muted">No invitations have been approved.</td></tr>`)
		}
		for _, invitation := range invitations {
			action := ""
			if invitation.Status == "approved" && invitation.RedeemedAt == nil &&
				invitation.ExpiresAt.After(time.Now().UTC()) {
				action = fmt.Sprintf(`<form method="post" action="/admin/portal/invitations/%s/revoke"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="reason" value="revoked by Super Administrator"><button class="btn btn-sm btn-outline-danger" type="submit">Revoke</button></form>`,
					invitation.ID,
					escapeHTML(csrf),
				)
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="text-end">%s</td></tr>`,
				escapeHTML(invitation.ClubName),
				escapeHTML(invitation.Email),
				escapeHTML(humanPortalRole(invitation.Role)),
				escapeHTML(invitation.Status),
				invitation.ApprovedAt.In(s.LondonLoc).Format("2 Jan 2006 15:04"),
				invitation.ExpiresAt.In(s.LondonLoc).Format("2 Jan 2006 15:04"),
				action,
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section>
<section class="card shadow-sm mt-4"><div class="card-header"><strong>Active named-account appointments</strong></div>
<div class="card-body border-bottom"><p class="small text-muted mb-0">Revocation is immediate: the appointment disappears from acting-context selection and every session currently using it is invalidated. Continue official email notification in parallel during the pilot.</p></div>
<div class="table-responsive"><table class="table table-striped align-middle mb-0"><thead><tr><th>Club</th><th>Named account</th><th>Role</th><th>Active since</th><th>Revocation reason</th><th></th></tr></thead><tbody>`)
		if len(assignments) == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-muted">No active portal appointments exist.</td></tr>`)
		}
		for _, assignment := range assignments {
			account := assignment.DisplayName
			if assignment.Email != "" {
				account += " · " + assignment.Email
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td>
<td><form id="revoke-%s" method="post" action="/admin/portal/assignments/%s/revoke"><input type="hidden" name="csrf_token" value="%s"><input class="form-control form-control-sm" name="reason" maxlength="500" aria-label="Revocation reason for %s" required></form></td>
<td class="text-end"><button class="btn btn-sm btn-outline-danger" type="submit" form="revoke-%s">Revoke</button></td></tr>`,
				escapeHTML(assignment.ClubName),
				escapeHTML(account),
				escapeHTML(humanPortalRole(assignment.Role)),
				assignment.StartsAt.In(s.LondonLoc).Format("2 Jan 2006 15:04"),
				assignment.ID,
				assignment.ID,
				escapeHTML(csrf),
				escapeHTML(assignment.DisplayName),
				assignment.ID,
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func renderAdminPortalNotificationHealth(
	w http.ResponseWriter,
	health portal.NotificationDeliveryHealth,
	smtpConfigured bool,
	location *time.Location,
) {
	deliveryBadge := `<span class="badge text-bg-danger">SMTP unavailable</span>`
	if smtpConfigured {
		deliveryBadge = `<span class="badge text-bg-success">SMTP configured</span>`
	}
	queueBadge := `<span class="badge text-bg-success">Queue healthy</span>`
	deadLetter := health.DeadLetter + health.OutboxDeadLetter
	if deadLetter > 0 {
		queueBadge = `<span class="badge text-bg-danger">Operator action required</span>`
	} else if health.UnpublishedEvents > 0 || health.Pending > 0 ||
		health.Retrying > 0 || health.Sending > 0 {
		queueBadge = `<span class="badge text-bg-warning">Work outstanding</span>`
	}
	oldest := "None"
	if health.OldestReadyAt != nil {
		oldest = portalLocalTime(*health.OldestReadyAt, location)
	}
	fmt.Fprintf(w, `<section class="card shadow-sm mb-4" aria-labelledby="portal-delivery-health-heading">
<div class="card-header d-flex flex-wrap justify-content-between align-items-center gap-2"><strong id="portal-delivery-health-heading">Account-security notification delivery</strong><div>%s %s</div></div>
<div class="card-body"><p class="small text-muted">Account activation and appointment-revocation events are materialized idempotently and delivered by the HMAC-protected portal notification worker. Message bodies contain no invitation token, administrative reason or case content.</p>
<div class="row g-3 text-center">
<div class="col-6 col-lg"><div class="border rounded p-2"><div class="small text-muted">Unpublished events</div><strong>%d</strong></div></div>
<div class="col-6 col-lg"><div class="border rounded p-2"><div class="small text-muted">Pending</div><strong>%d</strong></div></div>
<div class="col-6 col-lg"><div class="border rounded p-2"><div class="small text-muted">Retrying</div><strong>%d</strong></div></div>
<div class="col-6 col-lg"><div class="border rounded p-2"><div class="small text-muted">Sending</div><strong>%d</strong></div></div>
<div class="col-6 col-lg"><div class="border rounded p-2"><div class="small text-muted">Dead letter</div><strong>%d</strong></div></div>
</div><p class="small text-muted mt-3 mb-0">Sent: %d · oldest ready item: %s. Schedule <code>POST /internal/process-portal-notifications</code> with the existing internal HMAC controls.</p>`,
		deliveryBadge,
		queueBadge,
		health.UnpublishedEvents,
		health.Pending,
		health.Retrying,
		health.Sending,
		deadLetter,
		health.Sent,
		escapeHTML(oldest),
	)
	if strings.TrimSpace(health.LatestError) != "" {
		fmt.Fprintf(w, `<div class="alert alert-warning mt-3 mb-0"><strong>Latest delivery error:</strong> %s</div>`,
			escapeHTML(health.LatestError),
		)
	}
	fmt.Fprint(w, `</div></section>`)
}

func (s *Server) handleAdminPortalInvitationRevoke() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		invitationID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid invitation", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := s.PortalStore.RevokeInvitation(
			ctx,
			invitationID,
			adminIDForRequest(r),
			r.FormValue("reason"),
			uuid.NewString(),
		); err != nil {
			if err == portal.ErrNotFound {
				http.Error(w, "invitation is no longer active", http.StatusNotFound)
				return
			}
			slog.Warn("revoke portal invitation failed", "error", err)
			http.Error(w, "could not revoke invitation", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal?status=invitation-revoked", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalAssignmentRevoke() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assignmentID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid appointment", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" {
			http.Error(w, "revocation reason is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := s.PortalStore.RevokeRoleAssignment(
			ctx,
			assignmentID,
			adminIDForRequest(r),
			reason,
			uuid.NewString(),
		); err != nil {
			if err == portal.ErrNotFound {
				http.Error(w, "appointment is no longer active", http.StatusNotFound)
				return
			}
			slog.Warn("revoke portal appointment failed", "error", err)
			http.Error(w, "could not revoke appointment", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal?status=assignment-revoked", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalInvitationCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.PortalEnabled || s.PortalOIDC == nil || !s.PortalOIDC.Enabled() {
			http.Error(w, "portal and managed identity must be configured before invitations are issued", http.StatusConflict)
			return
		}
		adminID := adminIDForRequest(r)
		clubID64, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("club_id")), 10, 32)
		if err != nil || clubID64 <= 0 {
			http.Error(w, "invalid club", http.StatusBadRequest)
			return
		}
		role, ok := portal.ParseRoleKey(r.FormValue("role"))
		if !ok || !adminPortalInviteRoleAllowed(role) {
			http.Error(w, "invalid onboarding role", http.StatusBadRequest)
			return
		}
		correlationID := uuid.NewString()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		invitation, err := s.PortalStore.CreateInvitation(ctx, portal.InvitationRequest{
			ClubID:                    int32(clubID64),
			Email:                     r.FormValue("email"),
			Role:                      role,
			OfficialEvidenceReference: r.FormValue("evidence_reference"),
			ApprovedByAdminID:         adminID,
			ExpiresAt:                 time.Now().UTC().Add(72 * time.Hour),
		}, correlationID)
		if err != nil {
			slog.Warn("create portal invitation failed", "error", err)
			http.Error(w, "could not create onboarding approval", http.StatusBadRequest)
			return
		}

		if err := s.sendPortalOnboardingEmail(
			r,
			strings.TrimSpace(r.FormValue("email")),
			role,
			invitation,
		); err != nil {
			revokeCtx, revokeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			revokeErr := s.PortalStore.RevokeInvitation(
				revokeCtx,
				invitation.ID,
				adminID,
				"onboarding email delivery failed",
				correlationID,
			)
			revokeCancel()
			slog.Error("send portal invitation failed", "error", err, "revoke_error", revokeErr)
			http.Error(w, "onboarding email was not delivered; the invitation was revoked", http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, "/admin/portal?status=invited", http.StatusSeeOther)
	}
}

func (s *Server) sendPortalOnboardingEmail(
	r *http.Request,
	emailAddress string,
	role portal.RoleKey,
	invitation portal.Invitation,
) error {
	link := publicBaseURL(r) + "/portal/login?invite=" +
		url.QueryEscape(invitation.RawToken)
	body := fmt.Sprintf(`You have been approved for a named GMCL Club Operations Portal account.

Role: %s

Use this single-use onboarding link:
BUTTON_URL:%s

The link expires at %s. Sign in using the same verified email address this message was sent to. Do not forward the link or create a shared club login.

Email remains the official GMCL communication record during the portal pilot. If you were not expecting this invitation, contact the GMCL Club Liaison Officer.`,
		humanPortalRole(role),
		link,
		invitation.ExpiresAt.In(s.LondonLoc).Format("2 January 2006 at 15:04"),
	)
	return email.NewFromEnv().SendSensitive(
		strings.TrimSpace(emailAddress),
		"Your GMCL Club Operations Portal invitation",
		body,
	)
}

func (s *Server) handleAdminPortalFeatureUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID := adminIDForRequest(r)
		clubID64, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("club_id")), 10, 32)
		if err != nil || clubID64 <= 0 {
			http.Error(w, "invalid club", http.StatusBadRequest)
			return
		}
		key, ok := portal.ParseFeatureKey(r.FormValue("feature_key"))
		if !ok {
			http.Error(w, "invalid pilot feature", http.StatusBadRequest)
			return
		}
		enabled, err := strconv.ParseBool(strings.TrimSpace(r.FormValue("enabled")))
		if err != nil {
			http.Error(w, "invalid feature state", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := s.PortalStore.SetClubFeature(
			ctx,
			int32(clubID64),
			key,
			enabled,
			adminID,
			r.FormValue("notes"),
			uuid.NewString(),
		); err != nil {
			slog.Warn("update portal pilot feature failed", "error", err)
			http.Error(w, "could not update pilot feature", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/admin/portal?status=feature-updated", http.StatusSeeOther)
	}
}

func adminPortalInviteRoleAllowed(role portal.RoleKey) bool {
	switch role {
	case portal.RoleClubPrimaryAdmin,
		portal.RoleClubAdmin,
		portal.RoleClubSecretary,
		portal.RoleReadOnlyClubUser:
		return true
	default:
		return false
	}
}

func portalFeatureBadge(enabled bool) string {
	if enabled {
		return `<span class="badge text-bg-success">Enabled</span>`
	}
	return `<span class="badge text-bg-secondary">Off</span>`
}

func renderAdminPortalStatus(w http.ResponseWriter, status string) {
	switch status {
	case "invited":
		fmt.Fprint(w, `<div class="alert alert-success">The named official was approved and the single-use onboarding email was sent.</div>`)
	case "feature-updated":
		fmt.Fprint(w, `<div class="alert alert-success">The pilot feature flag was updated and audited.</div>`)
	case "invitation-revoked":
		fmt.Fprint(w, `<div class="alert alert-success">The unused onboarding invitation was revoked and audited.</div>`)
	case "assignment-revoked":
		fmt.Fprint(w, `<div class="alert alert-success">The named appointment and its selected sessions were revoked immediately and audited.</div>`)
	}
}
