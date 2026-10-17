package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

func juniorRecordDate(value string, required bool) (time.Time, error) {
	if strings.TrimSpace(value) == "" && !required {
		return time.Time{}, nil
	}
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: enter a valid calendar date", junior.ErrValidation)
	}
	return date, nil
}

func juniorRecordID(value string, required bool) (int64, error) {
	if value == "" && !required {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%w: select a valid record", junior.ErrValidation)
	}
	return id, nil
}

func (s *Server) juniorRecordClubID(r *http.Request) (int32, error) {
	p := s.juniorPrincipal(r)
	if p.AdminID == 0 {
		if p.ClubID < 1 {
			return 0, junior.ErrForbidden
		}
		return p.ClubID, nil
	}
	value := chi.URLParam(r, "clubID")
	if value == "" {
		value = r.FormValue("club_id")
	}
	id, err := juniorRecordID(value, true)
	if err != nil || id > 2147483647 {
		return 0, fmt.Errorf("%w: select a valid club", junior.ErrValidation)
	}
	return int32(id), nil
}

func (s *Server) juniorRecordSeason(r *http.Request) ([]junior.Season, int64, error) {
	seasons, err := junior.NewService(s.DB).ListSeasons(r.Context(), s.juniorPrincipal(r))
	if err != nil {
		return nil, 0, err
	}
	if r.URL.Query().Has("season_id") {
		id, err := juniorRecordID(r.URL.Query().Get("season_id"), true)
		if err != nil {
			return nil, 0, err
		}
		for _, season := range seasons {
			if season.ID == id {
				return seasons, id, nil
			}
		}
		return nil, 0, junior.ErrNotFound
	}
	for _, season := range seasons {
		if season.State == "open" {
			return seasons, season.ID, nil
		}
	}
	if len(seasons) > 0 {
		return seasons, seasons[0].ID, nil
	}
	return seasons, 0, nil
}

func juniorRecordsRedirect(w http.ResponseWriter, r *http.Request, p junior.Principal, clubID int32, seasonID int64, message string) {
	path := "/junior/contacts"
	if p.AdminID > 0 {
		path = fmt.Sprintf("/admin/juniors/clubs/%d", clubID)
	}
	values := url.Values{"success": {message}}
	if seasonID > 0 {
		values.Set("season_id", strconv.FormatInt(seasonID, 10))
	}
	http.Redirect(w, r, path+"?"+values.Encode(), http.StatusSeeOther)
}

