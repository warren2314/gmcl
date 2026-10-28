package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func juniorGroundSelect(w http.ResponseWriter, name string, selected int64, grounds []junior.Ground) {
	fmt.Fprintf(w, `<select class="form-select" name="%s" aria-label="Saved ground or neutral venue" required><option value="">Choose saved ground</option>`, escapeHTML(name))
	for _, g := range grounds {
		if g.Archived && g.ID != selected {
			continue
		}
		sel := ""
		if g.ID == selected {
			sel = " selected"
		}
		label := g.Name + " · " + g.Postcode + " · " + g.Address
		if g.Archived {
			label += " (archived - choose another venue)"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, g.ID, sel, escapeHTML(label))
	}
	fmt.Fprint(w, `</select>`)
}
func (s *Server) handleJuniorGrounds() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		svc := junior.NewService(s.DB)
		if p.AdminID < 1 || p.ReadOnly {
			juniorHTTPError(w, r, junior.ErrForbidden)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			var err error
			var id int64
			switch r.FormValue("action") {
			case "link":
				err = svc.LinkPublishedGround(r.Context(), p, juniorFixtureFormID(r, "publication_id"), juniorFixtureFormID(r, "fixture_id"), juniorFixtureFormID(r, "ground_id"))
			case "save_ground":
				g := junior.Ground{ID: juniorFixtureFormID(r, "ground_id"), Revision: juniorFixtureFormID(r, "revision"), Name: r.FormValue("name"), Address: r.FormValue("address"), Postcode: r.FormValue("postcode"), Source: r.FormValue("source"), Archived: r.FormValue("archived") == "1", RulesConfirmed: r.FormValue("confirm") == "1"}
				for i := 0; i < 8; i++ {
					layout := junior.GroundLayout{}
					for _, age := range junior.GroundAges {
						raw := r.FormValue(fmt.Sprintf("layout_%d_%s", i, age))
						n := 0
						if raw != "" {
							var e error
							n, e = strconv.Atoi(raw)
							if e != nil || n < 0 || n > 10 {
								juniorHTTPError(w, r, junior.ErrValidation)
								return
							}
						}
						if n > 0 {
							layout[age] = n
						}
					}
					if len(layout) > 0 {
						g.Layouts = append(g.Layouts, layout)
					}
				}
				id, err = svc.SaveGround(r.Context(), p, g)
			default:
				err = junior.ErrValidation
			}
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			path := fmt.Sprintf("/admin/juniors/grounds?success=%s", url.QueryEscape("Ground information saved. Review the refreshed clash warnings below."))
			if id > 0 {
				path += fmt.Sprintf("&edit=%d", id)
			}
			if season := juniorFixtureFormID(r, "season_id"); season > 0 {
				path += fmt.Sprintf("&season_id=%d", season)
			}
			http.Redirect(w, r, path, http.StatusSeeOther)
			return
		}
		grounds, err := svc.Grounds(r.Context(), p)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		bookings, issues, err := svc.GroundBookings(r.Context(), p, juniorQueryID(r, "season_id"))
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasons, err := svc.ListSeasons(r.Context(), p)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		edit := junior.Ground{}
		if id := juniorQueryID(r, "edit"); id > 0 {
			found := false
			for _, g := range grounds {
				if g.ID == id {
					edit = g
					found = true
				}
			}
			if !found {
				juniorHTTPError(w, r, junior.ErrNotFound)
				return
			}
		}
		s.juniorPageStart(w, r, "Grounds and clash warnings")
		defer juniorPageEnd(w)
		fmt.Fprint(w, `<p>Choose saved physical venues for fixtures, including neutral grounds. Postcodes and addresses help identify the right place; the saved ground ID connects its bookings even when names change. Postcode format checks are not address verification.</p><p>Each layout is an alternative use of the same space. U9 capacity, senior fixtures and outside bookings need separate checks. Warnings appear here and on competition drafts; no clash emails are sent.</p>`)
		fmt.Fprintf(w, `<form method="GET" class="d-flex gap-2 mb-3"><label>Find ground<input class="form-control" name="q" value="%s" placeholder="Name, postcode or address"></label><label>Booking season<select class="form-select" name="season_id"><option value="">All junior seasons</option>`, escapeHTML(r.URL.Query().Get("q")))
		for _, season := range seasons {
			sel := ""
			if season.ID == juniorQueryID(r, "season_id") {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, season.ID, sel, escapeHTML(season.Name))
		}
		fmt.Fprint(w, `</select></label><button class="btn btn-outline-primary">Find / refresh warnings</button></form>`)
		fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Clash warnings</h2>`)
		if len(issues) == 0 {
			fmt.Fprint(w, `<p>No saved-venue clashes detected in the recorded junior bookings. This does not confirm outside bookings or U9 coverage.</p>`)
		}
		for _, issue := range issues {
			class := "warning"
			if issue.Blocking {
				class = "danger"
			}
			fmt.Fprintf(w, `<div class="alert alert-%s">%s</div>`, class, escapeHTML(issue.Message))
		}
		fmt.Fprint(w, `</div></section>`)
		fmt.Fprint(w, `<h2 class="h4">Saved grounds</h2><div class="table-responsive"><table class="table"><thead><tr><th>Ground</th><th>Address / postcode</th><th>Allowed simultaneous layouts</th><th>Verification / source</th></tr></thead><tbody>`)
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		for _, g := range grounds {
			if query != "" && !strings.Contains(strings.ToLower(g.Name+" "+g.Address+" "+g.Postcode), query) {
				continue
			}
			status := ""
			if g.Archived {
				status = " (archived)"
			}
			fmt.Fprintf(w, `<tr><td><a href="/admin/juniors/grounds?edit=%d">%s</a>%s</td><td>%s<br><strong>%s</strong></td><td>%s</td><td>%s</td></tr>`, g.ID, escapeHTML(g.Name), status, escapeHTML(g.Address), escapeHTML(g.Postcode), escapeHTML(junior.GroundLayoutsText(g.Layouts)), escapeHTML(g.Source))
		}
		fmt.Fprint(w, `</tbody></table></div>`)
		fmt.Fprint(w, `<details class="card mb-4" open><summary class="card-header">Add / edit saved ground</summary><div class="card-body"><form method="POST">`)
		juniorFixtureCSRF(w, r)
		fmt.Fprintf(w, `<input type="hidden" name="action" value="save_ground"><input type="hidden" name="ground_id" value="%d"><input type="hidden" name="revision" value="%d"><div class="row g-3"><label class="col-md-6">Ground name<input class="form-control" name="name" value="%s" maxlength="200" required></label><label class="col-md-6">Postcode<input class="form-control" name="postcode" value="%s" maxlength="10" required></label><label class="col-12">Street address / site description<input class="form-control" name="address" value="%s" maxlength="500" required></label><label class="col-12">Verification / source note<input class="form-control" name="source" value="%s" maxlength="500" required placeholder="Club confirmed address and layouts on ..."></label></div><h3 class="h5 mt-3">Permitted layouts</h3><p>Each row is ONE possible simultaneous arrangement, not an extra set of pitches. For example: row 1 = two U11 games; row 2 = one U15 game. That does not allow a U11 and a U15 together unless you add a separately verified mixed row. Zero means that age is absent from that layout.</p><div class="table-responsive"><table class="table"><thead><tr><th>Alternative</th>`, edit.ID, edit.Revision, escapeHTML(edit.Name), escapeHTML(edit.Postcode), escapeHTML(edit.Address), escapeHTML(edit.Source))
		for _, age := range junior.GroundAges {
			fmt.Fprintf(w, `<th>%s games</th>`, age)
		}
		fmt.Fprint(w, `</tr></thead><tbody>`)
		for i := 0; i < 8; i++ {
			layout := junior.GroundLayout{}
			if i < len(edit.Layouts) {
				layout = edit.Layouts[i]
			}
			fmt.Fprintf(w, `<tr><td>%d</td>`, i+1)
			for _, age := range junior.GroundAges {
				fmt.Fprintf(w, `<td><label class="visually-hidden">Layout %d %s games</label><input aria-label="Layout %d %s games" class="form-control" type="number" name="layout_%d_%s" min="0" max="10" value="%d"></td>`, i+1, age, i+1, age, i, age, layout[age])
			}
			fmt.Fprint(w, `</tr>`)
		}
		checked := ""
		if edit.Archived {
			checked = " checked"
		}
		fmt.Fprintf(w, `</tbody></table></div><label class="form-check"><input class="form-check-input" type="checkbox" name="archived" value="1"%s>Archive this venue (retain booking history)</label><label class="form-check my-2"><input class="form-check-input" type="checkbox" name="confirm" value="1" required>I have checked this address and the physical layouts, including mixed-age use.</label><button class="btn btn-primary">Save ground and layouts</button> <a href="/admin/juniors/grounds">New ground / clear editor</a></form></div></details>`, checked)
		fmt.Fprint(w, `<h2 class="h4">Recorded bookings and legacy venue links</h2><p>Published and tentative draft bookings are both considered. The same fixture appearing in both is counted once where they overlap. Older published venue labels can be linked without rewriting their historical publication.</p><div class="table-responsive"><table class="table"><thead><tr><th>Date / time</th><th>Fixture / competition</th><th>Venue</th><th>Status / action</th></tr></thead><tbody>`)
		for _, f := range bookings {
			fmt.Fprintf(w, `<tr><td>%s %s<br>%d minutes</td><td><a href="/admin/juniors/competitions/%d">%s · %s</a><br>%s %s v %s %s · fixture %d</td><td>%s<br>%s</td><td>%s`, escapeHTML(f.MatchDate), escapeHTML(f.StartTime), f.DurationMinutes, f.CompetitionID, escapeHTML(f.CompetitionName), escapeHTML(f.AgeGroup), escapeHTML(f.HomeClubName), escapeHTML(f.HomeTeamName), escapeHTML(f.AwayClubName), escapeHTML(f.AwayTeamName), f.ID, escapeHTML(f.GroundName), escapeHTML(f.GroundPostcode), escapeHTML(f.BookingState))
			if f.GroundID == 0 {
				if f.PublicationID > 0 {
					fmt.Fprint(w, `<form method="POST">`)
					juniorFixtureCSRF(w, r)
					fmt.Fprintf(w, `<input type="hidden" name="action" value="link"><input type="hidden" name="publication_id" value="%d"><input type="hidden" name="fixture_id" value="%d">`, f.PublicationID, f.ID)
					juniorGroundSelect(w, "ground_id", 0, grounds)
					fmt.Fprint(w, `<button class="btn btn-sm btn-outline-primary">Link historical venue</button></form>`)
				} else {
					fmt.Fprint(w, `<strong>Unlinked venue - edit the draft and choose a saved ground.</strong>`)
				}
			}
			fmt.Fprint(w, `</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
	}
}
