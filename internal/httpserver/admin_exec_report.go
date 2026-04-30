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

		// ── Season ─────────────────────────────────────────────────────────
		var seasonID int32
		var seasonName string
		s.DB.QueryRow(ctx, `
			SELECT id, name FROM seasons WHERE is_archived=FALSE ORDER BY id DESC LIMIT 1
		`).Scan(&seasonID, &seasonName)

		// ── Current week ───────────────────────────────────────────────────
		var currentWeekID int32
		var currentWeekNum int32
		var currentWeekStart, currentWeekEnd time.Time
		s.DB.QueryRow(ctx, `
			SELECT id, week_number, start_date, end_date
			FROM weeks WHERE season_id=$1
			ORDER BY abs(start_date - CURRENT_DATE) LIMIT 1
		`, seasonID).Scan(&currentWeekID, &currentWeekNum, &currentWeekStart, &currentWeekEnd)

		// ── KPI 1: season compliance ────────────────────────────────────────
		var weeksElapsed int32
		var subsExpected int64
		var subsReceived int64
		var complianceRate float64

		s.DB.QueryRow(ctx, `
			SELECT COUNT(*) FROM weeks
			WHERE season_id=$1 AND start_date <= CURRENT_DATE
		`, seasonID).Scan(&weeksElapsed)

		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM teams WHERE active=TRUE`).Scan(&subsExpected)

		s.DB.QueryRow(ctx, `
			SELECT COUNT(DISTINCT team_id) FROM submissions WHERE season_id=$1
		`, seasonID).Scan(&subsReceived)

		totalExpected := subsExpected * int64(weeksElapsed)
		if totalExpected > 0 {
			complianceRate = float64(subsReceived) / float64(totalExpected) * 100
			if complianceRate > 100 {
				complianceRate = 100
			}
		}

		// ── KPI 2: avg pitch rating ─────────────────────────────────────────
		var avgPitch float64
		s.DB.QueryRow(ctx, `
			SELECT COALESCE(AVG(pitch_rating),0) FROM submissions WHERE season_id=$1
		`, seasonID).Scan(&avgPitch)

		// ── KPI 3: sanctions ───────────────────────────────────────────────
		var sanctionsTotal int64
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1`, seasonID).Scan(&sanctionsTotal)

		// ── KPI 4: this week submissions ───────────────────────────────────
		var weekSubs int64
		s.DB.QueryRow(ctx, `
			SELECT COUNT(DISTINCT team_id) FROM submissions WHERE week_id=$1
		`, currentWeekID).Scan(&weekSubs)

		// ── Weekly compliance trend ─────────────────────────────────────────
		type weekTrend struct {
			WeekNum int32
			Label   string
			Subs    int64
		}
		var trend []weekTrend
		trows, err := s.DB.Query(ctx, `
			SELECT w.week_number, w.start_date,
			       COUNT(DISTINCT sub.team_id)
			FROM weeks w
			LEFT JOIN submissions sub ON sub.week_id=w.id AND sub.season_id=$1
			WHERE w.season_id=$1 AND w.start_date <= CURRENT_DATE
			GROUP BY w.week_number, w.start_date
			ORDER BY w.week_number
		`, seasonID)
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
			WHERE w.season_id=$1 AND w.start_date <= CURRENT_DATE
			GROUP BY w.week_number ORDER BY w.week_number
		`, seasonID)
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
		type clubRow struct {
			Club     string
			Teams    int64
			Subs     int64
			AvgPitch float64
		}
		var clubs []clubRow
		crowsq, err := s.DB.Query(ctx, `
			SELECT cl.name,
			       COUNT(DISTINCT t.id)           AS teams,
			       COUNT(DISTINCT sub.team_id)    AS subs,
			       COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0) AS avg_pitch
			FROM clubs cl
			JOIN teams t ON t.club_id=cl.id AND t.active=TRUE
			LEFT JOIN submissions sub ON sub.team_id=t.id AND sub.season_id=$1
			GROUP BY cl.name
			ORDER BY cl.name ASC
		`, seasonID)
		if err == nil {
			defer crowsq.Close()
			for crowsq.Next() {
				var cr clubRow
				if crowsq.Scan(&cr.Club, &cr.Teams, &cr.Subs, &cr.AvgPitch) == nil {
					clubs = append(clubs, cr)
				}
			}
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
			Club string
			Team string
		}
		var missing []missingTeam
		mrows, err := s.DB.Query(ctx, `
			SELECT cl.name, t.name
			FROM teams t JOIN clubs cl ON t.club_id=cl.id
			WHERE t.active=TRUE
			  AND t.id NOT IN (SELECT team_id FROM submissions WHERE week_id=$1)
			  AND (
			    t.play_cricket_team_id IS NULL OR t.play_cricket_team_id = ''
			    OR EXISTS (
			        SELECT 1 FROM league_fixtures lf
			        WHERE (lf.home_team_pc_id = t.play_cricket_team_id OR lf.away_team_pc_id = t.play_cricket_team_id)
			          AND lf.match_date BETWEEN $2 AND $3
			    )
			  )
			ORDER BY cl.name, t.name
		`, currentWeekID, currentWeekStart, currentWeekEnd)
		if err == nil {
			defer mrows.Close()
			for mrows.Next() {
				var m missingTeam
				if mrows.Scan(&m.Club, &m.Team) == nil {
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
		writeAdminNav(w, csrfToken, r.URL.Path)

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
      <div class="kpi-label">Season Compliance</div>
      <div class="text-muted small mt-1">%d subs / %d expected</div>
    </div>
  </div>
  <div class="col-6 col-lg-3">
    <div class="card card-kpi kpi-green p-3 text-center h-100">
      <div class="kpi-number">%d / %d</div>
      <div class="kpi-label">This Week</div>
      <div class="text-muted small mt-1">Week %d (%s – %s)</div>
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
			complianceRate, subsReceived, totalExpected,
			weekSubs, subsExpected, currentWeekNum,
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
        <th>Club</th><th>Teams</th><th>Submissions</th>
        <th>Expected</th><th>Compliance</th><th>Avg Pitch</th>
      </tr></thead>
      <tbody>
`)
		for _, c := range clubs {
			expectedTotal := c.Teams * int64(weeksElapsed)
			clubRate := 0.0
			if expectedTotal > 0 {
				clubRate = float64(c.Subs) / float64(expectedTotal) * 100
				if clubRate > 100 {
					clubRate = 100
				}
			}
			badgeClass := "bg-success"
			if clubRate < 50 {
				badgeClass = "bg-danger"
			} else if clubRate < 80 {
				badgeClass = "bg-warning text-dark"
			}
			fmt.Fprintf(w,
				`<tr><td>%s</td><td class="text-muted">%d</td><td>%d</td><td class="text-muted">%d</td>`+
					`<td><span class="badge %s">%.1f%%</span></td><td>%.2f</td></tr>`,
				escapeHTML(c.Club), c.Teams, c.Subs, expectedTotal,
				badgeClass, clubRate, c.AvgPitch,
			)
		}
		if len(clubs) == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No data yet.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody></table></div></div>`)

		// ── Top umpires ─────────────────────────────────────────────────────
		if len(umpires) > 0 {
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
    Missing Submissions — Week %d (%s) — %d team(s)
  </div>
  <div class="table-responsive">
    <table class="table table-sm table-hover mb-0">
      <thead><tr><th>Club</th><th>Team</th></tr></thead>
      <tbody>
`, currentWeekNum, currentWeekStart.Format("2 Jan"), len(missing))
			for _, m := range missing {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`,
					escapeHTML(m.Club), escapeHTML(m.Team))
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

		var seasonID int32
		var seasonName string
		s.DB.QueryRow(ctx, `SELECT id, name FROM seasons WHERE is_archived=FALSE ORDER BY id DESC LIMIT 1`).
			Scan(&seasonID, &seasonName)

		var weeksElapsed int32
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM weeks WHERE season_id=$1 AND start_date <= CURRENT_DATE`, seasonID).
			Scan(&weeksElapsed)

		var subsExpected int64
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM teams WHERE active=TRUE`).Scan(&subsExpected)

		var subsReceived int64
		s.DB.QueryRow(ctx, `SELECT COUNT(DISTINCT team_id) FROM submissions WHERE season_id=$1`, seasonID).
			Scan(&subsReceived)

		totalExpected := subsExpected * int64(weeksElapsed)
		complianceRate := 0.0
		if totalExpected > 0 {
			complianceRate = float64(subsReceived) / float64(totalExpected) * 100
			if complianceRate > 100 {
				complianceRate = 100
			}
		}

		var avgPitch float64
		s.DB.QueryRow(ctx, `SELECT COALESCE(AVG(pitch_rating),0) FROM submissions WHERE season_id=$1`, seasonID).
			Scan(&avgPitch)

		var sanctionsTotal int64
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1`, seasonID).Scan(&sanctionsTotal)

		type clubRow struct {
			Club     string
			Teams    int64
			Subs     int64
			AvgPitch float64
		}
		var clubs []clubRow
		crowsq, err := s.DB.Query(ctx, `
			SELECT cl.name, COUNT(DISTINCT t.id), COUNT(DISTINCT sub.team_id),
			       COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0)
			FROM clubs cl
			JOIN teams t ON t.club_id=cl.id AND t.active=TRUE
			LEFT JOIN submissions sub ON sub.team_id=t.id AND sub.season_id=$1
			GROUP BY cl.name ORDER BY cl.name ASC
		`, seasonID)
		if err == nil {
			defer crowsq.Close()
			for crowsq.Next() {
				var cr clubRow
				if crowsq.Scan(&cr.Club, &cr.Teams, &cr.Subs, &cr.AvgPitch) == nil {
					clubs = append(clubs, cr)
				}
			}
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
  <div class="kpi"><div class="kpi-num">%.1f%%</div><div class="kpi-label">Season Compliance</div></div>
  <div class="kpi"><div class="kpi-num">%d / %d</div><div class="kpi-label">Subs / Expected</div></div>
  <div class="kpi"><div class="kpi-num">%.2f</div><div class="kpi-label">Avg Pitch Rating</div></div>
  <div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Sanctions Issued</div></div>
</div>
`,
			seasonName, escapeHTML(seasonName), time.Now().Format("2 Jan 2006 15:04"),
			complianceRate, subsReceived, totalExpected, avgPitch, sanctionsTotal,
		)

		fmt.Fprint(w, `<h2>Club Compliance</h2>
<table><tr><th>Club</th><th>Teams</th><th>Submissions</th><th>Expected</th><th>Compliance</th><th>Avg Pitch</th></tr>`)
		for _, c := range clubs {
			exp := c.Teams * int64(weeksElapsed)
			rate := 0.0
			if exp > 0 {
				rate = float64(c.Subs) / float64(exp) * 100
				if rate > 100 {
					rate = 100
				}
			}
			cls := "badge-ok"
			if rate < 50 {
				cls = "badge-bad"
			} else if rate < 80 {
				cls = "badge-warn"
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td class="%s">%.1f%%</td><td>%.2f</td></tr>`,
				escapeHTML(c.Club), c.Teams, c.Subs, exp, cls, rate, c.AvgPitch)
		}
		fmt.Fprint(w, `</table>`)

		if len(umpires) > 0 {
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
