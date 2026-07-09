package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func (s *Server) generateAIExecutiveReport(ctx context.Context, reportID int64, seasonID int32, weekID *int32, period string) {
	if weekID == nil {
		s.DB.Exec(ctx, `UPDATE generated_reports SET status='error', error_message=$1, completed_at=now() WHERE id=$2`,
			"no completed week is available for AI executive report", reportID)
		return
	}

	var seasonName string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM seasons WHERE id=$1`, seasonID).Scan(&seasonName)

	var weekNum int32
	var weekStart, weekEnd time.Time
	if err := s.DB.QueryRow(ctx, `SELECT week_number, start_date, end_date FROM weeks WHERE id=$1`, *weekID).
		Scan(&weekNum, &weekStart, &weekEnd); err != nil {
		s.DB.Exec(ctx, `UPDATE generated_reports SET status='error', error_message=$1, completed_at=now() WHERE id=$2`,
			err.Error(), reportID)
		return
	}

	rp := aiExecutiveReportPayload{
		SeasonID:     seasonID,
		SeasonName:   seasonName,
		ReportType:   "ai_executive",
		ReportPeriod: period,
		GeneratedAt:  time.Now(),
		Cover: aiExecutiveCover{
			Title:        "GMCL Executive Report",
			LatestPeriod: fmt.Sprintf("Week %d: %s to %s", weekNum, weekStart.Format("2 Jan 2006"), weekEnd.Format("2 Jan 2006")),
			SeasonPeriod: fmt.Sprintf("%s to %s", seasonName, weekEnd.Format("2 Jan 2006")),
			PreparedFor:  "GMCL Executive Committee",
		},
		TableOfContents: []string{
			"Executive Summary",
			"Latest Report",
			"Season Report",
			"Latest Umpire Reports",
			"Season Umpire Report",
			"Conclusion",
		},
	}

	latestExpected, latestSubmitted, latestMissing := s.loadAIWeeklyCompliance(ctx, *weekID, seasonID, weekStart, weekEnd)
	seasonExpected, seasonClosed := s.loadAIClosedExpectedReports(ctx, seasonID, time.Time{}, weekEnd)

	rp.Latest = s.loadAIExecutiveWindow(ctx, "Latest Report", rp.Cover.LatestPeriod,
		"sub.season_id=$1 AND sub.week_id=$2", []any{seasonID, *weekID}, latestExpected, &seasonID, weekID)
	applyAIExecutiveCompliance(&rp.Latest, latestExpected, latestSubmitted)
	rp.SeasonToDate = s.loadAIExecutiveWindow(ctx, "Season Report", rp.Cover.SeasonPeriod,
		"sub.season_id=$1 AND w.end_date <= $2", []any{seasonID, weekEnd}, seasonExpected, &seasonID, nil)
	applyAIExecutiveCompliance(&rp.SeasonToDate, seasonExpected, seasonClosed)
	rp.SeasonToDate.WeeklyTrend = s.loadAIExecutiveWeeklyTrend(ctx, seasonID, weekEnd)
	rp.Latest.MissingTeams = latestMissing
	rp.LatestUmpires = s.loadAIExecutiveUmpireWindow(ctx, "Latest Umpire Reports", rp.Cover.LatestPeriod,
		"sub.season_id=$1 AND sub.week_id=$2", []any{seasonID, *weekID}, 1)
	rp.SeasonUmpires = s.loadAIExecutiveUmpireWindow(ctx, "Season Umpire Report", rp.Cover.SeasonPeriod,
		"sub.season_id=$1 AND w.end_date <= $2", []any{seasonID, weekEnd}, 2)

	narrative, model, err := s.callOpenAIExecutiveNarrative(ctx, rp)
	if err != nil {
		rp.Executive = buildFallbackExecutiveNarrative(rp)
		rp.AIError = err.Error()
	} else {
		rp.Executive = narrative
		rp.GeneratedByAI = true
		rp.AIModel = model
	}

	payload, err := json.Marshal(rp)
	if err != nil {
		s.DB.Exec(ctx, `UPDATE generated_reports SET status='error', error_message=$1, completed_at=now() WHERE id=$2`,
			err.Error(), reportID)
		return
	}
	s.DB.Exec(ctx, `UPDATE generated_reports SET status='ready', payload_json=$1, completed_at=now() WHERE id=$2`, payload, reportID)
}

func (s *Server) generateAISeasonToDateReport(ctx context.Context, reportID int64, seasonID int32, asOfDate time.Time, period string) {
	var seasonName string
	var seasonStart, seasonEnd time.Time
	if err := s.DB.QueryRow(ctx, `SELECT name, start_date, end_date FROM seasons WHERE id=$1`, seasonID).
		Scan(&seasonName, &seasonStart, &seasonEnd); err != nil {
		s.DB.Exec(ctx, `UPDATE generated_reports SET status='error', error_message=$1, completed_at=now() WHERE id=$2`,
			err.Error(), reportID)
		return
	}
	seasonStart = dateOnlyInLocation(seasonStart, s.LondonLoc)
	seasonEnd = dateOnlyInLocation(seasonEnd, s.LondonLoc)
	asOfDate = dateOnlyInLocation(asOfDate, s.LondonLoc)
	if asOfDate.After(seasonEnd) {
		asOfDate = seasonEnd
	}

	var weekID int32
	var weekNum int32
	var weekStart, weekEnd time.Time
	err := s.DB.QueryRow(ctx, `
		SELECT id, week_number, start_date, end_date
		FROM weeks
		WHERE season_id=$1 AND start_date <= $2
		ORDER BY
		    CASE WHEN $2 BETWEEN start_date AND end_date THEN 0 ELSE 1 END,
		    end_date DESC,
		    week_number DESC
		LIMIT 1
	`, seasonID, asOfDate).Scan(&weekID, &weekNum, &weekStart, &weekEnd)
	if err != nil {
		s.DB.Exec(ctx, `UPDATE generated_reports SET status='error', error_message=$1, completed_at=now() WHERE id=$2`,
			"no season week is available for the selected as-of date", reportID)
		return
	}

	currentEnd := asOfDate
	if currentEnd.After(weekEnd) {
		currentEnd = weekEnd
	}
	if currentEnd.Before(weekStart) {
		currentEnd = weekStart
	}

	rp := aiExecutiveReportPayload{
		SeasonID:     seasonID,
		SeasonName:   seasonName,
		ReportType:   "ai_season_to_date",
		ReportPeriod: period,
		GeneratedAt:  time.Now(),
		Cover: aiExecutiveCover{
			Title:        "GMCL Season-to-Date Report",
			LatestPeriod: fmt.Sprintf("Week %d to date: %s to %s", weekNum, weekStart.Format("2 Jan 2006"), currentEnd.Format("2 Jan 2006")),
			SeasonPeriod: fmt.Sprintf("%s: %s to %s", seasonName, seasonStart.Format("2 Jan 2006"), asOfDate.Format("2 Jan 2006")),
			PreparedFor:  "GMCL Executive Committee",
		},
		TableOfContents: []string{
			"Executive Summary",
			"Current Period To Date",
			"Season To Date",
			"Current Umpire Feedback",
			"Season Umpire Feedback",
			"Conclusion",
		},
	}

	latestExpected, latestSubmitted, latestMissing := s.loadAIWeeklyCompliance(ctx, weekID, seasonID, weekStart, currentEnd)
	seasonExpected, seasonClosed := s.loadAIClosedExpectedReports(ctx, seasonID, seasonStart, asOfDate)

	rp.Latest = s.loadAIExecutiveWindow(ctx, "Current Period To Date", rp.Cover.LatestPeriod,
		"sub.season_id=$1 AND sub.week_id=$2 AND sub.match_date <= $3", []any{seasonID, weekID, currentEnd}, latestExpected, &seasonID, &weekID)
	applyAIExecutiveCompliance(&rp.Latest, latestExpected, latestSubmitted)
	rp.Latest.MissingTeams = latestMissing
	rp.Latest.SanctionsIssued = s.loadAIReportSanctionsIssued(ctx, seasonID, &weekID, &currentEnd)

	rp.SeasonToDate = s.loadAIExecutiveWindow(ctx, "Season To Date", rp.Cover.SeasonPeriod,
		"sub.season_id=$1 AND sub.match_date BETWEEN $2 AND $3", []any{seasonID, seasonStart, asOfDate}, seasonExpected, &seasonID, nil)
	applyAIExecutiveCompliance(&rp.SeasonToDate, seasonExpected, seasonClosed)
	rp.SeasonToDate.WeeklyTrend = s.loadAIExecutiveWeeklyTrendToDate(ctx, seasonID, asOfDate)
	rp.SeasonToDate.SanctionsIssued = s.loadAIReportSanctionsIssued(ctx, seasonID, nil, &asOfDate)

	rp.LatestUmpires = s.loadAIExecutiveUmpireWindow(ctx, "Current Umpire Feedback", rp.Cover.LatestPeriod,
		"sub.season_id=$1 AND sub.week_id=$2 AND sub.match_date <= $3", []any{seasonID, weekID, currentEnd}, 1)
	rp.SeasonUmpires = s.loadAIExecutiveUmpireWindow(ctx, "Season Umpire Feedback", rp.Cover.SeasonPeriod,
		"sub.season_id=$1 AND sub.match_date BETWEEN $2 AND $3", []any{seasonID, seasonStart, asOfDate}, 2)

	narrative, model, err := s.callOpenAIExecutiveNarrative(ctx, rp)
	if err != nil {
		rp.Executive = buildFallbackExecutiveNarrative(rp)
		rp.AIError = err.Error()
	} else {
		rp.Executive = narrative
		rp.GeneratedByAI = true
		rp.AIModel = model
	}

	payload, err := json.Marshal(rp)
	if err != nil {
		s.DB.Exec(ctx, `UPDATE generated_reports SET status='error', error_message=$1, completed_at=now() WHERE id=$2`,
			err.Error(), reportID)
		return
	}
	s.DB.Exec(ctx, `UPDATE generated_reports SET status='ready', payload_json=$1, completed_at=now() WHERE id=$2`, payload, reportID)
}

func (s *Server) loadAIExecutiveWindow(ctx context.Context, title, period, whereSQL string, args []any, expected int64, seasonID *int32, weekID *int32) aiExecutiveWindow {
	win := aiExecutiveWindow{Title: title, Period: period, SubmissionsExpected: expected}
	_ = s.DB.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(DISTINCT (sub.team_id, sub.week_id)),
		       COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'unevenness_of_bounce')::numeric)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'seam_movement')::numeric)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'carry_bounce')::numeric)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'turn')::numeric)::numeric,2),0)
		FROM submissions sub
		JOIN weeks w ON w.id=sub.week_id
		WHERE %s
	`, whereSQL), args...).Scan(&win.SubmissionsReceived, &win.AvgPitch, &win.AvgBounce, &win.AvgSeam, &win.AvgCarry, &win.AvgTurn)

	if expected > 0 {
		win.ComplianceRate = float64(win.SubmissionsReceived) / float64(expected) * 100
		if win.ComplianceRate > 100 {
			win.ComplianceRate = 100
		}
	}
	if seasonID != nil {
		win.SanctionsIssued = s.loadAIReportSanctionsIssued(ctx, *seasonID, weekID, nil)
	}
	win.TopClubs = s.loadAIExecutiveClubRows(ctx, whereSQL, args, "DESC")
	win.ConcernClubs = s.loadAIExecutiveClubRows(ctx, whereSQL, args, "ASC")
	win.RepresentativeNotes = s.loadAIExecutiveNotes(ctx, whereSQL, args, "comments")
	return win
}

