package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/leagueapi"
)

type sendRemindersRequest struct {
	Type      string `json:"type"`       // "game_day" | "monday" | "wednesday"
	MatchDate string `json:"match_date"` // YYYY-MM-DD, required for game_day
}

type internalResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// handleInternalSendReminders is called by n8n via HMAC-authenticated endpoint.
// type=game_day  → email captains playing on match_date (Sat/Sun 21:00)
// type=monday    → remind teams that haven't submitted for last weekend (Mon 10:00)
// type=wednesday → final reminder before midnight deadline (Wed 10:00)
func (s *Server) handleInternalSendReminders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req sendRemindersRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		mailer := email.NewFromEnv()
		now := time.Now().UTC()

		var matchDates []time.Time

		switch req.Type {
		case "game_day":
			if req.MatchDate == "" {
				http.Error(w, "match_date required for game_day", http.StatusBadRequest)
				return
			}
			d, err := time.Parse("2006-01-02", req.MatchDate)
			if err != nil {
				http.Error(w, "invalid match_date", http.StatusBadRequest)
				return
			}
			matchDates = []time.Time{d}
		case "monday":
			// Monday 10:00 → last Saturday and Sunday
			matchDates = []time.Time{
				now.AddDate(0, 0, -2).Truncate(24 * time.Hour),
				now.AddDate(0, 0, -1).Truncate(24 * time.Hour),
			}
		case "wednesday":
			// Wednesday 10:00 → last Saturday and Sunday
			matchDates = []time.Time{
				now.AddDate(0, 0, -4).Truncate(24 * time.Hour),
				now.AddDate(0, 0, -3).Truncate(24 * time.Hour),
			}
		default:
			http.Error(w, "unknown type", http.StatusBadRequest)
			return
		}

		sent := 0
		skipped := 0

		for _, matchDate := range matchDates {
			n, sk, err := s.sendRemindersForDate(ctx, mailer, matchDate, req.Type)
			if err != nil {
				http.Error(w, "error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			sent += n
			skipped += sk
		}

		s.audit(ctx, r, "n8n", nil, "send_reminders", "reminder", nil, map[string]any{
			"type":    req.Type,
			"sent":    sent,
			"skipped": skipped,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(internalResponse{
			Status:  "ok",
			Message: fmt.Sprintf("sent=%d skipped=%d", sent, skipped),
		})
	}
}

// sendRemindersForDate sends the appropriate reminder email to all captains
// who played on matchDate and haven't already received this reminder type.
func (s *Server) sendRemindersForDate(ctx context.Context, mailer *email.Client, matchDate time.Time, reminderType string) (sent, skipped int, err error) {
	dateStr := matchDate.Format("2006-01-02")

	type captainTarget struct {
		TeamID    int32
		WeekID    int32
		SeasonID  int32
		CaptainID int32
		FullName  string
		Email     string
		ClubName  string
		TeamName  string
	}

	rows, err := s.DB.Query(ctx, `
		SELECT DISTINCT ON (t.id)
		    t.id        AS team_id,
		    w.id        AS week_id,
		    w.season_id,
		    c.id        AS captain_id,
		    c.full_name,
		    c.email,
		    cl.name     AS club_name,
		    t.name      AS team_name
		FROM league_fixtures lf
		JOIN teams t ON (t.play_cricket_team_id = lf.home_team_pc_id
		              OR t.play_cricket_team_id = lf.away_team_pc_id)
		JOIN clubs cl ON cl.id = t.club_id
		JOIN captains c ON c.team_id = t.id
		    AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
		    AND TRIM(c.email) <> ''
		JOIN weeks w ON $1 BETWEEN w.start_date AND w.end_date
		    AND w.season_id = (SELECT s.id FROM seasons s WHERE $1 BETWEEN s.start_date AND s.end_date LIMIT 1)
		WHERE lf.match_date = $1
		  AND t.active = TRUE
		  AND NOT EXISTS (
		      SELECT 1 FROM captain_reminder_log
		      WHERE team_id = t.id AND match_date = $1 AND reminder_type = $2
		  )
		ORDER BY t.id, c.active_from DESC
	`, matchDate, reminderType)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	var targets []captainTarget
	for rows.Next() {
		var ct captainTarget
		if e := rows.Scan(&ct.TeamID, &ct.WeekID, &ct.SeasonID, &ct.CaptainID,
			&ct.FullName, &ct.Email, &ct.ClubName, &ct.TeamName); e != nil {
			return 0, 0, e
		}
		targets = append(targets, ct)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	// Wednesday 23:59 of the week as magic link expiry
	weekExpiry := nextWednesdayMidnight(matchDate)

	for _, ct := range targets {
		token, err := auth.GenerateAndStoreMagicTokenWithRevocation(
			ctx, s.DB, ct.CaptainID, ct.SeasonID, ct.WeekID, weekExpiry, "", "n8n-reminder",
		)
		if err != nil {
			skipped++
			continue
		}

		link := "https://gmcl.co.uk/magic-link/confirm?token=" + token
		subject, body := buildReminderEmail(reminderType, ct.FullName, ct.ClubName, ct.TeamName, dateStr, link)

		if err := mailer.Send(ct.Email, subject, body); err != nil {
			skipped++
			continue
		}

		_, _ = s.DB.Exec(ctx, `
			INSERT INTO captain_reminder_log (team_id, week_id, match_date, reminder_type, captain_email)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (team_id, match_date, reminder_type) DO NOTHING
		`, ct.TeamID, ct.WeekID, matchDate, reminderType, ct.Email)

		sent++
	}

	return sent, skipped, nil
}

// handleInternalGenerateSanctions runs at Wed 23:59 to auto-draft sanction notices
// for all teams that played last weekend and haven't submitted.
func (s *Server) handleInternalGenerateSanctions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		now := time.Now().UTC()
		// Wednesday 23:59 → last Saturday = today-4, last Sunday = today-3
		sat := now.AddDate(0, 0, -4).Truncate(24 * time.Hour)
		sun := now.AddDate(0, 0, -3).Truncate(24 * time.Hour)

		type missedTeam struct {
			TeamID    int32
			ClubID    int32
			WeekID    int32
			SeasonID  int32
			MatchDate time.Time
			ClubName  string
			TeamName  string
		}

		rows, err := s.DB.Query(ctx, `
			SELECT DISTINCT ON (t.id, lf.match_date)
			    t.id, t.club_id, w.id, w.season_id, lf.match_date, cl.name, t.name
			FROM league_fixtures lf
			JOIN teams t ON (t.play_cricket_team_id = lf.home_team_pc_id
			              OR t.play_cricket_team_id = lf.away_team_pc_id)
			JOIN clubs cl ON cl.id = t.club_id
			JOIN weeks w ON lf.match_date BETWEEN w.start_date AND w.end_date
			WHERE lf.match_date IN ($1, $2)
			  AND t.active = TRUE
			  AND NOT EXISTS (
			      SELECT 1 FROM submissions s
			      WHERE s.team_id = t.id AND s.match_date = lf.match_date
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM sanctions sa
			      WHERE sa.team_id = t.id AND sa.week_id = w.id
			        AND sa.reason = 'non_submission' AND sa.status IN ('active','served')
			  )
			ORDER BY t.id, lf.match_date
		`, sat, sun)
		if err != nil {
			http.Error(w, "query error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var missed []missedTeam
		for rows.Next() {
			var m missedTeam
			if e := rows.Scan(&m.TeamID, &m.ClubID, &m.WeekID, &m.SeasonID,
				&m.MatchDate, &m.ClubName, &m.TeamName); e != nil {
				http.Error(w, "scan error", http.StatusInternalServerError)
				return
			}
			missed = append(missed, m)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "rows error", http.StatusInternalServerError)
			return
		}

		drafted := 0
		for _, m := range missed {
			var totalOffences, redCount int64
			s.DB.QueryRow(ctx, `
				SELECT COUNT(*) FROM sanctions
				WHERE team_id=$1 AND season_id=$2 AND reason='non_submission'
				  AND status IN ('active','served')
			`, m.TeamID, m.SeasonID).Scan(&totalOffences)
			s.DB.QueryRow(ctx, `
				SELECT COUNT(*) FROM sanctions
				WHERE team_id=$1 AND season_id=$2 AND reason='non_submission'
				  AND colour='red' AND status IN ('active','served')
			`, m.TeamID, m.SeasonID).Scan(&redCount)

			// Every 3rd offence is a red card; points deduction increments per red.
			colour := "yellow"
			pointsDeduction := 0
			if (totalOffences+1)%3 == 0 {
				colour = "red"
				pointsDeduction = int(redCount) + 1
			}

			dateStr := m.MatchDate.Format("2 January 2006")
			subject, body := buildSanctionEmail(colour, m.ClubName, m.TeamName, dateStr, pointsDeduction)

			var sanctionID int64
			err := s.DB.QueryRow(ctx, `
				INSERT INTO sanctions
				    (season_id, week_id, team_id, club_id, colour, reason, points_deduction, email_subject, email_body, email_status)
				VALUES ($1, $2, $3, $4, $5, 'non_submission', $6, $7, $8, 'pending')
				RETURNING id
			`, m.SeasonID, m.WeekID, m.TeamID, m.ClubID, colour, pointsDeduction, subject, body).Scan(&sanctionID)
			if err != nil {
				continue
			}
			drafted++

			eid := sanctionID
			s.audit(ctx, r, "n8n", nil, "sanction_auto_drafted", "sanction", &eid, map[string]any{
				"team_id":          m.TeamID,
				"colour":           colour,
				"points_deduction": pointsDeduction,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"drafted": drafted,
		})
	}
}

// handleInternalGenerateWeeklyReport computes weekly stats and stores AI summary skeleton.
func (s *Server) handleInternalGenerateWeeklyReport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SeasonID int32 `json:"season_id"`
			WeekID   int32 `json:"week_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		var total int64
		var avgPitch float64
		err := s.DB.QueryRow(ctx, `
			SELECT COUNT(*), COALESCE(AVG(pitch_rating),0)
			FROM submissions
			WHERE season_id = $1 AND week_id = $2
		`, req.SeasonID, req.WeekID).Scan(&total, &avgPitch)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		summary := map[string]any{
			"total_submissions": total,
			"average_pitch":     avgPitch,
		}

		payload, err := json.Marshal(summary)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		_, err = s.DB.Exec(ctx, `
			INSERT INTO ai_summaries (season_id, week_id, summary_json, status)
			VALUES ($1, $2, $3, 'draft')
			ON CONFLICT (season_id, week_id)
			DO UPDATE SET summary_json = EXCLUDED.summary_json,
			              status = 'draft',
			              created_at = now()
		`, req.SeasonID, req.WeekID, payload)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "n8n", nil, "internal_generate_weekly_report", "ai_summary", nil, map[string]any{
			"season_id": req.SeasonID,
			"week_id":   req.WeekID,
			"total":     total,
			"avg_pitch": avgPitch,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(internalResponse{Status: "ok"})
	}
}

// handleInternalSyncLeagueFixtures pulls match details from the Play-Cricket API and upserts into league_fixtures.
func (s *Server) handleInternalSyncLeagueFixtures() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MatchDate string          `json:"match_date"`
			SeasonID  *int32          `json:"season_id"`
			RawBody   json.RawMessage `json:"raw_body"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		var details []leagueapi.MatchDetail
		if len(req.RawBody) > 0 {
			parsed, err := leagueapi.ParseMatchDetailsJSON(req.RawBody)
			if err != nil {
				http.Error(w, "invalid raw_body: "+err.Error(), http.StatusBadRequest)
				return
			}
			details = parsed.MatchDetails
		} else {
			if req.MatchDate == "" {
				http.Error(w, "match_date required when raw_body omitted", http.StatusBadRequest)
				return
			}
			md, err := time.Parse("2006-01-02", req.MatchDate)
			if err != nil {
				http.Error(w, "invalid match_date (use YYYY-MM-DD)", http.StatusBadRequest)
				return
			}
			cfg := leagueapi.NewConfigFromEnv()
			client := leagueapi.NewClient(cfg)
			var err2 error
			details, err2 = client.FetchMatchesForDate(ctx, md)
			if err2 != nil {
				http.Error(w, err2.Error(), http.StatusBadGateway)
				return
			}
		}

		if err := leagueapi.UpsertFixtureBatch(ctx, s.DB, req.SeasonID, details); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "n8n", nil, "internal_sync_league_fixtures", "league_fixture", nil, map[string]any{
			"count":      len(details),
			"match_date": req.MatchDate,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"count":  len(details),
		})
	}
}

