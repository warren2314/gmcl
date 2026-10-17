package httpserver

import (
	"fmt"
	"io"
	"net/http"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

func juniorUploadBytes(w http.ResponseWriter, r *http.Request, field string, limit int64) (string, []byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit+(128<<10))
	if err := r.ParseMultipartForm(limit); err != nil {
		return "", nil, fmt.Errorf("%w: choose a file within the upload limit", junior.ErrValidation)
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile(field)
	if err != nil {
		return "", nil, fmt.Errorf("%w: choose a file to upload", junior.ErrValidation)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(data)) > limit {
		return "", nil, fmt.Errorf("%w: the selected file is too large", junior.ErrValidation)
	}
	return header.Filename, data, nil
}

func (s *Server) HandleJuniorBadgeUpload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, data, err := juniorUploadBytes(w, r, "badge", junior.BadgeUploadLimit)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		clubID, err := s.juniorRecordClubID(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		if _, err = junior.NewService(s.DB).SaveBadge(r.Context(), s.juniorPrincipal(r), clubID, name, data); err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		seasonID, _ := juniorRecordID(r.FormValue("season_id"), false)
		juniorRecordsRedirect(w, r, s.juniorPrincipal(r), clubID, seasonID, "Club badge saved")
	}
}

func (s *Server) HandleJuniorBadgeRead() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clubID, err := s.juniorRecordClubID(r)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		id, err := juniorRecordID(chi.URLParam(r, "badgeID"), true)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		badge, data, err := junior.NewService(s.DB).ReadBadge(r.Context(), s.juniorPrincipal(r), clubID, id)
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", badge.MediaType)
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	}
}

func writeJuniorBadgeSection(w http.ResponseWriter, r *http.Request, p junior.Principal, profile junior.ClubProfile, readOnly bool) {
	base := "/junior"
	if p.AdminID > 0 {
		base = fmt.Sprintf("/admin/juniors/clubs/%d", profile.Club.ID)
	}
	readBase := base
	if p.ReadOnly {
		readBase = fmt.Sprintf("/admin/juniors/clubs/%d/preview", profile.Club.ID)
	}
	fmt.Fprint(w, `<section class="card mb-4"><div class="card-header">Club badge</div><div class="card-body">`)
	if profile.BadgeID > 0 {
		fmt.Fprintf(w, `<img class="img-thumbnail mb-3" src="%s/badges/%d" alt="%s club badge" style="max-width:160px;max-height:160px">`, readBase, profile.BadgeID, escapeHTML(profile.Club.Name))
	} else {
		fmt.Fprint(w, `<p class="text-muted">No club badge uploaded yet.</p>`)
	}
	if !readOnly {
		fmt.Fprintf(w, `<form method="POST" enctype="multipart/form-data" action="%s/badge"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season_id" value="%d"><label class="form-label">Upload a PNG or JPEG badge<input class="form-control" type="file" name="badge" accept="image/png,image/jpeg" required></label><p class="small text-muted">Maximum 2 MB and 2048 pixels on each side. Previous badge revisions are retained.</p><button class="btn btn-outline-primary">Save badge</button></form>`, base, escapeHTML(middleware.CSRFToken(r)), profile.Season.ID)
	}
	if len(profile.Badges) > 0 {
		fmt.Fprint(w, `<details class="mt-3"><summary>Badge history</summary><ul>`)
		for _, badge := range profile.Badges {
			selected := ""
			if badge.ID == profile.BadgeID {
				selected = " · current"
			}
			fmt.Fprintf(w, `<li><a href="%s/badges/%d">%s</a> · %s%s</li>`, readBase, badge.ID, escapeHTML(badge.OriginalName), badge.CreatedAt.Format("02 Jan 2006 15:04"), selected)
		}
		fmt.Fprint(w, `</ul></details>`)
	}
	fmt.Fprint(w, `</div></section>`)
}
