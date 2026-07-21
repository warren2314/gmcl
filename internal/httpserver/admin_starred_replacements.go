package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/starred"

	"github.com/go-chi/chi/v5"
)

type starredReplacementCaptain struct {
	ID                int32
	Name, Email, Team string
	Level             int
}

func (s *Server) starredReplacementCaptains(ctx context.Context, clubKey string, on time.Time) []starredReplacementCaptain {
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.full_name,CASE WHEN c.email_override_until >= $1::date AND COALESCE(c.email_override,'')<>'' THEN c.email_override ELSE c.email END,t.name,COALESCE(t.level,999),cl.name FROM captains c JOIN teams t ON t.id=c.team_id JOIN clubs cl ON cl.id=t.club_id WHERE t.active AND c.active_from <= $1::date AND (c.active_to IS NULL OR c.active_to >= $1::date) ORDER BY COALESCE(t.level,999),t.name,c.active_from DESC,c.id DESC`, on)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []starredReplacementCaptain
	seen := map[int32]bool{}
	for rows.Next() {
		var c starredReplacementCaptain
		var dbClub string
		if rows.Scan(&c.ID, &c.Name, &c.Email, &c.Team, &c.Level, &dbClub) == nil && starred.NormalizeClub(dbClub) == clubKey && !seen[c.ID] {
			seen[c.ID] = true
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}

func starredReplacementDraftText(player starredPlayerReviewRow, captain starredReplacementCaptain) (string, string) {
	name := strings.TrimSpace(captain.Name)
	if name == "" {
		name = "Captain"
	}
	subject := fmt.Sprintf("GMCL starred-player replacement review - %s - %s", player.ClubName, player.PlayerName)
	body := fmt.Sprintf(`Dear %s,

Following our review of the current GMCL starred-player list, we would like %s to be considered for replacement on %s's List %s.

Review information:
Player: %s
Current list: List %s
Total open-age XI appearances: %d
Appearances at the permitted list level: %d
Permitted-level percentage: %.1f%%
Review status: %s

Please reply with the proposed replacement player's full name and any relevant information, or let us know if you believe this review should be reconsidered.

This message is a request for review and does not change the published starred-player list automatically.

Regards,
Greater Manchester Cricket League`, name, player.PlayerName, player.ClubName, player.ListType, player.PlayerName, player.ListType, player.Total, player.RuleGames, player.RulePct, strings.Title(player.Signal))
	return subject, body
}

func (s *Server) loadStarredReplacementPlayer(ctx context.Context, r *http.Request) (starredPlayerReviewRow, error) {
	year := starredSeasonYear(r)
	clubKey, playerKey := strings.TrimSpace(r.FormValue("club_key")), strings.TrimSpace(r.FormValue("player_key"))
	cutoff := starred.ReviewCutoff(year, time.Now())
	periods, apps, mappings, _, err := starred.LoadEvaluationInputs(ctx, s.DB, year)
	if err != nil {
		return starredPlayerReviewRow{}, err
	}
	filtered := make([]starred.Appearance, 0, len(apps))
	for _, app := range apps {
		if app.TeamLevel > 0 && !app.MatchDate.After(cutoff) && !starred.IsWomensAppearance(app) {
			filtered = append(filtered, app)
		}
	}
	green, orange := starredReviewThresholds(r)
	for _, row := range buildStarredPlayerReviewRows(periods, filtered, mappings, cutoff, nil, "", clubKey, green, orange) {
		if row.ClubKey == clubKey && row.PlayerKey == playerKey && row.ListType != "" {
			return row, nil
		}
	}
	return starredPlayerReviewRow{}, fmt.Errorf("that player is no longer on the active starred-player list")
}

func (s *Server) handleAdminStarredReplacementNew() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		player, err := s.loadStarredReplacementPlayer(ctx, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		captains := s.starredReplacementCaptains(ctx, player.ClubKey, time.Now())
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "New player replacement request")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><div class="d-flex justify-content-between mb-3"><div><h1 class="h3">Request a player replacement</h1><p class="text-muted mb-0">%s — %s — List %s</p></div><a class="btn btn-outline-secondary align-self-start" href="/admin/starred-players?season=%d&amp;view=player-review">Back to review</a></div>`, escapeHTML(player.ClubName), escapeHTML(player.PlayerName), escapeHTML(player.ListType), starredSeasonYear(r))
		if len(captains) == 0 {
			fmt.Fprint(w, `<div class="alert alert-danger">No active captain could be matched to this club. Update Teams &amp; Captains before creating the request.</div>`)
		}
		fmt.Fprintf(w, `<form method="post" action="/admin/starred-player-replacements" class="card"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="club_key" value="%s"><input type="hidden" name="player_key" value="%s"><input type="hidden" name="green" value="%s"><input type="hidden" name="orange" value="%s"><div class="card-body"><div class="row g-3"><div class="col-md-8"><label class="form-label fw-semibold">Email recipient</label><select class="form-select" name="captain_id" required><option value="">Choose captain…</option>`, escapeHTML(csrf), starredSeasonYear(r), escapeHTML(player.ClubKey), escapeHTML(player.PlayerKey), escapeHTML(r.FormValue("green")), escapeHTML(r.FormValue("orange")))
		for i, c := range captains {
			selected := ""
			if i == 0 {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s — %s (%s)</option>`, c.ID, selected, escapeHTML(c.Name), escapeHTML(c.Team), escapeHTML(c.Email))
		}
		fmt.Fprintf(w, `</select><div class="form-text">The 1st XI captain is selected by default; choose another club captain if appropriate.</div></div><div class="col-md-4"><label class="form-label fw-semibold">Review evidence</label><div class="form-control bg-light">%d / %d permitted-level games (%.1f%%)</div></div></div></div><div class="card-footer d-flex justify-content-between align-items-center"><span class="text-muted small">This saves an editable draft. It does not send yet.</span><button class="btn btn-primary" %s>Create email draft</button></div></form></main>`, player.RuleGames, player.Total, player.RulePct, disabledIf(len(captains) == 0))
		pageFooter(w)
	}
}

func (s *Server) handleAdminStarredReplacementCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		player, err := s.loadStarredReplacementPlayer(ctx, r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		captainID, err := strconv.ParseInt(r.FormValue("captain_id"), 10, 32)
		if err != nil {
			http.Error(w, "choose a captain", 400)
			return
		}
		var captain starredReplacementCaptain
		for _, c := range s.starredReplacementCaptains(ctx, player.ClubKey, time.Now()) {
			if c.ID == int32(captainID) {
				captain = c
				break
			}
		}
		if captain.ID == 0 {
			http.Error(w, "captain is not active for this club", 400)
			return
		}
		subject, body := starredReplacementDraftText(player, captain)
		adminID := s.resolveAdminID(r)
		var id int64
		err = s.DB.QueryRow(ctx, `INSERT INTO starred_player_replacement_requests(season_year,club_name,club_key,player_name,player_key,play_cricket_player_id,list_type,review_signal,total_games,rule_games,rule_percentage,captain_id,captain_name,captain_email,email_subject,email_body,created_by) VALUES($1,$2,$3,$4,$5,NULLIF($6,0),$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING id`, starredSeasonYear(r), player.ClubName, player.ClubKey, player.PlayerName, player.PlayerKey, player.PlayerID, player.ListType, player.Signal, player.Total, player.RuleGames, player.RulePct, captain.ID, captain.Name, captain.Email, subject, body, adminID).Scan(&id)
		if err != nil {
			http.Error(w, "could not create replacement draft: "+err.Error(), 500)
			return
		}
		s.audit(ctx, r, "admin", adminID, "starred_replacement_draft_created", "starred_player_replacement_request", &id, map[string]any{"club": player.ClubName, "player": player.PlayerName, "recipient": captain.Email})
		http.Redirect(w, r, fmt.Sprintf("/admin/starred-player-replacements/%d", id), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminStarredReplacementDraft() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var year, total, rule int
		var club, player, listType, signal, captain, emailAddr, subject, body, status, sendError string
		var pct float64
		err = s.DB.QueryRow(ctx, `SELECT season_year,club_name,player_name,list_type,review_signal,total_games,rule_games,rule_percentage,COALESCE(captain_name,''),COALESCE(captain_email,''),email_subject,email_body,status,COALESCE(send_error,'') FROM starred_player_replacement_requests WHERE id=$1`, id).Scan(&year, &club, &player, &listType, &signal, &total, &rule, &pct, &captain, &emailAddr, &subject, &body, &status, &sendError)
		if err != nil {
			http.Error(w, "request not found", 404)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Player replacement email")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><div class="d-flex justify-content-between mb-3"><div><h1 class="h3">Player replacement email</h1><p class="text-muted mb-0">%s — %s — List %s</p></div><a class="btn btn-outline-secondary align-self-start" href="/admin/starred-player-replacements">All requests</a></div>`, escapeHTML(club), escapeHTML(player), escapeHTML(listType))
		if status == "sent" {
			fmt.Fprint(w, `<div class="alert alert-success">This replacement request has been emailed to the captain and is locked.</div>`)
		}
		if sendError != "" {
			fmt.Fprintf(w, `<div class="alert alert-danger">Previous send failed: %s</div>`, escapeHTML(sendError))
		}
		fmt.Fprintf(w, `<div class="card mb-3"><div class="card-body row g-3"><div class="col-md-4"><span class="text-muted small">Recipient</span><br><strong>%s</strong><br>%s</div><div class="col-md-4"><span class="text-muted small">Review status</span><br><strong>%s</strong></div><div class="col-md-4"><span class="text-muted small">Evidence</span><br><strong>%d / %d (%.1f%%)</strong></div></div></div><form method="post" action="/admin/starred-player-replacements/%d/send" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><div class="mb-3"><label class="form-label fw-semibold">Subject</label><input class="form-control" name="email_subject" value="%s" required maxlength="250" %s></div><label class="form-label fw-semibold">Message</label><textarea class="form-control" name="email_body" rows="18" required %s>%s</textarea></div><div class="card-footer d-flex justify-content-between"><span class="small text-muted">Review and edit the wording before sending.</span><button class="btn btn-danger" %s onclick="return confirm('Send this replacement request to %s?')">Send to captain</button></div></form></main>`, escapeHTML(captain), escapeHTML(emailAddr), escapeHTML(strings.Title(signal)), rule, total, pct, id, escapeHTML(csrf), escapeHTML(subject), disabledIf(status == "sent"), disabledIf(status == "sent"), escapeHTML(body), disabledIf(status == "sent"), escapeHTML(emailAddr))
		pageFooter(w)
	}
}

