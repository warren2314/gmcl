package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

type retentionSummary struct {
	Label  string
	Days   int
	Count  int64
	Action string
}

type securityPageData struct {
	Disable2FA         bool
	SessionSecretSet   bool
	AdminSecretSet     bool
	HMACSecretSet      bool
	SMTPConfigured     bool
	HSTSEnabled        bool
	MaxFailedAttempts  int
	LockoutMinutes     int
	Summaries          []retentionSummary
	SuccessMsg         string
	ErrorMsg           string
}

func (s *Server) handleAdminSecurityGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		data, err := s.buildSecurityPageData(ctx, r)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.renderAdminSecurityPage(w, r, data)
	}
}

func (s *Server) handleAdminSecurityCleanupPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		results, err := s.runRetentionCleanup(ctx)
		if err != nil {
			http.Redirect(w, r, "/admin/security?error="+urlQueryEscape("Cleanup failed: "+err.Error()), http.StatusSeeOther)
			return
		}

		meta := make(map[string]any, len(results))
		for k, v := range results {
			meta[k] = v
		}
		s.audit(ctx, r, "admin", nil, "security_cleanup_run", "retention", nil, meta)
		http.Redirect(w, r, "/admin/security?success="+urlQueryEscape("Retention cleanup completed."), http.StatusSeeOther)
	}
}

func (s *Server) buildSecurityPageData(ctx context.Context, r *http.Request) (securityPageData, error) {
	disable2FA := os.Getenv("DISABLE_2FA") == "1"
	maxAttempts, lockout := securityAdminLockoutPolicy()
	data := securityPageData{
		Disable2FA:        disable2FA,
		SessionSecretSet:  os.Getenv("SESSION_SECRET") != "",
		AdminSecretSet:    os.Getenv("ADMIN_SESSION_SECRET") != "",
		HMACSecretSet:     os.Getenv("INTERNAL_HMAC_SECRET") != "",
		SMTPConfigured:    os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_USERNAME") != "" && os.Getenv("SMTP_PASSWORD") != "",
		HSTSEnabled:       os.Getenv("ENABLE_HSTS") != "0",
		MaxFailedAttempts: maxAttempts,
		LockoutMinutes:    int(lockout / time.Minute),
		SuccessMsg:        r.URL.Query().Get("success"),
		ErrorMsg:          r.URL.Query().Get("error"),
	}
	summaries, err := s.loadRetentionSummaries(ctx)
	if err != nil {
		return data, err
	}
	data.Summaries = summaries
	return data, nil
}

func (s *Server) loadRetentionSummaries(ctx context.Context) ([]retentionSummary, error) {
	type spec struct {
		label  string
		days   int
		query  string
		action string
	}
	specs := []spec{
		{"Audit logs", retentionDays("RETENTION_AUDIT_LOG_DAYS", 365), `SELECT COUNT(*) FROM audit_logs WHERE created_at < now() - ($1::text || ' days')::interval`, "Delete"},
		{"Magic-link tokens", retentionDays("RETENTION_MAGIC_LINK_TOKEN_DAYS", 30), `SELECT COUNT(*) FROM magic_link_tokens WHERE (used_at IS NOT NULL OR expires_at < now()) AND created_at < now() - ($1::text || ' days')::interval`, "Delete"},
		{"Magic-link send log", retentionDays("RETENTION_MAGIC_LINK_SEND_LOG_DAYS", 90), `SELECT COUNT(*) FROM magic_link_send_log WHERE created_at < now() - ($1::text || ' days')::interval`, "Delete"},
		{"Admin 2FA codes", retentionDays("RETENTION_ADMIN_2FA_DAYS", 14), `SELECT COUNT(*) FROM admin_2fa_codes WHERE (used_at IS NOT NULL OR expires_at < now()) AND created_at < now() - ($1::text || ' days')::interval`, "Delete"},
		{"CSV preview tokens", retentionDays("RETENTION_CSV_PREVIEW_DAYS", 7), `SELECT COUNT(*) FROM csv_preview_tokens WHERE (used_at IS NOT NULL OR expires_at < now()) AND created_at < now() - ($1::text || ' days')::interval`, "Delete"},
		{"Draft autosaves", retentionDays("RETENTION_DRAFT_DAYS", 30), `SELECT COUNT(*) FROM drafts WHERE last_autosaved_at < now() - ($1::text || ' days')::interval`, "Delete"},
	}
	var out []retentionSummary
	for _, spec := range specs {
		var count int64
		if err := s.DB.QueryRow(ctx, spec.query, spec.days).Scan(&count); err != nil {
			return nil, err
		}
		out = append(out, retentionSummary{Label: spec.label, Days: spec.days, Count: count, Action: spec.action})
	}
	return out, nil
}

