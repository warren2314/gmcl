package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

func juniorIDSelected[T ~int32 | ~int64](ids []T, id T) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func directoryCanEmail(c junior.DirectoryContact) bool {
	_, err := junior.CanonicalEmail(c.Email)
	return err == nil && c.Role != "" && c.Participation == "active" && !c.Inactive
}

func writeJuniorAudienceSelection(w io.Writer, f junior.AudienceFilter, clubs []junior.ClubSummary, contacts []junior.DirectoryContact) {
	fmt.Fprint(w, `<fieldset class="border rounded p-3 mt-3"><legend class="h6">Choose recipients</legend><p class="small text-muted">Group filters above still apply. A contact selection never expands into an all-club message. Inactive junior clubs are excluded from new audiences.</p>`)
	for _, mode := range []struct{ value, label string }{{"group", "Everyone matching the group filters"}, {"contacts", "Only the contacts ticked below"}} {
		checked := ""
		if f.Selection == mode.value || (f.Selection == "" && mode.value == "group") {
			checked = " checked"
		}
		fmt.Fprintf(w, `<label class="form-check"><input class="form-check-input" type="radio" name="audience_mode" value="%s"%s> %s</label>`, mode.value, checked, mode.label)
	}
	fmt.Fprint(w, `<details class="mt-3"><summary>Limit to selected clubs (optional)</summary><p class="small text-muted">Leave all unticked to use the club filter above.</p><div class="row g-2">`)
	for _, c := range clubs {
		if c.Inactive {
			continue
		}
		checked := ""
		if juniorIDSelected(f.ClubIDs, c.ID) {
			checked = " checked"
		}
		fmt.Fprintf(w, `<label class="col-md-6"><input class="form-check-input me-2" type="checkbox" name="club_ids" value="%d"%s> %s</label>`, c.ID, checked, escapeHTML(c.Name))
	}
	fmt.Fprint(w, `</div></details><details class="mt-3"`)
	if f.Selection == "contacts" {
		fmt.Fprint(w, ` open`)
	}
	fmt.Fprint(w, `><summary>Select individual contacts</summary><p class="small text-muted">Current, participating-club contacts with a valid email are available here. Use the Contact directory to inspect missing details and roles.</p><div class="row g-2">`)
	seen := map[int64]bool{}
	for _, c := range contacts {
		if seen[c.ID] || !directoryCanEmail(c) {
			continue
		}
		seen[c.ID] = true
		checked := ""
		if juniorIDSelected(f.ContactIDs, c.ID) {
			checked = " checked"
		}
		fmt.Fprintf(w, `<label class="col-md-6"><input class="form-check-input me-2" type="checkbox" name="contact_ids" value="%d"%s> %s · %s &lt;%s&gt;</label>`, c.ID, checked, escapeHTML(c.Club), escapeHTML(c.Name), escapeHTML(c.Email))
	}
	if len(seen) == 0 {
		fmt.Fprint(w, `<p class="text-muted">No eligible contacts match these filters.</p>`)
	}
	fmt.Fprint(w, `</div></details><p class="small mt-3 mb-0">After changing any recipient choice, preview again before queuing.</p></fieldset>`)
}

