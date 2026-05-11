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

	latestExpected := s.loadAIExpectedReports(ctx, seasonID, weekStart, weekEnd)
	seasonExpected := s.loadAIExpectedReports(ctx, seasonID, time.Time{}, weekEnd)

	rp.Latest = s.loadAIExecutiveWindow(ctx, "Latest Report", rp.Cover.LatestPeriod,
		"sub.season_id=$1 AND sub.week_id=$2", []any{seasonID, *weekID}, latestExpected, &seasonID, weekID)
	rp.SeasonToDate = s.loadAIExecutiveWindow(ctx, "Season Report", rp.Cover.SeasonPeriod,
		"sub.season_id=$1 AND w.end_date <= $2", []any{seasonID, weekEnd}, seasonExpected, &seasonID, nil)
	rp.SeasonToDate.WeeklyTrend = s.loadAIExecutiveWeeklyTrend(ctx, seasonID, weekEnd)
	rp.Latest.MissingTeams = s.loadAIExecutiveMissingTeams(ctx, *weekID, weekStart, weekEnd)
	s.alignLatestComplianceWithDashboard(ctx, &rp.Latest, *weekID)
	rp.LatestUmpires = s.loadAIExecutiveUmpireWindow(ctx, "Latest Umpire Reports", rp.Cover.LatestPeriod,
		"sub.season_id=$1 AND sub.week_id=$2", []any{seasonID, *weekID})
	rp.SeasonUmpires = s.loadAIExecutiveUmpireWindow(ctx, "Season Umpire Report", rp.Cover.SeasonPeriod,
		"sub.season_id=$1 AND w.end_date <= $2", []any{seasonID, weekEnd})

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
		if weekID != nil {
			_ = s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1 AND week_id=$2`, *seasonID, *weekID).Scan(&win.SanctionsIssued)
		} else {
			_ = s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1`, *seasonID).Scan(&win.SanctionsIssued)
		}
	}
	win.TopClubs = s.loadAIExecutiveClubRows(ctx, whereSQL, args, "DESC")
	win.ConcernClubs = s.loadAIExecutiveClubRows(ctx, whereSQL, args, "ASC")
	win.RepresentativeNotes = s.loadAIExecutiveNotes(ctx, whereSQL, args, "comments")
	return win
}

func (s *Server) alignLatestComplianceWithDashboard(ctx context.Context, win *aiExecutiveWindow, weekID int32) {
	var received, expected int64
	_ = s.DB.QueryRow(ctx, `SELECT COUNT(DISTINCT team_id) FROM submissions WHERE week_id=$1`, weekID).Scan(&received)
	_ = s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM teams WHERE active=TRUE`).Scan(&expected)
	if expected <= 0 {
		return
	}
	win.SubmissionsReceived = received
	win.SubmissionsExpected = expected
	win.ComplianceRate = float64(received) / float64(expected) * 100
	if win.ComplianceRate > 100 {
		win.ComplianceRate = 100
	}
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
		SELECT COUNT(*) FROM (
		    SELECT DISTINCT t.id, lf.match_date
		    FROM league_fixtures lf
		    JOIN teams t ON (
		        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id) OR
		        TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
		    )
		    WHERE %s
		) expected_reports
	`, where), args...).Scan(&expected)
	return expected
}

func (s *Server) loadAIExecutiveMissingTeams(ctx context.Context, weekID int32, weekStart, weekEnd time.Time) []reportMissing {
	rows, err := s.DB.Query(ctx, `
		WITH fixture_counts AS (
		    SELECT t.id AS team_id,
		           COUNT(DISTINCT lf.match_date) AS fixture_count
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
		    GROUP BY t.id
		),
		submit_counts AS (
		    SELECT team_id,
		           COUNT(DISTINCT match_date) AS submit_count
		    FROM submissions
		    WHERE week_id=$1
		    GROUP BY team_id
		)
		SELECT t.id, cl.name, t.name
		FROM fixture_counts fc
		JOIN teams t ON t.id=fc.team_id
		JOIN clubs cl ON t.club_id=cl.id
		LEFT JOIN submit_counts sc ON sc.team_id=t.id
		WHERE fc.fixture_count > COALESCE(sc.submit_count, 0)
		ORDER BY cl.name, t.name
	`, weekID, weekStart, weekEnd)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var missing []reportMissing
	for rows.Next() {
		var row reportMissing
		if rows.Scan(&row.TeamID, &row.ClubName, &row.TeamName) == nil {
			missing = append(missing, row)
		}
	}
	return missing
}

