package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/portal"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type onboardingFeatureOption struct {
	Key         portal.FeatureKey
	Name        string
	Description string
	Available   bool
}

var onboardingFeatureOptions = []onboardingFeatureOption{
	{
		Key:         portal.FeaturePortalAccess,
		Name:        "Portal access",
		Description: "Required foundation for every named club account.",
		Available:   true,
	},
	{
		Key:         portal.FeatureReadOnlyDashboard,
		Name:        "Reports and club dashboard",
		Description: "Submission status, cards, sanctions, history and source-record drill-down.",
		Available:   true,
	},
	{
		Key:         portal.FeatureSecureMessaging,
		Name:        "Secure messaging",
		Description: "Club inbox, replies, acknowledgements and the parallel official email record.",
		Available:   true,
	},
	{
		Key:         portal.FeatureClubSelfService,
		Name:        "Club self-service",
		Description: "Contact corrections, evidence and module requests.",
		Available:   true,
	},
	{
		Key:         portal.FeatureJuniorAdministration,
		Name:        "Junior administration",
		Description: "Adult-recipient junior communications and acknowledgements.",
		Available:   true,
	},
	{
		Key:         portal.FeaturePlayerIdentity,
		Name:        "Player identity",
		Description: "Externally blocked pending ECB agreement, DPIA and photo rights.",
	},
	{
		Key:         portal.FeatureRegistration,
		Name:        "Registration redesign",
		Description: "Not yet available; Play-Cricket write capability is unconfirmed.",
	},
	{
		Key:         portal.FeatureFixtureOptimisation,
		Name:        "Fixture optimisation",
		Description: "A separate future programme; generated schedules cannot auto-publish.",
	},
}

func (s *Server) handleAdminPortalOnboardingGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		clubs, err := s.PortalStore.ListPilotClubs(ctx)
		if err != nil {
			slog.Error("list clubs for onboarding wizard", "error", err)
			http.Error(w, "could not load clubs", http.StatusInternalServerError)
			return
		}
		runs, err := s.PortalStore.ListOnboardingRuns(ctx, 25)
		if err != nil {
			slog.Error("list onboarding runs", "error", err)
			http.Error(w, "could not load onboarding history", http.StatusInternalServerError)
			return
		}
		selectedClubID, _ := strconv.ParseInt(
			strings.TrimSpace(r.URL.Query().Get("club_id")),
			10,
			32,
		)
		selectedClub := ""
		for _, club := range clubs {
			if club.ID == int32(selectedClubID) {
				selectedClub = club.Name
				break
			}
		}
		csrf := adminPortalCSRF(r)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Onboard a club")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-wrap justify-content-between align-items-start gap-3 mb-4"><div>
<p class="text-uppercase text-muted small mb-1">Guided setup</p>
<h1 class="h2">Onboard a club</h1>
<p class="text-muted mb-0">Create one named account, enable its modules and send both sign-in emails in the correct order.</p>
</div><a class="btn btn-outline-secondary" href="/admin/portal">Back to portal controls</a></div>`)
		renderOnboardingNotice(w, r.URL.Query())

		if selectedClub == "" {
			renderOnboardingSteps(w, 1)
			fmt.Fprint(w, `<section class="card shadow-sm mb-4"><div class="card-header"><strong>Step 1 — Choose the club</strong></div><div class="card-body">
<form method="get" action="/admin/portal/onboarding" class="row g-3 align-items-end">
<div class="col-lg-8"><label class="form-label" for="onboarding-club">Club</label>
<select class="form-select" id="onboarding-club" name="club_id" required><option value="">Choose club</option>`)
			for _, club := range clubs {
				fmt.Fprintf(w, `<option value="%d">%s</option>`, club.ID, escapeHTML(club.Name))
			}
			fmt.Fprint(w, `</select></div><div class="col-lg-4"><button class="btn btn-primary w-100" type="submit">Continue to named official</button></div>
</form></div></section>`)
		} else {
			renderOnboardingSteps(w, 2)
			fmt.Fprintf(w, `<section class="card shadow-sm mb-4"><div class="card-header"><strong>Step 2 — Verify the named official</strong></div><div class="card-body">
