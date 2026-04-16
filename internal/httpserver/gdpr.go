package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type gdprCaptainRow struct {
	ID       int32
	ClubName string
	TeamName string
	FullName string
	Email    string
	Phone    string
	IsActive bool
}

type gdprPageData struct {
	Query      string
	Rows       []gdprCaptainRow
	SuccessMsg string
	ErrorMsg   string
}

func (s *Server) handlePrivacyNotice() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contactEmail := gdprContactEmail()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Privacy Notice")
		writeCaptainNav(w)
		fmt.Fprintf(w, `<div class="container" style="max-width:900px">
<div class="card shadow-sm">
  <div class="card-body">
    <h3 class="mb-3">Privacy Notice</h3>
    <p>This GMCL reporting application processes personal data for the purpose of administering league reporting, authenticating captains and administrators, collecting match feedback, and operating league compliance workflows.</p>
    <h5>Personal data processed</h5>
    <ul>
      <li>Captain and admin identity details such as name, email address and, where provided, phone number.</li>
      <li>Authentication and security data such as login events, single-use access tokens, IP addresses and user-agent strings.</li>
      <li>Submission content and related operational metadata needed to administer reports and league processes.</li>
    </ul>
    <h5>Purpose and lawful basis</h5>
    <ul>
      <li>League administration and reporting operations.</li>
      <li>Security, fraud prevention and access control.</li>
      <li>Maintaining compliance, auditability and service integrity.</li>
      <li>Monitoring whether required captain submissions are received by the applicable deadline and supporting league sanction processes for non-submission.</li>
    </ul>
    <h5>Automated compliance checks and sanctions</h5>
    <p>The application uses submission and scheduling data to monitor whether required reports have been submitted on time. Where a required submission is not recorded by the deadline, the system may automatically flag or issue a league sanction, including yellow or red cards, in line with league rules.</p>
    <p>These outcomes are intended to support league administration rather than operate as an unreviewable black-box decision. League administrators may review, confirm, amend or reverse a sanction where a report was submitted, an exception applies, or the automated outcome was incorrect.</p>
    <h5>How long data is kept</h5>
    <p>Operational security data is retained for limited periods and can be cleaned up from the admin security tools. Submission records are retained for league administration unless separately anonymised or removed under an agreed process.</p>
    <p>See the <a href="/retention">retention schedule</a> for the current application defaults.</p>
    <h5>Data subject requests</h5>
    <p>Requests for access, correction or erasure, and queries or challenges relating to an apparent non-submission or sanction, should be sent to <a href="mailto:%s">%s</a>. The admin panel contains export and anonymisation tooling to help process captain-level requests.</p>
  </div>
</div>
</div>`, escapeHTML(contactEmail), escapeHTML(contactEmail))
		pageFooter(w)
	}
}

func (s *Server) handleRetentionNotice() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Retention Schedule")
		writeCaptainNav(w)
		fmt.Fprintf(w, `<div class="container" style="max-width:900px">
<div class="card shadow-sm">
  <div class="card-body">
    <h3 class="mb-3">Retention Schedule</h3>
    <table class="table table-sm">
      <thead><tr><th>Data</th><th>Default retention</th></tr></thead>
      <tbody>
        <tr><td>Audit logs</td><td>%d days</td></tr>
        <tr><td>Magic-link tokens</td><td>%d days after expiry/use</td></tr>
        <tr><td>Magic-link send log</td><td>%d days</td></tr>
        <tr><td>Admin 2FA codes</td><td>%d days after expiry/use</td></tr>
        <tr><td>CSV preview tokens</td><td>%d days after expiry/use</td></tr>
        <tr><td>Draft autosaves</td><td>%d days since last autosave</td></tr>
        <tr><td>Captain submissions</td><td>Retained for league administration unless specifically anonymised or deleted under an approved process</td></tr>
      </tbody>
    </table>
  </div>
</div>
</div>`,
			retentionDays("RETENTION_AUDIT_LOG_DAYS", 365),
			retentionDays("RETENTION_MAGIC_LINK_TOKEN_DAYS", 30),
			retentionDays("RETENTION_MAGIC_LINK_SEND_LOG_DAYS", 90),
			retentionDays("RETENTION_ADMIN_2FA_DAYS", 14),
			retentionDays("RETENTION_CSV_PREVIEW_DAYS", 7),
			retentionDays("RETENTION_DRAFT_DAYS", 30),
		)
		pageFooter(w)
	}
}

func (s *Server) handleAdminGDPRGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		data, err := s.buildGDPRPageData(ctx, r)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.renderAdminGDPRPage(w, r, data)
	}
}

