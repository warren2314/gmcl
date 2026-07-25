package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleAdminExecReport renders a live executive summary for the current season.
func (s *Server) handleAdminExecReport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		canViewUmpireFeedback := s.adminHasPermission(ctx, adminIDForRequest(r), "view_umpire_feedback")

		// ── Season ─────────────────────────────────────────────────────────
		currentWeek, err := s.resolveCompetitionWeek(ctx, competitionWeekForDisplay)
		if err != nil {
			http.Error(w, "No competition week is configured.", http.StatusServiceUnavailable)
			return
		}
		seasonID := currentWeek.SeasonID
		seasonName := currentWeek.SeasonName

		// ── Current week ───────────────────────────────────────────────────
		currentWeekID := currentWeek.ID
		currentWeekNum := currentWeek.Number
		currentWeekStart := currentWeek.StartDate
		currentWeekEnd := currentWeek.EndDate

		// ── KPI 1: season compliance ────────────────────────────────────────
		seasonProgress, err := s.loadSeasonReportProgress(ctx, seasonID, currentWeek.ComplianceStartWeek)
		if err != nil {
			http.Error(w, "Could not load season report progress.", http.StatusInternalServerError)
			return
		}
		subsReceived := seasonProgress.Submitted
		totalExpected := seasonProgress.Expected
		complianceRate := seasonProgress.completionRate()

		// ── KPI 2: avg pitch rating ─────────────────────────────────────────
		var avgPitch float64
		s.DB.QueryRow(ctx, `
			SELECT COALESCE(AVG(pitch_rating),0) FROM submissions WHERE season_id=$1
		`, seasonID).Scan(&avgPitch)

		// ── KPI 3: sanctions ───────────────────────────────────────────────
		var sanctionsTotal int64
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1`, seasonID).Scan(&sanctionsTotal)

		// ── KPI 4: this week submissions ───────────────────────────────────
		weekProgress, err := s.loadWeekReportProgress(ctx, currentWeek)
		if err != nil {
			http.Error(w, "Could not load current-week report progress.", http.StatusInternalServerError)
			return
		}
		weekSubs := weekProgress.Submitted
		weekExpected := weekProgress.Expected

		// ── Weekly compliance trend ─────────────────────────────────────────
		type weekTrend struct {
			WeekNum int32
			Label   string
			Subs    int64
		}
		var trend []weekTrend
		trows, err := s.DB.Query(ctx, `
			SELECT w.week_number, w.start_date,
			       COUNT(sub.id)
			FROM weeks w
			LEFT JOIN submissions sub ON sub.week_id=w.id AND sub.season_id=$1
			WHERE w.season_id=$1 AND w.start_date <= $2::date
			GROUP BY w.week_number, w.start_date
			ORDER BY w.week_number
		`, seasonID, s.londonDate())
		if err == nil {
			defer trows.Close()
			for trows.Next() {
				var wt weekTrend
				var sd time.Time
				if trows.Scan(&wt.WeekNum, &sd, &wt.Subs) == nil {
					wt.Label = fmt.Sprintf("Wk %d (%s)", wt.WeekNum, sd.Format("2 Jan"))
					trend = append(trend, wt)
				}
			}
		}

		// ── Pitch distribution ──────────────────────────────────────────────
		pitchDist := map[int]int64{1: 0, 2: 0, 3: 0, 4: 0, 5: 0}
		pdrows, err := s.DB.Query(ctx, `
			SELECT pitch_rating, COUNT(*) FROM submissions
			WHERE season_id=$1 AND pitch_rating IS NOT NULL
			GROUP BY pitch_rating
		`, seasonID)
		if err == nil {
			defer pdrows.Close()
			for pdrows.Next() {
				var r int
				var c int64
				if pdrows.Scan(&r, &c) == nil && r >= 1 && r <= 5 {
					pitchDist[r] = c
				}
			}
		}

		// ── Pitch trend (avg per week) ──────────────────────────────────────
		type pitchPoint struct {
			WeekNum int32
			Avg     float64
		}
		var pitchTrend []pitchPoint
		ptrows, err := s.DB.Query(ctx, `
			SELECT w.week_number, COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0)
			FROM weeks w
			LEFT JOIN submissions sub ON sub.week_id=w.id AND sub.season_id=$1 AND sub.pitch_rating IS NOT NULL
			WHERE w.season_id=$1 AND w.start_date <= $2::date
			GROUP BY w.week_number ORDER BY w.week_number
		`, seasonID, s.londonDate())
		if err == nil {
			defer ptrows.Close()
			for ptrows.Next() {
				var pp pitchPoint
				if ptrows.Scan(&pp.WeekNum, &pp.Avg) == nil {
					pitchTrend = append(pitchTrend, pp)
				}
			}
		}

		// ── Club compliance table ───────────────────────────────────────────
		clubs, err := s.loadSeasonClubReportProgress(ctx, seasonID, currentWeek.ComplianceStartWeek)
		if err != nil {
			http.Error(w, "Could not load club report progress.", http.StatusInternalServerError)
			return
		}

		// ── Top 10 umpires ──────────────────────────────────────────────────
		type umpireRow struct {
			Name    string
			Ratings int64
			Good    int64
			Average int64
			Poor    int64
			Score   float64
		}
		var umpires []umpireRow
		urows, err := s.DB.Query(ctx, `
			WITH latest AS (
			    SELECT DISTINCT ON (team_id, match_date)
			        form_data->>'umpire1_name'        AS u1name,
			        form_data->>'umpire1_performance' AS u1perf,
			        form_data->>'umpire2_name'        AS u2name,
			        form_data->>'umpire2_performance' AS u2perf
			    FROM submissions
			    WHERE season_id=$1
			    ORDER BY team_id, match_date, submitted_at DESC
			),
			r AS (
			    SELECT lower(trim(u1name)) AS name, u1perf AS perf FROM latest WHERE u1name IS NOT NULL AND u1name <> ''
			    UNION ALL
			    SELECT lower(trim(u2name)), u2perf FROM latest WHERE u2name IS NOT NULL AND u2name <> ''
			)
			SELECT name,
			       COUNT(*)                                    AS total,
			       COUNT(*) FILTER (WHERE perf='Good')         AS good,
			       COUNT(*) FILTER (WHERE perf='Average')      AS avg_c,
			       COUNT(*) FILTER (WHERE perf='Poor')         AS poor,
			       ROUND((
			           COUNT(*) FILTER (WHERE perf='Good') * 3.0 +
			           COUNT(*) FILTER (WHERE perf='Average') * 2.0 +
			           COUNT(*) FILTER (WHERE perf='Poor') * 1.0
			       ) / NULLIF(COUNT(*) FILTER (WHERE perf IN ('Good','Average','Poor')),0)::numeric, 2) AS score
			FROM r
			WHERE perf IN ('Good','Average','Poor')
			GROUP BY name
			HAVING COUNT(*) >= 2
			ORDER BY score DESC NULLS LAST, total DESC
			LIMIT 10
		`, seasonID)
		if err == nil {
			defer urows.Close()
			for urows.Next() {
				var u umpireRow
				if urows.Scan(&u.Name, &u.Ratings, &u.Good, &u.Average, &u.Poor, &u.Score) == nil {
					umpires = append(umpires, u)
				}
			}
		}

		// ── Missing teams this week ─────────────────────────────────────────
		type missingTeam struct {
			Club      string
			Team      string
			Opponent  string
			MatchDate time.Time
		}
		var missing []missingTeam
		mrows, err := s.DB.Query(ctx, `
			WITH expected_fixtures AS (
			    SELECT
			        t.id AS team_id,
			        cl.name AS club_name,
			        t.name AS team_name,
			        lf.play_cricket_match_id,
			        lf.match_date,
			        CASE
			            WHEN TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
			            THEN CONCAT_WS(' ', NULLIF(lf.away_club_name, ''), NULLIF(lf.away_team_name, ''))
			            ELSE CONCAT_WS(' ', NULLIF(lf.home_club_name, ''), NULLIF(lf.home_team_name, ''))
			        END AS opponent,
			        ROW_NUMBER() OVER (
			            PARTITION BY t.id, lf.match_date
			            ORDER BY lf.play_cricket_match_id
			        ) AS fixture_ordinal
			    FROM teams t
			    JOIN clubs cl ON cl.id = t.club_id
			    JOIN league_fixtures lf ON (
			        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
			        OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
			    )
			    WHERE t.active = TRUE
			      AND t.play_cricket_team_id IS NOT NULL
			      AND t.play_cricket_team_id <> ''
			      AND lf.match_date BETWEEN $2 AND $3
			      AND EXTRACT(DOW FROM lf.match_date) <> 5
			      AND NOT lf.is_bye
			),
			legacy_submissions AS (
			    SELECT team_id, match_date, COUNT(*) AS legacy_count
			    FROM submissions
			    WHERE week_id = $1
			      AND play_cricket_match_id IS NULL
			    GROUP BY team_id, match_date
			)
			SELECT ef.club_name, ef.team_name, ef.opponent, ef.match_date
			FROM expected_fixtures ef
			LEFT JOIN legacy_submissions ls
			  ON ls.team_id = ef.team_id
			 AND ls.match_date = ef.match_date
			WHERE NOT (
			    EXISTS (
			        SELECT 1
			        FROM submissions sub
			        WHERE sub.week_id = $1
			          AND sub.team_id = ef.team_id
			          AND sub.play_cricket_match_id = ef.play_cricket_match_id
			    )
			    OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
			    OR EXISTS (
			        SELECT 1
			        FROM report_exemptions re
			        WHERE re.week_id = $1
			          AND re.team_id = ef.team_id
			          AND re.match_date = ef.match_date
			          AND (
			              re.play_cricket_match_id = ef.play_cricket_match_id
			              OR re.play_cricket_match_id IS NULL
			          )
			    )
			)
			ORDER BY ef.club_name, ef.team_name, ef.match_date, ef.play_cricket_match_id
		`, currentWeekID, currentWeekStart, currentWeekEnd)
		if err == nil {
			defer mrows.Close()
			for mrows.Next() {
				var m missingTeam
				if mrows.Scan(&m.Club, &m.Team, &m.Opponent, &m.MatchDate) == nil {
					missing = append(missing, m)
				}
			}
		}

		// ── Render ──────────────────────────────────────────────────────────
		csrfToken := ""
		if c, err := r.Cookie("csrf_token"); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHeadWithCharts(w, "Executive Report")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		fmt.Fprint(w, `<div class="container-fluid px-4">`)

		fmt.Fprintf(w, `
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Executive Report</h4>
    <p class="text-muted mb-0 small">%s &mdash; as of %s</p>
  </div>
  <a href="/admin/reports/exec/print" target="_blank" class="btn btn-sm btn-outline-secondary">Print / Save as PDF</a>
