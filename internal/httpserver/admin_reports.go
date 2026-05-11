package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

// reportPayload is the structured content stored in generated_reports.payload_json.
type reportPayload struct {
	SeasonID            int32             `json:"season_id"`
	SeasonName          string            `json:"season_name"`
	WeekID              int32             `json:"week_id,omitempty"`
	WeekNumber          int32             `json:"week_number,omitempty"`
	ReportType          string            `json:"report_type"`
	ReportPeriod        string            `json:"report_period"`
	GeneratedAt         time.Time         `json:"generated_at"`
	SubmissionsReceived int64             `json:"submissions_received"`
	SubmissionsExpected int64             `json:"submissions_expected"`
	ComplianceRate      float64           `json:"compliance_rate"`
	AvgPitchRating      float64           `json:"avg_pitch_rating"`
	PitchDistribution   map[string]int64  `json:"pitch_rating_distribution"`
	UmpireSummary       []reportUmpire    `json:"umpire_summary"`
	MissingTeams        []reportMissing   `json:"missing_teams"`
	ClubBreakdown       []reportClub      `json:"club_breakdown"`
	WeeklyTrend         []reportWeekTrend `json:"weekly_trend,omitempty"`
	SanctionsIssued     int64             `json:"sanctions_issued"`
}

type reportUmpire struct {
	Name    string  `json:"name"`
	Ratings int64   `json:"ratings"`
	Good    int64   `json:"good"`
	Average int64   `json:"average"`
	Poor    int64   `json:"poor"`
	Score   float64 `json:"score"`
}

type reportMissing struct {
	ClubName string `json:"club"`
	TeamName string `json:"team"`
	TeamID   int32  `json:"team_id"`
}

type reportClub struct {
	Club     string  `json:"club"`
	Subs     int64   `json:"submissions"`
	AvgPitch float64 `json:"avg_pitch"`
}

type reportWeekTrend struct {
	WeekNumber int32   `json:"week_number"`
	Subs       int64   `json:"submissions"`
	AvgPitch   float64 `json:"avg_pitch"`
}

type aiExecutiveReportPayload struct {
	SeasonID        int32                   `json:"season_id"`
	SeasonName      string                  `json:"season_name"`
	ReportType      string                  `json:"report_type"`
	ReportPeriod    string                  `json:"report_period"`
	GeneratedAt     time.Time               `json:"generated_at"`
	GeneratedByAI   bool                    `json:"generated_by_ai"`
	AIModel         string                  `json:"ai_model,omitempty"`
	AIError         string                  `json:"ai_error,omitempty"`
	Cover           aiExecutiveCover        `json:"cover"`
	Executive       aiExecutiveNarrative    `json:"executive_narrative"`
	Latest          aiExecutiveWindow       `json:"latest_report"`
	SeasonToDate    aiExecutiveWindow       `json:"season_to_date_report"`
	LatestUmpires   aiExecutiveUmpireWindow `json:"latest_umpire_report"`
	SeasonUmpires   aiExecutiveUmpireWindow `json:"season_umpire_report"`
	TableOfContents []string                `json:"table_of_contents"`
}

type aiExecutiveCover struct {
	Title        string `json:"title"`
	LatestPeriod string `json:"latest_period"`
	SeasonPeriod string `json:"season_period"`
	PreparedFor  string `json:"prepared_for"`
}

type aiExecutiveNarrative struct {
	ExecutiveSummary   string `json:"executive_summary"`
	LatestReport       string `json:"latest_report"`
	SeasonReport       string `json:"season_report"`
	LatestUmpireReport string `json:"latest_umpire_report"`
	SeasonUmpireReport string `json:"season_umpire_report"`
	Conclusion         string `json:"conclusion"`
}

type aiExecutiveWindow struct {
	Title               string               `json:"title"`
	Period              string               `json:"period"`
	SubmissionsReceived int64                `json:"submissions_received"`
	SubmissionsExpected int64                `json:"submissions_expected"`
	ComplianceRate      float64              `json:"compliance_rate"`
	AvgPitch            float64              `json:"avg_pitch"`
	AvgBounce           float64              `json:"avg_bounce"`
	AvgSeam             float64              `json:"avg_seam"`
	AvgCarry            float64              `json:"avg_carry"`
	AvgTurn             float64              `json:"avg_turn"`
	SanctionsIssued     int64                `json:"sanctions_issued"`
	TopClubs            []aiExecutiveClubRow `json:"top_clubs"`
	ConcernClubs        []aiExecutiveClubRow `json:"concern_clubs"`
	MissingTeams        []reportMissing      `json:"missing_teams,omitempty"`
	WeeklyTrend         []reportWeekTrend    `json:"weekly_trend,omitempty"`
	RepresentativeNotes []string             `json:"representative_notes,omitempty"`
}

type aiExecutiveClubRow struct {
	Club      string  `json:"club"`
	Reports   int64   `json:"reports"`
	AvgPitch  float64 `json:"avg_pitch"`
	AvgBounce float64 `json:"avg_bounce"`
	AvgSeam   float64 `json:"avg_seam"`
	AvgCarry  float64 `json:"avg_carry"`
	AvgTurn   float64 `json:"avg_turn"`
}

type aiExecutiveUmpireWindow struct {
	Title               string         `json:"title"`
	Period              string         `json:"period"`
	TotalRatings        int64          `json:"total_ratings"`
	Good                int64          `json:"good"`
	Average             int64          `json:"average"`
	Poor                int64          `json:"poor"`
	TopUmpires          []reportUmpire `json:"top_umpires"`
	ConcernUmpires      []reportUmpire `json:"concern_umpires"`
	RepresentativeNotes []string       `json:"representative_notes,omitempty"`
}