func (s *Server) handleAdminGDPRExport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		captainID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("captain_id")), 10, 32)
		if captainID <= 0 {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		payload, err := s.buildCaptainExport(ctx, int32(captainID))
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.audit(ctx, r, "admin", nil, "gdpr_export_generated", "captain", func() *int64 { v := captainID; return &v }(), map[string]any{})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="captain-%d-export.json"`, captainID))
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func (s *Server) handleAdminGDPRAnonymise() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		captainID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 32)
		if captainID <= 0 {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := s.anonymiseCaptain(ctx, int32(captainID)); err != nil {
			http.Redirect(w, r, "/admin/gdpr?error="+urlQueryEscape("Could not anonymise captain: "+err.Error()), http.StatusSeeOther)
			return
		}
		s.audit(ctx, r, "admin", nil, "gdpr_captain_anonymised", "captain", func() *int64 { v := captainID; return &v }(), map[string]any{})
		http.Redirect(w, r, "/admin/gdpr?success="+urlQueryEscape("Captain anonymised."), http.StatusSeeOther)
	}
}

func (s *Server) buildGDPRPageData(ctx context.Context, r *http.Request) (gdprPageData, error) {
	like := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	data := gdprPageData{
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		SuccessMsg: strings.TrimSpace(r.URL.Query().Get("success")),
		ErrorMsg:   strings.TrimSpace(r.URL.Query().Get("error")),
	}
	rows, err := s.DB.Query(ctx, `
		SELECT c.id, cl.name, t.name, c.full_name, c.email, COALESCE(c.phone, ''),
		       (c.active_to IS NULL OR c.active_to >= CURRENT_DATE) AS is_active
		FROM captains c
		JOIN teams t ON t.id = c.team_id
		JOIN clubs cl ON cl.id = t.club_id
		WHERE $1 = '%%'
		   OR cl.name ILIKE $1
		   OR t.name ILIKE $1
		   OR c.full_name ILIKE $1
		   OR c.email ILIKE $1
		   OR COALESCE(c.phone, '') ILIKE $1
		ORDER BY is_active DESC, cl.name, t.name, c.full_name
		LIMIT 300
	`, like)
	if err != nil {
		return data, err
	}
	defer rows.Close()
	for rows.Next() {
		var row gdprCaptainRow
		if err := rows.Scan(&row.ID, &row.ClubName, &row.TeamName, &row.FullName, &row.Email, &row.Phone, &row.IsActive); err != nil {
			return data, err
		}
		data.Rows = append(data.Rows, row)
	}
	return data, rows.Err()
}

func (s *Server) renderAdminGDPRPage(w http.ResponseWriter, r *http.Request, data gdprPageData) {
	csrfToken := middleware.CSRFToken(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHead(w, "GDPR")
	writeAdminNav(w, csrfToken, r.URL.Path)
	fmt.Fprint(w, `<div class="container-fluid"><div class="d-flex align-items-center justify-content-between mb-4"><div><h3 class="mb-1">GDPR</h3><p class="text-muted mb-0">Search captains, export a data package, and anonymise a captain while preserving league reporting history.</p></div></div>`)
	if data.SuccessMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(data.SuccessMsg))
	}
	if data.ErrorMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(data.ErrorMsg))
	}
	fmt.Fprintf(w, `<form method="GET" action="/admin/gdpr" class="row g-2 mb-4"><div class="col-md-6"><input class="form-control" name="q" value="%s" placeholder="Search captain, club, team, email or phone"></div><div class="col-auto"><button class="btn btn-primary" type="submit">Search</button></div><div class="col-auto"><a class="btn btn-outline-secondary" href="/admin/gdpr">Clear</a></div></form>`, escapeHTML(data.Query))
	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Captain Data Requests</div><div class="table-responsive"><table class="table table-hover mb-0"><thead><tr><th>Captain</th><th>Team</th><th>Status</th><th></th></tr></thead><tbody>`)
	if len(data.Rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="4" class="text-center text-muted py-3">No captain records found.</td></tr>`)
	}
	for _, row := range data.Rows {
		status := `<span class="badge text-bg-secondary">Inactive</span>`
		if row.IsActive {
			status = `<span class="badge text-bg-success">Active</span>`
		}
		fmt.Fprintf(w, `<tr><td>%s<div class="text-muted small">%s</div>%s</td><td>%s - %s</td><td>%s</td><td class="text-nowrap"><a class="btn btn-sm btn-outline-primary" href="/admin/gdpr/export?captain_id=%d">Export JSON</a> <form method="POST" action="/admin/gdpr/captains/%d/anonymise" class="d-inline" onsubmit="return confirm('Anonymise this captain record and related access data?');"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-sm btn-outline-danger" type="submit">Anonymise</button></form></td></tr>`,
			escapeHTML(row.FullName),
			escapeHTML(row.Email),
			func() string {
				if row.Phone == "" {
					return ""
				}
				return `<div class="text-muted small">` + escapeHTML(row.Phone) + `</div>`
			}(),
			escapeHTML(row.ClubName), escapeHTML(row.TeamName), status, row.ID, row.ID, csrfToken)
	}
	fmt.Fprint(w, `</tbody></table></div></div></div>`)
	pageFooter(w)
}

