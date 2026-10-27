package httpserver

import (
	"cricket-ground-feedback/internal/junior"
	"fmt"
	"net/http"
	"time"
)

func writeJuniorClubWorkspace(w http.ResponseWriter, r *http.Request, p junior.Principal, clubID int32, seasonID int64, data junior.ClubWorkspace) {
	staff := p.AdminID > 0 && !p.ReadOnly
	seasonSuffix := ""
	if seasonID > 0 {
		seasonSuffix = fmt.Sprintf("&season_id=%d", seasonID)
	}
	base := "/junior"
	if staff {
		base = "/admin/juniors"
	} else if p.ReadOnly {
		base = fmt.Sprintf("/admin/juniors/clubs/%d/preview", clubID)
	}
	path := func(view string) string {
		href := base + "/" + view
		if p.ReadOnly {
			href = base + "?view=" + view
		}
		return juniorSeasonURL(r, href)
	}
	fmt.Fprint(w, `<nav class="d-flex flex-wrap gap-2 mb-4" aria-label="Club record sections"><a class="btn btn-outline-primary" href="#club-forms">Forms</a><a class="btn btn-outline-primary" href="#club-competitions">Competitions</a><a class="btn btn-outline-primary" href="#club-fixtures">Fixtures</a><a class="btn btn-outline-primary" href="#club-concessions">Concessions</a><a class="btn btn-outline-primary" href="#club-messages">Communications</a><a class="btn btn-outline-secondary" href="#junior-contacts">Contacts</a></nav>`)
	fmt.Fprint(w, `<section class="card mb-4" id="club-forms"><div class="card-body"><h2 class="h4">Official forms</h2><ul class="list-group list-group-flush">`)
	for _, f := range data.Forms {
		href := path("forms")
		if staff {
			href = fmt.Sprintf("/admin/juniors/forms/assignments/%d", f.ID)
		} else if !p.ReadOnly {
			href = fmt.Sprintf("/junior/forms/%d", f.ID)
		}
		fmt.Fprintf(w, `<li class="list-group-item"><a href="%s">%s</a> · due %s <span class="badge text-bg-light">%s</span></li>`, escapeHTML(href), escapeHTML(f.Title), f.DueDate.Format("2 Jan 2006"), escapeHTML(junior.FormAssignmentStatus(f, time.Now())))
	}
	if len(data.Forms) == 0 {
		fmt.Fprint(w, `<li class="list-group-item text-muted">No forms assigned to this club in this season.</li>`)
	}
	fmt.Fprint(w, `</ul></div></section><section class="card mb-4" id="club-competitions"><div class="card-body"><h2 class="h4">Competitions and entries</h2><ul class="list-group list-group-flush">`)
	for _, c := range data.Competitions {
		href := path("fixtures")
		if staff {
			href = fmt.Sprintf("/admin/juniors/competitions/%d", c.ID)
		}
		status := "Draft fixtures"
		if c.PublicationID > 0 {
			status = "Published fixtures"
			if c.PublicationRevision != c.Revision {
				status = "Published fixtures · draft changes awaiting publication"
			}
		}
		fmt.Fprintf(w, `<li class="list-group-item"><a href="%s">%s</a> · %s %s · %s</li>`, escapeHTML(href), escapeHTML(c.Name), escapeHTML(c.AgeGroup), escapeHTML(c.Division), status)
	}
	if len(data.Competitions) == 0 {
		fmt.Fprint(w, `<li class="list-group-item text-muted">No competition entries for this club in this season.</li>`)
	}
	fmt.Fprint(w, `</ul></div></section><section class="card mb-4" id="club-fixtures"><div class="card-body"><h2 class="h4">Official club fixtures</h2><p class="small text-muted">Current published fixtures. Draft changes do not replace these until published.</p><ul class="list-group list-group-flush">`)
	for _, f := range data.Fixtures {
		fmt.Fprintf(w, `<li class="list-group-item">%s %s · %s %s v %s %s · %s</li>`, escapeHTML(f.MatchDate), escapeHTML(f.StartTime), escapeHTML(f.HomeClubName), escapeHTML(f.HomeTeamName), escapeHTML(f.AwayClubName), escapeHTML(f.AwayTeamName), escapeHTML(f.GroundName))
	}
	if len(data.Fixtures) == 0 {
		fmt.Fprint(w, `<li class="list-group-item text-muted">No published fixtures for this club in this season.</li>`)
	}
	href := path("fixtures")
	if staff {
		href = fmt.Sprintf("/admin/juniors/fixtures?club_id=%d%s", clubID, seasonSuffix)
	}
	fmt.Fprintf(w, `</ul><a class="btn btn-outline-secondary mt-3" href="%s">Open fixtures and export</a></div></section><section class="card mb-4" id="club-concessions"><div class="card-body"><h2 class="h4">Concession history</h2><p class="small text-muted">Review candidates and confirmed decisions remain distinct. Each record identifies the fixture and conceding side.</p><ul class="list-group list-group-flush">`, escapeHTML(href))
	for _, c := range data.Concessions {
		href = path("concessions")
		if staff {
			href = fmt.Sprintf("/admin/juniors/concessions?candidate_id=%d%s", c.ID, seasonSuffix)
		}
		label := map[string]string{"awaiting_review": "Awaiting review", "information_requested": "Information requested", "source_changed": "New source evidence", "dismissed": "Dismissed", "confirmed": "Confirmed"}[c.State]
		if label == "" {
			label = c.State
		}
		if c.Confirmed {
			club, team := junior.ConcessionConcedingNames(c)
			label = "Confirmed against " + club + " " + team
		}
		fmt.Fprintf(w, `<li class="list-group-item"><a href="%s">%s · %s %s v %s %s</a> <span class="badge text-bg-light">%s</span></li>`, escapeHTML(href), escapeHTML(c.MatchDate), escapeHTML(c.HomeClubName), escapeHTML(c.HomeTeamName), escapeHTML(c.AwayClubName), escapeHTML(c.AwayTeamName), escapeHTML(label))
	}
	if len(data.Concessions) == 0 {
		fmt.Fprint(w, `<li class="list-group-item text-muted">No concession records for this club in this season.</li>`)
	}
	fmt.Fprint(w, `</ul></div></section><section class="card mb-4" id="club-messages"><div class="card-body"><h2 class="h4">Communication history</h2>`)
	renderJuniorMessages(w, r, data.Messages, juniorMessageView{ClubID: clubID})
	href = path("messages")
	if staff {
		href = fmt.Sprintf("/admin/juniors/messages?club_id=%d%s", clubID, seasonSuffix)
	}
	fmt.Fprintf(w, `<a class="btn btn-outline-secondary" href="%s">Open club correspondence</a></div></section>`, escapeHTML(href))
}