func applyAIExecutiveCompliance(win *aiExecutiveWindow, expected, submitted int64) {
	win.SubmissionsExpected = expected
	win.SubmissionsReceived = submitted
	win.ComplianceRate = 0
	if expected <= 0 {
		return
	}
	win.ComplianceRate = float64(submitted) / float64(expected) * 100
	if win.ComplianceRate > 100 {
		win.ComplianceRate = 100
	}
}

func (s *Server) loadAIReportSanctionsIssued(ctx context.Context, seasonID int32, weekID *int32, through *time.Time) int64 {
	where := "season_id=$1 AND status <> 'overturned'"
	args := []any{seasonID}
	if weekID != nil {
		where += fmt.Sprintf(" AND week_id=$%d", len(args)+1)
		args = append(args, *weekID)
	}
	if through != nil && !through.IsZero() {
		where += fmt.Sprintf(" AND issued_at::date <= $%d", len(args)+1)
		args = append(args, through.Format("2006-01-02"))
	}

	var count int64
	_ = s.DB.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM sanctions WHERE %s`, where), args...).Scan(&count)
	return count
}

func (s *Server) loadAIExecutiveClubRows(ctx context.Context, whereSQL string, args []any, direction string) []aiExecutiveClubRow {
	if direction != "ASC" {
		direction = "DESC"
	}
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`
		SELECT COALESCE(hc.name, cl.name) AS club_name,
		       COUNT(*) AS reports,
		       COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'unevenness_of_bounce')::numeric)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'seam_movement')::numeric)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'carry_bounce')::numeric)::numeric,2),0),
		       COALESCE(ROUND(AVG((sub.form_data->>'turn')::numeric)::numeric,2),0)
		FROM submissions sub
		JOIN weeks w ON w.id=sub.week_id
		JOIN teams t ON t.id=sub.team_id
		JOIN clubs cl ON cl.id=t.club_id
		LEFT JOIN clubs hc ON hc.id=sub.home_club_id
		WHERE %s
		GROUP BY COALESCE(hc.name, cl.name)
		ORDER BY COALESCE(AVG(sub.pitch_rating),0) %s, reports DESC, club_name
		LIMIT 10
	`, whereSQL, direction), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []aiExecutiveClubRow
	for rows.Next() {
		var row aiExecutiveClubRow
		if rows.Scan(&row.Club, &row.Reports, &row.AvgPitch, &row.AvgBounce, &row.AvgSeam, &row.AvgCarry, &row.AvgTurn) == nil {
			result = append(result, row)
		}
	}
	return result
}

func (s *Server) loadAIExecutiveNotes(ctx context.Context, whereSQL string, args []any, field string) []string {
	expr := "NULLIF(trim(sub.comments), '')"
	if field == "umpires" {
		expr = "NULLIF(trim(COALESCE(sub.form_data->>'umpire_comments', '')), '')"
	}
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM submissions sub
		JOIN weeks w ON w.id=sub.week_id
		WHERE %s AND %s IS NOT NULL
		ORDER BY sub.submitted_at DESC
		LIMIT 8
	`, expr, whereSQL, expr), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var notes []string
	for rows.Next() {
		var note string
		if rows.Scan(&note) == nil {
			notes = append(notes, truncateForReport(note, 280))
		}
	}
	return notes
}

