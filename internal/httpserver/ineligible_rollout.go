package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	ineligibledomain "cricket-ground-feedback/internal/ineligible"
	"cricket-ground-feedback/internal/middleware"
)

func configuredPrivateGoogleFormURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("INELIGIBLE_PRIVATE_GOOGLE_FORM_URL"))
	if raw == "" {
		return "", fmt.Errorf("INELIGIBLE_PRIVATE_GOOGLE_FORM_URL is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("INELIGIBLE_PRIVATE_GOOGLE_FORM_URL must be an absolute HTTPS URL")
	}
	return parsed.String(), nil
}

func (s *Server) nativeIneligibleRolloutActive(ctx context.Context) (bool, ineligibledomain.RolloutStatus, error) {
	status, err := ineligibledomain.GetRolloutStatus(ctx, s.DB)
	if err != nil {
		return false, status, err
	}
	// The import switch is also the emergency rollback for the public native
	// form. Keeping the rollout row activated must not bypass that kill switch.
	return envEnabled("INELIGIBLE_IMPORT_ENABLED") && status.ActivatedAt != nil, status, nil
}

func (s *Server) redirectToPrivateGoogleForm(w http.ResponseWriter, r *http.Request) bool {
	target, err := configuredPrivateGoogleFormURL()
	if err != nil {
		http.Error(w, "ineligible-player reporting is temporarily unavailable; the private form has not been configured", http.StatusServiceUnavailable)
		return false
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
	return true
}

func (s *Server) handleAdminIneligibleRollout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := ineligibledomain.GetRolloutStatus(r.Context(), s.DB)
		if err != nil {
			http.Error(w, "could not load rollout status", http.StatusInternalServerError)
			return
		}
		applied := "Not applied"
		if status.PrerequisiteApplicationID != nil {
			applied = fmt.Sprintf("Application %d, signed by %s", *status.PrerequisiteApplicationID, status.SignatoryName)
		}
		activation := "Native form locked"
		if status.ActivatedAt != nil {
			activation = status.ActivatedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04 MST")
		}
		grace := "Not started"
		if status.GoogleGraceUntil != nil {
			grace = status.GoogleGraceUntil.In(s.LondonLoc).Format("02 Jan 2006 15:04 MST")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Ineligible-player rollout")
		writeAdminNav(w, middleware.CSRFToken(r), r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><a class="btn btn-sm btn-outline-secondary mb-3" href="/admin/ineligible">Back to intake</a><h1 class="h2">Ineligible-player rollout gate</h1><div class="card"><dl class="row card-body mb-0"><dt class="col-sm-5">State</dt><dd class="col-sm-7">%s</dd><dt class="col-sm-5">Named backfill prerequisite</dt><dd class="col-sm-7">%s</dd><dt class="col-sm-5">Consecutive clean London dates</dt><dd class="col-sm-7">%d of 3</dd><dt class="col-sm-5">Native activation</dt><dd class="col-sm-7">%s</dd><dt class="col-sm-5">Google scheduled-import grace ends</dt><dd class="col-sm-7">%s</dd><dt class="col-sm-5">Import kill switch</dt><dd class="col-sm-7">%t</dd><dt class="col-sm-5">Bootstrap mode</dt><dd class="col-sm-7">%t</dd><dt class="col-sm-5">Outbound ineligible email</dt><dd class="col-sm-7">%t</dd></dl></div><p class="text-muted mt-3">Manual bootstrap can stage the initial private sheet only. Scheduled reconciliation cannot start until the named tracker application exists. Activation is recorded once after three distinct consecutive successful scheduled dates; Google polling then retires after 30 days.</p></main>`,
			escapeHTML(status.State), escapeHTML(applied), status.CleanDates, escapeHTML(activation), escapeHTML(grace), envEnabled("INELIGIBLE_IMPORT_ENABLED"), envEnabled("INELIGIBLE_BOOTSTRAP_IMPORT_ENABLED"), ineligibleOutboundEmailEnabled())
		pageFooter(w)
	}
}

func envEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