// handleInternalPreviewEmail renders reminder/sanction email templates without sending.
// POST body: {"type":"game_day"|"monday"|"wednesday"|"yellow"|"red", "send_to":"optional@email.com"}
func (s *Server) handleInternalPreviewEmail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Type   string `json:"type"`
			SendTo string `json:"send_to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		var subject, body string
		link := "https://gmcl.co.uk/magic-link/confirm?token=EXAMPLE_TOKEN"

		switch req.Type {
		case "yellow":
			subject, body = buildSanctionEmail("yellow", "Example CC", "Example CC - 1st XI", "26 April 2026", 0)
		case "red":
			subject, body = buildSanctionEmail("red", "Example CC", "Example CC - 1st XI", "26 April 2026", 5)
		default:
			t := req.Type
			if t == "" {
				t = "game_day"
			}
			subject, body = buildReminderEmail(t, "Joe Bloggs", "Example CC", "Example CC - 1st XI", "26 April 2026", link)
		}

		if req.SendTo != "" {
			mailer := email.NewFromEnv()
			if err := mailer.Send(req.SendTo, subject, body); err != nil {
				http.Error(w, "send failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subject": subject,
			"body":    body,
			"sent_to": req.SendTo,
		})
	}
}

// nextWednesdayMidnight returns 23:59:59 on the Wednesday following or equal to matchDate.
func nextWednesdayMidnight(matchDate time.Time) time.Time {
	d := matchDate
	for d.Weekday() != time.Wednesday {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.UTC)
}

// buildReminderEmail returns subject and plain-text body for a captain reminder.
func buildReminderEmail(reminderType, captainName, clubName, teamName, matchDate, link string) (subject, body string) {
	firstName := strings.SplitN(strings.TrimSpace(captainName), " ", 2)[0]
	if firstName == "" {
		firstName = "Captain"
	}

	switch reminderType {
	case "game_day":
		subject = "GMCL Captain's Report — Please Submit Today"
		body = fmt.Sprintf(`Dear %s,

