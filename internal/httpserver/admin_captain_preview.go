package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cricket-ground-feedback/internal/leagueapi"
	"cricket-ground-feedback/internal/middleware"
)

func (s *Server) handleAdminCaptainPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		// Load all active teams for the selector
		type teamOpt struct {
			ID       int32
			Club     string
			Team     string
			SeasonID int32
			WeekID   int32
		}
		var teams []teamOpt
		trows, _ := s.DB.Query(ctx, `
			SELECT t.id, cl.name, t.name,
			       (SELECT id FROM seasons WHERE is_archived=FALSE ORDER BY id DESC LIMIT 1),
			       (SELECT id FROM weeks WHERE season_id=(SELECT id FROM seasons WHERE is_archived=FALSE ORDER BY id DESC LIMIT 1)
			        ORDER BY
			            CASE WHEN CURRENT_DATE BETWEEN start_date AND end_date THEN 0
			                 WHEN start_date > CURRENT_DATE THEN 1 ELSE 2 END,
			            abs(start_date - CURRENT_DATE) LIMIT 1)
			FROM teams t JOIN clubs cl ON cl.id=t.club_id
			WHERE t.active=TRUE
			ORDER BY cl.name, t.name
		`)
		if trows != nil {
			defer trows.Close()
			for trows.Next() {
				var to teamOpt
				if trows.Scan(&to.ID, &to.Club, &to.Team, &to.SeasonID, &to.WeekID) == nil {
					teams = append(teams, to)
				}
			}
		}

		teamIDStr := r.URL.Query().Get("team_id")
		teamID, _ := strconv.Atoi(teamIDStr)

		if teamID == 0 {
			// Show selector page
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			pageHead(w, "Captain Form Preview")
			writeAdminNav(w, csrfToken, r.URL.Path)
			fmt.Fprint(w, `<div class="container-fluid px-4">`)
			fmt.Fprint(w, `
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Captain Form Preview</h4>
    <p class="text-muted mb-0 small">Preview the captain form as any team would see it</p>
  </div>
</div>
<div class="card shadow-sm mb-4" style="max-width:480px">
  <div class="card-body">
    <form method="GET" action="/admin/captain-preview">
      <label class="form-label fw-semibold">Select team</label>
      <select name="team_id" class="form-select mb-3" onchange="this.form.submit()">
        <option value="">— choose a team —</option>`)
			for _, to := range teams {
				fmt.Fprintf(w, `<option value="%d">%s — %s</option>`, to.ID, escapeHTML(to.Club), escapeHTML(to.Team))
			}
			fmt.Fprint(w, `      </select>
    </form>
  </div>
</div>
</div>`)
			pageFooter(w)
			return
		}

		// Load team/captain details
		var clubName, teamName, captainName, captainEmail string
		var seasonID, weekID int32
		err := s.DB.QueryRow(ctx, `
			SELECT cl.name, t.name, COALESCE(c.full_name,'(no captain)'), COALESCE(c.email,''),
			       (SELECT id FROM seasons WHERE is_archived=FALSE ORDER BY id DESC LIMIT 1),
			       (SELECT id FROM weeks WHERE season_id=(SELECT id FROM seasons WHERE is_archived=FALSE ORDER BY id DESC LIMIT 1)
			        ORDER BY
			            CASE WHEN CURRENT_DATE BETWEEN start_date AND end_date THEN 0
			                 WHEN start_date > CURRENT_DATE THEN 1 ELSE 2 END,
			            abs(start_date - CURRENT_DATE) LIMIT 1)
			FROM teams t
			JOIN clubs cl ON cl.id=t.club_id
			LEFT JOIN captains c ON c.team_id=t.id AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
			WHERE t.id=$1
			ORDER BY c.active_from DESC NULLS LAST
			LIMIT 1
		`, int32(teamID)).Scan(&clubName, &teamName, &captainName, &captainEmail, &seasonID, &weekID)
		if err != nil {
			http.Error(w, "team not found", http.StatusNotFound)
			return
		}

		// Load week dates
		var weekStart, weekEnd time.Time
		_ = s.DB.QueryRow(ctx, `SELECT start_date, end_date FROM weeks WHERE id=$1`, weekID).Scan(&weekStart, &weekEnd)

		// Resolve fixture — same logic as captain form
		now := time.Now()
		var weekFixtures []leagueapi.WeekFixture
		if !weekStart.IsZero() {
			weekFixtures, _ = leagueapi.LookupWeekFixtures(ctx, s.DB, int32(teamID), weekStart, weekEnd, now)
		}

		var targetDate *time.Time
		if qd := r.URL.Query().Get("match_date"); qd != "" {
			if pd, err := time.Parse("2006-01-02", qd); err == nil {
				targetDate = &pd
			}
		}
		if targetDate == nil {
			for i := range weekFixtures {
				if !weekFixtures[i].Submitted {
					d := weekFixtures[i].MatchDate
					targetDate = &d
					break
				}
			}
			if targetDate == nil && len(weekFixtures) > 0 {
				d := weekFixtures[len(weekFixtures)-1].MatchDate
				targetDate = &d
			}
		}

		draft := make(map[string]any)
		var fp leagueapi.FixturePrefill
		var fpOK bool
		if targetDate != nil {
			fp, fpOK = leagueapi.LookupFixturePrefillByDate(ctx, s.DB, int32(teamID), *targetDate)
		} else if !weekStart.IsZero() {
			fp, fpOK = leagueapi.LookupFixturePrefill(ctx, s.DB, int32(teamID), weekStart, weekEnd)
		}
		if fpOK {
			if fp.MatchDate != "" {
				draft["match_date"] = fp.MatchDate
			}
			if fp.Umpire1 != "" {
				draft["umpire1_name"] = fp.Umpire1
			}
			if fp.Umpire2 != "" {
				draft["umpire2_name"] = fp.Umpire2
			}
			if fp.Opposition != "" {
				draft["opposition"] = fp.Opposition
			}
			if fp.Venue != "" {
				draft["venue"] = fp.Venue
			}
			draft["prefill_source"] = "league_api"
		}
		if _, ok := draft["match_date"]; !ok {
			draft["match_date"] = now.Format("2006-01-02")
		}

		// Fixture chooser
		var fixtureChooser string
		if len(weekFixtures) > 1 {
			activeDateStr := ""
			if targetDate != nil {
				activeDateStr = targetDate.Format("2006-01-02")
			}
			fixtureChooser = `<div class="alert alert-info mb-3"><strong>You have multiple fixtures this week.</strong> Select the match you are reporting on:<div class="d-flex flex-wrap gap-2 mt-2">`
			for _, wf := range weekFixtures {
				ds := wf.MatchDate.Format("2006-01-02")
				venue := "Away"
				if wf.IsHome {
					venue = "Home"
				}
				label := wf.MatchDate.Format("Mon 2 Jan")
				if wf.Opposition != "" {
					label += " — " + wf.Opposition + " (" + venue + ")"
				}
				btnClass := "btn btn-outline-primary btn-sm"
				if ds == activeDateStr {
					btnClass = "btn btn-primary btn-sm"
				}
				tick := ""
				if wf.Submitted {
					tick = " ✓"
					btnClass = "btn btn-outline-success btn-sm"
				}
				fixtureChooser += fmt.Sprintf(`<a href="/admin/captain-preview?team_id=%d&match_date=%s" class="%s">%s%s</a>`,
					teamID, ds, btnClass, escapeHTML(label), tick)
			}
			fixtureChooser += `</div></div>`
		}

		// Admin banner at top so it's clear this is a preview
		adminBanner := fmt.Sprintf(`<div class="alert alert-warning d-flex align-items-center justify-content-between mb-3">
  <span><strong>Admin preview</strong> — viewing as <em>%s — %s</em> (%s). Form is read-only in preview.</span>
  <div class="d-flex gap-2">
    <a href="/admin/captain-preview" class="btn btn-sm btn-outline-secondary">Change team</a>
    <a href="/admin/captain-preview?team_id=%d" class="btn btn-sm btn-outline-primary">Refresh</a>
  </div>
</div>`,
			escapeHTML(clubName), escapeHTML(teamName), escapeHTML(captainName), teamID)
		fixtureChooser = adminBanner + fixtureChooser

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		umpires := s.loadUmpires(ctx)
		s.renderGMCLFormWithChooser(w, seasonID, csrfToken, clubName, teamName, captainName, captainEmail,
			captainName, captainEmail, "captain", now.Format("2006-01-02"), draft, umpires, fixtureChooser)
	}
}