func (s *Server) loadAIExecutiveWeeklyTrend(ctx context.Context, seasonID int32, weekEnd time.Time) []reportWeekTrend {
	rows, err := s.DB.Query(ctx, `
		SELECT w.week_number, COUNT(DISTINCT sub.team_id), COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0)
		FROM weeks w
		LEFT JOIN submissions sub ON sub.week_id=w.id AND sub.season_id=$1
		WHERE w.season_id=$1 AND w.end_date <= $2
		GROUP BY w.week_number ORDER BY w.week_number
	`, seasonID, weekEnd)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var trend []reportWeekTrend
	for rows.Next() {
		var row reportWeekTrend
		if rows.Scan(&row.WeekNumber, &row.Subs, &row.AvgPitch) == nil {
			trend = append(trend, row)
		}
	}
	return trend
}

func (s *Server) loadAIExecutiveWeeklyTrendToDate(ctx context.Context, seasonID int32, asOfDate time.Time) []reportWeekTrend {
	rows, err := s.DB.Query(ctx, `
		SELECT w.week_number, COUNT(DISTINCT sub.team_id), COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0)
		FROM weeks w
		LEFT JOIN submissions sub ON sub.week_id=w.id AND sub.season_id=$1 AND sub.match_date <= $2
		WHERE w.season_id=$1 AND w.start_date <= $2
		GROUP BY w.week_number ORDER BY w.week_number
	`, seasonID, asOfDate)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var trend []reportWeekTrend
	for rows.Next() {
		var row reportWeekTrend
		if rows.Scan(&row.WeekNumber, &row.Subs, &row.AvgPitch) == nil {
			trend = append(trend, row)
		}
	}
	return trend
}

