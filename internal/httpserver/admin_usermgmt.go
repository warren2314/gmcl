package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

// ── requirePasswordChanged middleware ─────────────────────────────────────────
// Redirects to /admin/change-password if force_password_change is set.
func (s *Server) requirePasswordChanged() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, err := getAdminSessionFromRequest(r)
			if err != nil {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()

			var force bool
			s.DB.QueryRow(ctx, `SELECT force_password_change FROM admin_users WHERE id=$1`, sess.AdminID).Scan(&force)
			if force {
				http.Redirect(w, r, "/admin/change-password", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── Change password ────────────────────────────────────────────────────────────

func (s *Server) handleAdminChangePasswordGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, _ := getAdminSessionFromRequest(r)
		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		var force bool
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if sess != nil {
			s.DB.QueryRow(ctx, `SELECT force_password_change FROM admin_users WHERE id=$1`, sess.AdminID).Scan(&force)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Change Password")
		writeCaptainNav(w) // plain nav — not full admin nav to avoid confusion during forced change

		banner := ""
		if force {
			banner = `<div class="alert alert-warning mb-4">
  <strong>Password change required.</strong> You must set a new password before continuing.
</div>`
		}

		fmt.Fprintf(w, `<div class="container" style="max-width:480px;margin-top:3rem">
<div class="card shadow-sm">
  <div class="card-header fw-semibold">Change Password</div>
  <div class="card-body">
    %s
    <form method="POST" action="/admin/change-password">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="mb-3">
        <label class="form-label">Current Password</label>
        <input type="password" name="current_password" class="form-control" required autocomplete="current-password">
      </div>
      <div class="mb-3">
        <label class="form-label">New Password</label>
        <input type="password" name="new_password" class="form-control" required autocomplete="new-password"
               minlength="12" id="newPwd">
        <div class="form-text">At least 12 characters, including a number and a symbol.</div>
      </div>
      <div class="mb-4">
        <label class="form-label">Confirm New Password</label>
        <input type="password" name="confirm_password" class="form-control" required autocomplete="new-password"
               id="confirmPwd">
      </div>
      <button type="submit" class="btn btn-primary w-100">Update Password</button>
    </form>
  </div>
</div>
</div>
`, banner, csrfToken)
		pageFooter(w)
	}
}

func (s *Server) handleAdminChangePasswordPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		sess, err := getAdminSessionFromRequest(r)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		current := r.FormValue("current_password")
		newPwd := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")

		if newPwd != confirm {
			http.Error(w, "passwords do not match", http.StatusBadRequest)
			return
		}
		if err := validatePasswordStrength(newPwd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		// Verify current password
		var storedHash []byte
		if err := s.DB.QueryRow(ctx, `SELECT password_hash FROM admin_users WHERE id=$1`, sess.AdminID).Scan(&storedHash); err != nil {
			http.Error(w, "user not found", http.StatusInternalServerError)
			return
		}
		if err := auth.CheckPassword(storedHash, current); err != nil {
			http.Error(w, "current password is incorrect", http.StatusUnauthorized)
			return
		}

		newHash, err := auth.HashPassword(newPwd)
		if err != nil {
			http.Error(w, "hash error", http.StatusInternalServerError)
			return
		}

		_, err = s.DB.Exec(ctx, `
			UPDATE admin_users
			SET password_hash = $1, force_password_change = FALSE
			WHERE id = $2
		`, newHash, sess.AdminID)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "admin", &sess.AdminID, "admin_password_changed", "admin_user",
			func() *int64 { v := int64(sess.AdminID); return &v }(),
			map[string]any{})

		http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
	}
}

// ── Admin user list ────────────────────────────────────────────────────────────

