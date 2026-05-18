package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
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

		pendingID := strings.TrimSpace(r.URL.Query().Get("pending"))
		codeSent := r.URL.Query().Get("sent") == "1"
		banner := ""
		if force {
			banner = `<div class="alert alert-warning mb-4">
  <strong>Password change required.</strong> You must set a new password before continuing.
</div>`
		}
		if codeSent {
			banner += `<div class="alert alert-info mb-4">
  <strong>Verification code sent.</strong> Check your admin email and enter the code below to finish changing your password.
</div>`
		}

		if pendingID != "" {
			fmt.Fprintf(w, `<div class="container" style="max-width:480px;margin-top:3rem">
<div class="card shadow-sm">
  <div class="card-header fw-semibold">Verify Password Change</div>
  <div class="card-body">
    %s
    <form method="POST" action="/admin/change-password">
      <input type="hidden" name="csrf_token" value="%s">
      <input type="hidden" name="pending_id" value="%s">
      <div class="mb-4">
        <label class="form-label">One-time code</label>
        <input type="text" name="otp_code" class="form-control form-control-lg text-center" required inputmode="numeric" autocomplete="one-time-code" maxlength="10" autofocus>
      </div>
      <button type="submit" class="btn btn-primary w-100">Confirm Password Change</button>
      <a href="/admin/change-password" class="btn btn-link w-100 mt-2">Start again</a>
    </form>
  </div>
</div>
</div>
`, banner, csrfToken, escapeHTML(pendingID))
			pageFooter(w)
			return
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
      <button type="submit" class="btn btn-primary w-100">Email Verification Code</button>
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

		pendingID := strings.TrimSpace(r.FormValue("pending_id"))
		otpCode := strings.TrimSpace(r.FormValue("otp_code"))
		if pendingID != "" || otpCode != "" {
			s.confirmAdminPasswordChangeOTP(w, r, sess, pendingID, otpCode)
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

		code := generateAdminOTPCode(6)
		codeHash := sha256.Sum256([]byte(code))
		expiresAt := time.Now().Add(10 * time.Minute)

		tx, err := s.DB.Begin(ctx)
		if err != nil {
			http.Error(w, "otp setup failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)

		_, _ = tx.Exec(ctx, `UPDATE admin_password_change_otps SET used_at = now() WHERE admin_user_id=$1 AND used_at IS NULL`, sess.AdminID)

		var requestID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO admin_password_change_otps (admin_user_id, code_hash, new_password_hash, expires_at)
			VALUES ($1, $2, $3, $4)
			RETURNING id
		`, sess.AdminID, codeHash[:], newHash, expiresAt).Scan(&requestID)
		if err != nil {
			http.Error(w, "otp setup failed", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			http.Error(w, "otp setup failed", http.StatusInternalServerError)
			return
		}

		var emailAddr string
		_ = s.DB.QueryRow(ctx, `SELECT email FROM admin_users WHERE id=$1`, sess.AdminID).Scan(&emailAddr)
		body := fmt.Sprintf(`A password change was requested for your GMCL Admin account.

Your one-time verification code is:

CODE: %s

This code expires in 10 minutes. If you did not request this change, contact the league office immediately.`, code)
		if err := email.NewFromEnv().Send(emailAddr, "GMCL Admin password change code", body); err != nil {
			http.Error(w, "could not send verification code", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "admin", &sess.AdminID, "admin_password_change_otp_sent", "admin_user",
			func() *int64 { v := int64(sess.AdminID); return &v }(),
			map[string]any{})

		http.Redirect(w, r, fmt.Sprintf("/admin/change-password?pending=%d&sent=1", requestID), http.StatusSeeOther)
	}
}

func (s *Server) confirmAdminPasswordChangeOTP(w http.ResponseWriter, r *http.Request, sess *adminSession, pendingID, code string) {
	id, err := strconv.ParseInt(pendingID, 10, 64)
	if err != nil || id <= 0 || code == "" {
		http.Error(w, "invalid verification code", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	codeHash := sha256.Sum256([]byte(code))
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		http.Error(w, "verification failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	var storedHash, newHash []byte
	var expiresAt time.Time
	var usedAt sql.NullTime
	err = tx.QueryRow(ctx, `
		SELECT code_hash, new_password_hash, expires_at, used_at
		FROM admin_password_change_otps
		WHERE id=$1 AND admin_user_id=$2
		FOR UPDATE
	`, id, sess.AdminID).Scan(&storedHash, &newHash, &expiresAt, &usedAt)
	if err != nil {
		http.Error(w, "verification failed", http.StatusBadRequest)
		return
	}
	if usedAt.Valid || time.Now().After(expiresAt) || subtle.ConstantTimeCompare(storedHash, codeHash[:]) != 1 {
		http.Error(w, "invalid or expired verification code", http.StatusUnauthorized)
		return
	}

	_, err = tx.Exec(ctx, `
		UPDATE admin_users
		SET password_hash=$1, force_password_change=FALSE
		WHERE id=$2
	`, newHash, sess.AdminID)
	if err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_password_change_otps SET used_at=now() WHERE id=$1`, id); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}

	s.audit(ctx, r, "admin", &sess.AdminID, "admin_password_changed", "admin_user",
		func() *int64 { v := int64(sess.AdminID); return &v }(),
		map[string]any{"otp": true})

	http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
}

// ── Admin user list ────────────────────────────────────────────────────────────

func (s *Server) handleAdminUsers() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		type userRow struct {
			ID          int32
			Username    string
			Email       string
			Role        string
			UmpireACL   bool
			IsActive    bool
			ForceChange bool
			LastLogin   *time.Time
			InvitedAt   *time.Time
		}
		var users []userRow
		rows, err := s.DB.Query(ctx, `
			SELECT id, username, email, COALESCE(role, 'admin'), is_active, force_password_change,
			       last_login_at, invited_at,
			       EXISTS (
			           SELECT 1 FROM admin_user_permissions aup
			           WHERE aup.admin_user_id = admin_users.id
			             AND aup.permission = 'view_umpire_feedback'
			       )
			FROM admin_users
			ORDER BY invited_at DESC NULLS LAST, id
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var u userRow
				if rows.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.IsActive,
					&u.ForceChange, &u.LastLogin, &u.InvitedAt, &u.UmpireACL) == nil {
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
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

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
      <div class="col-md-4">
        <label class="form-label small fw-semibold">Email address</label>
        <input type="email" name="email" class="form-control form-control-sm"
               placeholder="e.g. jsmith@gmcl.co.uk" required>
      </div>
      <div class="col-md-2">
        <label class="form-label small fw-semibold">Role</label>
        <select name="role" class="form-select form-select-sm">
          <option value="admin">Admin</option>
          <option value="super_admin">Super admin</option>
        </select>
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
        <th>Username</th><th>Email</th><th>Role</th><th>Umpire Feedback</th><th>Status</th>
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
			roleLabel := "Admin"
			roleClass := "text-bg-secondary"
			if u.Role == "super_admin" {
				roleLabel = "Super admin"
				roleClass = "text-bg-dark"
			}
			roleCell := fmt.Sprintf(`<span class="badge %s">%s</span>`, roleClass, roleLabel)
			umpireACLCell := `<span class="badge text-bg-secondary">No access</span>`
			if u.Role == "super_admin" {
				umpireACLCell = `<span class="badge text-bg-dark">Allowed by role</span>`
			} else if u.UmpireACL {
				umpireACLCell = `<span class="badge text-bg-success">Allowed</span>`
			}
			invitedAt := `<span class="text-muted">—</span>`
			if u.InvitedAt != nil {
				invitedAt = u.InvitedAt.Format("02 Jan 2006")
			}

			actions := ""
			if sess == nil || u.ID == sess.AdminID {
				actions = `<span class="text-muted small">—</span>`
			} else {
				if u.IsActive {
					actions += fmt.Sprintf(`
<form method="POST" action="/admin/users/%d/role" class="d-inline me-1">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="role" value="%s">
  <button type="submit" class="btn btn-sm btn-outline-dark py-0">Make %s</button>
</form>`, u.ID, csrfToken, oppositeAdminRole(u.Role), escapeHTML(oppositeAdminRoleLabel(u.Role)))
					if u.Role != "super_admin" {
						nextPermission := "1"
						permissionLabel := "Grant umpire feedback"
						if u.UmpireACL {
							nextPermission = "0"
							permissionLabel = "Revoke umpire feedback"
						}
						actions += fmt.Sprintf(`
<form method="POST" action="/admin/users/%d/permissions" class="d-inline me-1">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="view_umpire_feedback" value="%s">
  <button type="submit" class="btn btn-sm btn-outline-primary py-0">%s</button>
</form>`, u.ID, csrfToken, nextPermission, permissionLabel)
					}
					actions += fmt.Sprintf(`
<form method="POST" action="/admin/users/%d/deactivate" class="d-inline me-1">
  <input type="hidden" name="csrf_token" value="%s">
  <button type="submit" class="btn btn-sm btn-outline-secondary py-0"
          onclick="return confirm('Deactivate %s?')">Deactivate</button>
</form>`, u.ID, csrfToken, escapeHTML(u.Username))
				}
				actions += fmt.Sprintf(`
<form method="POST" action="/admin/users/%d/resend-invite" class="d-inline me-1">
  <input type="hidden" name="csrf_token" value="%s">
  <button type="submit" class="btn btn-sm btn-outline-primary py-0"
          onclick="return confirm('Resend invite email to %s? A new temporary password will be generated.')">Resend Invite</button>
</form>`, u.ID, csrfToken, escapeHTML(u.Username))
				actions += fmt.Sprintf(`
<form method="POST" action="/admin/users/%d/delete" class="d-inline">
  <input type="hidden" name="csrf_token" value="%s">
  <button type="submit" class="btn btn-sm btn-outline-danger py-0"
          onclick="return confirm('Permanently delete %s? This cannot be undone.')">Delete</button>
</form>`, u.ID, csrfToken, escapeHTML(u.Username))
			}

			fmt.Fprintf(w,
				`<tr><td><strong>%s</strong></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="small text-muted">%s</td><td class="small text-muted">%s</td><td>%s</td></tr>`,
				escapeHTML(u.Username), escapeHTML(u.Email),
				roleCell, umpireACLCell, statusBadge, lastLogin, invitedAt, actions)
		}
		if len(users) == 0 {
			fmt.Fprint(w, `<tr><td colspan="8" class="text-center text-muted py-3">No admin users found.</td></tr>`)
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
		role := normaliseAdminRole(r.FormValue("role"))
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
			     invited_by_admin_id, invited_at, role)
			VALUES ($1, $2, $3, TRUE, TRUE, $4, now(), $5)
			ON CONFLICT (username) DO NOTHING
			RETURNING id
		`, username, hash, emailAddr, inviterID, role).Scan(&newAdminID)
		if err != nil || newAdminID == 0 {
			http.Error(w, "username already exists or DB error", http.StatusConflict)
			return
		}

		// Send invite email
		emailClient := email.NewFromEnv()
		appURL := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
		if appURL == "" {
			appURL = strings.TrimSpace(os.Getenv("APP_URL"))
		}
		if appURL == "" {
			appURL = "https://gmcl.co.uk"
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
			map[string]any{"username": username, "email": emailAddr, "role": role})

		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// ── Deactivate ─────────────────────────────────────────────────────────────────

func (s *Server) handleAdminUserRoleUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetID, err := strconv.Atoi(chi.URLParam(r, "id"))
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
			http.Error(w, "cannot change your own role", http.StatusBadRequest)
			return
		}
		role := normaliseAdminRole(r.FormValue("role"))

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, err := s.DB.Exec(ctx, `UPDATE admin_users SET role=$1 WHERE id=$2`, role, targetID); err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		eid := int64(targetID)
		s.audit(ctx, r, "admin", &sess.AdminID, "admin_user_role_updated", "admin_user", &eid, map[string]any{"role": role})
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminUserPermissionsUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetID, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil || targetID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		sess, err := getAdminSessionFromRequest(r)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		allowUmpireFeedback := r.FormValue("view_umpire_feedback") == "1"

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var role string
		if err := s.DB.QueryRow(ctx, `SELECT COALESCE(role, 'admin') FROM admin_users WHERE id=$1`, targetID).Scan(&role); err != nil {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		if role == "super_admin" {
			http.Error(w, "super admins already have this permission", http.StatusBadRequest)
			return
		}

		if allowUmpireFeedback {
			_, err = s.DB.Exec(ctx, `
				INSERT INTO admin_user_permissions (admin_user_id, permission)
				VALUES ($1, 'view_umpire_feedback')
				ON CONFLICT DO NOTHING
			`, targetID)
		} else {
			_, err = s.DB.Exec(ctx, `
				DELETE FROM admin_user_permissions
				WHERE admin_user_id=$1 AND permission='view_umpire_feedback'
			`, targetID)
		}
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		eid := int64(targetID)
		s.audit(ctx, r, "admin", &sess.AdminID, "admin_user_permissions_updated", "admin_user", &eid,
			map[string]any{"view_umpire_feedback": allowUmpireFeedback})
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

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

// ── Resend invite ──────────────────────────────────────────────────────────────

func (s *Server) handleAdminUserResendInvite() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetID, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil || targetID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		var username, emailAddr string
		if err := s.DB.QueryRow(ctx, `SELECT username, email FROM admin_users WHERE id=$1`, targetID).
			Scan(&username, &emailAddr); err != nil {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}

		tmpPassword := generateTempPassword()
		hash, err := auth.HashPassword(tmpPassword)
		if err != nil {
			http.Error(w, "hash error", http.StatusInternalServerError)
			return
		}

		if _, err := s.DB.Exec(ctx, `
			UPDATE admin_users SET password_hash=$1, force_password_change=TRUE, is_active=TRUE WHERE id=$2
		`, hash, targetID); err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		appURL := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
		if appURL == "" {
			appURL = strings.TrimSpace(os.Getenv("APP_URL"))
		}
		if appURL == "" {
			appURL = "https://gmcl.co.uk"
		}
		body := fmt.Sprintf(`You have been invited to the GMCL Admin Portal.

Login URL: %s/admin/login
Username:  %s
Temporary password: %s

You will be required to change your password immediately after logging in.

If you did not expect this email, please ignore it.

— GMCL`, appURL, username, tmpPassword)

		emailClient := email.NewFromEnv()
		if err := emailClient.Send(emailAddr, "Your GMCL Admin Portal invitation", body); err != nil {
			http.Error(w, "email send failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		inviterSess, _ := getAdminSessionFromRequest(r)
		var inviterID *int32
		if inviterSess != nil {
			inviterID = &inviterSess.AdminID
		}
		eid := int64(targetID)
		s.audit(ctx, r, "admin", inviterID, "admin_user_invite_resent", "admin_user", &eid,
			map[string]any{"username": username, "email": emailAddr})

		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// ── Delete ─────────────────────────────────────────────────────────────────────

func (s *Server) handleAdminUserDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		targetID, err := strconv.Atoi(chi.URLParam(r, "id"))
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
			http.Error(w, "cannot delete yourself", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var username string
		s.DB.QueryRow(ctx, `SELECT username FROM admin_users WHERE id=$1`, targetID).Scan(&username)

		if _, err := s.DB.Exec(ctx, `DELETE FROM admin_users WHERE id=$1`, targetID); err != nil {
			// FK constraint — fall back to deactivation
			s.DB.Exec(ctx, `UPDATE admin_users SET is_active=FALSE WHERE id=$1`, targetID)
		}

		eid := int64(targetID)
		s.audit(ctx, r, "admin", &sess.AdminID, "admin_user_deleted", "admin_user", &eid,
			map[string]any{"username": username})

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

func generateAdminOTPCode(length int) string {
	if length <= 0 {
		length = 6
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(i)
		}
	}
	for i := range buf {
		buf[i] = '0' + (buf[i] % 10)
	}
	return string(buf)
}

func normaliseAdminRole(role string) string {
	if strings.TrimSpace(role) == "super_admin" {
		return "super_admin"
	}
	return "admin"
}

func oppositeAdminRole(role string) string {
	if role == "super_admin" {
		return "admin"
	}
	return "super_admin"
}

func oppositeAdminRoleLabel(role string) string {
	if role == "super_admin" {
		return "admin"
	}
	return "super admin"
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

// ── Forgot / reset password ────────────────────────────────────────────────────

func (s *Server) handleAdminForgotPasswordGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Forgot Password")
		writeCaptainNav(w)
		fmt.Fprintf(w, `<div class="container" style="max-width:480px;margin-top:3rem">
<div class="card shadow-sm">
  <div class="card-header fw-semibold">Reset Password</div>
  <div class="card-body">
    <p class="text-muted small">Enter your admin email address and we'll send you a reset link.</p>
    <form method="POST" action="/admin/forgot-password">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="mb-3">
        <label class="form-label">Email address</label>
        <input type="email" name="email" class="form-control" required autocomplete="email">
      </div>
      <button type="submit" class="btn btn-primary w-100">Send reset link</button>
    </form>
    <div class="mt-3 text-center small">
      <a href="/admin/login">&larr; Back to login</a>
    </div>
  </div>
</div>
</div>`, csrfToken)
		pageFooter(w)
	}
}

func (s *Server) handleAdminForgotPasswordPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		emailAddr := strings.TrimSpace(r.FormValue("email"))

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Look up admin by email — do this silently so we don't reveal whether the address exists.
		var adminID int32
		var username string
		_ = s.DB.QueryRow(ctx, `
			SELECT id, username FROM admin_users
			WHERE LOWER(email) = LOWER($1) AND is_active = TRUE
		`, emailAddr).Scan(&adminID, &username)

		if adminID > 0 {
			plain, hash, err := generateInviteTokenHash()
			if err == nil {
				expires := time.Now().Add(1 * time.Hour)
				_, err = s.DB.Exec(ctx, `
					INSERT INTO admin_invite_tokens (admin_user_id, token_hash, expires_at)
					VALUES ($1, $2, $3)
				`, adminID, hash, expires)
				if err == nil {
					appURL := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
					if appURL == "" {
						appURL = "https://gmcl.co.uk"
					}
					resetLink := appURL + "/admin/reset-password?token=" + plain
					body := fmt.Sprintf(`Hi %s,

A password reset was requested for your GMCL Admin account.

Click the link below to set a new password (valid for 1 hour):
%s

If you did not request this, you can safely ignore this email.

— GMCL`, username, resetLink)
					emailClient := email.NewFromEnv()
					_ = emailClient.Send(emailAddr, "GMCL Admin — password reset", body)
					s.audit(ctx, r, "public", nil, "admin_password_reset_requested", "admin_user",
						func() *int64 { v := int64(adminID); return &v }(), map[string]any{})
				}
			}
		}

		// Always show the same page regardless of whether the email matched.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Forgot Password")
		writeCaptainNav(w)
		fmt.Fprint(w, `<div class="container" style="max-width:480px;margin-top:3rem">
<div class="card shadow-sm">
  <div class="card-header fw-semibold">Reset Password</div>
  <div class="card-body">
    <div class="alert alert-success">If that email is registered, a reset link has been sent. Check your inbox.</div>
    <div class="text-center small"><a href="/admin/login">&larr; Back to login</a></div>
  </div>
</div>
</div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminResetPasswordGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.URL.Query().Get("token"))
		csrfToken := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Reset Password")
		writeCaptainNav(w)

		if token == "" {
			fmt.Fprint(w, `<div class="container" style="max-width:480px;margin-top:3rem"><div class="alert alert-danger">Invalid or missing reset token.</div></div>`)
			pageFooter(w)
			return
		}

		fmt.Fprintf(w, `<div class="container" style="max-width:480px;margin-top:3rem">
<div class="card shadow-sm">
  <div class="card-header fw-semibold">Set New Password</div>
  <div class="card-body">
    <form method="POST" action="/admin/reset-password">
      <input type="hidden" name="csrf_token" value="%s">
      <input type="hidden" name="token" value="%s">
      <div class="mb-3">
        <label class="form-label">New Password</label>
        <input type="password" name="new_password" class="form-control" required
               minlength="12" autocomplete="new-password">
        <div class="form-text">At least 12 characters, including uppercase, a number and a symbol.</div>
      </div>
      <div class="mb-4">
        <label class="form-label">Confirm New Password</label>
        <input type="password" name="confirm_password" class="form-control" required
               autocomplete="new-password">
      </div>
      <button type="submit" class="btn btn-primary w-100">Set Password</button>
    </form>
  </div>
</div>
</div>`, csrfToken, escapeHTML(token))
		pageFooter(w)
	}
}

func (s *Server) handleAdminResetPasswordPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		token := strings.TrimSpace(r.FormValue("token"))
		newPwd := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")

		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		if newPwd != confirm {
			http.Error(w, "passwords do not match", http.StatusBadRequest)
			return
		}
		if err := validatePasswordStrength(newPwd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		h := sha256.Sum256([]byte(token))

		var tokenID int64
		var adminID int32
		var expiresAt time.Time
		var usedAt *time.Time
		err := s.DB.QueryRow(ctx, `
			SELECT id, admin_user_id, expires_at, used_at
			FROM admin_invite_tokens
			WHERE token_hash = $1
		`, h[:]).Scan(&tokenID, &adminID, &expiresAt, &usedAt)
		if err != nil || usedAt != nil || time.Now().After(expiresAt) {
			http.Error(w, "Reset link is invalid or has expired.", http.StatusBadRequest)
			return
		}

		newHash, err := auth.HashPassword(newPwd)
		if err != nil {
			http.Error(w, "hash error", http.StatusInternalServerError)
			return
		}

		// Mark token used and update password in one go.
		_, err = s.DB.Exec(ctx, `
			UPDATE admin_invite_tokens SET used_at = now() WHERE id = $1
		`, tokenID)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		_, err = s.DB.Exec(ctx, `
			UPDATE admin_users
			SET password_hash = $1, force_password_change = FALSE
			WHERE id = $2
		`, newHash, adminID)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		eid := int64(adminID)
		s.audit(ctx, r, "public", nil, "admin_password_reset", "admin_user", &eid, map[string]any{})

		http.Redirect(w, r, "/admin/login?reset=1", http.StatusSeeOther)
	}
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