func (s *Server) loadAIExpectedReports(ctx context.Context, seasonID int32, startDate, endDate time.Time) int64 {
	if endDate.IsZero() {
		return 0
	}

	where := `
		lf.season_id=$1
		AND lf.match_date <= $2
		AND EXTRACT(DOW FROM lf.match_date) <> 5
		AND NOT lf.is_bye
		AND t.active=TRUE
		AND t.play_cricket_team_id IS NOT NULL
		AND TRIM(t.play_cricket_team_id) <> ''
	`
	args := []any{seasonID, endDate}
	if !startDate.IsZero() {
		where += ` AND lf.match_date >= $3`
		args = append(args, startDate)
	}

	var expected int64
	_ = s.DB.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*)
		FROM league_fixtures lf
		JOIN teams t ON (
		    TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id) OR
		    TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
		)
		WHERE %s
	`, where), args...).Scan(&expected)
	return expected
}

func (s *Server) loadAIClosedExpectedReports(ctx context.Context, seasonID int32, startDate, endDate time.Time) (int64, int64) {
	if endDate.IsZero() {
		return 0, 0
	}

	var startArg any
	if !startDate.IsZero() {
		startArg = startDate.Format("2006-01-02")
	}

	var expected, closed int64
	err := s.DB.QueryRow(ctx, `
		WITH expected_fixtures AS (
		    SELECT t.id AS team_id,
		           lf.play_cricket_match_id,
		           lf.match_date,
		           ROW_NUMBER() OVER (
		               PARTITION BY t.id, lf.match_date
		               ORDER BY lf.play_cricket_match_id
		           ) AS fixture_ordinal
		    FROM teams t
		    JOIN league_fixtures lf ON (
		        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id) OR
		        TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
		    )
		    WHERE t.active=TRUE
		      AND t.play_cricket_team_id IS NOT NULL
		      AND TRIM(t.play_cricket_team_id) <> ''
		      AND lf.season_id=$1
		      AND lf.match_date <= $2
		      AND ($3::date IS NULL OR lf.match_date >= $3)
		      AND EXTRACT(DOW FROM lf.match_date) <> 5
		      AND NOT lf.is_bye
		),
		legacy_submissions AS (
		    SELECT team_id,
		           match_date,
		           COUNT(*) AS legacy_count
		    FROM submissions
		    WHERE season_id=$1
		      AND play_cricket_match_id IS NULL
		      AND match_date <= $2
		      AND ($3::date IS NULL OR match_date >= $3)
		    GROUP BY team_id, match_date
		)
		SELECT COUNT(*) AS expected,
		       COUNT(*) FILTER (
		           WHERE EXISTS (
		               SELECT 1 FROM submissions sub
		               WHERE sub.season_id=$1
		                 AND sub.team_id=ef.team_id
		                 AND sub.match_date=ef.match_date
		                 AND sub.play_cricket_match_id=ef.play_cricket_match_id
		           )
		           OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
		           OR EXISTS (
		               SELECT 1 FROM report_exemptions re
		               WHERE re.season_id=$1
		                 AND re.team_id=ef.team_id
		                 AND re.match_date=ef.match_date
		                 AND (
		                     re.play_cricket_match_id=ef.play_cricket_match_id
		                     OR re.play_cricket_match_id IS NULL
		                 )
		           )
		       ) AS closed
		FROM expected_fixtures ef
		LEFT JOIN legacy_submissions ls
		  ON ls.team_id=ef.team_id AND ls.match_date=ef.match_date
	`, seasonID, endDate.Format("2006-01-02"), startArg).Scan(&expected, &closed)
	if err != nil {
		return 0, 0
	}
	return expected, closed
}

func (s *Server) loadAIWeeklyCompliance(ctx context.Context, weekID, seasonID int32, weekStart, weekEnd time.Time) (int64, int64, []reportMissing) {
	rows, err := s.DB.Query(ctx, `
		WITH expected_fixtures AS (
		    SELECT t.id AS team_id,
		           lf.play_cricket_match_id,
		           lf.match_date,
		           ROW_NUMBER() OVER (
		               PARTITION BY t.id, lf.match_date
		               ORDER BY lf.play_cricket_match_id
		           ) AS fixture_ordinal
		    FROM teams t
		    JOIN league_fixtures lf ON (
		        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id) OR
		        TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
		    )
		    WHERE t.active=TRUE
		      AND t.play_cricket_team_id IS NOT NULL
		      AND TRIM(t.play_cricket_team_id) <> ''
		      AND lf.match_date BETWEEN $2 AND $3
		      AND EXTRACT(DOW FROM lf.match_date) <> 5
		      AND NOT lf.is_bye
		),
		legacy_submissions AS (
		    SELECT team_id,
		           match_date,
		           COUNT(*) AS legacy_count
		    FROM submissions
		    WHERE week_id=$1 AND play_cricket_match_id IS NULL
		    GROUP BY team_id, match_date
		),
		fixture_counts AS (
		    SELECT ef.team_id,
		           COUNT(*) AS fixture_count,
		           COUNT(*) FILTER (
		               WHERE EXISTS (
		                   SELECT 1 FROM submissions sub
		                   WHERE sub.week_id=$1
		                     AND sub.team_id=ef.team_id
		                     AND sub.play_cricket_match_id=ef.play_cricket_match_id
		               )
		               OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
		               OR EXISTS (
		                   SELECT 1 FROM report_exemptions re
		                   WHERE re.week_id=$1
		                     AND re.team_id=ef.team_id
		                     AND re.match_date=ef.match_date
		                     AND (
		                         re.play_cricket_match_id=ef.play_cricket_match_id
		                         OR re.play_cricket_match_id IS NULL
		                     )
		               )
		           ) AS submit_count
		    FROM expected_fixtures ef
		    LEFT JOIN legacy_submissions ls
		      ON ls.team_id=ef.team_id AND ls.match_date=ef.match_date
		    GROUP BY ef.team_id
		)
		SELECT t.id, cl.name, t.name,
		       fc.fixture_count,
		       COALESCE(fc.submit_count, 0) AS submit_count
		FROM teams t
		JOIN clubs cl ON t.club_id=cl.id
		LEFT JOIN fixture_counts fc ON fc.team_id=t.id
		WHERE t.active=TRUE
		ORDER BY
		    CASE WHEN COALESCE(fc.fixture_count,0) = 0 THEN 2
		         WHEN COALESCE(fc.submit_count,0) >= COALESCE(fc.fixture_count,1) THEN 0
		         ELSE 1 END,
		    cl.name, t.name
	`, weekID, weekStart, weekEnd)
	if err != nil {
		return 0, 0, nil
	}
	defer rows.Close()

	var missing []reportMissing
	var expected, submitted int64
	for rows.Next() {
		var row reportMissing
		var fixtureCount, submitCount int64
		if rows.Scan(&row.TeamID, &row.ClubName, &row.TeamName, &fixtureCount, &submitCount) == nil {
			if fixtureCount <= 0 {
				continue
			}
			cappedSubmitted := submitCount
			if cappedSubmitted > fixtureCount {
				cappedSubmitted = fixtureCount
			}
			expected += fixtureCount
			submitted += cappedSubmitted
			if fixtureCount > submitCount {
				missing = append(missing, row)
			}
		}
	}
	return expected, submitted, missing
}

func (s *Server) loadAIExecutiveUmpireWindow(ctx context.Context, title, period, whereSQL string, args []any, minRatings int64) aiExecutiveUmpireWindow {
	win := aiExecutiveUmpireWindow{Title: title, Period: period}
	all := s.loadUmpireRankings(ctx, whereSQL, args, minRatings, "", "")
	for _, row := range all {
		win.TotalRatings += row.Ratings
		win.Good += row.Good
		win.Average += row.Average
		win.Poor += row.Poor
	}
	for i, row := range all {
		if i < 10 {
			win.TopUmpires = append(win.TopUmpires, row)
		}
	}
	for i := len(all) - 1; i >= 0 && len(win.ConcernUmpires) < 10; i-- {
		win.ConcernUmpires = append(win.ConcernUmpires, all[i])
	}
	win.RepresentativeNotes = s.loadAIExecutiveNotes(ctx, whereSQL, args, "umpires")
	return win
}

func (s *Server) callOpenAIExecutiveNarrative(ctx context.Context, rp aiExecutiveReportPayload) (aiExecutiveNarrative, string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return aiExecutiveNarrative{}, "", fmt.Errorf("OPENAI_API_KEY is not configured")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_REPORT_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}

	data, _ := json.Marshal(rp)
	narrativeSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"executive_summary":    map[string]any{"type": "string"},
			"latest_report":        map[string]any{"type": "string"},
			"season_report":        map[string]any{"type": "string"},
			"latest_umpire_report": map[string]any{"type": "string"},
			"season_umpire_report": map[string]any{"type": "string"},
			"conclusion":           map[string]any{"type": "string"},
		},
		"required": []string{
			"executive_summary",
			"latest_report",
			"season_report",
			"latest_umpire_report",
			"season_umpire_report",
			"conclusion",
		},
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model": model,
		"instructions": strings.Join([]string{
			"You are writing a professional executive report for a cricket league committee.",
			"Use only the supplied JSON data. Do not invent figures, clubs, umpires, dates, or trends.",
			"Write in clear British English for a board-level audience. Be direct, measured, and evidence-led.",
			"Use a concise board-paper style: short paragraphs, explicit implications, and clear recommended focus areas.",
			"When report_type is ai_season_to_date, treat latest_report as the current reporting period to the as-of date, and make season_report the main season-start-to-as-of-date analysis.",
			"Make the season_report analytical rather than static: compare latest-week performance with season-to-date, identify direction of travel from weekly_trend, name notable strongest and concern clubs or grounds, and explain what this means for executive attention.",
			"The conclusion must be more than a generic close: prioritise the next operational actions, separate immediate compliance actions from medium-term ground or umpire follow-up, and state what should be monitored next week.",
			"Use the representative_notes only as qualitative context, not as facts beyond the supplied text.",
			"Where there is limited data, say so plainly and focus on what can be concluded from the available ratings.",
			"Each value should be one or two concise paragraphs. Mention the most important numbers, but do not simply repeat KPI values.",
		}, " "),
		"input":             "Create the executive narrative sections from this report data:\n" + string(data),
		"max_output_tokens": 4000,
		"text": map[string]any{
			"format": map[string]any{
				"type":        "json_schema",
				"name":        "executive_narrative",
				"description": "Narrative sections for a GMCL executive report.",
				"strict":      true,
				"schema":      narrativeSchema,
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(reqBody))
	if err != nil {
		return aiExecutiveNarrative{}, model, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return aiExecutiveNarrative{}, model, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return aiExecutiveNarrative{}, model, fmt.Errorf("OpenAI response status %d: %s", resp.StatusCode, truncateForReport(string(body), 300))
	}
	var parsed struct {
		OutputText        string `json:"output_text"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return aiExecutiveNarrative{}, model, err
	}
	text := strings.TrimSpace(parsed.OutputText)
	if text == "" {
		var b strings.Builder
		for _, out := range parsed.Output {
			for _, c := range out.Content {
				if c.Text != "" {
					b.WriteString(c.Text)
				} else if c.Refusal != "" {
					return aiExecutiveNarrative{}, model, fmt.Errorf("OpenAI refused report narrative: %s", truncateForReport(c.Refusal, 300))
				}
			}
		}
		text = strings.TrimSpace(b.String())
	}
	if text == "" {
		reason := parsed.IncompleteDetails.Reason
		if reason == "" {
			reason = "no output text"
		}
		return aiExecutiveNarrative{}, model, fmt.Errorf("OpenAI returned no narrative text (%s; status=%s)", reason, parsed.Status)
	}
	var narrative aiExecutiveNarrative
	jsonText := extractJSONObject(text)
	if jsonText == "" {
		return aiExecutiveNarrative{}, model, fmt.Errorf("OpenAI narrative did not contain a JSON object: %s", truncateForReport(text, 300))
	}
	if err := json.Unmarshal([]byte(jsonText), &narrative); err != nil {
		return aiExecutiveNarrative{}, model, fmt.Errorf("OpenAI narrative JSON parse failed: %w; output=%s", err, truncateForReport(text, 300))
	}
	return narrative, model, nil
}

