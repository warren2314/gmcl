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

func (s *Server) loadUmpireRankings(ctx context.Context, whereSQL string, args []any, minRatings int64, umpireType string) []reportUmpire {
	if minRatings < 1 {
		minRatings = 1
	}
	qargs := append([]any{}, args...)
	typeWhere := ""
	if umpireType == "panel" || umpireType == "club" {
		typeParam := len(qargs) + 1
		qargs = append(qargs, umpireType)
		typeWhere = fmt.Sprintf("AND %%s = $%d", typeParam)
	}
	u1TypeWhere := ""
	u2TypeWhere := ""
	if typeWhere != "" {
		u1TypeWhere = fmt.Sprintf(typeWhere, "u1type")
		u2TypeWhere = fmt.Sprintf(typeWhere, "u2type")
	}
	minParam := len(qargs) + 1
	qargs = append(qargs, minRatings)
	rows, err := s.DB.Query(ctx, fmt.Sprintf(`
		WITH deduped AS (
		    SELECT DISTINCT ON (sub.team_id, sub.match_date)
		        trim(sub.form_data->>'umpire1_name')        AS u1name,
		        sub.form_data->>'umpire1_performance'       AS u1perf,
		        COALESCE(NULLIF(sub.umpire1_type, ''), NULLIF(sub.form_data->>'umpire1_type', ''), 'panel') AS u1type,
		        CASE WHEN sub.form_data->>'decision_making_umpire1'  ~ '^[1-5]$' THEN (sub.form_data->>'decision_making_umpire1')::int  ELSE NULL END AS u1_dm,
		        CASE WHEN sub.form_data->>'match_management_umpire1' ~ '^[1-5]$' THEN (sub.form_data->>'match_management_umpire1')::int ELSE NULL END AS u1_mm,
		        CASE WHEN sub.form_data->>'player_management_umpire1'~ '^[1-5]$' THEN (sub.form_data->>'player_management_umpire1')::int ELSE NULL END AS u1_pm,
		        CASE WHEN sub.form_data->>'presence_image_umpire1'   ~ '^[1-5]$' THEN (sub.form_data->>'presence_image_umpire1')::int   ELSE NULL END AS u1_pi,
		        CASE WHEN sub.form_data->>'teamwork_umpire1'          ~ '^[1-5]$' THEN (sub.form_data->>'teamwork_umpire1')::int          ELSE NULL END AS u1_tw,
		        trim(sub.form_data->>'umpire2_name')        AS u2name,
		        sub.form_data->>'umpire2_performance'       AS u2perf,
		        COALESCE(NULLIF(sub.umpire2_type, ''), NULLIF(sub.form_data->>'umpire2_type', ''), 'panel') AS u2type,
		        CASE WHEN sub.form_data->>'decision_making_umpire2'  ~ '^[1-5]$' THEN (sub.form_data->>'decision_making_umpire2')::int  ELSE NULL END AS u2_dm,
		        CASE WHEN sub.form_data->>'match_management_umpire2' ~ '^[1-5]$' THEN (sub.form_data->>'match_management_umpire2')::int ELSE NULL END AS u2_mm,
		        CASE WHEN sub.form_data->>'player_management_umpire2'~ '^[1-5]$' THEN (sub.form_data->>'player_management_umpire2')::int ELSE NULL END AS u2_pm,
		        CASE WHEN sub.form_data->>'presence_image_umpire2'   ~ '^[1-5]$' THEN (sub.form_data->>'presence_image_umpire2')::int   ELSE NULL END AS u2_pi,
		        CASE WHEN sub.form_data->>'teamwork_umpire2'          ~ '^[1-5]$' THEN (sub.form_data->>'teamwork_umpire2')::int          ELSE NULL END AS u2_tw,
		        COALESCE(sub.form_data->>'umpire_comments','') AS comment
		    FROM submissions sub
		    JOIN weeks w ON w.id=sub.week_id
		    WHERE %s
		    ORDER BY sub.team_id, sub.match_date, sub.submitted_at DESC
		),
		ratings AS (
		    SELECT lower(trim(u1name)) AS key,
		           trim(u1name)        AS display,
		           u1perf              AS perf,
		           u1type              AS umpire_type,
		           comment,
		           CASE WHEN u1_dm IS NOT NULL AND u1_mm IS NOT NULL AND u1_pm IS NOT NULL AND u1_pi IS NOT NULL AND u1_tw IS NOT NULL
		                THEN (u1_dm + u1_mm + u1_pm + u1_pi + u1_tw) ELSE NULL END AS total_score
		    FROM deduped
		    WHERE u1name IS NOT NULL AND trim(u1name) <> ''
		      AND u1perf IS NOT NULL
		      AND u1perf IN ('Good','Average','Poor')
		      %s
		    UNION ALL
		    SELECT lower(trim(u2name)),
		           trim(u2name),
		           u2perf,
		           u2type,
		           comment,
		           CASE WHEN u2_dm IS NOT NULL AND u2_mm IS NOT NULL AND u2_pm IS NOT NULL AND u2_pi IS NOT NULL AND u2_tw IS NOT NULL
		                THEN (u2_dm + u2_mm + u2_pm + u2_pi + u2_tw) ELSE NULL END
		    FROM deduped
		    WHERE u2name IS NOT NULL AND trim(u2name) <> ''
		      AND u2perf IS NOT NULL
		      AND u2perf IN ('Good','Average','Poor')
		      %s
		),
		scored AS (
		    SELECT
		        key,
		        mode() WITHIN GROUP (ORDER BY display)       AS umpire_name,
		        COUNT(*)                                      AS total,
		        COUNT(*) FILTER (WHERE perf = 'Good')         AS good,
		        COUNT(*) FILTER (WHERE perf = 'Average')      AS avg_c,
		        COUNT(*) FILTER (WHERE perf = 'Poor')         AS poor,
		        ROUND((
		            COUNT(*) FILTER (WHERE perf='Good')    * 3.0 +
		            COUNT(*) FILTER (WHERE perf='Average') * 2.0 +
		            COUNT(*) FILTER (WHERE perf='Poor')    * 1.0
		        ) / NULLIF(COUNT(*),0), 3)                   AS score,
		        COUNT(*) FILTER (WHERE comment <> '')        AS comment_count,
		        ROUND(AVG(total_score), 1)                   AS avg_score_25
		    FROM ratings
		    GROUP BY key
		    HAVING COUNT(*) >= $%d
		)
		SELECT umpire_name, total, good, avg_c, poor, COALESCE(score,0), comment_count, COALESCE(avg_score_25,0)
		FROM scored
		ORDER BY score DESC NULLS LAST, total DESC, umpire_name
	`, whereSQL, u1TypeWhere, u2TypeWhere, minParam), qargs...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var umpires []reportUmpire
	for rows.Next() {
		var u reportUmpire
		if e := rows.Scan(&u.Name, &u.Ratings, &u.Good, &u.Average, &u.Poor, &u.Score, &u.CommentCount, &u.AvgScore25); e == nil {
			if u.Ratings > 0 {
				u.GoodPct = float64(u.Good) / float64(u.Ratings) * 100
			}
			umpires = append(umpires, u)
		}
	}
	return umpires
}

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
		category := r.URL.Query().Get("category")
		if category != "club" {
			category = "panel"
		}
		categoryTitle := "Panel Umpires"
		otherCategory := "club"
		otherCategoryTitle := "Club Umpires"
		if category == "club" {
			categoryTitle = "Club Umpires"
			otherCategory = "panel"
			otherCategoryTitle = "Panel Umpires"
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

		// Umpire performance query, split by whether the captain marked each
		// official as panel-appointed or club-provided.
		var umpires, panelUmpires, clubUmpires []reportUmpire
		var totalRatings, uniqueUmpires int64

		if seasonID > 0 {
			panelUmpires = s.loadUmpireRankings(ctx, "sub.season_id=$1", []any{seasonID}, int64(minRatings), "panel")
			clubUmpires = s.loadUmpireRankings(ctx, "sub.season_id=$1", []any{seasonID}, int64(minRatings), "club")
			if category == "club" {
				umpires = clubUmpires
			} else {
				umpires = panelUmpires
			}
			for _, u := range umpires {
				totalRatings += u.Ratings
				uniqueUmpires++
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
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		fmt.Fprint(w, `<div class="container-fluid px-4">`)

		// Header + season selector
		fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">%s</h4>
    <p class="text-muted mb-0 small">Performance ratings from captain reports &mdash; min %d ratings to appear</p>
  </div>
  <form method="GET" action="/admin/rankings/umpires" class="d-flex gap-2 align-items-center">
    <select name="season_id" class="form-select form-select-sm" onchange="this.form.submit()">
`, escapeHTML(categoryTitle), minRatings)
		for _, ss := range seasons {
			sel := ""
			if ss.ID == seasonID {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, ss.ID, sel, escapeHTML(ss.Name))
		}
		fmt.Fprintf(w, `    </select>
    <input type="hidden" name="min_ratings" value="%d">
    <input type="hidden" name="category" value="%s">
  </form>
</div>
`, minRatings, escapeHTML(category))

		// Summary KPI strip
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-blue text-center p-3">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">%s Rated</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-teal text-center p-3">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Total Ratings</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <a class="text-decoration-none" href="/admin/rankings/umpires?season_id=%d&amp;min_ratings=%d&amp;category=panel">
      <div class="card card-kpi %s text-center p-3">
        <div class="kpi-number">%d</div>
        <div class="kpi-label">Panel Umpires</div>
      </div>
    </a>
  </div>
  <div class="col-6 col-md-3">
    <a class="text-decoration-none" href="/admin/rankings/umpires?season_id=%d&amp;min_ratings=%d&amp;category=club">
      <div class="card card-kpi %s text-center p-3">
        <div class="kpi-number">%d</div>
        <div class="kpi-label">Club Umpires</div>
      </div>
    </a>
  </div>
</div>
`, uniqueUmpires, escapeHTML(categoryTitle), totalRatings,
			seasonID, minRatings, umpireCategoryCardClass(category, "panel"), len(panelUmpires),
			seasonID, minRatings, umpireCategoryCardClass(category, "club"), len(clubUmpires))

		// Chart
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-12 col-xl-7">
    <div class="card shadow-sm">
      <div class="card-header fw-semibold">Top 15 %s - Score (1.0-3.0)</div>
      <div class="card-body">
        <div class="chart-container-lg"><canvas id="chartUmpireScore"></canvas></div>
      </div>
    </div>
  </div>
  <div class="col-12 col-xl-5">
    <div class="card shadow-sm">
      <div class="card-header fw-semibold">Top 15 %s - Good Rating %%</div>
      <div class="card-body">
        <div class="chart-container-lg"><canvas id="chartUmpireGood"></canvas></div>
      </div>
    </div>
  </div>
</div>
`, escapeHTML(categoryTitle), escapeHTML(categoryTitle))

		// Rankings table
		fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header d-flex align-items-center gap-3 py-2">
    <span class="fw-semibold me-auto">All Rated %s</span>
    <input type="search" id="umpireSearch" class="form-control form-control-sm" style="max-width:240px"
           placeholder="Search umpire name…" oninput="filterUmpires(this.value)" autocomplete="off">
  </div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>#</th><th>Umpire</th><th>Ratings</th>
        <th class="text-success">Good</th>
        <th class="text-warning">Average</th>
        <th class="text-danger">Poor</th>
        <th>Score</th><th title="Average total score out of 25 per game">Avg/25</th><th>Performance Bar</th><th></th>
      </tr></thead>
      <tbody id="umpireTableBody">
`, escapeHTML(categoryTitle))
		for i, u := range umpires {
			scoreClass := "text-success"
			if u.Score < 2.0 {
				scoreClass = "text-danger"
			} else if u.Score < 2.5 {
				scoreClass = "text-warning"
			}
			avg25Class := "text-success"
			if u.AvgScore25 > 0 {
				if u.AvgScore25 < 15 {
					avg25Class = "text-danger"
				} else if u.AvgScore25 < 20 {
					avg25Class = "text-warning"
				}
			}
			barGood := int(u.GoodPct)
			barAvg := 0
			if u.Ratings > 0 {
				barAvg = int(float64(u.Average) / float64(u.Ratings) * 100)
			}
			barPoor := 100 - barGood - barAvg
			if barPoor < 0 {
				barPoor = 0
			}
			commentURL := "/admin/umpires/" + url.PathEscape(u.Name) + "/comments?season_id=" + strconv.Itoa(int(seasonID)) + "&category=" + url.QueryEscape(category)
			scoresURL := "/admin/umpires/" + url.PathEscape(u.Name) + "/scores?season_id=" + strconv.Itoa(int(seasonID)) + "&category=" + url.QueryEscape(category)
			commentBtn := fmt.Sprintf(`<a href="%s" class="btn btn-outline-secondary btn-sm py-0 px-2" style="font-size:.75rem">Comments</a>`, commentURL)
			if u.CommentCount > 0 {
				commentBtn = fmt.Sprintf(`<a href="%s" class="btn btn-warning btn-sm py-0 px-2 fw-semibold" style="font-size:.75rem">%d comment(s)</a>`, commentURL, u.CommentCount)
			}
			avg25Cell := `<span class="text-muted">—</span>`
			if u.AvgScore25 > 0 {
				avg25Cell = fmt.Sprintf(`<span class="%s fw-bold">%.1f</span>`, avg25Class, u.AvgScore25)
			}
			fmt.Fprintf(w, `
<tr>
  <td class="text-muted">%d</td>
  <td><strong><a href="%s" class="text-decoration-none">%s</a></strong></td>
  <td>%d</td>
  <td class="text-success">%d</td>
  <td class="text-warning">%d</td>
  <td class="text-danger">%d</td>
  <td><span class="%s fw-bold">%.2f</span></td>
  <td>%s</td>
  <td style="min-width:120px">
    <div class="progress" style="height:8px;border-radius:4px">
      <div class="progress-bar bg-success" style="width:%d%%"></div>
      <div class="progress-bar bg-warning" style="width:%d%%"></div>
      <div class="progress-bar bg-danger"  style="width:%d%%"></div>
    </div>
  </td>
  <td class="d-flex gap-1">%s <a href="%s" class="btn btn-outline-primary btn-sm py-0 px-2" style="font-size:.75rem">Scores</a></td>
</tr>`,
				i+1, scoresURL, escapeHTML(u.Name), u.Ratings,
				u.Good, u.Average, u.Poor,
				scoreClass, u.Score,
				avg25Cell,
				barGood, barAvg, barPoor,
				commentBtn, scoresURL)
		}
		if len(umpires) == 0 {
			fmt.Fprintf(w, `<tr><td colspan="10" class="text-center text-muted py-3">No %s ratings found for this season. <a href="/admin/rankings/umpires?season_id=%d&amp;min_ratings=%d&amp;category=%s">View %s</a>.</td></tr>`,
				escapeHTML(categoryTitle), seasonID, minRatings, escapeHTML(otherCategory), escapeHTML(otherCategoryTitle))
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

		script += `
function filterUmpires(q) {
  q = q.toLowerCase();
  var rows = document.querySelectorAll('#umpireTableBody tr');
  var visible = 0;
  rows.forEach(function(row) {
    var show = !q || row.textContent.toLowerCase().indexOf(q) !== -1;
    row.style.display = show ? '' : 'none';
    if (show) visible++;
  });
  var empty = document.getElementById('umpireSearchEmpty');
  if (!empty) {
    empty = document.createElement('tr');
    empty.id = 'umpireSearchEmpty';
    empty.innerHTML = '<td colspan="10" class="text-center text-muted py-3">No umpires match your search.</td>';
    document.getElementById('umpireTableBody').appendChild(empty);
  }
  empty.style.display = (visible === 0 && q) ? '' : 'none';
}
`
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
		category := r.URL.Query().Get("category")
		if category != "club" {
			category = "panel"
		}
		categoryTitle := "Panel Umpire"
		if category == "club" {
			categoryTitle = "Club Umpire"
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
			        COALESCE(NULLIF(umpire1_type, ''), NULLIF(form_data->>'umpire1_type', ''), 'panel') AS u1type,
			        lower(trim(form_data->>'umpire2_name'))    AS u2,
			        COALESCE(NULLIF(umpire2_type, ''), NULLIF(form_data->>'umpire2_type', ''), 'panel') AS u2type
			    FROM submissions
			    WHERE season_id = $1
			    ORDER BY team_id, match_date, submitted_at DESC
			)
			SELECT l.id, l.match_date, cl.name, l.comment
			FROM latest l
			JOIN teams t  ON t.id  = l.team_id
			JOIN clubs cl ON cl.id = t.club_id
			WHERE ((l.u1 = lower($2) AND l.u1type = $3) OR (l.u2 = lower($2) AND l.u2type = $3))
			  AND l.comment <> ''
			ORDER BY l.match_date DESC
		`, seasonID, umpireName, category)
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
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		backURL := fmt.Sprintf("/admin/rankings/umpires?season_id=%d&category=%s", seasonID, url.QueryEscape(category))
		fmt.Fprintf(w, `<div class="container-fluid px-4">
<nav aria-label="breadcrumb" class="mb-3">
  <ol class="breadcrumb">
    <li class="breadcrumb-item"><a href="%s">Umpire Rankings</a></li>
    <li class="breadcrumb-item active">%s</li>
  </ol>
</nav>
<h4 class="fw-bold mb-1">%s</h4>
<p class="text-muted mb-4 small">%s comments &mdash; %s</p>
`, escapeHTML(backURL), escapeHTML(umpireName), escapeHTML(umpireName), escapeHTML(categoryTitle), escapeHTML(seasonName))

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

// handleAdminUmpireScores renders the per-game scoring breakdown for a named umpire.
func (s *Server) handleAdminUmpireScores() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		umpireName := r.PathValue("name")
		if umpireName == "" {
			http.NotFound(w, r)
			return
		}
		category := r.URL.Query().Get("category")
		if category != "club" {
			category = "panel"
		}
		categoryTitle := "Panel Umpire"
		if category == "club" {
			categoryTitle = "Club Umpire"
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

		type scoreRow struct {
			SubID    int64
			Date     time.Time
			Club     string
			Perf     string
			DM       *int32
			MM       *int32
			PM       *int32
			PI       *int32
			TW       *int32
		}

		var rows []scoreRow
		dbRows, err := s.DB.Query(ctx, `
			WITH latest AS (
			    SELECT DISTINCT ON (sub.team_id, sub.match_date)
			        sub.id,
			        sub.team_id,
			        sub.match_date,
			        lower(trim(sub.form_data->>'umpire1_name')) AS u1,
			        sub.form_data->>'umpire1_performance'       AS u1perf,
			        COALESCE(NULLIF(sub.umpire1_type,''), NULLIF(sub.form_data->>'umpire1_type',''), 'panel') AS u1type,
			        CASE WHEN sub.form_data->>'decision_making_umpire1'  ~ '^[1-5]$' THEN (sub.form_data->>'decision_making_umpire1')::int  END AS u1_dm,
			        CASE WHEN sub.form_data->>'match_management_umpire1' ~ '^[1-5]$' THEN (sub.form_data->>'match_management_umpire1')::int END AS u1_mm,
			        CASE WHEN sub.form_data->>'player_management_umpire1'~ '^[1-5]$' THEN (sub.form_data->>'player_management_umpire1')::int END AS u1_pm,
			        CASE WHEN sub.form_data->>'presence_image_umpire1'   ~ '^[1-5]$' THEN (sub.form_data->>'presence_image_umpire1')::int   END AS u1_pi,
			        CASE WHEN sub.form_data->>'teamwork_umpire1'          ~ '^[1-5]$' THEN (sub.form_data->>'teamwork_umpire1')::int          END AS u1_tw,
			        lower(trim(sub.form_data->>'umpire2_name')) AS u2,
			        sub.form_data->>'umpire2_performance'       AS u2perf,
			        COALESCE(NULLIF(sub.umpire2_type,''), NULLIF(sub.form_data->>'umpire2_type',''), 'panel') AS u2type,
			        CASE WHEN sub.form_data->>'decision_making_umpire2'  ~ '^[1-5]$' THEN (sub.form_data->>'decision_making_umpire2')::int  END AS u2_dm,
			        CASE WHEN sub.form_data->>'match_management_umpire2' ~ '^[1-5]$' THEN (sub.form_data->>'match_management_umpire2')::int END AS u2_mm,
			        CASE WHEN sub.form_data->>'player_management_umpire2'~ '^[1-5]$' THEN (sub.form_data->>'player_management_umpire2')::int END AS u2_pm,
			        CASE WHEN sub.form_data->>'presence_image_umpire2'   ~ '^[1-5]$' THEN (sub.form_data->>'presence_image_umpire2')::int   END AS u2_pi,
			        CASE WHEN sub.form_data->>'teamwork_umpire2'          ~ '^[1-5]$' THEN (sub.form_data->>'teamwork_umpire2')::int          END AS u2_tw
			    FROM submissions sub
			    WHERE sub.season_id = $1
			    ORDER BY sub.team_id, sub.match_date, sub.submitted_at DESC
			)
			SELECT
			    l.id,
			    l.match_date,
			    cl.name AS club,
			    CASE WHEN l.u1 = lower($2) AND l.u1type = $3 THEN l.u1perf ELSE l.u2perf END AS perf,
			    CASE WHEN l.u1 = lower($2) AND l.u1type = $3 THEN l.u1_dm ELSE l.u2_dm END AS dm,
			    CASE WHEN l.u1 = lower($2) AND l.u1type = $3 THEN l.u1_mm ELSE l.u2_mm END AS mm,
			    CASE WHEN l.u1 = lower($2) AND l.u1type = $3 THEN l.u1_pm ELSE l.u2_pm END AS pm,
			    CASE WHEN l.u1 = lower($2) AND l.u1type = $3 THEN l.u1_pi ELSE l.u2_pi END AS pi,
			    CASE WHEN l.u1 = lower($2) AND l.u1type = $3 THEN l.u1_tw ELSE l.u2_tw END AS tw
			FROM latest l
			JOIN teams t  ON t.id  = l.team_id
			JOIN clubs cl ON cl.id = t.club_id
			WHERE (l.u1 = lower($2) AND l.u1type = $3) OR (l.u2 = lower($2) AND l.u2type = $3)
			ORDER BY l.match_date DESC
		`, seasonID, umpireName, category)
		if err == nil {
			defer dbRows.Close()
			for dbRows.Next() {
				var row scoreRow
				if e := dbRows.Scan(&row.SubID, &row.Date, &row.Club, &row.Perf,
					&row.DM, &row.MM, &row.PM, &row.PI, &row.TW); e == nil {
					rows = append(rows, row)
				}
			}
		}

		// Compute summary stats
		var totalGames, gamesWithScores int
		var sumDM, sumMM, sumPM, sumPI, sumTW, sumTotal int
		for _, row := range rows {
			totalGames++
			if row.DM != nil && row.MM != nil && row.PM != nil && row.PI != nil && row.TW != nil {
				gamesWithScores++
				sumDM += int(*row.DM)
				sumMM += int(*row.MM)
				sumPM += int(*row.PM)
				sumPI += int(*row.PI)
				sumTW += int(*row.TW)
				sumTotal += int(*row.DM) + int(*row.MM) + int(*row.PM) + int(*row.PI) + int(*row.TW)
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Umpire Scores – "+umpireName)
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		backURL := fmt.Sprintf("/admin/rankings/umpires?season_id=%d&category=%s", seasonID, url.QueryEscape(category))
		fmt.Fprintf(w, `<div class="container-fluid px-4">
<nav aria-label="breadcrumb" class="mb-3">
  <ol class="breadcrumb">
    <li class="breadcrumb-item"><a href="%s">Umpire Rankings</a></li>
    <li class="breadcrumb-item active">%s</li>
  </ol>
</nav>
<h4 class="fw-bold mb-1">%s</h4>
<p class="text-muted mb-4 small">%s &mdash; %s score breakdown by game</p>
`, escapeHTML(backURL), escapeHTML(umpireName), escapeHTML(umpireName), escapeHTML(categoryTitle), escapeHTML(seasonName))

		// KPI summary strip
		avgTotal := 0.0
		avgDM, avgMM, avgPM, avgPI, avgTW := 0.0, 0.0, 0.0, 0.0, 0.0
		if gamesWithScores > 0 {
			avgTotal = float64(sumTotal) / float64(gamesWithScores)
			avgDM = float64(sumDM) / float64(gamesWithScores)
			avgMM = float64(sumMM) / float64(gamesWithScores)
			avgPM = float64(sumPM) / float64(gamesWithScores)
			avgPI = float64(sumPI) / float64(gamesWithScores)
			avgTW = float64(sumTW) / float64(gamesWithScores)
		}
		avgClass := "kpi-green"
		if avgTotal > 0 && avgTotal < 15 {
			avgClass = "kpi-red"
		} else if avgTotal > 0 && avgTotal < 20 {
			avgClass = "kpi-yellow"
		}
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-6 col-md-2">
    <div class="card card-kpi kpi-blue text-center p-3">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Games Rated</div>
    </div>
  </div>
  <div class="col-6 col-md-2">
    <div class="card card-kpi %s text-center p-3">
      <div class="kpi-number">%.1f<small style="font-size:.6em">/25</small></div>
      <div class="kpi-label">Avg Score/Game</div>
    </div>
  </div>
  <div class="col-6 col-md-2">
    <div class="card card-kpi kpi-teal text-center p-3">
      <div class="kpi-number">%.1f<small style="font-size:.6em">/5</small></div>
      <div class="kpi-label">Decision Making</div>
    </div>
  </div>
  <div class="col-6 col-md-2">
    <div class="card card-kpi kpi-teal text-center p-3">
      <div class="kpi-number">%.1f<small style="font-size:.6em">/5</small></div>
      <div class="kpi-label">Match Mgmt</div>
    </div>
  </div>
  <div class="col-6 col-md-2">
    <div class="card card-kpi kpi-teal text-center p-3">
      <div class="kpi-number">%.1f<small style="font-size:.6em">/5</small></div>
      <div class="kpi-label">Player Mgmt</div>
    </div>
  </div>
  <div class="col-6 col-md-2">
    <div class="card card-kpi kpi-teal text-center p-3">
      <div class="kpi-number">%.1f / %.1f<small style="font-size:.6em">/5</small></div>
      <div class="kpi-label">Presence / Teamwork</div>
    </div>
  </div>
</div>
`, totalGames, avgClass, avgTotal, avgDM, avgMM, avgPM, avgPI, avgTW)

		// Per-game table
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Score Breakdown by Game</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Date</th><th>Club</th>
        <th title="Decision Making">Dec. Making</th>
        <th title="Match Management">Match Mgmt</th>
        <th title="Player Management">Player Mgmt</th>
        <th title="Presence &amp; Image">Presence</th>
        <th title="Teamwork">Teamwork</th>
        <th>Total<small class="text-muted fw-normal">/25</small></th>
        <th>Performance</th>
        <th></th>
      </tr></thead>
      <tbody>`)

		for _, row := range rows {
			perfBadge := ""
			switch row.Perf {
			case "Good":
				perfBadge = `<span class="badge bg-success">Good</span>`
			case "Average":
				perfBadge = `<span class="badge bg-warning text-dark">Average</span>`
			case "Poor":
				perfBadge = `<span class="badge bg-danger">Poor</span>`
			}
			scoreCell := func(v *int32) string {
				if v == nil {
					return `<td class="text-muted">—</td>`
				}
				cls := "text-success"
				if *v <= 2 {
					cls = "text-danger"
				} else if *v == 3 {
					cls = "text-warning"
				}
				return fmt.Sprintf(`<td class="%s fw-semibold">%d</td>`, cls, *v)
			}
			totalCell := `<td class="text-muted">—</td>`
			if row.DM != nil && row.MM != nil && row.PM != nil && row.PI != nil && row.TW != nil {
				t := int(*row.DM) + int(*row.MM) + int(*row.PM) + int(*row.PI) + int(*row.TW)
				cls := "text-success fw-bold"
				if t < 15 {
					cls = "text-danger fw-bold"
				} else if t < 20 {
					cls = "text-warning fw-bold"
				}
				totalCell = fmt.Sprintf(`<td><span class="%s">%d</span></td>`, cls, t)
			}
			fmt.Fprintf(w, `<tr>
  <td>%s</td>
  <td>%s</td>
  %s%s%s%s%s
  %s
  <td>%s</td>
  <td><a href="/admin/submissions/%d" class="btn btn-outline-secondary btn-sm py-0 px-2" style="font-size:.75rem">#%d</a></td>
</tr>`,
				row.Date.Format("2 Jan 2006"),
				escapeHTML(row.Club),
				scoreCell(row.DM), scoreCell(row.MM), scoreCell(row.PM), scoreCell(row.PI), scoreCell(row.TW),
				totalCell,
				perfBadge,
				row.SubID, row.SubID)
		}

		if len(rows) == 0 {
			fmt.Fprint(w, `<tr><td colspan="10" class="text-center text-muted py-3">No ratings found for this umpire this season.</td></tr>`)
		}

		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>`)
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

func umpireCategoryCardClass(activeCategory, cardCategory string) string {
	if activeCategory == cardCategory {
		return "kpi-green"
	}
	return "kpi-blue"
}
