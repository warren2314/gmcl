package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/leagueapi"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

var (
	errScorecardFixtureMissing   = errors.New("no Play-Cricket fixture matches the mapped team and fixture date")
	errScorecardFixtureAmbiguous = errors.New("more than one Play-Cricket fixture matches the mapped team and fixture date")
)

type scorecardEvidenceResult struct {
	MatchID int64
	Players int
	Already bool
}

// resolveIneligibleScorecardMatch only accepts a stored match ID or one exact
// team-ID/date match. It deliberately does not fuzzy-match club or team names.
func (s *Server) resolveIneligibleScorecardMatch(ctx context.Context, caseID int64) (int64, string, time.Time, error) {
	var source, teamPCID string
	var matchID *int64
	var matchDate *time.Time
	err := s.DB.QueryRow(ctx, `SELECT c.source_type,c.play_cricket_match_id,c.match_date,COALESCE(t.play_cricket_team_id,'')
		FROM sanction_cases c LEFT JOIN teams t ON t.id=c.team_id WHERE c.id=$1`, caseID).
		Scan(&source, &matchID, &matchDate, &teamPCID)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	if source != "ineligible_player" {
		return 0, "", time.Time{}, errors.New("scorecard collection is only available for ineligible-player cases")
	}
	if matchDate == nil || strings.TrimSpace(teamPCID) == "" {
		return 0, "", time.Time{}, errors.New("map the case to a Play-Cricket team and fixture date first")
	}
	if matchID != nil && *matchID > 0 {
		return *matchID, strings.TrimSpace(teamPCID), *matchDate, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT play_cricket_match_id FROM league_fixtures
		WHERE match_date=$1::date AND play_cricket_match_id>0
		  AND (COALESCE(home_team_pc_id,'')=$2 OR COALESCE(away_team_pc_id,'')=$2)
		ORDER BY play_cricket_match_id`, *matchDate, strings.TrimSpace(teamPCID))
	if err != nil {
		return 0, "", time.Time{}, err
	}
	defer rows.Close()
	ids := make([]int64, 0, 2)
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return 0, "", time.Time{}, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return 0, "", time.Time{}, err
	}
	if len(ids) == 0 {
		return 0, "", time.Time{}, errScorecardFixtureMissing
	}
	if len(ids) != 1 {
		return 0, "", time.Time{}, errScorecardFixtureAmbiguous
	}
	return ids[0], strings.TrimSpace(teamPCID), *matchDate, nil
}

func scorecardMatchID(match leagueapi.ScorecardMatch) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(match.MatchID), 10, 64)
	if id <= 0 {
		id = match.ID
	}
	return id
}

func validateScorecardForCase(match leagueapi.ScorecardMatch, expectedID int64, teamPCID string, expectedDate time.Time) error {
	if scorecardMatchID(match) != expectedID {
		return errors.New("Play-Cricket returned a different match ID")
	}
	date, err := leagueapi.ParseMatchDate(match.MatchDate, "")
	if err != nil || date.Format("2006-01-02") != expectedDate.Format("2006-01-02") {
		return errors.New("Play-Cricket scorecard date does not match the case fixture date")
	}
	teamPCID = strings.TrimSpace(teamPCID)
	if strings.TrimSpace(match.HomeTeamID) != teamPCID && strings.TrimSpace(match.AwayTeamID) != teamPCID {
		return errors.New("Play-Cricket scorecard does not contain the mapped case team")
	}
	return nil
}

func retainScorecardBytes(raw []byte) (string, string, error) {
	if len(raw) == 0 || len(raw) > 8<<20 {
		return "", "", errors.New("Play-Cricket scorecard evidence is empty or too large")
	}
	if err := os.MkdirAll(evidenceDir(), 0700); err != nil {
		return "", "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	key := hex.EncodeToString(random)
	path := filepath.Join(evidenceDir(), key)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return key, hex.EncodeToString(sum[:]), nil
}

func (s *Server) collectIneligibleScorecardEvidence(ctx context.Context, caseID int64, adminID int32, actorLabel, requestID string) (scorecardEvidenceResult, error) {
	matchID, teamPCID, matchDate, err := s.resolveIneligibleScorecardMatch(ctx, caseID)
	if err != nil {
		return scorecardEvidenceResult{}, err
	}
	detail, raw, err := leagueapi.NewClient(leagueapi.NewConfigFromEnv()).FetchMatchDetail(ctx, matchID)
	if err != nil {
		return scorecardEvidenceResult{}, fmt.Errorf("fetch Play-Cricket scorecard: %w", err)
	}
	if err = validateScorecardForCase(*detail, matchID, teamPCID, matchDate); err != nil {
		return scorecardEvidenceResult{}, err
	}
	sumBytes := sha256.Sum256(raw)
	sum := hex.EncodeToString(sumBytes[:])
	var existingID int64
	err = s.DB.QueryRow(ctx, `SELECT evidence_id FROM sanction_case_scorecard_evidence
		WHERE case_id=$1 AND play_cricket_match_id=$2 AND source_sha256=$3`, caseID, matchID, sum).Scan(&existingID)
	if err == nil {
		return scorecardEvidenceResult{MatchID: matchID, Players: len(detail.Players.HomeTeam) + len(detail.Players.AwayTeam), Already: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return scorecardEvidenceResult{}, err
	}
	key, retainedSum, err := retainScorecardBytes(raw)
	if err != nil {
		return scorecardEvidenceResult{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(filepath.Join(evidenceDir(), filepath.Base(key)))
		}
	}()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return scorecardEvidenceResult{}, err
	}
	defer tx.Rollback(ctx)
	var eventID int64
	err = tx.QueryRow(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id)
		VALUES($1,'play_cricket_scorecard_collected','admin',$2,$3,$4,jsonb_build_object('play_cricket_match_id',$5::bigint,'sha256',$6::text),$7) RETURNING id`,
		caseID, adminID, actorLabel, fmt.Sprintf("Immutable Play-Cricket scorecard snapshot collected for match %d", matchID), matchID, retainedSum, requestID).Scan(&eventID)
	var evidenceID int64
	if err == nil {
		err = tx.QueryRow(ctx, `INSERT INTO sanction_case_evidence(case_id,event_id,visibility,original_name,media_type,byte_size,storage_key,sha256,uploaded_by_type,uploaded_by_id)
			VALUES($1,$2,'private',$3,'application/json',$4,$5,$6,'play_cricket',$7) RETURNING id`,
			caseID, eventID, fmt.Sprintf("play-cricket-scorecard-%d.json", matchID), len(raw), key, retainedSum, adminID).Scan(&evidenceID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO sanction_case_scorecard_evidence(case_id,evidence_id,play_cricket_match_id,source_last_updated,source_sha256,snapshot_payload,fetched_by_admin_id)
			VALUES($1,$2,$3,$4,$5,$6::jsonb,$7)`, caseID, evidenceID, matchID, nullIfEmptyHTTP(detail.LastUpdated), retainedSum, raw, adminID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE sanction_cases SET play_cricket_match_id=COALESCE(play_cricket_match_id,$2),updated_at=now() WHERE id=$1`, caseID, matchID)
	}
	if err != nil {
		return scorecardEvidenceResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return scorecardEvidenceResult{}, err
	}
	keep = true
	return scorecardEvidenceResult{MatchID: matchID, Players: len(detail.Players.HomeTeam) + len(detail.Players.AwayTeam)}, nil
}