func openAIReportConfigured() bool {
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
}

func buildFallbackExecutiveNarrative(rp aiExecutiveReportPayload) aiExecutiveNarrative {
	if rp.ReportType == "ai_season_to_date" {
		return buildFallbackSeasonToDateNarrative(rp)
	}

	seasonTrend := "No weekly trend is available yet."
	if len(rp.SeasonToDate.WeeklyTrend) >= 2 {
		first := rp.SeasonToDate.WeeklyTrend[0]
		last := rp.SeasonToDate.WeeklyTrend[len(rp.SeasonToDate.WeeklyTrend)-1]
		pitchMovement := last.AvgPitch - first.AvgPitch
		direction := "broadly stable"
		if pitchMovement >= 0.25 {
			direction = "improving"
		} else if pitchMovement <= -0.25 {
			direction = "softening"
		}
		seasonTrend = fmt.Sprintf("The weekly trend is %s: average pitch rating moved from %.2f in week %d to %.2f in week %d.",
			direction, first.AvgPitch, first.WeekNumber, last.AvgPitch, last.WeekNumber)
	}
	strongest := describeExecutiveClubRows(rp.SeasonToDate.TopClubs)
	concerns := describeExecutiveClubRows(rp.SeasonToDate.ConcernClubs)
	if strongest == "" {
		strongest = "No clear strongest club or ground group is available yet"
	}
	if concerns == "" {
		concerns = "No repeated concern group is available yet"
	}

	return aiExecutiveNarrative{
		ExecutiveSummary: fmt.Sprintf("The latest completed reporting period is %s. It recorded %d reports against %d expected submissions, giving %.1f%% compliance. Season to date, the league has %d reports against %d expected submissions and an average pitch rating of %.2f. The immediate management focus is submission compliance first, then recurring ground or officiating patterns where the evidence is repeated rather than isolated.",
			rp.Latest.Period, rp.Latest.SubmissionsReceived, rp.Latest.SubmissionsExpected, rp.Latest.ComplianceRate,
			rp.SeasonToDate.SubmissionsReceived, rp.SeasonToDate.SubmissionsExpected, rp.SeasonToDate.AvgPitch),
		LatestReport: fmt.Sprintf("For the latest week, the average pitch score was %.2f, with bounce %.2f, seam %.2f, carry %.2f and turn %.2f. There were %d missing submissions and %d sanctions recorded for the period. These items should be treated as the current operational queue: chase missing returns while they are still fresh, then check whether any low-rated grounds also appear in the season concern list.",
			rp.Latest.AvgPitch, rp.Latest.AvgBounce, rp.Latest.AvgSeam, rp.Latest.AvgCarry, rp.Latest.AvgTurn, len(rp.Latest.MissingTeams), rp.Latest.SanctionsIssued),
		SeasonReport: fmt.Sprintf("Across the season to date, compliance is %.1f%% and the average pitch score is %.2f. %s Strongest rated clubs or grounds include %s, while the current concern list includes %s. This gives the committee a clearer split between isolated weekly issues and patterns that may justify direct club follow-up, ground inspection, or a request for remedial action.",
			rp.SeasonToDate.ComplianceRate, rp.SeasonToDate.AvgPitch, seasonTrend, strongest, concerns),
		LatestUmpireReport: fmt.Sprintf("The latest umpire report contains %d individual ratings: %d good, %d average and %d poor. The latest table should be used for triage rather than judgement in isolation: poor or average feedback is most useful when matched to appointment context, match conditions and any repeated comments.",
			rp.LatestUmpires.TotalRatings, rp.LatestUmpires.Good, rp.LatestUmpires.Average, rp.LatestUmpires.Poor),
		SeasonUmpireReport: fmt.Sprintf("Season-to-date umpire feedback contains %d individual ratings: %d good, %d average and %d poor. The season view is the better basis for committee action because it separates one-off dissatisfaction from repeated patterns; any official appearing regularly in the review list should be discussed with appointments context before escalation.",
			rp.SeasonUmpires.TotalRatings, rp.SeasonUmpires.Good, rp.SeasonUmpires.Average, rp.SeasonUmpires.Poor),
		Conclusion: "Recommended focus for the next cycle is clear: close out missing submissions immediately, review any sanctions or repeated non-compliance, and compare the latest low-rated grounds against the season concern list before contacting clubs. For medium-term governance, track whether the same venues or officials remain in review positions next week; repeated evidence should trigger committee follow-up, while isolated results should remain under observation.",
	}
}

