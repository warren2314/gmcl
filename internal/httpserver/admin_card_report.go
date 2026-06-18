package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

type cardReportWeek struct {
	ID         int32
	SeasonID   int32
	SeasonName string
	Number     int32
	StartDate  time.Time
	EndDate    time.Time
}

type weeklyCardReportFixture struct {
	MatchDate          time.Time
	Opposition         string
	PlayCricketMatchID int64
}

type weeklyCardReportRow struct {
	TeamID              int32
	ClubID              int32
	ClubName            string
	TeamName            string
	FixtureCount        int
	SubmittedCount      int
	MissingCount        int
	MissingFixtures     []weeklyCardReportFixture
	PriorYellowCount    int64
	PriorRedCount       int64
	PriorOffenceCount   int64
	OffenceNumber       int64
	CardDue             string
	PointsDeduction     int
	ExistingSanctionID  *int64
	ExistingCard        string
	ExistingReason      string
	ExistingStatus      string
	ExistingEmailStatus string
	ExistingPoints      *int32
	ExistingIssuedAt    *time.Time
}

func (s *Server) handleAdminWeeklyCardReport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		week, err := s.resolveCardReportWeek(ctx, int32(parsePositiveInt(r.URL.Query().Get("week_id"))))
		if err != nil {
			http.Error(w, "no report week found", http.StatusNotFound)
			return
		}
		weeks := s.cardReportWeekOptions(ctx)
		rows, err := s.weeklyCardReportRows(ctx, week)
		if err != nil {
			http.Error(w, "could not build card report: "+err.Error(), http.StatusInternalServerError)
			return
		}

		csrfToken := middleware.CSRFToken(r)
		successMsg := strings.TrimSpace(r.URL.Query().Get("success"))
		errorMsg := strings.TrimSpace(r.URL.Query().Get("error"))

		yellowDue, redDue, issuedCount := cardReportCounts(rows)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Weekly Card Report")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		fmt.Fprint(w, `<div class="container-fluid px-4">`)
		fmt.Fprintf(w, `
<div class="d-flex align-items-start justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Weekly Card Report</h4>
    <p class="text-muted mb-0 small">%s - Week %d, %s to %s</p>
  </div>
  <div class="d-flex gap-2 align-items-center">
    <form method="GET" action="/admin/sanctions/weekly-report" class="d-flex gap-2 align-items-center">
      <select name="week_id" class="form-select form-select-sm" onchange="this.form.submit()">
`, escapeHTML(week.SeasonName), week.Number, week.StartDate.Format("2 Jan 2006"), week.EndDate.Format("2 Jan 2006"))
		for _, opt := range weeks {
			sel := ""
			if opt.ID == week.ID {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s - Week %d</option>`, opt.ID, sel, escapeHTML(opt.SeasonName), opt.Number)
		}
		fmt.Fprint(w, `      </select>
    </form>
    <a href="/admin/sanctions" class="btn btn-sm btn-outline-secondary">Back to Sanctions</a>
  </div>
</div>
`)
		if successMsg != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(successMsg))
		}
		if errorMsg != "" {
			fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(errorMsg))
		}

		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-auto"><div class="card card-kpi kpi-amber p-3 text-center" style="min-width:120px"><div class="kpi-number" style="color:#856404">%d</div><div class="kpi-label">Yellow Due</div></div></div>
  <div class="col-auto"><div class="card card-kpi kpi-red p-3 text-center" style="min-width:120px"><div class="kpi-number text-danger">%d</div><div class="kpi-label">Red Due</div></div></div>
  <div class="col-auto"><div class="card card-kpi kpi-purple p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Already Issued</div></div></div>
  <div class="col-auto"><div class="card card-kpi kpi-blue p-3 text-center" style="min-width:120px"><div class="kpi-number">%d</div><div class="kpi-label">Report Rows</div></div></div>
</div>
`, yellowDue, redDue, issuedCount, len(rows))

		if len(rows) == 0 {
			fmt.Fprint(w, `<div class="card shadow-sm"><div class="card-body text-muted">No card candidates or active cards found for this week.</div></div></div>`)
			pageFooter(w)
			return
		}

		fmt.Fprintf(w, `
<form method="POST" action="/admin/sanctions/weekly-report/pdf">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="week_id" value="%d">
  <div class="card shadow-sm mb-4">
    <div class="card-header d-flex justify-content-between align-items-center">
      <span class="fw-semibold">Review Clubs Before Publishing</span>
      <div class="d-flex gap-2">
        <button type="button" class="btn btn-sm btn-outline-secondary" onclick="document.querySelectorAll('.card-report-check').forEach(function(c){c.checked=true})">Select all</button>
        <button type="button" class="btn btn-sm btn-outline-secondary" onclick="document.querySelectorAll('.card-report-check').forEach(function(c){c.checked=false})">Clear</button>
        <button type="submit" class="btn btn-sm btn-primary">Download PDF</button>
      </div>
    </div>
    <div class="table-responsive">
      <table class="table table-hover table-gmcl mb-0 align-middle">
        <thead><tr>
          <th>Include</th><th>Club</th><th>Team</th><th>Missing Details</th><th>Prior Offences</th><th>Card Due</th><th>Current Card</th>
        </tr></thead>
        <tbody>
`, escapeHTML(csrfToken), week.ID)

		for _, row := range rows {
			fmt.Fprintf(w, `<tr>
  <td><input class="form-check-input card-report-check" type="checkbox" name="team_id" value="%d" checked aria-label="Include %s"></td>
  <td>%s</td>
  <td>%s</td>
  <td class="small">%s</td>
  <td class="small">%s</td>
  <td>%s</td>
  <td class="small">%s</td>
</tr>`, row.TeamID, escapeHTML(row.ClubName),
				escapeHTML(row.ClubName), escapeHTML(row.TeamName),
				cardReportMissingHTML(row),
				cardReportPriorHTML(row),
				cardReportDueBadge(row),
				cardReportCurrentHTML(row))
		}

		fmt.Fprint(w, `        </tbody>
      </table>
    </div>
  </div>
</form>
</div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminWeeklyCardReportPDF() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		weekID := int32(parsePositiveInt(r.FormValue("week_id")))
		selected := map[int32]bool{}
		for _, raw := range r.Form["team_id"] {
			if id := parsePositiveInt(raw); id > 0 {
				selected[int32(id)] = true
			}
		}
		if len(selected) == 0 {
			target := fmt.Sprintf("/admin/sanctions/weekly-report?week_id=%d", weekID)
			http.Redirect(w, r, redirectWithMessage(target, "error", "Select at least one club/team for the PDF."), http.StatusSeeOther)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		week, err := s.resolveCardReportWeek(ctx, weekID)
		if err != nil {
			http.Error(w, "week not found", http.StatusNotFound)
			return
		}
		allRows, err := s.weeklyCardReportRows(ctx, week)
		if err != nil {
			http.Error(w, "could not build card report: "+err.Error(), http.StatusInternalServerError)
			return
		}

		rows := make([]weeklyCardReportRow, 0, len(allRows))
		for _, row := range allRows {
			if selected[row.TeamID] {
				rows = append(rows, row)
			}
		}
		if len(rows) == 0 {
			target := fmt.Sprintf("/admin/sanctions/weekly-report?week_id=%d", week.ID)
			http.Redirect(w, r, redirectWithMessage(target, "error", "None of the selected rows are still valid for this report."), http.StatusSeeOther)
			return
		}

		pdf := buildWeeklyCardReportPDF(week, rows, time.Now().In(s.LondonLoc))
		filename := fmt.Sprintf("gmcl-week-%02d-card-report.pdf", week.Number)
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(pdf)
	}
}

func (s *Server) resolveCardReportWeek(ctx context.Context, requestedWeekID int32) (cardReportWeek, error) {
	var week cardReportWeek
	if requestedWeekID > 0 {
		err := s.DB.QueryRow(ctx, `
			SELECT w.id, s.id, s.name, w.week_number, w.start_date, w.end_date
			FROM weeks w
			JOIN seasons s ON s.id = w.season_id
			WHERE w.id = $1
		`, requestedWeekID).Scan(&week.ID, &week.SeasonID, &week.SeasonName, &week.Number, &week.StartDate, &week.EndDate)
		return week, err
	}
	err := s.DB.QueryRow(ctx, `
		SELECT w.id, s.id, s.name, w.week_number, w.start_date, w.end_date
		FROM weeks w
		JOIN seasons s ON s.id = w.season_id
		WHERE s.is_archived = FALSE
		ORDER BY
		    CASE WHEN CURRENT_DATE BETWEEN w.start_date AND w.end_date THEN 0
		         WHEN w.start_date > CURRENT_DATE THEN 1
		         ELSE 2 END,
		    abs(w.start_date - CURRENT_DATE)
		LIMIT 1
	`).Scan(&week.ID, &week.SeasonID, &week.SeasonName, &week.Number, &week.StartDate, &week.EndDate)
	return week, err
}

func (s *Server) cardReportWeekOptions(ctx context.Context) []cardReportWeek {
	rows, err := s.DB.Query(ctx, `
		SELECT w.id, s.id, s.name, w.week_number, w.start_date, w.end_date
		FROM weeks w
		JOIN seasons s ON s.id = w.season_id
		WHERE s.is_archived = FALSE
		ORDER BY s.start_date DESC, w.week_number DESC
		LIMIT 40
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var weeks []cardReportWeek
	for rows.Next() {
		var week cardReportWeek
		if rows.Scan(&week.ID, &week.SeasonID, &week.SeasonName, &week.Number, &week.StartDate, &week.EndDate) == nil {
			weeks = append(weeks, week)
		}
	}
	return weeks
}

func (s *Server) weeklyCardReportRows(ctx context.Context, week cardReportWeek) ([]weeklyCardReportRow, error) {
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
		    WHERE t.play_cricket_team_id IS NOT NULL AND t.play_cricket_team_id <> ''
		      AND lf.match_date BETWEEN $3 AND $4
		      AND EXTRACT(DOW FROM lf.match_date) <> 5
		      AND NOT lf.is_bye
		),
		legacy_submissions AS (
		    SELECT team_id,
		           match_date,
		           COUNT(*) AS legacy_count
		    FROM submissions
		    WHERE week_id = $1 AND play_cricket_match_id IS NULL
		    GROUP BY team_id, match_date
		),
		fixture_counts AS (
		    SELECT ef.team_id,
		           COUNT(*) AS fixture_count,
		           COUNT(*) FILTER (
		               WHERE EXISTS (
		                   SELECT 1 FROM submissions sub
		                   WHERE sub.week_id = $1
		                     AND sub.team_id = ef.team_id
		                     AND sub.play_cricket_match_id = ef.play_cricket_match_id
		               )
		               OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
		               OR EXISTS (
		                   SELECT 1 FROM report_exemptions re
		                   WHERE re.week_id = $1
		                     AND re.team_id = ef.team_id
		                     AND re.match_date = ef.match_date
		                     AND (
		                         re.play_cricket_match_id = ef.play_cricket_match_id
		                         OR re.play_cricket_match_id IS NULL
		                     )
		               )
		           ) AS submit_count
		    FROM expected_fixtures ef
		    LEFT JOIN legacy_submissions ls
		      ON ls.team_id = ef.team_id AND ls.match_date = ef.match_date
		    GROUP BY ef.team_id
		),
		current_sanction AS (
		    SELECT DISTINCT ON (team_id)
		           team_id,
		           id,
		           colour::text AS colour,
		           reason,
		           status::text AS status,
		           COALESCE(email_status, 'pending') AS email_status,
		           points_deduction,
		           issued_at
		    FROM sanctions
		    WHERE season_id = $2
		      AND week_id = $1
		      AND status IN ('active', 'served')
		    ORDER BY team_id, issued_at DESC, id DESC
		),
		prior_counts AS (
		    SELECT team_id,
		           COUNT(*) FILTER (WHERE colour = 'yellow') AS yellow_count,
		           COUNT(*) FILTER (WHERE colour = 'red') AS red_count,
		           COUNT(*) AS offence_count
		    FROM sanctions
		    WHERE season_id = $2
		      AND week_id <> $1
		      AND reason = 'non_submission'
		      AND status IN ('active', 'served')
		    GROUP BY team_id
		)
		SELECT
		    t.id,
		    t.club_id,
		    cl.name,
		    t.name,
		    COALESCE(fc.fixture_count, 0),
		    COALESCE(fc.submit_count, 0),
		    COALESCE(pc.yellow_count, 0),
		    COALESCE(pc.red_count, 0),
		    COALESCE(pc.offence_count, 0),
		    cs.id,
		    COALESCE(cs.colour, ''),
		    COALESCE(cs.reason, 'non_submission'),
		    COALESCE(cs.status, ''),
		    COALESCE(cs.email_status, ''),
		    cs.points_deduction,
		    cs.issued_at
		FROM teams t
		JOIN clubs cl ON cl.id = t.club_id
		LEFT JOIN fixture_counts fc ON fc.team_id = t.id
		LEFT JOIN current_sanction cs ON cs.team_id = t.id
		LEFT JOIN prior_counts pc ON pc.team_id = t.id
		WHERE t.active = TRUE
		  AND (
		      (COALESCE(fc.fixture_count, 0) > 0 AND COALESCE(fc.submit_count, 0) < COALESCE(fc.fixture_count, 0))
		      OR cs.team_id IS NOT NULL
		  )
		ORDER BY
		    CASE WHEN COALESCE(cs.colour, '') = 'red' THEN 0
		         WHEN ((COALESCE(pc.offence_count, 0) + 1) % 3) = 0 THEN 1
		         ELSE 2 END,
		    cl.name,
		    t.name
	`, week.ID, week.SeasonID, week.StartDate, week.EndDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []weeklyCardReportRow
	for rows.Next() {
		var row weeklyCardReportRow
		if err := rows.Scan(&row.TeamID, &row.ClubID, &row.ClubName, &row.TeamName,
			&row.FixtureCount, &row.SubmittedCount,
			&row.PriorYellowCount, &row.PriorRedCount, &row.PriorOffenceCount,
			&row.ExistingSanctionID, &row.ExistingCard, &row.ExistingReason,
			&row.ExistingStatus, &row.ExistingEmailStatus,
			&row.ExistingPoints, &row.ExistingIssuedAt); err != nil {
			return nil, err
		}
		row.MissingCount = row.FixtureCount - row.SubmittedCount
		if row.MissingCount < 0 {
			row.MissingCount = 0
		}
		row.OffenceNumber = row.PriorOffenceCount + 1
		row.CardDue, row.PointsDeduction = cardDueForOffence(row.OffenceNumber, row.PriorRedCount)
		if row.ExistingReason != "" && row.ExistingReason != "non_submission" && row.ExistingCard != "" {
			row.CardDue = row.ExistingCard
			if row.ExistingCard == "red" && row.ExistingPoints != nil {
				row.PointsDeduction = int(*row.ExistingPoints)
			}
		}
		if row.ExistingCard == row.CardDue && row.ExistingCard == "red" && row.ExistingPoints != nil {
			row.PointsDeduction = int(*row.ExistingPoints)
		}
		for _, f := range s.outstandingFixtures(ctx, row.TeamID, week.StartDate, week.EndDate) {
			row.MissingFixtures = append(row.MissingFixtures, weeklyCardReportFixture{
				MatchDate:          f.MatchDate,
				Opposition:         f.Opposition,
				PlayCricketMatchID: f.PlayCricketMatchID,
			})
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func parsePositiveInt(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func cardDueForOffence(offenceNumber, priorRedCount int64) (string, int) {
	if offenceNumber > 0 && offenceNumber%3 == 0 {
		return "red", int(priorRedCount) + 1
	}
	return "yellow", 0
}

func cardReportCounts(rows []weeklyCardReportRow) (yellowDue, redDue, issued int) {
	for _, row := range rows {
		if row.CardDue == "red" {
			redDue++
		} else {
			yellowDue++
		}
		if row.ExistingSanctionID != nil {
			issued++
		}
	}
	return yellowDue, redDue, issued
}

func cardReportMissingHTML(row weeklyCardReportRow) string {
	if row.MissingCount <= 0 {
		return `<span class="text-muted">No outstanding fixture now; current card remains listed.</span>`
	}
	var parts []string
	for _, fixture := range row.MissingFixtures {
		label := fixture.MatchDate.Format("Mon 2 Jan")
		if fixture.Opposition != "" {
			label += " vs " + fixture.Opposition
		}
		parts = append(parts, escapeHTML(label))
	}
	if len(parts) == 0 {
		return fmt.Sprintf(`<span class="text-danger">%d missing report(s)</span>`, row.MissingCount)
	}
	return fmt.Sprintf(`<span class="text-danger">%d missing report(s):</span><br>%s`, row.MissingCount, strings.Join(parts, "<br>"))
}

func cardReportPriorHTML(row weeklyCardReportRow) string {
	if row.ExistingReason != "" && row.ExistingReason != "non_submission" {
		return `<span class="text-muted">Manual/late card</span>`
	}
	return fmt.Sprintf(`<span class="text-muted">Yellow %d / Red %d</span><br>Next offence: %d`,
		row.PriorYellowCount, row.PriorRedCount, row.OffenceNumber)
}

func cardReportDueBadge(row weeklyCardReportRow) string {
	if row.CardDue == "red" {
		points := ""
		if row.PointsDeduction > 0 {
			points = fmt.Sprintf(`<br><span class="small">%d point deduction</span>`, row.PointsDeduction)
		}
		return `<span class="badge badge-red-card">Red Card</span>` + points
	}
	return `<span class="badge badge-yellow-card">Yellow Card</span>`
}

func cardReportCurrentHTML(row weeklyCardReportRow) string {
	if row.ExistingSanctionID == nil {
		return `<span class="text-muted">Not issued yet</span>`
	}
	card := strings.Title(row.ExistingCard) + " card"
	issuedAt := ""
	if row.ExistingIssuedAt != nil {
		issuedAt = "<br>Issued " + escapeHTML(row.ExistingIssuedAt.Format("02 Jan 15:04"))
	}
	points := ""
	if row.ExistingCard == "red" && row.ExistingPoints != nil && *row.ExistingPoints > 0 {
		points = fmt.Sprintf("<br>%d point deduction", *row.ExistingPoints)
	}
	return fmt.Sprintf(`<strong>%s #%d</strong><br>%s / %s%s%s<br>%s`,
		escapeHTML(card), *row.ExistingSanctionID,
		escapeHTML(statusLabel(row.ExistingStatus)),
		escapeHTML(emailStatusLabel(row.ExistingEmailStatus)),
		points,
		issuedAt,
		escapeHTML(reasonLabel(row.ExistingReason)))
}

