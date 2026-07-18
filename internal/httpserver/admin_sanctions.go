package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"
	sanctiondomain "cricket-ground-feedback/internal/sanctions"

	"github.com/go-chi/chi/v5"
)

// handleAdminSanctions lists all sanctions for the current season with filters.
func (s *Server) handleAdminSanctions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		successMsg := strings.TrimSpace(r.URL.Query().Get("success"))
		errorMsg := strings.TrimSpace(r.URL.Query().Get("error"))

		var seasonID int32
		var seasonName string
		s.DB.QueryRow(ctx, `
			SELECT s.id, s.name FROM weeks w JOIN seasons s ON w.season_id=s.id
			WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date LIMIT 1
		`).Scan(&seasonID, &seasonName)

		if sid := r.URL.Query().Get("season_id"); sid != "" {
			if n, err := strconv.Atoi(sid); err == nil {
				seasonID = int32(n)
				s.DB.QueryRow(ctx, `SELECT name FROM seasons WHERE id=$1`, seasonID).Scan(&seasonName)
			}
		}

		type sanRow struct {
			ID           int64
			Week         int32
			MatchDate    time.Time
			Club         string
			Team         string
			Colour       string
			Reason       string
			Notes        string
			Status       string
			IssuedAt     time.Time
			IssuedBy     string
			EmailStatus  string
			PointsDeduct *int32
		}
		var sanctions []sanRow
		var yellowCount, redCount, activeCount int64

		srows, err := s.DB.Query(ctx, `
			SELECT sa.id, w.week_number, cl.name, t.name,
			       COALESCE(missing_fixture.match_date, first_fixture.match_date, w.start_date) AS match_date,
			       sa.colour, sa.reason, COALESCE(sa.notes,''),
			       sa.status, sa.issued_at,
			       COALESCE(au.username, 'system'),
			       COALESCE(sa.email_status, 'pending'),
			       sa.points_deduction
			FROM sanctions sa
			JOIN weeks w       ON sa.week_id  = w.id
			JOIN clubs cl      ON sa.club_id  = cl.id
			JOIN teams t       ON sa.team_id  = t.id
			LEFT JOIN admin_users au ON sa.issued_by_admin_id = au.id
			LEFT JOIN LATERAL (
			    SELECT lf.match_date
			    FROM (
			        SELECT lf.play_cricket_match_id,
			               lf.match_date,
			               ROW_NUMBER() OVER (
			                   PARTITION BY lf.match_date
			                   ORDER BY lf.play_cricket_match_id
			               ) AS fixture_ordinal
			        FROM league_fixtures lf
			        WHERE (
			            TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
			            OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
			        )
			          AND lf.match_date BETWEEN w.start_date AND w.end_date
			          AND EXTRACT(DOW FROM lf.match_date) <> 5
			          AND NOT lf.is_bye
			    ) lf
			    LEFT JOIN (
			        SELECT sub.match_date, COUNT(*) AS legacy_count
			        FROM submissions sub
			        WHERE sub.team_id = sa.team_id
			          AND sub.week_id = sa.week_id
			          AND sub.play_cricket_match_id IS NULL
			        GROUP BY sub.match_date
			    ) legacy_subs ON legacy_subs.match_date = lf.match_date
			    WHERE NOT EXISTS (
			        SELECT 1 FROM submissions sub
			        WHERE sub.team_id = sa.team_id
			          AND sub.week_id = sa.week_id
			          AND sub.play_cricket_match_id = lf.play_cricket_match_id
			    )
			      AND lf.fixture_ordinal > COALESCE(legacy_subs.legacy_count, 0)
			      AND NOT EXISTS (
			          SELECT 1 FROM report_exemptions re
			          WHERE re.team_id = sa.team_id
			            AND re.week_id = sa.week_id
			            AND re.match_date = lf.match_date
			            AND (
			                re.play_cricket_match_id = lf.play_cricket_match_id
			                OR re.play_cricket_match_id IS NULL
			            )
			      )
			    ORDER BY lf.match_date, lf.play_cricket_match_id
			    LIMIT 1
			) missing_fixture ON TRUE
			LEFT JOIN LATERAL (
			    SELECT MIN(lf.match_date) AS match_date
			    FROM league_fixtures lf
			    WHERE (
			        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
			        OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
			    )
			      AND lf.match_date BETWEEN w.start_date AND w.end_date
			      AND EXTRACT(DOW FROM lf.match_date) <> 5
			      AND NOT lf.is_bye
			) first_fixture ON TRUE
			WHERE sa.season_id = $1
			ORDER BY sa.issued_at DESC
		`, seasonID)
		if err == nil {
			defer srows.Close()
			for srows.Next() {
				var sr sanRow
				if e := srows.Scan(&sr.ID, &sr.Week, &sr.Club, &sr.Team,
					&sr.MatchDate, &sr.Colour, &sr.Reason, &sr.Notes, &sr.Status,
					&sr.IssuedAt, &sr.IssuedBy, &sr.EmailStatus, &sr.PointsDeduct); e == nil {
					sanctions = append(sanctions, sr)
					if sr.Colour == "yellow" {
						yellowCount++
					} else {
						redCount++
					}
					if sr.Status == "active" {
						activeCount++
					}
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}
		sanctionsRedirect := "/admin/sanctions"
		if seasonID > 0 {
			sanctionsRedirect = fmt.Sprintf("/admin/sanctions?season_id=%d", seasonID)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		fmt.Fprint(w, `<div class="container-fluid px-4">`)
		// Bulk deletion is deliberately unavailable. Corrections and reversals
		// must retain an attributed immutable history.
		clearBtn := ""
		fmt.Fprintf(w, `
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h4 class="mb-0 fw-bold">Sanctions</h4>
    <p class="text-muted mb-0 small">%s</p>
  </div>
  <div>
    <a href="/admin/sanctions/weekly-report" class="btn btn-sm btn-primary">Weekly PDF Report</a>
    <a href="/admin/compliance" class="btn btn-sm btn-outline-primary">View Compliance</a>%s
  </div>
</div>
`, escapeHTML(seasonName), clearBtn)
		if successMsg != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(successMsg))
		}
		if errorMsg != "" {
			fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(errorMsg))
		}

		// KPI strip
		fmt.Fprintf(w, `
<div class="row g-3 mb-4">
  <div class="col-auto">
    <div class="card card-kpi kpi-amber p-3 text-center" style="min-width:110px">
      <div class="kpi-number" style="color:#856404">%d</div>
      <div class="kpi-label">Yellow Cards</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi kpi-red p-3 text-center" style="min-width:110px">
      <div class="kpi-number text-danger">%d</div>
      <div class="kpi-label">Red Cards</div>
    </div>
  </div>
  <div class="col-auto">
    <div class="card card-kpi kpi-purple p-3 text-center" style="min-width:110px">
      <div class="kpi-number">%d</div>
      <div class="kpi-label">Active</div>
    </div>
  </div>
</div>
`, yellowCount, redCount, activeCount)

		fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-body">
    <form method="GET" action="/admin/sanctions/export.csv" class="row g-2 align-items-end">
      <input type="hidden" name="season_id" value="%d">
      <div class="col-auto">
        <label class="form-label small text-muted mb-1">From week</label>
        <input class="form-control form-control-sm" type="number" name="week_from" min="1" value="5" style="width:100px" required>
      </div>
      <div class="col-auto">
        <label class="form-label small text-muted mb-1">To week</label>
        <input class="form-control form-control-sm" type="number" name="week_to" min="1" value="10" style="width:100px" required>
      </div>
      <div class="col-auto">
        <button class="btn btn-sm btn-outline-primary" type="submit">Export CSV</button>
      </div>
      <div class="col-auto text-muted small pb-1">Revoked cards excluded</div>
    </form>
  </div>