func buildFallbackSeasonToDateNarrative(rp aiExecutiveReportPayload) aiExecutiveNarrative {
	seasonTrend := "No weekly trend is available yet."
	if len(rp.SeasonToDate.WeeklyTrend) >= 2 {
		first := rp.SeasonToDate.WeeklyTrend[0]
		last := rp.SeasonToDate.WeeklyTrend[len(rp.SeasonToDate.WeeklyTrend)-1]
		pitchMovement := last.AvgPitch - first.AvgPitch
		direction := "broadly stable"
		if pitchMovement >= 0.25 {
			direction = "improving"
		} else if pitchMovement <= -0.25 {
			direction = "softening"
		}
		seasonTrend = fmt.Sprintf("The week-by-week pitch trend is %s, moving from %.2f in week %d to %.2f in week %d.",
			direction, first.AvgPitch, first.WeekNumber, last.AvgPitch, last.WeekNumber)
	}
	strongest := describeExecutiveClubRows(rp.SeasonToDate.TopClubs)
	concerns := describeExecutiveClubRows(rp.SeasonToDate.ConcernClubs)
	if strongest == "" {
		strongest = "no clear strongest club or ground group is available yet"
	}
	if concerns == "" {
		concerns = "no repeated concern group is available yet"
	}

	return aiExecutiveNarrative{
		ExecutiveSummary: fmt.Sprintf("This season-to-date report covers %s. The league has recorded %d reports against %d expected fixture reports, giving %.1f%% compliance. Average pitch rating is %.2f. The report should be read as an evidence snapshot: compliance tells the committee where chasing is still required, while the pitch and umpire sections identify repeated patterns that may need operational follow-up.",
			rp.Cover.SeasonPeriod, rp.SeasonToDate.SubmissionsReceived, rp.SeasonToDate.SubmissionsExpected, rp.SeasonToDate.ComplianceRate, rp.SeasonToDate.AvgPitch),
		LatestReport: fmt.Sprintf("The current reporting period is %s. It currently has %d reports against %d expected fixture reports, giving %.1f%% compliance. There are %d current missing submission item(s) and %d non-overturned sanction item(s) in this period, so the immediate operational focus is to close the remaining live gaps before they become historical disputes.",
			rp.Latest.Period, rp.Latest.SubmissionsReceived, rp.Latest.SubmissionsExpected, rp.Latest.ComplianceRate, len(rp.Latest.MissingTeams), rp.Latest.SanctionsIssued),
		SeasonReport: fmt.Sprintf("From season start to the as-of date, average pitch score is %.2f, with bounce %.2f, seam %.2f, carry %.2f and turn %.2f. %s Strongest rated clubs or grounds include %s; the lowest-rated group includes %s. The committee should distinguish isolated low scores from repeated ground patterns before deciding whether club contact, ground inspection or remedial action is justified.",
			rp.SeasonToDate.AvgPitch, rp.SeasonToDate.AvgBounce, rp.SeasonToDate.AvgSeam, rp.SeasonToDate.AvgCarry, rp.SeasonToDate.AvgTurn, seasonTrend, strongest, concerns),
		LatestUmpireReport: fmt.Sprintf("Current-period umpire feedback contains %d individual ratings: %d good, %d average and %d poor. This is useful for immediate triage, but individual current-period feedback should be matched against season context before drawing firm conclusions.",
			rp.LatestUmpires.TotalRatings, rp.LatestUmpires.Good, rp.LatestUmpires.Average, rp.LatestUmpires.Poor),
		SeasonUmpireReport: fmt.Sprintf("Season-to-date umpire feedback contains %d individual ratings: %d good, %d average and %d poor. The season view is the stronger evidence base because it separates one-off dissatisfaction from repeated feedback patterns; officials in the review list should be considered alongside appointments, match context and any written comments.",
			rp.SeasonUmpires.TotalRatings, rp.SeasonUmpires.Good, rp.SeasonUmpires.Average, rp.SeasonUmpires.Poor),
		Conclusion: "Recommended focus is to close live missing submissions first, then review low-rated grounds and umpire feedback where the evidence is repeated across the season-to-date window. For the next committee cycle, monitor whether current concern clubs, grounds or officials appear again after new reports arrive; repeated evidence should trigger follow-up, while isolated results should remain under observation.",
	}
}

func effectiveAIExecutiveNarrative(rp aiExecutiveReportPayload) aiExecutiveNarrative {
	if !rp.GeneratedByAI && rp.AIError != "" {
		return buildFallbackExecutiveNarrative(rp)
	}
	return rp.Executive
}

func describeExecutiveClubRows(rows []aiExecutiveClubRow) string {
	if len(rows) == 0 {
		return ""
	}
	limit := 3
	if len(rows) < limit {
		limit = len(rows)
	}
	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, fmt.Sprintf("%s (%.2f)", rows[i].Club, rows[i].AvgPitch))
	}
	return strings.Join(parts, ", ")
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return ""
}

func truncateForReport(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "..."
}

func printScoreBadge(score float64, max float64) string {
	cls := "score-badge score-red"
	if score >= max*0.8 {
		cls = "score-badge score-green"
	} else if score >= max*0.6 {
		cls = "score-badge score-gold"
	}
	return fmt.Sprintf(`<span class="%s">%.2f</span>`, cls, score)
}