<div class="alert alert-light border"><strong>Selected club:</strong> %s <a class="ms-2" href="/admin/portal/onboarding">Change</a></div>
<p class="small text-muted">Check the person against an authoritative official-contact source. Shared club accounts are not permitted.</p>
<form method="post" action="/admin/portal/onboarding" class="row g-3">
<input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="club_id" value="%d">
<div class="col-md-6"><label class="form-label" for="official-name">Full name</label><input class="form-control" id="official-name" name="official_name" maxlength="200" autocomplete="name" required></div>
<div class="col-md-6"><label class="form-label" for="official-email">Individual email address</label><input class="form-control" id="official-email" name="email" type="email" autocomplete="email" required></div>
<div class="col-md-6"><label class="form-label" for="official-role">Portal role</label><select class="form-select" id="official-role" name="role" required>
<option value="club_primary_admin">Club Primary Administrator</option><option value="club_admin">Club Administrator</option><option value="club_secretary">Club Secretary</option><option value="read_only_club_user">Read-only Club User</option>
</select></div>
<div class="col-md-6"><label class="form-label" for="official-evidence">Official-contact evidence</label><input class="form-control" id="official-evidence" name="evidence_reference" maxlength="500" placeholder="Source, page, record or CLO reference" required></div>
<div class="col-12 d-flex justify-content-end"><button class="btn btn-primary" type="submit">Save and choose access</button></div>
</form></div></section>`, escapeHTML(selectedClub), escapeHTML(csrf), selectedClubID)
		}
		renderOnboardingHistory(w, runs, s.LondonLoc)
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPortalOnboardingCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clubID64, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("club_id")), 10, 32)
		role, roleOK := portal.ParseRoleKey(r.FormValue("role"))
		if err != nil || clubID64 <= 0 || !roleOK || !adminPortalInviteRoleAllowed(role) {
			http.Error(w, "invalid club or portal role", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		run, err := s.PortalStore.CreateOnboardingRun(
			ctx,
			portal.OnboardingRunRequest{
				ClubID:            int32(clubID64),
				OfficialName:      r.FormValue("official_name"),
				Email:             r.FormValue("email"),
				Role:              role,
				EvidenceReference: r.FormValue("evidence_reference"),
				CreatedByAdminID:  adminIDForRequest(r),
			},
			uuid.NewString(),
		)
		if err != nil {
			slog.Warn("create onboarding run failed", "error", err)
			http.Error(w, "could not start onboarding: "+safeOnboardingError(err), http.StatusBadRequest)
			return
		}
		http.Redirect(
			w,
			r,
			"/admin/portal/onboarding/"+run.ID.String()+"?step=features",
			http.StatusSeeOther,
		)
	}
}

func (s *Server) handleAdminPortalOnboardingRunGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := onboardingRunID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		run, err := s.PortalStore.GetOnboardingRun(ctx, runID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		csrf := adminPortalCSRF(r)
		step := strings.TrimSpace(r.URL.Query().Get("step"))

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Club onboarding")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-wrap justify-content-between align-items-start gap-3 mb-4"><div><p class="text-uppercase text-muted small mb-1">Club onboarding</p><h1 class="h2">%s</h1><p class="text-muted mb-0">%s · %s</p></div>
<a class="btn btn-outline-secondary" href="/admin/portal/onboarding">All onboarding</a></div>`,
			escapeHTML(run.ClubName),
			escapeHTML(run.OfficialName),
			escapeHTML(run.Email),
		)
		renderOnboardingNotice(w, r.URL.Query())
		switch {
		case run.Activated() || step == "progress":
			renderOnboardingSteps(w, 4)
			s.renderOnboardingProgress(w, r, run, csrf)
		case step == "review":
			renderOnboardingSteps(w, 4)
			s.renderOnboardingReview(w, run, csrf)
		default:
			renderOnboardingSteps(w, 3)
			renderOnboardingFeatures(w, run, csrf)
		}
		fmt.Fprint(w, `</main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPortalOnboardingFeatures() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := onboardingRunID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		features := []portal.FeatureKey{portal.FeaturePortalAccess}
		for _, value := range r.Form["feature_key"] {
			if key, ok := portal.ParseFeatureKey(value); ok && onboardingFeatureAvailable(key) {
				features = append(features, key)
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.PortalStore.UpdateOnboardingFeatures(
			ctx,
			runID,
			features,
			adminIDForRequest(r),
			uuid.NewString(),
		); err != nil {
			slog.Warn("update onboarding features failed", "error", err)
			http.Error(w, "could not save module selection", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, onboardingURL(runID, "review", "", ""), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalOnboardingActivate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := onboardingRunID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		run, err := s.PortalStore.GetOnboardingRun(ctx, runID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		adminID := adminIDForRequest(r)
		correlationID := uuid.NewString()
		configuration := portal.CognitoProvisioningConfiguration{
			Reason: "automatic Cognito provisioning is unavailable",
		}
		if s.PortalCognito != nil {
			configuration = s.PortalCognito.Configuration()
		}
		if !configuration.Enabled {
			if err := s.PortalStore.RecordOnboardingIdentity(
				ctx,
				run.ID,
				portal.OnboardingIdentityResult{
					Status: portal.OnboardingIdentityManualRequired,
				},
				adminID,
				correlationID,
			); err != nil {
				http.Error(w, "could not save the manual identity checkpoint", http.StatusBadRequest)
				return
			}
			http.Redirect(
				w,
				r,
				onboardingURL(run.ID, "progress", "manual-identity", ""),
				http.StatusSeeOther,
			)
			return
		}
		identity, err := s.PortalCognito.EnsureNamedUser(
			ctx,
			run.OfficialName,
			run.Email,
		)
		if err != nil {
			s.recordOnboardingFailure(ctx, run.ID, "identity", err, adminID, correlationID)
			slog.Error("automatic Cognito onboarding failed", "run_id", run.ID, "error", err)
			http.Redirect(
				w,
				r,
				onboardingURL(run.ID, "progress", "identity-failed", ""),
				http.StatusSeeOther,
			)
			return
		}
		identityStatus := portal.OnboardingIdentityExisting
		if identity.Created {
			identityStatus = portal.OnboardingIdentityCreated
		} else if strings.EqualFold(identity.UserStatus, "CONFIRMED") {
			identityStatus = portal.OnboardingIdentityConfirmed
		}
		if err := s.PortalStore.RecordOnboardingIdentity(
			ctx,
			run.ID,
			portal.OnboardingIdentityResult{
				Status:        identityStatus,
				Username:      identity.Username,
				UserStatus:    identity.UserStatus,
				EmailVerified: identity.EmailVerified,
			},
			adminID,
			correlationID,
		); err != nil {
			http.Error(w, "could not record the Cognito account", http.StatusBadRequest)
			return
		}
		if err := s.completePortalOnboarding(ctx, r, run.ID, adminID, correlationID); err != nil {
			slog.Error("complete portal onboarding failed", "run_id", run.ID, "error", err)
			http.Redirect(w, r, onboardingURL(run.ID, "progress", "portal-email-failed", ""), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, onboardingURL(run.ID, "progress", "setup-sent", ""), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalOnboardingConfirmManualIdentity() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := onboardingRunID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.TrimSpace(r.FormValue("confirmation")) != "confirmed" {
			http.Error(w, "confirm that the named Cognito user and email were checked", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		adminID := adminIDForRequest(r)
		correlationID := uuid.NewString()
		if err := s.PortalStore.RecordOnboardingIdentity(
			ctx,
			runID,
			portal.OnboardingIdentityResult{
				Status:        portal.OnboardingIdentityManualConfirmed,
				Username:      strings.TrimSpace(r.FormValue("cognito_username")),
				UserStatus:    "MANUALLY_VERIFIED",
				EmailVerified: true,
			},
			adminID,
			correlationID,
		); err != nil {
			http.Error(w, "could not confirm the manual Cognito checkpoint", http.StatusBadRequest)
			return
		}
		if err := s.completePortalOnboarding(ctx, r, runID, adminID, correlationID); err != nil {
			slog.Error("complete manual portal onboarding failed", "run_id", runID, "error", err)
			http.Redirect(w, r, onboardingURL(runID, "progress", "portal-email-failed", ""), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, onboardingURL(runID, "progress", "setup-sent", ""), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalOnboardingResendCognito() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := onboardingRunID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		run, err := s.PortalStore.GetOnboardingRun(ctx, runID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if s.PortalCognito == nil || !s.PortalCognito.Configuration().Enabled {
			http.Error(w, "automatic Cognito provisioning is not configured", http.StatusConflict)
			return
		}
		identity, err := s.PortalCognito.ResendInvitation(ctx, run.CognitoUsername)
		if err != nil {
			s.recordOnboardingFailure(ctx, run.ID, "identity", err, adminIDForRequest(r), uuid.NewString())
			http.Redirect(w, r, onboardingURL(run.ID, "progress", "identity-failed", ""), http.StatusSeeOther)
			return
		}
		status := portal.OnboardingIdentityInvitationResent
		if strings.EqualFold(identity.UserStatus, "CONFIRMED") {
			status = portal.OnboardingIdentityConfirmed
		}
		if err := s.PortalStore.RecordOnboardingIdentity(
			ctx,
			run.ID,
			portal.OnboardingIdentityResult{
				Status:        status,
				Username:      identity.Username,
				UserStatus:    identity.UserStatus,
				EmailVerified: identity.EmailVerified,
			},
			adminIDForRequest(r),
			uuid.NewString(),
		); err != nil {
			http.Error(w, "could not record the Cognito resend", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, onboardingURL(run.ID, "progress", "cognito-resent", ""), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalOnboardingResendPortal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := onboardingRunID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := s.completePortalOnboarding(
			ctx,
			r,
			runID,
			adminIDForRequest(r),
			uuid.NewString(),
		); err != nil {
			slog.Error("resend portal onboarding invitation failed", "run_id", runID, "error", err)
			http.Redirect(w, r, onboardingURL(runID, "progress", "portal-email-failed", ""), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, onboardingURL(runID, "progress", "portal-resent", ""), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminPortalOnboardingCorrectEmail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, err := onboardingRunID(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.PortalStore.CorrectOnboardingEmail(
			ctx,
			runID,
			r.FormValue("email"),
			adminIDForRequest(r),
			uuid.NewString(),
		); err != nil {
			http.Error(w, "could not correct the email address: "+safeOnboardingError(err), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, onboardingURL(runID, "review", "email-corrected", ""), http.StatusSeeOther)
	}
}

func (s *Server) completePortalOnboarding(
	ctx context.Context,
	r *http.Request,
	runID uuid.UUID,
	adminID int32,
	correlationID string,
) error {
	run, err := s.PortalStore.GetOnboardingRun(ctx, runID)
	if err != nil {
		return err
	}
	if !run.IdentityReady() {
		return fmt.Errorf("the named Cognito identity has not been confirmed")
	}
	if !s.PortalEnabled || s.PortalOIDC == nil || !s.PortalOIDC.Enabled() {
		return fmt.Errorf("portal and managed identity must be configured before activation")
	}
	if err := s.PortalStore.EnableOnboardingFeatures(
		ctx,
		run.ID,
		adminID,
		"Enabled by the club onboarding wizard",
		correlationID,
	); err != nil {
		return err
	}
	if err := s.PortalStore.PrepareOnboardingInvitation(
		ctx,
		run.ID,
		adminID,
		correlationID,
	); err != nil {
		return err
	}
	invitation, err := s.PortalStore.CreateInvitation(
		ctx,
		portal.InvitationRequest{
			ClubID:                    run.ClubID,
			Email:                     run.Email,
			Role:                      run.Role,
			OnboardingRunID:           &run.ID,
			OfficialEvidenceReference: run.EvidenceReference,
			ApprovedByAdminID:         adminID,
			ExpiresAt:                 time.Now().UTC().Add(72 * time.Hour),
		},
		correlationID,
	)
	if err != nil {
		s.recordOnboardingFailure(ctx, run.ID, "invitation", err, adminID, correlationID)
		return err
	}
	if err := s.PortalStore.AttachOnboardingInvitation(
		ctx,
		run.ID,
		invitation.ID,
		adminID,
		correlationID,
	); err != nil {
		return err
	}
	if err := s.sendPortalOnboardingEmail(r, run.Email, run.Role, invitation); err != nil {
		revokeErr := s.PortalStore.RevokeInvitation(
			ctx,
			invitation.ID,
			adminID,
			"onboarding email delivery failed",
			correlationID,
		)
		s.recordOnboardingFailure(ctx, run.ID, "invitation", err, adminID, correlationID)
		if revokeErr != nil {
			slog.Error("revoke failed onboarding invitation", "error", revokeErr)
		}
		return err
	}
	return s.PortalStore.MarkOnboardingInvitationSent(
		ctx,
		run.ID,
		invitation.ID,
		adminID,
		correlationID,
	)
}

func (s *Server) recordOnboardingFailure(
	ctx context.Context,
	runID uuid.UUID,
	stage string,
	err error,
	adminID int32,
	correlationID string,
) {
	if recordErr := s.PortalStore.RecordOnboardingError(
		ctx,
		runID,
		stage,
		err,
		adminID,
		correlationID,
	); recordErr != nil {
		slog.Error("record onboarding failure", "error", recordErr)
	}
}

func (s *Server) renderOnboardingReview(
	w http.ResponseWriter,
	run portal.OnboardingRun,
	csrf string,
) {
	configuration := portal.CognitoProvisioningConfiguration{
		Reason: "automatic Cognito provisioning is unavailable",
	}
	if s.PortalCognito != nil {
		configuration = s.PortalCognito.Configuration()
	}
	fmt.Fprint(w, `<section class="card shadow-sm"><div class="card-header"><strong>Step 4 — Review and activate</strong></div><div class="card-body">`)
	fmt.Fprintf(w, `<dl class="row mb-4"><dt class="col-sm-3">Club</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Named official</dt><dd class="col-sm-9">%s &lt;%s&gt;</dd><dt class="col-sm-3">Role</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Evidence</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Access</dt><dd class="col-sm-9">%s</dd></dl>`,
		escapeHTML(run.ClubName),
		escapeHTML(run.OfficialName),
		escapeHTML(run.Email),
		escapeHTML(humanPortalRole(run.Role)),
		escapeHTML(run.EvidenceReference),
		escapeHTML(onboardingFeatureSummary(run.FeatureKeys)),
	)
	if configuration.Enabled {
		fmt.Fprintf(w, `<div class="alert alert-info"><strong>Automatic identity setup is ready.</strong> Activating will create or verify the named user in Cognito pool <code>%s</code>, send Cognito's temporary-password email when needed, enable the selected modules, then send the separate GMCL portal link.</div>`, escapeHTML(configuration.UserPoolID))
	} else {
		fmt.Fprintf(w, `<div class="alert alert-warning"><strong>One manual Cognito checkpoint is required.</strong> %s. The wizard will pause with exact instructions and will not send the portal link early.</div>`, escapeHTML(configuration.Reason))
	}
	fmt.Fprintf(w, `<div class="d-flex flex-wrap justify-content-between gap-2"><a class="btn btn-outline-secondary" href="/admin/portal/onboarding/%s?step=features">Back to access</a><form method="post" action="/admin/portal/onboarding/%s/activate"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-primary" type="submit">Activate onboarding</button></form></div>`,
		run.ID,
		run.ID,
		escapeHTML(csrf),
	)
	fmt.Fprint(w, `</div></section>`)
}