func (s *Server) loadAIExecutiveUmpireWindow(ctx context.Context, title, period, whereSQL string, args []any) aiExecutiveUmpireWindow {
	win := aiExecutiveUmpireWindow{Title: title, Period: period}
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`
		WITH r AS (
		    SELECT lower(trim(sub.form_data->>'umpire1_name')) AS name, sub.form_data->>'umpire1_performance' AS perf
		    FROM submissions sub JOIN weeks w ON w.id=sub.week_id
		    WHERE %s AND NULLIF(trim(sub.form_data->>'umpire1_name'), '') IS NOT NULL
		    UNION ALL
		    SELECT lower(trim(sub.form_data->>'umpire2_name')) AS name, sub.form_data->>'umpire2_performance' AS perf
		    FROM submissions sub JOIN weeks w ON w.id=sub.week_id
		    WHERE %s AND NULLIF(trim(sub.form_data->>'umpire2_name'), '') IS NOT NULL
		),
		agg AS (
		    SELECT name,
		           COUNT(*) FILTER (WHERE perf IN ('Good','Average','Poor')) AS total,
		           COUNT(*) FILTER (WHERE perf='Good') AS good,
		           COUNT(*) FILTER (WHERE perf='Average') AS average,
		           COUNT(*) FILTER (WHERE perf='Poor') AS poor,
		           COALESCE(ROUND((
		               COUNT(*) FILTER (WHERE perf='Good') * 3.0 +
		               COUNT(*) FILTER (WHERE perf='Average') * 2.0 +
		               COUNT(*) FILTER (WHERE perf='Poor') * 1.0
		           ) / NULLIF(COUNT(*) FILTER (WHERE perf IN ('Good','Average','Poor')),0), 2), 0) AS score
		    FROM r WHERE name IS NOT NULL AND name <> '' GROUP BY name
		)
		SELECT name, total, good, average, poor, score
		FROM agg WHERE total > 0
		ORDER BY score DESC, total DESC, name
	`, whereSQL, whereSQL), append(args, args...)...)
	if err != nil {
		return win
	}
	defer rows.Close()

	var all []reportUmpire
	for rows.Next() {
		var row reportUmpire
		if rows.Scan(&row.Name, &row.Ratings, &row.Good, &row.Average, &row.Poor, &row.Score) == nil {
			all = append(all, row)
			win.TotalRatings += row.Ratings
			win.Good += row.Good
			win.Average += row.Average
			win.Poor += row.Poor
		}
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
	reqBody, _ := json.Marshal(map[string]any{
		"model": model,
		"instructions": strings.Join([]string{
			"You are writing a professional executive report for a cricket league committee.",
			"Use only the supplied JSON data. Do not invent figures, clubs, umpires, dates, or trends.",
			"Write in clear British English for executives. Be direct, measured, and evidence-led.",
			"Return only valid JSON with keys: executive_summary, latest_report, season_report, latest_umpire_report, season_umpire_report, conclusion.",
			"Each value should be one or two concise paragraphs. Mention the most important numbers where relevant.",
		}, " "),
		"input":             "Create the executive narrative sections from this report data:\n" + string(data),
		"max_output_tokens": 2200,
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
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
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
				b.WriteString(c.Text)
			}
		}
		text = strings.TrimSpace(b.String())
	}
	var narrative aiExecutiveNarrative
	if err := json.Unmarshal([]byte(extractJSONObject(text)), &narrative); err != nil {
		return aiExecutiveNarrative{}, model, err
	}
	return narrative, model, nil
}

func openAIReportConfigured() bool {
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
}

func buildFallbackExecutiveNarrative(rp aiExecutiveReportPayload) aiExecutiveNarrative {
	return aiExecutiveNarrative{
		ExecutiveSummary: fmt.Sprintf("The latest completed reporting period is %s. It recorded %d reports against %d expected submissions, giving %.1f%% compliance. The season-to-date position is %d reports against %d expected submissions, with an average pitch rating of %.2f.",
			rp.Latest.Period, rp.Latest.SubmissionsReceived, rp.Latest.SubmissionsExpected, rp.Latest.ComplianceRate,
			rp.SeasonToDate.SubmissionsReceived, rp.SeasonToDate.SubmissionsExpected, rp.SeasonToDate.AvgPitch),
		LatestReport: fmt.Sprintf("For the latest week, the average pitch score was %.2f, with bounce %.2f, seam %.2f, carry %.2f and turn %.2f. There were %d missing submissions and %d sanctions recorded for the period.",
			rp.Latest.AvgPitch, rp.Latest.AvgBounce, rp.Latest.AvgSeam, rp.Latest.AvgCarry, rp.Latest.AvgTurn, len(rp.Latest.MissingTeams), rp.Latest.SanctionsIssued),
		SeasonReport: fmt.Sprintf("Across the season to date, compliance is %.1f%% and the average pitch score is %.2f. The cumulative data should be used to identify repeated ground-quality themes rather than isolated weekly outliers.",
			rp.SeasonToDate.ComplianceRate, rp.SeasonToDate.AvgPitch),
		LatestUmpireReport: fmt.Sprintf("The latest umpire report contains %d individual ratings: %d good, %d average and %d poor. The detailed table highlights the strongest feedback and the officials requiring review.",
			rp.LatestUmpires.TotalRatings, rp.LatestUmpires.Good, rp.LatestUmpires.Average, rp.LatestUmpires.Poor),
		SeasonUmpireReport: fmt.Sprintf("Season-to-date umpire feedback contains %d individual ratings: %d good, %d average and %d poor. This should be reviewed alongside appointment context and any repeated comments.",
			rp.SeasonUmpires.TotalRatings, rp.SeasonUmpires.Good, rp.SeasonUmpires.Average, rp.SeasonUmpires.Poor),
		Conclusion: "The priority is to use the latest weekly findings for immediate follow-up while using the season-to-date report to identify recurring club, ground and umpire patterns. Clubs with repeated low ratings, missing submissions or sanctions should be reviewed first.",
	}
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
	return text
}

