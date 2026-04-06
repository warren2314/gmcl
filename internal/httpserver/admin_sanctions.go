package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

// handleAdminSanctions lists all sanctions for the current season with filters.
func (s *Server) handleAdminSanctions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		var seasonID int32
		var seasonName string
		s.DB.QueryRow(ctx, `
			SELECT s.id, s.name FROM weeks w JOIN seasons s ON w.season_id=s.id
			WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date LIMIT 1
		`).Scan(&seasonID, &seasonName)

		if sid := r.URL.Query().Get("season_id"); sid != "" {
			if n, err := strconv.Atoi(sid); err == nil {
				seasonID = int32(n)
				s.DB.QueryRow(ctx, `SELECT name FROM seasons WHERE id=$1`, seasonID).Scan(&seasonName)
			}
		}

		type sanRow struct {
			ID        int64
			Week      int32
			Club      string
			Team      string
			Colour    string
			Reason    string
			Notes     string
			Status    string
			IssuedAt  time.Time
			IssuedBy  string
		}
		var sanctions []sanRow
		var yellowCount, redCount, activeCount int64

		srows, err := s.DB.Query(ctx, `
			SELECT sa.id, w.week_number, cl.name, t.name,
			       sa.colour, sa.reason, COALESCE(sa.notes,''),
			       sa.status, sa.issued_at,
			       COALESCE(au.username, 'system')
			FROM sanctions sa
			JOIN weeks w       ON sa.week_id  = w.id
			JOIN clubs cl      ON sa.club_id  = cl.id
			JOIN teams t       ON sa.team_id  = t.id
			LEFT JOIN admin_users au ON sa.issued_by_admin_id = au.id
			WHERE sa.season_id = $1
			ORDER BY sa.issued_at DESC
		`, seasonID)
		if err == nil {
			defer srows.Close()
			for srows.Next() {
				var sr sanRow
				if e := srows.Scan(&sr.ID, &sr.Week, &sr.Club, &sr.Team,
					&sr.Colour, &sr.Reason, &sr.Notes, &sr.Status,
					&sr.IssuedAt, &sr.IssuedBy); e == nil {
					sanctions = append(sanctions, sr)
					if sr.Colour == "yellow" {
						yellowCount++
					} else {
						redCount++
					}
					if sr.Status == "active" {
						activeCount++
					}
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)
		fmt.Fprintf(w, `
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Sanctions</h4>
    <p class="text-muted mb-0 small">%s</p>
  </div>
  <a href="/admin/compliance" class="btn btn-sm btn-outline-primary">View Compliance</a>
</div>
`, escapeHTML(seasonName))

		// KPI strip
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-auto">
    <div class="card card-kpi kpi-amber p-3 text-center" style="min-width:110px">
      <div class="kpi-number" style="color:#856404">%d</div>
      <div class="kpi-label">Yellow Cards</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi kpi-red p-3 text-center" style="min-width:110px">
      <div class="kpi-number text-danger">%d</div>
      <div class="kpi-label">Red Cards</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi kpi-purple p-3 text-center" style="min-width:110px">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Active</div>
    </div>
  </div>
</div>
`, yellowCount, redCount, activeCount)

		// Sanctions table
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Week</th><th>Club</th><th>Team</th><th>Card</th>
        <th>Reason</th><th>Notes</th><th>Status</th><th>Issued</th><th>By</th><th></th>
      </tr></thead>
      <tbody>
`)
		for _, sr := range sanctions {
			cardBadge := `<span class="badge badge-yellow-card">Yellow Card</span>`
			rowClass := "sanction-row-yellow"
			if sr.Colour == "red" {
				cardBadge = `<span class="badge badge-red-card">Red Card</span>`
				rowClass = "sanction-row-red"
			}

			statusBadge := ""
			switch sr.Status {
			case "active":
				statusBadge = `<span class="badge bg-danger">Active</span>`
			case "served":
				statusBadge = `<span class="badge bg-secondary">Served</span>`
			case "appealed":
				statusBadge = `<span class="badge bg-warning text-dark">Appealed</span>`
			case "overturned":
				statusBadge = `<span class="badge bg-success">Overturned</span>`
			}

			reasonLabel := sr.Reason
			switch sr.Reason {
			case "non_submission":
				reasonLabel = "Non-submission"
			case "late_submission":
				reasonLabel = "Late submission"
			case "manual":
				reasonLabel = "Manual"
			}

			actions := ""
			if sr.Status == "active" {
				actions = fmt.Sprintf(`
<form method="POST" action="/admin/sanctions/%d/resolve" class="d-inline">
  <input type="hidden" name="csrf_token" value="%s">
  <button type="submit" class="btn btn-sm btn-outline-success py-0"
          onclick="return confirm('Mark this sanction as resolved?')">Resolve</button>
</form>`, sr.ID, csrfToken)
			}

			notesDisplay := sr.Notes
			if len(notesDisplay) > 60 {
				notesDisplay = notesDisplay[:57] + "..."
			}

			fmt.Fprintf(w,
				`<tr class="%s"><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="text-muted small">%s</td><td>%s</td><td class="text-muted small">%s</td><td class="text-muted small">%s</td><td>%s</td></tr>`,
				rowClass, sr.Week,
				escapeHTML(sr.Club), escapeHTML(sr.Team),
				cardBadge, escapeHTML(reasonLabel),
				escapeHTML(notesDisplay), statusBadge,
				sr.IssuedAt.Format("02 Jan 15:04"),
				escapeHTML(sr.IssuedBy), actions)
		}

		if len(sanctions) == 0 {
			fmt.Fprint(w, `<tr><td colspan="10" class="text-center text-muted py-3">No sanctions issued this season.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>`)
		pageFooter(w)
	}
}