func cardReportMissingText(row weeklyCardReportRow) string {
	if row.MissingCount <= 0 {
		return "No outstanding fixture now; current card remains listed."
	}
	var parts []string
	for _, fixture := range row.MissingFixtures {
		label := fixture.MatchDate.Format("Mon 2 Jan")
		if fixture.Opposition != "" {
			label += " vs " + fixture.Opposition
		}
		if fixture.PlayCricketMatchID > 0 {
			label += fmt.Sprintf(" (match %d)", fixture.PlayCricketMatchID)
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d missing report(s)", row.MissingCount)
	}
	return fmt.Sprintf("%d missing report(s): %s", row.MissingCount, strings.Join(parts, "; "))
}

func cardReportPriorText(row weeklyCardReportRow) string {
	if row.ExistingReason != "" && row.ExistingReason != "non_submission" {
		return "Manual/late card"
	}
	return fmt.Sprintf("Yellow %d / Red %d; next offence %d", row.PriorYellowCount, row.PriorRedCount, row.OffenceNumber)
}

func cardReportDueText(row weeklyCardReportRow) string {
	if row.CardDue == "red" {
		if row.PointsDeduction > 0 {
			return fmt.Sprintf("Red card - %d point deduction", row.PointsDeduction)
		}
		return "Red card"
	}
	return "Yellow card"
}

func cardReportCurrentText(row weeklyCardReportRow) string {
	if row.ExistingSanctionID == nil {
		return "Not issued yet"
	}
	text := fmt.Sprintf("%s card #%d; %s; letter %s",
		strings.Title(row.ExistingCard),
		*row.ExistingSanctionID,
		statusLabel(row.ExistingStatus),
		emailStatusLabel(row.ExistingEmailStatus))
	if row.ExistingCard == "red" && row.ExistingPoints != nil && *row.ExistingPoints > 0 {
		text += fmt.Sprintf("; %d point deduction", *row.ExistingPoints)
	}
	if row.ExistingIssuedAt != nil {
		text += "; issued " + row.ExistingIssuedAt.Format("02 Jan 15:04")
	}
	return text
}

func reasonLabel(reason string) string {
	switch reason {
	case "non_submission", "":
		return "Non-submission"
	case "late_submission":
		return "Late submission"
	case "manual":
		return "Manual"
	default:
		return strings.ReplaceAll(strings.Title(strings.ReplaceAll(reason, "_", " ")), "  ", " ")
	}
}

func statusLabel(status string) string {
	switch status {
	case "active":
		return "Active"
	case "served":
		return "Served"
	case "appealed":
		return "Appealed"
	case "overturned":
		return "Overturned"
	default:
		return "Unknown"
	}
}

func emailStatusLabel(status string) string {
	switch status {
	case "pending", "":
		return "pending"
	case "approved":
		return "approved"
	case "sent":
		return "sent"
	case "skipped":
		return "skipped"
	default:
		return status
	}
}