func (s *Server) handleAdminUsers() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		type userRow struct {
			ID            int32
			Username      string
			Email         string
			IsActive      bool
			ForceChange   bool
			LastLogin     *time.Time
			InvitedAt     *time.Time
		}
		var users []userRow
		rows, err := s.DB.Query(ctx, `
			SELECT id, username, email, is_active, force_password_change,
			       last_login_at, invited_at
			FROM admin_users
			ORDER BY invited_at DESC NULLS LAST, id
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var u userRow
				if rows.Scan(&u.ID, &u.Username, &u.Email, &u.IsActive,
					&u.ForceChange, &u.LastLogin, &u.InvitedAt) == nil {
					users = append(users, u)
				}
			}
		}

		sess, _ := getAdminSessionFromRequest(r)
		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Admin Users")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)
		fmt.Fprint(w, `
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Admin Users</h4>
    <p class="text-muted mb-0 small">Invite admins — they receive an email with a temporary password they must change on first login</p>
  </div>
</div>
`)

		// Invite form
		fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Invite New Admin</div>
  <div class="card-body">
    <form method="POST" action="/admin/users/invite" class="row g-3 align-items-end">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="col-md-4">
        <label class="form-label small fw-semibold">Full name / username</label>
        <input type="text" name="username" class="form-control form-control-sm"
               placeholder="e.g. jsmith" required>
      </div>
      <div class="col-md-5">
        <label class="form-label small fw-semibold">Email address</label>
        <input type="email" name="email" class="form-control form-control-sm"
               placeholder="e.g. jsmith@gmcl.co.uk" required>
      </div>
      <div class="col-auto">
        <button type="submit" class="btn btn-primary btn-sm">Send Invite</button>
      </div>
    </form>
  </div>
</div>
`, csrfToken)

		// User table
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Username</th><th>Email</th><th>Status</th>
        <th>Last Login</th><th>Invited</th><th></th>
      </tr></thead>
      <tbody>
`)
		for _, u := range users {
			statusBadge := `<span class="badge bg-success">Active</span>`
			if !u.IsActive {
				statusBadge = `<span class="badge bg-secondary">Inactive</span>`
			} else if u.ForceChange {
				statusBadge = `<span class="badge bg-warning text-dark">Awaiting login</span>`
			}

			lastLogin := `<span class="text-muted">Never</span>`
			if u.LastLogin != nil {
				lastLogin = u.LastLogin.Format("02 Jan 2006 15:04")
			}
			invitedAt := `<span class="text-muted">—</span>`
			if u.InvitedAt != nil {
				invitedAt = u.InvitedAt.Format("02 Jan 2006")
			}

			// Don't allow deactivating yourself
			actions := ""
			if sess != nil && u.ID != sess.AdminID && u.IsActive {
				actions = fmt.Sprintf(`
<form method="POST" action="/admin/users/%d/deactivate" class="d-inline">
  <input type="hidden" name="csrf_token" value="%s">
  <button type="submit" class="btn btn-sm btn-outline-danger py-0"
          onclick="return confirm('Deactivate %s?')">Deactivate</button>
</form>`, u.ID, csrfToken, escapeHTML(u.Username))
			}

			fmt.Fprintf(w,
				`<tr><td><strong>%s</strong></td><td>%s</td><td>%s</td><td class="small text-muted">%s</td><td class="small text-muted">%s</td><td>%s</td></tr>`,
				escapeHTML(u.Username), escapeHTML(u.Email),
				statusBadge, lastLogin, invitedAt, actions)
		}
		if len(users) == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No admin users found.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>`)
		pageFooter(w)
	}
}

// ── Invite ─────────────────────────────────────────────────────────────────────