func (s *Server) renderOnboardingProgress(
	w http.ResponseWriter,
	r *http.Request,
	run portal.OnboardingRun,
	csrf string,
) {
	configuration := portal.CognitoProvisioningConfiguration{
		Reason: "automatic Cognito provisioning is unavailable",
	}
	if s.PortalCognito != nil {
		configuration = s.PortalCognito.Configuration()
	}
	fmt.Fprint(w, `<div class="row g-4"><div class="col-xl-8"><section class="card shadow-sm"><div class="card-header"><strong>Activation progress</strong></div><div class="card-body">`)
	renderOnboardingCheck(w, "Named Cognito account", run.IdentityReady(), onboardingIdentityDescription(run))
	renderOnboardingCheck(w, "Selected portal modules", portal.OnboardingFeaturesComplete(run), onboardingFeatureSummary(run.FeatureKeys))
	inviteSent := run.CurrentInvitationID != nil && run.CurrentInvitationStatus == "approved"
	if run.CurrentInvitationRedeemed != nil {
		inviteSent = true
	}
	renderOnboardingCheck(w, "GMCL portal link", inviteSent, onboardingInvitationDescription(run, s.LondonLoc))
	renderOnboardingCheck(w, "Club has signed in", run.Activated(), onboardingActivationDescription(run, s.LondonLoc))
	if run.LastError != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger mt-3 mb-0"><strong>The last action failed.</strong> %s</div>`, escapeHTML(safeOnboardingError(errors.New(run.LastError))))
	}
	fmt.Fprint(w, `</div></section></div><div class="col-xl-4"><section class="card shadow-sm"><div class="card-header"><strong>Next action</strong></div><div class="card-body">`)
	switch {
	case run.Activated():
		fmt.Fprint(w, `<div class="alert alert-success"><strong>Complete.</strong> The named official has redeemed the invitation and can access the club portal.</div><a class="btn btn-primary w-100" href="/admin/portal">Return to portal controls</a>`)
	case run.IdentityStatus == portal.OnboardingIdentityManualRequired ||
		run.IdentityStatus == portal.OnboardingIdentityFailed:
		fmt.Fprintf(w, `<p>Create or check the named user in the AWS Cognito console:</p><ol class="small"><li>Open the configured GMCL portal user pool.</li><li>Create a user with username/email <strong>%s</strong>.</li><li>Set the name to <strong>%s</strong> and confirm the email is verified.</li><li>Make sure Cognito has sent its temporary-password email.</li><li>Tick the confirmation below.</li></ol>
<form method="post" action="/admin/portal/onboarding/%s/confirm-manual-identity">
<input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="cognito_username" value="%s">
<div class="form-check mb-3"><input class="form-check-input" type="checkbox" value="confirmed" name="confirmation" id="manual-confirm" required><label class="form-check-label" for="manual-confirm">I checked this exact named Cognito user and verified email.</label></div>
<button class="btn btn-primary w-100" type="submit">Confirm and send portal link</button></form>`,
			escapeHTML(run.Email),
			escapeHTML(run.OfficialName),
			run.ID,
			escapeHTML(csrf),
			escapeHTML(run.Email),
		)
	case run.IdentityReady() && !inviteSent:
		fmt.Fprintf(w, `<p>The identity is ready, but the portal email has not been delivered.</p><form method="post" action="/admin/portal/onboarding/%s/resend-portal"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-primary w-100" type="submit">Send a new portal link</button></form>`, run.ID, escapeHTML(csrf))
	default:
		fmt.Fprint(w, `<p>The two emails have been sent. The club official should first complete Cognito's temporary-password sign-in, then use the GMCL portal link.</p><div class="alert alert-light border small mb-0">No approval work is needed while you wait. This page changes to complete when the portal link is redeemed.</div>`)
	}
	fmt.Fprint(w, `</div></section>`)
	if !run.Activated() {
		fmt.Fprintf(w, `<section class="card shadow-sm mt-4"><div class="card-header"><strong>Recovery</strong></div><div class="card-body">`)
		if configuration.Enabled && run.CognitoUsername != "" {
			fmt.Fprintf(w, `<form method="post" action="/admin/portal/onboarding/%s/resend-cognito" class="mb-3"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-outline-primary w-100" type="submit">Resend Cognito email</button></form>`, run.ID, escapeHTML(csrf))
		}
		if run.IdentityReady() {
			fmt.Fprintf(w, `<form method="post" action="/admin/portal/onboarding/%s/resend-portal" class="mb-3"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-outline-primary w-100" type="submit">Send new portal link</button></form>`, run.ID, escapeHTML(csrf))
		}
		fmt.Fprintf(w, `<form method="post" action="/admin/portal/onboarding/%s/correct-email"><input type="hidden" name="csrf_token" value="%s"><label class="form-label" for="correct-email">Correct email address</label><input class="form-control mb-2" id="correct-email" name="email" type="email" value="%s" required><button class="btn btn-outline-danger w-100" type="submit">Correct and restart checks</button></form>`, run.ID, escapeHTML(csrf), escapeHTML(run.Email))
		fmt.Fprint(w, `</div></section>`)
	}
	fmt.Fprint(w, `</div></div>`)
}