// handleAdminSanctionIssue issues a yellow (or red) card for a team/week.
// Two-yellows = automatic red escalation.
func (s *Server) handleAdminSanctionIssue() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		teamID, _ := strconv.Atoi(r.FormValue("team_id"))
		weekID, _ := strconv.Atoi(r.FormValue("week_id"))
		seasonID, _ := strconv.Atoi(r.FormValue("season_id"))
		reason := r.FormValue("reason")
		notes := r.FormValue("notes")

		if teamID == 0 || weekID == 0 || seasonID == 0 {
			http.Error(w, "missing required fields", http.StatusBadRequest)
			return
		}
		if reason == "" {
			reason = "non_submission"
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Look up club_id
		var clubID int32
		if err := s.DB.QueryRow(ctx, `SELECT club_id FROM teams WHERE id=$1`, teamID).Scan(&clubID); err != nil {
			http.Error(w, "team not found", http.StatusBadRequest)
			return
		}

		// Count existing active yellows for this team this season
		var yellowCount int64
		s.DB.QueryRow(ctx, `
			SELECT COUNT(*) FROM sanctions
			WHERE team_id=$1 AND season_id=$2 AND colour='yellow' AND status='active'
		`, teamID, seasonID).Scan(&yellowCount)

		// Resolve issuing admin
		adminID := s.resolveAdminID(r)

		// Determine card colour
		colour := "yellow"
		if yellowCount >= 1 {
			colour = "red"
		}

		// Idempotency: don't double-issue same colour for same week
		var existing int64
		s.DB.QueryRow(ctx, `
			SELECT COUNT(*) FROM sanctions
			WHERE team_id=$1 AND week_id=$2 AND season_id=$3 AND colour=$4 AND status IN ('active','served')
		`, teamID, weekID, seasonID, colour).Scan(&existing)
		if existing > 0 {
			redirect := r.FormValue("redirect")
			if redirect == "" {
				redirect = "/admin/compliance?week_id=" + strconv.Itoa(weekID)
			}
			http.Redirect(w, r, redirect, http.StatusSeeOther)
			return
		}

		// If escalating to red, mark existing yellows as served
		if colour == "red" {
			s.DB.Exec(ctx, `
				UPDATE sanctions SET status='served', resolved_at=now()
				WHERE team_id=$1 AND season_id=$2 AND colour='yellow' AND status='active'
			`, teamID, seasonID)
		}

		var sanctionID int64
		err := s.DB.QueryRow(ctx, `
			INSERT INTO sanctions (season_id, week_id, team_id, club_id, colour, reason, notes, issued_by_admin_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id
		`, seasonID, weekID, teamID, clubID, colour, reason, notes, adminID).Scan(&sanctionID)
		if err != nil {
			http.Error(w, "failed to issue sanction: "+err.Error(), http.StatusInternalServerError)
			return
		}

		eid := int64(sanctionID)
		s.audit(ctx, r, "admin", adminID, "sanction_issued", "sanction", &eid, map[string]any{
			"team_id": teamID,
			"week_id": weekID,
			"colour":  colour,
			"reason":  reason,
		})

		redirect := r.FormValue("redirect")
		if redirect == "" {
			redirect = "/admin/compliance?week_id=" + strconv.Itoa(weekID)
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	}
}

