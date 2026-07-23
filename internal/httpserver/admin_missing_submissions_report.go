package httpserver

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

type missingSubmissionWeek struct {
	ID        int32
	Number    int32
	StartDate time.Time
	EndDate   time.Time
	Season    string
}

type missingSubmissionFixture struct {
	WeekID             int32
	WeekNumber         int32
	Season             string
	MatchDate          time.Time
	TeamID             int32
	ClubName           string
	TeamName           string
	PlayCricketMatchID int64
	Opponent           string
	Ground             string
	CaptainName        string
	CaptainEmail       string
	HasSubmission      bool
	AdminResolved      bool
	SanctionStatus     string
	CaseID             int64
	CaseReference      string
	CaseStatus         string
	CardRequired       string
	PointsDeduction    int
}

type missingSubmissionTeamSummary struct {
	TeamID            int32
	ClubName          string
	TeamName          string
	MissingCount      int
	ActionableCount   int
	ResolvedCount     int
	MatchDates        []string
	LatestMatchDate   time.Time
	CaptainName       string
	CaptainEmail      string
	LastSanctionState string
}

type missingSubmissionsReportData struct {
	Weeks             []missingSubmissionWeek
	Rows              []missingSubmissionFixture
	Summary           []missingSubmissionTeamSummary
	ExpectedFixtures  int
	SubmittedFixtures int
	NoSubmission      int
	AdminResolved     int
	ActionableMissing int
	ComplianceRate    float64
	GeneratedAt       time.Time
	PeriodLabel       string
	CSVURL            string
}

func (s *Server) handleAdminMissingSubmissionsReport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		data, err := s.loadMissingSubmissionsReport(ctx)
		if err != nil {
			http.Error(w, "report error", http.StatusInternalServerError)
			return
		}
		latestWeekID := int32(0)
		if len(data.Weeks) > 0 {
			latestWeekID = data.Weeks[len(data.Weeks)-1].ID
		}
		data.CSVURL = fmt.Sprintf("/admin/reports/missing-submissions.csv?week_id=%d", latestWeekID)

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Missing Submissions Report")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))
		renderMissingSubmissionsReport(w, data)
		pageFooter(w)
	}
}

func (s *Server) handleAdminMissingSubmissionsReportCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		data, err := s.loadMissingSubmissionsReport(ctx)
		if err != nil {
			http.Error(w, "report error", http.StatusInternalServerError)
			return
		}

		requestedWeekID := int32(parsePositiveInt(r.URL.Query().Get("week_id")))
		if requestedWeekID == 0 && len(data.Weeks) > 0 {
			requestedWeekID = data.Weeks[len(data.Weeks)-1].ID
		}
		exportRows := data.Rows
		if requestedWeekID > 0 {
			exportRows = exportRows[:0]
			for _, row := range data.Rows {
				if row.WeekID == requestedWeekID {
					exportRows = append(exportRows, row)
				}
			}
		}

		filename := "missing-submissions-" + time.Now().Format("20060102-150405") + ".csv"
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

		cw := csv.NewWriter(w)
		_ = cw.Write([]string{
			"Season",
			"Week",
			"Match date",
			"Club",
			"Team",
			"Play-Cricket match ID",
			"Opponent",
			"Ground",
			"Captain",
			"Captain email",
			"Admin resolved",
			"Actionable missing",
			"Sanction status",
			"Card required",
			"Points deduction",
			"Sanctions case",
			"Case status",
		})
		for _, row := range exportRows {
			actionable := "Yes"
			if row.AdminResolved {
				actionable = "No"
			}
			resolved := "No"
			if row.AdminResolved {
				resolved = "Yes"
			}
			_ = cw.Write([]string{
				row.Season,
				strconv.Itoa(int(row.WeekNumber)),
				row.MatchDate.Format("2006-01-02"),
				row.ClubName,
				row.TeamName,
				strconv.FormatInt(row.PlayCricketMatchID, 10),
				row.Opponent,
				row.Ground,
				row.CaptainName,
				row.CaptainEmail,
				resolved,
				actionable,
				row.SanctionStatus,
				missingSubmissionCardLabel(row.CardRequired),
				strconv.Itoa(row.PointsDeduction),
				row.CaseReference,
				row.CaseStatus,
			})
		}
		cw.Flush()
	}
}

