package httpserver

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

func juniorFixtureFormID(r *http.Request, key string) int64 {
	id, _ := strconv.ParseInt(r.FormValue(key), 10, 64)
	return id
}
func juniorFixturePathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id
}
func juniorFixtureDates(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' })
}
func juniorFixtureLink(p junior.Principal, view string, id int64) string {
	if p.ReadOnly {
		return fmt.Sprintf("/admin/juniors/clubs/%d/preview?view=%s&competition_id=%d", p.ClubID, url.QueryEscape(view), id)
	}
	base := "/junior/"
	if p.AdminID > 0 {
		base = "/admin/juniors/"
	}
	return fmt.Sprintf("%s%s?competition_id=%d", base, view, id)
}
func juniorFixtureCSRF(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `<input type="hidden" name="csrf_token" value="%s">`, escapeHTML(middleware.CSRFToken(r)))
}
func juniorFixtureCSV(w http.ResponseWriter, filename string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	writer := csv.NewWriter(w)
	for _, row := range rows {
		for i := range row {
			row[i] = junior.CSVCell(row[i])
		}
		if err := writer.Write(row); err != nil {
			return
		}
	}
	writer.Flush()
}
func (s *Server) handleJuniorCompetitions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
			if err := r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			id, err := svc.CreateCompetition(r.Context(), p, junior.Competition{SeasonID: juniorFixtureFormID(r, "season_id"), Name: r.FormValue("name"), AgeGroup: r.FormValue("age_group"), Division: r.FormValue("division"), Format: r.FormValue("format"), StartDate: r.FormValue("start_date"), EndDate: r.FormValue("end_date"), DefaultStartTime: r.FormValue("start_time")})
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/juniors/competitions/%d", id), http.StatusSeeOther)
			return
		}
		items, err := svc.ListCompetitions(r.Context(), p, juniorQueryID(r, "season_id"))
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		divisions := juniorCompetitionDivisions(items)
		{
			filtered := []junior.Competition{}
			for _, c := range items {
				if juniorCompetitionMatches(c, r.URL.Query()) {
					filtered = append(filtered, c)
				}
			}
			items = filtered
		}
		type seasonOption struct {
			id   int64
			name string
		}
		seasons := []seasonOption{}
		rows, err := s.DB.Query(r.Context(), `SELECT id,name FROM junior_seasons ORDER BY start_date DESC,id DESC`)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		for rows.Next() {
			var x seasonOption
			if err = rows.Scan(&x.id, &x.name); err != nil {
				rows.Close()
				juniorHTTPError(w, r, err)
				return
			}
			seasons = append(seasons, x)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Junior competitions and fixtures")
		defer juniorPageEnd(w)
		fmt.Fprintf(w, `<form method="GET" class="d-flex flex-wrap gap-2 align-items-end mb-3"><label>Season<select class="form-select" name="season_id"><option value="">All junior seasons</option>`)
		for _, season := range seasons {
			selected := ""
			if season.id == juniorQueryID(r, "season_id") {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, season.id, selected, escapeHTML(season.name))
		}
		fmt.Fprint(w, `</select></label><label>View<select class="form-select" name="queue"><option value="">All competitions</option>`)
		selected := ""
		if r.URL.Query().Get("queue") == "draft" {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="draft"%s>Unpublished or changed drafts</option></select></label>`, selected)
		juniorFixtureStringSelect(w, "age_group", "Age group", r.URL.Query().Get("age_group"), []string{"U9", "U11", "U13", "U15", "U18"})
		juniorFixtureStringSelect(w, "division", "Division", r.URL.Query().Get("division"), divisions)
		fmt.Fprintf(w, `<label>Search<input class="form-control" name="q" value="%s"></label><button class="btn btn-outline-primary">Apply filters</button></form><p>%d matching competitions.</p>`, escapeHTML(r.URL.Query().Get("q")), len(items))
		fmt.Fprint(w, `<p class="text-muted">Create competitions, review draft schedules and publish official fixtures. Junior schedules are separate from senior reporting.</p><div class="table-responsive"><table class="table"><thead><tr><th>Competition</th><th>Age group / division</th><th>Dates</th><th>Format</th><th>Official schedule</th></tr></thead><tbody>`)
		for _, c := range items {
			fmt.Fprintf(w, `<tr><td><a href="/admin/juniors/competitions/%d">%s</a></td><td>%s</td><td>%s to %s</td><td>%s</td><td>`, c.ID, escapeHTML(c.Name), escapeHTML(juniorCompetitionAgeDivision(c)), escapeHTML(c.StartDate), escapeHTML(c.EndDate), escapeHTML(c.Format))
			if c.PublicationID > 0 {
				fmt.Fprintf(w, `<a href="%s">Publication%d</a>`, escapeHTML(juniorFixtureLink(p, "fixtures", c.ID)), c.PublicationID)
			} else {
				fmt.Fprint(w, "Not published")
			}
			fmt.Fprint(w, `</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
		if p.AdminID > 0 && !p.ReadOnly {
			fmt.Fprint(w, `<section class="card mt-4"><div class="card-body"><h2 class="h4">Create competition</h2><form method="POST" class="row g-3">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprint(w, `<div class="col-md-4"><label class="form-label">Junior season</label><select class="form-select" name="season_id" required><option value="">Choose season</option>`)
			for _, x := range seasons {
				fmt.Fprintf(w, `<option value="%d">%s</option>`, x.id, escapeHTML(x.name))
			}
			fmt.Fprint(w, `</select></div><div class="col-md-5"><label class="form-label">Competition name</label><input class="form-control" name="name" required maxlength="200"></div><div class="col-md-3"><label class="form-label">Age group</label><select class="form-select" name="age_group">`)
			for _, a := range []string{"U9", "U11", "U13", "U15", "U18"} {
				fmt.Fprintf(w, `<option>%s</option>`, a)
			}
			fmt.Fprint(w, `</select></div><div class="col-md-4"><label class="form-label">Division (if applicable)</label><input class="form-control" name="division" maxlength="100" placeholder="e.g. North or Division 1"></div><div class="col-md-4"><label class="form-label">Format</label><select class="form-select" name="format"><option value="once">Play each other once</option><option value="homeaway">Home and away</option><option value="custom">Custom / manual</option></select></div><div class="col-md-3"><label class="form-label">First permitted date</label><input class="form-control" type="date" name="start_date" required></div><div class="col-md-3"><label class="form-label">Last permitted date</label><input class="form-control" type="date" name="end_date" required></div><div class="col-md-2"><label class="form-label">Default start</label><input class="form-control" type="time" name="start_time" value="18:00" required></div><div><button class="btn btn-primary">Create competition</button></div></form></div></section>`)
		}
	}
}

func (s *Server) handleJuniorCompetition() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		id := juniorFixturePathID(r)
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
			if err := r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			revision := juniorFixtureFormID(r, "revision")
			var err error
			switch r.FormValue("action") {
			case "entry":
				err = svc.SaveCompetitionEntry(r.Context(), p, id, revision, juniorFixtureFormID(r, "team_id"), r.FormValue("active") == "1")
			case "constraints":
				f := junior.FixtureConstraints{PlayingDates: juniorFixtureDates(r.FormValue("playing_dates")), ReserveDates: juniorFixtureDates(r.FormValue("reserve_dates")), DurationMinutes: int(juniorFixtureFormID(r, "duration_minutes"))}
				entries, e := svc.CompetitionEntries(r.Context(), p, id)
				if e != nil {
					err = e
					break
				}
				seen := map[int32]bool{}
				for _, entry := range entries {
					if seen[entry.ClubID] {
						continue
					}
					seen[entry.ClubID] = true
					key := strconv.FormatInt(int64(entry.ClubID), 10)
					name := strings.TrimSpace(r.FormValue("ground_" + key))
					groundID := juniorFixtureFormID(r, "ground_id_"+key)
					if name != "" || groundID > 0 {
						f.Grounds = append(f.Grounds, junior.GroundConstraint{ClubID: entry.ClubID, GroundID: groundID, Name: name, Capacity: int(juniorFixtureFormID(r, "capacity_"+key))})
					}
					for _, date := range juniorFixtureDates(r.FormValue("unavailable_" + key)) {
						f.Unavailable = append(f.Unavailable, junior.UnavailableDate{ClubID: entry.ClubID, Date: date})
					}
				}
				err = svc.SaveFixtureConstraints(r.Context(), p, id, revision, f)
			case "generate":
				err = svc.GenerateFixtures(r.Context(), p, id, revision, r.FormValue("replace_draft") == "1")
			case "save_fixture":
				err = svc.SaveDraftFixture(r.Context(), p, id, revision, junior.Fixture{ID: juniorFixtureFormID(r, "fixture_id"), HomeTeamID: juniorFixtureFormID(r, "home_team_id"), AwayTeamID: juniorFixtureFormID(r, "away_team_id"), MatchDate: r.FormValue("match_date"), StartTime: r.FormValue("start_time"), GroundName: r.FormValue("ground_name"), GroundID: juniorFixtureFormID(r, "ground_id")})
			case "retire_fixture":
				err = svc.RetireDraftFixture(r.Context(), p, id, revision, juniorFixtureFormID(r, "fixture_id"), r.FormValue("reason"))
			case "publish":
				_, err = svc.PublishFixtures(r.Context(), p, id, revision, r.FormValue("acknowledge") == "1", r.FormValue("reason"))
			default:
				err = junior.ErrValidation
			}
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/juniors/competitions/%d?success=%s", id, url.QueryEscape("Saved. Published fixtures and message delivery are shown separately.")), http.StatusSeeOther)
			return
		}
		c, err := svc.GetCompetition(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if p.AdminID == 0 || p.ReadOnly {
			http.Redirect(w, r, juniorFixtureLink(p, "fixtures", id), http.StatusSeeOther)
			return
		}
		entries, err := svc.CompetitionEntries(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		constraints, err := svc.GetFixtureConstraints(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		fixtures, err := svc.ListFixtures(r.Context(), p, id, false)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		issues, err := svc.FixtureChecks(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		grounds, err := svc.Grounds(r.Context(), p)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		options := []junior.CompetitionEntry{}
		rows, err := s.DB.Query(r.Context(), `SELECT t.id,t.club_id,t.name,c.name FROM junior_teams t JOIN clubs c ON c.id=t.club_id JOIN junior_club_seasons cs ON cs.club_id=t.club_id WHERE cs.season_id=$1 AND cs.state='active' AND NOT t.archived ORDER BY c.name,t.name,t.id`, c.SeasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		for rows.Next() {
			var e junior.CompetitionEntry
			if err = rows.Scan(&e.TeamID, &e.ClubID, &e.TeamName, &e.ClubName); err != nil {
				rows.Close()
				juniorHTTPError(w, r, err)
				return
			}
			options = append(options, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, c.Name)
		defer juniorPageEnd(w)
		fmt.Fprint(w, `<p><a href="/admin/juniors/grounds">Grounds and clashes: addresses, postcodes and permitted layouts</a></p>`)
		fmt.Fprintf(w, `<p>%s · %s · %s to %s · draft revision%d</p><div class="d-flex gap-2 flex-wrap mb-4"><a class="btn btn-outline-primary" href="%s">Official fixtures</a><a class="btn btn-outline-primary" href="/admin/juniors/results?competition_id=%d">Results and source history</a></div>`, escapeHTML(juniorCompetitionAgeDivision(c)), escapeHTML(c.Format), escapeHTML(c.StartDate), escapeHTML(c.EndDate), c.Revision, escapeHTML(juniorFixtureLink(p, "fixtures", id)), id)
		if c.SeasonState == "closed" {
			fmt.Fprint(w, `<div class="alert alert-info">This junior season is closed. Fixtures and result records are read-only.</div><h2 class="h4">Retained draft schedule</h2><div class="table-responsive"><table class="table"><thead><tr><th>Fixture</th><th>Date and time</th><th>Home</th><th>Away</th><th>Ground</th></tr></thead><tbody>`)
			for _, f := range fixtures {
				fmt.Fprintf(w, `<tr><td>%d</td><td>%s %s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, f.ID, escapeHTML(f.MatchDate), escapeHTML(f.StartTime), escapeHTML(f.HomeTeamName), escapeHTML(f.AwayTeamName), escapeHTML(f.GroundName))
			}
			fmt.Fprint(w, `</tbody></table></div>`)
			return
		}
		fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Participating teams</h2><ul>`)
		for _, e := range entries {
			fmt.Fprintf(w, `<li>%s — %s<form method="POST" class="d-inline ms-2">`, escapeHTML(e.ClubName), escapeHTML(e.TeamName))
			juniorFixtureCSRF(w, r)
			fmt.Fprintf(w, `<input type="hidden" name="action" value="entry"><input type="hidden" name="revision" value="%d"><input type="hidden" name="team_id" value="%d"><button class="btn btn-sm btn-outline-secondary">Withdraw entry</button></form></li>`, c.Revision, e.TeamID)
		}
		fmt.Fprint(w, `</ul><form method="POST" class="d-flex gap-2 flex-wrap">`)
		juniorFixtureCSRF(w, r)
		fmt.Fprintf(w, `<input type="hidden" name="action" value="entry"><input type="hidden" name="active" value="1"><input type="hidden" name="revision" value="%d"><label class="visually-hidden" for="entry-team">Team</label><select id="entry-team" class="form-select" style="max-width:32rem" name="team_id" required>`, c.Revision)
		for _, e := range options {
			fmt.Fprintf(w, `<option value="%d">%s — %s</option>`, e.TeamID, escapeHTML(e.ClubName), escapeHTML(e.TeamName))
		}
		fmt.Fprint(w, `</select><button class="btn btn-primary">Add team</button></form></div></section>`)
		fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Dates and constraints</h2><form method="POST" class="row g-3">`)
		juniorFixtureCSRF(w, r)
		fmt.Fprintf(w, `<input type="hidden" name="action" value="constraints"><input type="hidden" name="revision" value="%d"><div class="col-md-5"><label class="form-label">Playing dates — one YYYY-MM-DD per line</label><textarea class="form-control" name="playing_dates" rows="6">%s</textarea></div><div class="col-md-5"><label class="form-label">Reserve dates — one per line</label><textarea class="form-control" name="reserve_dates" rows="6">%s</textarea></div><div class="col-md-2"><label class="form-label">Match duration (minutes)</label><input class="form-control" type="number" name="duration_minutes" min="1" max="720" value="%d" required></div>`, c.Revision, escapeHTML(strings.Join(constraints.PlayingDates, "\n")), escapeHTML(strings.Join(constraints.ReserveDates, "\n")), func() int {
			if constraints.DurationMinutes > 0 {
				return constraints.DurationMinutes
			}
			return 120
		}())
		clubs := map[int32]junior.CompetitionEntry{}
		for _, e := range entries {
			clubs[e.ClubID] = e
		}
		clubIDs := []int32{}
		for clubID := range clubs {
			clubIDs = append(clubIDs, clubID)
		}
		sort.Slice(clubIDs, func(i, j int) bool { return clubs[clubIDs[i]].ClubName < clubs[clubIDs[j]].ClubName })
		for _, clubID := range clubIDs {
			ground := ""
			var groundID int64
			capacity := 1
			for _, g := range constraints.Grounds {
				if g.ClubID == clubID {
					ground = g.Name
					groundID = g.GroundID
					capacity = g.Capacity
					break
				}
			}
			unavailable := []string{}
			for _, u := range constraints.Unavailable {
				if u.ClubID == clubID {
					unavailable = append(unavailable, u.Date)
				}
			}
			fmt.Fprintf(w, `<fieldset class="col-12 border rounded p-3"><legend class="h6">%s</legend><div class="row g-2"><div class="col-md-7">`, escapeHTML(clubs[clubID].ClubName))
			if len(grounds) > 0 && c.AgeGroup != "U9" {
				fmt.Fprint(w, `<label class="form-label">Saved home venue (neutral venues may also be selected)</label>`)
				juniorGroundSelect(w, fmt.Sprintf("ground_id_%d", clubID), groundID, grounds)
			} else {
				fmt.Fprintf(w, `<label class="form-label">Home ground (blank means unknown)</label><input class="form-control" name="ground_%d" value="%s" maxlength="200"><label class="form-label">Concurrent pitches</label><input class="form-control" type="number" name="capacity_%d" value="%d" min="1" max="20">`, clubID, escapeHTML(ground), clubID, capacity)
			}
			fmt.Fprintf(w, `</div><div class="col-md-5"><label class="form-label">Unavailable dates — one per line</label><textarea class="form-control" name="unavailable_%d" rows="2">%s</textarea></div></div></fieldset>`, clubID, escapeHTML(strings.Join(unavailable, "\n")))
		}
		fmt.Fprint(w, `<div><button class="btn btn-primary">Save dates and constraints</button></div></form></div></section>`)
		fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Draft schedule</h2><p>Draft edits do not change the official publication. Regeneration retains the replaced draft as history.</p>`)
		if c.Format != "custom" && c.PublicationID == 0 {
			fmt.Fprint(w, `<form method="POST" class="mb-3">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprintf(w, `<input type="hidden" name="action" value="generate"><input type="hidden" name="revision" value="%d"><label class="form-check"><input class="form-check-input" type="checkbox" name="replace_draft" value="1">Replace existing draft, including manual edits</label><button class="btn btn-outline-primary mt-2">Generate draft</button></form>`, c.Revision)
		}
		for _, issue := range issues {
			class := "warning"
			if issue.Blocking {
				class = "danger"
			}
			fmt.Fprintf(w, `<div class="alert alert-%s py-2">%s</div>`, class, escapeHTML(issue.Message))
		}
		for _, f := range fixtures {
			s.renderJuniorDraftFixtureForm(w, r, c, entries, f, grounds)
			fmt.Fprint(w, `<form method="POST" class="d-flex flex-wrap gap-2 mb-3">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprintf(w, `<input type="hidden" name="action" value="retire_fixture"><input type="hidden" name="revision" value="%d"><input type="hidden" name="fixture_id" value="%d"><label class="visually-hidden" for="retire-%d">Removal reason</label><input id="retire-%d" class="form-control" style="max-width:30rem" name="reason" placeholder="Reason for removing this draft fixture" required maxlength="10000"><button class="btn btn-sm btn-outline-danger">Remove draft fixture</button></form>`, c.Revision, f.ID, f.ID, f.ID)
		}
		fmt.Fprint(w, `<h3 class="h5 mt-3">Add manual fixture</h3>`)
		s.renderJuniorDraftFixtureForm(w, r, c, entries, junior.Fixture{MatchDate: c.StartDate, StartTime: c.DefaultStartTime}, grounds)
		blocked := ""
		for _, issue := range issues {
			if issue.Blocking {
				blocked = " disabled"
				break
			}
		}
		if blocked != "" {
			fmt.Fprint(w, `<p class="text-danger">Resolve the blocking warnings before publishing official fixtures.</p>`)
		}
		fmt.Fprint(w, `<hr><form method="POST">`)
		juniorFixtureCSRF(w, r)
		fmt.Fprintf(w, `<input type="hidden" name="action" value="publish"><input type="hidden" name="revision" value="%d"><label class="form-check mb-2"><input class="form-check-input" type="checkbox" name="acknowledge" value="1" required>I have checked missing ground information and external/senior bookings.</label><label class="form-label">Publication / correction reason and checks performed</label><textarea class="form-control mb-3" name="reason" required maxlength="10000"></textarea><button class="btn btn-success"%s>Publish official fixtures</button></form></div></section>`, c.Revision, blocked)
	}
}
func (s *Server) renderJuniorDraftFixtureForm(w http.ResponseWriter, r *http.Request, c junior.Competition, entries []junior.CompetitionEntry, f junior.Fixture, grounds []junior.Ground) {
	fmt.Fprint(w, `<form method="POST" class="row g-2 border rounded p-2 mb-3">`)
	juniorFixtureCSRF(w, r)
	fmt.Fprintf(w, `<input type="hidden" name="action" value="save_fixture"><input type="hidden" name="revision" value="%d"><input type="hidden" name="fixture_id" value="%d">`, c.Revision, f.ID)
	for _, side := range []struct {
		name, label string
		id          int64
	}{{"home_team_id", "Home team", f.HomeTeamID}, {"away_team_id", "Away team", f.AwayTeamID}} {
		fmt.Fprintf(w, `<div class="col-md-3"><label class="form-label">%s</label><select class="form-select" name="%s" required><option value="">Choose team</option>`, side.label, side.name)
		for _, e := range entries {
			selected := ""
			if e.TeamID == side.id {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s — %s</option>`, e.TeamID, selected, escapeHTML(e.ClubName), escapeHTML(e.TeamName))
		}
		fmt.Fprint(w, `</select></div>`)
	}
	fmt.Fprintf(w, `<div class="col-md-2"><label class="form-label">Date</label><input class="form-control" type="date" name="match_date" value="%s" required></div><div class="col-md-2"><label class="form-label">Start</label><input class="form-control" type="time" name="start_time" value="%s" required></div><div class="col-12"><label class="form-label">Ground / neutral venue</label>`, escapeHTML(f.MatchDate), escapeHTML(f.StartTime))
	if len(grounds) > 0 && c.AgeGroup != "U9" {
		juniorGroundSelect(w, "ground_id", f.GroundID, grounds)
		if f.ID > 0 && f.GroundID == 0 {
			fmt.Fprintf(w, `<p class="text-danger">Choose the saved venue for this older label: %s</p>`, escapeHTML(f.GroundName))
		}
	} else {
		fmt.Fprintf(w, `<input class="form-control" name="ground_name" value="%s" maxlength="200">`, escapeHTML(f.GroundName))
	}
	fmt.Fprintf(w, `</div><div><button class="btn btn-sm btn-primary">Save draft fixture</button><span class="small text-muted ms-2">Fixture %d</span></div></form>`, f.ID)
}
func (s *Server) handleJuniorFixture() http.HandlerFunc { return s.handleJuniorFixtures() }
func juniorCompetitionAgeDivision(c junior.Competition) string {
	if c.Division == "" {
		return c.AgeGroup
	}
	return c.AgeGroup + " · " + c.Division
}
func juniorCompetitionDivisions(items []junior.Competition) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range items {
		if c.Division != "" && !seen[c.Division] {
			out = append(out, c.Division)
			seen[c.Division] = true
		}
	}
	sort.Strings(out)
	return out
}
func juniorCompetitionMatches(c junior.Competition, q url.Values) bool {
	if q.Get("queue") == "draft" && c.PublicationID > 0 && c.PublicationRevision == c.Revision {
		return false
	}
	if q.Get("age_group") != "" && q.Get("age_group") != c.AgeGroup {
		return false
	}
	if q.Get("division") != "" && q.Get("division") != c.Division {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(q.Get("q")))
	return query == "" || strings.Contains(strings.ToLower(c.Name+" "+c.AgeGroup+" "+c.Division), query)
}
func juniorFixtureStringSelect(w http.ResponseWriter, name, label, value string, options []string) {
	fmt.Fprintf(w, `<label>%s<select class="form-select" name="%s"><option value="">All</option>`, escapeHTML(label), name)
	for _, option := range options {
		selected := ""
		if option == value {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(option), selected, escapeHTML(option))
	}
	fmt.Fprint(w, `</select></label>`)
}
func juniorFilterFixtures(items []junior.Fixture, q url.Values) ([]junior.Fixture, error) {
	for _, key := range []string{"date_from", "date_to"} {
		if q.Get(key) != "" {
			if _, err := juniorRecordDate(q.Get(key), true); err != nil {
				return nil, err
			}
		}
	}
	if q.Get("date_from") != "" && q.Get("date_to") != "" && q.Get("date_from") > q.Get("date_to") {
		return nil, junior.ErrValidation
	}
	var seasonID, clubID int64
	var err error
	if seasonID, err = juniorRecordID(q.Get("season_id"), false); err != nil {
		return nil, err
	}
	if clubID, err = juniorRecordID(q.Get("club_id"), false); err != nil || clubID > 2147483647 {
		return nil, junior.ErrValidation
	}
	out := []junior.Fixture{}
	query := strings.ToLower(strings.TrimSpace(q.Get("q")))
	for _, f := range items {
		if seasonID > 0 && f.SeasonID != seasonID {
			continue
		}
		if clubID > 0 && int64(f.HomeClubID) != clubID && int64(f.AwayClubID) != clubID {
			continue
		}
		if q.Get("age_group") != "" && q.Get("age_group") != f.AgeGroup {
			continue
		}
		if q.Get("division") != "" && q.Get("division") != f.Division {
			continue
		}
		if q.Get("date_from") != "" && f.MatchDate < q.Get("date_from") {
			continue
		}
		if q.Get("date_to") != "" && f.MatchDate > q.Get("date_to") {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(f.CompetitionName+" "+f.Division+" "+f.HomeClubName+" "+f.AwayClubName+" "+f.HomeTeamName+" "+f.AwayTeamName+" "+f.GroundName+" "+f.GroundPostcode+" "+f.GroundAddress), query) {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}
func (s *Server) handleJuniorFixtures() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		id := juniorQueryID(r, "competition_id")
		publicationID := juniorQueryID(r, "publication_id")
		published := r.URL.Query().Get("view") != "draft"
		if publicationID > 0 {
			published = true
		}
		if p.AdminID == 0 || p.ReadOnly {
			published = true
		}
		var items []junior.Fixture
		var err error
		if publicationID > 0 {
			items, err = svc.PublicationFixtures(r.Context(), p, id, publicationID)
		} else {
			items, err = svc.ListFixtures(r.Context(), p, id, published)
		}
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		items, err = juniorFilterFixtures(items, r.URL.Query())
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		competitions, err := svc.ListCompetitions(r.Context(), p, 0)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if r.URL.Query().Get("export") == "csv" {
			juniorFixtureCSV(w, "junior-fixtures.csv", junior.FixtureCSVRows(items))
			return
		}
		seasons, competitionOptions, clubs, err := s.juniorConcessionFilterOptions(r, p)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		publications, err := svc.FixturePublications(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Official junior fixtures")
		defer juniorPageEnd(w)
		if !published {
			fmt.Fprint(w, `<div class="alert alert-warning">Draft fixtures — not visible as official fixtures to clubs.</div>`)
		}
		if publicationID > 0 {
			fmt.Fprintf(w, `<div class="alert alert-info">Showing immutable publication %d. Select Current official fixtures to return to the latest schedule.</div>`, publicationID)
		}
		fmt.Fprint(w, `<form method="GET" class="row g-2 mb-3">`)
		if p.ReadOnly {
			fmt.Fprint(w, `<input type="hidden" name="view" value="fixtures">`)
		} else if !published {
			fmt.Fprint(w, `<input type="hidden" name="view" value="draft">`)
		}
		juniorConcessionSelect(w, "season_id", "Season", juniorQueryID(r, "season_id"), seasons)
		juniorConcessionSelect(w, "competition_id", "Competition", id, competitionOptions)
		fmt.Fprint(w, `<label class="col-md-4">Publication history<select class="form-select" name="publication_id"><option value="">Current official fixtures</option>`)
		for _, publication := range publications {
			selected := ""
			if publication.ID == publicationID {
				selected = " selected"
			}
			current := ""
			if publication.Current {
				current = " · current"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s · %s · publication %d%s</option>`, publication.ID, selected, escapeHTML(publication.CompetitionName), escapeHTML(publication.Division), publication.ID, current)
		}
		fmt.Fprint(w, `</select></label>`)
		if p.AdminID > 0 && !p.ReadOnly {
			juniorConcessionSelect(w, "club_id", "Club", juniorQueryID(r, "club_id"), clubs)
		}
		fmt.Fprint(w, `<div class="col-md-2">`)
		juniorFixtureStringSelect(w, "age_group", "Age group", r.URL.Query().Get("age_group"), []string{"U9", "U11", "U13", "U15", "U18"})
		fmt.Fprint(w, `</div><div class="col-md-2">`)
		juniorFixtureStringSelect(w, "division", "Division", r.URL.Query().Get("division"), juniorCompetitionDivisions(competitions))
		fmt.Fprint(w, `</div>`)
		fmt.Fprintf(w, `<label class="col-md-3">From date<input class="form-control" type="date" name="date_from" value="%s"></label><label class="col-md-3">To date<input class="form-control" type="date" name="date_to" value="%s"></label><label class="col-md-4">Club, team, competition or ground<input class="form-control" name="q" value="%s"></label><div class="col-md-2 align-self-end"><button class="btn btn-outline-primary">Apply filters</button></div></form><div class="d-flex flex-wrap gap-2 mb-3">`, escapeHTML(r.URL.Query().Get("date_from")), escapeHTML(r.URL.Query().Get("date_to")), escapeHTML(r.URL.Query().Get("q")))
		export := *r.URL
		values := export.Query()
		values.Set("export", "csv")
		export.RawQuery = values.Encode()
		fmt.Fprintf(w, `<a class="btn btn-outline-secondary" href="%s">Export matching fixtures</a></div><p>%d fixtures. Official publication is independent of email delivery.</p>`, escapeHTML(export.String()), len(items))
		fmt.Fprint(w, `<div class="table-responsive"><table class="table"><thead><tr><th>Date/start</th><th>Competition</th><th>Home</th><th>Away</th><th>Ground</th><th>Publication</th></tr></thead><tbody>`)
		for _, f := range items {
			fmt.Fprintf(w, `<tr><td>%s %s<div class="small">Fixture%d</div></td><td>%s<div class="small">%s · %s</div></td><td>%s — %s</td><td>%s — %s</td><td>%s</td><td>%d</td></tr>`, escapeHTML(f.MatchDate), escapeHTML(f.StartTime), f.ID, escapeHTML(f.CompetitionName), escapeHTML(f.AgeGroup), escapeHTML(f.Division), escapeHTML(f.HomeClubName), escapeHTML(f.HomeTeamName), escapeHTML(f.AwayClubName), escapeHTML(f.AwayTeamName), escapeHTML(f.GroundName+" · "+f.GroundPostcode+" · "+f.GroundAddress), f.PublicationID)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
	}
}
