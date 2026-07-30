package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/portal"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) handleAdminPortalCompetitionsGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		seasons, err := s.PortalStore.ListStaffCompetitionSeasons(ctx)
		if err != nil {
			http.Error(w, "could not load seasons", http.StatusInternalServerError)
			return
		}
		clubs, err := s.PortalStore.ListPilotClubs(ctx)
		if err != nil {
			http.Error(w, "could not load clubs", http.StatusInternalServerError)
			return
		}
		competitions, err := s.PortalStore.ListAllStaffCompetitions(ctx)
		if err != nil {
			http.Error(w, "could not load competition contexts", http.StatusInternalServerError)
			return
		}

		var editing *portal.StaffCompetition
		if raw := strings.TrimSpace(r.URL.Query().Get("edit")); raw != "" {
			if editID, parseErr := uuid.Parse(raw); parseErr == nil {
				for index := range competitions {
					if competitions[index].ID == editID &&
						competitions[index].Manageable {
						editing = &competitions[index]
						break
					}
				}
			}
		}

		csrf := adminPortalCSRF(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Competition contexts")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container-fluid px-3 px-lg-4 pb-5">
<div class="d-flex flex-column flex-lg-row justify-content-between align-items-lg-start gap-3 mb-4">
  <div>
    <p class="text-uppercase text-muted small mb-1">Club operations</p>
    <h1 class="h2">Competition contexts</h1>
    <p class="text-muted mb-0">Create the competition options used for staff scope and outbound club messages.</p>
  </div>
  <div class="d-flex flex-wrap gap-2">
    <a class="btn btn-outline-secondary" href="/admin/portal/staff">&larr; Staff roles</a>
    <a class="btn btn-primary" href="/admin/portal/messages/new">Compose club message</a>
  </div>
</div>`)
		renderAdminPortalCompetitionStatus(
			w,
			r.URL.Query().Get("status"),
			r.URL.Query().Get("message"),
		)
		fmt.Fprint(w, `<div class="alert alert-info">
<strong>What this controls.</strong> A competition context records which competition a campaign relates to, limits competition-scoped staff to mapped clubs, and filters the club selector on the compose page. It does not choose a recipient role or send anything by itself.
</div>`)

		fmt.Fprintf(w, `<section class="card shadow-sm mb-4"><div class="card-header"><strong>Add competition context</strong></div>
<form method="post" action="/admin/portal/competitions">
<input type="hidden" name="csrf_token" value="%s">
<div class="card-body"><div class="row g-3">
<div class="col-lg-5"><label class="form-label" for="competition-name">Competition name</label><input class="form-control" id="competition-name" name="name" maxlength="200" required placeholder="For example: Premier League"></div>
<div class="col-lg-4"><label class="form-label" for="competition-season">Season</label><select class="form-select" id="competition-season" name="season_id" required><option value="">Choose season</option>`, escapeHTML(csrf))
		for _, season := range seasons {
			fmt.Fprintf(w, `<option value="%d">%s</option>`, season.ID, escapeHTML(season.Name))
		}
		fmt.Fprint(w, `</select></div>
<div class="col-12"><label class="form-label" for="competition-clubs">Clubs in this competition</label><select class="form-select" id="competition-clubs" name="club_id" multiple size="12" required aria-describedby="competition-clubs-help">`)
		for _, club := range clubs {
			fmt.Fprintf(w, `<option value="%d">%s</option>`, club.ID, escapeHTML(club.Name))
		}
		fmt.Fprint(w, `</select><div class="form-text" id="competition-clubs-help">Hold Ctrl (Windows) or Command (Mac) to select more than one club. These mappings are enforced when competition-scoped staff send or open cases.</div></div>
</div></div><div class="card-footer text-end"><button class="btn btn-primary" type="submit">Add competition context</button></div></form></section>`)

		if editing != nil {
			fmt.Fprintf(w, `<section class="card border-primary shadow-sm mb-4" id="edit-competition"><div class="card-header"><strong>Update clubs: %s</strong></div>
<form method="post" action="/admin/portal/competitions/%s/clubs">
<input type="hidden" name="csrf_token" value="%s"><div class="card-body">
<label class="form-label" for="edit-competition-clubs">Mapped clubs</label><select class="form-select" id="edit-competition-clubs" name="club_id" multiple size="12" required>`,
				escapeHTML(editing.Name),
				editing.ID,
				escapeHTML(csrf),
			)
			for _, club := range clubs {
				selected := ""
				if editing.ContainsClub(club.ID) {
					selected = " selected"
				}
				fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, selected, escapeHTML(club.Name))
			}
			fmt.Fprintf(w, `</select></div><div class="card-footer d-flex justify-content-between gap-2"><a class="btn btn-outline-secondary" href="/admin/portal/competitions">Cancel</a><button class="btn btn-primary" type="submit">Save mapped clubs</button></div></form></section>`)
		}

		fmt.Fprint(w, `<section class="card shadow-sm"><div class="card-header"><strong>Configured contexts</strong></div><div class="table-responsive"><table class="table table-hover responsive-cards align-middle mb-0"><thead><tr><th>Competition</th><th>Season</th><th>Mapped clubs</th><th>Status</th><th class="text-end">Actions</th></tr></thead><tbody>`)
		if len(competitions) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-muted">No competition contexts have been configured.</td></tr>`)
		}
		for _, competition := range competitions {
			status := "Ended"
			statusClass := "text-bg-secondary"
			if competition.Active {
				status = "Active"
				statusClass = "text-bg-success"
			} else if competition.Manageable && !competition.Endable {
				status = "Scheduled"
				statusClass = "text-bg-info"
				if len(competition.ClubIDs) == 0 {
					status = "Scheduled - needs clubs"
				}
			} else if competition.Manageable &&
				len(competition.ClubIDs) == 0 {
				status = "Needs club mapping"
				statusClass = "text-bg-warning"
			}
			seasonName := competition.SeasonName
			if seasonName == "" {
				seasonName = "No season"
			}
			clubNames := strings.Join(competition.ClubNames, ", ")
			if clubNames == "" {
				clubNames = "No clubs mapped"
			}
			actions := ""
			if competition.Manageable {
				endAction := ""
				if competition.Endable {
					endAction = fmt.Sprintf(`<details><summary class="btn btn-sm btn-outline-danger">End context</summary><form method="post" action="/admin/portal/competitions/%s/end" class="border rounded-3 bg-body-tertiary p-2 mt-2 text-start" style="min-width:17rem"><input type="hidden" name="csrf_token" value="%s"><label class="form-label small" for="end-%s">Reason</label><input class="form-control form-control-sm mb-2" id="end-%s" name="reason" maxlength="500" required><button class="btn btn-sm btn-danger w-100" type="submit">Confirm end</button></form></details>`,
						competition.ID,
						escapeHTML(csrf),
						competition.ID,
						competition.ID,
					)
				}
				actions = fmt.Sprintf(`<div class="d-flex flex-column flex-sm-row justify-content-end gap-2"><a class="btn btn-sm btn-outline-primary" href="/admin/portal/competitions?edit=%s#edit-competition">Edit clubs</a>%s</div>`,
					competition.ID,
					endAction,
				)
			}
			fmt.Fprintf(w, `<tr><td data-label="Competition"><strong>%s</strong></td><td data-label="Season">%s</td><td data-label="Mapped clubs"><span class="small">%s</span></td><td data-label="Status"><span class="badge rounded-pill %s">%s</span>%s</td><td data-label="Actions" class="text-end">%s</td></tr>`,
				escapeHTML(competition.Name),
				escapeHTML(seasonName),
				escapeHTML(clubNames),
				statusClass,
				escapeHTML(status),
				adminPortalCompetitionEndReason(competition),
				actions,
			)
		}
		fmt.Fprint(w, `</tbody></table></div></section></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPortalCompetitionCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid competition form", http.StatusBadRequest)
			return
		}
		seasonID64, err := strconv.ParseInt(
			strings.TrimSpace(r.FormValue("season_id")),
			10,
			32,
		)
		if err != nil || seasonID64 <= 0 {
			http.Error(w, "invalid competition season", http.StatusBadRequest)
			return
		}
		clubIDs, err := parsePortalClubIDs(r.Form["club_id"])
		if err != nil {
			http.Error(w, "invalid competition clubs", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		_, err = s.PortalStore.CreateStaffCompetition(
			ctx,
			portal.StaffCompetitionRequest{
				Name:      r.FormValue("name"),
				SeasonID:  int32(seasonID64),
				ClubIDs:   clubIDs,
				CreatedBy: adminIDForRequest(r),
			},
		)
		if err != nil {
			redirectAdminPortalCompetition(
				w,
				r,
				"competition-invalid",
				"The context could not be created. Use a unique name for the season and select at least one club.",
			)
			return
		}
		redirectAdminPortalCompetition(w, r, "competition-created", "")
	}
}

func (s *Server) handleAdminPortalCompetitionClubsUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		competitionID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid competition context", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid competition clubs", http.StatusBadRequest)
			return
		}
		clubIDs, err := parsePortalClubIDs(r.Form["club_id"])
		if err != nil {
			http.Error(w, "invalid competition clubs", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.PortalStore.UpdateStaffCompetitionClubs(
			ctx,
			competitionID,
			clubIDs,
			adminIDForRequest(r),
		); err != nil {
			redirectAdminPortalCompetition(
				w,
				r,
				"competition-invalid",
				"The mapped clubs could not be updated.",
			)
			return
		}
		redirectAdminPortalCompetition(w, r, "competition-clubs-updated", "")
	}
}

func (s *Server) handleAdminPortalCompetitionEnd() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		competitionID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
		if err != nil {
			http.Error(w, "invalid competition context", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := s.PortalStore.EndStaffCompetition(
			ctx,
			competitionID,
			adminIDForRequest(r),
			r.FormValue("reason"),
		); err != nil {
			redirectAdminPortalCompetition(
				w,
				r,
				"competition-invalid",
				"The competition context could not be ended.",
			)
			return
		}
		redirectAdminPortalCompetition(w, r, "competition-ended", "")
	}
}

func parsePortalClubIDs(values []string) ([]int32, error) {
	clubIDs := make([]int32, 0, len(values))
	seen := make(map[int32]struct{}, len(values))
	for _, raw := range values {
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("invalid club")
		}
		clubID := int32(value)
		if _, exists := seen[clubID]; exists {
			continue
		}
		seen[clubID] = struct{}{}
		clubIDs = append(clubIDs, clubID)
	}
	if len(clubIDs) == 0 {
		return nil, fmt.Errorf("at least one club is required")
	}
	return clubIDs, nil
}

func renderAdminPortalCompetitionStatus(
	w http.ResponseWriter,
	status string,
	message string,
) {
	switch status {
	case "competition-created":
		fmt.Fprint(w, `<div class="alert alert-success">The competition context and its club mappings were added.</div>`)
	case "competition-clubs-updated":
		fmt.Fprint(w, `<div class="alert alert-success">The competition club mappings were updated.</div>`)
	case "competition-ended":
		fmt.Fprint(w, `<div class="alert alert-success">The competition context was ended. Historical campaigns and assignments remain recorded.</div>`)
	case "competition-invalid":
		if strings.TrimSpace(message) == "" {
			message = "The competition context could not be changed."
		}
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(message))
	}
}

func adminPortalCompetitionEndReason(
	competition portal.StaffCompetition,
) string {
	if strings.TrimSpace(competition.EndReason) == "" {
		return ""
	}
	return `<div class="small text-muted mt-1">` +
		escapeHTML(competition.EndReason) +
		`</div>`
}

func redirectAdminPortalCompetition(
	w http.ResponseWriter,
	r *http.Request,
	status string,
	message string,
) {
	values := url.Values{"status": {status}}
	if strings.TrimSpace(message) != "" {
		values.Set("message", message)
	}
	http.Redirect(
		w,
		r,
		"/admin/portal/competitions?"+values.Encode(),
		http.StatusSeeOther,
	)
}