// handleAdminReports lists all generated reports.
func (s *Server) handleAdminReports() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		type reportRow struct {
			ID          int64
			SeasonName  string
			ReportType  string
			Period      string
			Status      string
			GeneratedAt time.Time
			CompletedAt *time.Time
			GeneratedBy string
		}
		var reports []reportRow

		rrows, err := s.DB.Query(ctx, `
			SELECT gr.id, s.name, gr.report_type, gr.report_period,
			       gr.status, gr.generated_at, gr.completed_at,
			       COALESCE(au.username, 'system')
			FROM generated_reports gr
			JOIN seasons s ON gr.season_id = s.id
			LEFT JOIN admin_users au ON gr.generated_by_admin_id = au.id
			ORDER BY gr.generated_at DESC LIMIT 50
		`)
		if err == nil {
			defer rrows.Close()
			for rrows.Next() {
				var rr reportRow
				if e := rrows.Scan(&rr.ID, &rr.SeasonName, &rr.ReportType, &rr.Period,
					&rr.Status, &rr.GeneratedAt, &rr.CompletedAt, &rr.GeneratedBy); e == nil {
					reports = append(reports, rr)
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		// Current season for the generate form
		var currentSeasonID int32
		var currentSeasonName string
		var currentWeekID int32
		var currentWeekNum int32
		s.DB.QueryRow(ctx, `
			SELECT s.id, s.name, w.id, w.week_number
			FROM weeks w JOIN seasons s ON w.season_id=s.id
			WHERE s.is_archived = FALSE
			ORDER BY
			    CASE WHEN CURRENT_DATE BETWEEN w.start_date AND w.end_date THEN 0
			         WHEN w.start_date > CURRENT_DATE THEN 1
			         ELSE 2 END,
			    abs(w.start_date - CURRENT_DATE)
			LIMIT 1
		`).Scan(&currentSeasonID, &currentSeasonName, &currentWeekID, &currentWeekNum)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Reports")
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprint(w, `<div class="container-fluid px-4">`)
		fmt.Fprint(w, `
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Reports</h4>
    <p class="text-muted mb-0 small">Weekly, season, and AI executive summaries</p>
  </div>
</div>
`)

		// Generate panel
		fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Generate New Report</div>
  <div class="card-body">
    <form method="POST" action="/admin/reports/generate" class="row g-3 align-items-end">
      <input type="hidden" name="csrf_token" value="%s">
      <input type="hidden" name="season_id" value="%d">
      <div class="col-auto">
        <label class="form-label small fw-semibold">Type</label>
        <select name="report_type" class="form-select form-select-sm" id="rptType" onchange="updatePeriod()">
          <option value="ai_executive">AI Executive</option>
          <option value="weekly">Weekly</option>
          <option value="monthly">Monthly</option>
          <option value="quarterly">Quarterly</option>
          <option value="season_end">Season End</option>
        </select>
      </div>
      <div class="col-auto">
        <label class="form-label small fw-semibold">Period</label>
        <input type="text" name="period" id="rptPeriod" class="form-control form-control-sm"
               placeholder="e.g. 2025-W14" value="%s" style="width:160px">
        <div class="form-text" id="rptPeriodHint">Format: 2025-W14 for weekly</div>
      </div>
      <div class="col-auto">
        <button type="submit" class="btn btn-primary btn-sm">Generate</button>
      </div>
    </form>
  </div>
</div>
<script>
function updatePeriod() {
  var t = document.getElementById('rptType').value;
  var p = document.getElementById('rptPeriod');
  var h = document.getElementById('rptPeriodHint');
  var d = new Date();
  if (t === 'ai_executive') {
    p.placeholder = 'Auto';
    h.textContent = 'Uses the latest completed week plus season to date';
    p.value = 'Auto';
  } else if (t === 'weekly') {
    p.placeholder = '2025-W14';
    h.textContent = 'Format: YYYY-Www';
  } else if (t === 'monthly') {
    p.placeholder = d.getFullYear()+'-'+(String(d.getMonth()+1).padStart(2,'0'));
    h.textContent = 'Format: YYYY-MM';
    p.value = p.placeholder;
  } else if (t === 'quarterly') {
    var q = Math.ceil((d.getMonth()+1)/3);
    p.placeholder = d.getFullYear()+'-Q'+q;
    h.textContent = 'Format: YYYY-Q1 through YYYY-Q4';
    p.value = p.placeholder;
  } else {
    p.placeholder = String(d.getFullYear());
    h.textContent = 'Format: YYYY';
    p.value = p.placeholder;
  }
}
</script>
`,
			csrfToken, currentSeasonID,
			func() string {
				if currentWeekNum > 0 {
					y := time.Now().Year()
					return fmt.Sprintf("%d-W%02d", y, currentWeekNum)
				}
				return ""
			}())

		// Reports list
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Generated Reports</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Season</th><th>Type</th><th>Period</th><th>Status</th>
        <th>Generated</th><th>By</th><th></th>
      </tr></thead>
      <tbody>
`)
		for _, rr := range reports {
			statusBadge := ""
			cardClass := ""
			switch rr.Status {
			case "ready":
				statusBadge = `<span class="badge bg-success">Ready</span>`
				cardClass = "report-card-ready"
			case "generating":
				statusBadge = `<span class="badge bg-secondary">Generating...</span>`
				cardClass = "report-card-generating"
			case "error":
				statusBadge = `<span class="badge bg-danger">Error</span>`
				cardClass = "report-card-error"
			}
			typeLabel := strings.ReplaceAll(strings.Title(strings.ReplaceAll(rr.ReportType, "_", " ")), " ", " ")
			viewLink := ""
			if rr.Status == "ready" {
				viewLink = fmt.Sprintf(`<a href="/admin/reports/%d" class="btn btn-sm btn-outline-primary py-0">View</a>`, rr.ID)
			}
			fmt.Fprintf(w,
				`<tr class="%s"><td>%s</td><td>%s</td><td><code>%s</code></td><td>%s</td><td class="text-muted small">%s</td><td class="text-muted small">%s</td><td>%s</td></tr>`,
				cardClass, escapeHTML(rr.SeasonName), escapeHTML(typeLabel),
				escapeHTML(rr.Period), statusBadge,
				rr.GeneratedAt.Format("02 Jan 15:04"), escapeHTML(rr.GeneratedBy), viewLink)
		}
		if len(reports) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-3">No reports generated yet.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>`)
		pageFooter(w)
	}
}

