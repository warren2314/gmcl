package httpserver

import (
	"context"
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
			TeamID      int32
			ClubName    string
			TeamName    string
			CaptainName string
			Email       string
			HasFixture  bool
			HasSubmit   bool
			AlreadySent bool
			IsBye       bool
			IsResolved  bool
			WouldSend   bool
		}

		// All active teams with a fixture on this date, plus their submission/reminder status
		rows, _ := s.DB.Query(ctx, `
			SELECT
			    t.id                                             AS team_id,
			    cl.name                                           AS club,
			    t.name                                           AS team,
			    COALESCE(c.full_name, '(no captain)')           AS captain,
			    COALESCE(TRIM(c.email), '')                     AS email,
			    TRUE                                            AS has_fixture,
			    EXISTS(
			        SELECT 1 FROM submissions sub
			        WHERE sub.team_id = t.id
			          AND sub.match_date = $1
			    )                                               AS has_submit,
			    EXISTS(
			        SELECT 1 FROM captain_reminder_log rl
			        WHERE rl.team_id = t.id AND rl.match_date = $1
			    )                                               AS already_sent,
			    lf.is_bye                                       AS is_bye,
			    EXISTS(
			        SELECT 1 FROM report_exemptions re
			        WHERE re.team_id = t.id
			          AND re.match_date = lf.match_date
			          AND (
			              re.play_cricket_match_id = lf.play_cricket_match_id
			              OR re.play_cricket_match_id IS NULL
			          )
			    )                                               AS is_resolved
			FROM league_fixtures lf
			JOIN teams t ON (TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
			              OR TRIM(t.play_cricket_team_id) = TRIM(lf.away_team_pc_id))
			JOIN clubs cl ON cl.id = t.club_id
			LEFT JOIN captains c ON c.team_id = t.id
			    AND c.active_from <= lf.match_date
			    AND (c.active_to IS NULL OR c.active_to >= lf.match_date)
			WHERE lf.match_date = $1
			  AND t.active = TRUE
			ORDER BY cl.name, t.name
		`, matchDate)

		var results []row
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var rr row
				if rows.Scan(&rr.TeamID, &rr.ClubName, &rr.TeamName, &rr.CaptainName, &rr.Email,
					&rr.HasFixture, &rr.HasSubmit, &rr.AlreadySent, &rr.IsBye, &rr.IsResolved) == nil {
					rr.WouldSend = rr.HasFixture && !rr.IsBye && !rr.IsResolved && !rr.HasSubmit && !rr.AlreadySent && rr.Email != ""
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

		type unmappedFixtureRow struct {
			MatchID  int64
			HomeClub string
			HomeTeam string
			HomePCID string
			AwayClub string
			AwayTeam string
			AwayPCID string
			Ground   string
			Missing  string
		}
		unmappedFixturesRows, _ := s.DB.Query(ctx, `
			SELECT lf.play_cricket_match_id,
			       COALESCE(lf.home_club_name,''), COALESCE(lf.home_team_name,''), COALESCE(lf.home_team_pc_id,''),
			       COALESCE(lf.away_club_name,''), COALESCE(lf.away_team_name,''), COALESCE(lf.away_team_pc_id,''),
			       COALESCE(lf.ground_name,''),
			       TRIM(CASE WHEN ht.id IS NULL THEN 'home ' ELSE '' END || CASE WHEN at.id IS NULL THEN 'away' ELSE '' END)
			FROM league_fixtures lf
			LEFT JOIN teams ht ON TRIM(ht.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
			LEFT JOIN teams at ON TRIM(at.play_cricket_team_id) = TRIM(lf.away_team_pc_id)
			WHERE lf.match_date = $1
			  AND NOT lf.is_bye
			  AND (ht.id IS NULL OR at.id IS NULL)
			ORDER BY lf.home_club_name, lf.away_club_name
		`, matchDate)
		var unmappedFixtures []unmappedFixtureRow
		if unmappedFixturesRows != nil {
			defer unmappedFixturesRows.Close()
			for unmappedFixturesRows.Next() {
				var row unmappedFixtureRow
				if unmappedFixturesRows.Scan(&row.MatchID, &row.HomeClub, &row.HomeTeam, &row.HomePCID, &row.AwayClub, &row.AwayTeam, &row.AwayPCID, &row.Ground, &row.Missing) == nil {
					unmappedFixtures = append(unmappedFixtures, row)
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Reminder Preview")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

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
		if sentRaw := r.URL.Query().Get("sent"); sentRaw != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">Reminder send completed: %s sent, %s skipped.</div>`,
				escapeHTML(sentRaw), escapeHTML(r.URL.Query().Get("skipped")))
		}
		if msg := strings.TrimSpace(r.URL.Query().Get("msg")); msg != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(msg))
		}
		if errMsg := strings.TrimSpace(r.URL.Query().Get("error")); errMsg != "" {
			fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(errMsg))
		}

		// Summary badges
		wouldSend := 0
		alreadySent := 0
		submitted := 0
		noEmail := 0
		for _, rr := range results {
			switch {
			case rr.HasSubmit:
				submitted++
			case rr.IsResolved:
				submitted++
			case rr.IsBye:
				alreadySent++
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
			fmt.Fprintf(w, `<div class="mb-3">
  <form method="POST" action="/admin/reminders/send-date" class="d-inline" onsubmit="return confirm('Send game-day reminders for %s to eligible captains?')">
    <input type="hidden" name="csrf_token" value="%s">
    <input type="hidden" name="date" value="%s">
    <button type="submit" class="btn btn-warning btn-sm">Send game-day reminders for this date</button>
  </form>
</div>`, escapeHTML(dateStr), escapeHTML(csrfToken), escapeHTML(dateStr))
			fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Teams with fixtures</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Club</th><th>Team</th><th>Captain</th><th>Email</th><th>Submitted?</th><th>Reminder sent?</th><th>Will receive email?</th><th></th>
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
				} else if rr.IsResolved {
					willSend = `<span class="badge bg-success">Marked done</span>`
				} else if rr.IsBye {
					willSend = `<span class="badge bg-secondary">Marked bye</span>`
				} else if rr.HasSubmit {
					willSend = `<span class="text-muted small">Submitted</span>`
				} else if rr.AlreadySent {
					willSend = `<span class="text-muted small">Already sent</span>`
				} else if rr.Email == "" {
					willSend = `<span class="badge bg-danger">No email</span>`
				}
				actionCell := `<span class="text-muted">-</span>`
				if rr.WouldSend {
					actionCell = fmt.Sprintf(`<form method="POST" action="/admin/reminders/send-team" class="d-inline" onsubmit="return confirm('Send reminder to %s?')">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="date" value="%s">
  <input type="hidden" name="team_id" value="%d">
  <button type="submit" class="btn btn-warning btn-sm py-0">Send now</button>
</form>`, escapeHTML(rr.Email), escapeHTML(csrfToken), escapeHTML(dateStr), rr.TeamID)
				} else if rr.IsBye {
					actionCell = fmt.Sprintf(`<form method="POST" action="/admin/reminders/unmark-bye" class="d-inline" onsubmit="return confirm('Unmark this fixture as a bye?')">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="date" value="%s">
  <input type="hidden" name="team_id" value="%d">
  <button type="submit" class="btn btn-outline-secondary btn-sm py-0">Unmark bye</button>
</form>`, escapeHTML(csrfToken), escapeHTML(dateStr), rr.TeamID)
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
					escapeHTML(rr.ClubName), escapeHTML(rr.TeamName), escapeHTML(rr.CaptainName),
					escapeHTML(rr.Email), submitBadge, sentBadge, willSend, actionCell)
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		}

		if len(unmappedFixtures) > 0 {
			adminRole := adminRoleForRequest(r)
			fmt.Fprintf(w, `
<div class="card shadow-sm mb-4 border-danger">
  <div class="card-header fw-semibold text-danger">%d Play-Cricket fixture(s) are not fully mapped locally</div>
  <div class="table-responsive">
    <table class="table table-sm mb-0">
      <thead><tr><th>Match</th><th>Home</th><th>Away</th><th>Ground</th><th>Missing mapping</th></tr></thead>
      <tbody>
`, len(unmappedFixtures))
			for _, f := range unmappedFixtures {
				missing := strings.TrimSpace(f.Missing)
				if missing == "" {
					missing = "unknown"
				}
				mapActions := missing
				if adminRole == "super_admin" {
					var actionParts []string
					if strings.Contains(missing, "home") && strings.TrimSpace(f.HomePCID) != "" {
						actionParts = append(actionParts, fixtureSideMapButton(csrfToken, f.MatchID, "home", dateStr, f.HomeClub, f.HomeTeam))
					}
					if strings.Contains(missing, "away") && strings.TrimSpace(f.AwayPCID) != "" {
						actionParts = append(actionParts, fixtureSideMapButton(csrfToken, f.MatchID, "away", dateStr, f.AwayClub, f.AwayTeam))
					}
					if len(actionParts) > 0 {
						mapActions = strings.Join(actionParts, " ")
					}
				}
				fmt.Fprintf(w, `<tr>
  <td class="small text-muted">%d</td>
  <td>%s<br><span class="text-muted small">%s / PC %s</span></td>
  <td>%s<br><span class="text-muted small">%s / PC %s</span></td>
  <td class="small text-muted">%s</td>
  <td>%s</td>
</tr>`,
					f.MatchID,
					escapeHTML(f.HomeClub), escapeHTML(f.HomeTeam), escapeHTML(f.HomePCID),
					escapeHTML(f.AwayClub), escapeHTML(f.AwayTeam), escapeHTML(f.AwayPCID),
					escapeHTML(f.Ground), mapActions)
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

func (s *Server) handleAdminReminderSendDate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		dateStr := strings.TrimSpace(r.FormValue("date"))
		matchDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			http.Error(w, "invalid date", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		sent, skipped, err := s.sendRemindersForDate(r, ctx, email.NewFromEnv(), matchDate, "game_day")
		redirect := fmt.Sprintf("/admin/reminders/preview?date=%s&sent=%d&skipped=%d", url.QueryEscape(dateStr), sent, skipped)
		if err != nil {
			http.Redirect(w, r, redirect+"&error="+url.QueryEscape("Some reminders failed: "+err.Error()), http.StatusSeeOther)
			return
		}
		s.audit(ctx, r, "admin", nil, "admin_send_game_day_reminders", "reminder", nil, map[string]any{
			"match_date": dateStr,
			"sent":       sent,
			"skipped":    skipped,
		})
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	}
}

func (s *Server) handleAdminReminderSendTeam() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		dateStr := strings.TrimSpace(r.FormValue("date"))
		matchDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			http.Error(w, "invalid date", http.StatusBadRequest)
			return
		}
		teamID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("team_id")), 10, 32)
		if err != nil || teamID <= 0 {
			http.Error(w, "invalid team", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		emailAddr, err := s.sendReminderForTeamDate(r, ctx, email.NewFromEnv(), int32(teamID), matchDate, "game_day")
		redirect := "/admin/reminders/preview?date=" + url.QueryEscape(dateStr)
		if err != nil {
			http.Redirect(w, r, redirect+"&error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		s.audit(ctx, r, "admin", nil, "admin_send_team_game_day_reminder", "reminder", nil, map[string]any{
			"match_date": dateStr,
			"team_id":    teamID,
			"email":      emailAddr,
		})
		http.Redirect(w, r, redirect+"&msg="+url.QueryEscape("Reminder sent to "+emailAddr), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminReminderUnmarkBye() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		dateStr := strings.TrimSpace(r.FormValue("date"))
		matchDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			http.Error(w, "invalid date", http.StatusBadRequest)
			return
		}
		teamID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("team_id")), 10, 32)
		if err != nil || teamID <= 0 {
			http.Error(w, "invalid team", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		tag, err := s.DB.Exec(ctx, `
			UPDATE league_fixtures lf
			SET is_bye = FALSE
			FROM teams t
			WHERE t.id = $2
			  AND lf.match_date = $1
			  AND (TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
			       OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id))
		`, matchDate, int32(teamID))
		if err != nil {
			http.Redirect(w, r, "/admin/reminders/preview?date="+url.QueryEscape(dateStr)+"&error="+url.QueryEscape("Could not unmark bye: "+err.Error()), http.StatusSeeOther)
			return
		}
		s.audit(ctx, r, "admin", nil, "fixture_unmarked_bye", "league_fixture", nil, map[string]any{
			"match_date": dateStr,
			"team_id":    teamID,
			"updated":    tag.RowsAffected(),
		})
		http.Redirect(w, r, "/admin/reminders/preview?date="+url.QueryEscape(dateStr)+"&msg="+url.QueryEscape("Fixture unmarked as bye. You can now send the reminder."), http.StatusSeeOther)
	}
}

