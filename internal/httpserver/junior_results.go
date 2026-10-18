package httpserver

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"cricket-ground-feedback/internal/junior"
)

func (s *Server) handleJuniorResults() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		id := juniorQueryID(r, "competition_id")
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
			if err := r.ParseMultipartForm(5 << 20); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			id = juniorFixtureFormID(r, "competition_id")
			file, _, err := r.FormFile("results_file")
			if err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			defer file.Close()
			raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			summary, err := svc.ImportFixtureResultsCSV(r.Context(), p, id, r.FormValue("source_name"), raw)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			message := fmt.Sprintf("Import %d saved: %d result observations, %d new concession candidates.", summary.ImportID, summary.Observations, summary.Candidates)
			if summary.Replayed {
				message = "This exact source file was already imported. No observations or candidates duplicated."
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/juniors/results?competition_id=%d&success=%s", id, url.QueryEscape(message)), http.StatusSeeOther)
			return
		}
		competitions, err := svc.ListCompetitions(r.Context(), p, 0)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		importCompetitions := []junior.Competition{}
		selectedClosed := false
		for _, c := range competitions {
			if c.SeasonState == "open" {
				importCompetitions = append(importCompetitions, c)
			}
			if c.ID == id && c.SeasonState == "closed" {
				selectedClosed = true
			}
		}
		var observations []junior.ResultObservation
		if id > 0 {
			observations, err = svc.ListResultObservations(r.Context(), p, id)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
		}
		s.juniorPageStart(w, r, "Junior results and source evidence")
		defer juniorPageEnd(w)
		fmt.Fprint(w, `<div class="alert alert-info">Play-Cricket junior result-code mapping and feed coverage are not connected. Import verified source data using local official fixture IDs. Unknown/missing results do not prove concession.</div><form method="GET" class="d-flex gap-2 mb-3"><label class="visually-hidden" for="result-competition">Competition</label><select class="form-select" name="competition_id" id="result-competition" required><option value="">Choose competition</option>`)
		for _, c := range competitions {
			selected := ""
			if c.ID == id {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s · %s</option>`, c.ID, selected, escapeHTML(c.Name), escapeHTML(c.AgeGroup))
		}
		fmt.Fprint(w, `</select><button class="btn btn-outline-primary">View results</button></form>`)
		if selectedClosed {
			fmt.Fprint(w, `<div class="alert alert-info">This junior season is closed. Result observations and decisions are read-only.</div>`)
		}
		if p.AdminID > 0 && !p.ReadOnly && !selectedClosed && len(importCompetitions) > 0 {
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Import result observations</h2><p>CSV columns: <code>fixture_id,outcome,evidence</code>. Outcomes: <code>played, conceded_home, conceded_away, possible_concession, abandoned, postponed, bye, unknown</code>. Evidence is required for concession indicators. Maximum 4 MiB / 5,000 rows; every row is validated before any import is committed.</p><p>Download the <a href="`+escapeHTML(juniorFixtureLink(p, "fixtures", id))+`&amp;export=csv">official fixture IDs</a> first. Use the source's explicit conceding side; do not guess from a win, missing scorecard or absent report.</p><form method="POST" enctype="multipart/form-data" class="row g-3">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprint(w, `<div class="col-md-5"><label class="form-label">Competition</label><select class="form-select" name="competition_id" required>`)
			for _, c := range importCompetitions {
				selected := ""
				if c.ID == id {
					selected = " selected"
				}
				fmt.Fprintf(w, `<option value="%d"%s>%s · %s</option>`, c.ID, selected, escapeHTML(c.Name), escapeHTML(c.AgeGroup))
			}
			fmt.Fprint(w, `</select></div><div class="col-md-7"><label class="form-label">Source name / reference</label><input class="form-control" name="source_name" required maxlength="200" placeholder="e.g. reviewed results export,17October2026"></div><div class="col-12"><label class="form-label">Results CSV</label><input class="form-control" type="file" accept=".csv,text/csv" name="results_file" required></div><div><button class="btn btn-primary">Validate and import results</button></div></form></div></section>`)
		}
		fmt.Fprintf(w, `<h2 class="h4">Immutable observation history (%d)</h2><div class="table-responsive"><table class="table"><thead><tr><th>Fixture</th><th>Outcome</th><th>Evidence</th><th>Source/observed</th><th>Revision</th></tr></thead><tbody>`, len(observations))
		for _, o := range observations {
			fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td class="text-break">%s</td><td>%s<div class="small">%s</div></td><td>Observation%d; supersedes%d</td></tr>`, o.FixtureID, escapeHTML(o.Outcome), escapeHTML(o.Evidence), escapeHTML(o.SourceName), escapeHTML(o.ObservedAt), o.ID, o.SupersedesID)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
	}
}

