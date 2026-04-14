package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

type captainFormConfig struct {
	SeasonID  int32
	Title     string
	IntroText string
}

type seasonOption struct {
	ID   int32
	Name string
}

func (s *Server) handleAdminCaptainFormSettingsGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		seasons, selectedSeasonID, err := s.loadCaptainFormSeasons(ctx, r)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		cfg, err := s.loadCaptainFormConfig(ctx, selectedSeasonID)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.renderCaptainFormSettingsPage(w, r, seasons, cfg, "")
	}
}

func (s *Server) handleAdminCaptainFormSettingsPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}

		seasonID, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("season_id")))
		title := strings.TrimSpace(r.FormValue("title"))
		introText := strings.TrimSpace(r.FormValue("intro_text"))
		if seasonID <= 0 || title == "" || introText == "" {
			http.Error(w, "season, title and intro text are required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		_, err := s.DB.Exec(ctx, `
			INSERT INTO captain_form_config (season_id, title, intro_text, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (season_id)
			DO UPDATE SET
				title = EXCLUDED.title,
				intro_text = EXCLUDED.intro_text,
				updated_at = now()
		`, int32(seasonID), title, introText)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		seasons, _, err := s.loadCaptainFormSeasons(ctx, r)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		cfg := captainFormConfig{
			SeasonID:  int32(seasonID),
			Title:     title,
			IntroText: introText,
		}
		s.audit(ctx, r, "admin", nil, "captain_form_config_updated", "captain_form_config", nil, map[string]any{
			"season_id": seasonID,
		})
		s.renderCaptainFormSettingsPage(w, r, seasons, cfg, "Captain form content updated.")
	}
}

func (s *Server) loadCaptainFormSeasons(ctx context.Context, r *http.Request) ([]seasonOption, int32, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, name FROM seasons ORDER BY start_date DESC, id DESC`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var seasons []seasonOption
	for rows.Next() {
		var opt seasonOption
		if err := rows.Scan(&opt.ID, &opt.Name); err != nil {
			return nil, 0, err
		}
		seasons = append(seasons, opt)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	selected := int32(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("season_id")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			selected = int32(parsed)
		}
	}
	if selected == 0 && len(seasons) > 0 {
		selected = seasons[0].ID
	}
	return seasons, selected, nil
}

func (s *Server) loadCaptainFormConfig(ctx context.Context, seasonID int32) (captainFormConfig, error) {
	cfg := captainFormConfig{
		SeasonID:  seasonID,
		Title:     "GMCL Captain's Report - Prem 1, Prem 2, Championship & Division 1",
		IntroText: "This form is for you to comment on:\n\n1. The performance of the umpires at your game for GMCLUA.\n2. The pitch and ground conditions to meet ECB requirements.\n\nComments about league playing regulations should go to rules@gtrmcricket.co.uk.",
	}
	if seasonID == 0 {
		return cfg, nil
	}
	_ = s.DB.QueryRow(ctx, `
		SELECT title, intro_text
		FROM captain_form_config
		WHERE season_id = $1
	`, seasonID).Scan(&cfg.Title, &cfg.IntroText)
	return cfg, nil
}

func (s *Server) renderCaptainFormSettingsPage(w http.ResponseWriter, r *http.Request, seasons []seasonOption, cfg captainFormConfig, successMsg string) {
	csrfToken := middleware.CSRFToken(r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHead(w, "Form Settings")
	writeAdminNav(w, csrfToken, r.URL.Path)

	fmt.Fprint(w, `<div class="container-fluid" style="max-width:980px">
<h3 class="mb-3">Captain Form Settings</h3>
<p class="text-muted">Change the captain form title and intro text without a code deploy. Settings are stored per season.</p>
`)
	if successMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(successMsg))
	}
	fmt.Fprint(w, `<div class="card card-gmcl shadow-sm"><div class="card-body">`)
	fmt.Fprintf(w, `<form method="GET" action="/admin/form-settings" class="row g-3 align-items-end mb-4">
  <div class="col-md-4">
    <label class="form-label">Season</label>
    <select class="form-select" name="season_id" onchange="this.form.submit()">`)
	for _, season := range seasons {
		sel := ""
		if season.ID == cfg.SeasonID {
			sel = " selected"
		}
		fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, season.ID, sel, escapeHTML(season.Name))
	}
	fmt.Fprint(w, `</select></div></form>`)

	fmt.Fprintf(w, `<form method="POST" action="/admin/form-settings">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="season_id" value="%d">
  <div class="mb-3">
    <label class="form-label">Form title</label>
    <input type="text" class="form-control" name="title" value="%s" required>
  </div>
  <div class="mb-3">
    <label class="form-label">Intro text</label>
    <textarea class="form-control" name="intro_text" rows="10" required>%s</textarea>
    <div class="form-text">Plain text only. Leave blank lines to create paragraph breaks.</div>
  </div>
  <button type="submit" class="btn btn-primary">Save</button>
</form>`, escapeHTML(csrfToken), cfg.SeasonID, escapeHTML(cfg.Title), escapeHTML(cfg.IntroText))

	fmt.Fprint(w, `</div></div></div>`)
	pageFooter(w)
}