Thank you for playing today (%s) for %s — %s.

Please take a few minutes to submit your captain's report using the secure link below. Your report covers the pitch, outfield, and facilities at today's ground.

Submit your report:
%s

This link is valid until Wednesday at midnight. Reports submitted after this deadline will not be accepted and may result in a yellow card being issued to your team.

If you have any questions, please contact the league office.

Kind regards,
Greater Manchester Cricket League`, firstName, matchDate, clubName, teamName, link)

	case "monday":
		subject = "GMCL Captain's Report — Reminder"
		body = fmt.Sprintf(`Dear %s,

This is a reminder that your captain's report for %s — %s (match played %s) has not yet been submitted.

Please submit your report using the link below. The deadline is Wednesday at midnight.

Submit your report:
%s

If you have already submitted your report, please disregard this message.

Kind regards,
Greater Manchester Cricket League`, firstName, clubName, teamName, matchDate, link)

	case "wednesday":
		subject = "GMCL Captain's Report — Final Reminder (Deadline Tonight)"
		body = fmt.Sprintf(`Dear %s,

This is a final reminder that your captain's report for %s — %s (match played %s) must be submitted by tonight at midnight.

Submit your report now:
%s

Failure to submit before midnight tonight will result in a yellow card being issued to your team.

Kind regards,
Greater Manchester Cricket League`, firstName, clubName, teamName, matchDate, link)
	}

	return subject, body
}

// buildSanctionEmail returns subject and plain-text body for a yellow or red card letter.
func buildSanctionEmail(colour, clubName, teamName, matchDate string, pointsDeduction int) (subject, body string) {
	if colour == "yellow" {
		subject = fmt.Sprintf("GMCL Notice of Yellow Card — %s, %s", clubName, teamName)
		body = fmt.Sprintf(`Dear %s,