// handleAdminReportGenerate triggers async report generation.
func (s *Server) handleAdminReportGenerate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		seasonID, _ := strconv.Atoi(r.FormValue("season_id"))
		reportType := r.FormValue("report_type")
		period := strings.TrimSpace(r.FormValue("period"))

		if seasonID == 0 || reportType == "" || period == "" {
			http.Error(w, "missing fields", http.StatusBadRequest)
			return
		}

		validTypes := map[string]bool{"ai_executive": true, "weekly": true, "monthly": true, "quarterly": true, "season_end": true}
		if !validTypes[reportType] {
			http.Error(w, "invalid report type", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		adminID := s.resolveAdminID(r)

		// Resolve week_id for weekly and AI executive reports.
		var weekID *int32
		if reportType == "ai_executive" {
			var wid int32
			var weekNum int32
			var weekEnd time.Time
			err := s.DB.QueryRow(ctx, `
				SELECT id, week_number, end_date
				FROM weeks
				WHERE season_id=$1 AND end_date < CURRENT_DATE
				ORDER BY end_date DESC
				LIMIT 1
			`, seasonID).Scan(&wid, &weekNum, &weekEnd)
			if err != nil {
				err = s.DB.QueryRow(ctx, `
					SELECT id, week_number, end_date
					FROM weeks
					WHERE season_id=$1
					ORDER BY
					    CASE WHEN CURRENT_DATE BETWEEN start_date AND end_date THEN 0
					         WHEN start_date > CURRENT_DATE THEN 1
					         ELSE 2 END,
					    abs(start_date - CURRENT_DATE)
					LIMIT 1
				`, seasonID).Scan(&wid, &weekNum, &weekEnd)
			}
			if err == nil {
				weekID = &wid
				period = fmt.Sprintf("%d-W%02d Executive", weekEnd.Year(), weekNum)
			}
		} else if reportType == "weekly" {
			var wid int32
			if err := s.DB.QueryRow(ctx, `
				SELECT id FROM weeks WHERE season_id=$1
				ORDER BY abs(week_number - $2::int) LIMIT 1
			`, seasonID, extractWeekNum(period)).Scan(&wid); err == nil {
				weekID = &wid
			}
		}

		var reportID int64
		err := s.DB.QueryRow(ctx, `
			INSERT INTO generated_reports (season_id, week_id, report_type, report_period, generated_by_admin_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (season_id, report_type, report_period) DO UPDATE
			  SET status='generating', payload_json=NULL, error_message=NULL,
			      generated_at=now(), completed_at=NULL
			RETURNING id
		`, seasonID, weekID, reportType, period, adminID).Scan(&reportID)
		if err != nil {
			http.Error(w, "failed to create report: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Kick off async generation
		go s.generateReport(reportID, int32(seasonID), weekID, reportType, period)

		http.Redirect(w, r, fmt.Sprintf("/admin/reports/%d", reportID), http.StatusSeeOther)
	}
}

// handleAdminReportView renders a completed report.
func (s *Server) handleAdminReportView() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		reportID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		var status, reportType, period string
		var payloadRaw []byte
		var errMsg *string
		var generatedAt time.Time
		var seasonName string

		err = s.DB.QueryRow(ctx, `
			SELECT gr.status, gr.report_type, gr.report_period,
			       gr.payload_json, gr.error_message, gr.generated_at, s.name
			FROM generated_reports gr JOIN seasons s ON gr.season_id=s.id
			WHERE gr.id=$1
		`, reportID).Scan(&status, &reportType, &period, &payloadRaw, &errMsg, &generatedAt, &seasonName)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHeadWithCharts(w, "Report: "+period)
		writeAdminNav(w, csrfToken, r.URL.Path)

		fmt.Fprintf(w, `<div class="container-fluid px-4">
<nav aria-label="breadcrumb" class="mb-3">
  <ol class="breadcrumb">
    <li class="breadcrumb-item"><a href="/admin/reports">Reports</a></li>
    <li class="breadcrumb-item active">%s</li>
  </ol>
</nav>
`, escapeHTML(period))

		if status == "generating" {
			fmt.Fprintf(w, `
<div class="card shadow-sm mb-4 report-card-generating"
     hx-get="/admin/reports/%d/status" hx-trigger="every 2s" hx-swap="outerHTML" id="report-status-card">
  <div class="card-body text-center py-5">
    <div class="spinner-border text-secondary mb-3" role="status"></div>
    <p class="text-muted">Generating report for %s...</p>
  </div>
</div>
`, reportID, escapeHTML(period))
			pageFooter(w)
			return
		}

		if status == "error" {
			msg := "unknown error"
			if errMsg != nil {
				msg = *errMsg
			}
			fmt.Fprintf(w, `<div class="alert alert-danger"><strong>Report failed:</strong> %s</div>`, escapeHTML(msg))
			fmt.Fprint(w, `</div>`)
			pageFooter(w)
			return
		}

		if reportType == "ai_executive" {
			var rp aiExecutiveReportPayload
			if err := json.Unmarshal(payloadRaw, &rp); err != nil {
				fmt.Fprint(w, `<div class="alert alert-danger">Could not parse AI executive report data.</div></div>`)
				pageFooter(w)
				return
			}
			s.renderAIExecutiveReport(w, rp, reportID)
			return
		}

		// Parse payload
		var rp reportPayload
		if err := json.Unmarshal(payloadRaw, &rp); err != nil {
			fmt.Fprint(w, `<div class="alert alert-danger">Could not parse report data.</div></div>`)
			pageFooter(w)
			return
		}

		typeLabel := strings.Title(strings.ReplaceAll(reportType, "_", " "))
		fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">%s Report: %s</h4>
    <p class="text-muted mb-0 small">%s &mdash; Generated %s</p>
  </div>
  <a href="/admin/reports/%d/print" target="_blank" class="btn btn-sm btn-outline-secondary">🖨 Print / Save as PDF</a>
</div>
`, escapeHTML(typeLabel), escapeHTML(rp.ReportPeriod), escapeHTML(rp.SeasonName),
			rp.GeneratedAt.Format("02 Jan 2006 15:04"), reportID)

		// KPI cards
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-blue p-3 text-center">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Received</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-teal p-3 text-center">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Expected</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-green p-3 text-center">
      <div class="kpi-number">%.1f%%</div>
      <div class="kpi-label">Compliance</div>
    </div>
  </div>
  <div class="col-6 col-md-3">
    <div class="card card-kpi kpi-amber p-3 text-center">
      <div class="kpi-number">%.2f</div>
      <div class="kpi-label">Avg Pitch</div>
    </div>
  </div>
</div>
`, rp.SubmissionsReceived, rp.SubmissionsExpected, rp.ComplianceRate, rp.AvgPitchRating)

		// Charts row
		fmt.Fprint(w, `<div class="row g-3 mb-4">`)

		// Trend chart (if available) or pitch distribution
		if len(rp.WeeklyTrend) > 0 {
			trendLabels, _ := json.Marshal(func() []int32 {
				var l []int32
				for _, t := range rp.WeeklyTrend {
					l = append(l, t.WeekNumber)
				}
				return l
			}())
			trendData, _ := json.Marshal(func() []int64 {
				var d []int64
				for _, t := range rp.WeeklyTrend {
					d = append(d, t.Subs)
				}
				return d
			}())
			fmt.Fprintf(w, `
<div class="col-12 col-xl-8">
  <div class="card shadow-sm">
    <div class="card-header fw-semibold">Submissions per Week</div>
    <div class="card-body">
      <div class="chart-container"><canvas id="chartTrend"></canvas></div>
    </div>
  </div>
</div>
<script>
window.__trendLabels = %s; window.__trendData = %s;
</script>
`, string(trendLabels), string(trendData))
		}

		// Pitch distribution
		pd1 := rp.PitchDistribution["1"]
		pd2 := rp.PitchDistribution["2"]
		pd3 := rp.PitchDistribution["3"]
		pd4 := rp.PitchDistribution["4"]
		pd5 := rp.PitchDistribution["5"]
		fmt.Fprintf(w, `
<div class="col-12 col-xl-4">
  <div class="card shadow-sm">
    <div class="card-header fw-semibold">Pitch Rating Distribution</div>
    <div class="card-body">
      <div class="chart-container"><canvas id="chartPitchRpt"></canvas></div>
    </div>
  </div>
</div>
<script>
window.__pitchDist = [%d,%d,%d,%d,%d];
</script>
`, pd1, pd2, pd3, pd4, pd5)

		fmt.Fprint(w, `</div>`) // end charts row

		// Club breakdown
		if len(rp.ClubBreakdown) > 0 {
			fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Club Breakdown</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr><th>#</th><th>Club</th><th>Submissions</th><th>Avg Pitch</th></tr></thead>
      <tbody>
`)
			for i, c := range rp.ClubBreakdown {
				fmt.Fprintf(w, `<tr><td class="text-muted">%d</td><td>%s</td><td>%d</td><td>%.2f</td></tr>`,
					i+1, escapeHTML(c.Club), c.Subs, c.AvgPitch)
			}
			fmt.Fprint(w, `      </tbody></table></div></div>`)
		}

		// Umpire summary
		if len(rp.UmpireSummary) > 0 {
			fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Umpire Performance</div>
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr><th>Umpire</th><th>Ratings</th><th class="text-success">Good</th><th class="text-warning">Avg</th><th class="text-danger">Poor</th><th>Score</th></tr></thead>
      <tbody>
`)
			for _, u := range rp.UmpireSummary {
				scoreClass := "text-success"
				if u.Score < 2.0 {
					scoreClass = "text-danger"
				} else if u.Score < 2.5 {
					scoreClass = "text-warning"
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td class="text-success">%d</td><td class="text-warning">%d</td><td class="text-danger">%d</td><td class="%s fw-bold">%.2f</td></tr>`,
					escapeHTML(u.Name), u.Ratings, u.Good, u.Average, u.Poor, scoreClass, u.Score)
			}
			fmt.Fprint(w, `      </tbody></table></div></div>`)
		}

		// Missing teams
		if len(rp.MissingTeams) > 0 {
			fmt.Fprintf(w, `
<div class="card shadow-sm mb-4 border-danger">
  <div class="card-header fw-semibold text-danger">Missing Submissions (%d teams)</div>
  <div class="table-responsive">
    <table class="table table-hover mb-0">
      <thead><tr><th>Club</th><th>Team</th></tr></thead>
      <tbody>
`, len(rp.MissingTeams))
			for _, m := range rp.MissingTeams {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`,
					escapeHTML(m.ClubName), escapeHTML(m.TeamName))
			}
			fmt.Fprint(w, `      </tbody></table></div></div>`)
		}

		fmt.Fprint(w, `</div>`)

		// Chart init
		script := fmt.Sprintf(`
Chart.defaults.font.family = "'Segoe UI', system-ui, sans-serif";
Chart.defaults.color = '#6c757d';

if (window.__trendLabels) {
  new Chart(document.getElementById('chartTrend'), {
    type: 'line',
    data: {
      labels: window.__trendLabels,
      datasets: [{
        label: 'Submissions',
        data: window.__trendData,
        borderColor: '#C41E3A',
        backgroundColor: 'rgba(196,30,58,.1)',
        tension: .35, fill: true,
        pointRadius: 4, pointBackgroundColor: '#C41E3A'
      }]
    },
    options: {
      responsive: true, maintainAspectRatio: false,
      plugins: { legend: { display: false } },
      scales: { y: { beginAtZero: true, ticks: { stepSize: 1 } }, x: { grid: { display: false } } }
    }
  });
}

if (window.__pitchDist) {
  new Chart(document.getElementById('chartPitchRpt'), {
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
}
`)
		pageFooterWithScript(w, script)
	}
}

