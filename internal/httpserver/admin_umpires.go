package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

// handleAdminUmpireRankings renders umpire performance rankings derived from form_data JSONB.
func (s *Server) handleAdminUmpireRankings() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Season selector
		var seasonID int32
		var seasonName string
		if sid := r.URL.Query().Get("season_id"); sid != "" {
			n, _ := strconv.Atoi(sid)
			seasonID = int32(n)
			s.DB.QueryRow(ctx, `SELECT name FROM seasons WHERE id=$1`, seasonID).Scan(&seasonName)
		}
		if seasonID == 0 {
			s.DB.QueryRow(ctx, `
				SELECT s.id, s.name FROM weeks w JOIN seasons s ON w.season_id=s.id
				WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date LIMIT 1
			`).Scan(&seasonID, &seasonName)
		}

		minRatings := 2
		if mr := r.URL.Query().Get("min_ratings"); mr != "" {
			if n, err := strconv.Atoi(mr); err == nil && n >= 1 {
				minRatings = n
			}
		}

		// All seasons for the selector
		type season struct {
			ID   int32
			Name string
		}
		var seasons []season
		srows, _ := s.DB.Query(ctx, `SELECT id, name FROM seasons ORDER BY start_date DESC`)
		if srows != nil {
			defer srows.Close()
			for srows.Next() {
				var ss season
				if srows.Scan(&ss.ID, &ss.Name) == nil {
					seasons = append(seasons, ss)
				}
			}
		}

		// Umpire performance query
		type umpireRow struct {
			Name         string
			Total        int64
			Good         int64
			Average      int64
			Poor         int64
			Score        float64
			GoodPct      float64
			CommentCount int64
		}
		var umpires []umpireRow
		var totalRatings, uniqueUmpires int64

		if seasonID > 0 {
			urows, err := s.DB.Query(ctx, `
				WITH deduped AS (
				    SELECT DISTINCT ON (team_id, match_date)
				        form_data->>'umpire1_name'        AS u1name,
				        form_data->>'umpire1_performance' AS u1perf,
				        form_data->>'umpire2_name'        AS u2name,
				        form_data->>'umpire2_performance' AS u2perf,
				        COALESCE(form_data->>'umpire_comments','') AS comment
				    FROM submissions
				    WHERE season_id = $1
				    ORDER BY team_id, match_date, submitted_at DESC
				),
				ratings AS (
				    SELECT lower(trim(u1name))  AS key,
				           trim(u1name)         AS display,
				           u1perf               AS perf,
				           comment
				    FROM deduped
				    WHERE u1name IS NOT NULL AND trim(u1name) <> ''
				      AND u1perf IS NOT NULL
				      AND u1perf IN ('Good','Average','Poor')
				    UNION ALL
				    SELECT lower(trim(u2name)),
				           trim(u2name),
				           u2perf,
				           comment
				    FROM deduped
				    WHERE u2name IS NOT NULL AND trim(u2name) <> ''
				      AND u2perf IS NOT NULL
				      AND u2perf IN ('Good','Average','Poor')
				),
				scored AS (
				    SELECT
				        key,
				        mode() WITHIN GROUP (ORDER BY display)             AS umpire_name,
				        COUNT(*)                                            AS total,
				        COUNT(*) FILTER (WHERE perf = 'Good')              AS good,
				        COUNT(*) FILTER (WHERE perf = 'Average')           AS avg_c,
				        COUNT(*) FILTER (WHERE perf = 'Poor')              AS poor,
				        ROUND((
				            COUNT(*) FILTER (WHERE perf='Good')    * 3.0 +
				            COUNT(*) FILTER (WHERE perf='Average') * 2.0 +
				            COUNT(*) FILTER (WHERE perf='Poor')    * 1.0
				        ) / NULLIF(COUNT(*),0), 3)                         AS score,
				        COUNT(*) FILTER (WHERE comment <> '')              AS comment_count
				    FROM ratings
				    GROUP BY key
				    HAVING COUNT(*) >= $2
				)
				SELECT umpire_name, total, good, avg_c, poor, COALESCE(score,0), comment_count
				FROM scored
				ORDER BY score DESC NULLS LAST, total DESC
			`, seasonID, minRatings)
			if err == nil {
				defer urows.Close()
				for urows.Next() {
					var u umpireRow
					if e := urows.Scan(&u.Name, &u.Total, &u.Good, &u.Average, &u.Poor, &u.Score, &u.CommentCount); e == nil {
						if u.Total > 0 {
							u.GoodPct = float64(u.Good) / float64(u.Total) * 100
						}
						umpires = append(umpires, u)
						totalRatings += u.Total
						uniqueUmpires++
					}
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		// Chart data
		var chartLabels, chartScores, chartGoodPct []string
		limit := 15
		if len(umpires) < limit {
			limit = len(umpires)
		}
		for i := 0; i < limit; i++ {
			u := umpires[i]
			lb, _ := json.Marshal(u.Name)
			chartLabels = append(chartLabels, string(lb))
			chartScores = append(chartScores, fmt.Sprintf("%.3f", u.Score))
			chartGoodPct = append(chartGoodPct, fmt.Sprintf("%.1f", u.GoodPct))
		}
		labelsJSON := "[" + joinStrings(chartLabels, ",") + "]"
		scoresJSON := "[" + joinStrings(chartScores, ",") + "]"
		goodPctJSON := "[" + joinStrings(chartGoodPct, ",") + "]"

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHeadWithCharts(w, "Umpire Rankings")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)

		// Header + season selector
		fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Umpire Rankings</h4>
    <p class="text-muted mb-0 small">Performance ratings from captain reports &mdash; min %d ratings to appear</p>
  </div>
  <form method="GET" action="/admin/rankings/umpires" class="d-flex gap-2 align-items-center">
    <select name="season_id" class="form-select form-select-sm" onchange="this.form.submit()">
`, minRatings)
		for _, ss := range seasons {
			sel := ""
			if ss.ID == seasonID {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, ss.ID, sel, escapeHTML(ss.Name))
		}
		fmt.Fprintf(w, `    </select>
    <input type="hidden" name="min_ratings" value="%d">
  </form>
</div>
`, minRatings)

		// Summary KPI strip
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-blue text-center p-3">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Umpires Rated</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-teal text-center p-3">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Total Ratings</div>
    </div>
  </div>
</div>
`, uniqueUmpires, totalRatings)

		// Chart
		fmt.Fprint(w, `
<div class="row g-3 mb-4">
  <div class="col-12 col-xl-7">
    <div class="card shadow-sm">
      <div class="card-header fw-semibold">Top 15 Umpires — Score (1.0–3.0)</div>
      <div class="card-body">
        <div class="chart-container-lg"><canvas id="chartUmpireScore"></canvas></div>
      </div>
    </div>
  </div>
  <div class="col-12 col-xl-5">
    <div class="card shadow-sm">
      <div class="card-header fw-semibold">Top 15 — Good Rating %</div>
      <div class="card-body">
        <div class="chart-container-lg"><canvas id="chartUmpireGood"></canvas></div>
      </div>
    </div>
  </div>
</div>
`)

		// Rankings table
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">All Rated Umpires</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>#</th><th>Umpire</th><th>Ratings</th>
        <th class="text-success">Good</th>
        <th class="text-warning">Average</th>
        <th class="text-danger">Poor</th>
        <th>Score</th><th>Performance Bar</th><th></th>
      </tr></thead>
      <tbody>
`)
		for i, u := range umpires {
			scoreClass := "text-success"
			if u.Score < 2.0 {
				scoreClass = "text-danger"
			} else if u.Score < 2.5 {
				scoreClass = "text-warning"
			}
			barGood := int(u.GoodPct)
			barAvg := 0
			if u.Total > 0 {
				barAvg = int(float64(u.Average) / float64(u.Total) * 100)
			}
			barPoor := 100 - barGood - barAvg
			if barPoor < 0 {
				barPoor = 0
			}
			commentURL := "/admin/umpires/" + url.PathEscape(u.Name) + "/comments?season_id=" + strconv.Itoa(int(seasonID))
			commentBtn := fmt.Sprintf(`<a href="%s" class="btn btn-outline-secondary btn-sm py-0 px-2" style="font-size:.75rem">Comments</a>`, commentURL)
			if u.CommentCount > 0 {
				commentBtn = fmt.Sprintf(`<a href="%s" class="btn btn-warning btn-sm py-0 px-2 fw-semibold" style="font-size:.75rem">%d comment(s)</a>`, commentURL, u.CommentCount)
			}
			fmt.Fprintf(w, `
<tr>
  <td class="text-muted">%d</td>
  <td><strong>%s</strong></td>
  <td>%d</td>
  <td class="text-success">%d</td>
  <td class="text-warning">%d</td>
  <td class="text-danger">%d</td>
  <td><span class="%s fw-bold">%.2f</span></td>
  <td style="min-width:120px">
    <div class="progress" style="height:8px;border-radius:4px">
      <div class="progress-bar bg-success" style="width:%d%%"></div>
      <div class="progress-bar bg-warning" style="width:%d%%"></div>
      <div class="progress-bar bg-danger"  style="width:%d%%"></div>
    </div>
  </td>
  <td>%s</td>
</tr>`,
				i+1, escapeHTML(u.Name), u.Total,
				u.Good, u.Average, u.Poor,
				scoreClass, u.Score,
				barGood, barAvg, barPoor,
				commentBtn)
		}
		if len(umpires) == 0 {
			fmt.Fprint(w, `<tr><td colspan="9" class="text-center text-muted py-3">No umpire ratings found for this season.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>`)

		script := fmt.Sprintf(`
Chart.defaults.font.family = "'Segoe UI', system-ui, sans-serif";
Chart.defaults.color = '#6c757d';

new Chart(document.getElementById('chartUmpireScore'), {
  type: 'bar',
  data: {
    labels: %s,
    datasets: [{
      label: 'Score',
      data: %s,
      backgroundColor: function(ctx) {
        var v = ctx.raw;
        return v >= 2.5 ? 'rgba(25,135,84,.75)' : v >= 2.0 ? 'rgba(255,193,7,.8)' : 'rgba(220,53,69,.75)';
      },
      borderRadius: 4
    }]
  },
  options: {
    indexAxis: 'y',
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { display: false } },
    scales: {
      x: { min: 1, max: 3, ticks: { stepSize: .5 }, grid: { color: 'rgba(0,0,0,.05)' } },
      y: { grid: { display: false } }
    }
  }
});

