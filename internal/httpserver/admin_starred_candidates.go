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

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/starred"

	"github.com/jackc/pgx/v5"
)

type starredCandidateReviewState struct {
	ID             int64
	Status         string
	EmailRecipient string
	EmailSentAt    *time.Time
	EmailSendError string
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
	rows, err := s.DB.Query(ctx, `SELECT candidate_key,id,status,COALESCE(email_recipient,''),email_sent_at,COALESCE(email_send_error,'') FROM starred_candidate_reviews WHERE season_year=$1`, seasonYear)
	if err != nil {
		return states
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var state starredCandidateReviewState
		if rows.Scan(&key, &state.ID, &state.Status, &state.EmailRecipient, &state.EmailSentAt, &state.EmailSendError) == nil {
			states[key] = state
		}
	}
	return states
}

func starredCandidateActionsHTML(candidate starred.Candidate, csrf string, year int) string {
	appearanceSearch := starredAppearanceSearch(candidate.PlayerName, candidate.PlayerID)
	hidden := fmt.Sprintf(`<input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="club_key" value="%s"><input type="hidden" name="player_id" value="%d"><input type="hidden" name="player_key" value="%s">`, escapeHTML(csrf), year, escapeHTML(candidate.ClubKey), candidate.PlayerID, escapeHTML(candidate.PlayerKey))
	return fmt.Sprintf(`<div class="d-flex flex-wrap gap-1" style="min-width:220px"><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">Review games</a><form method="post" action="/admin/starred-players/candidates/accept" onsubmit="return confirm('Accept and close this List B review? It will leave the outstanding list but remain in the audit trail.')">%s<button class="btn btn-sm btn-outline-primary">Accept / close</button></form></div>`, year, url.QueryEscape(appearanceSearch), hidden)
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
			INSERT INTO starred_candidate_reviews(candidate_key,season_year,club_name,club_key,play_cricket_player_id,player_name,player_key,first_xi_league,first_second_xi_league,all_league,percentage,status,decision_note,reviewed_by,reviewed_at)
			VALUES($1,$2,$3,$4,NULLIF($5,0),$6,$7,$8,$9,$10,$11,'accepted',NULLIF($12,''),$13,now())
			ON CONFLICT(candidate_key) DO NOTHING
			RETURNING id`, starredCandidateKey(year, candidate), year, candidate.ClubName, candidate.ClubKey, candidate.PlayerID, candidate.PlayerName, candidate.PlayerKey, candidate.FirstXILeague, candidate.TopTwoXILeague, candidate.AllLeague, candidate.Percentage, strings.TrimSpace(r.FormValue("decision_note")), adminID).Scan(&reviewID)
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
			"play_cricket_player_id": candidate.PlayerID, "first_xi_league": candidate.FirstXILeague, "first_second_xi_league": candidate.TopTwoXILeague,
			"all_league": candidate.AllLeague, "percentage": candidate.Percentage,
		})
		redirectStarredAnchor(w, r, year, "List B review accepted and removed from the outstanding list for "+candidate.PlayerName+".", "", "july-31-test")
	}
}

func (s *Server) verifiedStarredPlayerRequest(ctx context.Context, year int, cutoff time.Time, playerID int64, clubKey, playerKey string) (starredPlayerReviewRow, error) {
	periods, appearances, mappings, _, err := starred.LoadEvaluationInputs(ctx, s.DB, year)
	if err != nil {
		return starredPlayerReviewRow{}, err
	}
	appearances = remapStarredAppearanceClubs(appearances, s.loadStarredAppearanceClubOverrides(ctx, year), activeStarredClubNames(periods, cutoff))
	rows := buildStarredPlayerReviewRows(periods, appearances, mappings, cutoff, nil, nil, clubKey, 50, 25)
	for _, row := range rows {
		if row.ListType != "" || row.FirstPct < 50 {
			continue
		}
		if playerID > 0 && row.PlayerID == playerID {
			return row, nil
		}
		if playerID == 0 && row.PlayerID == 0 && row.PlayerKey == playerKey {
			return row, nil
		}
	}
	return starredPlayerReviewRow{}, fmt.Errorf("player is not an unstarred 50%% 1st XI candidate at this review date")
}

func redirectStarredPlayerReview(w http.ResponseWriter, r *http.Request, year int, cutoff time.Time, message, errMsg string) {
	query := url.Values{
		"season":      {strconv.Itoa(year)},
		"view":        {"player-review"},
		"review_date": {cutoff.Format("2006-01-02")},
	}
	for _, division := range r.Form["division"] {
		if division = strings.TrimSpace(division); division != "" {
			query.Add("division", division)
		}
	}
	if club := strings.TrimSpace(r.FormValue("club")); club != "" {
		query.Set("club", club)
	}
	if message != "" {
		query.Set("message", message)
	}
	if errMsg != "" {
		query.Set("error", errMsg)
	}
	http.Redirect(w, r, "/admin/starred-players?"+query.Encode()+"#card-detail", http.StatusSeeOther)
}

func (s *Server) handleAdminStarredCandidateRequest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, playerID, clubKey, playerKey, err := parseStarredCandidateForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		maximum := starred.ReviewCutoff(year, time.Now())
		cutoff := starredPlayerReviewCutoff(r, year, maximum)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		row, err := s.verifiedStarredPlayerRequest(ctx, year, cutoff, playerID, clubKey, playerKey)
		if err != nil {
			redirectStarredPlayerReview(w, r, year, cutoff, "", err.Error())
			return
		}
		candidate := starred.Candidate{
			ClubName: row.ClubName, ClubKey: row.ClubKey, PlayerID: row.PlayerID,
			PlayerName: row.PlayerName, PlayerKey: row.PlayerKey, FirstXILeague: row.Counts[1],
			TopTwoXILeague: row.Counts[1] + row.Counts[2], AllLeague: row.Total, Percentage: row.FirstPct / 100,
		}
		recipient, err := starredClubEmail(row.ClubKey, row.ClubName)
		if err != nil {
			redirectStarredPlayerReview(w, r, year, cutoff, "", "Could not determine club email: "+err.Error())
			return
		}
		adminID := s.resolveAdminID(r)
		var reviewID int64
		var status string
		var emailSentAt *time.Time
		err = s.DB.QueryRow(ctx, `
			INSERT INTO starred_candidate_reviews(candidate_key,season_year,club_name,club_key,play_cricket_player_id,player_name,player_key,first_xi_league,first_second_xi_league,all_league,percentage,status,decision_note,reviewed_by,reviewed_at,requested_cutoff,email_recipient)
			VALUES($1,$2,$3,$4,NULLIF($5,0),$6,$7,$8,$9,$10,$11,'requested',NULL,$12,now(),$13::date,$14)
			ON CONFLICT(candidate_key) DO UPDATE SET
				club_name=EXCLUDED.club_name, player_name=EXCLUDED.player_name,
				first_xi_league=EXCLUDED.first_xi_league, first_second_xi_league=EXCLUDED.first_second_xi_league,
				all_league=EXCLUDED.all_league, percentage=EXCLUDED.percentage,
				requested_cutoff=EXCLUDED.requested_cutoff, reviewed_by=EXCLUDED.reviewed_by,
				email_recipient=EXCLUDED.email_recipient, reviewed_at=now(), updated_at=now()
			WHERE starred_candidate_reviews.status <> 'accepted'
			RETURNING id,status,email_sent_at`,
			starredCandidateKey(year, candidate), year, row.ClubName, row.ClubKey, row.PlayerID, row.PlayerName, row.PlayerKey,
			row.Counts[1], row.Counts[1]+row.Counts[2], row.Total, row.FirstPct/100, adminID, cutoff, recipient).Scan(&reviewID, &status, &emailSentAt)
		if errors.Is(err, pgx.ErrNoRows) {
			redirectStarredPlayerReview(w, r, year, cutoff, "This player's review has already been closed.", "")
			return
		}
		if err != nil {
			redirectStarredPlayerReview(w, r, year, cutoff, "", "Could not request List B review: "+err.Error())
			return
		}
		if emailSentAt != nil {
			redirectStarredPlayerReview(w, r, year, cutoff, "The List B review email was already sent to "+recipient+".", "")
			return
		}
		subject, body := starredCandidateRequestEmail(row, cutoff)
		if err := email.NewFromEnv().Send(recipient, subject, body); err != nil {
			_, _ = s.DB.Exec(ctx, `UPDATE starred_candidate_reviews SET email_send_error=$1,email_sent_at=NULL,updated_at=now() WHERE id=$2`, err.Error(), reviewID)
			s.audit(ctx, r, "admin", adminID, "starred_list_b_candidate_email_failed", "starred_candidate_review", &reviewID, map[string]any{
				"season": year, "club": row.ClubName, "player": row.PlayerName, "recipient": recipient, "error": err.Error(),
			})
			redirectStarredPlayerReview(w, r, year, cutoff, "", "List B review was recorded, but the email to "+recipient+" failed: "+err.Error())
			return
		}
		_, _ = s.DB.Exec(ctx, `UPDATE starred_candidate_reviews SET email_sent_at=now(),email_send_error=NULL,updated_at=now() WHERE id=$1`, reviewID)
		s.audit(ctx, r, "admin", adminID, "starred_list_b_candidate_requested", "starred_candidate_review", &reviewID, map[string]any{
			"season": year, "cutoff": cutoff.Format("2006-01-02"), "club": row.ClubName, "player": row.PlayerName,
			"play_cricket_player_id": row.PlayerID, "first_xi_games": row.Counts[1],
			"first_xi_team_fixtures": row.TeamGames[1], "percentage": row.FirstPct / 100, "recipient": recipient,
		})
		redirectStarredPlayerReview(w, r, year, cutoff, "List B review requested for "+row.PlayerName+" and emailed to "+recipient+".", "")
	}
}