// handleAdminReportStatus returns the status fragment for HTMX polling.
func (s *Server) handleAdminReportStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		reportID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		var status, period string
		s.DB.QueryRow(ctx, `SELECT status, report_period FROM generated_reports WHERE id=$1`, reportID).
			Scan(&status, &period)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if status == "ready" {
			// Return without hx-trigger so polling stops, and include a redirect
			fmt.Fprintf(w, `<div id="report-status-card" class="card shadow-sm mb-4 report-card-ready">
  <div class="card-body text-center py-4">
    <p class="text-success fw-semibold mb-2">&#10003; Report ready</p>
    <a href="/admin/reports/%d" class="btn btn-primary btn-sm">View Report</a>
  </div>
</div>`, reportID)
		} else if status == "error" {
			fmt.Fprintf(w, `<div id="report-status-card" class="card shadow-sm mb-4 report-card-error">
  <div class="card-body text-center py-4">
    <p class="text-danger fw-semibold">Report generation failed.</p>
    <a href="/admin/reports" class="btn btn-outline-secondary btn-sm">Back to Reports</a>
  </div>
</div>`)
		} else {
			fmt.Fprintf(w, `<div id="report-status-card" class="card shadow-sm mb-4 report-card-generating"
     hx-get="/admin/reports/%d/status" hx-trigger="every 2s" hx-swap="outerHTML">
  <div class="card-body text-center py-5">
    <div class="spinner-border text-secondary mb-3" role="status"></div>
    <p class="text-muted">Generating report for %s...</p>
  </div>
</div>`, reportID, escapeHTML(period))
		}
	}
}

