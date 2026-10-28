package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"fmt"
	"net/http"
	"strconv"
)

func juniorRenderTeamPlan(w http.ResponseWriter, items []junior.TeamEntryChoice) {
	fmt.Fprint(w, `<div class="table-responsive"><table class="table"><thead><tr><th>Team</th><th>Age</th><th>League / division</th><th>Action</th></tr></thead><tbody>`)
	for _, item := range items {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, escapeHTML(item.Name), escapeHTML(item.AgeGroup), escapeHTML(item.Competition), escapeHTML(item.Effect))
	}
	fmt.Fprint(w, `</tbody></table></div>`)
}

func (s *Server) handleJuniorTeamEntry() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		if p.AdminID < 1 || p.ReadOnly {
			juniorHTTPError(w, r, junior.ErrForbidden)
			return
		}
		svc := junior.NewService(s.DB)
		id := juniorFormPathID(r, "assignmentID")
		a, err := svc.FormAssignment(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		entries, err := junior.AssignmentTeamEntries(a)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		choices := make([]int64, len(entries))
		var plan junior.TeamEntryPlan
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
			if err = r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			for i := range entries {
				choices[i], err = strconv.ParseInt(r.FormValue(fmt.Sprintf("competition_%d", i)), 10, 64)
				if err != nil || choices[i] < 1 {
					juniorHTTPError(w, r, fmt.Errorf("%w: choose a league for every team", junior.ErrValidation))
					return
				}
			}
			sub := juniorFixtureFormID(r, "submission_id")
			switch r.FormValue("action") {
			case "approve":
				err = svc.ApproveTeamEntry(r.Context(), p, id, sub, choices, r.FormValue("preview_hash"))
				if err != nil {
					juniorHTTPError(w, r, err)
					return
				}
				http.Redirect(w, r, juniorFormLink(p, id)+"?success=Team+entries+approved+and+allocated.+Receipt+saved.", http.StatusSeeOther)
				return
			case "preview":
				plan, err = svc.PreviewTeamEntry(r.Context(), p, id, sub, choices)
			default:
				err = junior.ErrValidation
			}
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
		}
		if a.Status != "submitted" {
			http.Redirect(w, r, juniorFormLink(p, id), http.StatusSeeOther)
			return
		}
		competitions, err := svc.ListCompetitions(r.Context(), p, a.SeasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Review team entries and choose leagues")
		defer juniorPageEnd(w)
		fmt.Fprintf(w, `<p>%s · %s · submission receipt %d</p><p><a href="%s">Back to response / return for changes</a></p><p>Choose the league and division for every submitted team. Existing teams are reused. Approval does not regenerate or publish fixtures, remove teams, or move an existing entry to another league.</p>`, escapeHTML(a.Club), escapeHTML(a.Title), a.SubmissionID, escapeHTML(juniorFormLink(p, id)))
		if plan.Hash != "" {
			fmt.Fprint(w, `<h2 class="h4">Approval preview</h2>`)
			juniorRenderTeamPlan(w, plan.Items)
			fmt.Fprint(w, `<form method="POST">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprintf(w, `<input type="hidden" name="submission_id" value="%d"><input type="hidden" name="preview_hash" value="%s">`, a.SubmissionID, plan.Hash)
			for i, c := range choices {
				fmt.Fprintf(w, `<input type="hidden" name="competition_%d" value="%d">`, i, c)
			}
			fmt.Fprint(w, `<p>Confirm this reviewed allocation. If teams, participation or competition settings change, preview again before approving.</p><button class="btn btn-success" name="action" value="approve">Approve teams and allocate leagues</button></form><hr>`)
		}
		fmt.Fprint(w, `<h2 class="h4">League choices</h2><form method="POST">`)
		juniorFixtureCSRF(w, r)
		fmt.Fprintf(w, `<input type="hidden" name="submission_id" value="%d">`, a.SubmissionID)
		for i, entry := range entries {
			fmt.Fprintf(w, `<label class="d-block mb-3">%s · %s<select class="form-select" name="competition_%d" required><option value="">Choose league / division</option>`, escapeHTML(entry.Name), escapeHTML(entry.AgeGroup), i)
			for _, c := range competitions {
				if c.AgeGroup != entry.AgeGroup || c.SeasonState != "open" {
					continue
				}
				sel := ""
				if choices[i] == c.ID {
					sel = " selected"
				}
				fmt.Fprintf(w, `<option value="%d"%s>%s · %s · %s</option>`, c.ID, sel, escapeHTML(c.Name), escapeHTML(c.AgeGroup), escapeHTML(c.Division))
			}
			fmt.Fprint(w, `</select></label>`)
		}
		fmt.Fprint(w, `<p>If no appropriate league is listed, create it under Competitions and fixtures in this season, then return here.</p><button class="btn btn-primary" name="action" value="preview">Preview team allocation</button></form>`)
	}
}