func (s *Server) handleAdminStarredReplacementSend() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		subject, body := strings.TrimSpace(r.FormValue("email_subject")), strings.TrimSpace(r.FormValue("email_body"))
		if subject == "" || body == "" || len(subject) > 250 {
			http.Error(w, "subject and message are required", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		adminID := s.resolveAdminID(r)
		var recipient string
		err = s.DB.QueryRow(ctx, `UPDATE starred_player_replacement_requests SET email_subject=$1,email_body=$2,status='sending',send_error=NULL,updated_at=now() WHERE id=$3 AND status IN ('draft','send_failed') RETURNING COALESCE(captain_email,'')`, subject, body, id).Scan(&recipient)
		if err != nil {
			http.Error(w, "this request has already been sent or is currently being sent", http.StatusConflict)
			return
		}
		if _, err = mail.ParseAddress(recipient); err != nil {
			_, _ = s.DB.Exec(ctx, `UPDATE starred_player_replacement_requests SET status='send_failed',send_error='captain email is invalid',updated_at=now() WHERE id=$1 AND status='sending'`, id)
			http.Error(w, "captain email is invalid", 400)
			return
		}
		if err = email.NewFromEnv().Send(recipient, subject, body); err != nil {
			_, _ = s.DB.Exec(ctx, `UPDATE starred_player_replacement_requests SET status='send_failed',send_error=$1,updated_at=now() WHERE id=$2`, err.Error(), id)
			s.audit(ctx, r, "admin", adminID, "starred_replacement_send_failed", "starred_player_replacement_request", &id, map[string]any{"recipient": recipient, "error": err.Error()})
			http.Redirect(w, r, fmt.Sprintf("/admin/starred-player-replacements/%d", id), 303)
			return
		}
		_, _ = s.DB.Exec(ctx, `UPDATE starred_player_replacement_requests SET status='sent',sent_by=$1,sent_at=now(),send_error=NULL,updated_at=now() WHERE id=$2`, adminID, id)
		s.audit(ctx, r, "admin", adminID, "starred_replacement_email_sent", "starred_player_replacement_request", &id, map[string]any{"recipient": recipient})
		http.Redirect(w, r, "/admin/starred-player-replacements?message="+url.QueryEscape("Replacement request sent to "+recipient), 303)
	}
}