func (s *Server) runRetentionCleanup(ctx context.Context) (map[string]int64, error) {
	queries := []struct {
		key  string
		days int
		sql  string
	}{
		{"audit_logs_deleted", retentionDays("RETENTION_AUDIT_LOG_DAYS", 365), `DELETE FROM audit_logs WHERE created_at < now() - ($1::text || ' days')::interval`},
		{"magic_link_tokens_deleted", retentionDays("RETENTION_MAGIC_LINK_TOKEN_DAYS", 30), `DELETE FROM magic_link_tokens WHERE (used_at IS NOT NULL OR expires_at < now()) AND created_at < now() - ($1::text || ' days')::interval`},
		{"magic_link_send_log_deleted", retentionDays("RETENTION_MAGIC_LINK_SEND_LOG_DAYS", 90), `DELETE FROM magic_link_send_log WHERE created_at < now() - ($1::text || ' days')::interval`},
		{"admin_2fa_codes_deleted", retentionDays("RETENTION_ADMIN_2FA_DAYS", 14), `DELETE FROM admin_2fa_codes WHERE (used_at IS NOT NULL OR expires_at < now()) AND created_at < now() - ($1::text || ' days')::interval`},
		{"csv_preview_tokens_deleted", retentionDays("RETENTION_CSV_PREVIEW_DAYS", 7), `DELETE FROM csv_preview_tokens WHERE (used_at IS NOT NULL OR expires_at < now()) AND created_at < now() - ($1::text || ' days')::interval`},
		{"drafts_deleted", retentionDays("RETENTION_DRAFT_DAYS", 30), `DELETE FROM drafts WHERE last_autosaved_at < now() - ($1::text || ' days')::interval`},
	}
	results := make(map[string]int64, len(queries))
	for _, q := range queries {
		tag, err := s.DB.Exec(ctx, q.sql, q.days)
		if err != nil {
			return nil, err
		}
		results[q.key] = tag.RowsAffected()
	}
	return results, nil
}

func retentionDays(envKey string, def int) int {
	if raw := os.Getenv(envKey); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return def
}

func securityAdminLockoutPolicy() (maxAttempts int, duration time.Duration) {
	maxAttempts = 5
	duration = 15 * time.Minute
	if raw := os.Getenv("ADMIN_MAX_FAILED_ATTEMPTS"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			maxAttempts = v
		}
	}
	if raw := os.Getenv("ADMIN_LOCKOUT_MINUTES"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			duration = time.Duration(v) * time.Minute
		}
	}
	return maxAttempts, duration
}

func (s *Server) renderAdminSecurityPage(w http.ResponseWriter, r *http.Request, data securityPageData) {
	csrfToken := middleware.CSRFToken(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHead(w, "Security & Privacy")
	writeAdminNav(w, csrfToken, r.URL.Path)

	statusBadge := func(ok bool, goodLabel, badLabel string) string {
		if ok {
			return `<span class="badge text-bg-success">` + goodLabel + `</span>`
		}
		return `<span class="badge text-bg-danger">` + badLabel + `</span>`
	}

	fmt.Fprint(w, `<div class="container-fluid"><div class="d-flex align-items-center justify-content-between mb-4"><div><h3 class="mb-1">Security & Privacy</h3><p class="text-muted mb-0">Operational security checks and GDPR-supporting retention controls.</p></div></div>`)
	if data.SuccessMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(data.SuccessMsg))
	}
	if data.ErrorMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(data.ErrorMsg))
	}

	fmt.Fprintf(w, `<div class="row row-cols-1 row-cols-md-3 g-3 mb-4">
<div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Admin 2FA</div><div class="fs-5 fw-semibold">%s</div></div></div>
<div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Admin lockout</div><div class="fs-5 fw-semibold">%d attempts / %d mins</div></div></div>
<div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">HSTS</div><div class="fs-5 fw-semibold">%s</div></div></div>
</div>`,
		statusBadge(!data.Disable2FA, "Enabled", "Disabled"),
		data.MaxFailedAttempts, data.LockoutMinutes,
		statusBadge(data.HSTSEnabled, "Enabled", "Disabled"),
	)

	fmt.Fprintf(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Security Checks</div><div class="table-responsive"><table class="table mb-0"><tbody>
<tr><th>Captain session secret</th><td>%s</td></tr>
<tr><th>Admin session secret</th><td>%s</td></tr>
<tr><th>Internal HMAC secret</th><td>%s</td></tr>
<tr><th>SMTP configured</th><td>%s</td></tr>
<tr><th>DISABLE_2FA</th><td>%s</td></tr>
</tbody></table></div></div>`,
		statusBadge(data.SessionSecretSet, "Configured", "Missing"),
		statusBadge(data.AdminSecretSet, "Configured", "Missing"),
		statusBadge(data.HMACSecretSet, "Configured", "Missing"),
		statusBadge(data.SMTPConfigured, "Configured", "Missing"),
		statusBadge(!data.Disable2FA, "Off", "On"),
	)

	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Retention Cleanup</div><div class="card-body"><p class="text-muted">This removes expired or stale operational data such as used tokens, old draft autosaves, and aged audit logs. It does not delete submissions.</p><div class="table-responsive"><table class="table table-sm align-middle"><thead><tr><th>Data</th><th>Retention</th><th>Rows currently eligible</th></tr></thead><tbody>`)
	for _, item := range data.Summaries {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%d days</td><td>%d</td></tr>`, escapeHTML(item.Label), item.Days, item.Count)
	}
	fmt.Fprintf(w, `</tbody></table></div><form method="POST" action="/admin/security/cleanup" onsubmit="return confirm('Run retention cleanup now?');"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-primary" type="submit">Run cleanup</button></form></div></div>`, csrfToken)
	fmt.Fprint(w, `</div>`)
	pageFooter(w)
}