</div>
`, seasonID)

		// Sanctions table
		fmt.Fprint(w, `
<div class="card shadow-sm mb-4">
  <div class="table-responsive">
    <table class="table table-hover table-gmcl mb-0">
      <thead><tr>
        <th>Week</th><th>Match Date</th><th>Club</th><th>Team</th><th>Card</th>
        <th>Reason</th><th>Notes</th><th>Status</th><th>Email</th><th>By</th><th></th>
      </tr></thead>
      <tbody>
`)
		for _, sr := range sanctions {
			cardBadge := `<span class="badge badge-yellow-card">Yellow Card</span>`
			rowClass := "sanction-row-yellow"
			if sr.Colour == "red" {
				cardBadge = `<span class="badge badge-red-card">Red Card</span>`
				rowClass = "sanction-row-red"
			}

			statusBadge := ""
			switch sr.Status {
			case "active":
				statusBadge = `<span class="badge bg-danger">Active</span>`
			case "served":
				statusBadge = `<span class="badge bg-secondary">Served</span>`
			case "appealed":
				statusBadge = `<span class="badge bg-warning text-dark">Appealed</span>`
			case "overturned":
				statusBadge = `<span class="badge bg-success">Overturned</span>`
			}

			reasonLabel := sr.Reason
			switch sr.Reason {
			case "non_submission":
				reasonLabel = "Non-submission"
			case "late_submission":
				reasonLabel = "Late submission"
			case "manual":
				reasonLabel = "Manual"
			}

			actions := ""
			if sr.Colour == "yellow" && (sr.Status == "active" || sr.Status == "served") {
				actions = revokeYellowCardForm(csrfToken, sr.ID, sanctionsRedirect)
			} else if sr.Status == "active" {
				actions = fmt.Sprintf(`
