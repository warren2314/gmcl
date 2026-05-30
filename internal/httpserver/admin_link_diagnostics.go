package httpserver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"
)

type linkDiagCaptain struct {
	ID         int32
	TeamID     int32
	FullName   string
	Email      string
	ClubName   string
	TeamName   string
	ActiveFrom string
	ActiveTo   string
	IsActive   bool
}

type linkDiagToken struct {
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    sql.NullTime
	MatchDate sql.NullTime
	RequestIP string
	UserAgent string
	Status    string
}

type linkDiagSend struct {
	CreatedAt  time.Time
	SeasonName string
	WeekNumber int32
	TokenID    sql.NullInt64
}

type linkDiagReminderSend struct {
	SentAt       time.Time
	MatchDate    time.Time
	ReminderType string
	Recipient    string
	ClubName     string
	TeamName     string
	TokenID      sql.NullInt64
}

type linkDiagEmailEvent struct {
	CreatedAt  time.Time
	OccurredAt sql.NullTime
	EventType  string
	Recipient  string
	Subject    string
	Detail     string
	LinkURL    string
	ClickIP    string
	ClickUA    string
}

type linkDiagSubmission struct {
	ID        int64
	MatchDate time.Time
	Submitted time.Time
	TeamName  string
}

type linkDiagAudit struct {
	CreatedAt time.Time
	Action    string
	Metadata  string
	UserAgent string
}

type linkDiagPageData struct {
	Query      string
	SuccessMsg string
	ErrorMsg   string
	Captains   []linkDiagCaptain
	Tokens     []linkDiagToken
	Sends      []linkDiagSend
	Reminders  []linkDiagReminderSend
	Events     []linkDiagEmailEvent
	Submits    []linkDiagSubmission
	AuditRows  []linkDiagAudit
}

func (s *Server) handleAdminLinkDiagnostics() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()

		data := linkDiagPageData{
			Query:      strings.TrimSpace(r.URL.Query().Get("q")),
			SuccessMsg: strings.TrimSpace(r.URL.Query().Get("success")),
			ErrorMsg:   strings.TrimSpace(r.URL.Query().Get("error")),
		}
		if data.Query == "" {
			data.Query = strings.TrimSpace(r.URL.Query().Get("email"))
		}
		if data.Query != "" {
			if err := s.loadLinkDiagnostics(ctx, &data); err != nil {
				data.ErrorMsg = "Could not load diagnostics: " + err.Error()
			}
		}

		csrfToken := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Link Diagnostics")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))
		renderLinkDiagnosticsPage(w, csrfToken, data)
		pageFooter(w)
	}
}