new Chart(document.getElementById('chartUmpireGood'), {
  type: 'bar',
  data: {
    labels: %s,
    datasets: [{
      label: 'Good %%',
      data: %s,
      backgroundColor: 'rgba(25,135,84,.7)',
      borderRadius: 4
    }]
  },
  options: {
    indexAxis: 'y',
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { display: false } },
    scales: {
      x: { min: 0, max: 100, ticks: { callback: v => v+'%%' } },
      y: { grid: { display: false } }
    }
  }
});
`, labelsJSON, scoresJSON, labelsJSON, goodPctJSON)

		pageFooterWithScript(w, script)
	}
}

// handleAdminUmpireComments renders all free-text comments for a named umpire this season.
func (s *Server) handleAdminUmpireComments() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		umpireName := r.PathValue("name")
		if umpireName == "" {
			http.NotFound(w, r)
			return
		}

		var seasonID int32
		var seasonName string
		if sid := r.URL.Query().Get("season_id"); sid != "" {
			n, _ := strconv.Atoi(sid)
			seasonID = int32(n)
			s.DB.QueryRow(ctx, `SELECT name FROM seasons WHERE id=$1`, seasonID).Scan(&seasonName)
		}
		if seasonID == 0 {
			s.DB.QueryRow(ctx, `
				SELECT s.id, s.name FROM weeks w JOIN seasons s ON w.season_id=s.id
				WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date LIMIT 1
			`).Scan(&seasonID, &seasonName)
		}

		type commentRow struct {
			SubID     int64
			MatchDate time.Time
			Club      string
			Comment   string
		}
		var comments []commentRow

		crows, err := s.DB.Query(ctx, `
			WITH latest AS (
			    SELECT DISTINCT ON (team_id, match_date)
			        id, team_id, match_date,
			        COALESCE(form_data->>'umpire_comments','') AS comment,
			        lower(trim(form_data->>'umpire1_name'))    AS u1,
			        lower(trim(form_data->>'umpire2_name'))    AS u2
			    FROM submissions
			    WHERE season_id = $1
			    ORDER BY team_id, match_date, submitted_at DESC
			)
			SELECT l.id, l.match_date, cl.name, l.comment
			FROM latest l
			JOIN teams t  ON t.id  = l.team_id
			JOIN clubs cl ON cl.id = t.club_id
			WHERE (l.u1 = lower($2) OR l.u2 = lower($2))
			  AND l.comment <> ''
			ORDER BY l.match_date DESC
		`, seasonID, umpireName)
		if err == nil {
			defer crows.Close()
			for crows.Next() {
				var c commentRow
				if e := crows.Scan(&c.SubID, &c.MatchDate, &c.Club, &c.Comment); e == nil {
					comments = append(comments, c)
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Umpire Comments")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprintf(w, `<div class="container-fluid px-4">
<nav aria-label="breadcrumb" class="mb-3">
  <ol class="breadcrumb">
    <li class="breadcrumb-item"><a href="/admin/rankings/umpires">Umpire Rankings</a></li>
    <li class="breadcrumb-item active">%s</li>
  </ol>
</nav>
<h4 class="fw-bold mb-1">%s</h4>
<p class="text-muted mb-4 small">Captain comments &mdash; %s</p>
`, escapeHTML(umpireName), escapeHTML(umpireName), escapeHTML(seasonName))

		if len(comments) == 0 {
			fmt.Fprint(w, `<div class="alert alert-info">No comments recorded for this umpire.</div>`)
		} else {
			for _, c := range comments {
				fmt.Fprintf(w, `
<div class="card shadow-sm mb-3">
  <div class="card-body">
    <div class="d-flex justify-content-between align-items-start mb-2">
      <span class="fw-semibold">%s</span>
      <span class="text-muted small">%s &mdash; <a href="/admin/submissions/%d">#%d</a></span>
    </div>
    <p class="mb-0">%s</p>
  </div>
</div>`, escapeHTML(c.Club), c.MatchDate.Format("2 Jan 2006"), c.SubID, c.SubID, escapeHTML(c.Comment))
			}
		}
		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}

func joinStrings(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