<form method="POST" action="/admin/sanctions/%d/resolve" class="d-inline">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="text" name="reason" class="form-control form-control-sm d-inline-block" style="width:12rem" placeholder="Overturn reason" required>
  <button type="submit" class="btn btn-sm btn-outline-success py-0"
          >Overturn</button>
</form>`, sr.ID, csrfToken)
			}
			actions += fmt.Sprintf(` <a class="btn btn-sm btn-outline-primary py-0" href="/admin/sanctions/%d/timeline">Details</a>`, sr.ID)

			notesDisplay := sr.Notes
			if len(notesDisplay) > 60 {
				notesDisplay = notesDisplay[:57] + "..."
			}

			emailBadge := ""
			switch sr.EmailStatus {
			case "pending":
				emailBadge = fmt.Sprintf(`<a href="/admin/sanctions/%d/email" class="badge bg-warning text-dark text-decoration-none">Pending</a>`, sr.ID)
			case "approved":
				emailBadge = `<span class="badge bg-info">Approved</span>`
			case "sent":
				emailBadge = `<span class="badge bg-success">Sent</span>`
			case "skipped":
				emailBadge = `<span class="badge bg-secondary">Skipped</span>`
			}

			fmt.Fprintf(w,
				`<tr class="%s"><td>%d</td><td class="text-muted small" title="Processed %s">%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="text-muted small">%s</td><td>%s</td><td>%s</td><td class="text-muted small">%s</td><td>%s</td></tr>`,
				rowClass, sr.Week,
				escapeHTML(sr.IssuedAt.Format("02 Jan 15:04")),
				escapeHTML(sr.MatchDate.Format("02 Jan 2006")),
				escapeHTML(sr.Club), escapeHTML(sr.Team),
				cardBadge, escapeHTML(reasonLabel),
				escapeHTML(notesDisplay), statusBadge, emailBadge,
				escapeHTML(sr.IssuedBy), actions)
		}

		if len(sanctions) == 0 {
			fmt.Fprint(w, `<tr><td colspan="11" class="text-center text-muted py-3">No sanctions issued this season.</td></tr>`)
		}
		fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>`)

		// Recipients management card
		type recipientRow struct {
			ID     int32
			Name   string
			Email  string
			Active bool
		}
		var recipients []recipientRow
		rrows, _ := s.DB.Query(ctx, `SELECT id, name, email, active FROM disciplinary_recipients ORDER BY name`)
		if rrows != nil {
			defer rrows.Close()
			for rrows.Next() {
				var rr recipientRow
				if rrows.Scan(&rr.ID, &rr.Name, &rr.Email, &rr.Active) == nil {
					recipients = append(recipients, rr)
				}
			}
		}

		fmt.Fprintf(w, `
<div class="card shadow-sm mb-4">
  <div class="card-header fw-semibold">Disciplinary Email Recipients</div>
  <div class="card-body">
    <p class="text-muted small mb-3">These people receive a copy of every card letter that is approved and sent.</p>
    <form method="POST" action="/admin/sanctions/recipients/save" class="row g-2 mb-3">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="col-md-4"><input class="form-control form-control-sm" name="name" placeholder="Name" required></div>
      <div class="col-md-5"><input class="form-control form-control-sm" type="email" name="email" placeholder="Email address" required></div>
      <div class="col-auto"><button class="btn btn-sm btn-primary" type="submit">Add recipient</button></div>
    </form>`, csrfToken)

		if len(recipients) > 0 {
			fmt.Fprint(w, `<table class="table table-sm mb-0"><thead><tr><th>Name</th><th>Email</th><th>Active</th><th></th></tr></thead><tbody>`)
			for _, rr := range recipients {
				activeBadge := `<span class="badge bg-success">Active</span>`
				if !rr.Active {
					activeBadge = `<span class="badge bg-secondary">Inactive</span>`
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>
  <form method="POST" action="/admin/sanctions/recipients/%d/delete" class="d-inline">
    <input type="hidden" name="csrf_token" value="%s">
    <button class="btn btn-sm btn-outline-danger py-0" onclick="return confirm('Remove this recipient?')">Remove</button>
  </form></td></tr>`,
					escapeHTML(rr.Name), escapeHTML(rr.Email), activeBadge, rr.ID, csrfToken)
			}
			fmt.Fprint(w, `</tbody></table>`)
		} else {
			fmt.Fprint(w, `<p class="text-muted small">No recipients configured yet.</p>`)
		}
		fmt.Fprint(w, `  </div>
</div>
</div>`)
		pageFooter(w)
	}
}

func revokeYellowCardForm(csrfToken string, sanctionID int64, redirect string) string {
	return fmt.Sprintf(`
<form method="POST" action="/admin/sanctions/%d/revoke-yellow" class="d-inline">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="redirect" value="%s">
  <input type="hidden" name="reason" value="">
  <button type="submit" class="btn btn-sm btn-outline-danger py-0"
          onclick="const reason = prompt('Reason for revoking this yellow card?'); if (!reason || !reason.trim()) return false; this.form.reason.value = reason.trim(); return confirm('Revoke this yellow card?')">Revoke yellow</button>
</form>`, sanctionID, escapeHTML(csrfToken), escapeHTML(redirect))
}

func safeAdminRedirect(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/admin/") {
		return raw
	}
	return fallback
}

func redirectWithMessage(target, key, message string) string {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return target + sep + key + "=" + urlQueryEscape(message)
}

// handleAdminSanctionIssue issues a yellow (or red) card for a team/week.
// Two-yellows = automatic red escalation.
func (s *Server) handleAdminSanctionIssue() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		teamID, _ := strconv.Atoi(r.FormValue("team_id"))
		weekID, _ := strconv.Atoi(r.FormValue("week_id"))
		seasonID, _ := strconv.Atoi(r.FormValue("season_id"))
		reason := r.FormValue("reason")
		notes := r.FormValue("notes")

		if teamID == 0 || weekID == 0 || seasonID == 0 {
			http.Error(w, "missing required fields", http.StatusBadRequest)
			return
		}
		if reason == "" {
			reason = "non_submission"
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Look up club_id
		var clubID int32
		if err := s.DB.QueryRow(ctx, `SELECT club_id FROM teams WHERE id=$1`, teamID).Scan(&clubID); err != nil {
			http.Error(w, "team not found", http.StatusBadRequest)
			return
		}

		// Idempotency: don't propose the same source offence twice for a week.
		var existing int64
		s.DB.QueryRow(ctx, `
			SELECT (SELECT COUNT(*) FROM sanctions WHERE team_id=$1 AND week_id=$2 AND season_id=$3 AND reason=$4 AND status IN ('active','served'))
			     + (SELECT COUNT(*) FROM sanction_cases WHERE team_id=$1 AND week_id=$2 AND season_id=$3 AND source_type=CASE WHEN $4='non_submission' THEN 'captain_report' ELSE 'manual' END AND status NOT IN ('rejected','withdrawn'))
		`, teamID, weekID, seasonID, reason).Scan(&existing)
		if existing > 0 {
			redirect := r.FormValue("redirect")
			if redirect == "" {
				redirect = "/admin/compliance?week_id=" + strconv.Itoa(weekID)
			}
			http.Redirect(w, r, redirect, http.StatusSeeOther)
			return
		}

		sourceType := "manual"
		if reason == "non_submission" {
			sourceType = "captain_report"
		}
		proposed, err := sanctiondomain.NewService(s.DB).ProposeCardCase(ctx, sanctiondomain.CardCaseRequest{
			SourceType: sourceType, SeasonID: int32(seasonID), WeekID: int32(weekID), ClubID: clubID, TeamID: int32(teamID),
			PublicReason: reasonLabel(reason), PrivateReason: notes, CardRequest: sanctiondomain.CardRequest{Kind: "yellow"},
			Actor: adminActor(r), LegacyReason: reason,
		})
		if err != nil {
			http.Error(w, "failed to propose sanction: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", proposed.CaseID), http.StatusSeeOther)
	}
}

// handleAdminSanctionBulkIssue issues cards to all non-submitting teams for a week.
func (s *Server) handleAdminSanctionBulkIssue() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		weekID, _ := strconv.Atoi(r.FormValue("week_id"))
		seasonID, _ := strconv.Atoi(r.FormValue("season_id"))
		if weekID == 0 || seasonID == 0 {
			http.Error(w, "missing fields", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// Find teams with a fixture this week that haven't fully submitted (including partial),
		// and don't already have a non_submission sanction for this week.
		rows, err := s.DB.Query(ctx, `
			SELECT t.id
			FROM teams t
			JOIN weeks w ON w.id = $1
			WHERE t.active = TRUE
			  AND t.play_cricket_team_id IS NOT NULL AND t.play_cricket_team_id <> ''
			  AND EXISTS (
			      SELECT 1 FROM league_fixtures lf
			      WHERE (TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
			             OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id))
			        AND lf.match_date BETWEEN w.start_date AND w.end_date
			        AND EXTRACT(DOW FROM lf.match_date) <> 5
			        AND NOT lf.is_bye
			        AND NOT EXISTS (SELECT 1 FROM submissions WHERE team_id = t.id AND match_date = lf.match_date)
			        AND NOT EXISTS (
			            SELECT 1 FROM report_exemptions re
			            WHERE re.team_id = t.id
			              AND re.match_date = lf.match_date
			              AND (
			                  re.play_cricket_match_id = lf.play_cricket_match_id
			                  OR re.play_cricket_match_id IS NULL
			              )
			        )
			  )
			  AND t.id NOT IN (
			      SELECT team_id FROM sanctions
			      WHERE week_id=$1 AND season_id=$2 AND reason='non_submission'
			      AND status IN ('active','served')
			  )
			  AND t.id NOT IN (
			      SELECT team_id FROM sanction_cases
			      WHERE week_id=$1 AND season_id=$2 AND source_type='captain_report'
			        AND status NOT IN ('rejected','withdrawn')
			  )
		`, weekID, seasonID)
		if err != nil {
			http.Error(w, "query error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var teamIDs []int32
		for rows.Next() {
			var id int32
			if rows.Scan(&id) == nil {
				teamIDs = append(teamIDs, id)
			}
		}
		rows.Close()

		for _, tid := range teamIDs {
			var clubID int32
			if s.DB.QueryRow(ctx, `SELECT club_id FROM teams WHERE id=$1`, tid).Scan(&clubID) != nil {
				continue
			}
			_, _ = sanctiondomain.NewService(s.DB).ProposeCardCase(ctx, sanctiondomain.CardCaseRequest{
				SourceType: "captain_report", SeasonID: int32(seasonID), WeekID: int32(weekID), ClubID: clubID, TeamID: tid,
				PublicReason: "Failure to submit captain's report", PrivateReason: "Bulk compliance proposal",
				CardRequest: sanctiondomain.CardRequest{Kind: "yellow"}, Actor: adminActor(r), LegacyReason: "non_submission",
			})
		}

		http.Redirect(w, r, "/admin/cases", http.StatusSeeOther)
	}
}

// handleAdminSanctionResolve marks a sanction as resolved/overturned.
func (s *Server) handleAdminSanctionResolve() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		sanctionID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || sanctionID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err = r.ParseForm(); err != nil || strings.TrimSpace(r.FormValue("reason")) == "" {
			http.Error(w, "overturn reason is required", http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)
		var beforeData []byte
		if err = tx.QueryRow(ctx, `SELECT to_jsonb(sa) FROM sanctions sa WHERE id=$1 AND status='active' FOR UPDATE`, sanctionID).Scan(&beforeData); err != nil {
			http.Error(w, "active sanction not found", http.StatusNotFound)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		_, err = tx.Exec(ctx, `
			UPDATE sanctions SET status='overturned', resolved_at=now(), resolved_by_admin_id=$1
			WHERE id=$2 AND status='active'
		`, *actor.ID, sanctionID)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id,actor_label,reason,before_data,after_data,request_id) SELECT id,'overturned_with_reason',now(),$2,$3,$4,$2,$5::jsonb,to_jsonb(sa),$6 FROM sanctions sa WHERE id=$1`, sanctionID, reason, *actor.ID, actor.Label, string(beforeData), actor.RequestID)
		if err != nil || tx.Commit(ctx) != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "admin", actor.ID, "sanction_resolved", "sanction", &sanctionID, map[string]any{"reason": reason})

		http.Redirect(w, r, "/admin/sanctions", http.StatusSeeOther)
	}
}

