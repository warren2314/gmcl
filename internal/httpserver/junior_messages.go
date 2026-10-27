package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/google/uuid"
)

func juniorMessageFilter(r *http.Request) (junior.AudienceFilter, error) {
	var filter junior.AudienceFilter
	parse := func(key string, bits int) (int64, error) {
		value := r.FormValue(key)
		if value == "" {
			return 0, nil
		}
		id, err := strconv.ParseInt(value, 10, bits)
		if err != nil || id < 0 {
			return 0, junior.ErrValidation
		}
		return id, nil
	}
	var err error
	if filter.SeasonID, err = parse("season_id", 64); err != nil {
		return filter, err
	}
	clubID, err := parse("club_id", 32)
	if err != nil {
		return filter, err
	}
	filter.ClubID = int32(clubID)
	if filter.CompetitionID, err = parse("competition_id", 64); err != nil {
		return filter, err
	}
	filter.Role, filter.AgeGroup = r.FormValue("role"), r.FormValue("age_group")
	filter.Selection = r.FormValue("audience_mode")
	for _, value := range r.Form["club_ids"] {
		id, e := strconv.ParseInt(value, 10, 32)
		if e != nil || id <= 0 {
			return filter, junior.ErrValidation
		}
		filter.ClubIDs = append(filter.ClubIDs, int32(id))
	}
	for _, value := range r.Form["contact_ids"] {
		id, e := strconv.ParseInt(value, 10, 64)
		if e != nil || id <= 0 {
			return filter, junior.ErrValidation
		}
		filter.ContactIDs = append(filter.ContactIDs, id)
	}
	return junior.NormaliseAudienceFilter(filter)
}

func (s *Server) handleAdminJuniorMessages() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter, err := juniorMessageFilter(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if filter.SeasonID == 0 {
			_, filter.SeasonID, err = s.juniorRecordSeason(r)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
		}
		s.renderAdminJuniorMessages(w, r, filter, nil)
	}
}

