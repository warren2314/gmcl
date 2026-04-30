package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleAdminFixtures() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Default to next week; allow override via week_id query param
		var weekID int32
		var weekStart, weekEnd time.Time
		var weekNum int32
		var seasonName string

		if wid := r.URL.Query().Get("week_id"); wid != "" {
			if n, err := strconv.Atoi(wid); err == nil {
				weekID = int32(n)
				s.DB.QueryRow(ctx, `
					SELECT w.start_date, w.end_date, w.week_number, s.name
					FROM weeks w JOIN seasons s ON s.id = w.season_id
					WHERE w.id = $1
				`, weekID).Scan(&weekStart, &weekEnd, &weekNum, &seasonName)
			}
		}

		// Default: next week relative to today
		if weekID == 0 {
			s.DB.QueryRow(ctx, `
				SELECT w.id, w.start_date, w.end_date, w.week_number, s.name
				FROM weeks w JOIN seasons s ON s.id = w.season_id
				WHERE s.is_archived = FALSE AND w.start_date > CURRENT_DATE
				ORDER BY w.start_date LIMIT 1
			`).Scan(&weekID, &weekStart, &weekEnd, &weekNum, &seasonName)
		}

		// All weeks for selector
		type weekOpt struct {
			ID    int32
			Num   int32
			Start time.Time
			End   time.Time
		}
		var weekOpts []weekOpt
		worows, _ := s.DB.Query(ctx, `
			SELECT w.id, w.week_number, w.start_date, w.end_date
			FROM weeks w JOIN seasons s ON s.id = w.season_id
			WHERE s.is_archived = FALSE
			ORDER BY w.start_date ASC
		`)
		if worows != nil {
			defer worows.Close()
			for worows.Next() {
				var wo weekOpt
				if worows.Scan(&wo.ID, &wo.Num, &wo.Start, &wo.End) == nil {
					weekOpts = append(weekOpts, wo)
				}
			}
		}

		// Fixtures for the selected week, grouped by date then competition
		type fixtureRow struct {
			MatchDate         time.Time
			Competition       string
			HomeClub          string
			HomeTeam          string
			AwayClub          string
			AwayTeam          string
			Ground            string
			Umpire1           string
			Umpire2           string
			LinkedHomeTeam    string
			LinkedAwayTeam    string
		}
		var fixtures []fixtureRow

		if weekID > 0 {
			frows, err := s.DB.Query(ctx, `
				SELECT
				    lf.match_date,
				    lf.competition_id,
				    COALESCE(lf.home_club_name, ''),
				    COALESCE(lf.home_team_name, ''),
				    COALESCE(lf.away_club_name, ''),
				    COALESCE(lf.away_team_name, ''),
				    COALESCE(lf.ground_name, ''),
				    COALESCE(lf.umpire_1_name, ''),
				    COALESCE(lf.umpire_2_name, ''),
				    COALESCE(ht.name, ''),
				    COALESCE(at.name, '')
				FROM league_fixtures lf
				LEFT JOIN teams ht ON ht.play_cricket_team_id = lf.home_team_pc_id
				LEFT JOIN teams at ON at.play_cricket_team_id = lf.away_team_pc_id
				WHERE lf.match_date BETWEEN $1 AND $2
				ORDER BY lf.match_date, lf.competition_id, lf.home_club_name
			`, weekStart, weekEnd)
			if err == nil {
				defer frows.Close()
				for frows.Next() {
					var f fixtureRow
					if frows.Scan(&f.MatchDate, &f.Competition,
						&f.HomeClub, &f.HomeTeam,
						&f.AwayClub, &f.AwayTeam,
						&f.Ground, &f.Umpire1, &f.Umpire2,
						&f.LinkedHomeTeam, &f.LinkedAwayTeam) == nil {
						fixtures = append(fixtures, f)
					}
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie("csrf_token"); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Fixtures")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)
		fmt.Fprintf(w, `
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Fixtures</h4>
    <p class="text-muted mb-0 small">Synced from Play-Cricket</p>
  </div>
  <form method="GET" action="/admin/fixtures" class="d-flex gap-2 align-items-center">
    <select name="week_id" class="form-select form-select-sm" onchange="this.form.submit()">`)

		for _, wo := range weekOpts {
			sel := ""
			if wo.ID == weekID {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>Week %d &mdash; %s to %s</option>`,
				wo.ID, sel, wo.Num, wo.Start.Format("2 Jan"), wo.End.Format("2 Jan 2006"))
		}
		fmt.Fprint(w, `    </select>
  </form>
</div>`)

		if weekID == 0 || len(fixtures) == 0 {
			fmt.Fprint(w, `<div class="alert alert-info">No fixtures found for this week.</div>`)
			fmt.Fprint(w, `</div>`)
			pageFooter(w)
			return
		}

		// Group by date
		currentDate := time.Time{}
		for _, f := range fixtures {
			if !f.MatchDate.Equal(currentDate) {
				if !currentDate.IsZero() {
					fmt.Fprint(w, `</tbody></table></div></div>`)
				}
				currentDate = f.MatchDate
				fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold bg-gmcl text-white">%s</div>
  <div class="table-responsive">
    <table class="table table-sm table-hover mb-0">
      <thead class="table-light">
        <tr>
          <th>Home</th><th>Away</th><th>Ground</th><th>Umpires</th><th>Linked Team</th>
        </tr>
      </thead>
      <tbody>`, f.MatchDate.Format("Monday 2 January 2006"))
			}

			linkedCell := `<span class="text-muted small">—</span>`
			if f.LinkedHomeTeam != "" || f.LinkedAwayTeam != "" {
				linkedCell = ""
				if f.LinkedHomeTeam != "" {
					linkedCell += fmt.Sprintf(`<span class="badge bg-success small me-1">H: %s</span>`, escapeHTML(f.LinkedHomeTeam))
				}
				if f.LinkedAwayTeam != "" {
					linkedCell += fmt.Sprintf(`<span class="badge bg-primary small">A: %s</span>`, escapeHTML(f.LinkedAwayTeam))
				}
			}

			u1 := f.Umpire1
			u2 := f.Umpire2
			umpireCell := `<span class="text-muted small">TBC</span>`
			if u1 != "" || u2 != "" {
				umpireCell = fmt.Sprintf(`<span class="small">%s / %s</span>`, escapeHTML(u1), escapeHTML(u2))
			}

			fmt.Fprintf(w, `<tr>
  <td class="small">%s<br><span class="text-muted">%s</span></td>
  <td class="small">%s<br><span class="text-muted">%s</span></td>
  <td class="small text-muted">%s</td>
  <td>%s</td>
  <td>%s</td>
</tr>`,
				escapeHTML(f.HomeClub), escapeHTML(f.HomeTeam),
				escapeHTML(f.AwayClub), escapeHTML(f.AwayTeam),
				escapeHTML(f.Ground),
				umpireCell,
				linkedCell,
			)
		}

		if !currentDate.IsZero() {
			fmt.Fprint(w, `</tbody></table></div></div>`)
		}

		fmt.Fprintf(w, `<p class="text-muted small">%d fixtures total</p>`, len(fixtures))
		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}