// handleAdminSanctionRevokeYellow overturns a yellow card for a team and keeps
// the record for audit/history. Overturned yellows are excluded from future
// offence counts by the compliance and sanctioning queries.
func (s *Server) handleAdminSanctionRevokeYellow() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		sanctionID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || sanctionID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" {
			http.Error(w, "reason is required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)

		type sanctionState struct {
			TeamID      int32
			WeekID      int32
			SeasonID    int32
			Status      string
			EmailStatus string
		}
		var before sanctionState
		err = tx.QueryRow(ctx, `
			SELECT team_id, week_id, season_id, status::text, COALESCE(email_status, 'pending')
			FROM sanctions
			WHERE id=$1 AND colour='yellow' AND status IN ('active','served')
		`, sanctionID).Scan(&before.TeamID, &before.WeekID, &before.SeasonID, &before.Status, &before.EmailStatus)
		if err != nil {
			redirect := safeAdminRedirect(r.FormValue("redirect"), "/admin/sanctions")
			http.Redirect(w, r, redirectWithMessage(redirect, "error", "Yellow card is not revocable."), http.StatusSeeOther)
			return
		}

		beforeData, _ := json.Marshal(before)
		tag, err := tx.Exec(ctx, `
			UPDATE sanctions
			SET status='overturned',
			    resolved_at=now(),
			    resolved_by_admin_id=$1,
			    notes = CASE
			        WHEN COALESCE(NULLIF(BTRIM(notes), ''), '') = '' THEN 'Revoked: ' || $3
			        ELSE notes || E'\nRevoked: ' || $3
			    END,
			    email_status = CASE
			        WHEN COALESCE(email_status, 'pending') IN ('pending', 'approved') THEN 'skipped'
			        ELSE email_status
			    END
			WHERE id=$2 AND colour='yellow' AND status IN ('active','served')
		`, *actor.ID, sanctionID, reason)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			redirect := safeAdminRedirect(r.FormValue("redirect"), "/admin/sanctions")
			http.Redirect(w, r, redirectWithMessage(redirect, "error", "Yellow card is not revocable."), http.StatusSeeOther)
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id,actor_label,reason,before_data,after_data,request_id) SELECT id,'yellow_revoked',now(),$2,$3,$4,$2,$5::jsonb,to_jsonb(sa),$6 FROM sanctions sa WHERE id=$1`, sanctionID, reason, *actor.ID, actor.Label, string(beforeData), actor.RequestID)
		if err != nil || tx.Commit(ctx) != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "admin", actor.ID, "yellow_card_revoked", "sanction", &sanctionID, map[string]any{
			"team_id":               before.TeamID,
			"week_id":               before.WeekID,
			"season_id":             before.SeasonID,
			"reason":                reason,
			"previous_status":       before.Status,
			"previous_email_status": before.EmailStatus,
		})

		redirect := safeAdminRedirect(r.FormValue("redirect"), "/admin/sanctions")
		http.Redirect(w, r, redirectWithMessage(redirect, "success", "Yellow card revoked."), http.StatusSeeOther)
	}
}

// handleAdminSanctionEmailPage shows the email draft for a sanction with approve/skip/edit options.
func (s *Server) handleAdminSanctionEmailPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sanctionID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || sanctionID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		type sanctionDetail struct {
			ID           int64
			Club         string
			Team         string
			Colour       string
			MatchWeek    int32
			EmailSubject string
			EmailBody    string
			EmailStatus  string
			PointsDeduct *int32
		}
		var sd sanctionDetail
		err = s.DB.QueryRow(ctx, `
			SELECT sa.id, cl.name, t.name, sa.colour, w.week_number,
			       COALESCE(sa.email_subject,''), COALESCE(sa.email_body,''),
			       COALESCE(sa.email_status,'pending'), sa.points_deduction
			FROM sanctions sa
			JOIN clubs cl ON cl.id = sa.club_id
			JOIN teams t  ON t.id  = sa.team_id
			JOIN weeks w  ON w.id  = sa.week_id
			WHERE sa.id = $1
		`, sanctionID).Scan(&sd.ID, &sd.Club, &sd.Team, &sd.Colour, &sd.MatchWeek,
			&sd.EmailSubject, &sd.EmailBody, &sd.EmailStatus, &sd.PointsDeduct)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		csrfToken := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanction Email")
		writeAdminNav(w, csrfToken, r.URL.Path, adminRoleForRequest(r))

		cardLabel := "Yellow Card"
		if sd.Colour == "red" {
			cardLabel = "Red Card"
		}
		pointsVal := ""
		if sd.PointsDeduct != nil {
			pointsVal = strconv.Itoa(int(*sd.PointsDeduct))
		}

		fmt.Fprintf(w, `<div class="container" style="max-width:800px">