func (s *Server) handleAdminUserInvite() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		username := strings.TrimSpace(r.FormValue("username"))
		emailAddr := strings.TrimSpace(r.FormValue("email"))
		if username == "" || emailAddr == "" {
			http.Error(w, "username and email required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		inviterSess, _ := getAdminSessionFromRequest(r)
		var inviterID *int32
		if inviterSess != nil {
			inviterID = &inviterSess.AdminID
		}

		// Generate a random temporary password (16 chars)
		tmpPassword := generateTempPassword()
		hash, err := auth.HashPassword(tmpPassword)
		if err != nil {
			http.Error(w, "hash error", http.StatusInternalServerError)
			return
		}

		var newAdminID int32
		err = s.DB.QueryRow(ctx, `
			INSERT INTO admin_users
			    (username, password_hash, email, is_active, force_password_change,
			     invited_by_admin_id, invited_at)
			VALUES ($1, $2, $3, TRUE, TRUE, $4, now())
			ON CONFLICT (username) DO NOTHING
			RETURNING id
		`, username, hash, emailAddr, inviterID).Scan(&newAdminID)
		if err != nil || newAdminID == 0 {
			http.Error(w, "username already exists or DB error", http.StatusConflict)
			return
		}

		// Send invite email
		emailClient := email.NewFromEnv()
		appURL := os.Getenv("APP_URL")
		if appURL == "" {
			appURL = "https://your-domain.com"
		}
		body := fmt.Sprintf(`You have been invited to the GMCL Admin Portal.

Login URL: %s/admin/login
Username:  %s
Temporary password: %s

You will be required to change your password immediately after logging in.

If you did not expect this email, please ignore it.

— GMCL`, appURL, username, tmpPassword)

		if err := emailClient.Send(emailAddr, "Your GMCL Admin Portal invitation", body); err != nil {
			// Don't fail the whole request — the admin was created; log the error
			fmt.Printf("invite email failed for %s: %v\n", emailAddr, err)
		}

		eid := int64(newAdminID)
		s.audit(ctx, r, "admin", inviterID, "admin_user_invited", "admin_user", &eid,
			map[string]any{"username": username, "email": emailAddr})

		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// ── Deactivate ─────────────────────────────────────────────────────────────────

func (s *Server) handleAdminUserDeactivate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		targetID, err := strconv.Atoi(idStr)
		if err != nil || targetID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		sess, err := getAdminSessionFromRequest(r)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if int32(targetID) == sess.AdminID {
			http.Error(w, "cannot deactivate yourself", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		_, err = s.DB.Exec(ctx, `UPDATE admin_users SET is_active=FALSE WHERE id=$1`, targetID)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		eid := int64(targetID)
		s.audit(ctx, r, "admin", &sess.AdminID, "admin_user_deactivated", "admin_user", &eid,
			map[string]any{})

		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// generateTempPassword creates a 16-character random password that satisfies
// the strength rules: upper, lower, digit, symbol.
func generateTempPassword() string {
	const letters = "abcdefghijkmnpqrstuvwxyz" // no l/o to avoid confusion
	const uppers = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	const digits = "23456789"
	const symbols = "!@#$%^&*"

	buf := make([]byte, 16)
	rand.Read(buf)

	pwd := make([]byte, 16)
	pwd[0] = uppers[int(buf[0])%len(uppers)]
	pwd[1] = digits[int(buf[1])%len(digits)]
	pwd[2] = symbols[int(buf[2])%len(symbols)]
	all := letters + uppers + digits + symbols
	for i := 3; i < 16; i++ {
		pwd[i] = all[int(buf[i])%len(all)]
	}
	// Shuffle using Fisher-Yates with random bytes
	shuffleBuf := make([]byte, 16)
	rand.Read(shuffleBuf)
	for i := 15; i > 0; i-- {
		j := int(shuffleBuf[i]) % (i + 1)
		pwd[i], pwd[j] = pwd[j], pwd[i]
	}
	return string(pwd)
}

// validatePasswordStrength enforces minimum password policy.
func validatePasswordStrength(pwd string) error {
	if len(pwd) < 12 {
		return fmt.Errorf("password must be at least 12 characters")
	}
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, c := range pwd {
		switch {
		case unicode.IsUpper(c):
			hasUpper = true
		case unicode.IsLower(c):
			hasLower = true
		case unicode.IsDigit(c):
			hasDigit = true
		case unicode.IsPunct(c) || unicode.IsSymbol(c):
			hasSymbol = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit || !hasSymbol {
		return fmt.Errorf("password must include uppercase, lowercase, a number, and a symbol")
	}
	return nil
}

// generateInviteTokenHash creates a secure random token and returns (plaintext, sha256hash).
func generateInviteTokenHash() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plain := hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plain))
	return plain, h[:], nil
}
