package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleClubSearch returns JSON array of {id, name} matching the query.
// GET /api/clubs/search?q=droyl
func (s *Server) handleClubSearch() http.HandlerFunc {
	type club struct {
		ID   int32  `json:"id"`
		Name string `json:"name"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) < 2 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		rows, err := s.DB.Query(ctx, `
			SELECT id, name FROM clubs
			WHERE name ILIKE '%' || $1 || '%'
			ORDER BY name
			LIMIT 15
		`, q)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		clubs := []club{}
		for rows.Next() {
			var c club
			if err := rows.Scan(&c.ID, &c.Name); err != nil {
				continue
			}
			clubs = append(clubs, c)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(clubs)
	}
}

// handleTeamsByClub returns JSON array of {id, name} for a given club.
// GET /api/teams?club_id=5
func (s *Server) handleTeamsByClub() http.HandlerFunc {
	type team struct {
		ID   int32  `json:"id"`
		Name string `json:"name"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		clubID, _ := strconv.Atoi(r.URL.Query().Get("club_id"))
		if clubID <= 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		rows, err := s.DB.Query(ctx, `
			SELECT id, name FROM teams
			WHERE club_id = $1 AND active = TRUE
			ORDER BY name
		`, clubID)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		teams := []team{}
		for rows.Next() {
			var t team
			if err := rows.Scan(&t.ID, &t.Name); err != nil {
				continue
			}
			teams = append(teams, t)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(teams)
	}
}

// handleCaptainByTeam returns JSON {name, email} for the active captain of a team.
// GET /api/captain?team_id=12
func (s *Server) handleCaptainByTeam() http.HandlerFunc {
	type captain struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		teamID, _ := strconv.Atoi(r.URL.Query().Get("team_id"))
		if teamID <= 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		var c captain
		err := s.DB.QueryRow(ctx, `
			SELECT full_name, email FROM captains
			WHERE team_id = $1 AND (active_to IS NULL OR active_to >= CURRENT_DATE)
			ORDER BY active_from DESC
			LIMIT 1
		`, teamID).Scan(&c.Name, &c.Email)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c)
	}
}