func renderOnboardingFeatures(
	w http.ResponseWriter,
	run portal.OnboardingRun,
	csrf string,
) {
	selected := make(map[portal.FeatureKey]bool, len(run.FeatureKeys))
	for _, feature := range run.FeatureKeys {
		selected[feature] = true
	}
	fmt.Fprint(w, `<section class="card shadow-sm"><div class="card-header"><strong>Step 3 — Choose portal access</strong></div><div class="card-body"><p class="text-muted">The recommended pilot modules are selected. Future programmes remain unavailable until their dependencies are resolved.</p>`)
	fmt.Fprintf(w, `<form method="post" action="/admin/portal/onboarding/%s/features"><input type="hidden" name="csrf_token" value="%s"><div class="row g-3">`, run.ID, escapeHTML(csrf))
	for _, option := range onboardingFeatureOptions {
		checked := ""
		if selected[option.Key] {
			checked = " checked"
		}
		disabled := ""
		badge := ""
		if option.Key == portal.FeaturePortalAccess || !option.Available {
			disabled = " disabled"
		}
		if option.Key == portal.FeaturePortalAccess {
			badge = `<span class="badge text-bg-primary ms-2">Required</span>`
		} else if !option.Available {
			badge = `<span class="badge text-bg-secondary ms-2">Future</span>`
		}
		fmt.Fprintf(w, `<div class="col-lg-6"><div class="border rounded p-3 h-100"><div class="form-check"><input class="form-check-input" type="checkbox" name="feature_key" value="%s" id="feature-%s"%s%s><label class="form-check-label fw-semibold" for="feature-%s">%s%s</label></div><p class="small text-muted mb-0 mt-2">%s</p></div></div>`,
			option.Key,
			option.Key,
			checked,
			disabled,
			option.Key,
			escapeHTML(option.Name),
			badge,
			escapeHTML(option.Description),
		)
	}
	fmt.Fprint(w, `</div><div class="d-flex justify-content-between gap-2 mt-4"><a class="btn btn-outline-secondary" href="/admin/portal/onboarding">Cancel</a><button class="btn btn-primary" type="submit">Review activation</button></div></form></div></section>`)
}

