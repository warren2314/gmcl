package httpserver

import (
	"fmt"
	"net/http"
	"strconv"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
)

func (s *Server) handleJuniorAdminHome() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		seasons, seasonID, err := s.juniorRecordSeason(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		svc := junior.NewService(s.DB)
		d, err := svc.Dashboard(r.Context(), p, seasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		results, err := svc.Search(r.Context(), p, seasonID, r.URL.Query().Get("q"))
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Junior Home")
		fmt.Fprint(w, `<p class="text-muted">Your junior season, club records and next actions in one place. Select a season to keep every total in context.</p>`)
		writeJuniorRecordSeasonSelector(w, r, seasons, seasonID)
		portalState := "Closed for rollout"
		if junior.PortalEnabled() {
			portalState = "Open"
		}
		emailState := "Disabled"
		if junior.OutboundEmailConfigured() {
			emailState = "Configured — check delivery history"
		}
		fmt.Fprintf(w, `<div class="alert alert-light border"><strong>Club portal:</strong> %s · <strong>Junior email:</strong> %s · <strong>Shared inbox:</strong> connection being prepared separately.</div>`, portalState, emailState)
		if seasonID == 0 {
			fmt.Fprint(w, `<p><a class="btn btn-primary" href="/admin/juniors/clubs">Configure the first junior season</a></p>`)
		}
		fmt.Fprint(w, `<section aria-labelledby="junior-attention"><h2 id="junior-attention" class="h4">Needs attention</h2><div class="row g-3 mb-4">`)
		cards := []struct {
			label, detail, path string
			count               int64
		}{
			{"Outstanding forms", "Assigned or returned; overdue is included", "/forms?status=outstanding", d.OutstandingForms},
			{"Overdue forms", "Deadline has passed; response still needed", "/forms?status=overdue", d.OverdueForms},
			{"Responses to review", "Submitted and waiting for staff review", "/forms?status=review", d.ReviewForms},
			{"Concessions to review", "New evidence, information requested or source changed", "/concessions?queue=review", d.ConcessionReview},
			{"Fixture drafts", "No publication or changes since publication", "/competitions?queue=draft", d.UnpublishedCompetitions},
			{"Message problems", "Missing recipients or failed delivery attempts", "/messages?queue=problems", d.MessageProblems},
		}
		for _, c := range cards {
			sep := "?"
			for _, ch := range c.path {
				if ch == '?' {
					sep = "&"
					break
				}
			}
			href := "/admin/juniors" + c.path
			if seasonID > 0 {
				href += sep + "season_id=" + strconv.FormatInt(seasonID, 10)
			}
			fmt.Fprintf(w, `<div class="col-md-6 col-lg-4"><a class="card h-100 text-decoration-none" href="%s"><div class="card-body"><h3 class="h6">%s</h3><div class="display-6">%d</div><p class="text-muted small mb-0">%s</p></div></a></div>`, escapeHTML(href), c.label, c.count, c.detail)
		}
		fmt.Fprint(w, `</div></section>`)
		clubListURL := "/admin/juniors/clubs"
		if seasonID > 0 {
			clubListURL += fmt.Sprintf("?season_id=%d", seasonID)
		}
		fmt.Fprintf(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Season readiness</h2><p><strong>%d</strong> participating clubs · <strong>%d</strong> without a current Junior Lead contact · <strong>%d</strong> without a current Safeguarding Officer contact · <strong>%d</strong> confirmed concessions.</p><p class="small text-muted">Contact roles describe responsibilities. Portal access is verified and managed separately on each club record. Confirmed concession totals retain decisions while new evidence awaits review.</p><a class="btn btn-outline-primary" href="%s">Review clubs and contacts</a> <a class="btn btn-outline-secondary" href="/admin/juniors/roster">Import a reviewed roster</a>`, d.ActiveClubs, d.MissingLead, d.MissingSafeguarding, d.ConfirmedConcessions, clubListURL)
		if s.effectiveAdminRole(r.Context(), p.AdminID) == "super_admin" {
			fmt.Fprint(w, ` <a class="btn btn-outline-secondary" href="/admin/juniors/staff-access">Staff access</a>`)
		}
		fmt.Fprint(w, `</div></section>`)
		fmt.Fprint(w, `<section class="card"><div class="card-body"><h2 class="h4">Find a junior record</h2><form method="GET" class="d-flex gap-2">`)
		if seasonID > 0 {
			fmt.Fprintf(w, `<input type="hidden" name="season_id" value="%d">`, seasonID)
		}
		fmt.Fprintf(w, `<label class="visually-hidden" for="junior-search">Club, contact, email, team, fixture, competition or form</label><input id="junior-search" class="form-control" name="q" value="%s" minlength="2" maxlength="100" placeholder="Club, contact, email, team, fixture, competition or form"><button class="btn btn-primary">Search</button></form>`, escapeHTML(r.URL.Query().Get("q")))
		if len(r.URL.Query().Get("q")) >= 2 {
			fmt.Fprint(w, `<ul class="list-group list-group-flush mt-3">`)
			for _, item := range results {
				fmt.Fprintf(w, `<li class="list-group-item"><span class="badge text-bg-secondary me-2">%s</span><a href="%s">%s</a><span class="text-muted ms-2">%s</span></li>`, escapeHTML(item.Kind), escapeHTML(item.URL), escapeHTML(item.Label), escapeHTML(item.Detail))
			}
			if len(results) == 0 {
				fmt.Fprint(w, `<li class="list-group-item">No matching records in this season.</li>`)
			}
			fmt.Fprint(w, `</ul><p class="small text-muted mt-2">Up to 50 results. Narrow your search if needed.</p>`)
		}
		fmt.Fprint(w, `</div></section>`)
		juniorPageEnd(w)
	}
}

func (s *Server) handleJuniorClubProfile() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		clubID := p.ClubID
		if clubID == 0 {
			clubID = juniorAccessClubID(r)
		}
		if clubID <= 0 {
			juniorHTTPError(w, r, junior.ErrNotFound)
			return
		}
		seasons, seasonID, err := s.juniorRecordSeason(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		svc := junior.NewService(s.DB)
		profile, err := svc.ClubProfile(r.Context(), p, clubID, seasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		var access []junior.LeadAccess
		if p.AdminID > 0 && !p.ReadOnly {
			access, err = s.juniorAccessService().LeadAccess(r.Context(), p, clubID)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
		}
		forms, err := svc.ListForms(r.Context(), p, seasonID)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, profile.Club.Name+" · Juniors")
		writeJuniorRecordSeasonSelector(w, r, seasons, seasonID)
		if p.AdminID > 0 && !p.ReadOnly {
			previewURL := fmt.Sprintf("/admin/juniors/clubs/%d/preview", clubID)
			messageURL := fmt.Sprintf("/admin/juniors/messages?club_id=%d", clubID)
			if seasonID > 0 {
				previewURL += fmt.Sprintf("?season_id=%d", seasonID)
				messageURL += fmt.Sprintf("&season_id=%d", seasonID)
			}
			fmt.Fprintf(w, `<p><a class="btn btn-outline-primary" href="%s">View as Club</a> <a class="btn btn-outline-secondary" href="%s">Club message history</a></p>`, previewURL, messageURL)
		}
		if p.AdminID == 0 || p.ReadOnly {
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Official forms</h2><p>Open Official forms above to save a draft, submit a response or see its receipt.</p><ul>`)
			for _, f := range forms {
				fmt.Fprintf(w, `<li>%s — due %s · %d outstanding · %d awaiting review · %d completed</li>`, escapeHTML(f.Title), f.DueDate.Format("2 Jan 2006"), f.Outstanding, f.RequiresReview, f.Completed)
			}
			if len(forms) == 0 {
				fmt.Fprint(w, `<li>No official forms assigned for this season.</li>`)
			}
			fmt.Fprint(w, `</ul></div></section>`)
		}
		s.JuniorClubRecordSection(w, r, profile)
		if p.AdminID > 0 && !p.ReadOnly {
			csrf := escapeHTML(middleware.CSRFToken(r))
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-body"><h2 class="h4">Verified Junior Lead access</h2><p>Only the invited email can accept access. A replacement invitation keeps the current lead active until acceptance, then revokes the previous lead's sessions.</p><ul class="list-group mb-3">`)
			for _, a := range access {
				field, id := "invite_id", a.InviteID
				if a.MembershipID > 0 {
					field, id = "membership_id", a.MembershipID
				}
				fmt.Fprintf(w, `<li class="list-group-item d-flex flex-wrap gap-3 justify-content-between"><span>%s · %s</span><form method="POST" action="/admin/juniors/clubs/%d/revoke"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="%s" value="%d"><button class="btn btn-sm btn-outline-danger">Revoke access / invitation</button></form></li>`, escapeHTML(a.Email), escapeHTML(a.Status), clubID, csrf, field, id)
			}
			if len(access) == 0 {
				fmt.Fprint(w, `<li class="list-group-item">No active lead or outstanding invitation.</li>`)
			}
			fmt.Fprintf(w, `</ul><form method="POST" action="/admin/juniors/clubs/%d/invite" class="d-flex flex-wrap gap-2 align-items-end"><input type="hidden" name="csrf_token" value="%s"><label>Junior Lead email<input class="form-control" type="email" name="email" maxlength="254" required></label><button class="btn btn-primary">Send verified invitation</button></form><p class="small text-muted mt-3">Requires the club portal and junior email to be explicitly enabled. An adult contact record alone never grants access.</p></div></section>`, clubID, csrf)
		}
		juniorPageEnd(w)
	}
}