// handleAdminReportPrint renders a print-friendly HTML page (open in browser → Ctrl+P → Save as PDF).
func (s *Server) handleAdminReportPrint() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		reportID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var payload []byte
		var seasonName string
		var reportType string
		if err := s.DB.QueryRow(ctx, `
			SELECT gr.payload_json, s.name, gr.report_type
			FROM generated_reports gr JOIN seasons s ON gr.season_id=s.id
			WHERE gr.id=$1 AND gr.status='ready'
		`, reportID).Scan(&payload, &seasonName, &reportType); err != nil {
			http.NotFound(w, r)
			return
		}

		if reportType == "ai_executive" {
			var rp aiExecutiveReportPayload
			if err := json.Unmarshal(payload, &rp); err != nil {
				http.Error(w, "corrupt report", http.StatusInternalServerError)
				return
			}
			writeAIExecutivePrintReport(w, rp)
			return
		}

		var rp reportPayload
		if err := json.Unmarshal(payload, &rp); err != nil {
			http.Error(w, "corrupt report", http.StatusInternalServerError)
			return
		}

		typeLabel := strings.Title(strings.ReplaceAll(rp.ReportType, "_", " "))

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>GMCL %s Report — %s</title>
<style>
body { font-family: Arial, sans-serif; font-size: 13px; margin: 30px; color: #222; }
h1 { font-size: 20px; margin-bottom: 4px; }
h2 { font-size: 15px; margin-top: 24px; border-bottom: 1px solid #ccc; padding-bottom: 4px; }
p.meta { color: #666; font-size: 12px; margin: 0 0 20px; }
table { border-collapse: collapse; width: 100%%; margin-top: 8px; }
th { background: #8b0000; color: #fff; padding: 6px 10px; text-align: left; font-size: 12px; }
td { padding: 5px 10px; border-bottom: 1px solid #eee; }
.kpis { display: flex; gap: 20px; margin: 16px 0; flex-wrap: wrap; }
.kpi { border: 1px solid #ddd; border-radius: 6px; padding: 12px 18px; min-width: 120px; }
.kpi-num { font-size: 28px; font-weight: bold; }
.kpi-label { font-size: 11px; color: #666; text-transform: uppercase; }
@media print { button { display: none; } body { margin: 10mm; } }
</style>
</head><body>
<button onclick="window.print()" style="float:right;padding:6px 14px;background:#8b0000;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:13px;">🖨 Print / Save as PDF</button>
<h1>GMCL %s Report: %s</h1>
<p class="meta">%s &mdash; Generated %s</p>
<div class="kpis">
  <div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Submissions Received</div></div>
  <div class="kpi"><div class="kpi-num">%d</div><div class="kpi-label">Teams Expected</div></div>
  <div class="kpi"><div class="kpi-num">%.1f%%</div><div class="kpi-label">Compliance</div></div>
  <div class="kpi"><div class="kpi-num">%.2f</div><div class="kpi-label">Avg Pitch Rating</div></div>
</div>`,
			typeLabel, rp.ReportPeriod,
			typeLabel, escapeHTML(rp.ReportPeriod),
			escapeHTML(seasonName), rp.GeneratedAt.Format("02 Jan 2006 15:04"),
			rp.SubmissionsReceived, rp.SubmissionsExpected, rp.ComplianceRate, rp.AvgPitchRating)

		if len(rp.MissingTeams) > 0 {
			fmt.Fprint(w, `<h2>Missing Submissions</h2><table><tr><th>Club</th><th>Team</th></tr>`)
			for _, m := range rp.MissingTeams {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`, escapeHTML(m.ClubName), escapeHTML(m.TeamName))
			}
			fmt.Fprint(w, `</table>`)
		}

		if len(rp.ClubBreakdown) > 0 {
			fmt.Fprint(w, `<h2>Club Breakdown</h2><table><tr><th>Club</th><th>Submissions</th><th>Avg Pitch</th></tr>`)
			for _, c := range rp.ClubBreakdown {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%.2f</td></tr>`, escapeHTML(c.Club), c.Subs, c.AvgPitch)
			}
			fmt.Fprint(w, `</table>`)
		}

		if len(rp.UmpireSummary) > 0 {
			fmt.Fprint(w, `<h2>Umpire Summary</h2><table><tr><th>Umpire</th><th>Ratings</th><th>Good</th><th>Average</th><th>Poor</th><th>Score</th></tr>`)
			for _, u := range rp.UmpireSummary {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%.2f</td></tr>`,
					escapeHTML(u.Name), u.Ratings, u.Good, u.Average, u.Poor, u.Score)
			}
			fmt.Fprint(w, `</table>`)
		}

		fmt.Fprint(w, `</body></html>`)
	}
}

// handleAdminReportDownload returns the raw report JSON.
func (s *Server) handleAdminReportDownload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		reportID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var payload []byte
		var period string
		if err := s.DB.QueryRow(ctx, `
			SELECT payload_json, report_period FROM generated_reports WHERE id=$1 AND status='ready'
		`, reportID).Scan(&payload, &period); err != nil {
			http.NotFound(w, r)
			return
		}

		filename := fmt.Sprintf("gmcl-report-%s.json", strings.ReplaceAll(period, " ", "-"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.Write(payload)
	}
}

func (s *Server) renderAIExecutiveReport(w http.ResponseWriter, rp aiExecutiveReportPayload, reportID int64) {
	fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">%s</h4>
    <p class="text-muted mb-0 small">%s &mdash; Generated %s</p>
  </div>
  <a href="/admin/reports/%d/print" target="_blank" class="btn btn-sm btn-outline-secondary">Print / Save as PDF</a>
</div>
`, escapeHTML(rp.Cover.Title), escapeHTML(rp.SeasonName), rp.GeneratedAt.Format("02 Jan 2006 15:04"), reportID)

	if rp.GeneratedByAI {
		fmt.Fprintf(w, `<div class="alert alert-success py-2 small">Narrative generated with %s from source-of-truth report data.</div>`, escapeHTML(rp.AIModel))
	} else if rp.AIError != "" {
		fmt.Fprintf(w, `<div class="alert alert-warning py-2 small">AI narrative was unavailable, so a deterministic executive summary was generated. %s</div>`, escapeHTML(rp.AIError))
	}

	fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-body">`)
	fmt.Fprintf(w, `<h5 class="fw-bold mb-3">Table of Contents</h5><ol class="mb-0">`)
	for _, item := range rp.TableOfContents {
		fmt.Fprintf(w, `<li>%s</li>`, escapeHTML(item))
	}
	fmt.Fprint(w, `</ol></div></div>`)

	renderNarrativeSection := func(title, body string) {
		fmt.Fprintf(w, `<div class="card shadow-sm mb-4"><div class="card-body"><h5 class="fw-bold mb-3">%s</h5>%s</div></div>`,
			escapeHTML(title), paragraphsHTML(body))
	}

	renderNarrativeSection("Executive Summary", rp.Executive.ExecutiveSummary)
	s.renderAIExecutiveWindow(w, "Latest Report", rp.Latest)
	renderNarrativeSection("Latest Report Findings", rp.Executive.LatestReport)
	s.renderAIExecutiveWindow(w, "Season Report", rp.SeasonToDate)
	renderNarrativeSection("Season Report Findings", rp.Executive.SeasonReport)
	s.renderAIExecutiveUmpireWindow(w, "Latest Umpire Reports", rp.LatestUmpires)
	renderNarrativeSection("Latest Umpire Findings", rp.Executive.LatestUmpireReport)
	s.renderAIExecutiveUmpireWindow(w, "Season Umpire Report", rp.SeasonUmpires)
	renderNarrativeSection("Season Umpire Findings", rp.Executive.SeasonUmpireReport)
	renderNarrativeSection("Conclusion", rp.Executive.Conclusion)
	fmt.Fprint(w, `</div>`)
	pageFooter(w)
}

