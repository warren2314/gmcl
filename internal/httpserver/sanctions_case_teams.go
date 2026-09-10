package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sanctiondomain "cricket-ground-feedback/internal/sanctions"
	"github.com/go-chi/chi/v5"
)

func (s *Server) adminCaseTeamFormHTML(ctx context.Context, caseID int64, csrf string) string {
	rows, err := s.DB.Query(ctx, `SELECT t.id,c.name,t.name FROM sanction_cases sc JOIN teams t ON t.club_id=sc.club_id JOIN clubs c ON c.id=t.club_id WHERE sc.id=$1 AND t.active AND t.id IS DISTINCT FROM sc.team_id AND NOT EXISTS(SELECT 1 FROM sanction_case_subjects subject WHERE subject.case_id=sc.id AND subject.subject_type='team' AND subject.team_id=t.id) ORDER BY t.name,t.id`, caseID)
	if err != nil {
		return `<div class="alert alert-warning">Could not load additional teams. Refresh the case before preparing the decision.</div>`
	}
	defer rows.Close()
	var options strings.Builder
	for rows.Next() {
		var id int32
		var club, team string
		if rows.Scan(&id, &club, &team) != nil {
			return ""
		}
		fmt.Fprintf(&options, `<option value="%d">%s — %s</option>`, id, escapeHTML(club), escapeHTML(team))
	}
	if rows.Err() != nil || options.Len() == 0 {
		return ""
	}
	return fmt.Sprintf(`<section class="card mb-4" id="case-teams"><div class="card-header">Add another affected team</div><form method="POST" action="/admin/cases/%d/teams"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><p>Add a team from the same offending club so its own deduction can be recorded in this decision. The original team and report stay attached. Add the team before filling in the decision below.</p><label class="form-label" for="additional-case-team">Additional team</label><select class="form-select mb-3" id="additional-case-team" name="team_id" required><option value="">Choose team…</option>%s</select><label class="form-label" for="case-team-reason">Why is this team part of the decision?</label><textarea class="form-control" id="case-team-reason" name="reason" required maxlength="2000" rows="2"></textarea></div><div class="card-footer"><button class="btn btn-outline-primary">Add team to case</button><span class="small text-muted ms-2">Records an audit entry. No sanction or email is sent.</span></div></form></section>`, caseID, escapeHTML(csrf), options.String())
}

func (s *Server) handleAdminCaseTeamAdd() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID < 1 || r.ParseForm() != nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		teamID, err := strconv.ParseInt(r.FormValue("team_id"), 10, 32)
		if err != nil || teamID < 1 {
			http.Error(w, "select a valid team", http.StatusBadRequest)
			return
		}
		err = sanctiondomain.NewService(s.DB).AddCaseTeam(r.Context(), caseID, int32(teamID), r.FormValue("reason"), adminActor(r))
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?error=%s#case-teams", caseID, urlQueryEscape(err.Error())), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", caseID, urlQueryEscape("Team attached. Select it under Decision effects to record its separate deduction.")), http.StatusSeeOther)
	}
}