func (s *Server) handleAdminStarredReplacements() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		rows, err := s.DB.Query(ctx, `SELECT id,season_year,club_name,player_name,list_type,COALESCE(captain_name,''),COALESCE(captain_email,''),status,created_at,COALESCE(sent_at,created_at) FROM starred_player_replacement_requests ORDER BY created_at DESC LIMIT 250`)
		if err != nil {
			http.Error(w, "could not load replacement requests", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Player Replacements")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-4 py-4"><div class="d-flex justify-content-between align-items-start mb-3"><div><h1 class="h3">Player Replacements</h1><p class="text-muted">Draft and sent requests created from the starred-player review.</p></div><a class="btn btn-primary" href="/admin/starred-players?view=player-review">Open player review</a></div>`)
		if msg := r.URL.Query().Get("message"); msg != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(msg))
		}
		fmt.Fprint(w, `<div class="card"><div class="table-responsive"><table class="table align-middle mb-0"><thead><tr><th>Player</th><th>Club</th><th>List</th><th>Recipient</th><th>Status</th><th>Created</th><th></th></tr></thead><tbody>`)
		count := 0
		for rows.Next() {
			var id int64
			var year int
			var club, player, listType, captain, emailAddr, status string
			var created, event time.Time
			if rows.Scan(&id, &year, &club, &player, &listType, &captain, &emailAddr, &status, &created, &event) != nil {
				continue
			}
			count++
			badge := "bg-secondary"
			if status == "sent" {
				badge = "bg-success"
			} else if status == "send_failed" {
				badge = "bg-danger"
			}
			fmt.Fprintf(w, `<tr><td><strong>%s</strong><div class="small text-muted">%d</div></td><td>%s</td><td>List %s</td><td>%s<div class="small text-muted">%s</div></td><td><span class="badge %s">%s</span></td><td>%s</td><td><a class="btn btn-sm btn-outline-primary" href="/admin/starred-player-replacements/%d">%s</a></td></tr>`, escapeHTML(player), year, escapeHTML(club), escapeHTML(listType), escapeHTML(captain), escapeHTML(emailAddr), badge, escapeHTML(status), created.In(s.LondonLoc).Format("02 Jan 2006 15:04"), id, map[bool]string{true: "View", false: "Review"}[status == "sent"])
		}
		if count == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-4">No replacement requests have been created yet.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div></main>`)
		pageFooter(w)
	}
}