</div>
`, escapeHTML(seasonName), time.Now().Format("2 Jan 2006 15:04"))

		// ── KPI row ────────────────────────────────────────────────────────
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-6 col-lg-3">
    <div class="card card-kpi kpi-blue p-3 text-center h-100">
      <div class="kpi-number">%.1f%%</div>
      <div class="kpi-label">Season completion</div>
      <div class="text-muted small mt-1">%d received + %d exempt / %d required</div>
    </div>
  </div>
  <div class="col-6 col-lg-3">
    <div class="card card-kpi kpi-green p-3 text-center h-100">
      <div class="kpi-number">%d / %d</div>
      <div class="kpi-label">Week completion</div>
      <div class="text-muted small mt-1">%d received + %d exempt &middot; Week %d (%s – %s)</div>
    </div>
  </div>
  <div class="col-6 col-lg-3">
    <div class="card card-kpi kpi-teal p-3 text-center h-100">
      <div class="kpi-number">%.2f</div>
      <div class="kpi-label">Avg Pitch Rating</div>
      <div class="text-muted small mt-1">season average (1–5)</div>
    </div>
  </div>
  <div class="col-6 col-lg-3">
    <div class="card card-kpi kpi-amber p-3 text-center h-100">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Sanctions Issued</div>
      <div class="text-muted small mt-1">season total</div>
    </div>
  </div>
</div>
`,
			complianceRate, subsReceived, seasonProgress.Exempt, totalExpected,
			weekProgress.Satisfied, weekExpected, weekSubs, weekProgress.Exempt, currentWeekNum,
			currentWeekStart.Format("2 Jan"), currentWeekEnd.Format("2 Jan"),
			avgPitch,
			sanctionsTotal,
		)

		// ── Charts row ─────────────────────────────────────────────────────
		fmt.Fprint(w, `<div class="row g-3 mb-4">`)

		// Compliance trend
		trendLabels := make([]string, len(trend))
		trendData := make([]int64, len(trend))
		pitchAvgData := make([]float64, len(pitchTrend))
		for i, t := range trend {
			trendLabels[i] = t.Label
			trendData[i] = t.Subs
		}
		for i, p := range pitchTrend {
			pitchAvgData[i] = p.Avg
		}
		trendLabelsJSON, _ := json.Marshal(trendLabels)
		trendDataJSON, _ := json.Marshal(trendData)
		pitchAvgJSON, _ := json.Marshal(pitchAvgData)

		fmt.Fprintf(w, `
<div class="col-12 col-xl-8">
  <div class="card shadow-sm h-100">
    <div class="card-header fw-semibold">Submissions per Week</div>
    <div class="card-body"><div class="chart-container"><canvas id="chartTrend"></canvas></div></div>
  </div>
</div>
<div class="col-12 col-xl-4">
  <div class="card shadow-sm h-100">
    <div class="card-header fw-semibold">Pitch Rating Distribution</div>
    <div class="card-body"><div class="chart-container"><canvas id="chartPitch"></canvas></div></div>
  </div>
</div>
<script>
window.__trendLabels=%s; window.__trendData=%s; window.__pitchAvg=%s;
window.__pitchDist=[%d,%d,%d,%d,%d];
</script>
`,
			string(trendLabelsJSON), string(trendDataJSON), string(pitchAvgJSON),
			pitchDist[1], pitchDist[2], pitchDist[3], pitchDist[4], pitchDist[5],
		)

		fmt.Fprint(w, `</div>`) // end charts row

		// ── Pitch trend chart ───────────────────────────────────────────────
		if len(pitchTrend) > 1 {
			fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Average Pitch Rating per Week</div>
  <div class="card-body"><div class="chart-container"><canvas id="chartPitchTrend"></canvas></div></div>
</div>
`)
		}

		// ── Club compliance table ───────────────────────────────────────────
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Club Compliance</div>
  <div class="table-responsive">
    <table class="table table-sm table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Club</th><th>Teams</th><th>Received</th><th>Exempt</th>
        <th>Required</th><th>Missing</th><th>Completion</th><th>Avg Pitch</th>
      </tr></thead>
      <tbody>
`)
		for _, c := range clubs {
			clubRate := c.completionRate()
			badgeClass := "bg-success"
			if clubRate < 50 {
				badgeClass = "bg-danger"
			} else if clubRate < 80 {
				badgeClass = "bg-warning text-dark"
			}
			fmt.Fprintf(w,
				`<tr><td data-label="Club">%s</td><td data-label="Teams" class="text-muted">%d</td>`+
					`<td data-label="Received">%d</td><td data-label="Exempt">%d</td>`+
					`<td data-label="Required" class="text-muted">%d</td><td data-label="Missing">%d</td>`+
					`<td data-label="Completion"><span class="badge %s">%.1f%%</span></td>`+
					`<td data-label="Avg Pitch">%.2f</td></tr>`,
				escapeHTML(c.Club), c.Teams, c.Submitted, c.Exempt, c.Expected, c.Missing,
				badgeClass, clubRate, c.AvgPitch,
			)
		}
		if len(clubs) == 0 {
			fmt.Fprint(w, `<tr><td colspan="8" class="text-center text-muted py-3">No data yet.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody></table></div></div>`)

		// ── Top umpires ─────────────────────────────────────────────────────
		if canViewUmpireFeedback && len(umpires) > 0 {
			fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Top Umpires (season, min. 2 ratings)</div>
  <div class="table-responsive">
    <table class="table table-sm table-hover table-gmcl mb-0">
      <thead><tr>
        <th>#</th><th>Umpire</th><th>Ratings</th>
        <th class="text-success">Good</th><th class="text-warning">Avg</th>
        <th class="text-danger">Poor</th><th>Score</th>
      </tr></thead>
      <tbody>
`)
			for i, u := range umpires {
				scoreClass := "text-success fw-bold"
				if u.Score < 2.0 {
					scoreClass = "text-danger fw-bold"
				} else if u.Score < 2.5 {
					scoreClass = "text-warning fw-bold"
				}
				fmt.Fprintf(w,
					`<tr><td class="text-muted">%d</td><td>%s</td><td>%d</td>`+
						`<td class="text-success">%d</td><td class="text-warning">%d</td>`+
						`<td class="text-danger">%d</td><td class="%s">%.2f</td></tr>`,
					i+1, escapeHTML(titleCase(u.Name)), u.Ratings,
					u.Good, u.Average, u.Poor, scoreClass, u.Score,
				)
			}
			fmt.Fprint(w, `      </tbody></table></div></div>`)
		}

		// ── Missing this week ───────────────────────────────────────────────
		if len(missing) > 0 {
			fmt.Fprintf(w, `
<div class="card shadow-sm mb-4 border-warning">
  <div class="card-header fw-semibold text-warning-emphasis bg-warning-subtle">
    Missing Reports — Week %d (%s) — %d requirement(s)
  </div>
  <div class="table-responsive">
    <table class="table table-sm table-hover mb-0">
      <thead><tr><th>Club</th><th>Team</th><th>Opponent</th><th>Match date</th></tr></thead>
      <tbody>
`, currentWeekNum, currentWeekStart.Format("2 Jan"), len(missing))
			for _, m := range missing {
				fmt.Fprintf(w, `<tr><td data-label="Club">%s</td><td data-label="Team">%s</td>`+
					`<td data-label="Opponent">%s</td><td data-label="Match date">%s</td></tr>`,
					escapeHTML(m.Club), escapeHTML(m.Team), escapeHTML(m.Opponent),
					m.MatchDate.Format("2 Jan 2006"))
			}
			fmt.Fprint(w, `      </tbody></table></div></div>`)
		} else {
			fmt.Fprintf(w, `<div class="alert alert-success">All teams with fixtures this week have submitted. Week %d complete.</div>`, currentWeekNum)
		}

		fmt.Fprint(w, `</div>`) // container

		// ── Chart scripts ───────────────────────────────────────────────────
		pitchTrendScript := ""
		if len(pitchTrend) > 1 {
			pitchTrendScript = fmt.Sprintf(`
new Chart(document.getElementById('chartPitchTrend'), {
  type: 'line',
  data: {
    labels: window.__trendLabels,
    datasets: [{
      label: 'Avg Pitch',
      data: window.__pitchAvg,
      borderColor: '#198754',
      backgroundColor: 'rgba(25,135,84,.1)',
      tension: .3, fill: true,
      pointRadius: 4, pointBackgroundColor: '#198754'
    }]
  },
  options: {
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { display: false } },
    scales: { y: { min: 0, max: 5, ticks: { stepSize: 1 } }, x: { grid: { display: false } } }
  }
});`)
		}

		script := fmt.Sprintf(`
Chart.defaults.font.family = "'Segoe UI', system-ui, sans-serif";
Chart.defaults.color = '#6c757d';

new Chart(document.getElementById('chartTrend'), {
  type: 'bar',
  data: {
    labels: window.__trendLabels,
    datasets: [{
      label: 'Submissions',
      data: window.__trendData,
      backgroundColor: 'rgba(196,30,58,.75)',
      borderColor: '#C41E3A',
      borderWidth: 1,
      borderRadius: 4
    }]
  },
  options: {
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { display: false } },
    scales: { y: { beginAtZero: true, ticks: { stepSize: 1 } }, x: { grid: { display: false } } }
  }
});

