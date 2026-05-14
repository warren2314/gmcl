package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

type generateSanctionsRequest struct {
	WeekendStart string `json:"weekend_start"` // Saturday YYYY-MM-DD
	MatchDate    string `json:"match_date"`    // any date; resolves to that date's latest weekend
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
		sat, sun := mostRecentWeekendDates(time.Now(), s.LondonLoc)

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
			matchDates = []time.Time{sat, sun}
		case "wednesday":
			// Wednesday 10:00 → last Saturday and Sunday
			matchDates = []time.Time{sat, sun}
		default:
			http.Error(w, "unknown type", http.StatusBadRequest)
			return
		}

		sent := 0
		skipped := 0

		for _, matchDate := range matchDates {
			n, sk, err := s.sendRemindersForDate(ctx, mailer, matchDate, req.Type)
			if err != nil {
				log.Printf("[reminders] date=%s error: %v", matchDate.Format("2006-01-02"), err)
				http.Error(w, "error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			log.Printf("[reminders] date=%s sent=%d skipped=%d", matchDate.Format("2006-01-02"), n, sk)
			sent += n
			skipped += sk
		}

		log.Printf("[reminders] type=%s sent=%d skipped=%d", req.Type, sent, skipped)
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
	seasonID, weekID, err := s.reminderWeekForDate(ctx, matchDate)
	if err != nil {
		return 0, 0, err
	}

	type captainTarget struct {
		TeamID     int32
		CaptainID  int32
		FullName   string
		Email      string
		ClubName   string
		TeamName   string
		Opposition string
		IsHome     bool
	}

	rows, err := s.DB.Query(ctx, `
		SELECT DISTINCT ON (t.id)
		    t.id        AS team_id,
		    c.id        AS captain_id,
		    c.full_name,
		    c.email,
		    cl.name     AS club_name,
		    t.name      AS team_name,
		    CASE WHEN TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
		         THEN COALESCE(TRIM(lf.away_club_name),'') || ' ' || COALESCE(TRIM(lf.away_team_name),'')
		         ELSE COALESCE(TRIM(lf.home_club_name),'') || ' ' || COALESCE(TRIM(lf.home_team_name),'')
		    END         AS opposition,
		    TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id) AS is_home
		FROM league_fixtures lf
		JOIN teams t ON (TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
		              OR TRIM(t.play_cricket_team_id) = TRIM(lf.away_team_pc_id))
		JOIN clubs cl ON cl.id = t.club_id
		JOIN captains c ON c.team_id = t.id
		    AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
		    AND TRIM(c.email) <> ''
		WHERE lf.match_date = $1
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
		if e := rows.Scan(&ct.TeamID, &ct.CaptainID,
			&ct.FullName, &ct.Email, &ct.ClubName, &ct.TeamName,
			&ct.Opposition, &ct.IsHome); e != nil {
			return 0, 0, e
		}
		ct.Opposition = strings.TrimSpace(ct.Opposition)
		targets = append(targets, ct)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	// Wednesday 23:59 of the week as magic link expiry
	weekExpiry := nextWednesdayMidnight(matchDate)

	for _, ct := range targets {
		token, err := auth.GenerateAndStoreMagicTokenForDate(
			ctx, s.DB, ct.CaptainID, seasonID, weekID, matchDate, weekExpiry, "", "n8n-reminder",
		)
		if err != nil {
			skipped++
			continue
		}

		link := "https://gmcl.co.uk/magic-link/confirm?token=" + token
		subject, body := buildReminderEmail(reminderType, ct.FullName, ct.ClubName, ct.TeamName, dateStr, ct.Opposition, ct.IsHome, link)

		if err := mailer.Send(ct.Email, subject, body); err != nil {
			skipped++
			continue
		}

		_, _ = s.DB.Exec(ctx, `
			INSERT INTO captain_reminder_log (team_id, week_id, match_date, reminder_type, captain_email)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (team_id, match_date, reminder_type) DO NOTHING
		`, ct.TeamID, weekID, matchDate, reminderType, ct.Email)

		sent++
	}

	return sent, skipped, nil
}

func (s *Server) reminderWeekForDate(ctx context.Context, matchDate time.Time) (seasonID, weekID int32, err error) {
	err = s.DB.QueryRow(ctx, `
		SELECT w.season_id, w.id
		FROM weeks w
		JOIN seasons se ON se.id = w.season_id
		WHERE $1::date BETWEEN w.start_date AND w.end_date
		ORDER BY se.is_archived ASC, se.start_date DESC, w.week_number DESC
		LIMIT 1
	`, matchDate.Format("2006-01-02")).Scan(&seasonID, &weekID)
	if err != nil {
		return 0, 0, fmt.Errorf("no week found covering %s; generate weeks from Play-Cricket fixtures first", matchDate.Format("2006-01-02"))
	}
	return seasonID, weekID, nil
}

// handleInternalGenerateSanctions runs at Wed 23:59 to auto-draft sanction notices
// for all teams that played last weekend and haven't submitted.
func (s *Server) handleInternalGenerateSanctions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		sat, sun, targetSource, err := resolveGenerateSanctionsDates(r, s.LondonLoc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
		JOIN teams t ON (TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
			              OR TRIM(t.play_cricket_team_id) = TRIM(lf.away_team_pc_id))
			JOIN clubs cl ON cl.id = t.club_id
			JOIN weeks w ON lf.match_date BETWEEN w.start_date AND w.end_date
			WHERE lf.match_date IN ($1, $2)
			  AND t.active = TRUE
			  AND NOT lf.is_bye
			  AND NOT EXISTS (
			      SELECT 1 FROM submissions s
			      WHERE s.team_id = t.id AND s.match_date = lf.match_date
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
			"status":        "ok",
			"drafted":       drafted,
			"target_dates":  []string{sat.Format("2006-01-02"), sun.Format("2006-01-02")},
			"target_source": targetSource,
		})
	}
}

func resolveGenerateSanctionsDates(r *http.Request, loc *time.Location) (time.Time, time.Time, string, error) {
	weekendStart := strings.TrimSpace(r.URL.Query().Get("weekend_start"))
	matchDate := strings.TrimSpace(r.URL.Query().Get("match_date"))

	if weekendStart == "" && matchDate == "" && r.Body != nil {
		data, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("could not read request body")
		}
		if strings.TrimSpace(string(data)) != "" {
			var req generateSanctionsRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return time.Time{}, time.Time{}, "", fmt.Errorf("invalid JSON body")
			}
			weekendStart = strings.TrimSpace(req.WeekendStart)
			matchDate = strings.TrimSpace(req.MatchDate)
		}
	}

	if weekendStart != "" {
		sat, sun, err := weekendStartingOn(weekendStart, loc)
		if err != nil {
			return time.Time{}, time.Time{}, "", err
		}
		return sat, sun, "weekend_start", nil
	}
	if matchDate != "" {
		d, err := parseLocalDate(matchDate, loc)
		if err != nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("invalid match_date; use YYYY-MM-DD")
		}
		sat, sun := weekendForLocalDate(d, loc)
		return sat, sun, "match_date", nil
	}

	sat, sun := mostRecentWeekendDates(time.Now(), loc)
	return sat, sun, "default", nil
}

func weekendStartingOn(dateStr string, loc *time.Location) (time.Time, time.Time, error) {
	sat, err := parseLocalDate(dateStr, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid weekend_start; use YYYY-MM-DD")
	}
	if sat.Weekday() != time.Saturday {
		return time.Time{}, time.Time{}, fmt.Errorf("weekend_start must be a Saturday")
	}
	return dateOnlyUTC(sat), dateOnlyUTC(sat.AddDate(0, 0, 1)), nil
}

func parseLocalDate(dateStr string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	d, err := time.ParseInLocation("2006-01-02", dateStr, loc)
	if err != nil {
		return time.Time{}, err
	}
	y, m, day := d.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, loc), nil
}

func weekendForLocalDate(d time.Time, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	local := d.In(loc)
	y, m, day := local.Date()
	date := time.Date(y, m, day, 0, 0, 0, 0, loc)
	switch date.Weekday() {
	case time.Saturday:
		return dateOnlyUTC(date), dateOnlyUTC(date.AddDate(0, 0, 1))
	case time.Sunday:
		sat := date.AddDate(0, 0, -1)
		return dateOnlyUTC(sat), dateOnlyUTC(date)
	default:
		return mostRecentWeekendDates(date, loc)
	}
}

func dateOnlyUTC(d time.Time) time.Time {
	y, m, day := d.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

func mostRecentWeekendDates(now time.Time, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	y, m, d := local.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	sunday := today.AddDate(0, 0, -int(today.Weekday()))
	saturday := sunday.AddDate(0, 0, -1)

	sy, sm, sd := saturday.Date()
	uy, um, ud := sunday.Date()
	return time.Date(sy, sm, sd, 0, 0, 0, 0, time.UTC), time.Date(uy, um, ud, 0, 0, 0, 0, time.UTC)
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

// handleInternalRefreshUmpirePrefills is called by n8n every Friday.
// It re-syncs league fixtures for the upcoming weekend from Play-Cricket, then
// updates any existing drafts for the current week with the latest umpire names.
func (s *Server) handleInternalRefreshUmpirePrefills() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()

		// Find the active or next week's date range.
		var weekID, seasonID int32
		var weekStart, weekEnd time.Time
		err := s.DB.QueryRow(ctx, `
			WITH active AS (
				SELECT w.id, w.season_id, w.start_date, w.end_date, 1 AS p
				FROM weeks w
				WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date
				LIMIT 1
			), upcoming AS (
				SELECT w.id, w.season_id, w.start_date, w.end_date, 2 AS p
				FROM weeks w
				WHERE w.start_date > CURRENT_DATE
				ORDER BY w.start_date LIMIT 1
			)
			SELECT id, season_id, start_date, end_date
			FROM (SELECT * FROM active UNION ALL SELECT * FROM upcoming) x
			ORDER BY p LIMIT 1
		`).Scan(&weekID, &seasonID, &weekStart, &weekEnd)
		if err != nil {
			http.Error(w, "no active week found", http.StatusInternalServerError)
			return
		}

		// Collect Sat/Sun dates within the week range.
		var matchDates []time.Time
		for d := weekStart; !d.After(weekEnd); d = d.AddDate(0, 0, 1) {
			if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
				matchDates = append(matchDates, d)
			}
		}

		cfg := leagueapi.NewConfigFromEnv()
		client := leagueapi.NewClient(cfg)

		// Fetch per match date (so a {date}-filtered URL template works correctly).
		// Deduplicate by match ID before upserting to avoid double-counting when the
		// API returns all-season fixtures regardless of date.
		seen := make(map[string]leagueapi.MatchDetail)
		for _, md := range matchDates {
			details, err := client.FetchMatchesForDate(ctx, md)
			if err != nil {
				log.Printf("[umpire-refresh] fetch failed for %s: %v", md.Format("2006-01-02"), err)
				continue
			}
			for _, d := range details {
				seen[d.MatchID] = d
			}
		}
		unique := make([]leagueapi.MatchDetail, 0, len(seen))
		for _, d := range seen {
			unique = append(unique, d)
		}
		synced := 0
		if len(unique) > 0 {
			if err := leagueapi.UpsertFixtureBatch(ctx, s.DB, &seasonID, unique); err != nil {
				log.Printf("[umpire-refresh] upsert failed: %v", err)
			} else {
				synced = len(unique)
			}
		}
		log.Printf("[umpire-refresh] synced %d fixtures for week %d", synced, weekID)

		// Update drafts for this week with the latest umpire data from league_fixtures.
		rows, err := s.DB.Query(ctx, `
			SELECT d.id, d.team_id, d.draft_data
			FROM drafts d
			WHERE d.season_id = $1 AND d.week_id = $2
		`, seasonID, weekID)
		if err != nil {
			log.Printf("[umpire-refresh] draft query failed: %v", err)
			http.Error(w, "draft query error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type draftRow struct {
			ID     int64
			TeamID int32
			Data   map[string]any
		}
		var drafts []draftRow
		for rows.Next() {
			var dr draftRow
			var raw []byte
			if err := rows.Scan(&dr.ID, &dr.TeamID, &raw); err != nil {
				continue
			}
			_ = json.Unmarshal(raw, &dr.Data)
			if dr.Data == nil {
				dr.Data = map[string]any{}
			}
			drafts = append(drafts, dr)
		}
		rows.Close()

		updated := 0
		for _, dr := range drafts {
			var u1, u2 string
			for _, md := range matchDates {
				u1, u2, _ = leagueapi.LookupUmpirePrefill(ctx, s.DB, dr.TeamID, md)
				if u1 != "" || u2 != "" {
					break
				}
			}
			if u1 == "" && u2 == "" {
				continue
			}
			if u1 != "" {
				dr.Data["umpire1_name"] = u1
			}
			if u2 != "" {
				dr.Data["umpire2_name"] = u2
			}
			payload, err := json.Marshal(dr.Data)
			if err != nil {
				continue
			}
			_, err = s.DB.Exec(ctx, `
				UPDATE drafts SET draft_data = $1, last_autosaved_at = now()
				WHERE id = $2
			`, payload, dr.ID)
			if err != nil {
				log.Printf("[umpire-refresh] draft update failed id=%d: %v", dr.ID, err)
				continue
			}
			updated++
		}
		log.Printf("[umpire-refresh] updated %d drafts with umpire data for week %d", updated, weekID)

		s.audit(ctx, r, "n8n", nil, "internal_refresh_umpire_prefills", "week", func() *int64 {
			v := int64(weekID)
			return &v
		}(), map[string]any{"synced_fixtures": synced, "updated_drafts": updated})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":          "ok",
			"synced_fixtures": synced,
			"updated_drafts":  updated,
			"week_id":         weekID,
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
			subject, body = buildReminderEmail(t, "Joe Bloggs", "Example CC", "Example CC - 1st XI", "26 April 2026", "Opponents CC 1st XI", true, link)
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
func buildReminderEmail(reminderType, captainName, clubName, teamName, matchDate, opposition string, isHome bool, link string) (subject, body string) {
	firstName := strings.SplitN(strings.TrimSpace(captainName), " ", 2)[0]
	if firstName == "" {
		firstName = "Captain"
	}

	venue := "Away"
	if isHome {
		venue = "Home"
	}
	fixtureDesc := teamName
	if opposition != "" {
		fixtureDesc = fmt.Sprintf("%s v %s (%s)", teamName, opposition, venue)
	}

	const expiredLinkNote = `If your link appears to have expired or is not working, you can access a fresh submission form directly at:

https://gmcl.co.uk

Simply select your club and team from the filters on the home page to retrieve your personalised link.

NOTE: Having trouble opening the link? If you are on a work computer or work Wi-Fi, your organisation may be blocking this site. Try opening the link on your mobile phone using mobile data instead.`

	switch reminderType {
	case "game_day":
		subject = fmt.Sprintf("GMCL Captain's Report — %s, %s", fixtureDesc, matchDate)
		body = fmt.Sprintf(`Dear %s,

Thank you for playing today (%s) for %s — %s.

Please take a few minutes to submit your captain's report using the secure link below. Your report covers the pitch, outfield, and facilities at today's ground.

Submit your report:
%s

This link is valid until Wednesday at midnight. Reports submitted after this deadline will not be accepted and may result in a yellow card being issued to your team.

%s

If you have any questions, please contact the league office.

Kind regards,
Greater Manchester Cricket League`, firstName, matchDate, clubName, teamName, link, expiredLinkNote)

	case "monday":
		subject = fmt.Sprintf("GMCL Captain's Report — Reminder: %s, %s", fixtureDesc, matchDate)
		body = fmt.Sprintf(`Dear %s,

This is a reminder that your captain's report for %s — %s (match played %s) has not yet been submitted.

Please submit your report using the link below. The deadline is Wednesday at midnight.

Submit your report:
%s

%s

If you have already submitted your report, please disregard this message.

Kind regards,
Greater Manchester Cricket League`, firstName, clubName, teamName, matchDate, link, expiredLinkNote)

	case "wednesday":
		subject = fmt.Sprintf("GMCL Captain's Report — Final Reminder: %s, %s", fixtureDesc, matchDate)
		body = fmt.Sprintf(`Dear %s,

This is a final reminder that your captain's report for %s — %s (match played %s) must be submitted by tonight at midnight.

Submit your report now:
%s

%s

Failure to submit before midnight tonight will result in a yellow card being issued to your team.

Kind regards,
Greater Manchester Cricket League`, firstName, clubName, teamName, matchDate, link, expiredLinkNote)
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
