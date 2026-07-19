package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/starred"

	"github.com/jackc/pgx/v5"
)

type starredCandidateReviewState struct {
	ID     int64
	Status string
}

func starredCandidateKey(year int, candidate starred.Candidate) string {
	identity := candidate.PlayerKey
	if candidate.PlayerID > 0 {
		identity = "id:" + strconv.FormatInt(candidate.PlayerID, 10)
	}
	raw := fmt.Sprintf("%d|%s|%s", year, candidate.ClubKey, identity)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Server) loadStarredCandidateReviewStates(ctx context.Context, seasonYear int) map[string]starredCandidateReviewState {
	states := make(map[string]starredCandidateReviewState)
	rows, err := s.DB.Query(ctx, `SELECT candidate_key,id,status FROM starred_candidate_reviews WHERE season_year=$1`, seasonYear)
	if err != nil {
		return states
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var state starredCandidateReviewState
		if rows.Scan(&key, &state.ID, &state.Status) == nil {
			states[key] = state
		}
	}
	return states
}

func starredCandidateActionsHTML(candidate starred.Candidate, csrf string, year int) string {
	appearanceSearch := starredAppearanceSearch(candidate.PlayerName, candidate.PlayerID)
	hidden := fmt.Sprintf(`<input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="club_key" value="%s"><input type="hidden" name="player_id" value="%d"><input type="hidden" name="player_key" value="%s">`, escapeHTML(csrf), year, escapeHTML(candidate.ClubKey), candidate.PlayerID, escapeHTML(candidate.PlayerKey))
	return fmt.Sprintf(`<div class="d-flex flex-wrap gap-1" style="min-width:220px"><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">Review games</a><form method="post" action="/admin/starred-players/candidates/accept" onsubmit="return confirm('Accept and close this List B review? It will leave the outstanding list but remain in the audit trail.')">%s<button class="btn btn-sm btn-outline-success">Accept / close</button></form></div>`, year, url.QueryEscape(appearanceSearch), hidden)
}

func (s *Server) verifiedStarredCandidate(ctx context.Context, year int, playerID int64, clubKey, playerKey string) (starred.Candidate, error) {
	periods, appearances, mappings, _, err := starred.LoadEvaluationInputs(ctx, s.DB, year)
	if err != nil {
		return starred.Candidate{}, err
	}
	cutoff := starred.ReviewCutoff(year, time.Now())
	reviewAppearances := make([]starred.Appearance, 0, len(appearances))
	for _, appearance := range appearances {
		if appearance.TeamLevel > 0 && !appearance.MatchDate.After(cutoff) && !starred.IsWomensAppearance(appearance) {
			reviewAppearances = append(reviewAppearances, appearance)
		}
	}
	reviewAppearances = remapStarredAppearanceClubs(reviewAppearances, s.loadStarredAppearanceClubOverrides(ctx, year), activeStarredClubNames(periods, cutoff))
	for _, candidate := range starred.Evaluate(periods, reviewAppearances, mappings, cutoff).Candidates {
		if candidate.AlreadyStarred || candidate.ClubKey != clubKey {
			continue
		}
		if playerID > 0 && candidate.PlayerID == playerID {
			return candidate, nil
		}
		if playerID == 0 && candidate.PlayerID == 0 && candidate.PlayerKey == playerKey {
			return candidate, nil
		}
	}
	return starred.Candidate{}, fmt.Errorf("candidate is no longer present in the 31 July List B review")
}

func parseStarredCandidateForm(r *http.Request) (year int, playerID int64, clubKey, playerKey string, err error) {
	if err = r.ParseForm(); err != nil {
		return
	}
	year, err = strconv.Atoi(strings.TrimSpace(r.FormValue("season")))
	if err != nil || year < 2000 || year > 2100 {
		err = fmt.Errorf("invalid season")
		return
	}
	playerID, _ = strconv.ParseInt(strings.TrimSpace(r.FormValue("player_id")), 10, 64)
	clubKey = strings.TrimSpace(r.FormValue("club_key"))
	playerKey = strings.TrimSpace(r.FormValue("player_key"))
	if clubKey == "" || (playerID <= 0 && playerKey == "") {
		err = fmt.Errorf("invalid List B candidate")
	}
	return
}

func (s *Server) handleAdminStarredCandidateAccept() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, playerID, clubKey, playerKey, err := parseStarredCandidateForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		candidate, err := s.verifiedStarredCandidate(ctx, year, playerID, clubKey, playerKey)
		if err != nil {
			redirectStarredAnchor(w, r, year, "", err.Error(), "july-31-test")
			return
		}
		adminID := s.resolveAdminID(r)
		var reviewID int64
		err = s.DB.QueryRow(ctx, `
			INSERT INTO starred_candidate_reviews(candidate_key,season_year,club_name,club_key,play_cricket_player_id,player_name,player_key,first_xi_league,all_league,percentage,status,decision_note,reviewed_by,reviewed_at)
			VALUES($1,$2,$3,$4,NULLIF($5,0),$6,$7,$8,$9,$10,'accepted',NULLIF($11,''),$12,now())
			ON CONFLICT(candidate_key) DO NOTHING
			RETURNING id`, starredCandidateKey(year, candidate), year, candidate.ClubName, candidate.ClubKey, candidate.PlayerID, candidate.PlayerName, candidate.PlayerKey, candidate.FirstXILeague, candidate.AllLeague, candidate.Percentage, strings.TrimSpace(r.FormValue("decision_note")), adminID).Scan(&reviewID)
		if errors.Is(err, pgx.ErrNoRows) {
			redirectStarredAnchor(w, r, year, "This List B review was already accepted and closed.", "", "july-31-test")
			return
		}
		if err != nil {
			redirectStarredAnchor(w, r, year, "", "Could not close List B review: "+err.Error(), "july-31-test")
			return
		}
		s.audit(ctx, r, "admin", adminID, "starred_list_b_candidate_accepted", "starred_candidate_review", &reviewID, map[string]any{
			"season": year, "club": candidate.ClubName, "player": candidate.PlayerName,
			"play_cricket_player_id": candidate.PlayerID, "first_xi_league": candidate.FirstXILeague,
			"all_league": candidate.AllLeague, "percentage": candidate.Percentage,
		})
		redirectStarredAnchor(w, r, year, "List B review accepted and removed from the outstanding list for "+candidate.PlayerName+".", "", "july-31-test")
	}
}