func (s *Server) handleAdminLinkDiagnosticsSend() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		captainID64, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("captain_id")), 10, 32)
		query := strings.TrimSpace(r.FormValue("q"))
		if query == "" {
			query = strings.TrimSpace(r.FormValue("email"))
		}
		redirect := "/admin/link-diagnostics?q=" + url.QueryEscape(query)
		if err != nil || captainID64 <= 0 {
			http.Redirect(w, r, redirect+"&error="+url.QueryEscape("Invalid captain."), http.StatusSeeOther)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		emailAddr, err := s.sendFreshCaptainAccessLink(ctx, r, int32(captainID64))
		if err != nil {
			http.Redirect(w, r, redirect+"&error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}

		s.audit(ctx, r, "admin", nil, "support_magic_link_sent", "captain", func() *int64 {
			v := captainID64
			return &v
		}(), map[string]any{"email": emailAddr})
		http.Redirect(w, r, redirect+"&success="+url.QueryEscape("Fresh link sent to "+emailAddr), http.StatusSeeOther)
	}
}

func (s *Server) loadLinkDiagnostics(ctx context.Context, data *linkDiagPageData) error {
	filter := strings.ToLower(strings.TrimSpace(data.Query))
	rows, err := s.DB.Query(ctx, `
		SELECT c.id, t.id, c.full_name, c.email, cl.name, t.name,
		       TO_CHAR(c.active_from, 'YYYY-MM-DD'),
		       COALESCE(TO_CHAR(c.active_to, 'YYYY-MM-DD'), ''),
		       (c.active_to IS NULL OR c.active_to >= CURRENT_DATE) AS is_active
		FROM captains c
		JOIN teams t ON t.id = c.team_id
		JOIN clubs cl ON cl.id = t.club_id
		WHERE LOWER(c.email) = LOWER($1)
		   OR LOWER(c.email) LIKE LOWER('%' || $1 || '%')
		   OR LOWER(c.full_name) LIKE LOWER('%' || $1 || '%')
		   OR LOWER(t.name) LIKE LOWER('%' || $1 || '%')
		   OR LOWER(cl.name) LIKE LOWER('%' || $1 || '%')
		ORDER BY is_active DESC, c.active_from DESC, c.id DESC
		LIMIT 20
	`, filter)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row linkDiagCaptain
		if err := rows.Scan(&row.ID, &row.TeamID, &row.FullName, &row.Email, &row.ClubName, &row.TeamName, &row.ActiveFrom, &row.ActiveTo, &row.IsActive); err != nil {
			return err
		}
		data.Captains = append(data.Captains, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(data.Captains) == 0 {
		return nil
	}

	var captainIDs []int32
	var teamIDs []int32
	var captainEmails []string
	for _, c := range data.Captains {
		captainIDs = append(captainIDs, c.ID)
		teamIDs = append(teamIDs, c.TeamID)
		captainEmails = append(captainEmails, strings.ToLower(strings.TrimSpace(c.Email)))
	}

	tokenRows, err := s.DB.Query(ctx, `
		SELECT created_at, expires_at, used_at, match_date, COALESCE(request_ip::text, ''), COALESCE(request_user_agent, '')
		FROM magic_link_tokens
		WHERE captain_id = ANY($1)
		ORDER BY created_at DESC
		LIMIT 20
	`, captainIDs)
	if err != nil {
		return err
	}
	defer tokenRows.Close()
	for tokenRows.Next() {
		var row linkDiagToken
		if err := tokenRows.Scan(&row.CreatedAt, &row.ExpiresAt, &row.UsedAt, &row.MatchDate, &row.RequestIP, &row.UserAgent); err != nil {
			return err
		}
		switch {
		case row.UsedAt.Valid:
			row.Status = "revoked/used"
		case time.Now().After(row.ExpiresAt):
			row.Status = "expired"
		default:
			row.Status = "valid"
		}
		data.Tokens = append(data.Tokens, row)
	}
	if err := tokenRows.Err(); err != nil {
		return err
	}

	sendRows, err := s.DB.Query(ctx, `
		SELECT m.created_at, se.name, w.week_number, m.token_id
		FROM magic_link_send_log m
		JOIN seasons se ON se.id = m.season_id
		JOIN weeks w ON w.id = m.week_id
		WHERE m.captain_id = ANY($1)
		ORDER BY m.created_at DESC
		LIMIT 20
	`, captainIDs)
	if err != nil {
		return err
	}
	defer sendRows.Close()
	for sendRows.Next() {
		var row linkDiagSend
		if err := sendRows.Scan(&row.CreatedAt, &row.SeasonName, &row.WeekNumber, &row.TokenID); err != nil {
			return err
		}
		data.Sends = append(data.Sends, row)
	}
	if err := sendRows.Err(); err != nil {
		return err
	}

	reminderRows, err := s.DB.Query(ctx, `
		SELECT crl.sent_at, crl.match_date, crl.reminder_type, crl.captain_email,
		       cl.name, t.name, crl.token_id
		FROM captain_reminder_log crl
		JOIN teams t ON t.id = crl.team_id
		JOIN clubs cl ON cl.id = t.club_id
		WHERE crl.team_id = ANY($1)
		   OR LOWER(crl.captain_email) = ANY($2)
		ORDER BY crl.sent_at DESC
		LIMIT 50
	`, teamIDs, captainEmails)
	if err != nil {
		return err
	}
	defer reminderRows.Close()
	for reminderRows.Next() {
		var row linkDiagReminderSend
		if err := reminderRows.Scan(&row.SentAt, &row.MatchDate, &row.ReminderType, &row.Recipient, &row.ClubName, &row.TeamName, &row.TokenID); err != nil {
			return err
		}
		data.Reminders = append(data.Reminders, row)
	}
	if err := reminderRows.Err(); err != nil {
		return err
	}

	subRows, err := s.DB.Query(ctx, `
		SELECT sub.id, sub.match_date, sub.submitted_at, t.name
		FROM submissions sub
		JOIN teams t ON t.id = sub.team_id
		WHERE sub.captain_id = ANY($1)
		ORDER BY sub.submitted_at DESC
		LIMIT 20
	`, captainIDs)
	if err != nil {
		return err
	}
	defer subRows.Close()
	for subRows.Next() {
		var row linkDiagSubmission
		if err := subRows.Scan(&row.ID, &row.MatchDate, &row.Submitted, &row.TeamName); err != nil {
			return err
		}
		data.Submits = append(data.Submits, row)
	}
	if err := subRows.Err(); err != nil {
		return err
	}

	eventRows, err := s.DB.Query(ctx, `
		SELECT created_at, occurred_at, event_type, COALESCE(recipient, ''), COALESCE(subject, ''),
		       COALESCE(NULLIF(link_url, ''), NULLIF(diagnostic_code,''), NULLIF(bounce_type,''), NULLIF(complaint_feedback_type,''), ''),
		       COALESCE(link_url, ''), COALESCE(click_ip::text, ''), COALESCE(click_user_agent, '')
		FROM email_events
		WHERE captain_id = ANY($1)
		   OR team_id = ANY($2)
		   OR LOWER(recipient) = ANY($3)
		ORDER BY COALESCE(occurred_at, created_at) DESC
		LIMIT 50
	`, captainIDs, teamIDs, captainEmails)
	if err != nil {
		return err
	}
	defer eventRows.Close()
	for eventRows.Next() {
		var row linkDiagEmailEvent
		if err := eventRows.Scan(&row.CreatedAt, &row.OccurredAt, &row.EventType, &row.Recipient, &row.Subject, &row.Detail, &row.LinkURL, &row.ClickIP, &row.ClickUA); err != nil {
			return err
		}
		data.Events = append(data.Events, row)
	}
	if err := eventRows.Err(); err != nil {
		return err
	}

	auditRows, err := s.DB.Query(ctx, `
		SELECT created_at, action, COALESCE(metadata::text, ''), COALESCE(user_agent, '')
		FROM audit_logs
		WHERE (entity_type = 'captain' AND entity_id = ANY($1::bigint[]))
		   OR (entity_type = 'team' AND entity_id = ANY($2::bigint[]))
		   OR (metadata->>'captain_id') = ANY($3::text[])
		   OR (metadata->>'team_id') = ANY($4::text[])
		   OR metadata::text ILIKE '%' || $5 || '%'
		ORDER BY created_at DESC
		LIMIT 50
	`, int32SliceToInt64(captainIDs), int32SliceToInt64(teamIDs), int32SliceToString(captainIDs), int32SliceToString(teamIDs), filter)
	if err != nil {
		return err
	}
	defer auditRows.Close()
	for auditRows.Next() {
		var row linkDiagAudit
		if err := auditRows.Scan(&row.CreatedAt, &row.Action, &row.Metadata, &row.UserAgent); err != nil {
			return err
		}
		data.AuditRows = append(data.AuditRows, row)
	}
	return auditRows.Err()
}

func (s *Server) sendFreshCaptainAccessLink(ctx context.Context, r *http.Request, captainID int32) (string, error) {
	var seasonID, weekID int32
	if err := s.DB.QueryRow(ctx, `
		WITH active AS (
			SELECT w.id, w.season_id, 1 AS p, w.start_date
			FROM weeks w
			WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date
			ORDER BY w.start_date
			LIMIT 1
		),
		past AS (
			SELECT w.id, w.season_id, 2 AS p, w.start_date
			FROM weeks w
			WHERE w.end_date < CURRENT_DATE
			ORDER BY w.end_date DESC
			LIMIT 1
		)
		SELECT id, season_id
		FROM (
			SELECT * FROM active
			UNION ALL
			SELECT * FROM past
		) choices
		ORDER BY p, start_date DESC
		LIMIT 1
	`).Scan(&weekID, &seasonID); err != nil {
		return "", fmt.Errorf("could not resolve current week: %w", err)
	}

	var captainEmail, captainName string
	if err := s.DB.QueryRow(ctx, `
		SELECT full_name, email
		FROM captains
		WHERE id = $1
	`, captainID).Scan(&captainName, &captainEmail); err != nil {
		return "", fmt.Errorf("could not load captain: %w", err)
	}

	now := time.Now()
	loc := s.LondonLoc
	if loc == nil {
		loc = time.UTC
	}
	expiresAt := nextWednesdayMidnight(now.In(loc))
	token, err := auth.GenerateAndStoreMagicTokenWithRevocation(ctx, s.DB, captainID, seasonID, weekID, expiresAt, r.RemoteAddr, "admin-support-link")
	if err != nil {
		return captainEmail, fmt.Errorf("could not create link: %w", err)
	}
	tokenID := s.magicTokenIDForPlaintext(ctx, token)

	_, _ = s.DB.Exec(ctx, `
		INSERT INTO magic_link_send_log (captain_id, season_id, week_id, token_id)
		VALUES ($1, $2, $3, $4)
	`, captainID, seasonID, weekID, tokenID)

	link := magicLinkEmailBlock(r, token)
	body := "Hello " + captainName + ",\n\n" +
		"Here is a fresh secure link for your GMCL captain report:\n\n" +
		link + "\n\n" +
		"Please use this latest email and ignore any older links. This link expires automatically."
	if err := email.NewFromEnv().Send(captainEmail, "GMCL Captain's Report - Fresh Access Link", body); err != nil {
		return captainEmail, fmt.Errorf("could not send fresh link to %s: %w", captainEmail, err)
	}
	return captainEmail, nil
}

func renderLinkDiagnosticsPage(w http.ResponseWriter, csrfToken string, data linkDiagPageData) {
	fmt.Fprint(w, `<div class="container-fluid px-4 py-4">`)
	fmt.Fprint(w, `<div class="d-flex align-items-start justify-content-between mb-4"><div><h4 class="mb-1 fw-bold">Link Diagnostics</h4><p class="text-muted mb-0 small">Super-admin tool for checking captain access-link delivery, token status, submissions, and resend actions.</p></div></div>`)
	if data.SuccessMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(data.SuccessMsg))
	}
	if data.ErrorMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(data.ErrorMsg))
	}
	fmt.Fprintf(w, `<form method="GET" action="/admin/link-diagnostics" class="row g-2 mb-4">
  <div class="col-md-6"><input type="text" class="form-control" name="q" placeholder="Captain, team, club, or email" value="%s" required></div>
  <div class="col-auto"><button class="btn btn-primary" type="submit">Check</button></div>
</form>`, escapeHTML(data.Query))

	if data.Query == "" {
		fmt.Fprint(w, `</div>`)
		return
	}
	if len(data.Captains) == 0 {
		fmt.Fprint(w, `<div class="alert alert-warning">No captain record found for that email address.</div></div>`)
		return
	}

	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Captain Records</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Captain</th><th>Club / Team</th><th>Dates</th><th>Status</th><th></th></tr></thead><tbody>`)
	for _, row := range data.Captains {
		status := `<span class="badge text-bg-secondary">Inactive</span>`
		if row.IsActive {
			status = `<span class="badge text-bg-success">Current</span>`
		}
		dates := row.ActiveFrom + " to "
		if row.ActiveTo == "" {
			dates += "present"
		} else {
			dates += row.ActiveTo
		}
		fmt.Fprintf(w, `<tr><td>%s<div class="text-muted small">%s</div></td><td>%s<br><span class="text-muted small">%s</span></td><td>%s</td><td>%s</td><td><form method="POST" action="/admin/link-diagnostics/send" class="d-inline" onsubmit="return confirm('Send a fresh link to %s? Older general links for this captain/week will be revoked.')"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="q" value="%s"><input type="hidden" name="captain_id" value="%d"><button class="btn btn-sm btn-warning" type="submit">Send fresh link</button></form></td></tr>`,
			escapeHTML(row.FullName), escapeHTML(row.Email), escapeHTML(row.ClubName), escapeHTML(row.TeamName), escapeHTML(dates), status, escapeHTML(row.Email), escapeHTML(csrfToken), escapeHTML(data.Query), row.ID)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)

	renderLinkDiagEvidenceSummary(w, data.Events, data.AuditRows)
	renderLinkDiagTokens(w, data.Tokens)
	renderLinkDiagSends(w, data.Sends)
	renderLinkDiagReminderSends(w, data.Reminders)
	renderLinkDiagEmailEvents(w, data.Events)
	renderLinkDiagSubmissions(w, data.Submits)
	renderLinkDiagAudit(w, data.AuditRows)
	fmt.Fprint(w, `</div>`)
}

func renderLinkDiagTokens(w http.ResponseWriter, rows []linkDiagToken) {
	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Magic-Link Tokens</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Created</th><th>Expires</th><th>Match date</th><th>Status</th><th>Request IP</th><th>User agent</th></tr></thead><tbody>`)
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No tokens found.</td></tr>`)
	}
	for _, row := range rows {
		badge := "text-bg-success"
		if row.Status == "expired" {
			badge = "text-bg-secondary"
		} else if row.Status == "revoked/used" {
			badge = "text-bg-warning"
		}
		matchDate := "-"
		if row.MatchDate.Valid {
			matchDate = row.MatchDate.Time.Format("02 Jan 2006")
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td><span class="badge %s">%s</span></td><td>%s</td><td class="small text-muted">%s</td></tr>`,
			row.CreatedAt.Format("02 Jan 15:04"), row.ExpiresAt.Format("02 Jan 15:04"), escapeHTML(matchDate), badge, escapeHTML(row.Status), escapeHTML(row.RequestIP), escapeHTML(row.UserAgent))
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func renderLinkDiagEvidenceSummary(w http.ResponseWriter, events []linkDiagEmailEvent, audits []linkDiagAudit) {
	orm := map[string]int{}
	for _, e := range events {
		orm[e.EventType]++
	}
	for _, a := range audits {
		orm[a.Action]++
	}
	fmt.Fprintf(w, `<div class="alert alert-info small">
<strong>Evidence summary:</strong>
SES clicks: %d |
App link landings: %d |
Confirmed links: %d |
Form opens: %d |
Autosaves: %d |
Submissions: %d.
Clicking the email link alone does not autosave; autosave evidence starts at <code>draft_autosaved</code>.
</div>`,
		orm["click"], orm["magic_link_clicked"], orm["magic_link_redeemed"], orm["captain_form_opened"], orm["draft_autosaved"], orm["submission_created"])
}