// handleAdminSanctionBulkIssue issues cards to all non-submitting teams for a week.
func (s *Server) handleAdminSanctionBulkIssue() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		weekID, _ := strconv.Atoi(r.FormValue("week_id"))
		seasonID, _ := strconv.Atoi(r.FormValue("season_id"))
		if weekID == 0 || seasonID == 0 {
			http.Error(w, "missing fields", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// Find teams that haven't submitted and don't have a non_submission sanction yet
		rows, err := s.DB.Query(ctx, `
			SELECT t.id
			FROM teams t
			WHERE t.active = TRUE
			  AND t.id NOT IN (SELECT team_id FROM submissions WHERE week_id=$1)
			  AND t.id NOT IN (
			      SELECT team_id FROM sanctions
			      WHERE week_id=$1 AND season_id=$2 AND reason='non_submission'
			      AND status IN ('active','served')
			  )
		`, weekID, seasonID)
		if err != nil {
			http.Error(w, "query error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var teamIDs []int32
		for rows.Next() {
			var id int32
			if rows.Scan(&id) == nil {
				teamIDs = append(teamIDs, id)
			}
		}
		rows.Close()

		adminID := s.resolveAdminID(r)

		for _, tid := range teamIDs {
			var clubID int32
			if s.DB.QueryRow(ctx, `SELECT club_id FROM teams WHERE id=$1`, tid).Scan(&clubID) != nil {
				continue
			}
			var yellowCount int64
			s.DB.QueryRow(ctx, `
				SELECT COUNT(*) FROM sanctions
				WHERE team_id=$1 AND season_id=$2 AND colour='yellow' AND status='active'
			`, tid, seasonID).Scan(&yellowCount)

			colour := "yellow"
			if yellowCount >= 1 {
				colour = "red"
				s.DB.Exec(ctx, `
					UPDATE sanctions SET status='served', resolved_at=now()
					WHERE team_id=$1 AND season_id=$2 AND colour='yellow' AND status='active'
				`, tid, seasonID)
			}

			var sid int64
			if err := s.DB.QueryRow(ctx, `
				INSERT INTO sanctions (season_id, week_id, team_id, club_id, colour, reason, issued_by_admin_id)
				VALUES ($1, $2, $3, $4, $5, 'non_submission', $6) RETURNING id
			`, seasonID, weekID, tid, clubID, colour, adminID).Scan(&sid); err == nil {
				eid := sid
				s.audit(ctx, r, "admin", adminID, "sanction_bulk_issued", "sanction", &eid, map[string]any{
					"team_id": tid, "week_id": weekID, "colour": colour,
				})
			}
		}

		http.Redirect(w, r, "/admin/compliance?week_id="+strconv.Itoa(weekID), http.StatusSeeOther)
	}
}

// handleAdminSanctionResolve marks a sanction as resolved/overturned.
func (s *Server) handleAdminSanctionResolve() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		sanctionID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || sanctionID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		adminID := s.resolveAdminID(r)

		_, err = s.DB.Exec(ctx, `
			UPDATE sanctions SET status='overturned', resolved_at=now(), resolved_by_admin_id=$1
			WHERE id=$2 AND status='active'
		`, adminID, sanctionID)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "admin", adminID, "sanction_resolved", "sanction", &sanctionID, map[string]any{})

		http.Redirect(w, r, "/admin/sanctions", http.StatusSeeOther)
	}
}

// resolveAdminID extracts the admin user ID from the signed session cookie.
func (s *Server) resolveAdminID(r *http.Request) *int32 {
	sess, err := getAdminSessionFromRequest(r)
	if err != nil {
		return nil
	}
	return &sess.AdminID
}