func (s *Server) handleJuniorContactDirectory() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := juniorMessageFilter(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasons, seasonID, err := s.juniorRecordSeason(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		f.SeasonID = seasonID
		svc := junior.NewService(s.DB)
		p := s.juniorPrincipal(r)
		contacts, err := svc.ContactDirectory(r.Context(), p, f, r.URL.Query().Get("q"))
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if r.URL.Query().Get("export") == "csv" {
			rows := [][]string{{"Club", "Name", "Email", "Phone", "Role", "Age group", "Team", "Season participation", "Junior club status"}}
			for _, c := range contacts {
				status := "Active record"
				if c.Inactive {
					status = "Inactive"
				}
				rows = append(rows, []string{c.Club, c.Name, c.Email, c.Phone, junior.RoleLabel(c.Role), c.AgeGroup, c.Team, junior.ParticipationLabel(c.Participation), status})
			}
			juniorFixtureCSV(w, "junior-contact-directory.csv", rows)
			return
		}
		clubs, err := svc.ListClubs(r.Context(), p, seasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		competitions, err := svc.ListCompetitions(r.Context(), p, seasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Junior contact directory")
		fmt.Fprint(w, `<p class="text-muted">Find adult contacts across the league. Current roles are shown for the selected season; missing email addresses and inactive clubs remain visible.</p>`)
		writeJuniorRecordSeasonSelector(w, r, seasons, seasonID)
		fmt.Fprintf(w, `<form method="GET" class="card card-body mb-4"><input type="hidden" name="season_id" value="%d"><div class="row g-3"><label class="col-md-4">Search contacts<input class="form-control" name="q" maxlength="100" value="%s" placeholder="Name, club, email or phone"></label><label class="col-md-4">Club<select class="form-select" name="club_id"><option value="0">All clubs</option>`, seasonID, escapeHTML(r.URL.Query().Get("q")))
		for _, c := range clubs {
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, c.ID, juniorMessageSelected(c.ID == f.ClubID), escapeHTML(c.Name))
		}
		fmt.Fprint(w, `</select></label><label class="col-md-4">Role<select class="form-select" name="role"><option value="">All roles</option>`)
		for _, role := range []string{"junior_lead", "safeguarding_officer", "coach"} {
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, role, juniorMessageSelected(f.Role == role), junior.RoleLabel(role))
		}
		fmt.Fprint(w, `</select></label><label class="col-md-4">Age group<select class="form-select" name="age_group"><option value="">All ages</option>`)
		for _, age := range []string{"U9", "U11", "U13", "U15", "U18"} {
			fmt.Fprintf(w, `<option%s>%s</option>`, juniorMessageSelected(f.AgeGroup == age), age)
		}
		fmt.Fprint(w, `</select></label><label class="col-md-4">Competition<select class="form-select" name="competition_id"><option value="0">All competitions</option>`)
		for _, c := range competitions {
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, c.ID, juniorMessageSelected(c.ID == f.CompetitionID), escapeHTML(c.Name))
		}
		fmt.Fprint(w, `</select></label><div class="col-md-4 align-self-end d-flex gap-2"><button class="btn btn-primary">Apply filters</button><button class="btn btn-outline-secondary" name="export" value="csv">Export CSV</button></div></div></form>`)
		people := map[int64]bool{}
		for _, c := range contacts {
			people[c.ID] = true
		}
		fmt.Fprintf(w, `<p><strong>%d contacts</strong> · %d current role rows. Tick contacts to prepare an individual or group message.</p><form method="GET" action="/admin/juniors/messages"><input type="hidden" name="season_id" value="%d"><input type="hidden" name="audience_mode" value="contacts"><div class="table-responsive"><table class="table responsive-cards align-middle"><thead><tr><th>Select</th><th>Club</th><th>Contact</th><th>Role / team</th><th>Status</th></tr></thead><tbody>`, len(people), len(contacts), seasonID)
		seen := map[int64]bool{}
		for _, c := range contacts {
			fmt.Fprint(w, `<tr><td data-label="Select">`)
			if !seen[c.ID] && directoryCanEmail(c) {
				fmt.Fprintf(w, `<input class="form-check-input" type="checkbox" name="contact_ids" value="%d" aria-label="Select %s at %s">`, c.ID, escapeHTML(c.Name), escapeHTML(c.Club))
			}
			seen[c.ID] = true
			status := junior.ParticipationLabel(c.Participation)
			if c.Inactive {
				status = "Inactive junior club"
			}
			fmt.Fprintf(w, `</td><td data-label="Club"><a href="/admin/juniors/clubs/%d?season_id=%d#contact-%d">%s</a></td><td data-label="Contact"><strong>%s</strong><div>%s</div><div>%s</div></td><td data-label="Role / team">%s %s<div>%s</div></td><td data-label="Status">%s</td></tr>`, c.ClubID, seasonID, c.ID, escapeHTML(c.Club), escapeHTML(c.Name), escapeHTML(c.Email), escapeHTML(c.Phone), escapeHTML(junior.RoleLabel(c.Role)), escapeHTML(c.AgeGroup), escapeHTML(c.Team), status)
		}
		if len(contacts) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5">No contacts match these filters. Add contacts on a club profile or import a reviewed roster.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div><button class="btn btn-primary">Prepare message to selected contacts</button><p class="small text-muted mt-2">This opens the composer. It does not queue or send email. Contacts without a current role, valid email or active participation cannot be selected.</p></form>`)
		juniorPageEnd(w)
	}
}

func (s *Server) handleJuniorClubStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.juniorRecordClubID(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		inactive, err := strconv.ParseBool(r.FormValue("inactive"))
		if err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		seasonID, err := juniorRecordID(r.FormValue("season_id"), false)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if err = junior.NewService(s.DB).SetClubInactive(r.Context(), s.juniorPrincipal(r), id, inactive, r.FormValue("reason")); err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		juniorRecordsRedirect(w, r, s.juniorPrincipal(r), id, seasonID, "Junior club status saved; records and participation history retained")
	}
}

func writeJuniorClubStatus(w io.Writer, r *http.Request, profile junior.ClubProfile) {
	status := "Available for junior participation"
	if profile.Club.Inactive {
		status = "Inactive junior club"
	}
	fmt.Fprintf(w, `<section class="card mb-4"><div class="card-body"><h2 class="h5">Junior club status</h2><p><strong>%s</strong></p><p class="small text-muted">This status persists across junior seasons. Inactive clubs remain in history and are excluded from new message audiences. Existing participation, official records, lead access and senior reporting are retained.</p><form method="POST" action="/admin/juniors/clubs/%d/status" class="row g-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d"><label class="col-md-4">Junior status<select class="form-select" name="inactive"><option value="false"%s>Active record</option><option value="true"%s>Inactive</option></select></label><label class="col-md-6">Reason<input class="form-control" name="reason" maxlength="300" required></label><div class="col-md-2 align-self-end"><button class="btn btn-outline-primary">Save status</button></div></form></div></section>`, status, profile.Club.ID, escapeHTML(middleware.CSRFToken(r)), profile.Season.ID, juniorMessageSelected(!profile.Club.Inactive), juniorMessageSelected(profile.Club.Inactive))
}