func (s *Server) renderAIExecutiveWindow(w http.ResponseWriter, title string, win aiExecutiveWindow) {
	fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">%s</div>
  <div class="card-body">
    <p class="text-muted small mb-3">%s</p>
    <div class="row g-3 mb-4">
      <div class="col-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4">%d</div><div class="text-muted small">Reports</div></div></div>
      <div class="col-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4">%d</div><div class="text-muted small">Expected</div></div></div>
      <div class="col-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4">%.1f%%</div><div class="text-muted small">Compliance</div></div></div>
      <div class="col-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4">%.2f</div><div class="text-muted small">Pitch</div></div></div>
      <div class="col-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4">%.2f</div><div class="text-muted small">Bounce</div></div></div>
      <div class="col-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4">%d</div><div class="text-muted small">Sanctions</div></div></div>
    </div>
`, escapeHTML(title), escapeHTML(win.Period), win.SubmissionsReceived, win.SubmissionsExpected, win.ComplianceRate,
		win.AvgPitch, win.AvgBounce, win.SanctionsIssued)

	writeClubTable := func(title string, rows []aiExecutiveClubRow) {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(w, `<h6 class="fw-semibold mt-3">%s</h6><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Club/Ground</th><th>Reports</th><th>Pitch</th><th>Bounce</th><th>Seam</th><th>Carry</th><th>Turn</th></tr></thead><tbody>`, escapeHTML(title))
		for _, row := range rows {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%.2f</td><td>%.2f</td><td>%.2f</td><td>%.2f</td><td>%.2f</td></tr>`,
				escapeHTML(row.Club), row.Reports, row.AvgPitch, row.AvgBounce, row.AvgSeam, row.AvgCarry, row.AvgTurn)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
	}
	writeClubTable("Strongest Rated Clubs/Grounds", win.TopClubs)
	writeClubTable("Lowest Rated Clubs/Grounds", win.ConcernClubs)

	if len(win.MissingTeams) > 0 {
		fmt.Fprintf(w, `<h6 class="fw-semibold mt-3 text-danger">Missing Submissions</h6><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Club</th><th>Team</th></tr></thead><tbody>`)
		for _, row := range win.MissingTeams {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`, escapeHTML(row.ClubName), escapeHTML(row.TeamName))
		}
		fmt.Fprint(w, `</tbody></table></div>`)
	}
	fmt.Fprint(w, `</div></div>`)
}

func (s *Server) renderAIExecutiveUmpireWindow(w http.ResponseWriter, title string, win aiExecutiveUmpireWindow) {
	fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">%s</div>
  <div class="card-body">
    <p class="text-muted small mb-3">%s</p>
    <div class="row g-3 mb-4">
      <div class="col-6 col-lg-3"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4">%d</div><div class="text-muted small">Ratings</div></div></div>
      <div class="col-6 col-lg-3"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4 text-success">%d</div><div class="text-muted small">Good</div></div></div>
      <div class="col-6 col-lg-3"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4 text-warning">%d</div><div class="text-muted small">Average</div></div></div>
      <div class="col-6 col-lg-3"><div class="border rounded p-3 h-100"><div class="fw-bold fs-4 text-danger">%d</div><div class="text-muted small">Poor</div></div></div>
    </div>
`, escapeHTML(title), escapeHTML(win.Period), win.TotalRatings, win.Good, win.Average, win.Poor)

	writeUmpireTable := func(title string, rows []reportUmpire) {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(w, `<h6 class="fw-semibold mt-3">%s</h6><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Umpire</th><th>Ratings</th><th>Good</th><th>Average</th><th>Poor</th><th>Score</th></tr></thead><tbody>`, escapeHTML(title))
		for _, row := range rows {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td class="text-success">%d</td><td class="text-warning">%d</td><td class="text-danger">%d</td><td>%.2f</td></tr>`,
				escapeHTML(titleCase(row.Name)), row.Ratings, row.Good, row.Average, row.Poor, row.Score)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
	}
	writeUmpireTable("Strongest Umpire Feedback", win.TopUmpires)
	writeUmpireTable("Umpires Requiring Review", win.ConcernUmpires)
	fmt.Fprint(w, `</div></div>`)
}

func paragraphsHTML(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return `<p class="text-muted mb-0">No narrative available.</p>`
	}
	parts := strings.Split(text, "\n")
	var b strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		b.WriteString(`<p>`)
		b.WriteString(escapeHTML(part))
		b.WriteString(`</p>`)
	}
	return b.String()
}

// generateReport runs in a goroutine and populates the report payload.
func (s *Server) generateReport(reportID int64, seasonID int32, weekID *int32, reportType, period string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if reportType == "ai_executive" {
		s.generateAIExecutiveReport(ctx, reportID, seasonID, weekID, period)
		return
	}

	rp := reportPayload{
		SeasonID:     seasonID,
		ReportType:   reportType,
		ReportPeriod: period,
		GeneratedAt:  time.Now(),
	}

	s.DB.QueryRow(ctx, `SELECT name FROM seasons WHERE id=$1`, seasonID).Scan(&rp.SeasonName)

	// Build WHERE clause additions
	whereExtra := "WHERE sub.season_id=$1"
	args := []any{seasonID}

	if reportType == "weekly" && weekID != nil {
		rp.WeekID = *weekID
		s.DB.QueryRow(ctx, `SELECT week_number FROM weeks WHERE id=$1`, *weekID).Scan(&rp.WeekNumber)
		whereExtra += " AND sub.week_id=$2"
		args = append(args, *weekID)
	} else if reportType == "monthly" {
		// period is YYYY-MM
		whereExtra += " AND to_char(sub.match_date,'YYYY-MM')=$2"
		args = append(args, period)
	} else if reportType == "quarterly" {
		// period is YYYY-Q1..Q4 → convert to date range
		var year, quarter int
		fmt.Sscanf(period, "%d-Q%d", &year, &quarter)
		startMonth := (quarter-1)*3 + 1
		endMonth := startMonth + 2
		start := fmt.Sprintf("%04d-%02d-01", year, startMonth)
		end := fmt.Sprintf("%04d-%02d-01", year, endMonth+1)
		whereExtra += " AND sub.match_date >= $2 AND sub.match_date < $3"
		args = append(args, start, end)
	}
	// season_end uses no extra filter

	// Submissions received + avg pitch
	s.DB.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(DISTINCT sub.team_id), COALESCE(AVG(pitch_rating),0) FROM submissions sub %s
	`, whereExtra), args...).Scan(&rp.SubmissionsReceived, &rp.AvgPitchRating)

	// Expected = active teams
	s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM teams WHERE active=TRUE`).Scan(&rp.SubmissionsExpected)
	if reportType == "weekly" && rp.SubmissionsExpected > 0 {
		rp.ComplianceRate = float64(rp.SubmissionsReceived) / float64(rp.SubmissionsExpected) * 100
		if rp.ComplianceRate > 100 {
			rp.ComplianceRate = 100
		}
	}

	// Pitch distribution
	rp.PitchDistribution = map[string]int64{"1": 0, "2": 0, "3": 0, "4": 0, "5": 0}
	prows, err := s.DB.Query(ctx, fmt.Sprintf(`
		SELECT pitch_rating, COUNT(*) FROM submissions sub %s GROUP BY pitch_rating
	`, whereExtra), args...)
	if err == nil {
		defer prows.Close()
		for prows.Next() {
			var rating int32
			var cnt int64
			if prows.Scan(&rating, &cnt) == nil {
				rp.PitchDistribution[strconv.Itoa(int(rating))] = cnt
			}
		}
	}

	// Umpire summary
	urows, err := s.DB.Query(ctx, fmt.Sprintf(`
		WITH r AS (
		    SELECT lower(trim(sub.form_data->>'umpire1_name')) AS key,
		           sub.form_data->>'umpire1_performance' AS perf
		    FROM submissions sub %s AND (sub.form_data->>'umpire1_name') IS NOT NULL
		    UNION ALL
		    SELECT lower(trim(sub.form_data->>'umpire2_name')),
		           sub.form_data->>'umpire2_performance'
		    FROM submissions sub %s AND (sub.form_data->>'umpire2_name') IS NOT NULL
		)
		SELECT key,
		       COUNT(*)                                       AS total,
		       COUNT(*) FILTER (WHERE perf='Good')            AS good,
		       COUNT(*) FILTER (WHERE perf='Average')         AS avg_c,
		       COUNT(*) FILTER (WHERE perf='Poor')            AS poor,
		       COALESCE(ROUND((
		           COUNT(*) FILTER (WHERE perf='Good') * 3.0 +
		           COUNT(*) FILTER (WHERE perf='Average') * 2.0 +
		           COUNT(*) FILTER (WHERE perf='Poor') * 1.0
		       ) / NULLIF(COUNT(*),0), 2), 0) AS score
		FROM r WHERE key IS NOT NULL AND key <> ''
		GROUP BY key ORDER BY score DESC, total DESC LIMIT 20
	`, whereExtra, whereExtra), append(args, args...)...)
	if err == nil {
		defer urows.Close()
		for urows.Next() {
			var u reportUmpire
			var key string
			if urows.Scan(&key, &u.Ratings, &u.Good, &u.Average, &u.Poor, &u.Score) == nil {
				u.Name = key
				rp.UmpireSummary = append(rp.UmpireSummary, u)
			}
		}
	}

	// Missing teams
	if weekID != nil {
		mrows, err := s.DB.Query(ctx, `
			SELECT t.id, cl.name, t.name
			FROM teams t JOIN clubs cl ON t.club_id=cl.id
			WHERE t.active=TRUE
			  AND t.id NOT IN (SELECT team_id FROM submissions WHERE week_id=$1)
			ORDER BY cl.name, t.name
		`, *weekID)
		if err == nil {
			defer mrows.Close()
			for mrows.Next() {
				var m reportMissing
				if mrows.Scan(&m.TeamID, &m.ClubName, &m.TeamName) == nil {
					rp.MissingTeams = append(rp.MissingTeams, m)
				}
			}
		}
	}

	// Club breakdown
	cbrows, err := s.DB.Query(ctx, fmt.Sprintf(`
		SELECT cl.name, COUNT(DISTINCT sub.team_id), COALESCE(ROUND(AVG(sub.pitch_rating)::numeric,2),0)
		FROM submissions sub
		JOIN teams t ON sub.team_id=t.id
		JOIN clubs cl ON t.club_id=cl.id
		%s GROUP BY cl.name ORDER BY COUNT(DISTINCT sub.team_id) DESC, cl.name ASC
	`, whereExtra), args...)
	if err == nil {
		defer cbrows.Close()
		for cbrows.Next() {
			var c reportClub
			if cbrows.Scan(&c.Club, &c.Subs, &c.AvgPitch) == nil {
				rp.ClubBreakdown = append(rp.ClubBreakdown, c)
			}
		}
	}

	// Weekly trend (for monthly/season reports)
	if reportType != "weekly" {
		trows, err := s.DB.Query(ctx, `
			SELECT w.week_number, COUNT(DISTINCT sub.team_id), COALESCE(AVG(sub.pitch_rating),0)
			FROM weeks w
			LEFT JOIN submissions sub ON sub.week_id=w.id AND sub.season_id=$1
			WHERE w.season_id=$1
			GROUP BY w.week_number ORDER BY w.week_number
		`, seasonID)
		if err == nil {
			defer trows.Close()
			for trows.Next() {
				var t reportWeekTrend
				if trows.Scan(&t.WeekNumber, &t.Subs, &t.AvgPitch) == nil {
					rp.WeeklyTrend = append(rp.WeeklyTrend, t)
				}
			}
		}
	}

	// Sanctions count
	if weekID != nil {
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1 AND week_id=$2`,
			seasonID, *weekID).Scan(&rp.SanctionsIssued)
	} else {
		s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sanctions WHERE season_id=$1`, seasonID).
			Scan(&rp.SanctionsIssued)
	}

	payload, err := json.Marshal(rp)
	if err != nil {
		s.DB.Exec(ctx, `UPDATE generated_reports SET status='error', error_message=$1, completed_at=now() WHERE id=$2`,
			err.Error(), reportID)
		return
	}

	s.DB.Exec(ctx, `UPDATE generated_reports SET status='ready', payload_json=$1, completed_at=now() WHERE id=$2`,
		payload, reportID)
}

// extractWeekNum parses "2025-W14" → 14.
func extractWeekNum(period string) int {
	parts := strings.Split(period, "-W")
	if len(parts) == 2 {
		n, _ := strconv.Atoi(parts[1])
		return n
	}
	return 0
}
