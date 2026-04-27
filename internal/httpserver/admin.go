package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type adminSession struct {
	AdminID  int32  `json:"aid"`
	Expiry   int64  `json:"exp"`
	Name     string `json:"name"`
	Aud      string `json:"aud"`
	JTI      string `json:"jti"`
	IssuedAt int64  `json:"iat"`
}

const adminSessionCookie = "adm_sess"

func (s *Server) adminRouter() http.Handler {
	r := chi.NewRouter()

	// apply rate limiting and CSRF to admin routes
	r.Use(middleware.RateLimit(60))
	r.Use(middleware.CSRFMiddleware)
	r.Get("/login", s.handleAdminLoginGet())
	r.Post("/login", s.handleAdminLoginPost())
	r.Get("/2fa", s.handleAdmin2FAGet())
	r.Post("/2fa", s.handleAdmin2FAPost())
	r.Get("/forgot-password", s.handleAdminForgotPasswordGet())
	r.Post("/forgot-password", s.handleAdminForgotPasswordPost())
	r.Get("/reset-password", s.handleAdminResetPasswordGet())
	r.Post("/reset-password", s.handleAdminResetPasswordPost())

	// Change-password is accessible to any authenticated admin, including those
	// who are forced to change — it sits outside the full requireAdmin group so
	// forced-change users aren't redirected back to login.
	r.With(s.requireAdmin()).Get("/change-password", s.handleAdminChangePasswordGet())
	r.With(s.requireAdmin()).Post("/change-password", s.handleAdminChangePasswordPost())

	r.Group(func(r chi.Router) {
		r.Use(s.requireAdmin())
		r.Use(s.requirePasswordChanged()) // block access to all other routes if force_password_change

		r.Get("/dashboard", s.handleAdminDashboard())
		r.Get("/weeks", s.handleAdminWeeks())
		r.Get("/weeks/{id}", s.handleAdminWeekDetail())
		r.Get("/submissions/{id}", s.handleAdminSubmissionDetail())

		// Rankings
		r.Get("/rankings", s.handleAdminRankings())
		r.Get("/rankings/umpires", s.handleAdminUmpireRankings())
		r.Get("/umpires/{name}/comments", s.handleAdminUmpireComments())

		// Compliance
		r.Get("/compliance", s.handleAdminCompliance())
		r.Post("/compliance/start-week", s.handleAdminComplianceStartWeek())
		r.Get("/reminders/preview", s.handleAdminReminderPreview())

		// Sanctions
		r.Get("/sanctions", s.handleAdminSanctions())
		r.Post("/sanctions/issue", s.handleAdminSanctionIssue())
		r.Post("/sanctions/bulk-issue", s.handleAdminSanctionBulkIssue())
		r.Post("/sanctions/{id}/resolve", s.handleAdminSanctionResolve())
		r.Get("/sanctions/{id}/email", s.handleAdminSanctionEmailPage())
		r.Post("/sanctions/{id}/email/approve", s.handleAdminSanctionEmailApprove())
		r.Post("/sanctions/{id}/email/skip", s.handleAdminSanctionEmailSkip())
		r.Post("/sanctions/recipients/save", s.handleAdminSanctionRecipientSave())
		r.Post("/sanctions/recipients/{id}/delete", s.handleAdminSanctionRecipientDelete())

		// Reports
		r.Get("/reports", s.handleAdminReports())
		r.Post("/reports/generate", s.handleAdminReportGenerate())
		r.Get("/reports/{id}", s.handleAdminReportView())
		r.Get("/reports/{id}/status", s.handleAdminReportStatus())
		r.Get("/reports/{id}/download", s.handleAdminReportDownload())
		r.Get("/reports/{id}/print", s.handleAdminReportPrint())

		r.Get("/csv/captains", s.handleAdminCSVGet())
		r.Post("/csv/captains/preview", s.handleAdminCSVPreview())
		r.Post("/csv/captains/apply", s.handleAdminCSVApply())
		r.Get("/play-cricket", s.handleAdminPlayCricketGet())
		r.Post("/play-cricket/sync", s.handleAdminPlayCricketSync())
		r.Post("/play-cricket/team-mapping/apply", s.handleAdminPlayCricketMappingApply())
		r.Post("/play-cricket/generate-weeks", s.handleAdminPlayCricketGenerateWeeks())
		r.Get("/teams-captains", s.handleAdminTeamsCaptainsGet())
		r.Get("/security", s.handleAdminSecurityGet())
		r.Get("/gdpr", s.handleAdminGDPRGet())
		r.Get("/gdpr/export", s.handleAdminGDPRExport())
		r.Post("/gdpr/captains/{id}/anonymise", s.handleAdminGDPRAnonymise())
		r.Post("/security/cleanup", s.handleAdminSecurityCleanupPost())
		r.Post("/clubs/save", s.handleAdminClubSave())
		r.Post("/teams/save", s.handleAdminTeamSave())
		r.Post("/teams/{id}/toggle-active", s.handleAdminTeamToggleActive())
		r.Post("/captains/save", s.handleAdminCaptainSave())
		r.Post("/captains/{id}/deactivate", s.handleAdminCaptainDeactivate())
		r.Get("/form-settings", s.handleAdminCaptainFormSettingsGet())
		r.Post("/form-settings", s.handleAdminCaptainFormSettingsPost())

		// Admin user management
		r.Get("/users", s.handleAdminUsers())
		r.Post("/users/invite", s.handleAdminUserInvite())
		r.Post("/users/{id}/deactivate", s.handleAdminUserDeactivate())
	})

	r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     adminSessionCookie,
			Value:    "",
			Path:     "/admin",
			MaxAge:   -1,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	})

	return r
}

func writeAdminLoginPage(w http.ResponseWriter, csrfToken, errMsg, username string, resetOK bool) {
	pageHead(w, "Admin Login")
	bannerHTML := ""
	if resetOK {
		bannerHTML = `<div class="alert alert-success py-2 text-start">Password updated — please log in.</div>`
	} else if errMsg != "" {
		bannerHTML = `<div class="alert alert-danger py-2 text-start" role="alert">` + escapeHTML(errMsg) + `</div>`
	}
	fmt.Fprintf(w, `<div class="container" style="max-width:440px;margin-top:5rem">
<div class="card card-gmcl shadow-sm">
  <div class="card-body text-center">
    <img src="/images/logo.webp" alt="GMCL" style="max-width:220px" class="mb-3">
    <h4 class="card-title mb-4">Admin Login</h4>
    %s
    <form method="POST" action="/admin/login">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="mb-3 text-start">
        <label class="form-label">Username</label>
        <input class="form-control" name="username" value="%s" required autofocus>
      </div>
      <div class="mb-3 text-start">
        <label class="form-label">Password</label>
        <input type="password" class="form-control" name="password" required>
      </div>
      <button type="submit" class="btn btn-primary w-100">Continue</button>
    </form>
    <div class="mt-3 text-center small">
      <a href="/admin/forgot-password">Forgot password?</a>
    </div>
  </div>
</div>
</div>
`, bannerHTML, csrfToken, escapeHTML(username))
	pageFooter(w)
}

func (s *Server) handleAdminLoginGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeAdminLoginPage(w, csrfToken, "", "", r.URL.Query().Get("reset") == "1")
	}
}