func renderOnboardingSteps(w http.ResponseWriter, active int) {
	labels := []string{"Choose club", "Named official", "Access", "Activate"}
	fmt.Fprint(w, `<ol class="row g-2 list-unstyled mb-4" aria-label="Onboarding progress">`)
	for index, label := range labels {
		step := index + 1
		className := "border rounded p-3 bg-light text-muted h-100"
		if step < active {
			className = "border border-success rounded p-3 bg-success-subtle text-success-emphasis h-100"
		} else if step == active {
			className = "border border-primary rounded p-3 bg-primary-subtle text-primary-emphasis h-100"
		}
		fmt.Fprintf(w, `<li class="col-6 col-lg-3"><div class="%s"><span class="small text-uppercase">Step %d</span><div class="fw-semibold">%s</div></div></li>`, className, step, escapeHTML(label))
	}
	fmt.Fprint(w, `</ol>`)
}

func renderOnboardingHistory(
	w http.ResponseWriter,
	runs []portal.OnboardingRun,
	location *time.Location,
) {
	fmt.Fprint(w, `<section class="card shadow-sm"><div class="card-header"><strong>Recent onboarding</strong></div><div class="table-responsive"><table class="table table-hover align-middle mb-0"><thead><tr><th>Club</th><th>Named official</th><th>Status</th><th>Updated</th><th></th></tr></thead><tbody>`)
	if len(runs) == 0 {
		fmt.Fprint(w, `<tr><td colspan="5" class="text-muted">No onboarding runs yet.</td></tr>`)
	}
	for _, run := range runs {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s<br><span class="small text-muted">%s</span></td><td>%s</td><td>%s</td><td class="text-end"><a class="btn btn-sm btn-outline-primary" href="/admin/portal/onboarding/%s?step=progress">Open</a></td></tr>`,
			escapeHTML(run.ClubName),
			escapeHTML(run.OfficialName),
			escapeHTML(run.Email),
			onboardingStatusBadge(run),
			run.UpdatedAt.In(location).Format("2 Jan 2006 15:04"),
			run.ID,
		)
	}
	fmt.Fprint(w, `</tbody></table></div></section>`)
}

func renderOnboardingCheck(
	w http.ResponseWriter,
	title string,
	complete bool,
	description string,
) {
	icon := `<span class="badge text-bg-secondary">Waiting</span>`
	if complete {
		icon = `<span class="badge text-bg-success">Complete</span>`
	}
	fmt.Fprintf(w, `<div class="d-flex justify-content-between align-items-start gap-3 border-bottom py-3"><div><div class="fw-semibold">%s</div><div class="small text-muted">%s</div></div>%s</div>`, escapeHTML(title), escapeHTML(description), icon)
}

func renderOnboardingNotice(w http.ResponseWriter, values url.Values) {
	switch values.Get("status") {
	case "manual-identity":
		fmt.Fprint(w, `<div class="alert alert-warning">The wizard paused before the portal email. Complete the one Cognito checkpoint shown below.</div>`)
	case "identity-failed":
		fmt.Fprint(w, `<div class="alert alert-danger">Cognito could not create or check the named user. Review the error and use the recovery action.</div>`)
	case "portal-email-failed":
		fmt.Fprint(w, `<div class="alert alert-danger">The portal email was not delivered. Its link was revoked, so it is safe to send a new one.</div>`)
	case "setup-sent":
		fmt.Fprint(w, `<div class="alert alert-success">The identity checkpoint and module setup are complete, and the GMCL portal email was sent. Club activation completes when that link is redeemed.</div>`)
	case "cognito-resent":
		fmt.Fprint(w, `<div class="alert alert-success">The Cognito welcome email was requested again.</div>`)
	case "portal-resent":
		fmt.Fprint(w, `<div class="alert alert-success">The previous unused portal link was revoked and a fresh one was sent.</div>`)
	case "email-corrected":
		fmt.Fprint(w, `<div class="alert alert-success">The email was corrected. Identity and invitation checks have been reset.</div>`)
	}
}

func onboardingRunID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
}

func onboardingURL(
	runID uuid.UUID,
	step string,
	status string,
	message string,
) string {
	values := url.Values{}
	if step != "" {
		values.Set("step", step)
	}
	if status != "" {
		values.Set("status", status)
	}
	if message != "" {
		values.Set("message", message)
	}
	return "/admin/portal/onboarding/" + runID.String() + "?" + values.Encode()
}

func onboardingFeatureAvailable(key portal.FeatureKey) bool {
	for _, option := range onboardingFeatureOptions {
		if option.Key == key {
			return option.Available
		}
	}
	return false
}

func onboardingFeatureSummary(features []portal.FeatureKey) string {
	var labels []string
	for _, feature := range portal.SortedOnboardingFeatures(features) {
		for _, option := range onboardingFeatureOptions {
			if option.Key == feature {
				labels = append(labels, option.Name)
				break
			}
		}
	}
	return strings.Join(labels, ", ")
}

func onboardingIdentityDescription(run portal.OnboardingRun) string {
	if run.IdentityStatus == portal.OnboardingIdentityManualRequired {
		return "Waiting for the manual Cognito checkpoint."
	}
	if run.IdentityStatus == portal.OnboardingIdentityFailed {
		return "The last Cognito operation failed."
	}
	if run.IdentityReady() {
		status := run.CognitoUserStatus
		if status == "" {
			status = "manually checked"
		}
		return "Ready (" + status + ")."
	}
	return "Not checked yet."
}

func onboardingInvitationDescription(
	run portal.OnboardingRun,
	location *time.Location,
) string {
	if run.CurrentInvitationRedeemed != nil {
		return "Redeemed " + run.CurrentInvitationRedeemed.In(location).Format("2 Jan 2006 at 15:04") + "."
	}
	if run.CurrentInvitationID == nil {
		return "Not sent yet."
	}
	if run.CurrentInvitationExpires != nil {
		return strings.ToUpper(run.CurrentInvitationStatus) + "; expires " +
			run.CurrentInvitationExpires.In(location).Format("2 Jan 2006 at 15:04") + "."
	}
	return strings.ToUpper(run.CurrentInvitationStatus) + "."
}

func onboardingActivationDescription(
	run portal.OnboardingRun,
	location *time.Location,
) string {
	if run.ActivatedAt != nil {
		return "Activated " + run.ActivatedAt.In(location).Format("2 Jan 2006 at 15:04") + "."
	}
	return "Waiting for the named official to redeem the portal link."
}

func onboardingStatusBadge(run portal.OnboardingRun) string {
	switch {
	case run.Activated():
		return `<span class="badge text-bg-success">Activated</span>`
	case run.Status == "invitation_sent":
		return `<span class="badge text-bg-primary">Waiting for club</span>`
	case run.IdentityStatus == portal.OnboardingIdentityManualRequired:
		return `<span class="badge text-bg-warning">Manual checkpoint</span>`
	case run.IdentityStatus == portal.OnboardingIdentityFailed || run.LastError != "":
		return `<span class="badge text-bg-danger">Needs attention</span>`
	default:
		return `<span class="badge text-bg-secondary">Draft</span>`
	}
}

func safeOnboardingError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "active onboarding run already exists"):
		return "an active onboarding run already exists for this club and email"
	case strings.Contains(lower, "email is invalid"):
		return "the named official email address is invalid"
	case strings.Contains(lower, "evidence"):
		return "an official-contact evidence reference is required"
	case strings.Contains(lower, "cognito"):
		return "Cognito could not complete this step; check the server log and provisioning permission"
	case strings.Contains(lower, "email delivery"),
		strings.Contains(lower, "smtp"),
		strings.Contains(lower, "ses"):
		return "the email service did not accept the message"
	default:
		return "the requested onboarding step could not be completed"
	}
}