<div class="d-flex align-items-center justify-content-between mb-4">
  <div><h4 class="mb-0 fw-bold">%s Email</h4>
  <p class="text-muted mb-0 small">%s &mdash; %s &mdash; Week %d</p></div>
  <a href="/admin/sanctions" class="btn btn-sm btn-outline-secondary">Back</a>
</div>
<form method="POST" action="/admin/sanctions/%d/email/approve">
  <input type="hidden" name="csrf_token" value="%s">
  <div class="card shadow-sm mb-3">
    <div class="card-header fw-semibold">Email draft</div>
    <div class="card-body">
      <div class="mb-3">
        <label class="form-label fw-semibold">Subject</label>
        <input class="form-control" name="email_subject" value="%s" required>
      </div>`,
			escapeHTML(cardLabel), escapeHTML(sd.Club), escapeHTML(sd.Team), sd.MatchWeek,
			sd.ID, csrfToken, escapeHTML(sd.EmailSubject))

		if sd.Colour == "red" {
			fmt.Fprintf(w, `
      <div class="mb-3">
        <label class="form-label fw-semibold">Points deduction <span class="text-danger">*</span></label>
        <input class="form-control" type="number" name="points_deduction" min="0" value="%s" required placeholder="e.g. 5">
        <div class="form-text">Required for red cards. This will be inserted into the letter.</div>
      </div>`, escapeHTML(pointsVal))
		}

		fmt.Fprintf(w, `
      <div class="mb-3">
        <label class="form-label fw-semibold">Email body</label>
        <textarea class="form-control font-monospace" name="email_body" rows="18" style="font-size:0.85rem">%s</textarea>
      </div>
    </div>
    <div class="card-footer d-flex gap-2">
      <button class="btn btn-success" type="submit">Approve &amp; Send</button>
    </div>
  </div>