func (s *Server) sendReminderForTeamDate(r *http.Request, ctx context.Context, mailer *email.Client, teamID int32, matchDate time.Time, reminderType string) (string, error) {
	dateStr := matchDate.Format("2006-01-02")
	seasonID, weekID, err := s.reminderWeekForDate(ctx, matchDate)
	if err != nil {
		return "", err
	}
	var ct struct {
		TeamID     int32
		CaptainID  int32
		FullName   string
		Email      string
		ClubName   string
		TeamName   string
		Opposition string
		IsHome     bool
	}
	err = s.DB.QueryRow(ctx, `
		SELECT DISTINCT ON (t.id)
		    t.id,
		    c.id,
		    c.full_name,
		    TRIM(c.email),
		    cl.name,
		    t.name,
		    CASE WHEN TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
		         THEN COALESCE(TRIM(lf.away_club_name),'') || ' ' || COALESCE(TRIM(lf.away_team_name),'')
		         ELSE COALESCE(TRIM(lf.home_club_name),'') || ' ' || COALESCE(TRIM(lf.home_team_name),'')
		    END,
		    TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
		FROM league_fixtures lf
		JOIN teams t ON (TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
		              OR TRIM(t.play_cricket_team_id) = TRIM(lf.away_team_pc_id))
		JOIN clubs cl ON cl.id = t.club_id
		JOIN captains c ON c.team_id = t.id
		    AND c.active_from <= lf.match_date
		    AND (c.active_to IS NULL OR c.active_to >= lf.match_date)
		    AND TRIM(c.email) <> ''
		WHERE lf.match_date = $1
		  AND t.id = $2
		  AND t.active = TRUE
		  AND NOT lf.is_bye
		  AND NOT EXISTS (
		      SELECT 1 FROM submissions
		      WHERE team_id = t.id AND match_date = $1
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM report_exemptions re
		      WHERE re.team_id = t.id
		        AND re.match_date = lf.match_date
		        AND (
		            re.play_cricket_match_id = lf.play_cricket_match_id
		            OR re.play_cricket_match_id IS NULL
		        )
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM captain_reminder_log
		      WHERE team_id = t.id AND match_date = $1 AND reminder_type = $3
		  )
		ORDER BY t.id, c.active_from DESC
	`, matchDate, teamID, reminderType).Scan(
		&ct.TeamID, &ct.CaptainID, &ct.FullName, &ct.Email,
		&ct.ClubName, &ct.TeamName, &ct.Opposition, &ct.IsHome,
	)
	if err != nil {
		return "", fmt.Errorf("no eligible reminder target found for this team/date")
	}

	token, err := auth.GenerateAndStoreMagicTokenForDate(
		ctx, s.DB, ct.CaptainID, seasonID, weekID, matchDate, nextWednesdayMidnight(matchDate), "", "admin-reminder",
	)
	if err != nil {
		_ = s.recordReminderSendFailure(ctx, ct.TeamID, weekID, matchDate, reminderType, ct.Email, "magic_link", err)
		return ct.Email, fmt.Errorf("could not create magic link: %w", err)
	}
	link := magicLinkEmailBlock(r, token)
	subject, body := buildReminderEmail(reminderType, ct.FullName, ct.ClubName, ct.TeamName, dateStr, strings.TrimSpace(ct.Opposition), ct.IsHome, link)
	if err := mailer.Send(ct.Email, subject, body); err != nil {
		_ = s.recordReminderSendFailure(ctx, ct.TeamID, weekID, matchDate, reminderType, ct.Email, "email_send", err)
		return ct.Email, fmt.Errorf("email send failed for %s: %w", ct.Email, err)
	}
	if _, err := s.DB.Exec(ctx, `
		INSERT INTO captain_reminder_log (team_id, week_id, match_date, reminder_type, captain_email)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, match_date, reminder_type) DO NOTHING
	`, ct.TeamID, weekID, matchDate, reminderType, ct.Email); err != nil {
		_ = s.recordReminderSendFailure(ctx, ct.TeamID, weekID, matchDate, reminderType, ct.Email, "send_log", err)
		return ct.Email, fmt.Errorf("could not record send log for %s: %w", ct.Email, err)
	}
	return ct.Email, nil
}

