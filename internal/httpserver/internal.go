package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"cricket-ground-feedback/internal/leagueapi"
)

type sendRemindersRequest struct {
	SeasonID int32   `json:"season_id"`
	WeekID   int32   `json:"week_id"`
	TeamIDs  []int32 `json:"team_ids"`
}

type internalResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// handleInternalSendReminders is called by n8n via HMAC-authenticated endpoint.
func (s *Server) handleInternalSendReminders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req sendRemindersRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		// For MVP: fetch teams with schedules and log; real implementation would enqueue emails.
		rows, err := s.DB.Query(ctx, `
			SELECT rs.team_id
			FROM reminder_schedules rs
			WHERE rs.season_id = $1
			  AND rs.active = TRUE
		`, req.SeasonID)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var teamIDs []int32
		for rows.Next() {
			var id int32
			if err := rows.Scan(&id); err != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}
			teamIDs = append(teamIDs, id)
		}

		s.audit(ctx, r, "n8n", nil, "internal_send_reminders", "reminder", nil, map[string]any{
			"season_id":  req.SeasonID,
			"week_id":    req.WeekID,
			"team_count": len(teamIDs),
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(internalResponse{Status: "ok"})
	}
}

// handleInternalGenerateWeeklyReport computes weekly stats and stores AI summary skeleton.
func (s *Server) handleInternalGenerateWeeklyReport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SeasonID int32 `json:"season_id"`
			WeekID   int32 `json:"week_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		var total int64
		var avgPitch float64
		err := s.DB.QueryRow(ctx, `
			SELECT COUNT(*), COALESCE(AVG(pitch_rating),0)
			FROM submissions
			WHERE season_id = $1 AND week_id = $2
		`, req.SeasonID, req.WeekID).Scan(&total, &avgPitch)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		summary := map[string]any{
			"total_submissions": total,
			"average_pitch":     avgPitch,
		}

		payload, err := json.Marshal(summary)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		_, err = s.DB.Exec(ctx, `
			INSERT INTO ai_summaries (season_id, week_id, summary_json, status)
			VALUES ($1, $2, $3, 'draft')
			ON CONFLICT (season_id, week_id)
			DO UPDATE SET summary_json = EXCLUDED.summary_json,
			              status = 'draft',
			              created_at = now()
		`, req.SeasonID, req.WeekID, payload)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "n8n", nil, "internal_generate_weekly_report", "ai_summary", nil, map[string]any{
			"season_id": req.SeasonID,
			"week_id":   req.WeekID,
			"total":     total,
			"avg_pitch": avgPitch,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(internalResponse{Status: "ok"})
	}
}

// handleInternalSyncLeagueFixtures pulls match details from the league / Play-Cricket API (or accepts raw JSON)
// and upserts into league_fixtures. Secured by HMAC like other /internal routes.
func (s *Server) handleInternalSyncLeagueFixtures() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MatchDate string          `json:"match_date"`
			SeasonID  *int32          `json:"season_id"`
			RawBody   json.RawMessage `json:"raw_body"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		var details []leagueapi.MatchDetail
		if len(req.RawBody) > 0 {
			parsed, err := leagueapi.ParseMatchDetailsJSON(req.RawBody)
			if err != nil {
				http.Error(w, "invalid raw_body: "+err.Error(), http.StatusBadRequest)
				return
			}
			details = parsed.MatchDetails
		} else {
			if req.MatchDate == "" {
				http.Error(w, "match_date required when raw_body omitted", http.StatusBadRequest)
				return
			}
			md, err := time.Parse("2006-01-02", req.MatchDate)
			if err != nil {
				http.Error(w, "invalid match_date (use YYYY-MM-DD)", http.StatusBadRequest)
				return
			}
			cfg := leagueapi.NewConfigFromEnv()
			client := leagueapi.NewClient(cfg)
			var err2 error
			details, err2 = client.FetchMatchesForDate(ctx, md)
			if err2 != nil {
				http.Error(w, err2.Error(), http.StatusBadGateway)
				return
			}
		}

		if err := leagueapi.UpsertFixtureBatch(ctx, s.DB, req.SeasonID, details); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.audit(ctx, r, "n8n", nil, "internal_sync_league_fixtures", "league_fixture", nil, map[string]any{
			"count":      len(details),
			"match_date": req.MatchDate,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"count":  len(details),
		})
	}
}
