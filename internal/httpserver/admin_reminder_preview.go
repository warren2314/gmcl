package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

func (s *Server) handleAdminReminderPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Default to today; allow ?date= override
		dateStr := r.URL.Query().Get("date")
		if dateStr == "" {
			dateStr = time.Now().Format("2006-01-02")
		}
		matchDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			matchDate = time.Now()
			dateStr = matchDate.Format("2006-01-02")
		}

		type row struct {
			ClubName    string
			TeamName    string
			CaptainName string
			Email       string
			HasFixture  bool
			HasSubmit   bool
			AlreadySent bool
			WouldSend   bool
		}

		// All active teams with a fixture on this date, plus their submission/reminder status
		rows, _ := s.DB.Query(ctx, `
			SELECT
			    cl.name                                           AS club,
			    t.name                                           AS team,
			    COALESCE(c.full_name, '(no captain)')           AS captain,
			    COALESCE(TRIM(c.email), '')                     AS email,
			    TRUE                                            AS has_fixture,
			    EXISTS(
			        SELECT 1 FROM submissions sub
			        JOIN weeks w ON sub.week_id = w.id
			        WHERE sub.team_id = t.id
			          AND $1 BETWEEN w.start_date AND w.end_date
			    )                                               AS has_submit,
			    EXISTS(
			        SELECT 1 FROM captain_reminder_log rl
			        WHERE rl.team_id = t.id AND rl.match_date = $1
			    )                                               AS already_sent
			FROM league_fixtures lf
			JOIN teams t ON (t.play_cricket_team_id = lf.home_team_pc_id
			              OR t.play_cricket_team_id = lf.away_team_pc_id)
			JOIN clubs cl ON cl.id = t.club_id
			LEFT JOIN captains c ON c.team_id = t.id
			    AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
			WHERE lf.match_date = $1
			  AND t.active = TRUE
			ORDER BY cl.name, t.name
		`, matchDate)

		var results []row
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var rr row
				if rows.Scan(&rr.ClubName, &rr.TeamName, &rr.CaptainName, &rr.Email,
					&rr.HasFixture, &rr.HasSubmit, &rr.AlreadySent) == nil {
					rr.WouldSend = rr.HasFixture && !rr.HasSubmit && !rr.AlreadySent && rr.Email != ""
					results = append(results, rr)
				}
			}
		}

		// Teams with no play_cricket_team_id mapping — won't get any reminders
		unmapped, _ := s.DB.Query(ctx, `
			SELECT cl.name, t.name
			FROM teams t
			JOIN clubs cl ON cl.id = t.club_id
			WHERE t.active = TRUE
			  AND (t.play_cricket_team_id IS NULL OR TRIM(t.play_cricket_team_id) = '')
			ORDER BY cl.name, t.name
		`)
		type unmappedRow struct{ Club, Team string }
		var unmappedTeams []unmappedRow
		if unmapped != nil {
			defer unmapped.Close()
			for unmapped.Next() {
				var u unmappedRow
				if unmapped.Scan(&u.Club, &u.Team) == nil {
					unmappedTeams = append(unmappedTeams, u)
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Reminder Preview")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)
		fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Reminder Preview</h4>
    <p class="text-muted mb-0 small">Which teams would receive a reminder email for a given match date</p>
  </div>
  <form method="GET" action="/admin/reminders/preview" class="d-flex gap-2 align-items-center">
    <label class="form-label mb-0 small text-muted">Match date</label>
    <input type="date" name="date" value="%s" class="form-control form-control-sm" style="width:160px">
    <button class="btn btn-outline-secondary btn-sm">Check</button>
  </form>
</div>
`, dateStr)

		// Summary badges
		wouldSend := 0
		alreadySent := 0
		submitted := 0
		noEmail := 0
		for _, rr := range results {
			switch {
			case rr.HasSubmit:
				submitted++
			case rr.AlreadySent:
				alreadySent++
			case rr.Email == "":
				noEmail++
			default:
				wouldSend++
			}
		}

		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-auto">
    <div class="card card-kpi p-3 text-center" style="min-width:110px;border-top:3px solid #0d6efd">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Fixtures Today</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi p-3 text-center" style="min-width:110px;border-top:3px solid #198754">
      <div class="kpi-number text-success">%d</div>
      <div class="kpi-label">Already Submitted</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi p-3 text-center" style="min-width:110px;border-top:3px solid #ffc107">
      <div class="kpi-number text-warning">%d</div>
      <div class="kpi-label">Would Get Email</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi p-3 text-center" style="min-width:110px;border-top:3px solid #6c757d">
      <div class="kpi-number text-muted">%d</div>
      <div class="kpi-label">Already Reminded</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi p-3 text-center" style="min-width:110px;border-top:3px solid #dc3545">
      <div class="kpi-number text-danger">%d</div>
      <div class="kpi-label">No Email on File</div>
    </div>
  </div>
</div>
`, len(results), submitted, wouldSend, alreadySent, noEmail)

		if len(results) == 0 {
			fmt.Fprintf(w, `<div class="alert alert-warning">No fixtures found in the database for %s. Make sure Play-Cricket fixtures are synced.</div>`, escapeHTML(dateStr))
		} else {
			fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Teams with fixtures</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Club</th><th>Team</th><th>Captain</th><th>Email</th><th>Submitted?</th><th>Reminder sent?</th><th>Will receive email?</th>
      </tr></thead>
      <tbody>
`)
			for _, rr := range results {
				submitBadge := `<span class="badge bg-danger">No</span>`
				if rr.HasSubmit {
					submitBadge = `<span class="badge bg-success">Yes</span>`
				}
				sentBadge := `<span class="text-muted">—</span>`
				if rr.AlreadySent {
					sentBadge = `<span class="badge bg-secondary">Sent</span>`
				}
				willSend := `<span class="text-muted">—</span>`
				if rr.WouldSend {
					willSend = `<span class="badge bg-warning text-dark">&#x2713; Yes</span>`
				} else if rr.HasSubmit {
					willSend = `<span class="text-muted small">Submitted</span>`
				} else if rr.AlreadySent {
					willSend = `<span class="text-muted small">Already sent</span>`
				} else if rr.Email == "" {
					willSend = `<span class="badge bg-danger">No email</span>`
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
					escapeHTML(rr.ClubName), escapeHTML(rr.TeamName), escapeHTML(rr.CaptainName),
					escapeHTML(rr.Email), submitBadge, sentBadge, willSend)
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		}

		if len(unmappedTeams) > 0 {
			fmt.Fprintf(w, `
<div class="card shadow-sm mb-4 border-warning">
  <div class="card-header fw-semibold text-warning">&#9888; %d active team(s) not mapped to Play-Cricket — will never receive reminders</div>
  <div class="table-responsive">
    <table class="table table-sm mb-0">
      <thead><tr><th>Club</th><th>Team</th></tr></thead>
      <tbody>
`, len(unmappedTeams))
			for _, u := range unmappedTeams {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`, escapeHTML(u.Club), escapeHTML(u.Team))
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		}

		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}