func (s *Server) loadMissingSubmissionsReport(ctx context.Context) (missingSubmissionsReportData, error) {
	data := missingSubmissionsReportData{GeneratedAt: time.Now()}

	wrows, err := s.DB.Query(ctx, `
		SELECT w.id, w.week_number, w.start_date, w.end_date, se.name
		FROM weeks w
		JOIN seasons se ON se.id = w.season_id
		WHERE se.is_archived = FALSE
		  AND w.start_date <= CURRENT_DATE
		ORDER BY w.start_date DESC, w.week_number DESC
		LIMIT 2
	`)
	if err != nil {
		return data, err
	}
	defer wrows.Close()
	for wrows.Next() {
		var wk missingSubmissionWeek
		if err := wrows.Scan(&wk.ID, &wk.Number, &wk.StartDate, &wk.EndDate, &wk.Season); err != nil {
			return data, err
		}
		data.Weeks = append(data.Weeks, wk)
	}
	if err := wrows.Err(); err != nil {
		return data, err
	}

	if len(data.Weeks) == 0 {
		data.PeriodLabel = "No active weeks found"
		return data, nil
	}

	sort.Slice(data.Weeks, func(i, j int) bool {
		return data.Weeks[i].StartDate.Before(data.Weeks[j].StartDate)
	})
	first := data.Weeks[0]
	last := data.Weeks[len(data.Weeks)-1]
	data.PeriodLabel = fmt.Sprintf("Weeks %d-%d, %s to %s", first.Number, last.Number,
		first.StartDate.Format("2 Jan 2006"), last.EndDate.Format("2 Jan 2006"))

	rows, err := s.DB.Query(ctx, `
		WITH selected_weeks AS (
		    SELECT w.id, w.week_number, w.start_date, w.end_date, se.name AS season_name
		    FROM weeks w
		    JOIN seasons se ON se.id = w.season_id
		    WHERE se.is_archived = FALSE
		      AND w.start_date <= CURRENT_DATE
		    ORDER BY w.start_date DESC, w.week_number DESC
		    LIMIT 2
		),
		expected_fixtures AS (
		    SELECT sw.id AS week_id,
		           sw.week_number,
		           sw.season_name,
		           lf.match_date,
		           t.id AS team_id,
		           cl.name AS club_name,
		           t.name AS team_name,
		           lf.play_cricket_match_id,
		           CASE
		               WHEN TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
		                   THEN CONCAT_WS(' ', NULLIF(lf.away_club_name, ''), NULLIF(lf.away_team_name, ''))
		               ELSE CONCAT_WS(' ', NULLIF(lf.home_club_name, ''), NULLIF(lf.home_team_name, ''))
		           END AS opponent,
		           COALESCE(lf.ground_name, '') AS ground,
		           COALESCE(cap.full_name, '') AS captain_name,
		           COALESCE(cap.email, '') AS captain_email,
		           ROW_NUMBER() OVER (
		               PARTITION BY t.id, lf.match_date
		               ORDER BY lf.play_cricket_match_id
		           ) AS fixture_ordinal
		    FROM selected_weeks sw
		    JOIN league_fixtures lf ON lf.match_date BETWEEN sw.start_date AND sw.end_date
		    JOIN teams t ON (
		        TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
		        OR TRIM(t.play_cricket_team_id) = TRIM(lf.away_team_pc_id)
		    )
		    JOIN clubs cl ON cl.id = t.club_id
		    LEFT JOIN LATERAL (
		        SELECT c.full_name, c.email
		        FROM captains c
		        WHERE c.team_id = t.id
		          AND c.active_from <= lf.match_date
		          AND (c.active_to IS NULL OR c.active_to >= lf.match_date)
		        ORDER BY c.active_from DESC, c.id DESC
		        LIMIT 1
		    ) cap ON TRUE
		    WHERE t.active = TRUE
		      AND t.play_cricket_team_id IS NOT NULL
		      AND t.play_cricket_team_id <> ''
		      AND EXTRACT(DOW FROM lf.match_date) <> 5
		      AND NOT lf.is_bye
		),
		legacy_submissions AS (
		    SELECT sub.team_id,
		           sub.match_date,
		           COUNT(*) AS legacy_count
		    FROM submissions sub
		    JOIN selected_weeks sw ON sw.id = sub.week_id
		    WHERE sub.play_cricket_match_id IS NULL
		    GROUP BY sub.team_id, sub.match_date
		),
		fixture_status AS (
		    SELECT ef.*,
		           (
		               EXISTS (
		                   SELECT 1 FROM submissions sub
		                   WHERE sub.team_id = ef.team_id
		                     AND sub.week_id = ef.week_id
		                     AND sub.play_cricket_match_id = ef.play_cricket_match_id
		               )
		               OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
		           ) AS has_submission,
		           EXISTS (
		               SELECT 1 FROM report_exemptions re
		               WHERE re.team_id = ef.team_id
		                 AND re.week_id = ef.week_id
		                 AND re.match_date = ef.match_date
		                 AND (
		                     re.play_cricket_match_id = ef.play_cricket_match_id
		                     OR re.play_cricket_match_id IS NULL
		                 )
		           ) AS admin_resolved,
		           COALESCE((
		               SELECT STRING_AGG(DISTINCT UPPER(sa.colour::text) || ' / ' || COALESCE(sa.email_status, 'pending'), ', ')
		               FROM sanctions sa
		               WHERE sa.team_id = ef.team_id
		                 AND sa.week_id = ef.week_id
		                 AND sa.reason = 'non_submission'
		                 AND sa.status IN ('active', 'served')
		           ), '') AS sanction_status,
		           COALESCE(staged.case_id,0),
		           COALESCE(staged.reference,''),
		           COALESCE(staged.case_status,''),
		           COALESCE(staged.effect_type,''),
		           COALESCE(staged.points,0)
		    FROM expected_fixtures ef
		    LEFT JOIN legacy_submissions ls
		      ON ls.team_id = ef.team_id AND ls.match_date = ef.match_date
		    LEFT JOIN LATERAL (
		        SELECT sc.id AS case_id,
		               sc.reference,
		               sc.status AS case_status,
		               effect.effect_type,
		               effect.points
		        FROM sanction_cases sc
		        JOIN LATERAL (
		            SELECT e.effect_type, COALESCE(e.points,0) AS points
		            FROM sanction_decision_revisions d
		            JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id
		            WHERE d.case_id=sc.id
		            ORDER BY d.revision DESC,e.id DESC
		            LIMIT 1
		        ) effect ON TRUE
		        WHERE sc.team_id=ef.team_id
		          AND sc.week_id=ef.week_id
		          AND sc.source_type='captain_report'
		          AND sc.status NOT IN ('rejected','withdrawn')
		        ORDER BY sc.id DESC
		        LIMIT 1
		    ) staged ON TRUE
		)
		SELECT week_id, week_number, season_name, match_date, team_id, club_name, team_name,
		       play_cricket_match_id, opponent, ground, captain_name, captain_email,
		       has_submission, admin_resolved, sanction_status,
		       case_id,reference,case_status,effect_type,points
		FROM fixture_status
		ORDER BY week_number DESC, match_date DESC, club_name, team_name, play_cricket_match_id
	`)
	if err != nil {
		return data, err
	}
	defer rows.Close()

	summaryByTeam := map[int32]*missingSubmissionTeamSummary{}
	for rows.Next() {
		var row missingSubmissionFixture
		if err := rows.Scan(
			&row.WeekID,
			&row.WeekNumber,
			&row.Season,
			&row.MatchDate,
			&row.TeamID,
			&row.ClubName,
			&row.TeamName,
			&row.PlayCricketMatchID,
			&row.Opponent,
			&row.Ground,
			&row.CaptainName,
			&row.CaptainEmail,
			&row.HasSubmission,
			&row.AdminResolved,
			&row.SanctionStatus,
			&row.CaseID,
			&row.CaseReference,
			&row.CaseStatus,
			&row.CardRequired,
			&row.PointsDeduction,
		); err != nil {
			return data, err
		}

		data.ExpectedFixtures++
		if row.HasSubmission {
			data.SubmittedFixtures++
			continue
		}

		data.NoSubmission++
		if row.AdminResolved {
			data.AdminResolved++
		} else {
			data.ActionableMissing++
		}
		data.Rows = append(data.Rows, row)

		summary := summaryByTeam[row.TeamID]
		if summary == nil {
			summary = &missingSubmissionTeamSummary{
				TeamID:       row.TeamID,
				ClubName:     row.ClubName,
				TeamName:     row.TeamName,
				CaptainName:  row.CaptainName,
				CaptainEmail: row.CaptainEmail,
			}
			summaryByTeam[row.TeamID] = summary
		}
		summary.MissingCount++
		if row.AdminResolved {
			summary.ResolvedCount++
		} else {
			summary.ActionableCount++
		}
		summary.MatchDates = append(summary.MatchDates, row.MatchDate.Format("2 Jan"))
		if row.MatchDate.After(summary.LatestMatchDate) {
			summary.LatestMatchDate = row.MatchDate
			summary.LastSanctionState = row.SanctionStatus
			if row.CaseID > 0 {
				summary.LastSanctionState = missingSubmissionCardLabel(row.CardRequired) + " — " + row.CaseReference + " — " + strings.ReplaceAll(row.CaseStatus, "_", " ")
			}
		}
	}
	if err := rows.Err(); err != nil {
		return data, err
	}

	for _, summary := range summaryByTeam {
		data.Summary = append(data.Summary, *summary)
	}
	sort.Slice(data.Summary, func(i, j int) bool {
		if data.Summary[i].ActionableCount != data.Summary[j].ActionableCount {
			return data.Summary[i].ActionableCount > data.Summary[j].ActionableCount
		}
		if data.Summary[i].MissingCount != data.Summary[j].MissingCount {
			return data.Summary[i].MissingCount > data.Summary[j].MissingCount
		}
		if data.Summary[i].ClubName != data.Summary[j].ClubName {
			return data.Summary[i].ClubName < data.Summary[j].ClubName
		}
		return data.Summary[i].TeamName < data.Summary[j].TeamName
	})

	if data.ExpectedFixtures > 0 {
		data.ComplianceRate = float64(data.SubmittedFixtures) / float64(data.ExpectedFixtures) * 100
	}
	return data, nil
}

