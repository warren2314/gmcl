package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

// handleAdminCompliance shows which teams have and haven't submitted for a given week.
func (s *Server) handleAdminCompliance() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Resolve current season/week, allow override via query param
		var seasonID, weekID int32
		var seasonName string
		var weekNum int32

		s.DB.QueryRow(ctx, `
			SELECT s.id, s.name, w.id, w.week_number
			FROM weeks w JOIN seasons s ON w.season_id=s.id
			WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date
			ORDER BY w.start_date LIMIT 1
		`).Scan(&seasonID, &seasonName, &weekID, &weekNum)

		if wid := r.URL.Query().Get("week_id"); wid != "" {
			if n, err := strconv.Atoi(wid); err == nil {
				weekID = int32(n)
				s.DB.QueryRow(ctx, `
					SELECT s.id, s.name, w.week_number
					FROM weeks w JOIN seasons s ON w.season_id=s.id WHERE w.id=$1
				`, weekID).Scan(&seasonID, &seasonName, &weekNum)
			}
		}

		// All weeks for the week selector
		type weekOpt struct {
			ID     int32
			Num    int32
			Season string
		}
		var weekOpts []weekOpt
		worows, _ := s.DB.Query(ctx, `
			SELECT w.id, w.week_number, s.name
			FROM weeks w JOIN seasons s ON w.season_id=s.id
			ORDER BY s.start_date DESC, w.week_number DESC LIMIT 40
		`)
		if worows != nil {
			defer worows.Close()
			for worows.Next() {
				var wo weekOpt
				if worows.Scan(&wo.ID, &wo.Num, &wo.Season) == nil {
					weekOpts = append(weekOpts, wo)
				}
			}
		}

		// Compliance query: all active teams left-joined to submissions this week
		type compRow struct {
			TeamID        int32
			ClubName      string
			TeamName      string
			HasSubmitted  bool
			HasSanction   bool
			YellowCount   int64
			RedCount      int64
			SuggestedCard string
		}
		var rows []compRow
		var submitted, missing int

		if weekID > 0 {
			crows, err := s.DB.Query(ctx, `
				WITH submitted AS (
				    SELECT DISTINCT team_id FROM submissions WHERE week_id=$1
				),
				sanctioned AS (
				    SELECT DISTINCT team_id FROM sanctions
				    WHERE season_id=$2 AND week_id=$1 AND reason='non_submission' AND status IN ('active','served')
				),
				yellow_counts AS (
				    SELECT team_id, COUNT(*) AS cnt
				    FROM sanctions
				    WHERE season_id=$2 AND reason='non_submission' AND colour='yellow' AND status IN ('active','served')
				    GROUP BY team_id
				),
				red_counts AS (
				    SELECT team_id, COUNT(*) AS cnt
				    FROM sanctions
				    WHERE season_id=$2 AND reason='non_submission' AND colour='red' AND status IN ('active','served')
				    GROUP BY team_id
				)
				SELECT
				    t.id,
				    cl.name  AS club,
				    t.name   AS team,
				    (s.team_id IS NOT NULL)  AS has_submitted,
				    (sa.team_id IS NOT NULL) AS has_sanction,
				    COALESCE(yc.cnt, 0)      AS yellow_count,
				    COALESCE(rc.cnt, 0)      AS red_count
				FROM teams t
				JOIN clubs cl ON t.club_id = cl.id
				LEFT JOIN submitted    s  ON s.team_id  = t.id
				LEFT JOIN sanctioned   sa ON sa.team_id = t.id
				LEFT JOIN yellow_counts yc ON yc.team_id = t.id
				LEFT JOIN red_counts    rc ON rc.team_id = t.id
				WHERE t.active = TRUE
				ORDER BY has_submitted DESC, cl.name, t.name
			`, weekID, seasonID)
			if err == nil {
				defer crows.Close()
				for crows.Next() {
					var cr compRow
					if e := crows.Scan(&cr.TeamID, &cr.ClubName, &cr.TeamName,
						&cr.HasSubmitted, &cr.HasSanction, &cr.YellowCount, &cr.RedCount); e == nil {
					if cr.YellowCount >= 3 {
						cr.SuggestedCard = "red"
					} else {
						cr.SuggestedCard = "yellow"
					}
						rows = append(rows, cr)
						if cr.HasSubmitted {
							submitted++
						} else {
							missing++
						}
					}
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Compliance")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)

		// Header
		fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Compliance</h4>
    <p class="text-muted mb-0 small">Who has and hasn't submitted for a given week</p>
  </div>
  <form method="GET" action="/admin/compliance" class="d-flex gap-2 align-items-center">
    <select name="week_id" class="form-select form-select-sm" onchange="this.form.submit()">
`)
		for _, wo := range weekOpts {
			sel := ""
			if wo.ID == weekID {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s — Week %d</option>`,
				wo.ID, sel, escapeHTML(wo.Season), wo.Num)
		}
		fmt.Fprint(w, `    </select>
  </form>
</div>
`)

		if weekID == 0 {
			fmt.Fprint(w, `<div class="alert alert-warning">No active week found.</div></div>`)
			pageFooter(w)
			return
		}

		// Summary badges
		total := submitted + missing
		compliancePct := 0.0
		if total > 0 {
			compliancePct = float64(submitted) / float64(total) * 100
		}
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-auto">
    <div class="card card-kpi kpi-blue p-3 text-center" style="min-width:110px">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Total Teams</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi kpi-green p-3 text-center" style="min-width:110px">
      <div class="kpi-number text-success">%d</div>
      <div class="kpi-label">Submitted</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi kpi-red p-3 text-center" style="min-width:110px">
      <div class="kpi-number text-danger">%d</div>
      <div class="kpi-label">Missing</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi kpi-teal p-3 text-center" style="min-width:110px">
      <div class="kpi-number">%.0f%%</div>
      <div class="kpi-label">Compliance</div>
    </div>
  </div>
</div>
`, total, submitted, missing, compliancePct)

		// Bulk issue button
		if missing > 0 {
			fmt.Fprintf(w, `
<form method="POST" action="/admin/sanctions/bulk-issue" class="mb-3">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="week_id" value="%d">
  <input type="hidden" name="season_id" value="%d">
  <button type="submit" class="btn btn-warning btn-sm"
          onclick="return confirm('Issue yellow cards to all %d teams without a submission?')">
    Issue Yellow Cards to All %d Missing Teams
  </button>
</form>
`, csrfToken, weekID, seasonID, missing, missing)
		}

		// Compliance table
		fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">%s &mdash; Week %d</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Club</th><th>Team</th><th>Status</th><th>Prior offences</th><th>Suggested card</th><th>Sanction</th><th></th>
      </tr></thead>
      <tbody>
`, escapeHTML(seasonName), weekNum)

		for _, cr := range rows {
			rowClass := "compliance-submitted"
			statusBadge := `<span class="badge bg-success">&#10003; Submitted</span>`
			actionCell := ""

			if !cr.HasSubmitted {
				rowClass = "compliance-missing"
				statusBadge = `<span class="badge bg-danger">&#10007; Missing</span>`
				if cr.HasSanction {
					actionCell = `<span class="badge badge-yellow-card">Card Issued</span>`
				} else {
					actionCell = fmt.Sprintf(`
<form method="POST" action="/admin/sanctions/issue" class="d-inline">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="team_id" value="%d">
  <input type="hidden" name="week_id" value="%d">
  <input type="hidden" name="season_id" value="%d">
  <input type="hidden" name="reason" value="non_submission">
  <button type="submit" class="btn btn-warning btn-sm py-0">Issue Card</button>
</form>`, csrfToken, cr.TeamID, weekID, seasonID)
				}
			}

			priorCell := fmt.Sprintf(`<span class="text-muted">🟡 %d / 🔴 %d</span>`, cr.YellowCount, cr.RedCount)
			suggestedCell := `<span class="badge badge-yellow-card">🟡 Yellow</span>`
			if cr.SuggestedCard == "red" {
				suggestedCell = `<span class="badge badge-red-card">🔴 Red</span>`
			}
			sanctionCell := `<span class="text-muted">—</span>`
			if cr.HasSanction {
				sanctionCell = `<span class="badge badge-yellow-card">Active</span>`
			}
			if cr.HasSubmitted {
				priorCell = `<span class="text-muted">—</span>`
				suggestedCell = `<span class="text-muted">—</span>`
			}

			fmt.Fprintf(w,
				`<tr class="%s"><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				rowClass, escapeHTML(cr.ClubName), escapeHTML(cr.TeamName),
				statusBadge, priorCell, suggestedCell, sanctionCell, actionCell)
		}

		if len(rows) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-3">No active teams found.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>`)
		pageFooter(w)
	}
}