func (s *Server) handleAdminCaseScorecardEvidence() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		actor := adminActor(r)
		if err != nil || caseID < 1 || actor.ID == nil {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		result, err := s.collectIneligibleScorecardEvidence(r.Context(), caseID, *actor.ID, actor.Label, actor.RequestID)
		if err != nil {
			slog.Warn("collect Play-Cricket scorecard evidence", "case_id", caseID, "error", err)
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?error=%s", caseID, urlQueryEscape(scorecardCollectionMessage(err))), http.StatusSeeOther)
			return
		}
		message := fmt.Sprintf("Play-Cricket scorecard %d retained with %d players.", result.MatchID, result.Players)
		if result.Already {
			message = fmt.Sprintf("The current Play-Cricket scorecard %d snapshot is already retained.", result.MatchID)
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", caseID, urlQueryEscape(message)), http.StatusSeeOther)
	}
}

func scorecardCollectionMessage(err error) string {
	switch {
	case errors.Is(err, errScorecardFixtureMissing):
		return "No exact Play-Cricket fixture was found for this mapped team and date. Sync fixtures, check the case mapping, then retry."
	case errors.Is(err, errScorecardFixtureAmbiguous):
		return "More than one Play-Cricket fixture matched. Set the correct match ID before collecting evidence."
	default:
		return "The Play-Cricket scorecard could not be collected: " + err.Error()
	}
}