func renderMissingSubmissionsReport(w http.ResponseWriter, data missingSubmissionsReportData) {
	latestWeekID := int32(0)
	if len(data.Weeks) > 0 {
		latestWeekID = data.Weeks[len(data.Weeks)-1].ID
	}
	stagedCount := 0
	for _, row := range data.Rows {
		if row.CaseID > 0 {
			stagedCount++
		}
	}
	fmt.Fprintf(w, `<div class="container-fluid px-4 py-4">
<div class="d-flex flex-wrap align-items-start justify-content-between gap-3 mb-4">
  <div>
    <h4 class="mb-1 fw-bold">Missing Submissions Report</h4>
    <p class="text-muted mb-0">%s</p>
    <p class="text-muted small mb-0">Generated %s</p>
  </div>
  <a class="btn btn-outline-primary btn-sm" href="%s">Export CSV</a>
</div>`,
		escapeHTML(data.PeriodLabel),
		escapeHTML(data.GeneratedAt.Format("2 Jan 2006 15:04")),
		escapeHTML(data.CSVURL),
	)
	fmt.Fprintf(w, `<div class="alert alert-info"><strong>Automatic staging is in manual-approval mode.</strong> %d missing-submission sanction case(s) are staged below. Staging calculates the card but does not publish it or queue any email. Open a case to review it.</div>`, stagedCount)

	fmt.Fprintf(w, `<div class="row g-3 mb-4">
  <div class="col-sm-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="text-muted small">Expected Fixtures</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col-sm-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="text-muted small">Submitted</div><div class="fs-4 fw-semibold text-success">%d</div></div></div>
  <div class="col-sm-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="text-muted small">No Submission</div><div class="fs-4 fw-semibold text-danger">%d</div></div></div>
  <div class="col-sm-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="text-muted small">Action Required</div><div class="fs-4 fw-semibold text-warning">%d</div></div></div>
  <div class="col-sm-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="text-muted small">Admin Resolved</div><div class="fs-4 fw-semibold text-info">%d</div></div></div>
  <div class="col-sm-6 col-lg-2"><div class="border rounded p-3 h-100"><div class="text-muted small">Compliance</div><div class="fs-4 fw-semibold">%.0f%%</div></div></div>
</div>`,
		data.ExpectedFixtures, data.SubmittedFixtures, data.NoSubmission, data.ActionableMissing, data.AdminResolved, data.ComplianceRate)

	if len(data.Weeks) == 0 {
		fmt.Fprint(w, `<div class="alert alert-warning">No active season weeks were found.</div></div>`)
		return
	}
	if len(data.Rows) == 0 {
		fmt.Fprint(w, `<div class="alert alert-success">No missing submissions found for the selected two-week period.</div></div>`)
		return
	}

	fmt.Fprintf(w, `<div class="card shadow-sm mb-4"><div class="card-body"><div class="row g-3 align-items-end">
  <div class="col-md-3"><label class="form-label" for="missing-week-filter">Week</label><select class="form-select" id="missing-week-filter"><option value="">All shown weeks</option>`)
	for _, week := range data.Weeks {
		selected := ""
		if week.ID == latestWeekID {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s — Week %d (%s–%s)</option>`, week.ID, selected, escapeHTML(week.Season), week.Number, week.StartDate.Format("2 Jan"), week.EndDate.Format("2 Jan"))
	}
	fmt.Fprint(w, `</select></div>
  <div class="col-md-3"><label class="form-label" for="missing-card-filter">Card required</label><select class="form-select" id="missing-card-filter"><option value="">All cards</option><option value="yellow_card">Yellow</option><option value="red_card">Red</option><option value="unstaged">Not staged</option></select></div>
  <div class="col-md-3"><label class="form-label" for="missing-status-filter">Case status</label><select class="form-select" id="missing-status-filter"><option value="">All statuses</option><option value="decision_proposed">Proposed — needs approval</option><option value="published">Published</option><option value="unstaged">Not staged</option></select></div>
  <div class="col-md-3"><label class="form-label" for="missing-search-filter">Club or team</label><input class="form-control" id="missing-search-filter" type="search" placeholder="Filter club or team"></div>
</div><div class="small text-muted mt-2" id="missing-filter-count" aria-live="polite"></div></div></div>`)

	fmt.Fprint(w, `<div class="mb-4">
  <h5 class="fw-semibold mb-2">Teams With Missing Reports</h5>
  <div class="table-responsive">
    <table class="table table-sm table-hover align-middle">
      <thead class="table-light"><tr><th>Club</th><th>Team</th><th class="text-end">Missing</th><th class="text-end">Action Required</th><th>Dates</th><th>Captain</th><th>Latest Sanction</th></tr></thead>
      <tbody>`)
	for _, row := range data.Summary {
		sanction := row.LastSanctionState
		if sanction == "" {
			sanction = "-"
		}
		fmt.Fprintf(w, `<tr>
  <td>%s</td>
  <td>%s</td>
  <td class="text-end fw-semibold">%d</td>
  <td class="text-end">%d</td>
  <td>%s</td>
  <td>%s<br><span class="text-muted small">%s</span></td>
  <td>%s</td>
</tr>`,
			escapeHTML(row.ClubName),
			escapeHTML(row.TeamName),
			row.MissingCount,
			row.ActionableCount,
			escapeHTML(strings.Join(uniqueStrings(row.MatchDates), ", ")),
			escapeHTML(blankDash(row.CaptainName)),
			escapeHTML(row.CaptainEmail),
			escapeHTML(sanction),
		)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)

	fmt.Fprint(w, `<div>
  <h5 class="fw-semibold mb-2">Fixture Detail</h5>
  <div class="table-responsive">
    <table class="table table-sm table-hover align-middle">
      <thead class="table-light"><tr><th>Week</th><th>Date</th><th>Club</th><th>Team</th><th>Opponent</th><th>Ground</th><th>Match</th><th>Status</th><th>Card required</th><th>Sanctions case</th></tr></thead>
      <tbody>`)
	for _, row := range data.Rows {
		status := `<span class="badge bg-danger">Action required</span>`
		if row.AdminResolved {
			status = `<span class="badge bg-info text-dark">Admin resolved</span>`
		}
		cardRequired := missingSubmissionCardLabel(row.CardRequired)
		if row.PointsDeduction > 0 {
			cardRequired += fmt.Sprintf(" — %d point deduction", row.PointsDeduction)
		}
		caseStatus := "unstaged"
		caseHTML := `<span class="text-warning">Not staged yet</span>`
		if row.CaseID > 0 {
			caseStatus = row.CaseStatus
			caseHTML = fmt.Sprintf(`<a href="/admin/cases/%d">%s</a><div class="small text-muted">%s — no email queued</div>`, row.CaseID, escapeHTML(row.CaseReference), escapeHTML(strings.ReplaceAll(row.CaseStatus, "_", " ")))
		} else if row.SanctionStatus != "" {
			caseHTML = `<span class="text-muted">Legacy: ` + escapeHTML(row.SanctionStatus) + `</span>`
		}
		cardFilter := row.CardRequired
		if cardFilter == "" {
			cardFilter = "unstaged"
		}
		fmt.Fprintf(w, `<tr class="missing-submission-row" data-week="%d" data-card="%s" data-case-status="%s" data-search="%s">
  <td>%d</td>
  <td>%s</td>
  <td>%s</td>
  <td>%s</td>
  <td>%s</td>
  <td>%s</td>
  <td>%d</td>
  <td>%s</td>
  <td>%s</td>
  <td>%s</td>
</tr>`,
			row.WeekID,
			escapeHTML(cardFilter),
			escapeHTML(caseStatus),
			escapeHTML(strings.ToLower(row.ClubName+" "+row.TeamName)),
			row.WeekNumber,
			escapeHTML(row.MatchDate.Format("2 Jan 2006")),
			escapeHTML(row.ClubName),
			escapeHTML(row.TeamName),
			escapeHTML(blankDash(row.Opponent)),
			escapeHTML(blankDash(row.Ground)),
			row.PlayCricketMatchID,
			status,
			escapeHTML(cardRequired),
			caseHTML,
		)
	}
	fmt.Fprint(w, `</tbody></table></div></div></div>
<script>
(function () {
  const week = document.getElementById('missing-week-filter');
  const card = document.getElementById('missing-card-filter');
  const status = document.getElementById('missing-status-filter');
  const search = document.getElementById('missing-search-filter');
  const count = document.getElementById('missing-filter-count');
  const exportLink = document.querySelector('a[href^="/admin/reports/missing-submissions.csv"]');
  const rows = Array.from(document.querySelectorAll('.missing-submission-row'));
  function applyFilters() {
    const query = search.value.trim().toLowerCase();
    let shown = 0;
    rows.forEach(function (row) {
      const visible = (!week.value || row.dataset.week === week.value) &&
        (!card.value || row.dataset.card === card.value) &&
        (!status.value || row.dataset.caseStatus === status.value) &&
        (!query || row.dataset.search.includes(query));
      row.hidden = !visible;
      if (visible) shown++;
    });
    count.textContent = shown + ' missing fixture(s) shown.';
    if (exportLink) {
      exportLink.href = '/admin/reports/missing-submissions.csv' + (week.value ? '?week_id=' + encodeURIComponent(week.value) : '');
    }
  }
  [week, card, status, search].forEach(function (control) {
    control.addEventListener(control === search ? 'input' : 'change', applyFilters);
  });
  applyFilters();
}());
</script>`)
}

func missingSubmissionCardLabel(effectType string) string {
	switch effectType {
	case "yellow_card":
		return "Yellow card"
	case "red_card":
		return "Red card"
	case "suspended_red":
		return "Suspended red card"
	case "":
		return "Not staged"
	default:
		return strings.ReplaceAll(effectType, "_", " ")
	}
}

func blankDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