We are writing to inform you that a yellow card has been issued to %s for the match played on %s.

Reason: Non-submission of captain's report.

The captain's report deadline is Wednesday at 23:59 following each weekend fixture. All teams are required to ensure their report is submitted on time.

This is a formal warning. A further failure to submit a captain's report may result in a red card and a points deduction being applied to your team's league record.

If you believe this notice has been issued in error, please contact the GMCL Discipline Committee in writing within seven days of this notice.

Yours sincerely,

Greater Manchester Cricket League
Discipline Committee`, clubName, teamName, matchDate)

	} else {
		pointsText := ""
		if pointsDeduction > 0 {
			pointsText = fmt.Sprintf("\n\nAs a result of this red card, a points deduction of %d point(s) has been applied to %s's league record.", pointsDeduction, teamName)
		}
		subject = fmt.Sprintf("GMCL Notice of Red Card — %s, %s", clubName, teamName)
		body = fmt.Sprintf(`Dear %s,

We are writing to inform you that a red card has been issued to %s for the match played on %s.

Reason: Repeated non-submission of captain's report.%s

This represents a serious breach of league regulations. Further non-compliance may result in additional penalties, including a points deduction or potential exclusion from the league.

If you wish to appeal this decision, you must do so in writing within seven days of this notice, addressed to the GMCL Discipline Committee.

Yours sincerely,

Greater Manchester Cricket League
Discipline Committee`, clubName, teamName, matchDate, pointsText)
	}

	return subject, body
}