func (s *Server) handleAdminReminderMapFixtureSide() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		matchID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("match_id")), 10, 64)
		if err != nil || matchID <= 0 {
			http.Error(w, "invalid match", http.StatusBadRequest)
			return
		}
		side := strings.TrimSpace(r.FormValue("side"))
		if side != "home" && side != "away" {
			http.Error(w, "invalid side", http.StatusBadRequest)
			return
		}
		dateStr := strings.TrimSpace(r.FormValue("date"))

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		var matchDate time.Time
		var homeClub, homeTeam, homePCID, awayClub, awayTeam, awayPCID string
		err = s.DB.QueryRow(ctx, `
			SELECT match_date,
			       COALESCE(home_club_name,''), COALESCE(home_team_name,''), COALESCE(home_team_pc_id,''),
			       COALESCE(away_club_name,''), COALESCE(away_team_name,''), COALESCE(away_team_pc_id,'')
			FROM league_fixtures
			WHERE play_cricket_match_id = $1
		`, matchID).Scan(&matchDate, &homeClub, &homeTeam, &homePCID, &awayClub, &awayTeam, &awayPCID)
		if err != nil {
			http.Error(w, "fixture not found", http.StatusNotFound)
			return
		}
		if dateStr == "" {
			dateStr = matchDate.Format("2006-01-02")
		}

		clubName, teamName, pcID := homeClub, homeTeam, strings.TrimSpace(homePCID)
		if side == "away" {
			clubName, teamName, pcID = awayClub, awayTeam, strings.TrimSpace(awayPCID)
		}
		if pcID == "" {
			http.Error(w, "fixture side has no Play-Cricket team ID", http.StatusBadRequest)
			return
		}

		resolver, err := newPlayCricketResolver(ctx, s)
		if err != nil {
			http.Error(w, "resolver failed", http.StatusInternalServerError)
			return
		}
		local, ok := resolver.resolve(clubName, teamName)
		if !ok {
			http.Error(w, "no unique local team match for "+clubName+" "+teamName, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(local.CurrentID) != "" && strings.TrimSpace(local.CurrentID) != pcID {
			http.Error(w, "local team already has a different Play-Cricket ID", http.StatusConflict)
			return
		}

		_, err = s.DB.Exec(ctx, `
			UPDATE teams
			SET play_cricket_team_id = $1
			WHERE id = $2
			  AND (play_cricket_team_id IS NULL OR TRIM(play_cricket_team_id) = '' OR TRIM(play_cricket_team_id) = $1)
		`, pcID, local.ID)
		if err != nil {
			http.Error(w, "could not update team mapping", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "admin", nil, "play_cricket_fixture_side_mapped", "team", func() *int64 {
			v := int64(local.ID)
			return &v
		}(), map[string]any{
			"match_id": matchID,
			"side":     side,
			"pc_id":    pcID,
			"club":     clubName,
			"team":     teamName,
		})

		http.Redirect(w, r, "/admin/reminders/preview?date="+dateStr, http.StatusSeeOther)
	}
}

func fixtureSideMapButton(csrfToken string, matchID int64, side, dateStr, clubName, teamName string) string {
	label := "Map " + side
	title := "Map " + clubName + " " + teamName
	return fmt.Sprintf(`<form method="POST" action="/admin/reminders/map-fixture-side" class="d-inline me-1">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="match_id" value="%d">
  <input type="hidden" name="side" value="%s">
  <input type="hidden" name="date" value="%s">
  <button type="submit" class="btn btn-outline-danger btn-sm py-0" title="%s">%s</button>
</form>`, escapeHTML(csrfToken), matchID, escapeHTML(side), escapeHTML(dateStr), escapeHTML(title), escapeHTML(label))
}
