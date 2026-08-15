package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

func (s *Server) handleAdminIneligibleTrainingForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "administrator identity is required", http.StatusUnauthorized)
			return
		}
		var email string
		var reportingClubID int32
		_ = s.DB.QueryRow(r.Context(), `SELECT COALESCE(email,'') FROM admin_users WHERE id=$1`, *actor.ID).Scan(&email)

		rawID, _, err := newPublicToken()
		if err != nil {
			http.Error(w, "could not open training form", http.StatusInternalServerError)
			return
		}
		clubs, teams, _, err := s.loadIneligibleMappingOptions(r.Context())
		if err != nil {
			http.Error(w, "could not load clubs and teams", http.StatusInternalServerError)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Create an ineligible-player training report")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container py-4" style="max-width:820px"><div class="d-flex justify-content-between gap-3 mb-3"><div><h1 class="h2 mb-1">Create a training report</h1><p class="text-muted mb-0">Complete the same information required from a real ineligible-player reporter.</p></div><a class="btn btn-outline-secondary align-self-start" href="/admin/ineligible">Back to reports</a></div>`)
		fmt.Fprint(w, `<div class="alert alert-warning"><strong>Training journey with real email.</strong> The report and resulting case are excluded from live workload totals, but investigators can send the normal response request and approved outcome emails to the selected clubs and reporter. Check every address before sending.</div>`)
		fmt.Fprintf(w, `<form method="POST" action="/admin/ineligible/training/new" enctype="multipart/form-data" class="card border-warning"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="submission_id" value="%s"><input type="hidden" name="submission_timestamp" value="%s"><div class="card-body row g-3">`, escapeHTML(csrf), escapeHTML(rawID), time.Now().UTC().Format(time.RFC3339Nano))
		fmt.Fprintf(w, `<div class="col-md-6"><label class="form-label">Your name</label><input class="form-control" name="reporter_name" value="%s" required maxlength="150"></div><div class="col-md-6"><label class="form-label">Your email</label><input class="form-control" type="email" name="reporter_email" value="%s" required maxlength="320"></div>`, escapeHTML(actor.Label), escapeHTML(email))
		fmt.Fprint(w, `<div class="col-md-6"><label class="form-label">Your role at the club or league</label><input class="form-control" name="reporter_role" value="GMCL training reporter" required maxlength="200"></div><div class="col-md-6"><label class="form-label">Your preferred telephone number</label><input class="form-control" type="tel" name="reporter_phone" required maxlength="100"></div>`)
		fmt.Fprint(w, `<div class="col-md-6"><label class="form-label">Your club</label><select class="form-select" name="reporting_club_id" required><option value="">Choose your club...</option>`)
		for _, club := range clubs {
			selected := ""
			if int32(club.ID) == reportingClubID {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, selected, escapeHTML(club.Name))
		}
		fmt.Fprint(w, `</select></div><div class="col-md-6"><label class="form-label">Offending club and team</label><select class="form-select" name="team_id" required><option value="">Choose...</option>`)
		for _, team := range teams {
			fmt.Fprintf(w, `<option value="%d">%s - %s</option>`, team.ID, escapeHTML(team.ClubName), escapeHTML(team.TeamName))
		}
		fmt.Fprint(w, `</select></div><div class="col-md-6"><label class="form-label">Fixture date</label><input class="form-control" type="date" name="match_date" required></div><div class="col-md-6"><label class="form-label">Name of defaulting player as shown on scorecard</label><input class="form-control" name="player_name" required maxlength="200"></div>`)
		fmt.Fprint(w, `<div class="col-12"><label class="form-label">Reason you believe the player is ineligible</label><textarea class="form-control" name="summary" rows="5" required maxlength="10000"></textarea></div><div class="col-12"><label class="form-label">Additional information (optional)</label><textarea class="form-control" name="additional_info" rows="3" maxlength="10000"></textarea></div><div class="col-12"><label class="form-label">Additional evidence or links (optional)</label><textarea class="form-control" name="additional_evidence" rows="3" maxlength="10000"></textarea></div><div class="col-md-6"><label class="form-label">Score or Play-Cricket scorecard reference (optional)</label><input class="form-control" name="score" maxlength="500"></div><div class="col-md-6"><label class="form-label">Evidence file (optional)</label><input class="form-control" type="file" name="evidence" accept=".pdf,image/jpeg,image/png,image/webp,video/mp4,.mp4,.txt"></div>`)
		fmt.Fprint(w, `<div class="col-12 form-check ms-2"><input class="form-check-input" type="checkbox" name="consent" value="yes" required id="training-consent"><label class="form-check-label" for="training-consent">I confirm this is a training allegation and understand that later workflow steps can send real emails.</label></div></div><div class="card-footer d-flex justify-content-between align-items-center gap-3"><span class="small text-muted">Submitting creates a private intake only. No email is sent at this step.</span><button class="btn btn-warning">Submit training report</button></div></form></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminIneligibleTrainingSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(512<<10))
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "invalid or oversized submission", http.StatusBadRequest)
			return
		}
		if r.FormValue("consent") != "yes" {
			http.Error(w, "training email consent is required", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("reporter_name"))
		email := strings.ToLower(strings.TrimSpace(r.FormValue("reporter_email")))
		reason := strings.TrimSpace(r.FormValue("summary"))
		teamID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("team_id")))
		if name == "" || email == "" || !strings.Contains(email, "@") || reason == "" || err != nil || teamID < 1 {
			http.Error(w, "all required reporter and case fields are required", http.StatusBadRequest)
			return
		}
		var offendingClub, team string
		if s.DB.QueryRow(r.Context(), `SELECT c.name,t.name FROM teams t JOIN clubs c ON c.id=t.club_id WHERE t.id=$1 AND t.active`, teamID).Scan(&offendingClub, &team) != nil {
			http.Error(w, "team not found", http.StatusBadRequest)
			return
		}
		s.stageNativeIneligibleReport(w, r, name, email, reason, teamID, offendingClub, team, true)
	}
}