new Chart(document.getElementById('chartPitch'), {
  type: 'doughnut',
  data: {
    labels: ['Rating 1','Rating 2','Rating 3','Rating 4','Rating 5'],
    datasets: [{
      data: window.__pitchDist,
      backgroundColor: ['#dc3545','#fd7e14','#ffc107','#20c997','#198754'],
      borderWidth: 2, borderColor: '#fff'
    }]
  },
  options: {
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { position: 'bottom', labels: { boxWidth: 12 } } },
    cutout: '60%%'
  }
});

%s
`, pitchTrendScript)

		pageFooterWithScript(w, script)
	}
}

// handleAdminExecReportPrint renders a print-optimised version of the exec report.
func (s *Server) handleAdminExecReportPrint() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		canViewUmpireFeedback := s.adminHasPermission(ctx, adminIDForRequest(r), "view_umpire_feedback")

		currentWeek, err := s.resolveCompetitionWeek(ctx, competitionWeekForDisplay)
		if err != nil {
			http.Error(w, "No competition week is configured.", http.StatusServiceUnavailable)
			return
		}
		seasonID := currentWeek.SeasonID
		seasonName := currentWeek.SeasonName

		seasonProgress, err := s.loadSeasonReportProgress(ctx, seasonID, currentWeek.ComplianceStartWeek)
		if err != nil {
			http.Error(w, "Could not load season report progress.", http.StatusInternalServerError)
			return
		}
		subsReceived := seasonProgress.Submitted
		totalExpected := seasonProgress.Expected
		complianceRate := seasonProgress.completionRate()

		var avgPitch float64
		s.DB.QueryRow(ctx, `SELECT COALESCE(AVG(pitch_rating),0) FROM submissions WHERE season_id=$1`, seasonID).
			Scan(&avgPitch)

		var sanctionsTotal int64
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1`, seasonID).Scan(&sanctionsTotal)

		clubs, err := s.loadSeasonClubReportProgress(ctx, seasonID, currentWeek.ComplianceStartWeek)
		if err != nil {
			http.Error(w, "Could not load club report progress.", http.StatusInternalServerError)
			return
		}

		type umpireRow struct {
			Name    string
			Ratings int64
			Good    int64
			Average int64
			Poor    int64
			Score   float64
		}
		var umpires []umpireRow
		urows, err := s.DB.Query(ctx, `
			WITH latest AS (
			    SELECT DISTINCT ON (team_id, match_date)
			        form_data->>'umpire1_name' AS u1name, form_data->>'umpire1_performance' AS u1perf,
			        form_data->>'umpire2_name' AS u2name, form_data->>'umpire2_performance' AS u2perf
			    FROM submissions WHERE season_id=$1
			    ORDER BY team_id, match_date, submitted_at DESC
			),
			r AS (
			    SELECT lower(trim(u1name)) AS name, u1perf AS perf FROM latest WHERE u1name IS NOT NULL AND u1name <> ''
			    UNION ALL
			    SELECT lower(trim(u2name)), u2perf FROM latest WHERE u2name IS NOT NULL AND u2name <> ''
			)
			SELECT name, COUNT(*), COUNT(*) FILTER (WHERE perf='Good'),
			       COUNT(*) FILTER (WHERE perf='Average'), COUNT(*) FILTER (WHERE perf='Poor'),
			       ROUND((
			           COUNT(*) FILTER (WHERE perf='Good')*3.0 + COUNT(*) FILTER (WHERE perf='Average')*2.0 +
			           COUNT(*) FILTER (WHERE perf='Poor')*1.0
			       ) / NULLIF(COUNT(*) FILTER (WHERE perf IN ('Good','Average','Poor')),0)::numeric, 2) AS score
			FROM r WHERE perf IN ('Good','Average','Poor')
			GROUP BY name HAVING COUNT(*) >= 2
			ORDER BY score DESC NULLS LAST, COUNT(*) DESC LIMIT 10
		`, seasonID)
		if err == nil {
			defer urows.Close()
			for urows.Next() {
				var u umpireRow
				if urows.Scan(&u.Name, &u.Ratings, &u.Good, &u.Average, &u.Poor, &u.Score) == nil {
					umpires = append(umpires, u)
				}
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>GMCL Executive Report — %s</title>
<style>
* { box-sizing: border-box; }
body { font-family: Arial, sans-serif; font-size: 12px; margin: 20mm 15mm; color: #222; }
h1 { font-size: 18px; margin: 0 0 2px; }
p.meta { color: #666; font-size: 11px; margin: 0 0 16px; }
h2 { font-size: 13px; margin: 20px 0 6px; border-bottom: 2px solid #8b0000; padding-bottom: 3px; color: #8b0000; text-transform: uppercase; letter-spacing: .5px; }
table { border-collapse: collapse; width: 100%%; margin-top: 4px; font-size: 11px; }
th { background: #8b0000; color: #fff; padding: 5px 8px; text-align: left; }
td { padding: 4px 8px; border-bottom: 1px solid #eee; }
tr:nth-child(even) td { background: #fafafa; }
.kpis { display: flex; gap: 12px; margin: 12px 0 20px; flex-wrap: wrap; }
.kpi { border: 1px solid #ddd; border-radius: 6px; padding: 10px 16px; min-width: 110px; flex: 1; }
.kpi-num { font-size: 24px; font-weight: bold; color: #8b0000; }
.kpi-label { font-size: 10px; color: #666; text-transform: uppercase; letter-spacing: .5px; }
.badge-ok { color: #1a7a3a; font-weight: bold; }
.badge-warn { color: #856404; font-weight: bold; }
.badge-bad { color: #b02020; font-weight: bold; }
@media print { button { display: none !important; } }
button.print-btn { float: right; padding: 6px 14px; background: #8b0000; color: #fff; border: none; border-radius: 4px; cursor: pointer; font-size: 12px; }
</style>
</head><body>
<button class="print-btn" onclick="window.print()">Print / Save as PDF</button>
<h1>GMCL Executive Report</h1>
<p class="meta">%s &mdash; Generated %s</p>
<div class="kpis">
  <div class="kpi"><div class="kpi-num">%.1f%%</div><div class="kpi-label">Season Completion</div></div>
  <div class="kpi"><div class="kpi-num">%d + %d / %d</div><div class="kpi-label">Received + exempt / required</div></div>
  <div class="kpi"><div class="kpi-num">%.2f</div><div class="kpi-label">Avg Pitch Rating</div></div>
  <div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Sanctions Issued</div></div>
</div>
`,
			seasonName, escapeHTML(seasonName), time.Now().Format("2 Jan 2006 15:04"),
			complianceRate, subsReceived, seasonProgress.Exempt, totalExpected, avgPitch, sanctionsTotal,
		)

		fmt.Fprint(w, `<h2>Club Compliance</h2>
<table><tr><th>Club</th><th>Teams</th><th>Received</th><th>Exempt</th><th>Required</th><th>Missing</th><th>Completion</th><th>Avg Pitch</th></tr>`)
		for _, c := range clubs {
			rate := c.completionRate()
			cls := "badge-ok"
			if rate < 50 {
				cls = "badge-bad"
			} else if rate < 80 {
				cls = "badge-warn"
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td class="%s">%.1f%%</td><td>%.2f</td></tr>`,
				escapeHTML(c.Club), c.Teams, c.Submitted, c.Exempt, c.Expected, c.Missing, cls, rate, c.AvgPitch)
		}
		fmt.Fprint(w, `</table>`)

		if canViewUmpireFeedback && len(umpires) > 0 {
			fmt.Fprint(w, `<h2>Top Umpires</h2>
<table><tr><th>#</th><th>Umpire</th><th>Ratings</th><th>Good</th><th>Average</th><th>Poor</th><th>Score</th></tr>`)
			for i, u := range umpires {
				fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%.2f</td></tr>`,
					i+1, escapeHTML(titleCase(u.Name)), u.Ratings, u.Good, u.Average, u.Poor, u.Score)
			}
			fmt.Fprint(w, `</table>`)
		}

		fmt.Fprint(w, `</body></html>`)
	}
}

// titleCase converts "john smith" → "John Smith".
func titleCase(s string) string {
	if s == "" {
		return s
	}
	result := make([]byte, 0, len(s))
	capitalise := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '-' {
			capitalise = true
			result = append(result, c)
		} else if capitalise {
			if c >= 'a' && c <= 'z' {
				result = append(result, c-32)
			} else {
				result = append(result, c)
			}
			capitalise = false
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}
