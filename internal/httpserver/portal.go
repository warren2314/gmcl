package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/portal"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const portalSessionCookie = "gmcl_portal_session"

type portalPrincipalContextKey struct{}

func (s *Server) portalRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RateLimit(60))
	r.Use(middleware.CSRFMiddleware)

	r.Get("/login", s.handlePortalLogin())
	r.Get("/auth/callback", s.handlePortalAuthCallback())

	r.Group(func(r chi.Router) {
		r.Use(s.requirePortalSession())
		r.Get("/", s.handlePortalHome())
		r.Get("/contexts", s.handlePortalContexts())
		r.Post("/contexts", s.handlePortalContextSelect())
		r.Get("/sessions", s.handlePortalSessions())
		r.Post("/sessions/revoke-all", s.handlePortalRevokeAllSessions())
		r.Post("/sessions/{sessionID}/revoke", s.handlePortalRevokeSession())
		r.Get("/step-up", s.handlePortalStepUp())
		r.Post("/logout", s.handlePortalLogout())
	})
	return r
}

func (s *Server) requirePortalSession() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(portalSessionCookie)
			if err != nil || strings.TrimSpace(cookie.Value) == "" {
				redirectPortalLogin(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			principal, err := s.PortalStore.Authenticate(ctx, cookie.Value)
			if err != nil {
				clearPortalSessionCookie(w)
				redirectPortalLogin(w, r)
				return
			}
			ctx = context.WithValue(r.Context(), portalPrincipalContextKey{}, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func redirectPortalLogin(w http.ResponseWriter, r *http.Request) {
	returnTo := r.URL.Path
	if r.URL.RawQuery != "" {
		returnTo += "?" + r.URL.RawQuery
	}
	if !strings.HasPrefix(returnTo, "/portal") {
		returnTo = "/portal"
	}
	http.Redirect(
		w,
		r,
		"/portal/login?return_to="+url.QueryEscape(returnTo),
		http.StatusSeeOther,
	)
}

func portalPrincipalForRequest(r *http.Request) (portal.Principal, bool) {
	principal, ok := r.Context().Value(portalPrincipalContextKey{}).(portal.Principal)
	return principal, ok
}

func portalClientDetails(r *http.Request) portal.ClientDetails {
	ipAddress := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(ipAddress); err == nil {
		ipAddress = host
	}
	if net.ParseIP(ipAddress) == nil {
		ipAddress = ""
	}
	return portal.ClientDetails{
		IPAddress: ipAddress,
		UserAgent: r.UserAgent(),
	}
}

func (s *Server) handlePortalLogin() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.PortalOIDC == nil || !s.PortalOIDC.Enabled() {
			renderPortalUnavailable(w, "Named-account sign-in is not configured on this environment.")
			return
		}
		returnTo := strings.TrimSpace(r.URL.Query().Get("return_to"))
		if returnTo == "" {
			returnTo = "/portal"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		result, err := s.PortalOIDC.BeginLogin(
			ctx,
			returnTo,
			strings.TrimSpace(r.URL.Query().Get("invite")),
		)
		if err != nil {
			slog.Warn("portal OIDC login could not start", "error", err)
			renderPortalUnavailable(w, "Sign-in is temporarily unavailable. Please try again shortly.")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, result.AuthorizationURL, http.StatusSeeOther)
	}
}

func (s *Server) handlePortalAuthCallback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.PortalOIDC == nil || !s.PortalOIDC.Enabled() {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("error") != "" {
			renderPortalSignInFailed(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := s.PortalOIDC.CompleteLogin(
			ctx,
			r.URL.Query().Get("state"),
			r.URL.Query().Get("code"),
			portalClientDetails(r),
		)
		if err != nil {
			if !errors.Is(err, portal.ErrUnauthenticated) {
				slog.Error("portal OIDC callback failed", "error", err)
			}
			renderPortalSignInFailed(w)
			return
		}
		setPortalSessionCookie(w, result.RawSessionToken, result.Principal.AbsoluteExpiresAt)
		http.Redirect(w, r, result.ReturnTo, http.StatusSeeOther)
	}
}

func (s *Server) handlePortalContexts() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		contexts, err := s.PortalStore.ListActingContexts(ctx, principal)
		if err != nil {
			slog.Error("load portal acting contexts", "error", err)
			http.Error(w, "could not load club access", http.StatusInternalServerError)
			return
		}
		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Choose club access")
		writePortalNav(w, csrf, principal)
		fmt.Fprint(w, `<main class="container pb-5"><div class="row justify-content-center"><div class="col-xl-8">
<div class="d-flex justify-content-between align-items-start mb-4"><div><p class="text-uppercase text-muted small mb-1">Named account</p><h1 class="h2">Choose how you are acting</h1><p class="text-muted">Your club, role and season scope are checked again on every request.</p></div></div>`)
		if len(contexts) == 0 {
			fmt.Fprint(w, `<div class="alert alert-info"><strong>No pilot access is active.</strong> Your identity is valid, but no approved appointment currently belongs to a club with portal access enabled. Contact the GMCL Club Liaison Officer.</div>`)
		} else {
			fmt.Fprint(w, `<div class="row g-3">`)
			for _, acting := range contexts {
				scope := []string{humanPortalRole(acting.Assignment.Role)}
				if acting.TeamName != "" {
					scope = append(scope, acting.TeamName)
				}
				if acting.SeasonName != "" {
					scope = append(scope, acting.SeasonName)
				}
				if acting.CompetitionName != "" {
					scope = append(scope, acting.CompetitionName)
				}
				fmt.Fprintf(w, `<div class="col-md-6"><div class="card h-100 shadow-sm"><div class="card-body">
<h2 class="h5">%s</h2><p class="text-muted">%s</p>
<form method="post" action="/portal/contexts">
<input type="hidden" name="csrf_token" value="%s">
<input type="hidden" name="assignment_id" value="%s">
<button class="btn btn-primary">Continue as this role</button>
</form></div></div></div>`,
					escapeHTML(acting.ClubName),
					escapeHTML(strings.Join(scope, " · ")),
					escapeHTML(csrf),
					acting.Assignment.ID,
				)
			}
			fmt.Fprint(w, `</div>`)
		}
		fmt.Fprint(w, `</div></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) handlePortalContextSelect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		assignmentID, err := uuid.Parse(strings.TrimSpace(r.FormValue("assignment_id")))
		if err != nil {
			http.Error(w, "invalid access selection", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		selected, token, err := s.PortalStore.SelectActingContext(
			ctx,
			principal,
			assignmentID,
			portalClientDetails(r),
		)
		if err != nil {
			if errors.Is(err, portal.ErrForbidden) || errors.Is(err, portal.ErrUnauthenticated) {
				http.Error(w, "access selection is no longer available", http.StatusForbidden)
				return
			}
			slog.Error("select portal acting context", "error", err)
			http.Error(w, "could not select club access", http.StatusInternalServerError)
			return
		}
		setPortalSessionCookie(w, token, selected.AbsoluteExpiresAt)
		http.Redirect(w, r, "/portal", http.StatusSeeOther)
	}
}

func (s *Server) handlePortalHome() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if principal.Assignment == nil {
			http.Redirect(w, r, "/portal/contexts", http.StatusSeeOther)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		enabled, err := s.PortalStore.FeatureEnabled(ctx, principal, portal.FeatureReadOnlyDashboard)
		if err != nil {
			slog.Error("load portal dashboard feature", "error", err)
			http.Error(w, "could not load portal", http.StatusInternalServerError)
			return
		}
		if !enabled {
			http.Error(w, "this portal module is not enabled for the selected club", http.StatusForbidden)
			return
		}
		dashboard, err := s.PortalStore.LoadClubDashboard(ctx, principal)
		if err != nil {
			slog.Error("load tenant-scoped portal dashboard", "error", err)
			http.Error(w, "could not load club action centre", http.StatusInternalServerError)
			return
		}

		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Club action centre")
		writePortalNav(w, csrf, principal)
		fmt.Fprintf(w, `<main class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-wrap justify-content-between align-items-start gap-3 mb-4">
<div><p class="text-uppercase text-muted small mb-1">Club Operations Portal</p><h1 class="h2 mb-1">Action centre</h1>
<p class="text-muted mb-0">%s · %s · Acting as %s. Official email processes continue in parallel during the pilot.</p></div>
<a class="btn btn-outline-primary" href="/portal/contexts">Change club or role</a></div>
<div class="row g-3">
<div class="col-md-6 col-xl-3"><div class="card h-100 shadow-sm"><div class="card-body"><p class="text-muted small">Reports requiring attention</p><p class="display-6 mb-0">%s</p><p class="small text-muted mt-2 mb-0">%s</p></div></div></div>
<div class="col-md-6 col-xl-3"><div class="card h-100 shadow-sm"><div class="card-body"><p class="text-muted small">Submitted reports</p><p class="display-6 mb-0">%s</p><p class="small text-muted mt-2 mb-0">%s</p></div></div></div>
<div class="col-md-6 col-xl-3"><div class="card h-100 shadow-sm"><div class="card-body"><p class="text-muted small">Team-level card ledger</p><p class="display-6 mb-0">%d<span class="fs-5 text-muted"> yellow</span></p><p class="small text-muted mt-2 mb-0">%d red · %d points deduction</p></div></div></div>
<div class="col-md-6 col-xl-3"><div class="card h-100 shadow-sm"><div class="card-body"><p class="text-muted small">Play-Cricket source</p><p class="h4 mb-0">%s</p><p class="small text-muted mt-2 mb-0">%s</p></div></div></div>
</div>`,
			escapeHTML(dashboard.ClubName),
			escapeHTML(dashboard.SeasonName),
			escapeHTML(humanPortalRole(principal.Assignment.Role)),
			portalReportAttentionValue(dashboard),
			escapeHTML(portalReportAttentionNote(dashboard)),
			portalSubmittedValue(dashboard),
			escapeHTML(portalSubmittedNote(dashboard)),
			dashboard.Sanctions.Yellow,
			dashboard.Sanctions.Red,
			dashboard.Sanctions.PointsDeduction,
			escapeHTML(portalSourceHeadline(dashboard)),
			escapeHTML(portalSourceNote(dashboard, s.LondonLoc)),
		)
		if dashboard.Sanctions.UnreconciledLegacy > 0 {
			fmt.Fprintf(w, `<div class="alert alert-warning mt-4"><strong>Reconciliation required.</strong> %d legacy sanction row(s) are not linked to the case ledger, so they are excluded from the derived totals above rather than silently double-counted.</div>`, dashboard.Sanctions.UnreconciledLegacy)
		}
		fmt.Fprint(w, `<section class="card shadow-sm mt-4"><div class="card-header d-flex justify-content-between align-items-center"><strong>Team card and points ledger</strong><span class="small text-muted">Club total is the sum of these rows</span></div><div class="table-responsive"><table class="table table-striped mb-0"><thead><tr><th>Team</th><th class="text-end">Yellow</th><th class="text-end">Red</th><th class="text-end">Points deduction</th></tr></thead><tbody>`)
		if len(dashboard.TeamSanctions) == 0 {
			fmt.Fprint(w, `<tr><td colspan="4" class="text-muted">No non-zero team ledger balance exists for this season and scope.</td></tr>`)
		}
		for _, team := range dashboard.TeamSanctions {
			fmt.Fprintf(w, `<tr><td>%s</td><td class="text-end">%d</td><td class="text-end">%d</td><td class="text-end">%d</td></tr>`,
				escapeHTML(team.TeamName),
				team.Yellow,
				team.Red,
				team.PointsDeduction,
			)
		}
		fmt.Fprintf(w, `</tbody><tfoot><tr class="table-light"><th>Club total</th><th class="text-end">%d</th><th class="text-end">%d</th><th class="text-end">%d</th></tr></tfoot></table></div></section>
<p class="small text-muted mt-3">Calculated %s. Report requirements are derived from mapped Play-Cricket fixtures, existing submissions and approved exemptions. Late means received after the Wednesday 23:59 Europe/London deadline. This read-only pilot does not amend any source record.</p>
</main>`,
			dashboard.Sanctions.Yellow,
			dashboard.Sanctions.Red,
			dashboard.Sanctions.PointsDeduction,
			dashboard.CalculatedAt.In(s.LondonLoc).Format("2 January 2006 at 15:04 MST"),
		)
		pageFooter(w)
	}
}

func portalReportAttentionValue(dashboard portal.ClubDashboard) string {
	if dashboard.LastFixtureSyncAt == nil {
		return "—"
	}
	return strconv.FormatInt(dashboard.Reports.Due+dashboard.Reports.Missed, 10)
}

func portalReportAttentionNote(dashboard portal.ClubDashboard) string {
	if dashboard.LastFixtureSyncAt == nil {
		return "Fixture source has not been reconciled; no zero is inferred"
	}
	return fmt.Sprintf("%d due · %d missed · %d exempt", dashboard.Reports.Due, dashboard.Reports.Missed, dashboard.Reports.Exempt)
}

func portalSubmittedValue(dashboard portal.ClubDashboard) string {
	if dashboard.LastFixtureSyncAt == nil {
		return "—"
	}
	return strconv.FormatInt(dashboard.Reports.Submitted, 10)
}

func portalSubmittedNote(dashboard portal.ClubDashboard) string {
	if dashboard.LastFixtureSyncAt == nil {
		return "Submission status unavailable until fixture mapping is present"
	}
	return fmt.Sprintf("%.1f%% satisfied · %d received late", dashboard.Reports.CompletionPercent(), dashboard.Reports.Late)
}

func portalSourceHeadline(dashboard portal.ClubDashboard) string {
	if dashboard.LastFixtureSyncAt == nil {
		return "Unavailable"
	}
	if dashboard.FixtureSourceStale {
		return "Stale"
	}
	return "Current"
}

func portalSourceNote(dashboard portal.ClubDashboard, location *time.Location) string {
	if dashboard.LastFixtureSyncAt == nil {
		return "No successful fixture synchronization is visible for this scope"
	}
	if location == nil {
		location = time.UTC
	}
	return "Last synchronized " + dashboard.LastFixtureSyncAt.In(location).Format("2 Jan 2006 15:04 MST")
}

func (s *Server) handlePortalLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if ok {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			_ = s.PortalStore.RevokeSession(ctx, principal, "user signed out")
			cancel()
		}
		clearPortalSessionCookie(w)
		http.Redirect(w, r, "/portal/login", http.StatusSeeOther)
	}
}

func (s *Server) handlePortalSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		sessions, err := s.PortalStore.ListUserSessions(ctx, principal)
		if err != nil {
			slog.Error("load portal sessions", "error", err)
			http.Error(w, "could not load account sessions", http.StatusInternalServerError)
			return
		}

		csrf := portalCSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Account sessions")
		writePortalNav(w, csrf, principal)
		fmt.Fprint(w, `<main class="container pb-5"><div class="row justify-content-center"><div class="col-xl-9">
<div class="mb-4"><p class="text-uppercase text-muted small mb-1">Named account security</p><h1 class="h2">Account sessions</h1>
<p class="text-muted">Review where your account is signed in and revoke access immediately. Session identifiers and bearer tokens are never displayed.</p></div>
<div class="card shadow-sm"><div class="table-responsive"><table class="table align-middle mb-0"><thead><tr><th>Session</th><th>Club context</th><th>Last used</th><th>Expires</th><th></th></tr></thead><tbody>`)
		if len(sessions) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-muted">No active sessions were found.</td></tr>`)
		}
		for _, session := range sessions {
			device := strings.TrimSpace(session.UserAgent)
			if device == "" {
				device = "Unknown browser"
			}
			network := strings.TrimSpace(session.IPAddress)
			if network != "" {
				device += " · " + network
			}
			contextName := "No club role selected"
			if session.ClubName != "" {
				contextName = session.ClubName
				if session.Role != "" {
					contextName += " · " + humanPortalRole(session.Role)
				}
			}
			currentLabel := ""
			if session.Current {
				currentLabel = ` <span class="badge text-bg-primary">Current</span>`
			}
			action := fmt.Sprintf(`<form method="post" action="/portal/sessions/%s/revoke"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-sm btn-outline-danger" type="submit">Revoke</button></form>`,
				session.ID,
				escapeHTML(csrf),
			)
			fmt.Fprintf(w, `<tr><td><div>%s%s</div><div class="small text-muted">%s</div></td><td>%s</td><td>%s</td><td>%s</td><td class="text-end">%s</td></tr>`,
				escapeHTML(device),
				currentLabel,
				escapeHTML("Signed in "+portalLocalTime(session.AuthenticatedAt, s.LondonLoc)),
				escapeHTML(contextName),
				escapeHTML(portalLocalTime(session.LastSeenAt, s.LondonLoc)),
				escapeHTML(portalLocalTime(session.AbsoluteExpiresAt, s.LondonLoc)),
				action,
			)
		}
		fmt.Fprint(w, `</tbody></table></div></div>
<div class="card border-danger mt-4"><div class="card-body"><h2 class="h5">Sign out everywhere</h2>
<p class="text-muted">This increments your account security version and invalidates every current portal session, including this one. A recent strong authentication is required.</p>`)
		if s.PortalStore.StepUpRequired(principal) {
			fmt.Fprint(w, `<a class="btn btn-outline-danger" href="/portal/step-up">Verify identity to continue</a>`)
		} else {
			fmt.Fprintf(w, `<form method="post" action="/portal/sessions/revoke-all"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-danger" type="submit">Sign out everywhere</button></form>`, escapeHTML(csrf))
		}
		fmt.Fprint(w, `</div></div></div></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) handlePortalRevokeSession() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		sessionID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "sessionID")))
		if err != nil {
			http.Error(w, "invalid session", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := s.PortalStore.RevokeUserSession(
			ctx,
			principal,
			sessionID,
			"revoked by account holder",
		); err != nil {
			if errors.Is(err, portal.ErrNotFound) {
				http.Error(w, "session is no longer active", http.StatusNotFound)
				return
			}
			slog.Error("revoke portal session", "error", err)
			http.Error(w, "could not revoke session", http.StatusInternalServerError)
			return
		}
		if sessionID == principal.SessionID {
			clearPortalSessionCookie(w)
			http.Redirect(w, r, "/portal/login", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/portal/sessions", http.StatusSeeOther)
	}
}

func (s *Server) handlePortalRevokeAllSessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		err := s.PortalStore.RevokeAllUserSessions(
			ctx,
			principal,
			"all sessions revoked by account holder",
		)
		if errors.Is(err, portal.ErrStepUpRequired) {
			http.Redirect(w, r, "/portal/step-up", http.StatusSeeOther)
			return
		}
		if err != nil {
			slog.Error("revoke all portal sessions", "error", err)
			http.Error(w, "could not revoke all sessions", http.StatusInternalServerError)
			return
		}
		clearPortalSessionCookie(w)
		http.Redirect(w, r, "/portal/login", http.StatusSeeOther)
	}
}

func (s *Server) handlePortalStepUp() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := portalPrincipalForRequest(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if s.PortalOIDC == nil || !s.PortalOIDC.Enabled() {
			renderPortalUnavailable(w, "Strong authentication is not configured on this environment.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		result, err := s.PortalOIDC.BeginStepUp(ctx, "/portal/sessions", principal)
		if err != nil {
			slog.Error("portal step-up could not start", "error", err)
			renderPortalUnavailable(w, "Strong authentication is temporarily unavailable.")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, result.AuthorizationURL, http.StatusSeeOther)
	}
}

func portalLocalTime(value time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	return value.In(location).Format("2 Jan 2006 15:04 MST")
}

func setPortalSessionCookie(w http.ResponseWriter, rawToken string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     portalSessionCookie,
		Value:    rawToken,
		Path:     "/portal",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearPortalSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     portalSessionCookie,
		Value:    "",
		Path:     "/portal",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func portalCSRFToken(r *http.Request) string {
	if cookie, err := r.Cookie(middleware.CSRFCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func writePortalNav(w http.ResponseWriter, csrf string, principal portal.Principal) {
	role := ""
	if principal.Assignment != nil {
		role = humanPortalRole(principal.Assignment.Role)
	}
	fmt.Fprintf(w, `<nav class="navbar navbar-expand-lg navbar-dark bg-gmcl mb-4"><div class="container-fluid px-3 px-lg-4">
<a class="navbar-brand d-flex align-items-center" href="/portal"><img src="/images/logo.webp" alt="GMCL" height="44" class="me-2"><span>Club portal</span></a>
<button class="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#portalNav" aria-controls="portalNav" aria-expanded="false" aria-label="Toggle navigation"><span class="navbar-toggler-icon"></span></button>
<div class="collapse navbar-collapse" id="portalNav"><ul class="navbar-nav ms-auto align-items-lg-center">
<li class="nav-item"><span class="navbar-text me-lg-3">%s%s</span></li>
<li class="nav-item"><a class="nav-link" href="/portal/contexts">Switch role</a></li>
<li class="nav-item"><a class="nav-link" href="/portal/sessions">Sessions</a></li>
<li class="nav-item"><form method="post" action="/portal/logout"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-link nav-link" type="submit">Sign out</button></form></li>
</ul></div></div></nav>`,
		escapeHTML(principal.DisplayName),
		func() string {
			if role == "" {
				return ""
			}
			return " · " + escapeHTML(role)
		}(),
		escapeHTML(csrf),
	)
}

func humanPortalRole(role portal.RoleKey) string {
	switch role {
	case portal.RoleClubPrimaryAdmin:
		return "Club Primary Administrator"
	case portal.RoleClubAdmin:
		return "Club Administrator"
	case portal.RoleClubSecretary:
		return "Club Secretary"
	case portal.RoleCaptainManager:
		return "Captain or Manager"
	case portal.RoleReadOnlyClubUser:
		return "Read-only Club User"
	case portal.RoleClubJuniorOfficer:
		return "Junior Officer"
	case portal.RoleClubSafeguarding:
		return "Safeguarding Officer"
	default:
		return "Unknown role"
	}
}

func renderPortalUnavailable(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusServiceUnavailable)
	pageHead(w, "Club portal unavailable")
	writeCaptainNav(w)
	fmt.Fprintf(w, `<main class="container"><div class="row justify-content-center"><div class="col-lg-7"><div class="alert alert-warning"><h1 class="h4">Club portal unavailable</h1><p class="mb-0">%s</p></div></div></div></main>`, escapeHTML(message))
	pageFooter(w)
}

func renderPortalSignInFailed(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	pageHead(w, "Club portal sign-in")
	writeCaptainNav(w)
	fmt.Fprint(w, `<main class="container"><div class="row justify-content-center"><div class="col-lg-7"><div class="alert alert-danger"><h1 class="h4">Sign-in could not be completed</h1><p>The link may have expired, already been used, or may not match an approved club appointment. Start again or contact the GMCL Club Liaison Officer.</p><a class="btn btn-primary" href="/portal/login">Start again</a></div></div></div></main>`)
	pageFooter(w)
}