</form>
<form method="POST" action="/admin/sanctions/%d/email/skip">
  <input type="hidden" name="csrf_token" value="%s">
  <button class="btn btn-outline-secondary btn-sm" type="submit"
          onclick="return confirm('Skip sending the email for this sanction?')">Skip email</button>
</form>
</div>`, escapeHTML(sd.EmailBody), sd.ID, csrfToken)

		pageFooter(w)
	}
}

// handleAdminSanctionEmailApprove saves edits, sets email_status=approved, and sends to all recipients.
func (s *Server) handleAdminSanctionEmailApprove() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		sanctionID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || sanctionID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		subject := strings.TrimSpace(r.FormValue("email_subject"))
		body := strings.TrimSpace(r.FormValue("email_body"))
		pointsRaw := strings.TrimSpace(r.FormValue("points_deduction"))

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)
		var beforeData []byte
		if tx.QueryRow(ctx, `SELECT to_jsonb(sa) FROM sanctions sa WHERE id=$1 FOR UPDATE`, sanctionID).Scan(&beforeData) != nil {
			http.NotFound(w, r)
			return
		}

		// Persist edits and mark approved
		var pointsArg interface{}
		if p, err := strconv.Atoi(pointsRaw); err == nil && p >= 0 {
			pointsArg = p
		}
		sess, _ := getAdminSessionFromRequest(r)
		approvedBy := "admin"
		if sess != nil {
			approvedBy = strconv.Itoa(int(sess.AdminID))
		}

		_, err = tx.Exec(ctx, `
			UPDATE sanctions
			SET email_subject=$1, email_body=$2, points_deduction=$3,
			    email_status='approved', email_approved_by=$4, email_approved_at=now()
			WHERE id=$5
		`, subject, body, pointsArg, approvedBy, sanctionID)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id,actor_label,reason,before_data,after_data,request_id) SELECT id,'email_approved',now(),'Legacy sanction notice approved',$2,$3,'Notice approved for sending',$4::jsonb,to_jsonb(sa),$5 FROM sanctions sa WHERE id=$1`, sanctionID, *actor.ID, actor.Label, string(beforeData), actor.RequestID)
		if err != nil || tx.Commit(ctx) != nil {
			http.Error(w, "could not record immutable email approval", http.StatusInternalServerError)
			return
		}

		// Fetch active recipients
		rrows, err := s.DB.Query(ctx, `SELECT email FROM disciplinary_recipients WHERE active=TRUE`)
		if err != nil {
			http.Redirect(w, r, "/admin/sanctions?error=approved+but+could+not+fetch+recipients", http.StatusSeeOther)
			return
		}
		defer rrows.Close()
		var recipients []string
		for rrows.Next() {
			var e string
			if rrows.Scan(&e) == nil {
				recipients = append(recipients, e)
			}
		}

		mailer := email.NewFromEnv()
		sentCount := 0
		for _, to := range recipients {
			if mailer.Send(to, subject, body) == nil {
				sentCount++
			}
		}

		if sentCount > 0 || len(recipients) == 0 {
			sentTx, sentErr := s.DB.Begin(ctx)
			if sentErr != nil {
				http.Error(w, "notice sent but delivery status could not be recorded", http.StatusInternalServerError)
				return
			}
			if sentErr == nil {
				defer sentTx.Rollback(ctx)
				if _, sentErr = sentTx.Exec(ctx, `UPDATE sanctions SET email_status='sent', email_sent_at=now() WHERE id=$1`, sanctionID); sentErr == nil {
					_, sentErr = sentTx.Exec(ctx, `INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id,actor_label,reason,after_data,request_id) SELECT id,'email_sent',now(),$2,$3,$4,'Approved notice sent',jsonb_build_object('recipient_count',$5,'sent_count',$6,'email_status',email_status,'email_sent_at',email_sent_at),$7 FROM sanctions WHERE id=$1`, sanctionID, fmt.Sprintf("Sent %d of %d configured notices", sentCount, len(recipients)), *actor.ID, actor.Label, len(recipients), sentCount, actor.RequestID)
				}
				if sentErr == nil {
					sentErr = sentTx.Commit(ctx)
				}
				if sentErr != nil {
					http.Error(w, "notice sent but delivery status could not be recorded", http.StatusInternalServerError)
					return
				}
			}
		}

		s.audit(ctx, r, "admin", actor.ID, "sanction_email_sent", "sanction", &sanctionID, map[string]any{
			"recipients": len(recipients),
			"sent":       sentCount,
		})

		http.Redirect(w, r, "/admin/sanctions", http.StatusSeeOther)
	}
}