func (s *Server) HandleJuniorAdminRecords() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		seasons, seasonID, err := s.juniorRecordSeason(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		clubs, err := junior.NewService(s.DB).ListClubs(r.Context(), p, seasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Junior clubs and seasons")
		fmt.Fprint(w, `<p class="text-muted">Permanent club records, junior participation and adult contacts. Junior teams and dates are separate from senior captain reporting.</p>`)
		writeJuniorRecordSeasonSelector(w, r, seasons, seasonID)
		fmt.Fprint(w, `<section class="card mb-4"><div class="card-header">Clubs</div><div class="table-responsive"><table class="table table-hover mb-0"><thead><tr><th>Club</th><th>Selected-season participation</th><th>Junior teams</th><th>Adult contacts</th></tr></thead><tbody>`)
		for _, club := range clubs {
			href := fmt.Sprintf("/admin/juniors/clubs/%d", club.ID)
			if seasonID > 0 {
				href += fmt.Sprintf("?season_id=%d", seasonID)
			}
			fmt.Fprintf(w, `<tr><td><a href="%s">%s</a></td><td>%s</td><td>%d</td><td>%d</td></tr>`, href, escapeHTML(club.Name), escapeHTML(junior.ParticipationLabel(club.State)), club.TeamCount, club.ContactCount)
		}
		if len(clubs) == 0 {
			fmt.Fprint(w, `<tr><td colspan="4" class="text-muted p-3">No clubs recorded yet. Add the first club below.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></section>`)
		fmt.Fprint(w, `<p><a class="btn btn-outline-primary" href="/admin/juniors/roster">Review a previous-season roster CSV</a></p>`)
		csrf := escapeHTML(middleware.CSRFToken(r))
		fmt.Fprintf(w, `<details class="card mb-4"><summary class="card-header">Add New Club</summary><form method="POST" action="/admin/juniors/clubs" class="card-body"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d"><div class="row g-3"><label class="col-md-6">Official club name<input class="form-control" name="name" maxlength="200" required></label><label class="col-md-3">Short name<input class="form-control" name="short_name" maxlength="80"></label><label class="col-md-3">Source / provenance<input class="form-control" name="source" maxlength="300" placeholder="Previous-season list or manual entry"></label></div><p class="small text-muted mt-3">Use an existing club above when it already exists. Adding a club does not activate senior reporting or enter it into a junior competition.</p><button class="btn btn-primary">Add club</button></form></details>`, csrf, seasonID)
		fmt.Fprintf(w, `<details class="card mb-4"%s><summary class="card-header">Configure junior seasons</summary><div class="card-body">`, map[bool]string{true: " open", false: ""}[len(seasons) == 0])
		for _, season := range seasons {
			writeJuniorSeasonForm(w, csrf, season)
		}
		writeJuniorSeasonForm(w, csrf, junior.Season{State: "open"})
		fmt.Fprint(w, `</div></details>`)
		juniorPageEnd(w)
	}
}

func writeJuniorRecordSeasonSelector(w http.ResponseWriter, r *http.Request, seasons []junior.Season, selected int64) {
	if len(seasons) == 0 {
		fmt.Fprint(w, `<div class="alert alert-info">No junior season is configured. A League Admin can add its dates below.</div>`)
		return
	}
	fmt.Fprintf(w, `<form method="GET" action="%s" class="d-flex flex-wrap align-items-end gap-2 mb-4"><label>Junior season<select class="form-select" name="season_id">`, escapeHTML(r.URL.Path))
	for _, season := range seasons {
		sel := ""
		if season.ID == selected {
			sel = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s · %s</option>`, season.ID, sel, escapeHTML(season.Name), escapeHTML(season.State))
	}
	fmt.Fprint(w, `</select></label>`)
	if strings.HasSuffix(r.URL.Path, "/preview") && r.URL.Query().Get("view") == "contacts" {
		fmt.Fprint(w, `<input type="hidden" name="view" value="contacts">`)
	}
	fmt.Fprint(w, `<button class="btn btn-outline-primary">Open season</button></form>`)
}

func writeJuniorSeasonForm(w http.ResponseWriter, csrf string, season junior.Season) {
	start, end := "", ""
	if !season.StartDate.IsZero() {
		start = season.StartDate.Format("2006-01-02")
	}
	if !season.EndDate.IsZero() {
		end = season.EndDate.Format("2006-01-02")
	}
	label := "Add junior season"
	if season.ID > 0 {
		label = "Save " + season.Name
	}
	fmt.Fprintf(w, `<form method="POST" action="/admin/juniors/seasons" class="border rounded p-3 mb-3"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="id" value="%d"><div class="row g-2"><label class="col-md-4">Season name<input class="form-control" name="name" maxlength="120" value="%s" required></label><label class="col-md-3">Start date<input class="form-control" type="date" name="start_date" value="%s" required></label><label class="col-md-3">End date<input class="form-control" type="date" name="end_date" value="%s" required></label><label class="col-md-2">State<select class="form-select" name="state"><option value="open"%s>Open</option><option value="closed"%s>Closed</option></select></label></div><button class="btn btn-outline-primary mt-3">%s</button></form>`, csrf, season.ID, escapeHTML(season.Name), start, end, selectedMode(season.State, "open"), selectedMode(season.State, "closed"), escapeHTML(label))
}

func (s *Server) HandleJuniorAdminSeasonSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		id, err := juniorRecordID(r.FormValue("id"), false)
		if r.FormValue("id") == "0" {
			id, err = 0, nil
		}
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		start, err := juniorRecordDate(r.FormValue("start_date"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		end, err := juniorRecordDate(r.FormValue("end_date"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		in := junior.SeasonInput{ID: id, Name: r.FormValue("name"), StartDate: start, EndDate: end, State: r.FormValue("state")}
		id, err = junior.NewService(s.DB).SaveSeason(r.Context(), s.juniorPrincipal(r), in)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/juniors/clubs?season_id=%d&success=Junior+season+saved", id), http.StatusSeeOther)
	}
}

func (s *Server) HandleJuniorAdminClubSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		id, err := juniorRecordID(r.FormValue("club_id"), false)
		if err != nil || id > 2147483647 {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		clubID, err := junior.NewService(s.DB).SaveClub(r.Context(), s.juniorPrincipal(r), junior.ClubInput{ID: int32(id), Name: r.FormValue("name"), ShortName: r.FormValue("short_name"), Source: r.FormValue("source")})
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasonID, _ := juniorRecordID(r.FormValue("season_id"), false)
		juniorRecordsRedirect(w, r, s.juniorPrincipal(r), clubID, seasonID, "Club saved")
	}
}

func (s *Server) HandleJuniorAdminParticipationSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		clubID, err := s.juniorRecordClubID(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasonID, err := juniorRecordID(r.FormValue("season_id"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		err = junior.NewService(s.DB).SetParticipation(r.Context(), s.juniorPrincipal(r), clubID, seasonID, r.FormValue("state"), r.FormValue("source"))
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		juniorRecordsRedirect(w, r, s.juniorPrincipal(r), clubID, seasonID, "Junior participation saved")
	}
}

func (s *Server) HandleJuniorAdminTeamSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		clubID, err := s.juniorRecordClubID(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		id, err := juniorRecordID(r.FormValue("team_id"), false)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		_, err = junior.NewService(s.DB).SaveTeam(r.Context(), s.juniorPrincipal(r), junior.TeamInput{ID: id, ClubID: clubID, Name: r.FormValue("name"), Archived: r.FormValue("archived") == "1"})
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasonID, _ := juniorRecordID(r.FormValue("season_id"), false)
		juniorRecordsRedirect(w, r, s.juniorPrincipal(r), clubID, seasonID, "Junior team saved")
	}
}

func (s *Server) HandleJuniorAdminContactSave() http.HandlerFunc {
	return s.handleJuniorRecordContactSave()
}
func (s *Server) HandleJuniorPortalContactSave() http.HandlerFunc {
	return s.handleJuniorRecordContactSave()
}

func (s *Server) handleJuniorRecordContactSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		clubID, err := s.juniorRecordClubID(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		id, err := juniorRecordID(r.FormValue("contact_id"), false)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasonID, err := juniorRecordID(r.FormValue("season_id"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		in := junior.ContactInput{ID: id, ClubID: clubID, SeasonID: seasonID, FullName: r.FormValue("full_name"), Email: r.FormValue("email"), Phone: r.FormValue("phone"), Archived: r.FormValue("archived") == "1", Role: r.FormValue("role"), AgeGroup: r.FormValue("age_group")}
		if value := r.FormValue("team_id"); value != "" {
			teamID, e := juniorRecordID(value, true)
			if e != nil {
				juniorHTTPError(w, r, e)
				return
			}
			in.TeamID = &teamID
		}
		if in.Role != "" {
			in.StartsOn, err = juniorRecordDate(r.FormValue("starts_on"), true)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			if value := r.FormValue("ends_on"); value != "" {
				end, e := juniorRecordDate(value, true)
				if e != nil {
					juniorHTTPError(w, r, e)
					return
				}
				in.EndsOn = &end
			}
		}
		_, err = junior.NewService(s.DB).SaveContact(r.Context(), s.juniorPrincipal(r), in)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		juniorRecordsRedirect(w, r, s.juniorPrincipal(r), clubID, seasonID, "Adult contact saved")
	}
}

func (s *Server) HandleJuniorAdminContactRoleEnd() http.HandlerFunc {
	return s.handleJuniorRecordRoleEnd()
}
func (s *Server) HandleJuniorPortalContactRoleEnd() http.HandlerFunc {
	return s.handleJuniorRecordRoleEnd()
}
func (s *Server) handleJuniorRecordRoleEnd() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		clubID, err := s.juniorRecordClubID(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		roleID, err := juniorRecordID(chi.URLParam(r, "roleID"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		end, err := juniorRecordDate(r.FormValue("ends_on"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		err = junior.NewService(s.DB).EndContactRole(r.Context(), s.juniorPrincipal(r), clubID, roleID, end)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasonID, _ := juniorRecordID(r.FormValue("season_id"), false)
		juniorRecordsRedirect(w, r, s.juniorPrincipal(r), clubID, seasonID, "Contact role end date saved")
	}
}

func (s *Server) HandleJuniorPortalContacts() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		seasons, seasonID, err := s.juniorRecordSeason(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		profile, err := junior.NewService(s.DB).ClubProfile(r.Context(), p, p.ClubID, seasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, profile.Club.Name+" — contacts and teams")
		writeJuniorRecordSeasonSelector(w, r, seasons, seasonID)
		s.JuniorClubRecordSection(w, r, profile)
		juniorPageEnd(w)
	}
}

// The common club dashboard uses this section for staff, Junior Leads and a
// read-only staff preview. Write capability is also checked by every command.
func (s *Server) JuniorClubRecordSection(w http.ResponseWriter, r *http.Request, profile junior.ClubProfile) {
	p := s.juniorPrincipal(r)
	readOnly := p.ReadOnly || (p.AdminID == 0 && profile.Season.State != "open")
	csrf := escapeHTML(middleware.CSRFToken(r))
	base := "/junior"
	if p.AdminID > 0 {
		base = fmt.Sprintf("/admin/juniors/clubs/%d", profile.Club.ID)
	}
	if p.AdminID > 0 && !readOnly {
		fmt.Fprintf(w, `<details class="card mb-4"><summary class="card-header">Edit club details</summary><form class="card-body" method="POST" action="/admin/juniors/clubs"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="club_id" value="%d"><input type="hidden" name="season_id" value="%d"><div class="row g-2"><label class="col-md-6">Official club name<input class="form-control" name="name" maxlength="200" value="%s" required></label><label class="col-md-3">Short name<input class="form-control" name="short_name" maxlength="80" value="%s"></label><label class="col-md-3">Reason / source<input class="form-control" name="source" maxlength="300" required></label></div><p class="small text-muted mt-2">This is the permanent club identity shared with the league. Correcting its name updates that shared identity; junior participation is managed separately.</p><button class="btn btn-outline-primary">Save club details</button></form></details>`, csrf, profile.Club.ID, profile.Season.ID, escapeHTML(profile.Club.Name), escapeHTML(profile.Club.ShortName))
	}
	writeJuniorBadgeSection(w, r, p, profile, readOnly)
	fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header">Junior participation</div><div class="card-body"><strong>%s</strong><div class="small text-muted">%s</div>`, escapeHTML(junior.ParticipationLabel(profile.Club.State)), escapeHTML(profile.Season.Name))
	if p.AdminID > 0 && !readOnly && profile.Season.ID > 0 {
		fmt.Fprintf(w, `<form method="POST" action="%s/participation" class="row g-2 mt-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d"><label class="col-md-4">Participation<select class="form-select" name="state"><option value="active"%s>Active</option><option value="withdrawn"%s>Withdrawn this season</option></select></label><label class="col-md-5">Source / reason<input class="form-control" name="source" maxlength="300" required placeholder="Entry form, previous season or staff decision"></label><div class="col-md-3 align-self-end"><button class="btn btn-primary">Save participation</button></div></form>`, base, csrf, profile.Season.ID, selectedMode(profile.Club.State, "active"), selectedMode(profile.Club.State, "withdrawn"))
	}
	if len(profile.Participation) > 0 {
		fmt.Fprint(w, `<details class="mt-3"><summary>Participation history</summary><ul>`)
		for _, item := range profile.Participation {
			fmt.Fprintf(w, `<li>%s — %s</li>`, escapeHTML(item.SeasonName), escapeHTML(junior.ParticipationLabel(item.State)))
		}
		fmt.Fprint(w, `</ul></details>`)
	}
	fmt.Fprint(w, `</div></section><section class="card mb-4" id="junior-teams"><div class="card-header">Permanent junior teams</div><div class="card-body"><p class="small text-muted">Competition entry is separate. Retired teams remain in history.</p>`)
	for _, team := range profile.Teams {
		if readOnly {
			fmt.Fprintf(w, `<p><strong>%s</strong> %s</p>`, escapeHTML(team.Name), map[bool]string{true: "(archived)", false: ""}[team.Archived])
			continue
		}
		checked := ""
		if team.Archived {
			checked = " checked"
		}
		fmt.Fprintf(w, `<form method="POST" action="%s/teams" class="d-flex flex-wrap align-items-end gap-2 border rounded p-3 mb-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d"><input type="hidden" name="team_id" value="%d"><label>Team name<input class="form-control" name="name" maxlength="120" value="%s" required></label><label class="form-check mb-2"><input class="form-check-input" type="checkbox" name="archived" value="1"%s> Archived</label><button class="btn btn-outline-primary">Save team</button></form>`, base, csrf, profile.Season.ID, team.ID, escapeHTML(team.Name), checked)
	}
	if len(profile.Teams) == 0 {
		fmt.Fprint(w, `<p class="text-muted">No junior teams recorded.</p>`)
	}
	if !readOnly {
		fmt.Fprintf(w, `<form method="POST" action="%s/teams" class="d-flex flex-wrap align-items-end gap-2 mt-3"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d"><label>New team name<input class="form-control" name="name" maxlength="120" placeholder="U11 A" required></label><button class="btn btn-primary">Add junior team</button></form>`, base, csrf, profile.Season.ID)
	}
	fmt.Fprint(w, `</div></section><section class="card mb-4" id="junior-contacts"><div class="card-header">Adult contacts and dated roles</div><div class="card-body"><p class="small text-muted">Keep the Junior Lead, Safeguarding Officer and age-group coaches up to date. Coaches do not need portal accounts. League Admin manages portal access separately.</p>`)
	for _, contact := range profile.Contacts {
		fmt.Fprintf(w, `<article class="border rounded p-3 mb-3" id="contact-%d"><h3 class="h6">%s%s</h3><p class="small">%s · %s</p>`, contact.ID, escapeHTML(contact.FullName), map[bool]string{true: " (archived)", false: ""}[contact.Archived], escapeHTML(contact.Email), escapeHTML(contact.Phone))
		for _, role := range contact.Roles {
			until := "current"
			if role.EndsOn != nil {
				until = role.EndsOn.Format("02 Jan 2006")
			}
			fmt.Fprintf(w, `<div class="small border-top py-2"><strong>%s %s %s</strong> · %s to %s`, escapeHTML(junior.RoleLabel(role.Role)), escapeHTML(role.AgeGroup), escapeHTML(role.TeamName), role.StartsOn.Format("02 Jan 2006"), escapeHTML(until))
			if !readOnly && role.EndsOn == nil {
				date := s.londonDate()
				if role.StartsOn.Format("2006-01-02") > date {
					date = role.StartsOn.Format("2006-01-02")
				}
				fmt.Fprintf(w, `<form method="POST" action="%s/contact-roles/%d/end" class="d-flex flex-wrap align-items-end gap-2 mt-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d"><label>Role end date<input class="form-control form-control-sm" type="date" name="ends_on" value="%s" required></label><button class="btn btn-sm btn-outline-secondary">End role</button></form>`, base, role.ID, csrf, profile.Season.ID, date)
			}
			fmt.Fprint(w, `</div>`)
		}
		if !readOnly && profile.Season.ID > 0 {
			fmt.Fprint(w, `<details class="mt-2"><summary>Edit contact / add another role</summary>`)
			writeJuniorContactForm(w, csrf, base, profile, contact, s.londonDate())
			fmt.Fprint(w, `</details>`)
		}
		fmt.Fprint(w, `</article>`)
	}
	if len(profile.Contacts) == 0 {
		fmt.Fprint(w, `<p class="text-muted">No adult contacts recorded yet.</p>`)
	}
	if !readOnly && profile.Season.ID > 0 {
		fmt.Fprint(w, `<details><summary>Add adult contact</summary>`)
		writeJuniorContactForm(w, csrf, base, profile, junior.Contact{}, s.londonDate())
		fmt.Fprint(w, `</details>`)
	}
	if readOnly {
		fmt.Fprint(w, `<p class="small text-muted">This view is read-only. Closed-season changes are managed by League Admin.</p>`)
	}
	fmt.Fprint(w, `</div></section>`)
}

func writeJuniorContactForm(w http.ResponseWriter, csrf, base string, profile junior.ClubProfile, contact junior.Contact, today string) {
	fmt.Fprintf(w, `<form method="POST" action="%s/contacts" class="mt-3"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d">`, base, csrf, profile.Season.ID)
	if contact.ID > 0 {
		fmt.Fprintf(w, `<input type="hidden" name="contact_id" value="%d">`, contact.ID)
	}
	fmt.Fprintf(w, `<div class="row g-2"><label class="col-md-4">Full name<input class="form-control" name="full_name" maxlength="200" value="%s" required></label><label class="col-md-4">Email<input class="form-control" type="email" name="email" maxlength="254" value="%s"></label><label class="col-md-4">Phone<input class="form-control" name="phone" maxlength="80" value="%s"></label><label class="col-md-4">Add role<select class="form-select" name="role"><option value="">Keep current roles / details only</option><option value="junior_lead">Junior Lead</option><option value="safeguarding_officer">Safeguarding Officer</option><option value="coach">Coach / age-group contact</option></select></label><label class="col-md-3">Coach age group<select class="form-select" name="age_group"><option value="">Club-wide role</option>`, escapeHTML(contact.FullName), escapeHTML(contact.Email), escapeHTML(contact.Phone))
	for _, age := range []string{"U9", "U11", "U13", "U15", "U18"} {
		fmt.Fprintf(w, `<option value="%s">%s</option>`, age, age)
	}
	fmt.Fprint(w, `</select></label><label class="col-md-5">Coach team (optional)<select class="form-select" name="team_id"><option value="">All teams at this age group</option>`)
	for _, team := range profile.Teams {
		if !team.Archived {
			fmt.Fprintf(w, `<option value="%d">%s</option>`, team.ID, escapeHTML(team.Name))
		}
	}
	fmt.Fprintf(w, `</select></label><label class="col-md-3">Role starts<input class="form-control" type="date" name="starts_on" value="%s"></label><label class="col-md-3">Role ends (optional)<input class="form-control" type="date" name="ends_on"></label>`, today)
	checked := ""
	if contact.Archived {
		checked = " checked"
	}
	if contact.ID > 0 {
		fmt.Fprintf(w, `<label class="col-md-6 form-check align-self-end mb-2"><input class="form-check-input" type="checkbox" name="archived" value="1"%s> Archive contact (retains role history)</label>`, checked)
	}
	fmt.Fprint(w, `</div><p class="small text-muted mt-2">Provide an email address or phone number. Junior Leads need an email address. Leave Add role blank when only changing contact details.</p><button class="btn btn-primary">Save adult contact</button></form>`)
}