func renderLinkDiagSends(w http.ResponseWriter, rows []linkDiagSend) {
	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Magic-Link Send Log</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Sent/logged</th><th>Season</th><th>Week</th><th>Token</th></tr></thead><tbody>`)
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="4" class="text-center text-muted py-3">No send log rows found.</td></tr>`)
	}
	for _, row := range rows {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%d</td><td>%s</td></tr>`,
			row.CreatedAt.Format("02 Jan 15:04"), escapeHTML(row.SeasonName), row.WeekNumber, tokenIDLabel(row.TokenID))
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func renderLinkDiagReminderSends(w http.ResponseWriter, rows []linkDiagReminderSend) {
	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Reminder Email Sends</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Sent</th><th>Match</th><th>Type</th><th>Recipient</th><th>Club / Team</th><th>Token</th></tr></thead><tbody>`)
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No reminder send rows found.</td></tr>`)
	}
	for _, row := range rows {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s<br><span class="text-muted small">%s</span></td><td>%s</td></tr>`,
			row.SentAt.Format("02 Jan 15:04"),
			row.MatchDate.Format("02 Jan 2006"),
			escapeHTML(row.ReminderType),
			escapeHTML(row.Recipient),
			escapeHTML(row.ClubName),
			escapeHTML(row.TeamName),
			tokenIDLabel(row.TokenID))
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func renderLinkDiagEmailEvents(w http.ResponseWriter, rows []linkDiagEmailEvent) {
	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">SES Events Stored By App</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Event time</th><th>Received by app</th><th>Event</th><th>Recipient</th><th>Subject</th><th>Detail</th><th>Click client</th></tr></thead><tbody>`)
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-3">No SES events stored for this address/team. If AWS shows delivery/clicks, the SNS webhook is not feeding the app yet.</td></tr>`)
	}
	for _, row := range rows {
		eventTime := row.CreatedAt.Format("02 Jan 15:04")
		if row.OccurredAt.Valid {
			eventTime = row.OccurredAt.Time.Format("02 Jan 15:04")
		}
		clickClient := strings.TrimSpace(row.ClickIP + " " + row.ClickUA)
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="small">%s</td><td class="small text-muted">%s</td><td class="small text-muted">%s</td></tr>`,
			eventTime, row.CreatedAt.Format("02 Jan 15:04"), escapeHTML(row.EventType), escapeHTML(row.Recipient), escapeHTML(row.Subject), escapeHTML(redactMagicTokenInText(row.Detail)), escapeHTML(clickClient))
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func renderLinkDiagSubmissions(w http.ResponseWriter, rows []linkDiagSubmission) {
	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Submissions</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>ID</th><th>Match date</th><th>Submitted</th><th>Team</th></tr></thead><tbody>`)
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="4" class="text-center text-muted py-3">No submissions found for this captain.</td></tr>`)
	}
	for _, row := range rows {
		fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%s</td><td>%s</td></tr>`, row.ID, row.MatchDate.Format("02 Jan 2006"), row.Submitted.Format("02 Jan 15:04"), escapeHTML(row.TeamName))
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func renderLinkDiagAudit(w http.ResponseWriter, rows []linkDiagAudit) {
	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Recent Audit Activity</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Time</th><th>Action</th><th>Metadata</th><th>User agent</th></tr></thead><tbody>`)
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="4" class="text-center text-muted py-3">No matching audit rows found.</td></tr>`)
	}
	for _, row := range rows {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td class="small text-muted">%s</td><td class="small text-muted">%s</td></tr>`, row.CreatedAt.Format("02 Jan 15:04"), escapeHTML(row.Action), escapeHTML(row.Metadata), escapeHTML(row.UserAgent))
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func int32SliceToInt64(values []int32) []int64 {
	out := make([]int64, 0, len(values))
	for _, v := range values {
		out = append(out, int64(v))
	}
	return out
}

func int32SliceToString(values []int32) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, strconv.Itoa(int(v)))
	}
	return out
}

func tokenIDLabel(v sql.NullInt64) string {
	if !v.Valid || v.Int64 == 0 {
		return `<span class="text-muted">-</span>`
	}
	return fmt.Sprintf(`<code>%d</code>`, v.Int64)
}