func (s *Server) handleJuniorConcessions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			err := svc.ReviewConcession(r.Context(), p, juniorFixtureFormID(r, "candidate_id"), juniorFixtureFormID(r, "revision"), r.FormValue("action"), juniorFixtureFormID(r, "conceding_team_id"), int32(juniorFixtureFormID(r, "information_club_id")), r.FormValue("reason"))
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			values := r.URL.Query()
			values.Del("candidate_id")
			values.Del("export")
			values.Set("success", "Review saved. Information requests and delivery outcomes are in Messages.")
			http.Redirect(w, r, "/admin/juniors/concessions?"+values.Encode(), http.StatusSeeOther)
			return
		}
		filter := junior.ConcessionFilter{SeasonID: juniorQueryID(r, "season_id"), CompetitionID: juniorQueryID(r, "competition_id"), ClubID: int32(juniorQueryID(r, "club_id")), AgeGroup: r.URL.Query().Get("age_group"), Division: r.URL.Query().Get("division"), DateFrom: r.URL.Query().Get("date_from"), DateTo: r.URL.Query().Get("date_to"), State: r.URL.Query().Get("state"), Query: r.URL.Query().Get("q")}
		if r.URL.Query().Get("queue") == "review" && filter.State == "" {
			filter.State = "review"
		}
		items, err := svc.ListConcessions(r.Context(), p, filter)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		candidateID := juniorQueryID(r, "candidate_id")
		if candidateID > 0 {
			selected := []junior.Concession{}
			for _, c := range items {
				if c.ID == candidateID {
					selected = append(selected, c)
				}
			}
			if len(selected) == 0 {
				juniorHTTPError(w, r, junior.ErrNotFound)
				return
			}
			items = selected
		}
		if r.URL.Query().Get("export") == "csv" {
			juniorFixtureCSV(w, "junior-concessions.csv", junior.ConcessionCSVRows(items))
			return
		}
		seasons, competitions, clubs, err := s.juniorConcessionFilterOptions(r, p)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		competitionRecords, err := svc.ListCompetitions(r.Context(), p, filter.SeasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Junior concessions")
		defer juniorPageEnd(w)
		fmt.Fprint(w, `<p>Detected indicators require a staff decision. Missing results, rain, abandonment, postponement and byes are never automatically confirmed. A changed source reopens review while the last confirmed decision remains in totals until staff supersede it.</p><form method="GET" class="row g-2 mb-3">`)
		if p.ReadOnly {
			fmt.Fprint(w, `<input type="hidden" name="view" value="concessions">`)
		}
		juniorConcessionSelect(w, "season_id", "Season", filter.SeasonID, seasons)
		juniorConcessionSelect(w, "competition_id", "Competition", filter.CompetitionID, competitions)
		if p.AdminID > 0 && !p.ReadOnly {
			juniorConcessionSelect(w, "club_id", "Conceding / indicated club", int64(filter.ClubID), clubs)
		}
		fmt.Fprint(w, `<div class="col-md-2"><label class="form-label">Age group</label><select class="form-select" name="age_group"><option value="">All ages</option>`)
		for _, a := range []string{"U9", "U11", "U13", "U15", "U18"} {
			selected := ""
			if a == filter.AgeGroup {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option%s>%s</option>`, selected, a)
		}
		fmt.Fprint(w, `</select></div><div class="col-md-2">`)
		juniorFixtureStringSelect(w, "division", "Division", filter.Division, juniorCompetitionDivisions(competitionRecords))
		fmt.Fprint(w, `</div><div class="col-md-2"><label class="form-label">Review view</label><select class="form-select" name="state"><option value="">All detected</option>`)
		for _, state := range []string{"review", "awaiting_review", "information_requested", "source_changed", "confirmed", "dismissed"} {
			selected := ""
			if state == filter.State {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, state, selected, escapeHTML(strings.ReplaceAll(state, "_", " ")))
		}
		fmt.Fprintf(w, `</select></div><label class="col-md-3">From date<input class="form-control" type="date" name="date_from" value="%s"></label><label class="col-md-3">To date<input class="form-control" type="date" name="date_to" value="%s"></label><div class="col-md-4"><label class="form-label">Search</label><input class="form-control" name="q" value="%s"></div><div><button class="btn btn-outline-primary">Filter</button></div></form>`, escapeHTML(filter.DateFrom), escapeHTML(filter.DateTo), escapeHTML(filter.Query))
		export := *r.URL
		values := export.Query()
		values.Set("export", "csv")
		export.RawQuery = values.Encode()
		confirmed := 0
		for _, c := range items {
			if c.Confirmed {
				confirmed++
			}
		}
		fmt.Fprintf(w, `<div class="d-flex justify-content-between flex-wrap gap-2 mb-3"><p>%d matching records · %d currently confirmed. Views may overlap while confirmed decisions await source review.</p><a class="btn btn-outline-secondary" href="%s">Export matching concessions</a></div>`, len(items), confirmed, escapeHTML(export.String()))
		for _, c := range items {
			fmt.Fprintf(w, `<article class="card mb-3"><div class="card-body"><h2 class="h5">%s — %s v %s</h2><p>%s · %s · %s · Fixture%d</p><p><strong>%s</strong> · currently confirmed: %t</p><p class="text-break">Source: %s · %s<br>%s</p>`, escapeHTML(c.CompetitionName), escapeHTML(c.HomeTeamName), escapeHTML(c.AwayTeamName), escapeHTML(juniorCompetitionAgeDivision(junior.Competition{AgeGroup: c.AgeGroup, Division: c.Division})), escapeHTML(c.MatchDate), escapeHTML(c.HomeClubName+" / "+c.AwayClubName), c.FixtureID, escapeHTML(strings.ReplaceAll(c.State, "_", " ")), c.Confirmed, escapeHTML(c.SourceName), escapeHTML(c.Outcome), escapeHTML(c.Evidence))
			if c.Confirmed {
				club, team := junior.ConcessionConcedingNames(c)
				fmt.Fprintf(w, `<p>Last confirmed conceding side: <strong>%s — %s</strong>.</p>`, escapeHTML(club), escapeHTML(team))
			}
			closed := false
			for _, comp := range competitionRecords {
				if comp.ID == c.CompetitionID {
					closed = comp.SeasonState == "closed"
					break
				}
			}
			if closed {
				fmt.Fprint(w, `<p class="text-muted">Closed junior season — review history is read-only.</p>`)
			}
			if p.AdminID > 0 && !p.ReadOnly && !closed {
				confirmButton := ""
				if c.Outcome == "conceded_home" || c.Outcome == "conceded_away" || c.Outcome == "possible_concession" {
					confirmButton = `<button class="btn btn-success" name="action" value="confirm">Confirm concession</button>`
				}
				fmt.Fprint(w, `<form method="POST" class="row g-2">`)
				juniorFixtureCSRF(w, r)
				fmt.Fprintf(w, `<input type="hidden" name="candidate_id" value="%d"><input type="hidden" name="revision" value="%d"><div class="col-md-4"><label class="form-label">Conceding team (for confirmation)</label><select class="form-select" name="conceding_team_id"><option value="">Choose</option><option value="%d">%s — %s</option><option value="%d">%s — %s</option></select></div><div class="col-md-4"><label class="form-label">Information recipient club</label><select class="form-select" name="information_club_id"><option value="%d">%s</option><option value="%d">%s</option></select></div><div class="col-12"><label class="form-label">Review reason / information request</label><textarea class="form-control" name="reason" required maxlength="10000"></textarea></div><div class="d-flex flex-wrap gap-2">%s<button class="btn btn-outline-secondary" name="action" value="dismiss">Dismiss</button><button class="btn btn-outline-primary" name="action" value="request_information">Request information</button></div></form>`, c.ID, c.Revision, c.HomeTeamID, escapeHTML(c.HomeClubName), escapeHTML(c.HomeTeamName), c.AwayTeamID, escapeHTML(c.AwayClubName), escapeHTML(c.AwayTeamName), c.HomeClubID, escapeHTML(c.HomeClubName), c.AwayClubID, escapeHTML(c.AwayClubName), confirmButton)
			}
			history, e := svc.ConcessionHistory(r.Context(), p, c.ID)
			if e != nil {
				fmt.Fprint(w, `<p class="text-danger">Review history unavailable</p>`)
			} else {
				fmt.Fprint(w, `<details class="mt-3"><summary>Decision history</summary><ol>`)
				for _, d := range history {
					fmt.Fprintf(w, `<li>%s — %s; observation%d; conceding team%d<br>%s</li>`, escapeHTML(d.DecidedAt), escapeHTML(d.Action), d.ObservationID, d.ConcedingTeamID, escapeHTML(d.Reason))
				}
				fmt.Fprint(w, `</ol></details>`)
			}
			fmt.Fprint(w, `</div></article>`)
		}
	}
}

type juniorConcessionOption struct {
	ID    int64
	Label string
}

func juniorConcessionSelect(w http.ResponseWriter, name, label string, selectedID int64, options []juniorConcessionOption) {
	fmt.Fprintf(w, `<div class="col-md-2"><label class="form-label" for="filter-%s">%s</label><select class="form-select" id="filter-%s" name="%s"><option value="">All</option>`, name, escapeHTML(label), name, name)
	for _, x := range options {
		selected := ""
		if x.ID == selectedID {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, x.ID, selected, escapeHTML(x.Label))
	}
	fmt.Fprint(w, `</select></div>`)
}
func (s *Server) juniorConcessionFilterOptions(r *http.Request, p junior.Principal) ([]juniorConcessionOption, []juniorConcessionOption, []juniorConcessionOption, error) {
	read := func(query string, args ...any) ([]juniorConcessionOption, error) {
		rows, err := s.DB.Query(r.Context(), query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []juniorConcessionOption{}
		for rows.Next() {
			var x juniorConcessionOption
			if err = rows.Scan(&x.ID, &x.Label); err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, rows.Err()
	}
	seasons, err := read(`SELECT s.id,s.name FROM junior_seasons s WHERE ($1::int=0 OR EXISTS(SELECT 1 FROM junior_club_seasons cs WHERE cs.season_id=s.id AND cs.club_id=$1)) ORDER BY s.start_date DESC,s.id DESC`, p.ClubID)
	if err != nil {
		return nil, nil, nil, err
	}
	cs, err := junior.NewService(s.DB).ListCompetitions(r.Context(), p, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	competitions := []juniorConcessionOption{}
	for _, c := range cs {
		competitions = append(competitions, juniorConcessionOption{c.ID, c.Name + " · " + juniorCompetitionAgeDivision(c)})
	}
	clubs, err := read(`SELECT DISTINCT c.id::bigint,c.name FROM clubs c JOIN junior_club_seasons cs ON cs.club_id=c.id WHERE ($1::int=0 OR c.id=$1) ORDER BY c.name,c.id::bigint`, p.ClubID)
	return seasons, competitions, clubs, err
}