func (s *Server) writeAdminScorecardEvidence(w http.ResponseWriter, ctx context.Context, caseID int64, csrf string) {
	rows, err := s.DB.Query(ctx, `SELECT snapshot.play_cricket_match_id,snapshot.fetched_at,snapshot.source_sha256,snapshot.snapshot_payload,evidence.id
		FROM sanction_case_scorecard_evidence snapshot JOIN sanction_case_evidence evidence ON evidence.id=snapshot.evidence_id
		WHERE snapshot.case_id=$1 ORDER BY snapshot.id DESC`, caseID)
	if err != nil {
		return
	}
	defer rows.Close()
	type snapshot struct {
		matchID, evidenceID int64
		fetched             time.Time
		digest              string
		raw                 []byte
	}
	items := []snapshot{}
	for rows.Next() {
		var item snapshot
		if rows.Scan(&item.matchID, &item.fetched, &item.digest, &item.raw, &item.evidenceID) == nil {
			items = append(items, item)
		}
	}
	fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header d-flex justify-content-between align-items-center"><span>Play-Cricket scorecard evidence</span><form method="POST" action="/admin/cases/%d/scorecard-evidence"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-sm btn-outline-primary">Fetch latest scorecard</button></form></div><div class="card-body">`, caseID, escapeHTML(csrf))
	if len(items) == 0 {
		fmt.Fprint(w, `<p class="text-muted mb-0">No scorecard snapshot retained yet. The lookup requires one exact Play-Cricket team-and-date match and will not guess.</p>`)
	}
	for _, item := range items {
		parsed, parseErr := leagueapi.ParseScorecardJSON(item.raw)
		fmt.Fprintf(w, `<article class="border rounded p-3 mb-3"><div class="d-flex justify-content-between gap-2"><strong>Match %d</strong><a href="/admin/cases/%d/evidence/%d">Download original JSON</a></div><div class="small text-muted mb-2">Fetched %s &middot; SHA-256 %s</div>`, item.matchID, caseID, item.evidenceID, escapeHTML(item.fetched.In(s.LondonLoc).Format("02 Jan 2006 15:04")), escapeHTML(item.digest[:minInt(12, len(item.digest))]))
		if parseErr == nil && len(parsed.MatchDetails) > 0 {
			match := parsed.MatchDetails[0]
			fmt.Fprintf(w, `<div class="small mb-2"><strong>%s</strong> v <strong>%s</strong> &middot; %s</div><div class="row g-3">`, escapeHTML(match.HomeTeamName), escapeHTML(match.AwayTeamName), escapeHTML(match.MatchDate))
			for _, side := range []struct {
				name    string
				players []leagueapi.ScorecardPlayer
			}{{match.HomeTeamName, match.Players.HomeTeam}, {match.AwayTeamName, match.Players.AwayTeam}} {
				fmt.Fprintf(w, `<div class="col-md-6"><div class="fw-semibold">%s</div><ol class="small mb-0">`, escapeHTML(side.name))
				for _, player := range side.players {
					marks := ""
					if player.Captain {
						marks += " (c)"
					}
					if player.WicketKeeper {
						marks += " (wk)"
					}
					fmt.Fprintf(w, `<li>%s%s</li>`, escapeHTML(player.PlayerName), escapeHTML(marks))
				}
				fmt.Fprint(w, `</ol></div>`)
			}
			fmt.Fprint(w, `</div>`)
		}
		fmt.Fprint(w, `</article>`)
	}
	fmt.Fprint(w, `</div></section>`)
}