func truncateForReport(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "..."
}

func writeAIExecutivePrintReport(w http.ResponseWriter, rp aiExecutiveReportPayload) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>%s - %s</title>
<style>
* { box-sizing: border-box; }
body { font-family: Arial, sans-serif; color: #222; font-size: 12px; margin: 18mm 15mm; }
.cover { min-height: 245mm; display: flex; flex-direction: column; justify-content: center; border-top: 10px solid #C41E3A; border-bottom: 1px solid #bbb; }
h1 { font-size: 34px; margin: 0 0 8px; color: #C41E3A; letter-spacing: 0; }
h2 { font-size: 18px; margin: 24px 0 8px; color: #C41E3A; border-bottom: 1px solid #C41E3A; padding-bottom: 4px; page-break-after: avoid; }
h3 { font-size: 13px; margin: 16px 0 6px; }
p { line-height: 1.45; }
.meta { color: #555; font-size: 13px; }
.page-break { page-break-before: always; }
.section { page-break-inside: avoid; }
.kpis { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; margin: 12px 0; }
.kpi { border: 1px solid #ddd; border-top: 3px solid #C41E3A; padding: 9px; }
.kpi-num { font-size: 20px; font-weight: bold; color: #C41E3A; }
.kpi-label { font-size: 10px; color: #666; text-transform: uppercase; }
table { border-collapse: collapse; width: 100%%; margin: 8px 0 14px; font-size: 11px; }
th { background: #C41E3A; color: #fff; text-align: left; padding: 5px 7px; }
td { border-bottom: 1px solid #eee; padding: 4px 7px; }
ol { line-height: 1.7; }
button { float: right; padding: 6px 12px; border: 0; background: #C41E3A; color: #fff; border-radius: 4px; }
@media print { button { display: none; } body { margin: 14mm; } .cover { min-height: 250mm; } }
</style></head><body>
<button onclick="window.print()">Print / Save as PDF</button>
<section class="cover">
  <h1>%s</h1>
  <p class="meta">%s</p>
  <p><strong>Latest report:</strong> %s</p>
  <p><strong>Season report:</strong> %s</p>
  <p><strong>Prepared for:</strong> %s</p>
  <p><strong>Generated:</strong> %s</p>
</section>
<section class="page-break section"><h2>Table of Contents</h2><ol>`,
		escapeHTML(rp.Cover.Title), escapeHTML(rp.ReportPeriod),
		escapeHTML(rp.Cover.Title), escapeHTML(rp.SeasonName),
		escapeHTML(rp.Cover.LatestPeriod), escapeHTML(rp.Cover.SeasonPeriod),
		escapeHTML(rp.Cover.PreparedFor), rp.GeneratedAt.Format("02 Jan 2006 15:04"))
	for _, item := range rp.TableOfContents {
		fmt.Fprintf(w, `<li>%s</li>`, escapeHTML(item))
	}
	fmt.Fprint(w, `</ol></section>`)

	printNarrative := func(title, body string) {
		fmt.Fprintf(w, `<section class="page-break section"><h2>%s</h2>%s</section>`, escapeHTML(title), paragraphsHTML(body))
	}
	printWindow := func(title string, win aiExecutiveWindow) {
		expectedLabel := "Expected"
		if title == "Latest Report" {
			expectedLabel = "Active Teams"
		}
		fmt.Fprintf(w, `<section class="page-break section"><h2>%s</h2><p class="meta">%s</p>
<div class="kpis">
<div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Reports</div></div>
<div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">%s</div></div>
<div class="kpi"><div class="kpi-num">%.1f%%</div><div class="kpi-label">Compliance</div></div>
<div class="kpi"><div class="kpi-num">%.2f</div><div class="kpi-label">Pitch</div></div>
</div>`, escapeHTML(title), escapeHTML(win.Period), win.SubmissionsReceived, win.SubmissionsExpected, escapeHTML(expectedLabel), win.ComplianceRate, win.AvgPitch)
		if len(win.TopClubs) > 0 {
			fmt.Fprint(w, `<h3>Strongest Rated Clubs/Grounds</h3><table><tr><th>Club/Ground</th><th>Reports</th><th>Pitch</th><th>Bounce</th><th>Seam</th><th>Carry</th><th>Turn</th></tr>`)
			for _, row := range win.TopClubs {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%.2f</td><td>%.2f</td><td>%.2f</td><td>%.2f</td><td>%.2f</td></tr>`,
					escapeHTML(row.Club), row.Reports, row.AvgPitch, row.AvgBounce, row.AvgSeam, row.AvgCarry, row.AvgTurn)
			}
			fmt.Fprint(w, `</table>`)
		}
		if len(win.ConcernClubs) > 0 {
			fmt.Fprint(w, `<h3>Lowest Rated Clubs/Grounds</h3><table><tr><th>Club/Ground</th><th>Reports</th><th>Pitch</th><th>Bounce</th><th>Seam</th><th>Carry</th><th>Turn</th></tr>`)
			for _, row := range win.ConcernClubs {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%.2f</td><td>%.2f</td><td>%.2f</td><td>%.2f</td><td>%.2f</td></tr>`,
					escapeHTML(row.Club), row.Reports, row.AvgPitch, row.AvgBounce, row.AvgSeam, row.AvgCarry, row.AvgTurn)
			}
			fmt.Fprint(w, `</table>`)
		}
		if len(win.MissingTeams) > 0 {
			fmt.Fprint(w, `<h3>Missing Submissions</h3><table><tr><th>Club</th><th>Team</th></tr>`)
			for _, row := range win.MissingTeams {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`, escapeHTML(row.ClubName), escapeHTML(row.TeamName))
			}
			fmt.Fprint(w, `</table>`)
		}
		fmt.Fprint(w, `</section>`)
	}
	printUmpires := func(title string, win aiExecutiveUmpireWindow) {
		fmt.Fprintf(w, `<section class="page-break section"><h2>%s</h2><p class="meta">%s</p>
<div class="kpis">
<div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Ratings</div></div>
<div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Good</div></div>
<div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Average</div></div>
<div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Poor</div></div>
</div>`, escapeHTML(title), escapeHTML(win.Period), win.TotalRatings, win.Good, win.Average, win.Poor)
		if len(win.TopUmpires) > 0 {
			fmt.Fprint(w, `<table><tr><th>Umpire</th><th>Ratings</th><th>Good</th><th>Average</th><th>Poor</th><th>Score</th></tr>`)
			for _, row := range win.TopUmpires {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%.2f</td></tr>`,
					escapeHTML(titleCase(row.Name)), row.Ratings, row.Good, row.Average, row.Poor, row.Score)
			}
			fmt.Fprint(w, `</table>`)
		}
		if len(win.ConcernUmpires) > 0 {
			fmt.Fprint(w, `<h3>Umpires Requiring Review</h3><table><tr><th>Umpire</th><th>Ratings</th><th>Good</th><th>Average</th><th>Poor</th><th>Score</th></tr>`)
			for _, row := range win.ConcernUmpires {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%.2f</td></tr>`,
					escapeHTML(titleCase(row.Name)), row.Ratings, row.Good, row.Average, row.Poor, row.Score)
			}
			fmt.Fprint(w, `</table>`)
		}
		fmt.Fprint(w, `</section>`)
	}

	printNarrative("Executive Summary", rp.Executive.ExecutiveSummary)
	printWindow("Latest Report", rp.Latest)
	printNarrative("Latest Report Findings", rp.Executive.LatestReport)
	printWindow("Season Report", rp.SeasonToDate)
	printNarrative("Season Report Findings", rp.Executive.SeasonReport)
	printUmpires("Latest Umpire Reports", rp.LatestUmpires)
	printNarrative("Latest Umpire Findings", rp.Executive.LatestUmpireReport)
	printUmpires("Season Umpire Report", rp.SeasonUmpires)
	printNarrative("Season Umpire Findings", rp.Executive.SeasonUmpireReport)
	printNarrative("Conclusion", rp.Executive.Conclusion)
	fmt.Fprint(w, `</body></html>`)
}
