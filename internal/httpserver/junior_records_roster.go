package httpserver

import (
	"fmt"
	"net/http"
	"strings"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

func (s *Server) HandleJuniorRosterUpload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, data, err := juniorUploadBytes(w, r, "roster", junior.RosterUploadLimit)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasonID, err := juniorRecordID(r.FormValue("season_id"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		preview, err := junior.NewService(s.DB).PreviewRoster(r.Context(), s.juniorPrincipal(r), seasonID, name, data)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/juniors/roster/%d", preview.ID), http.StatusSeeOther)
	}
}

func juniorRosterMappings(r *http.Request, preview junior.RosterPreview, apply bool) ([]junior.RosterMapping, error) {
	var mappings []junior.RosterMapping
	for _, row := range preview.Rows {
		prefix := fmt.Sprintf("row_%d_", row.Number)
		mapping := junior.RosterMapping{RowNumber: row.Number, Skip: r.FormValue(prefix+"skip") == "1", Reason: strings.TrimSpace(r.FormValue(prefix + "reason"))}
		clubValue := r.FormValue(prefix + "club")
		if mapping.Skip && clubValue == "0" {
			clubValue = ""
		}
		clubID, err := juniorRecordID(clubValue, mapping.Skip == false)
		if err != nil || clubID > 2147483647 {
			return nil, fmt.Errorf("%w: select the existing club for row %d or skip it with a reason", junior.ErrValidation, row.Number)
		}
		mapping.ClubID = int32(clubID)
		if apply && !mapping.Skip {
			mapping.ContactID, err = juniorRecordID(r.FormValue(prefix+"contact"), false)
			if err != nil {
				return nil, err
			}
			mapping.TeamID, err = juniorRecordID(r.FormValue(prefix+"team"), false)
			if err != nil {
				return nil, err
			}
			mapping.CreateContact = r.FormValue(prefix+"create_contact") == "1"
			mapping.CreateTeam = r.FormValue(prefix+"create_team") == "1"
			mapping.ActivateParticipation = r.FormValue(prefix+"activate") == "1"
		}
		mappings = append(mappings, mapping)
	}
	return mappings, nil
}

func (s *Server) HandleJuniorRosterReview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		id, err := juniorRecordID(chi.URLParam(r, "importID"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		service := junior.NewService(s.DB)
		preview, err := service.GetRosterPreview(r.Context(), s.juniorPrincipal(r), id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		mappings, err := juniorRosterMappings(r, preview, false)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if _, err = service.ReviewRoster(r.Context(), s.juniorPrincipal(r), id, mappings); err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/juniors/roster/%d", id), http.StatusSeeOther)
	}
}

func (s *Server) HandleJuniorRosterApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		if r.FormValue("confirm") != "apply" {
			juniorHTTPError(w, r, fmt.Errorf("%w: explicitly confirm the reviewed roster", junior.ErrValidation))
			return
		}
		id, err := juniorRecordID(chi.URLParam(r, "importID"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		service := junior.NewService(s.DB)
		preview, err := service.GetRosterPreview(r.Context(), s.juniorPrincipal(r), id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		mappings, err := juniorRosterMappings(r, preview, true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if _, err = service.ApplyRoster(r.Context(), s.juniorPrincipal(r), id, mappings); err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/juniors/roster/%d", id), http.StatusSeeOther)
	}
}

func (s *Server) HandleJuniorRosterPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.juniorPrincipal(r)
		service := junior.NewService(s.DB)
		if chi.URLParam(r, "importID") == "" {
			seasons, err := service.ListSeasons(r.Context(), p)
			if err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			s.juniorPageStart(w, r, "Review a previous-season roster")
			fmt.Fprint(w, `<p>Upload a CSV, map each row to an existing club, then explicitly choose which adult contacts and junior teams to reuse or create. Previewing and reviewing do not change club records.</p><p class="small text-muted">Required column: <code>club_name</code>. Optional columns: <code>contact_name,email,phone,role,age_group,team_name</code>. Roles: <code>junior_lead</code>, <code>safeguarding_officer</code>, <code>coach</code>. Coach age groups: U9, U11, U13, U15, U18. Use one row per adult role. Maximum 2 MB / 2,000 rows. Existing clubs are never created automatically.</p>`)
			if len(seasons) == 0 {
				fmt.Fprint(w, `<div class="alert alert-info">Configure a junior season in Clubs and contacts before reviewing a roster.</div>`)
			} else {
				fmt.Fprintf(w, `<form method="POST" enctype="multipart/form-data" action="/admin/juniors/roster" class="card card-body"><input type="hidden" name="csrf_token" value="%s"><label class="form-label">Target junior season<select class="form-select" name="season_id" required>`, escapeHTML(middleware.CSRFToken(r)))
				for _, season := range seasons {
					fmt.Fprintf(w, `<option value="%d">%s · %s</option>`, season.ID, escapeHTML(season.Name), escapeHTML(season.State))
				}
				fmt.Fprint(w, `</select></label><label class="form-label">Roster CSV<input class="form-control" type="file" name="roster" accept=".csv,text/csv" required></label><button class="btn btn-primary align-self-start">Preview CSV</button></form>`)
			}
			juniorPageEnd(w)
			return
		}
		id, err := juniorRecordID(chi.URLParam(r, "importID"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		preview, err := service.GetRosterPreview(r.Context(), p, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Roster review — "+preview.SourceName)
		fmt.Fprintf(w, `<p><strong>%d source rows</strong> · %s · target junior season %d</p><p class="small text-muted">Source SHA-256: <code>%s</code>. Junior records only; senior teams, reporting weeks, fixtures and obligations are unchanged.</p>`, len(preview.Rows), escapeHTML(preview.State), preview.SeasonID, escapeHTML(preview.SHA256))
		if preview.State == "applied" {
			result := preview.Result
			fmt.Fprintf(w, `<div class="alert alert-success">Applied once: %d rows, %d skipped, %d clubs activated, %d new adult contacts, %d new junior teams, %d new dated roles. Re-uploading the same source for this season returns this receipt.</div>`, result.RowsApplied, result.RowsSkipped, result.ClubsActivated, result.ContactsCreated, result.TeamsCreated, result.RolesAdded)
			fmt.Fprint(w, `<ul>`)
			for _, row := range preview.Rows {
				for _, mapping := range preview.Review {
					if mapping.RowNumber == row.Number {
						label := fmt.Sprintf("Club #%d", mapping.ClubID)
						if mapping.Skip {
							label = "Skipped: " + mapping.Reason
						}
						fmt.Fprintf(w, `<li>Row %d: %s — %s</li>`, row.Number, escapeHTML(row.ClubName), escapeHTML(label))
					}
				}
			}
			fmt.Fprint(w, `</ul><a href="/admin/juniors/clubs">Open club records</a>`)
			juniorPageEnd(w)
			return
		}
		apply := preview.State == "reviewed" && r.URL.Query().Get("review_clubs") != "1"
		action := "review"
		if apply {
			action = "apply"
			fmt.Fprintf(w, `<p><a href="/admin/juniors/roster/%d?review_clubs=1">Change reviewed club mappings</a></p><div class="alert alert-info">Step 2: confirm adult identities and junior teams. Reusing a contact keeps its existing name, email and phone unchanged. Creating requires an explicit checkbox. New role assignments start today. New season participation becomes Active; withdrawn clubs require explicit approval.</div>`, id)
		} else {
			fmt.Fprint(w, `<div class="alert alert-info">Step 1: explicitly review every club mapping. Exact name matches are suggestions only. Unresolved or ambiguous names require a selection or a documented skip.</div>`)
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/juniors/roster/%d/%s"><input type="hidden" name="csrf_token" value="%s">`, id, action, escapeHTML(middleware.CSRFToken(r)))
		reviewed := map[int]junior.RosterMapping{}
		for _, mapping := range preview.Review {
			reviewed[mapping.RowNumber] = mapping
		}
		for _, row := range preview.Rows {
			prefix := fmt.Sprintf("row_%d_", row.Number)
			mapping, hasReview := reviewed[row.Number]
			clubID := row.SuggestedClubID
			if hasReview {
				clubID = mapping.ClubID
			}
			fmt.Fprintf(w, `<fieldset class="card card-body mb-3"><legend class="h6">Row %d · %s</legend><p class="small mb-2">Adult: %s · %s · %s<br>Role: %s %s · Team: %s</p>`, row.Number, escapeHTML(row.ClubName), escapeHTML(row.ContactName), escapeHTML(row.Email), escapeHTML(row.Phone), escapeHTML(junior.RoleLabel(row.Role)), escapeHTML(row.AgeGroup), escapeHTML(row.TeamName))
			if row.Problem != "" {
				fmt.Fprintf(w, `<div class="alert alert-warning">%s — correct the source and upload again, or skip this row.</div>`, escapeHTML(row.Problem))
			}
			if apply {
				fmt.Fprintf(w, `<input type="hidden" name="%sclub" value="%d">`, prefix, clubID)
				if mapping.Skip {
					fmt.Fprintf(w, `<input type="hidden" name="%sskip" value="1"><input type="hidden" name="%sreason" value="%s"><p>Skipped: %s</p></fieldset>`, prefix, prefix, escapeHTML(mapping.Reason), escapeHTML(mapping.Reason))
					continue
				}
				for _, club := range preview.Clubs {
					if club.ID == clubID {
						fmt.Fprintf(w, `<p><strong>Reviewed club: %s (#%d)</strong> · %s</p>`, escapeHTML(club.Name), club.ID, escapeHTML(junior.ParticipationLabel(club.State)))
						if club.State == "withdrawn" {
							fmt.Fprintf(w, `<label class="form-check mb-3"><input class="form-check-input" type="checkbox" name="%sactivate" value="1"> Approve reactivating this withdrawn club for the target season</label>`, prefix)
						}
					}
				}
				if row.ContactName != "" {
					fmt.Fprintf(w, `<label class="form-label">Reuse an existing adult contact (details stay unchanged)<select class="form-select" name="%scontact"><option value="">Choose a contact or explicitly create below</option>`, prefix)
					for _, contact := range preview.Contacts {
						if contact.ClubID == clubID {
							fmt.Fprintf(w, `<option value="%d">%s · %s · %s (#%d)</option>`, contact.ID, escapeHTML(contact.FullName), escapeHTML(contact.Email), escapeHTML(contact.Phone), contact.ID)
						}
					}
					fmt.Fprintf(w, `</select></label><label class="form-check mb-3"><input class="form-check-input" type="checkbox" name="%screate_contact" value="1"> Create the source adult contact as a new junior record</label>`, prefix)
				}
				if row.TeamName != "" {
					fmt.Fprintf(w, `<label class="form-label">Reuse an existing junior team<select class="form-select" name="%steam"><option value="">Choose a team or explicitly create below</option>`, prefix)
					for _, team := range preview.Teams {
						if team.ClubID == clubID {
							fmt.Fprintf(w, `<option value="%d">%s (#%d)</option>`, team.ID, escapeHTML(team.Name), team.ID)
						}
					}
					fmt.Fprintf(w, `</select></label><label class="form-check"><input class="form-check-input" type="checkbox" name="%screate_team" value="1"> Create the source junior team</label>`, prefix)
				}
			} else {
				fmt.Fprintf(w, `<label class="form-label">Existing club<select class="form-select" name="%sclub"><option value="">Select an existing club</option>`, prefix)
				for _, club := range preview.Clubs {
					fmt.Fprintf(w, `<option value="%d"%s>%s (#%d)</option>`, club.ID, selectedMode(fmt.Sprint(clubID), fmt.Sprint(club.ID)), escapeHTML(club.Name), club.ID)
				}
				fmt.Fprintf(w, `</select></label><label class="form-check"><input class="form-check-input" type="checkbox" name="%sskip" value="1"%s> Skip this row</label><label class="form-label mt-2">Reason if skipped<input class="form-control" name="%sreason" maxlength="300" value="%s"></label>`, prefix, map[bool]string{true: " checked", false: ""}[mapping.Skip], prefix, escapeHTML(mapping.Reason))
			}
			fmt.Fprint(w, `</fieldset>`)
		}
		if apply {
			fmt.Fprint(w, `<label class="form-check mb-3"><input class="form-check-input" type="checkbox" name="confirm" value="apply" required> I have reviewed every mapping and approve applying these junior records.</label><button class="btn btn-primary">Apply reviewed roster once</button>`)
		} else {
			fmt.Fprint(w, `<button class="btn btn-primary">Confirm club mappings and review contacts</button>`)
		}
		fmt.Fprint(w, `</form>`)
		juniorPageEnd(w)
	}
}