func (s *Server) renderAdminJuniorMessages(w http.ResponseWriter, r *http.Request, filter junior.AudienceFilter, preview *junior.AudiencePreview) {
	// Preview is rendered directly after POST; preserve its season in navigation.
	if filter.SeasonID > 0 {
		query := r.URL.Query()
		query.Set("season_id", strconv.FormatInt(filter.SeasonID, 10))
		copied := *r.URL
		copied.RawQuery = query.Encode()
		r = r.Clone(r.Context())
		r.URL = &copied
	}
	service := junior.NewService(s.DB)
	p := s.juniorPrincipal(r)
	problemsOnly := r.FormValue("queue") == "problems"
	messages, err := service.MessagesFiltered(r.Context(), p, junior.MessageFilter{ClubID: filter.ClubID, SeasonID: filter.SeasonID, ProblemsOnly: problemsOnly})
	if err != nil {
		juniorHTTPError(w, r, err)
		return
	}
	incoming, err := service.IncomingMessagesForClub(r.Context(), p, filter.ClubID)
	if err != nil {
		juniorHTTPError(w, r, err)
		return
	}
	seasons, err := service.ListSeasons(r.Context(), p)
	if err != nil {
		juniorHTTPError(w, r, err)
		return
	}
	clubs, err := service.ListClubs(r.Context(), p, filter.SeasonID)
	if err != nil {
		juniorHTTPError(w, r, err)
		return
	}
	competitions, err := service.ListCompetitions(r.Context(), p, filter.SeasonID)
	if err != nil {
		juniorHTTPError(w, r, err)
		return
	}
	directory, err := service.ContactDirectory(r.Context(), p, filter, "")
	if err != nil {
		juniorHTTPError(w, r, err)
		return
	}
	csrf := escapeHTML(middleware.CSRFToken(r))
	key := r.FormValue("key")
	if key == "" {
		key = "manual:" + uuid.NewString()
	}
	resolveID := r.FormValue("resolve_id")
	subject, body := r.FormValue("subject"), r.FormValue("body")
	s.juniorPageStart(w, r, "Junior messages")
	if problemsOnly {
		fmt.Fprint(w, `<div class="alert alert-warning">Showing messages with missing recipients or failed transport in the selected junior season. <a href="/admin/juniors/messages">Show all messages</a></div>`)
	}
	fmt.Fprint(w, `<p>Preview the current audience before queuing a message. Each email address receives one message, even when it covers several roles or clubs. Clubs see only correspondence addressed to their club.</p>`)
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("JUNIOR_OUTBOUND_EMAIL_ENABLED")), "true") {
		fmt.Fprint(w, `<div class="alert alert-info"><strong>Outbound junior email is disabled.</strong> Messages can be reviewed and queued. No junior message is sent until an operator configures and enables the transport.</div>`)
	} else if !junior.OutboundEmailConfigured() {
		fmt.Fprint(w, `<div class="alert alert-warning"><strong>Junior email settings are incomplete.</strong> Configure the junior sender and SMTP transport before sending. Existing senior mail settings alone do not activate junior correspondence.</div>`)
	}
	fmt.Fprint(w, `<section class="card mb-4"><div class="card-header">Compose or resolve missing recipients</div><form method="POST" action="/admin/juniors/messages" class="card-body">`)
	if problemsOnly {
		fmt.Fprint(w, `<input type="hidden" name="queue" value="problems">`)
	}
	fmt.Fprintf(w, `<input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="key" value="%s"><input type="hidden" name="resolve_id" value="%s"><div class="row g-3"><label class="col-md-3">Junior season<select class="form-select" name="season_id" required>`, csrf, escapeHTML(key), escapeHTML(resolveID))
	for _, season := range seasons {
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, season.ID, juniorMessageSelected(season.ID == filter.SeasonID), escapeHTML(season.Name))
	}
	fmt.Fprint(w, `</select></label><label class="col-md-3">Club<select class="form-select" name="club_id"><option value="0">All participating clubs</option>`)
	for _, club := range clubs {
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, juniorMessageSelected(club.ID == filter.ClubID), escapeHTML(club.Name))
	}
	fmt.Fprint(w, `</select></label><label class="col-md-3">Contact role<select class="form-select" name="role"><option value="">All official contacts</option>`)
	for _, role := range []string{"junior_lead", "coach", "safeguarding_officer"} {
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, role, juniorMessageSelected(role == filter.Role), escapeHTML(junior.RoleLabel(role)))
	}
	fmt.Fprint(w, `</select></label><label class="col-md-3">Age group<select class="form-select" name="age_group"><option value="">All ages</option>`)
	for _, age := range []string{"U9", "U11", "U13", "U15", "U18"} {
		fmt.Fprintf(w, `<option%s>%s</option>`, juniorMessageSelected(age == filter.AgeGroup), age)
	}
	fmt.Fprint(w, `</select></label><label class="col-md-6">Competition<select class="form-select" name="competition_id"><option value="0">All competitions</option>`)
	for _, competition := range competitions {
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, competition.ID, juniorMessageSelected(competition.ID == filter.CompetitionID), escapeHTML(competition.Name))
	}
	fmt.Fprint(w, `</select></label></div>`)
	writeJuniorAudienceSelection(w, filter, clubs, directory)
	if resolveID != "" {
		fmt.Fprintf(w, `<div class="alert alert-warning mt-3">Resolving recipients for message #%s. Its existing subject and body will stay unchanged. Select the intended audience and preview it before confirming.</div>`, escapeHTML(resolveID))
	} else {
		fmt.Fprintf(w, `<label class="form-label d-block mt-3">Subject<input class="form-control" name="subject" maxlength="250" value="%s" required></label><label class="form-label d-block">Message<textarea class="form-control" name="body" rows="7" maxlength="50000" required>%s</textarea></label>`, escapeHTML(subject), escapeHTML(body))
	}
	fmt.Fprint(w, `<button class="btn btn-outline-primary" name="operation" value="preview">Preview current audience</button>`)
	if preview != nil {
		addresses := map[string]bool{}
		for _, recipient := range preview.Recipients {
			addresses[recipient.Email] = true
		}
		fmt.Fprintf(w, `<div class="alert alert-light border mt-3"><strong>Audience preview: %d unique email addresses</strong><ul class="mb-0">`, len(addresses))
		for _, recipient := range preview.Recipients {
			clubName := fmt.Sprintf("Club #%d", recipient.ClubID)
			for _, club := range clubs {
				if club.ID == recipient.ClubID {
					clubName = club.Name
					break
				}
			}
			fmt.Fprintf(w, `<li>%s — %s &lt;%s&gt;</li>`, escapeHTML(clubName), escapeHTML(recipient.Name), escapeHTML(recipient.Email))
		}
		if len(preview.Recipients) == 0 {
			fmt.Fprint(w, `<li>No eligible email recipients. A queued message will be marked Needs recipients.</li>`)
		}
		fmt.Fprint(w, `</ul></div>`)
		operation, label := "queue", "Queue reviewed message"
		if resolveID != "" {
			operation, label = "resolve", "Confirm recipient snapshot"
		}
		fmt.Fprintf(w, `<input type="hidden" name="audience_hash" value="%s"><button class="btn btn-primary ms-2" name="operation" value="%s">%s</button>`, escapeHTML(preview.Hash), operation, label)
	}
	fmt.Fprint(w, `</form></section><section class="card mb-4"><div class="card-header">Outgoing message history — latest 100</div><div class="card-body">`)
	renderJuniorMessages(w, r, messages, juniorMessageView{Staff: true, ClubID: filter.ClubID})
	fmt.Fprint(w, `</div></section><section class="card mb-4"><div class="card-header">Incoming correspondence</div><div class="card-body"><div class="alert alert-info"><strong>Mailbox provider is not connected.</strong> Automatic inbox synchronisation awaits the provider and authorised mailbox settings. Records below were entered manually by staff.</div>`)
	fmt.Fprintf(w, `<details class="mb-3"><summary>Record incoming correspondence manually</summary><form method="POST" action="/admin/juniors/messages" class="mt-3"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="operation" value="record_incoming"><label class="form-label d-block">From email<input class="form-control" type="email" name="from" required></label><label class="form-label d-block">Club<select class="form-select" name="club_id"><option value="0">Unmatched — review required</option>`, csrf)
	for _, club := range clubs {
		fmt.Fprintf(w, `<option value="%d">%s</option>`, club.ID, escapeHTML(club.Name))
	}
	fmt.Fprint(w, `</select></label><label class="form-label d-block">Subject<input class="form-control" name="subject" maxlength="250" required></label><label class="form-label d-block">Message<textarea class="form-control" name="body" rows="5" maxlength="50000" required></textarea></label><button class="btn btn-outline-primary">Record manual correspondence</button></form></details>`)
	for _, item := range incoming {
		fmt.Fprintf(w, `<details class="border rounded p-3 mb-2"><summary>#%d — %s — %s</summary><p class="mt-2">From: %s · Club #%d · Manually recorded %s</p><pre class="text-wrap">%s</pre>`, item.ID, escapeHTML(item.Subject), escapeHTML(item.State), escapeHTML(item.From), item.ClubID, item.CreatedAt.Format("02 Jan 2006 15:04 MST"), escapeHTML(item.Body))
		fmt.Fprintf(w, `<form method="POST" action="/admin/juniors/messages" class="d-flex flex-wrap gap-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="operation" value="link_incoming"><input type="hidden" name="incoming_id" value="%d"><label>Link to club<select class="form-select" name="club_id" required>`, csrf, item.ID)
		for _, club := range clubs {
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, juniorMessageSelected(club.ID == item.ClubID), escapeHTML(club.Name))
		}
		fmt.Fprint(w, `</select></label><button class="btn btn-outline-secondary align-self-end">Record club link</button></form></details>`)
	}
	if len(incoming) == 0 {
		fmt.Fprint(w, `<p class="text-muted">No incoming correspondence has been manually recorded.</p>`)
	}
	fmt.Fprint(w, `</div></section>`)
	juniorPageEnd(w)
}