func (s *Server) handleAdminLoginPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username == "" || password == "" {
			http.Error(w, "missing credentials", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		// When DISABLE_2FA=1, verify password only (no email code).
		if os.Getenv("DISABLE_2FA") == "1" {
			adminID, err := auth.VerifyPasswordOnly(ctx, s.DB, username, password)
			if err != nil {
				s.audit(ctx, r, "admin", nil, "admin_login_failed", "admin_user", nil, map[string]any{
					"username": username,
				})
				csrfToken := middleware.CSRFToken(r)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				writeAdminLoginPage(w, csrfToken, "Invalid username or password.", username, false)
				return
			}

			// Skip 2FA — go straight to session.
			s.issueAdminSession(w, r, ctx, adminID, username)
			return
		}

		mailer := email.NewFromEnv()
		adminID, err := auth.StartAdminLogin(ctx, s.DB, mailer, username, password, r.RemoteAddr)
		if err != nil {
			log.Printf("[admin login] username=%s error=%v", username, err)
			s.audit(ctx, r, "admin", nil, "admin_login_failed", "admin_user", nil, map[string]any{
				"username": username,
			})
			csrfToken := middleware.CSRFToken(r)
			errMsg := "Invalid username or password."
			if strings.Contains(err.Error(), "2fa_email_failed") {
				errMsg = "Credentials accepted but the verification email could not be sent. Please check server email configuration."
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			writeAdminLoginPage(w, csrfToken, errMsg, username, false)
			return
		}

		// audit successful password stage and 2FA sent
		s.audit(ctx, r, "admin", &adminID, "admin_2fa_sent", "admin_user", func() *int64 {
			v := int64(adminID)
			return &v
		}(), map[string]any{})

		// For simplicity, store username in a short-lived cookie for 2FA step.
		http.SetCookie(w, &http.Cookie{
			Name:     "adm_user",
			Value:    base64.RawURLEncoding.EncodeToString([]byte(username)),
			Path:     "/admin",
			Expires:  time.Now().Add(15 * time.Minute),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/admin/2fa", http.StatusSeeOther)
	}
}

// issueAdminSession creates the signed session cookie and redirects to the dashboard
// (or change-password page if force_password_change is set).
func (s *Server) issueAdminSession(w http.ResponseWriter, r *http.Request, ctx context.Context, adminID int32, username string) {
	now := time.Now().Unix()
	sess := adminSession{
		AdminID:  adminID,
		Expiry:   now + int64((8 * time.Hour).Seconds()),
		Name:     username,
		Aud:      "adm",
		JTI:      fmt.Sprintf("%d-%d", adminID, time.Now().UnixNano()),
		IssuedAt: now,
	}
	if err := setAdminSessionCookie(w, &sess); err != nil {
		http.Error(w, "could not set session", http.StatusInternalServerError)
		return
	}

	s.audit(ctx, r, "admin", &adminID, "admin_login_success", "admin_user", func() *int64 {
		v := int64(adminID)
		return &v
	}(), map[string]any{})

	var forceChange bool
	_ = s.DB.QueryRow(ctx, `SELECT force_password_change FROM admin_users WHERE id=$1`, adminID).Scan(&forceChange)
	if forceChange {
		http.Redirect(w, r, "/admin/change-password", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
}

func (s *Server) handleAdmin2FAGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Two-Factor Authentication")
		fmt.Fprint(w, `<div class="container" style="max-width:440px;margin-top:5rem">
<div class="card card-gmcl shadow-sm">
  <div class="card-body text-center">
    <img src="/images/logo.webp" alt="GMCL" style="max-width:220px" class="mb-3">
    <h4 class="card-title mb-3">Enter 2FA Code</h4>
    <p class="text-muted">Check your email for the verification code.</p>
    <form method="POST" action="/admin/2fa">
      <input type="hidden" name="csrf_token" value="`+csrfToken+`">
      <div class="mb-3">
        <input class="form-control form-control-lg text-center" name="code" required autofocus placeholder="000000" maxlength="10">
      </div>
      <button type="submit" class="btn btn-primary w-100">Verify</button>
    </form>
  </div>
</div>
</div>
`)
		pageFooter(w)
	}
}

func (s *Server) handleAdmin2FAPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		code := strings.TrimSpace(r.FormValue("code"))
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		uc, err := r.Cookie("adm_user")
		if err != nil {
			http.Error(w, "session expired", http.StatusBadRequest)
			return
		}
		rawUser, err := base64.RawURLEncoding.DecodeString(uc.Value)
		if err != nil {
			http.Error(w, "session invalid", http.StatusBadRequest)
			return
		}
		username := string(rawUser)

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var adminID int32
		err = s.DB.QueryRow(ctx, `SELECT id FROM admin_users WHERE username = $1`, username).Scan(&adminID)
		if err != nil {
			http.Error(w, "session invalid", http.StatusBadRequest)
			return
		}

		if err := auth.VerifyAdmin2FA(ctx, s.DB, adminID, code); err != nil {
			s.audit(ctx, r, "admin", &adminID, "admin_2fa_failed", "admin_user", func() *int64 {
				v := int64(adminID)
				return &v
			}(), map[string]any{})
			http.Error(w, "invalid code", http.StatusUnauthorized)
			return
		}

		// Clear temporary cookie.
		http.SetCookie(w, &http.Cookie{
			Name:     "adm_user",
			Value:    "",
			Path:     "/admin",
			MaxAge:   -1,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		now := time.Now().Unix()
		sess := adminSession{
			AdminID:  adminID,
			Expiry:   now + int64((8 * time.Hour).Seconds()),
			Name:     username,
			Aud:      "adm",
			JTI:      fmt.Sprintf("%d-%d", adminID, time.Now().UnixNano()),
			IssuedAt: now,
		}
		if err := setAdminSessionCookie(w, &sess); err != nil {
			http.Error(w, "could not set session", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "admin", &adminID, "admin_login_success", "admin_user", func() *int64 {
			v := int64(adminID)
			return &v
		}(), map[string]any{})

		// Check if a password change is required before granting dashboard access.
		var forceChange bool
		_ = s.DB.QueryRow(ctx, `SELECT force_password_change FROM admin_users WHERE id=$1`, adminID).Scan(&forceChange)
		if forceChange {
			http.Redirect(w, r, "/admin/change-password", http.StatusSeeOther)
			return
		}

		http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminDashboard() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// ── 1. Current season/week ─────────────────────────────────────────
		var seasonID int32
		var seasonName string
		var weekID int32
		var weekNum int32
		var complianceStartWeek int32
		weekErr := s.DB.QueryRow(ctx, `
			SELECT s.id, s.name, w.id, w.week_number, s.compliance_start_week
			FROM weeks w
			JOIN seasons s ON w.season_id = s.id
			WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date
			ORDER BY w.start_date LIMIT 1
		`).Scan(&seasonID, &seasonName, &weekID, &weekNum, &complianceStartWeek)

		// Display week offset: if compliance tracking starts at week N,
		// show week numbers relative to that start so week N displays as "Week 1".
		displayWeek := weekNum
		if complianceStartWeek > 1 {
			displayWeek = weekNum - (complianceStartWeek - 1)
			if displayWeek < 1 {
				displayWeek = 1
			}
		}

		// ── 2. KPI: submissions this week / season total / avg pitch ───────
		var weekSubs, seasonSubs, activeTeams int64
		var avgPitch float64
		if weekErr == nil {
			s.DB.QueryRow(ctx, `
				SELECT
				  (SELECT COUNT(*) FROM submissions WHERE week_id=$1)   AS week_subs,
				  (SELECT COUNT(*) FROM submissions WHERE season_id=$2) AS season_subs,
				  (SELECT COUNT(*) FROM teams WHERE active=TRUE)        AS active_teams,
				  COALESCE((SELECT AVG(pitch_rating) FROM submissions WHERE season_id=$2),0) AS avg_pitch
			`, weekID, seasonID).Scan(&weekSubs, &seasonSubs, &activeTeams, &avgPitch)
		}

		// ── 3. KPI: compliance rate ────────────────────────────────────────
		var weeksElapsed int64
		var complianceRate float64
		if weekErr == nil {
			s.DB.QueryRow(ctx, `
				SELECT COUNT(*) FROM weeks
				WHERE season_id=$1 AND end_date < CURRENT_DATE
				  AND week_number >= $2
			`, seasonID, complianceStartWeek).Scan(&weeksElapsed)
			var trackingSubs int64
			s.DB.QueryRow(ctx, `
				SELECT COUNT(*) FROM submissions sub
				JOIN weeks w ON sub.week_id = w.id
				WHERE sub.season_id=$1 AND w.week_number >= $2
			`, seasonID, complianceStartWeek).Scan(&trackingSubs)
			if weeksElapsed > 0 && activeTeams > 0 {
				complianceRate = float64(trackingSubs) / float64(weeksElapsed*activeTeams) * 100
				if complianceRate > 100 {
					complianceRate = 100
				}
			}
		}

		// ── 4. KPI: open sanctions ─────────────────────────────────────────
		var openSanctions int64
		if weekErr == nil {
			s.DB.QueryRow(ctx, `
				SELECT COUNT(*) FROM sanctions WHERE season_id=$1 AND status='active'
			`, seasonID).Scan(&openSanctions)
		}

		// ── 5. Chart A: submissions per week ──────────────────────────────
		type weekBar struct {
			WeekNum int32
			Count   int64
		}
		var weekBars []weekBar
		if weekErr == nil {
			wrows, err := s.DB.Query(ctx, `
				SELECT w.week_number, COUNT(sub.id)
				FROM weeks w
				LEFT JOIN submissions sub ON sub.week_id = w.id
				WHERE w.season_id = $1
				GROUP BY w.week_number ORDER BY w.week_number
			`, seasonID)
			if err == nil {
				defer wrows.Close()
				for wrows.Next() {
					var wb weekBar
					if e := wrows.Scan(&wb.WeekNum, &wb.Count); e == nil {
						weekBars = append(weekBars, wb)
					}
				}
			}
		}

		// ── 6. Chart B: pitch rating distribution ─────────────────────────
		pitchDist := [6]int64{} // index 0 unused; 1–5
		if weekErr == nil {
			prows, err := s.DB.Query(ctx, `
				SELECT pitch_rating, COUNT(*) FROM submissions
				WHERE season_id=$1 GROUP BY pitch_rating ORDER BY pitch_rating
			`, seasonID)
			if err == nil {
				defer prows.Close()
				for prows.Next() {
					var rating int32
					var cnt int64
					if e := prows.Scan(&rating, &cnt); e == nil && rating >= 1 && rating <= 5 {
						pitchDist[rating] = cnt
					}
				}
			}
		}

		// ── 7. Chart C: submissions by club ───────────────────────────────
		type clubBar struct {
			Club  string
			Count int64
		}
		var clubBars []clubBar
		if weekErr == nil {
			crows, err := s.DB.Query(ctx, `
				SELECT cl.name, COUNT(sub.id)
				FROM submissions sub
				JOIN teams t ON sub.team_id = t.id
				JOIN clubs cl ON t.club_id = cl.id
				WHERE sub.season_id = $1
				GROUP BY cl.name ORDER BY COUNT(sub.id) DESC LIMIT 20
			`, seasonID)
			if err == nil {
				defer crows.Close()
				for crows.Next() {
					var cb clubBar
					if e := crows.Scan(&cb.Club, &cb.Count); e == nil {
						clubBars = append(clubBars, cb)
					}
				}
			}
		}

		// ── 8. Recent submissions ─────────────────────────────────────────
		type subRow struct {
			ID          int64
			Season      string
			Week        int32
			Club        string
			Team        string
			Captain     string
			MatchDate   time.Time
			SubmittedAt time.Time
		}
		var recentSubs []subRow
		srows, err := s.DB.Query(ctx, `
			SELECT sub.id, s.name, w.week_number, cl.name, t.name, c.full_name,
			       sub.match_date, sub.submitted_at
			FROM submissions sub
			JOIN seasons s  ON sub.season_id = s.id
			JOIN weeks w    ON sub.week_id   = w.id
			JOIN teams t    ON sub.team_id   = t.id
			JOIN clubs cl   ON t.club_id     = cl.id
			JOIN captains c ON sub.captain_id = c.id
			ORDER BY sub.submitted_at DESC LIMIT 20
		`)
		if err == nil {
			defer srows.Close()
			for srows.Next() {
				var sr subRow
				if e := srows.Scan(&sr.ID, &sr.Season, &sr.Week, &sr.Club, &sr.Team,
					&sr.Captain, &sr.MatchDate, &sr.SubmittedAt); e == nil {
					recentSubs = append(recentSubs, sr)
				}
			}
		}

		// ── Render ────────────────────────────────────────────────────────
		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		pitchColour := "#198754"
		if avgPitch < 3 {
			pitchColour = "#dc3545"
		} else if avgPitch < 4 {
			pitchColour = "#fd7e14"
		}

		sanctionClass := "text-dark"
		if openSanctions > 0 {
			sanctionClass = "text-danger"
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHeadWithCharts(w, "Dashboard")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)

		// Page title
		fmt.Fprintf(w, `<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Dashboard</h4>
    <p class="text-muted mb-0 small">`)
		if weekErr == nil {
			fmt.Fprintf(w, `%s &mdash; Week %d`, escapeHTML(seasonName), displayWeek)
		} else {
			fmt.Fprint(w, `No active week`)
		}
		fmt.Fprint(w, `</p>
  </div>
  <a href="/admin/compliance" class="btn btn-sm btn-outline-primary">View Compliance</a>
</div>
`)

		// ── KPI cards ────────────────────────────────────────────────────
		fmt.Fprint(w, `<div class="row g-3 mb-4">`)

		kpiCard := func(accentClass, number, label, sublabel, icon string) {
			fmt.Fprintf(w, `
<div class="col-6 col-md-4 col-xl-2">
  <div class="card card-kpi %s h-100">
    <div class="card-body p-3">
      <div class="d-flex justify-content-between align-items-start">
        <div>
          <div class="kpi-number">%s</div>
          <div class="kpi-label mt-1">%s</div>
          %s
        </div>
        <div class="kpi-icon">%s</div>
      </div>
    </div>
  </div>
</div>`, accentClass, number, label, sublabel, icon)
		}

		if weekErr != nil {
			fmt.Fprint(w, `<div class="col-12"><div class="alert alert-warning mb-0">No active week — check seed data.</div></div>`)
		} else {
			compStr := fmt.Sprintf("%.1f%%", complianceRate)
			pitchStr := fmt.Sprintf(`<span style="color:%s">%.1f</span>`, pitchColour, avgPitch)
			sanctStr := fmt.Sprintf(`<span class="%s">%d</span>`, sanctionClass, openSanctions)

			kpiCard("kpi-red", fmt.Sprintf("%d", displayWeek),
				"Current Week", fmt.Sprintf(`<div class="text-muted" style="font-size:.75rem">%s</div>`, escapeHTML(seasonName)), "📅")
			kpiCard("kpi-blue", fmt.Sprintf("%d", weekSubs),
				"This Week", fmt.Sprintf(`<div class="text-muted" style="font-size:.75rem">of %d teams</div>`, activeTeams), "📋")
			kpiCard("kpi-teal", fmt.Sprintf("%d", seasonSubs),
				"Season Total", ``, "📊")
			kpiCard("kpi-green", compStr,
				"Compliance", ``, "✅")
			kpiCard("kpi-amber", pitchStr,
				"Avg Pitch", `<div class="text-muted" style="font-size:.75rem">1–5 scale</div>`, "🏏")
			kpiCard("kpi-purple", sanctStr,
				"Open Sanctions", fmt.Sprintf(`<a href="/admin/sanctions" class="text-muted" style="font-size:.75rem">view all</a>`), "🃏")
		}

		fmt.Fprint(w, `</div>`) // end KPI row

		// ── Charts row ───────────────────────────────────────────────────
		fmt.Fprint(w, `<div class="row g-3 mb-4">`)

		// Chart A – submissions per week
		fmt.Fprint(w, `
<div class="col-12 col-xl-8">
  <div class="card shadow-sm h-100">
    <div class="card-header fw-semibold">Submissions per Week</div>
    <div class="card-body">
      <div class="chart-container"><canvas id="chartWeeks"></canvas></div>
    </div>
  </div>
</div>`)

		// Chart B – pitch distribution
		fmt.Fprint(w, `
<div class="col-12 col-xl-4">
  <div class="card shadow-sm h-100">
    <div class="card-header fw-semibold">Pitch Rating Distribution</div>
    <div class="card-body">
      <div class="chart-container"><canvas id="chartPitch"></canvas></div>
    </div>
  </div>
</div>`)

		fmt.Fprint(w, `</div>`) // end charts row

		// Chart C – club breakdown (full width)
		fmt.Fprint(w, `
<div class="row g-3 mb-4">
<div class="col-12">
  <div class="card shadow-sm">
    <div class="card-header fw-semibold">Submissions by Club</div>
    <div class="card-body">
      <div class="chart-container-lg"><canvas id="chartClubs"></canvas></div>
    </div>
  </div>
</div>
</div>`)

		// ── Recent submissions table ──────────────────────────────────────
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header d-flex justify-content-between align-items-center">
    <span class="fw-semibold">Recent Submissions</span>
    <a href="/admin/weeks" class="btn btn-sm btn-outline-secondary">All Weeks</a>
  </div>
  <div class="table-responsive">
    <table class="table table-hover table-sm table-gmcl mb-0">
      <thead><tr>
        <th>ID</th><th>Season</th><th>Wk</th><th>Club</th><th>Team</th>
        <th>Captain</th><th>Match Date</th><th>Submitted</th>
      </tr></thead>
      <tbody>
`)
		for _, sr := range recentSubs {
			fmt.Fprintf(w,
				`<tr><td><a href="/admin/submissions/%d">#%d</a></td><td>%s</td><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="text-muted">%s</td></tr>`,
				sr.ID, sr.ID, escapeHTML(sr.Season), sr.Week,
				escapeHTML(sr.Club), escapeHTML(sr.Team), escapeHTML(sr.Captain),
				sr.MatchDate.Format("2006-01-02"), sr.SubmittedAt.Format("02 Jan 15:04"))
		}
		if len(recentSubs) == 0 {
			fmt.Fprint(w, `<tr><td colspan="8" class="text-center text-muted py-3">No submissions yet.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>`)

		// ── Chart.js init scripts ─────────────────────────────────────────
		// Build JSON arrays for Chart.js
		weekLabels, _ := json.Marshal(func() []int32 {
			var l []int32
			for _, b := range weekBars {
				l = append(l, b.WeekNum)
			}
			return l
		}())
		weekData, _ := json.Marshal(func() []int64 {
			var d []int64
			for _, b := range weekBars {
				d = append(d, b.Count)
			}
			return d
		}())
		clubLabels, _ := json.Marshal(func() []string {
			var l []string
			for _, b := range clubBars {
				l = append(l, b.Club)
			}
			return l
		}())
		clubData, _ := json.Marshal(func() []int64 {
			var d []int64
			for _, b := range clubBars {
				d = append(d, b.Count)
			}
			return d
		}())

		script := fmt.Sprintf(`
Chart.defaults.font.family = "'Segoe UI', system-ui, sans-serif";
Chart.defaults.color = '#6c757d';

// Chart A — submissions per week
new Chart(document.getElementById('chartWeeks'), {
  type: 'bar',
  data: {
    labels: %s,
    datasets: [{
      label: 'Submissions',
      data: %s,
      backgroundColor: 'rgba(196,30,58,.75)',
      borderColor: '#C41E3A',
      borderWidth: 1,
      borderRadius: 4
    }]
  },
  options: {
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { display: false } },
    scales: {
      y: { beginAtZero: true, ticks: { stepSize: 1 } },
      x: { grid: { display: false } }
    }
  }
});

// Chart B — pitch distribution
new Chart(document.getElementById('chartPitch'), {
  type: 'doughnut',
  data: {
    labels: ['Rating 1','Rating 2','Rating 3','Rating 4','Rating 5'],
    datasets: [{
      data: [%d,%d,%d,%d,%d],
      backgroundColor: ['#dc3545','#fd7e14','#ffc107','#20c997','#198754'],
      borderWidth: 2,
      borderColor: '#fff'
    }]
  },
  options: {
    responsive: true, maintainAspectRatio: false,
    plugins: {
      legend: { position: 'bottom', labels: { boxWidth: 12 } }
    },
    cutout: '65%%'
  }
});

// Chart C — club breakdown
new Chart(document.getElementById('chartClubs'), {
  type: 'bar',
  data: {
    labels: %s,
    datasets: [{
      label: 'Submissions',
      data: %s,
      backgroundColor: 'rgba(46,134,193,.75)',
      borderColor: '#2E86C1',
      borderWidth: 1,
      borderRadius: 4
    }]
  },
  options: {
    indexAxis: 'y',
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { display: false } },
    scales: {
      x: { beginAtZero: true, ticks: { stepSize: 1 } },
      y: { grid: { display: false } }
    }
  }
});
`,
			string(weekLabels), string(weekData),
			pitchDist[1], pitchDist[2], pitchDist[3], pitchDist[4], pitchDist[5],
			string(clubLabels), string(clubData),
		)

		pageFooterWithScript(w, script)
	}
}

func (s *Server) handleAdminWeeks() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		rows, err := s.DB.Query(ctx, `
			SELECT w.id, s.name, w.week_number, w.start_date, w.end_date, COUNT(sub.id) AS submissions,
			       (CURRENT_DATE BETWEEN w.start_date AND w.end_date) AS is_current
			FROM weeks w
			JOIN seasons s ON w.season_id = s.id
			LEFT JOIN submissions sub ON sub.week_id = w.id
			WHERE s.id = (
			    SELECT id FROM seasons
			    WHERE CURRENT_DATE BETWEEN start_date AND end_date
			    ORDER BY start_date DESC LIMIT 1
			)
			GROUP BY w.id, s.name, w.week_number, w.start_date, w.end_date
			ORDER BY w.week_number
		`)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type row struct {
			WeekID    int32
			Season    string
			Week      int32
			StartDate time.Time
			EndDate   time.Time
			Count     int64
			IsCurrent bool
		}
		var data []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.WeekID, &r.Season, &r.Week, &r.StartDate, &r.EndDate, &r.Count, &r.IsCurrent); err != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}
			data = append(data, r)
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Weeks")
		writeAdminNav(w, csrfToken, r.URL.Path)
		fmt.Fprint(w, `<div class="container-fluid">
<h3 class="mb-4">Weekly Submissions</h3>
<div class="card card-gmcl shadow-sm">
  <div class="table-responsive">
    <table class="table table-hover table-striped table-gmcl mb-0">
      <thead><tr><th>Season</th><th>Week</th><th>Start Date</th><th>End Date</th><th>Submissions</th></tr></thead>
      <tbody>
`)
		for _, d := range data {
			rowClass := ""
			if d.IsCurrent {
				rowClass = ` class="table-warning fw-bold"`
			}
			fmt.Fprintf(w, `<tr%s style="cursor:pointer" onclick="location.href='/admin/weeks/%d'"><td>%s</td><td>%d</td><td>%s</td><td>%s</td><td>%d</td></tr>`,
				rowClass, d.WeekID, d.Season, d.Week,
				d.StartDate.Format("2 Jan 2006"), d.EndDate.Format("2 Jan 2006"), d.Count)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>
`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminWeekDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		weekID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 32)
		if err != nil {
			http.Error(w, "invalid week id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var seasonName string
		var weekNum int32
		var startDate, endDate time.Time
		err = s.DB.QueryRow(ctx, `
			SELECT s.name, w.week_number, w.start_date, w.end_date
			FROM weeks w JOIN seasons s ON s.id = w.season_id
			WHERE w.id = $1
		`, int32(weekID)).Scan(&seasonName, &weekNum, &startDate, &endDate)
		if err != nil {
			http.Error(w, "week not found", http.StatusNotFound)
			return
		}

		type subRow struct {
			ID               int64
			Club             string
			Team             string
			Captain          string
			MatchDate        time.Time
			MatchOutcome     string
			Umpire1          string
			Umpire2          string
			PitchRating      int
			OutfieldRating   int
			FacilitiesRating int
			SubmittedByRole  string
			CreatedAt        time.Time
		}

		rows, err := s.DB.Query(ctx, `
			SELECT sub.id, cl.name, t.name, c.full_name,
			       sub.match_date, COALESCE(sub.form_data->>'match_outcome','played'),
			       COALESCE(sub.form_data->>'umpire1_name',''), COALESCE(sub.form_data->>'umpire2_name',''),
			       sub.pitch_rating, sub.outfield_rating, sub.facilities_rating,
			       COALESCE(sub.submitted_by_role,'captain'), sub.created_at
			FROM submissions sub
			JOIN captains c ON c.id = sub.captain_id
			JOIN teams t ON t.id = sub.team_id
			JOIN clubs cl ON cl.id = t.club_id
			WHERE sub.week_id = $1
			ORDER BY sub.created_at
		`, int32(weekID))
		if err != nil {
			http.Error(w, "error loading submissions", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var subs []subRow
		for rows.Next() {
			var s subRow
			if err := rows.Scan(&s.ID, &s.Club, &s.Team, &s.Captain,
				&s.MatchDate, &s.MatchOutcome, &s.Umpire1, &s.Umpire2,
				&s.PitchRating, &s.OutfieldRating, &s.FacilitiesRating,
				&s.SubmittedByRole, &s.CreatedAt); err != nil {
				continue
			}
			subs = append(subs, s)
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, fmt.Sprintf("Week %d Submissions", weekNum))
		writeAdminNav(w, csrfToken, "/admin/weeks")
		fmt.Fprintf(w, `<div class="container-fluid">
<div class="d-flex align-items-center gap-3 mb-3">
  <a href="/admin/weeks" class="btn btn-outline-secondary btn-sm">&larr; All Weeks</a>
  <h3 class="mb-0">Week %d &mdash; %s &ndash; %s <span class="fs-6 text-muted">(%s)</span></h3>
</div>
`, weekNum, startDate.Format("2 Jan 2006"), endDate.Format("2 Jan 2006"), seasonName)

		if len(subs) == 0 {
			fmt.Fprint(w, `<div class="alert alert-info">No submissions for this week yet.</div>`)
		} else {
			fmt.Fprintf(w, `<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-header bg-gmcl text-white d-flex justify-content-between align-items-center">
    <strong>%d submission(s)</strong>
  </div>
  <div class="table-responsive">
    <table class="table table-hover table-striped table-gmcl mb-0">
      <thead><tr>
        <th>Club</th><th>Team</th><th>Captain</th><th>Match Date</th>
        <th>Outcome</th><th>Umpire 1</th><th>Umpire 2</th>
        <th>Pitch</th><th>Outfield</th><th>Facilities</th><th>Submitted</th><th></th>
      </tr></thead>
      <tbody>
`, len(subs))
			for _, s := range subs {
				outcome := strings.ReplaceAll(s.MatchOutcome, "_", " ")
				fmt.Fprintf(w, `<tr>
  <td>%s</td><td>%s</td><td>%s</td><td>%s</td>
  <td>%s</td><td>%s</td><td>%s</td>
  <td>%d</td><td>%d</td><td>%d</td>
  <td class="text-nowrap small">%s</td>
  <td><a href="/admin/submissions/%d" class="btn btn-outline-secondary btn-sm">View</a></td>
</tr>`,
					escapeHTML(s.Club), escapeHTML(s.Team), escapeHTML(s.Captain),
					s.MatchDate.Format("2 Jan 2006"),
					escapeHTML(outcome), escapeHTML(s.Umpire1), escapeHTML(s.Umpire2),
					s.PitchRating, s.OutfieldRating, s.FacilitiesRating,
					s.CreatedAt.Format("2 Jan 15:04"),
					s.ID)
			}
			fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>`)
		}
		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSubmissionDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var season string
		var week int32
		var clubName, teamName string
		var captainName string
		var matchDate time.Time
		var pitch, outfield, facilities int32
		var comments string
		var submittedByName, submittedByEmail, submittedByRole string
		var umpire1Type, umpire2Type string
		var formDataJSON []byte

		err = s.DB.QueryRow(ctx, `
			SELECT s.name, w.week_number, cl.name, t.name, c.full_name,
			       sub.match_date, sub.pitch_rating, sub.outfield_rating, sub.facilities_rating,
			       COALESCE(sub.comments, ''), COALESCE(sub.submitted_by_name, ''),
			       COALESCE(sub.submitted_by_email, ''), COALESCE(sub.submitted_by_role, 'captain'),
			       COALESCE(sub.umpire1_type, 'panel'), COALESCE(sub.umpire2_type, 'panel'),
			       sub.form_data
			FROM submissions sub
			JOIN weeks w ON sub.week_id = w.id
			JOIN seasons s ON sub.season_id = s.id
			JOIN teams t ON sub.team_id = t.id
			JOIN clubs cl ON t.club_id = cl.id
			JOIN captains c ON sub.captain_id = c.id
			WHERE sub.id = $1
		`, id).Scan(&season, &week, &clubName, &teamName, &captainName, &matchDate, &pitch, &outfield, &facilities,
			&comments, &submittedByName, &submittedByEmail, &submittedByRole, &umpire1Type, &umpire2Type, &formDataJSON)
		if err != nil {
			if err == pgx.ErrNoRows {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, fmt.Sprintf("Submission %d", id))
		writeAdminNav(w, csrfToken, r.URL.Path)
		fmt.Fprintf(w, `<div class="container-fluid">
<h3 class="mb-4">Submission %d</h3>
<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-header"><strong>Metadata</strong></div>
  <div class="card-body">
    <dl class="row mb-0">
      <dt class="col-sm-3">Season</dt><dd class="col-sm-9">%s &ndash; Week %d</dd>
      <dt class="col-sm-3">Club / Team</dt><dd class="col-sm-9">%s &ndash; %s</dd>
      <dt class="col-sm-3">Captain</dt><dd class="col-sm-9">%s</dd>
      <dt class="col-sm-3">Submitted By</dt><dd class="col-sm-9">%s (%s)</dd>
      <dt class="col-sm-3">Match Date</dt><dd class="col-sm-9">%s</dd>
      <dt class="col-sm-3">Pitch Rating</dt><dd class="col-sm-9">%d</dd>
      <dt class="col-sm-3">Outfield Rating</dt><dd class="col-sm-9">%d</dd>
      <dt class="col-sm-3">Facilities Rating</dt><dd class="col-sm-9">%d</dd>
      <dt class="col-sm-3">Umpire Types</dt><dd class="col-sm-9">Umpire 1: %s, Umpire 2: %s</dd>
`, id, season, week, clubName, teamName, captainName, func() string {
			if submittedByRole == "delegate" {
				if submittedByName != "" {
					return submittedByName
				}
				return submittedByEmail
			}
			return captainName
		}(), submittedByRole, matchDate.Format("2006-01-02"), pitch, outfield, facilities, umpire1Type, umpire2Type)
		if comments != "" {
			fmt.Fprintf(w, `      <dt class="col-sm-3">Comments</dt><dd class="col-sm-9">%s</dd>
`, escapeHTML(comments))
		}
		fmt.Fprint(w, `    </dl>
  </div>
</div>
`)

		if len(formDataJSON) > 0 {
			var formData map[string]any
			if json.Unmarshal(formDataJSON, &formData) == nil {
				fmt.Fprint(w, `<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-header"><strong>GMCL Form Data</strong></div>
  <div class="card-body">
    <dl class="row mb-0">
`)
				for _, key := range []string{"match_date", "umpire1_name", "umpire2_name", "umpire1_type", "umpire2_type", "umpire_names_other", "your_team",
					"umpire1_performance", "umpire2_performance", "umpire_comments", "umpire_comments_detail",
					"unevenness_of_bounce", "seam_movement", "carry_bounce", "turn"} {
					if v, ok := formData[key]; ok && v != nil {
						fmt.Fprintf(w, `      <dt class="col-sm-4">%s</dt><dd class="col-sm-8">%v</dd>
`, key, v)
					}
				}
				fmt.Fprint(w, `    </dl>
  </div>
</div>
`)
			}
		}

		fmt.Fprint(w, `<a href="/admin/dashboard" class="btn btn-outline-primary">&larr; Back to dashboard</a>
</div>
`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminRankings() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		// Season selector
		var seasonID int32
		var seasonName string
		if sid := r.URL.Query().Get("season_id"); sid != "" {
			n, _ := strconv.Atoi(sid)
			seasonID = int32(n)
			s.DB.QueryRow(ctx, `SELECT name FROM seasons WHERE id=$1`, seasonID).Scan(&seasonName)
		}
		if seasonID == 0 {
			s.DB.QueryRow(ctx, `
				SELECT s.id, s.name FROM weeks w JOIN seasons s ON w.season_id=s.id
				WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date LIMIT 1
			`).Scan(&seasonID, &seasonName)
		}

		// All seasons for selector
		type season struct {
			ID   int32
			Name string
		}
		var seasons []season
		srows, _ := s.DB.Query(ctx, `SELECT id, name FROM seasons WHERE is_archived=FALSE ORDER BY start_date DESC`)
		if srows != nil {
			defer srows.Close()
			for srows.Next() {
				var ss season
				if srows.Scan(&ss.ID, &ss.Name) == nil {
					seasons = append(seasons, ss)
				}
			}
		}

		rows, err := s.DB.Query(ctx, `
			SELECT COALESCE(home_cl.name, cl.name)                           AS club_name,
			       COUNT(sub.id)                                              AS submissions,
			       ROUND(AVG(sub.pitch_rating)::numeric, 2)                  AS avg_pitch,
			       ROUND(AVG((sub.form_data->>'unevenness_of_bounce')::numeric)::numeric,2) AS avg_bounce,
			       ROUND(AVG((sub.form_data->>'seam_movement')::numeric)::numeric,2)        AS avg_seam,
			       ROUND(AVG((sub.form_data->>'carry_bounce')::numeric)::numeric,2)         AS avg_carry,
			       ROUND(AVG((sub.form_data->>'turn')::numeric)::numeric,2)                 AS avg_turn,
			       COALESCE(sc.sanction_count, 0)                            AS sanctions
			FROM submissions sub
			JOIN teams t   ON sub.team_id   = t.id
			JOIN clubs cl  ON t.club_id     = cl.id
			LEFT JOIN clubs home_cl ON home_cl.id = sub.home_club_id
			LEFT JOIN (
			    SELECT club_id, COUNT(*) AS sanction_count FROM sanctions
			    WHERE season_id = $1 AND status = 'active' GROUP BY club_id
			) sc ON COALESCE(home_cl.id, cl.id) = sc.club_id
			WHERE sub.season_id = $1
			GROUP BY COALESCE(home_cl.name, cl.name), sc.sanction_count
			ORDER BY avg_pitch DESC NULLS LAST
		`, seasonID)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type rankRow struct {
			Club      string
			Subs      int64
			AvgPitch  float64
			AvgBounce float64
			AvgSeam   float64
			AvgCarry  float64
			AvgTurn   float64
			Sanctions int64
		}
		var data []rankRow
		var totalSubs int64
		var totalAvgPitch float64
		for rows.Next() {
			var rr rankRow
			if err := rows.Scan(&rr.Club, &rr.Subs, &rr.AvgPitch, &rr.AvgBounce,
				&rr.AvgSeam, &rr.AvgCarry, &rr.AvgTurn, &rr.Sanctions); err != nil {
				continue
			}
			totalSubs += rr.Subs
			totalAvgPitch += rr.AvgPitch
			data = append(data, rr)
		}
		avgPitchOverall := 0.0
		if len(data) > 0 {
			avgPitchOverall = totalAvgPitch / float64(len(data))
		}

		// Chart data — top 15 by avg pitch
		var chartLabels, chartPitch []string
		limit := 15
		if len(data) < limit {
			limit = len(data)
		}
		for i := 0; i < limit; i++ {
			lb, _ := json.Marshal(data[i].Club)
			chartLabels = append(chartLabels, string(lb))
			chartPitch = append(chartPitch, fmt.Sprintf("%.2f", data[i].AvgPitch))
		}
		labelsJSON := "[" + joinStrings(chartLabels, ",") + "]"
		pitchJSON := "[" + joinStrings(chartPitch, ",") + "]"

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		ratingBadge := func(v float64) string {
			if v == 0 {
				return `<span class="text-muted">—</span>`
			}
			cls := "text-success"
			if v < 3 {
				cls = "text-danger"
			} else if v < 4 {
				cls = "text-warning"
			}
			return fmt.Sprintf(`<span class="%s fw-semibold">%.2f</span>`, cls, v)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHeadWithCharts(w, "Club Rankings")
		writeAdminNav(w, csrfToken, r.URL.Path)
		fmt.Fprint(w, `<div class="container-fluid px-4">`)

		// Header + season selector
		fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Club Rankings</h4>
    <p class="text-muted mb-0 small">Pitch quality ranked by average score — bounce/seam/carry/turn rated 1–6</p>
  </div>
  <div class="d-flex gap-2 align-items-center">
    <form method="GET" action="/admin/rankings" class="d-flex gap-2 align-items-center">
      <select name="season_id" class="form-select form-select-sm" onchange="this.form.submit()">`)
		for _, ss := range seasons {
			sel := ""
			if ss.ID == seasonID {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, ss.ID, sel, escapeHTML(ss.Name))
		}
		fmt.Fprintf(w, `      </select>
    </form>
    <a href="/admin/rankings/umpires" class="btn btn-sm btn-outline-primary">Umpire Rankings</a>
  </div>
</div>`)

		// KPI cards
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-blue text-center p-3">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Clubs Ranked</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-teal text-center p-3">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Total Submissions</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-green text-center p-3">
      <div class="kpi-number">%.2f</div>
      <div class="kpi-label">Avg Pitch Rating</div>
    </div>
  </div>
</div>`, len(data), totalSubs, avgPitchOverall)

		// Chart
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-12">
    <div class="card shadow-sm">
      <div class="card-header fw-semibold">Top 15 Clubs — Average Pitch Rating</div>
      <div class="card-body">
        <div class="chart-container-lg"><canvas id="chartClubPitch"></canvas></div>
      </div>
    </div>
  </div>
</div>
<script>
(function(){
  var ctx = document.getElementById('chartClubPitch');
  if(!ctx) return;
  new Chart(ctx, {
    type: 'bar',
    data: {
      labels: %s,
      datasets: [{ label: 'Avg Pitch', data: %s, backgroundColor: '#8b0000' }]
    },
    options: { indexAxis: 'y', responsive: true, maintainAspectRatio: false,
      scales: { x: { min: 0, max: 5 } },
      plugins: { legend: { display: false } } }
  });
})();
</script>`, labelsJSON, pitchJSON)

		// Table
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">All Clubs</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>#</th><th>Club</th><th>Submissions</th>
        <th>Avg Pitch ↑</th><th>Bounce</th><th>Seam</th><th>Carry</th><th>Turn</th>
        <th>Sanctions</th>
      </tr></thead>
      <tbody>
`)
		for i, d := range data {
			sanBadge := `<span class="text-muted">—</span>`
			if d.Sanctions > 0 {
				sanBadge = fmt.Sprintf(`<span class="badge badge-yellow-card">%d</span>`, d.Sanctions)
			}
			fmt.Fprintf(w,
				`<tr><td class="text-muted">%d</td><td><strong>%s</strong></td><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				i+1, escapeHTML(d.Club), d.Subs,
				ratingBadge(d.AvgPitch), ratingBadge(d.AvgBounce), ratingBadge(d.AvgSeam),
				ratingBadge(d.AvgCarry), ratingBadge(d.AvgTurn), sanBadge)
		}
		if len(data) == 0 {
			fmt.Fprint(w, `<tr><td colspan="9" class="text-center text-muted py-3">No data yet.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>
`)
		pageFooter(w)
	}
}

// CSV upload (captains) preview/apply.

type csvPreview struct {
	Rows       []captainCSVRow `json:"rows"`
	Errors     []string        `json:"errors"`
	Checksum   string          `json:"checksum"`
	RowCount   int             `json:"row_count"`
	ValidCount int             `json:"valid_count"`
}

type captainCSVRow struct {
	Club   string
	Team   string
	Name   string
	Email  string
	Phone  string
	Errors []string
}

const csvPreviewCookie = "csv_token"

func (s *Server) handleAdminCSVGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "CSV Upload")
		writeAdminNav(w, csrfToken, r.URL.Path)
		fmt.Fprintf(w, `<div class="container-fluid">
<h3 class="mb-4">Upload Captains CSV</h3>
<div class="card card-gmcl shadow-sm" style="max-width:600px">
  <div class="card-body">
    <form method="POST" action="/admin/csv/captains/preview" enctype="multipart/form-data">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="mb-3">
        <label class="form-label">CSV file</label>
        <input type="file" class="form-control" name="file" accept=".csv" required>
      </div>
      <p class="text-muted small">Accepted headers: <code>club,team,captain_name,captain_email</code> or <code>first name,last name,email,MobileTel,Club,Team</code>. Missing clubs or teams will be created on apply.</p>
      <button type="submit" class="btn btn-primary">Preview</button>
    </form>
  </div>
</div>
</div>
`, escapeHTML(csrfToken))
		pageFooter(w)
	}
}

func (s *Server) handleAdminCSVPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(5 << 20); err != nil { // 5MiB
			http.Error(w, "file too large", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		limited := io.LimitReader(file, 5<<20)
		reader := csv.NewReader(limited)
		reader.FieldsPerRecord = -1

		header, err := reader.Read()
		if err != nil {
			http.Error(w, "invalid csv", http.StatusBadRequest)
			return
		}

		layout, err := parseCaptainCSVLayout(header)
		if err != nil {
			http.Error(w, "unexpected header", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		resolver, err := newCaptainCSVResolver(ctx, s)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		var (
			rows    []captainCSVRow
			errors  []string
			check   = sha256.New()
			rowNum  = 0
			seenKey = make(map[string]struct{})
		)

		for {
			record, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				errors = append(errors, fmt.Sprintf("row %d: %v", rowNum+2, err))
				break
			}
			rowNum++
			if rowNum > 2000 {
				errors = append(errors, "too many rows (max 2000)")
				break
			}

			for _, field := range record {
				check.Write([]byte(field))
			}

			rw := layout.buildRow(record)

			// Basic validation
			if rw.Club == "" || rw.Team == "" || rw.Name == "" || rw.Email == "" {
				rw.Errors = append(rw.Errors, "all fields required")
			}
			if len(rw.Name) > 200 {
				rw.Errors = append(rw.Errors, "name too long")
			}
			if len(rw.Email) > 320 || !strings.Contains(rw.Email, "@") {
				rw.Errors = append(rw.Errors, "invalid email")
			}

			// Defend against CSV formula injection.
			for _, f := range record {
				fs := strings.TrimSpace(f)
				if fs != "" && (fs[0] == '=' || fs[0] == '+' || fs[0] == '-' || fs[0] == '@') {
					rw.Errors = append(rw.Errors, "field looks like spreadsheet formula")
					break
				}
			}

			// Canonicalize existing club/team names where possible; apply will create missing rows.
			if resolvedClub, resolvedTeam, clubFound, teamFound := resolver.resolveClubAndTeam(rw.Club, rw.Team); clubFound {
				rw.Club = resolvedClub
				if teamFound {
					rw.Team = resolvedTeam
				} else if layout.requireExistingTeams {
					rw.Errors = append(rw.Errors, "team not found in database")
				}
			} else if layout.requireExistingTeams {
				rw.Errors = append(rw.Errors, "club not found in database")
			}

			key := normalizeCaptainCSVClubKey(rw.Club) + "|" + normalizeCaptainCSVTeamKey(rw.Team) + "|" + strings.ToLower(rw.Email)
			if _, found := seenKey[key]; found {
				rw.Errors = append(rw.Errors, "duplicate row in file")
			} else {
				seenKey[key] = struct{}{}
			}

			rows = append(rows, rw)
		}

		validCount := 0
		for _, rrow := range rows {
			if len(rrow.Errors) == 0 {
				validCount++
			}
		}

		sum := base64.RawURLEncoding.EncodeToString(check.Sum(nil))
		prev := csvPreview{
			Rows:       rows,
			Errors:     errors,
			Checksum:   sum,
			RowCount:   len(rows),
			ValidCount: validCount,
		}
		previewJSON, err := json.Marshal(prev)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		// Persist preview server-side using csv_preview_tokens.
		adminID := int32(0)
		if sess, err := getAdminSessionFromRequest(r); err == nil {
			adminID = sess.AdminID
		}

		rawToken := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, rawToken); err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		token := base64.RawURLEncoding.EncodeToString(rawToken)
		hashBytes := sha256.Sum256([]byte(token))

		id := uuid.New()
		expires := time.Now().Add(30 * time.Minute)

		_, err = s.DB.Exec(ctx, `
			INSERT INTO csv_preview_tokens (id, token_hash, admin_user_id, checksum, preview_json, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, id, hashBytes[:], adminID, sum, previewJSON, expires)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     csvPreviewCookie,
			Value:    token,
			Path:     "/admin",
			Expires:  time.Now().Add(30 * time.Minute),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "CSV Preview")
		writeAdminNav(w, csrfToken, r.URL.Path)
		fmt.Fprintf(w, `<div class="container-fluid">
<h3 class="mb-4">Preview Captains CSV</h3>
<p>Rows: <strong>%d</strong> &nbsp;|&nbsp; Valid: <strong class="text-success">%d</strong> &nbsp;|&nbsp; Skipped/errors: <strong class="text-danger">%d</strong></p>
`, prev.RowCount, prev.ValidCount, prev.RowCount-prev.ValidCount)
		if len(prev.Errors) > 0 {
			fmt.Fprint(w, `<div class="alert alert-danger"><strong>File errors:</strong><ul class="mb-0">`)
			for _, e := range prev.Errors {
				fmt.Fprintf(w, "<li>%s</li>", e)
			}
			fmt.Fprint(w, "</ul></div>")
		}
		fmt.Fprintf(w, `<div class="card card-gmcl shadow-sm mb-4" style="max-width:600px">
  <div class="card-body">
    <form method="POST" action="/admin/csv/captains/apply">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="mb-3">
        <label class="form-label">Apply mode</label>
        <select class="form-select" name="mode">
          <option value="all_or_nothing">Fail all if any errors</option>
          <option value="valid_only" selected>Apply valid rows only</option>
        </select>
      </div>
      <button type="submit" class="btn btn-primary">Apply</button>
    </form>
  </div>
</div>
<div class="card card-gmcl shadow-sm mb-4">
  <div class="table-responsive">
    <table class="table table-sm table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Status</th><th>Club</th><th>Team</th><th>Name</th><th>Email</th><th>Reason</th>
      </tr></thead>
      <tbody>
`, escapeHTML(csrfToken))
		for _, rr := range prev.Rows {
			status := `<span class="badge bg-success">OK</span>`
			reason := ""
			if len(rr.Errors) > 0 {
				status = `<span class="badge bg-danger">Skip</span>`
				reason = strings.Join(rr.Errors, "; ")
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="text-danger small">%s</td></tr>`,
				status,
				escapeHTML(rr.Club), escapeHTML(rr.Team),
				escapeHTML(rr.Name), escapeHTML(rr.Email),
				escapeHTML(reason))
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>
`)
		pageFooter(w)

		// audit preview creation
		s.audit(ctx, r, "admin", nil, "csv_captains_preview", "csv_upload", nil, map[string]any{
			"checksum":    prev.Checksum,
			"row_count":   prev.RowCount,
			"valid_count": prev.ValidCount,
		})
	}
}

func (s *Server) handleAdminCSVApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		mode := r.FormValue("mode")
		if mode != "all_or_nothing" && mode != "valid_only" {
			http.Error(w, "invalid mode", http.StatusBadRequest)
			return
		}

		c, err := r.Cookie(csvPreviewCookie)
		if err != nil {
			http.Error(w, "preview expired", http.StatusBadRequest)
			return
		}
		rawToken := c.Value
		hashBytes := sha256.Sum256([]byte(rawToken))

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)

		// Load preview row FOR UPDATE and ensure single-use and expiry.
		var storedHash []byte
		var previewJSON []byte
		var checksum string
		var expiresAt time.Time
		var usedAt *time.Time
		err = tx.QueryRow(ctx, `
			SELECT token_hash, preview_json, checksum, expires_at, used_at
			FROM csv_preview_tokens
			WHERE token_hash = $1
			FOR UPDATE
		`, hashBytes[:]).Scan(&storedHash, &previewJSON, &checksum, &expiresAt, &usedAt)
		if err != nil {
			if err == pgx.ErrNoRows {
				http.Error(w, "preview invalid", http.StatusBadRequest)
				return
			}
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		if subtle.ConstantTimeCompare(storedHash, hashBytes[:]) != 1 {
			http.Error(w, "preview invalid", http.StatusBadRequest)
			return
		}
		if usedAt != nil {
			http.Error(w, "preview already used", http.StatusBadRequest)
			return
		}
		if time.Now().After(expiresAt) {
			http.Error(w, "preview expired", http.StatusBadRequest)
			return
		}

		var prev csvPreview
		if err := json.Unmarshal(previewJSON, &prev); err != nil {
			http.Error(w, "preview invalid", http.StatusBadRequest)
			return
		}

		if mode == "all_or_nothing" {
			for _, rrow := range prev.Rows {
				if len(rrow.Errors) > 0 {
					http.Error(w, "errors present; not applying", http.StatusBadRequest)
					return
				}
			}
		}

		// mark preview as used
		_, err = tx.Exec(ctx, `
			UPDATE csv_preview_tokens
			SET used_at = now()
			WHERE token_hash = $1 AND used_at IS NULL
		`, hashBytes[:])
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		created := 0
		updated := 0

		for _, rrow := range prev.Rows {
			if len(rrow.Errors) > 0 && mode == "valid_only" {
				continue
			}

			// defence-in-depth: re-check for formula-like fields and length.
			fields := []string{rrow.Club, rrow.Team, rrow.Name, rrow.Email, rrow.Phone}
			invalidRow := false
			for _, f := range fields {
				fs := strings.TrimSpace(f)
				if fs != "" && (fs[0] == '=' || fs[0] == '+' || fs[0] == '-' || fs[0] == '@') {
					invalidRow = true
					break
				}
				if len(fs) > 320 {
					invalidRow = true
					break
				}
			}
			if invalidRow {
				if mode == "all_or_nothing" {
					http.Error(w, "preview invalid", http.StatusBadRequest)
					return
				}
				continue
			}

			var clubID int32
			err = tx.QueryRow(ctx, `
				INSERT INTO clubs (name) VALUES ($1)
				ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
				RETURNING id
			`, rrow.Club).Scan(&clubID)
			if err != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}

			var teamID int32
			err = tx.QueryRow(ctx, `
				INSERT INTO teams (club_id, name, active) VALUES ($1, $2, TRUE)
				ON CONFLICT (club_id, name) DO UPDATE SET active = TRUE
				RETURNING id
			`, clubID, rrow.Team).Scan(&teamID)
			if err != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}

			var exists bool
			err = tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM captains WHERE team_id = $1 AND email = $2
				)
			`, teamID, rrow.Email).Scan(&exists)
			if err != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}

			if exists {
				_, err = tx.Exec(ctx, `
					UPDATE captains
					SET full_name = $1, phone = NULLIF($2, ''), active_to = NULL
					WHERE team_id = $3 AND email = $4
				`, rrow.Name, rrow.Phone, teamID, rrow.Email)
				if err != nil {
					http.Error(w, "error", http.StatusInternalServerError)
					return
				}
				updated++
			} else {
				_, err = tx.Exec(ctx, `
					INSERT INTO captains (team_id, full_name, email, phone, active_from)
					VALUES ($1, $2, $3, NULLIF($4, ''), CURRENT_DATE)
				`, teamID, rrow.Name, rrow.Email, rrow.Phone)
				if err != nil {
					http.Error(w, "error", http.StatusInternalServerError)
					return
				}
				created++
			}
		}

		if err := tx.Commit(ctx); err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		// Clear preview cookie.
		http.SetCookie(w, &http.Cookie{
			Name:     csvPreviewCookie,
			Value:    "",
			Path:     "/admin",
			MaxAge:   -1,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "CSV Applied")
		writeAdminNav(w, csrfToken, r.URL.Path)
		fmt.Fprintf(w, `<div class="container-fluid">
