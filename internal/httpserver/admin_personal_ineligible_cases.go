package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

const personalIneligibleCasesFromSQL = `
	FROM sanction_cases cases
	LEFT JOIN clubs club ON club.id=cases.club_id
	LEFT JOIN teams team ON team.id=cases.team_id
	WHERE cases.assigned_admin_id=$1
	  AND cases.source_type='ineligible_player'
	  AND NOT cases.is_test
	  AND NOT EXISTS(
		SELECT 1 FROM sanction_case_events event
		WHERE event.case_id=cases.id AND event.event_type='case_training_designated'
	  )
	  AND cases.status NOT IN ('published','closed','rejected','withdrawn')`

func personalIneligibleCasesCountQuery() string {
	return `SELECT COUNT(*) ` + personalIneligibleCasesFromSQL
}

func personalIneligibleCasesListQuery() string {
	return `SELECT cases.id,cases.reference,cases.status,COALESCE(cases.player_name,''),
		COALESCE(club.name,''),COALESCE(team.name,''),cases.updated_at ` +
		personalIneligibleCasesFromSQL +
		` ORDER BY cases.updated_at DESC,cases.id DESC`
}

func (s *Server) handleAdminPersonalIneligibleCases() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		rows, err := s.DB.Query(ctx, personalIneligibleCasesListQuery(), *actor.ID)
		if err != nil {
			slog.Error("load personal ineligible-player cases", "admin_id", *actor.ID, "error", err)
			http.Error(w, "could not load assigned cases", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type rowView struct {
			ID        int64
			Reference string
			Status    string
			Player    string
			Club      string
			Team      string
			UpdatedAt time.Time
		}
		cases := []rowView{}
		for rows.Next() {
			var item rowView
			if err := rows.Scan(&item.ID, &item.Reference, &item.Status, &item.Player, &item.Club, &item.Team, &item.UpdatedAt); err != nil {
				http.Error(w, "could not read assigned cases", http.StatusInternalServerError)
				return
			}
			cases = append(cases, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "could not read assigned cases", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "My ineligible-player cases")
		writeAdminNav(w, middleware.CSRFToken(r), r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container py-4"><div class="d-flex flex-column flex-md-row justify-content-between gap-3 mb-4"><div><h1 class="h2 mb-1">My ineligible-player cases</h1><p class="text-muted mb-0">Active cases currently assigned to you. Test and completed cases are excluded.</p></div><a class="btn btn-outline-primary align-self-md-start" href="/admin/ineligible?scope=all&amp;state=open&amp;worklist=visible">Open report triage</a></div><section class="card shadow-sm"><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0"><thead><tr><th>Case</th><th>Player</th><th>Club / team</th><th>Status</th><th>Updated</th></tr></thead><tbody>`)
		for _, item := range cases {
			fmt.Fprintf(w, `<tr><td data-label="Case"><a href="/admin/cases/%d"><strong>%s</strong></a></td><td data-label="Player">%s</td><td data-label="Club / team">%s<div class="small text-muted">%s</div></td><td data-label="Status">%s</td><td data-label="Updated">%s</td></tr>`, item.ID, escapeHTML(item.Reference), escapeHTML(item.Player), escapeHTML(item.Club), escapeHTML(item.Team), escapeHTML(caseStatusLabel(item.Status)), item.UpdatedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04"))
		}
		if len(cases) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-center text-muted py-5">No active ineligible-player cases are assigned to you.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}