func (s *Server) buildCaptainExport(ctx context.Context, captainID int32) (map[string]any, error) {
	payload := map[string]any{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"captain_id":  captainID,
	}

	var captain map[string]any
	err := s.DB.QueryRow(ctx, `
		SELECT json_build_object(
			'id', c.id,
			'full_name', c.full_name,
			'email', c.email,
			'phone', c.phone,
			'active_from', c.active_from,
			'active_to', c.active_to,
			'team', json_build_object('id', t.id, 'name', t.name),
			'club', json_build_object('id', cl.id, 'name', cl.name)
		)
		FROM captains c
		JOIN teams t ON t.id = c.team_id
		JOIN clubs cl ON cl.id = t.club_id
		WHERE c.id = $1
	`, captainID).Scan(&captain)
	if err != nil {
		return nil, err
	}
	payload["captain"] = captain

	var submissions []map[string]any
	subRows, err := s.DB.Query(ctx, `
		SELECT json_build_object(
			'id', id,
			'season_id', season_id,
			'week_id', week_id,
			'match_date', match_date,
			'status', status,
			'submitted_at', submitted_at,
			'submitted_by_name', submitted_by_name,
			'submitted_by_email', submitted_by_email,
			'submitted_by_role', submitted_by_role,
			'comments', comments
		)
		FROM submissions
		WHERE captain_id = $1
		ORDER BY submitted_at DESC
	`, captainID)
	if err != nil {
		return nil, err
	}
	defer subRows.Close()
	for subRows.Next() {
		var item map[string]any
		if err := subRows.Scan(&item); err != nil {
			return nil, err
		}
		submissions = append(submissions, item)
	}
	payload["submissions"] = submissions

	var drafts []map[string]any
	draftRows, err := s.DB.Query(ctx, `
		SELECT json_build_object(
			'season_id', season_id,
			'week_id', week_id,
			'team_id', team_id,
			'last_autosaved_at', last_autosaved_at,
			'draft_data', draft_data
		)
		FROM drafts
		WHERE captain_id = $1
		ORDER BY last_autosaved_at DESC
	`, captainID)
	if err != nil {
		return nil, err
	}
	defer draftRows.Close()
	for draftRows.Next() {
		var item map[string]any
		if err := draftRows.Scan(&item); err != nil {
			return nil, err
		}
		drafts = append(drafts, item)
	}
	payload["drafts"] = drafts

	var accessSummary map[string]any
	err = s.DB.QueryRow(ctx, `
		SELECT json_build_object(
			'magic_link_tokens', (SELECT COUNT(*) FROM magic_link_tokens WHERE captain_id = $1),
			'magic_link_send_log', (SELECT COUNT(*) FROM magic_link_send_log WHERE captain_id = $1)
		)
	`, captainID).Scan(&accessSummary)
	if err != nil {
		return nil, err
	}
	payload["access_summary"] = accessSummary
	return payload, nil
}

func (s *Server) anonymiseCaptain(ctx context.Context, captainID int32) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	placeholderEmail := fmt.Sprintf("anonymised-captain-%d@redacted.local", captainID)
	placeholderName := fmt.Sprintf("Anonymised Captain %d", captainID)

	if _, err := tx.Exec(ctx, `
		UPDATE submissions
		SET submitted_by_name = CASE WHEN submitted_by_role = 'captain' THEN 'Anonymised Captain' ELSE submitted_by_name END,
		    submitted_by_email = CASE WHEN submitted_by_role = 'captain' THEN NULL ELSE submitted_by_email END
		WHERE captain_id = $1
	`, captainID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM drafts WHERE captain_id = $1`, captainID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM magic_link_send_log WHERE captain_id = $1`, captainID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM magic_link_tokens WHERE captain_id = $1`, captainID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE captains
		SET full_name = $1,
		    email = $2,
		    phone = NULL,
		    active_to = COALESCE(active_to, CURRENT_DATE)
		WHERE id = $3
	`, placeholderName, placeholderEmail, captainID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func gdprContactEmail() string {
	for _, key := range []string{"GDPR_CONTACT_EMAIL", "SEED_ADMIN_EMAIL", "SMTP_FROM"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			if key == "SMTP_FROM" && strings.Contains(v, "<") && strings.Contains(v, ">") {
				start := strings.Index(v, "<")
				end := strings.LastIndex(v, ">")
				if start >= 0 && end > start+1 {
					return strings.TrimSpace(v[start+1 : end])
				}
			}
			return v
		}
	}
	return "webmaster@gmcl.co.uk"
}