<div class="alert alert-success">
  <strong>Applied captains CSV.</strong> Created: %d, Updated: %d
</div>
<a href="/admin/csv/captains" class="btn btn-outline-primary">Upload another</a>
<a href="/admin/dashboard" class="btn btn-outline-primary ms-2">Dashboard</a>
</div>
`, created, updated)
		pageFooter(w)

		// audit apply
		s.audit(ctx, r, "admin", nil, "csv_captains_apply", "csv_upload", nil, map[string]any{
			"created": created,
			"updated": updated,
			"mode":    mode,
		})
	}
}

// admin session helpers

func (s *Server) requireAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := getAdminSessionFromRequest(r)
			if err != nil {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func getAdminSessionFromRequest(r *http.Request) (*adminSession, error) {
	c, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return nil, err
	}
	secret := []byte(os.Getenv("ADMIN_SESSION_SECRET"))
	if len(secret) == 0 {
		return nil, fmt.Errorf("admin session secret not configured")
	}

	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, err
	}
	if len(raw) < sha256.Size {
		return nil, fmt.Errorf("token too short")
	}
	sig := raw[:sha256.Size]
	payload := raw[sha256.Size:]

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid signature")
	}

	var sess adminSession
	if err := json.Unmarshal(payload, &sess); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if now > sess.Expiry {
		return nil, fmt.Errorf("session expired")
	}
	if sess.IssuedAt > now+60 {
		return nil, fmt.Errorf("session issued in the future")
	}
	if sess.Aud != "adm" {
		return nil, fmt.Errorf("invalid session audience")
	}
	return &sess, nil
}

func setAdminSessionCookie(w http.ResponseWriter, sess *adminSession) error {
	secret := []byte(os.Getenv("ADMIN_SESSION_SECRET"))
	if len(secret) == 0 {
		return fmt.Errorf("ADMIN_SESSION_SECRET not configured")
	}

	payload, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	token := append(sig, payload...)

	val := base64.RawURLEncoding.EncodeToString(token)

	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    val,
		Path:     "/admin",
		Expires:  time.Unix(sess.Expiry, 0),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// preview token signing helpers (use same ADMIN_SESSION_SECRET).

func signPreviewToken(payload []byte) (string, error) {
	secret := []byte(os.Getenv("ADMIN_SESSION_SECRET"))
	if len(secret) == 0 {
		return "", fmt.Errorf("ADMIN_SESSION_SECRET not configured")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	token := append(sig, payload...)
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func verifyPreviewToken(token string) ([]byte, error) {
	secret := []byte(os.Getenv("ADMIN_SESSION_SECRET"))
	if len(secret) == 0 {
		return nil, fmt.Errorf("ADMIN_SESSION_SECRET not configured")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	if len(raw) < sha256.Size {
		return nil, fmt.Errorf("token too short")
	}
	sig := raw[:sha256.Size]
	payload := raw[sha256.Size:]

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid signature")
	}
	return payload, nil
}