func writeAIExecutivePrintReport(w http.ResponseWriter, rp aiExecutiveReportPayload, canViewUmpireFeedback bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	narrative := effectiveAIExecutiveNarrative(rp)
	latestTitle := aiExecutiveLatestTitle(rp)
	seasonTitle := aiExecutiveSeasonTitle(rp)
	narrativeSource := "Deterministic fallback narrative"
	if rp.GeneratedByAI {
		narrativeSource = "AI-assisted narrative"
		if rp.AIModel != "" {
			narrativeSource += " (" + rp.AIModel + ")"
		}
	}

	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>%s - %s</title>
<style>
@page { size: A4; margin: 12mm; }
* { box-sizing: border-box; }
body { margin: 0; color: #18202a; font-family: "Segoe UI", Arial, sans-serif; font-size: 11px; line-height: 1.42; background: #fff; }
button.print-btn { position: fixed; top: 10px; right: 10px; padding: 7px 12px; border: 0; background: #C41E3A; color: #fff; border-radius: 4px; cursor: pointer; z-index: 10; }
.cover { border-top: 7px solid #C41E3A; padding: 14mm 0 8mm; }
.brand-row { display: flex; align-items: center; justify-content: space-between; gap: 18px; margin-bottom: 18mm; }
.logo { height: 42px; width: auto; }
.doc-label { text-transform: uppercase; letter-spacing: .12em; color: #6c7380; font-size: 9px; font-weight: 700; text-align: right; }
h1 { margin: 0; color: #C41E3A; font-size: 32px; line-height: 1.05; letter-spacing: 0; }
h2 { margin: 0 0 8px; color: #C41E3A; font-size: 17px; page-break-after: avoid; }
h3 { margin: 14px 0 6px; font-size: 11px; text-transform: uppercase; letter-spacing: .08em; color: #4a5565; }
p { margin: 0 0 8px; }
.meta { color: #586271; font-size: 11px; }
.lede { max-width: 150mm; margin-top: 7px; color: #303846; font-size: 13px; line-height: 1.45; }
.cover-grid { display: grid; grid-template-columns: 1.35fr .65fr; gap: 14px; align-items: start; margin-top: 14px; }
.panel { border: 1px solid #d9dee7; border-radius: 6px; background: #fff; padding: 12px; }
.summary-panel { border-left: 5px solid #C41E3A; background: #fbfcfe; }
.section-list { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 8px; }
.chip { border: 1px solid #d9dee7; border-radius: 999px; padding: 3px 8px; color: #4a5565; font-size: 9px; }
.kpis { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; margin: 12px 0; }
.kpi { border: 1px solid #d9dee7; border-radius: 6px; background: #fff; padding: 9px; min-height: 58px; }
.kpi-num { font-size: 20px; line-height: 1.1; font-weight: 800; color: #C41E3A; }
.kpi-label { margin-top: 3px; font-size: 8.5px; color: #647084; text-transform: uppercase; letter-spacing: .08em; font-weight: 700; }
.kpi-green { border-top: 3px solid #198754; }
.kpi-gold { border-top: 3px solid #d9a400; }
.kpi-red { border-top: 3px solid #dc3545; }
.kpi-blue { border-top: 3px solid #0d6efd; }
.section { margin-top: 14px; page-break-inside: avoid; }
.major { page-break-before: always; }
.section-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; border-bottom: 2px solid #C41E3A; padding-bottom: 5px; margin-bottom: 10px; }
.copy { color: #2f3845; max-width: 178mm; }
.copy p { margin-bottom: 7px; }
.split { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; align-items: start; }
table { border-collapse: collapse; width: 100%%; margin: 6px 0 10px; font-size: 9.5px; }
th { background: #C41E3A; color: #fff; padding: 5px 6px; text-align: left; font-size: 8.5px; text-transform: uppercase; letter-spacing: .04em; }
td { border-bottom: 1px solid #e8ebf0; padding: 4px 6px; vertical-align: top; }
tr:nth-child(even) td { background: #fbfcfe; }
.score-badge { display: inline-block; min-width: 30px; padding: 1px 5px; border-radius: 999px; font-weight: 700; text-align: center; }
.score-green { color: #0f5132; background: #dff2e5; }
.score-gold { color: #664d03; background: #fff0bf; }
.score-red { color: #842029; background: #f8d7da; }
.note { color: #647084; font-size: 9px; border-top: 1px solid #e8ebf0; margin-top: 10px; padding-top: 6px; }
@media print { button.print-btn { display: none; } .cover { padding-top: 6mm; } }
</style></head><body>
<button class="print-btn" onclick="window.print()">Print / Save as PDF</button>
<section class="cover">
  <div class="brand-row">
    <img class="logo" src="/images/logo.webp" alt="GMCL">
    <div class="doc-label">Executive Committee Pack<br>%s</div>
  </div>
  <h1>%s</h1>
  <p class="lede">%s</p>
  <div class="kpis">
    <div class="kpi kpi-blue"><div class="kpi-num">%d</div><div class="kpi-label">Latest Reports</div></div>
    <div class="kpi %s"><div class="kpi-num">%.1f%%</div><div class="kpi-label">Latest Compliance</div></div>
    <div class="kpi %s"><div class="kpi-num">%.2f</div><div class="kpi-label">Season Pitch</div></div>
    <div class="kpi %s"><div class="kpi-num">%d</div><div class="kpi-label">Open Focus Items</div></div>
  </div>
  <div class="cover-grid">
    <div class="panel summary-panel"><h2>Executive Summary</h2><div class="copy">%s</div></div>
    <div class="panel">
      <h3>Report Details</h3>
      <p><strong>%s:</strong> %s</p>
      <p><strong>%s:</strong> %s</p>
      <p><strong>Prepared for:</strong> %s</p>
      <p><strong>Generated:</strong> %s</p>
      <p><strong>Narrative:</strong> %s</p>
      <h3>Sections</h3>
      <div class="section-list">`,
		escapeHTML(rp.Cover.Title), escapeHTML(rp.ReportPeriod),
		escapeHTML(rp.ReportPeriod),
		escapeHTML(rp.Cover.Title),
		escapeHTML(rp.Cover.LatestPeriod+"; "+rp.Cover.SeasonPeriod),
		rp.Latest.SubmissionsReceived,
		printKPIClass(execComplianceClass(rp.Latest.ComplianceRate)), rp.Latest.ComplianceRate,
		printKPIClass(execPitchClass(rp.SeasonToDate.AvgPitch)), rp.SeasonToDate.AvgPitch,
		printFocusClass(len(rp.Latest.MissingTeams)+int(rp.Latest.SanctionsIssued)), len(rp.Latest.MissingTeams)+int(rp.Latest.SanctionsIssued),
		paragraphsHTML(narrative.ExecutiveSummary),
		escapeHTML(latestTitle),
		escapeHTML(rp.Cover.LatestPeriod),
		escapeHTML(seasonTitle),
		escapeHTML(rp.Cover.SeasonPeriod),
		escapeHTML(rp.Cover.PreparedFor),
		rp.GeneratedAt.Format("02 Jan 2006 15:04"),
		escapeHTML(narrativeSource))
	for _, item := range rp.TableOfContents {
		if !canViewUmpireFeedback && strings.Contains(strings.ToLower(item), "umpire") {
			continue
		}
		fmt.Fprintf(w, `<span class="chip">%s</span>`, escapeHTML(item))
	}
	fmt.Fprint(w, `</div></div></div></section>`)

	printNarrative := func(title, body string) {
		fmt.Fprintf(w, `<section class="section"><div class="section-heading"><h2>%s</h2></div><div class="copy">%s</div></section>`, escapeHTML(title), paragraphsHTML(body))
	}
	printClubTable := func(title string, rows []aiExecutiveClubRow, limit int) {
		if len(rows) == 0 {
			return
		}
		if len(rows) < limit {
			limit = len(rows)
		}
		fmt.Fprintf(w, `<h3>%s</h3><table><tr><th>Club/Ground</th><th>Reports</th><th>Pitch</th><th>Bounce</th><th>Seam</th><th>Carry</th><th>Turn</th></tr>`, escapeHTML(title))
		for _, row := range rows[:limit] {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				escapeHTML(row.Club), row.Reports, printScoreBadge(row.AvgPitch, 5), printScoreBadge(row.AvgBounce, 5),
				printScoreBadge(row.AvgSeam, 5), printScoreBadge(row.AvgCarry, 5), printScoreBadge(row.AvgTurn, 5))
		}
		fmt.Fprint(w, `</table>`)
	}
	printWindow := func(title string, win aiExecutiveWindow, body string, major bool) {
		majorClass := ""
		if major {
			majorClass = " major"
		}
		expectedLabel := "Expected"
		if title == "Latest Report" || title == "Current Period To Date" {
			expectedLabel = "Fixtures Expected"
		}
		fmt.Fprintf(w, `<section class="section%s"><div class="section-heading"><h2>%s</h2><span class="meta">%s</span></div>
<div class="kpis">
<div class="kpi kpi-blue"><div class="kpi-num">%d</div><div class="kpi-label">Reports</div></div>
<div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">%s</div></div>
<div class="kpi %s"><div class="kpi-num">%.1f%%</div><div class="kpi-label">Compliance</div></div>
<div class="kpi %s"><div class="kpi-num">%.2f</div><div class="kpi-label">Pitch</div></div>
</div><div class="copy">%s</div>`,
			majorClass, escapeHTML(title), escapeHTML(win.Period),
			win.SubmissionsReceived, win.SubmissionsExpected, escapeHTML(expectedLabel),
			printKPIClass(execComplianceClass(win.ComplianceRate)), win.ComplianceRate,
			printKPIClass(execPitchClass(win.AvgPitch)), win.AvgPitch,
			paragraphsHTML(body))
		fmt.Fprint(w, `<div class="split">`)
		fmt.Fprint(w, `<div>`)
		printClubTable("Strongest rated", win.TopClubs, 8)
		fmt.Fprint(w, `</div><div>`)
		printClubTable("Lowest rated", win.ConcernClubs, 8)
		fmt.Fprint(w, `</div></div>`)
		if len(win.MissingTeams) > 0 {
			fmt.Fprint(w, `<h3>Missing Submissions</h3><table><tr><th>Club</th><th>Team</th></tr>`)
			for _, row := range win.MissingTeams {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`, escapeHTML(row.ClubName), escapeHTML(row.TeamName))
			}
			fmt.Fprint(w, `</table>`)
		}
		fmt.Fprint(w, `</section>`)
	}
	printUmpires := func(title string, win aiExecutiveUmpireWindow, body string) {
		fmt.Fprintf(w, `<section class="section major"><div class="section-heading"><h2>%s</h2><span class="meta">%s</span></div>
<div class="kpis">
<div class="kpi kpi-blue"><div class="kpi-num">%d</div><div class="kpi-label">Ratings</div></div>
<div class="kpi kpi-green"><div class="kpi-num">%d</div><div class="kpi-label">Good</div></div>
<div class="kpi kpi-gold"><div class="kpi-num">%d</div><div class="kpi-label">Average</div></div>
<div class="kpi %s"><div class="kpi-num">%d</div><div class="kpi-label">Poor</div></div>
</div><div class="copy">%s</div>`,
			escapeHTML(title), escapeHTML(win.Period), win.TotalRatings, win.Good, win.Average, printFocusClass(int(win.Poor)), win.Poor, paragraphsHTML(body))
		if len(win.TopUmpires) > 0 {
			fmt.Fprint(w, `<h3>Highest rated officials</h3><table><tr><th>Umpire</th><th>Ratings</th><th>Good</th><th>Average</th><th>Poor</th><th>Score</th></tr>`)
			for _, row := range win.TopUmpires {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%s</td></tr>`,
					escapeHTML(row.Name), row.Ratings, row.Good, row.Average, row.Poor, printScoreBadge(row.Score, 3))
			}
			fmt.Fprint(w, `</table>`)
		}
		if len(win.ConcernUmpires) > 0 {
			fmt.Fprint(w, `<h3>Officials requiring review</h3><table><tr><th>Umpire</th><th>Ratings</th><th>Good</th><th>Average</th><th>Poor</th><th>Score</th></tr>`)
			for _, row := range win.ConcernUmpires {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%s</td></tr>`,
					escapeHTML(row.Name), row.Ratings, row.Good, row.Average, row.Poor, printScoreBadge(row.Score, 3))
			}
			fmt.Fprint(w, `</table>`)
		}
		fmt.Fprint(w, `</section>`)
	}

	printWindow(latestTitle, rp.Latest, narrative.LatestReport, true)
	printWindow(seasonTitle, rp.SeasonToDate, narrative.SeasonReport, true)
	if canViewUmpireFeedback {
		printUmpires("Latest Umpire Reports", rp.LatestUmpires, narrative.LatestUmpireReport)
		printUmpires("Season Umpire Report", rp.SeasonUmpires, narrative.SeasonUmpireReport)
	}
	printNarrative("Conclusion and Recommended Focus", narrative.Conclusion)
	fmt.Fprint(w, `<p class="note">This report is generated from submitted captain reports and associated league records. Browser print headers/footers can be disabled in the print dialog for formal distribution.</p></body></html>`)
}

func printKPIClass(execClass string) string {
	switch execClass {
	case "exec-good":
		return "kpi-green"
	case "exec-watch":
		return "kpi-gold"
	default:
		return "kpi-red"
	}
}

func printFocusClass(count int) string {
	if count == 0 {
		return "kpi-green"
	}
	if count <= 5 {
		return "kpi-gold"
	}
	return "kpi-red"
}
