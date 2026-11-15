package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func juniorEntryGrading(w http.ResponseWriter, e junior.CompetitionEntry) {
	fmt.Fprintf(w, `<span class="d-block small" style="white-space:pre-wrap"><strong>Requested grade:</strong> %s · <strong>Zone:</strong> %s<br>%s</span>`, escapeHTML(e.RequestedGrade), escapeHTML(e.Zone), escapeHTML(e.GradingNotes))
	if e.SourceAssignmentID > 0 {
		fmt.Fprintf(w, `<a class="small" href="/admin/juniors/forms/assignments/%d">Original entry · submission %d</a> `, e.SourceAssignmentID, e.SourceSubmissionID)
	}
}
func juniorCalendarLink(date string, ground, competition int64) string {
	if competition < 0 {
		competition = 0
	}
	return fmt.Sprintf("/admin/juniors/ground-calendar?date=%s&ground_id=%d&competition_id=%d", url.QueryEscape(date), ground, competition)
}
func juniorRenderBookingTable(w http.ResponseWriter, fixtures []junior.Fixture) {
	if len(fixtures) == 0 {
		fmt.Fprint(w, `<p>No recorded bookings in this selection.</p>`)
		return
	}
	fmt.Fprint(w, `<div class="table-responsive"><table class="table"><thead><tr><th>Date / start / duration</th><th>Fixture / competition / division</th><th>Ground</th><th>Status / investigate</th></tr></thead><tbody>`)
	for _, f := range fixtures {
		fmt.Fprintf(w, `<tr><td>%s · %s<br>%d minutes</td><td>`, escapeHTML(f.MatchDate), escapeHTML(f.StartTime), f.DurationMinutes)
		if f.BookingState == "external" {
			fmt.Fprintf(w, `%s<br>Whole-ground reservation<br><small>Source: %s</small>`, escapeHTML(f.CompetitionName), escapeHTML(f.ExternalSource))
		} else {
			fmt.Fprintf(w, `%s %s v %s %s<br><a href="/admin/juniors/competitions/%d">%s · %s · %s</a> · fixture %d`, escapeHTML(f.HomeClubName), escapeHTML(f.HomeTeamName), escapeHTML(f.AwayClubName), escapeHTML(f.AwayTeamName), f.CompetitionID, escapeHTML(f.CompetitionName), escapeHTML(f.AgeGroup), escapeHTML(f.Division), f.ID)
		}
		fmt.Fprintf(w, `</td><td>%s · %s</td><td>%s<br><a href="%s">Ground and division on this day</a></td></tr>`, escapeHTML(f.GroundName), escapeHTML(f.GroundPostcode), escapeHTML(f.BookingState), escapeHTML(juniorCalendarLink(f.MatchDate, f.GroundID, f.CompetitionID)))
	}
	fmt.Fprint(w, `</tbody></table></div>`)
}
func juniorRenderClashAlerts(w http.ResponseWriter, issues []junior.FixtureIssue, bookings []junior.Fixture) {
	juniorRenderBlockingSummary(w, issues)
	for _, i := range issues {
		class := "warning"
		if i.Blocking {
			class = "danger"
		}
		fmt.Fprintf(w, `<div class="alert alert-%s">%s</div>`, class, escapeHTML(i.Message))
	}
	seen := map[string]bool{}
	for _, f := range bookings {
		if f.GroundID < 1 {
			continue
		}
		key := fmt.Sprintf("%d/%s", f.GroundID, f.MatchDate)
		if seen[key] {
			continue
		}
		seen[key] = true
		day := junior.GroundDay(bookings, f.MatchDate, f.GroundID)
		clash := false
		for _, i := range junior.CheckGroundLayouts(day, nil) {
			if i.Blocking {
				clash = true
			}
		}
		if !clash {
			continue
		}
		fmt.Fprintf(w, `<details class="border border-danger rounded p-3 mb-3"><summary><strong>%s · %s — inspect ground and divisions</strong></summary>`, escapeHTML(f.GroundName), escapeHTML(f.MatchDate))
		juniorRenderBookingTable(w, day)
		fmt.Fprint(w, `</details>`)
	}
}
func (s *Server) handleJuniorGroundCalendar() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		if p.AdminID < 1 || p.ReadOnly {
			juniorHTTPError(w, r, junior.ErrForbidden)
			return
		}
		svc := junior.NewService(s.DB)
		date := r.URL.Query().Get("date")
		if date == "" {
			date = time.Now().In(juniorLondon()).Format("2006-01-02")
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				juniorHTTPError(w, r, junior.ErrValidation)
				return
			}
			var err error
			if r.FormValue("action") == "cancel_booking" {
				err = svc.CancelExternalGroundBooking(r.Context(), p, juniorFixtureFormID(r, "booking_id"))
			} else if r.FormValue("action") == "external_booking" {
				_, err = svc.SaveExternalGroundBooking(r.Context(), p, junior.ExternalGroundBooking{GroundID: juniorFixtureFormID(r, "ground_id"), Title: r.FormValue("title"), Category: r.FormValue("category"), MatchDate: r.FormValue("match_date"), StartTime: r.FormValue("start_time"), DurationMinutes: int(juniorFixtureFormID(r, "duration_minutes")), Source: r.FormValue("source")})
			} else {
				err = junior.ErrValidation
			}
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			http.Redirect(w, r, juniorCalendarLink(r.FormValue("match_date"), juniorFixtureFormID(r, "ground_id"), 0), http.StatusSeeOther)
			return
		}
		day, err := time.Parse("2006-01-02", date)
		if err != nil {
			juniorHTTPError(w, r, fmt.Errorf("%w: choose a valid calendar date", junior.ErrValidation))
			return
		}
		grounds, err := svc.Grounds(r.Context(), p)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		bookings, _, err := svc.GroundBookings(r.Context(), p, 0)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		competitions, err := svc.ListCompetitions(r.Context(), p, 0)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		groundID, competitionID := juniorQueryID(r, "ground_id"), juniorQueryID(r, "competition_id")
		s.juniorPageStart(w, r, "Ground calendar")
		defer juniorPageEnd(w)
		fmt.Fprint(w, `<p>Recorded junior drafts, official fixtures and women’s, senior and other whole-ground bookings. No recorded booking is not confirmation of availability: check with the club for commitments not yet entered. All times are local to the ground (Europe/London).</p>`)
		fmt.Fprintf(w, `<form method="GET" class="row g-2 mb-3"><label class="col-md-3">Date<input class="form-control" type="date" name="date" value="%s" required></label><label class="col-md-4">Find ground<input class="form-control" name="q" value="%s" placeholder="Name, postcode or address"></label><label class="col-md-4">Division<select class="form-select" name="competition_id"><option value="">All divisions</option>`, escapeHTML(date), escapeHTML(r.URL.Query().Get("q")))
		for _, c := range competitions {
			sel := ""
			if c.ID == competitionID {
				sel = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s · %s · %s</option>`, c.ID, sel, escapeHTML(c.Name), escapeHTML(c.AgeGroup), escapeHTML(c.Division))
		}
		fmt.Fprintf(w, `</select></label><input type="hidden" name="ground_id" value="%d"><div><button class="btn btn-primary">Show date</button></div></form>`, groundID)
		fmt.Fprintf(w, `<p><a href="%s">Previous day</a> · <strong>%s</strong> · <a href="%s">Next day</a></p>`, escapeHTML(juniorCalendarLink(day.AddDate(0, 0, -1).Format("2006-01-02"), groundID, competitionID)), escapeHTML(day.Format("Monday 2 January 2006")), escapeHTML(juniorCalendarLink(day.AddDate(0, 0, 1).Format("2006-01-02"), groundID, competitionID)))
		dayBookings := []junior.Fixture{}
		for _, f := range bookings {
			if f.MatchDate == date {
				dayBookings = append(dayBookings, f)
			}
		}
		juniorRenderClashAlerts(w, junior.CheckGroundLayouts(dayBookings, nil), dayBookings)
		fmt.Fprint(w, `<h2 class="h4">Ground use on this date</h2><div class="table-responsive"><table class="table"><thead><tr><th>Ground</th><th>Confirmed alternative layouts</th><th>Recorded use</th></tr></thead><tbody>`)
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		for _, g := range grounds {
			use := junior.GroundDay(bookings, date, g.ID)
			if g.Archived && len(use) == 0 {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(g.Name+" "+g.Address+" "+g.Postcode), query) {
				continue
			}
			fmt.Fprintf(w, `<tr><td><a href="%s">%s · %s</a><br>%s</td><td>%s</td><td>`, escapeHTML(juniorCalendarLink(date, g.ID, competitionID)), escapeHTML(g.Name), escapeHTML(g.Postcode), escapeHTML(g.Address), escapeHTML(junior.GroundLayoutsText(g.Layouts)))
			if len(use) == 0 {
				fmt.Fprint(w, `No recorded bookings`)
			} else {
				for _, f := range use {
					fmt.Fprintf(w, `<div><strong>%s</strong> · %s · %s</div>`, escapeHTML(f.StartTime), escapeHTML(f.CompetitionName), escapeHTML(f.BookingState))
				}
			}
			fmt.Fprint(w, `</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
		if groundID > 0 {
			fmt.Fprint(w, `<h2 class="h4">All bookings at the selected ground on this day</h2>`)
			juniorRenderBookingTable(w, junior.GroundDay(bookings, date, groundID))
		}
		if competitionID > 0 {
			fmt.Fprint(w, `<h2 class="h4">All fixtures in the selected division on this day (every ground)</h2>`)
			juniorRenderBookingTable(w, junior.DivisionDay(bookings, date, competitionID))
		}
		fmt.Fprint(w, `<h2 class="h4">All recorded fixtures and bookings on this day</h2>`)
		juniorRenderBookingTable(w, dayBookings)
		fmt.Fprint(w, `<details class="card p-3 mb-3"><summary>Record a women’s, senior or other ground booking</summary><p>This reserves the whole ground. Include setup/clearance time in the booking duration. To correct a booking, cancel it and record the replacement.</p><form method="POST" class="row g-2">`)
		juniorFixtureCSRF(w, r)
		fmt.Fprint(w, `<input type="hidden" name="action" value="external_booking"><label class="col-md-6">Ground`)
		juniorGroundSelect(w, "ground_id", groundID, grounds)
		fmt.Fprint(w, `</label><label class="col-md-6">Category<select class="form-select" name="category"><option value="women">Women</option><option value="senior">Senior</option><option value="other">Other</option></select></label><label class="col-md-6">Fixture / booking title<input class="form-control" name="title" maxlength="200" required></label>`)
		fmt.Fprintf(w, `<label class="col-md-3">Date<input class="form-control" type="date" name="match_date" value="%s" required></label><label class="col-md-3">Start<input class="form-control" type="time" name="start_time" required></label><label class="col-md-3">Duration in minutes<input class="form-control" type="number" min="1" max="1440" name="duration_minutes" required></label><label class="col-md-9">Confirmed source<input class="form-control" name="source" maxlength="500" required></label><div><button class="btn btn-primary">Save whole-ground booking</button></div></form></details>`, escapeHTML(date))
		for _, f := range dayBookings {
			if f.BookingState != "external" {
				continue
			}
			fmt.Fprint(w, `<form method="POST" class="mb-2">`)
			juniorFixtureCSRF(w, r)
			fmt.Fprintf(w, `<input type="hidden" name="action" value="cancel_booking"><input type="hidden" name="booking_id" value="%d"><input type="hidden" name="ground_id" value="%d"><input type="hidden" name="match_date" value="%s"><button class="btn btn-sm btn-outline-danger">Cancel booking: %s · %s</button></form>`, f.ID, f.GroundID, escapeHTML(f.MatchDate), escapeHTML(f.StartTime), escapeHTML(f.CompetitionName))
		}
	}
}

func juniorRenderBlockingSummary(w http.ResponseWriter, issues []junior.FixtureIssue) {
	blocking := 0
	title := "Fixture checks need attention"
	for _, i := range issues {
		if i.Blocking {
			blocking++
			if i.Code == "ground_clash" || i.Code == "team_double_booking" {
				title = "Fixture clashes need attention"
			}
		}
	}
	if blocking > 0 {
		fmt.Fprintf(w, `<div class="alert alert-danger border border-danger border-3" role="alert"><strong>%s — %d blocking warning(s).</strong> Resolve the warnings below before publishing. <a class="alert-link" href="/admin/juniors/grounds">Ground clash details</a>.</div>`, title, blocking)
	}
}