// handleAdminSanctionEmailSkip marks a sanction email as skipped (no letter sent).
func (s *Server) handleAdminSanctionEmailSkip() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sanctionID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || sanctionID == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)
		var beforeData []byte
		if tx.QueryRow(ctx, `SELECT to_jsonb(sa) FROM sanctions sa WHERE id=$1 FOR UPDATE`, sanctionID).Scan(&beforeData) != nil {
			http.NotFound(w, r)
			return
		}
		if _, err = tx.Exec(ctx, `UPDATE sanctions SET email_status='skipped' WHERE id=$1`, sanctionID); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO sanction_events(sanction_id,event_type,event_at,notes,created_by_admin_id,actor_label,reason,before_data,after_data,request_id) SELECT id,'email_skipped',now(),'Legacy sanction notice skipped',$2,$3,'Notice deliberately skipped',$4::jsonb,to_jsonb(sa),$5 FROM sanctions sa WHERE id=$1`, sanctionID, *actor.ID, actor.Label, string(beforeData), actor.RequestID)
		}
		if err != nil || tx.Commit(ctx) != nil {
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/sanctions", http.StatusSeeOther)
	}
}

// handleAdminSanctionClearAll is retained only so old bookmarks fail safely.
// Approved records must be corrected or overturned with immutable events.
func (s *Server) handleAdminSanctionClearAll() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bulk deletion is disabled; record an immutable correction or reversal", http.StatusGone)
	}
}

// handleAdminSanctionRecipientSave adds a new disciplinary email recipient.
func (s *Server) handleAdminSanctionRecipientSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		emailAddr := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		if name == "" || emailAddr == "" {
			http.Redirect(w, r, "/admin/sanctions", http.StatusSeeOther)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		s.DB.Exec(ctx, `
			INSERT INTO disciplinary_recipients (name, email)
			VALUES ($1, $2)
			ON CONFLICT (email) DO UPDATE SET name=$1, active=TRUE
		`, name, emailAddr)
		s.DB.Exec(ctx, `INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES('executive',$1,$2) ON CONFLICT(recipient_role,email) DO UPDATE SET name=EXCLUDED.name,active=TRUE`, name, emailAddr)
		http.Redirect(w, r, "/admin/sanctions", http.StatusSeeOther)
	}
}

// handleAdminSanctionRecipientDelete removes a disciplinary email recipient.
func (s *Server) handleAdminSanctionRecipientDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 32)
		if id == 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var emailAddr string
		_ = s.DB.QueryRow(ctx, `SELECT email FROM disciplinary_recipients WHERE id=$1`, int32(id)).Scan(&emailAddr)
		s.DB.Exec(ctx, `UPDATE disciplinary_recipients SET active=FALSE WHERE id=$1`, int32(id))
		if emailAddr != "" {
			s.DB.Exec(ctx, `UPDATE sanction_recipient_directory SET active=FALSE WHERE recipient_role='executive' AND email=$1`, emailAddr)
		}
		http.Redirect(w, r, "/admin/sanctions", http.StatusSeeOther)
	}
}

// resolveAdminID extracts the admin user ID from the signed session cookie.
func (s *Server) resolveAdminID(r *http.Request) *int32 {
	sess, err := getAdminSessionFromRequest(r)
	if err != nil {
		return nil
	}
	return &sess.AdminID
}