func juniorMessageSelected(selected bool) string {
	if selected {
		return " selected"
	}
	return ""
}

type juniorMessageView struct {
	Staff  bool
	ClubID int32
}

func renderJuniorMessages(w http.ResponseWriter, r *http.Request, messages []junior.MessageSummary, view juniorMessageView) {
	if len(messages) == 0 {
		fmt.Fprint(w, `<p class="text-muted">No messages recorded for this view.</p>`)
		return
	}
	for _, message := range messages {
		label := map[string]string{"needs_recipients": "Needs recipients", "queued": "Queued / awaiting transport", "failed": "Transport failed — review or retry", "accepted": "Accepted by mail transport"}[message.Status]
		fmt.Fprintf(w, `<details class="border rounded p-3 mb-3"><summary>#%d — %s <span class="badge text-bg-light">%s</span></summary><p class="text-muted mt-2">Recorded %s · %d recipient addresses · %d accepted · %d failed. Acceptance does not confirm delivery or reading.</p><pre class="text-wrap">%s</pre><ul>`, message.ID, escapeHTML(message.Subject), escapeHTML(label), message.CreatedAt.Format("02 Jan 2006 15:04 MST"), message.RecipientCount, message.AcceptedCount, message.FailedCount, escapeHTML(message.Body))
		for _, recipient := range message.Recipients {
			fmt.Fprintf(w, `<li>%s &lt;%s&gt;</li>`, escapeHTML(recipient.Name), escapeHTML(recipient.Email))
		}
		fmt.Fprint(w, `</ul>`)
		if view.Staff {
			if message.Status == "needs_recipients" {
				fmt.Fprintf(w, `<a class="btn btn-outline-primary" href="/admin/juniors/messages?resolve_id=%d&amp;season_id=%d&amp;club_id=%d">Review and resolve missing recipients</a>`, message.ID, message.SeasonID, message.ClubID)
			} else if message.AcceptedCount < message.RecipientCount && view.ClubID > 0 {
				fmt.Fprintf(w, `<p class="small text-muted">The counts above cover this club. Sending and retrying applies to the whole message audience. <a href="/admin/juniors/messages?season_id=%d">Open whole-message delivery controls</a>.</p>`, message.SeasonID)
			} else if message.AcceptedCount < message.RecipientCount {
				fmt.Fprintf(w, `<form method="POST" action="/admin/juniors/messages"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="operation" value="dispatch"><input type="hidden" name="message_id" value="%d"><button class="btn btn-outline-primary">Send queued recipients / retry failed</button></form>`, escapeHTML(middleware.CSRFToken(r)), message.ID)
			}
		}
		fmt.Fprint(w, `</details>`)
	}
}

func (s *Server) handleAdminJuniorMessagesPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := junior.NewService(s.DB)
		p := s.juniorPrincipal(r)
		operation := r.FormValue("operation")
		filter, err := juniorMessageFilter(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		var id int64
		switch operation {
		case "preview":
			preview, e := service.PreviewAudience(r.Context(), p, filter)
			if e != nil {
				juniorHTTPError(w, r, e)
				return
			}
			s.renderAdminJuniorMessages(w, r, filter, &preview)
			return
		case "queue":
			id, err = service.ComposeMessage(r.Context(), p, filter, r.FormValue("audience_hash"), r.FormValue("subject"), r.FormValue("body"), r.FormValue("key"))
		case "resolve":
			id, err = juniorRecordID(r.FormValue("resolve_id"), true)
			if err == nil {
				err = service.ResolveMessageRecipients(r.Context(), p, id, filter, r.FormValue("audience_hash"))
			}
		case "dispatch":
			id, err = juniorRecordID(r.FormValue("message_id"), true)
			if err == nil {
				err = service.DispatchMessage(r.Context(), p, id)
			}
		case "record_incoming":
			id, err = service.RecordIncoming(r.Context(), p, filter.ClubID, r.FormValue("from"), r.FormValue("subject"), r.FormValue("body"))
		case "link_incoming":
			id, err = juniorRecordID(r.FormValue("incoming_id"), true)
			if err == nil {
				err = service.LinkIncomingClub(r.Context(), p, id, filter.ClubID)
			}
		default:
			err = junior.ErrValidation
		}
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		values := url.Values{"success": {fmt.Sprintf("Message action recorded (#%d). Review the status below.", id)}}
		if filter.SeasonID > 0 {
			values.Set("season_id", strconv.FormatInt(filter.SeasonID, 10))
		}
		if filter.ClubID > 0 {
			values.Set("club_id", strconv.FormatInt(int64(filter.ClubID), 10))
		}
		http.Redirect(w, r, "/admin/juniors/messages?"+values.Encode(), http.StatusSeeOther)
	}
}

func (s *Server) handleJuniorMessages() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		seasons, seasonID, err := s.juniorRecordSeason(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		messages, err := junior.NewService(s.DB).MessagesFiltered(r.Context(), p, junior.MessageFilter{ClubID: p.ClubID, SeasonID: seasonID})
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Club messages")
		writeJuniorRecordSeasonSelector(w, r, seasons, seasonID)
		fmt.Fprint(w, `<p>Correspondence addressed to your club. Transport acceptance does not confirm delivery or reading.</p>`)
		renderJuniorMessages(w, r, messages, juniorMessageView{ClubID: p.ClubID})
		juniorPageEnd(w)
	}
}
func (s *Server) handleJuniorClubMessages() http.HandlerFunc { return s.handleJuniorMessages() }
