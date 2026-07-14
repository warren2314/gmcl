package starred

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/leagueapi"

	"github.com/jackc/pgx/v5"
)

const DefaultPublishedCSVURL = "https://docs.google.com/spreadsheets/d/e/2PACX-1vS_jUUxFnK1V0zjnrHfIjp-MAEftFsjXrzjSj0hKBywU8T_r9KBQ8mmdo_agQp1BF5XEZC59-jIoILa/pub?gid=1336530032&single=true&output=csv"

type ImportResult struct {
	RunID          int64
	Entries        int
	Amendments     int
	Issues         int
	AlreadyCurrent bool
}

type ScorecardSyncResult struct {
	Matches     int
	Appearances int
	Failures    []string
}

func PublishedCSVURL() string {
	if v := strings.TrimSpace(os.Getenv("STARRED_PLAYERS_CSV_URL")); v != "" {
		return v
	}
	return DefaultPublishedCSVURL
}

func FetchSnapshot(ctx context.Context, seasonYear int) (Snapshot, []byte, string, error) {
	url := PublishedCSVURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Snapshot{}, nil, url, err
	}
	req.Header.Set("User-Agent", "GMCL-Starred-Player-Compliance/1.0")
	hc := &http.Client{Timeout: 25 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return Snapshot{}, nil, url, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Snapshot{}, nil, url, fmt.Errorf("starred list HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Snapshot{}, nil, url, err
	}
	if len(body) == 2<<20 {
		return Snapshot{}, nil, url, fmt.Errorf("starred list exceeds 2 MiB")
	}
	s, err := ParsePublishedCSV(strings.NewReader(string(body)), seasonYear)
	return s, body, url, err
}

func StoreSnapshot(ctx context.Context, pool *db.Pool, snapshot Snapshot, body []byte, sourceURL string, seasonStart time.Time) (ImportResult, error) {
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	var latestID int64
	var latestHash string
	err := pool.QueryRow(ctx, `SELECT id, source_sha256 FROM starred_import_runs WHERE season_year=$1 AND status='complete' ORDER BY imported_at DESC, id DESC LIMIT 1`, snapshot.SeasonYear).Scan(&latestID, &latestHash)
	if err == nil && latestHash == hash {
		return ImportResult{RunID: latestID, Entries: len(snapshot.Entries), Amendments: len(snapshot.Amendments), AlreadyCurrent: true}, nil
	}
	if err != nil && err != pgx.ErrNoRows {
		return ImportResult{}, err
	}

	periods, rosterIssues := BuildPeriods(snapshot, seasonStart)
	issueByAmendment := make(map[string]string)
	for _, issue := range rosterIssues {
		issueByAmendment[issue.ClubName+"|"+strconv.Itoa(issue.Sequence)] = issue.Reason
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	defer tx.Rollback(ctx)
	var runID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO starred_import_runs(season_year, source_url, source_sha256, entry_count, amendment_count, issue_count)
		VALUES($1,$2,$3,$4,$5,$6) RETURNING id
	`, snapshot.SeasonYear, sourceURL, hash, len(snapshot.Entries), len(snapshot.Amendments), len(rosterIssues)).Scan(&runID)
	if err != nil {
		return ImportResult{}, err
	}
	for _, e := range snapshot.Entries {
		_, err = tx.Exec(ctx, `INSERT INTO starred_list_entries(import_run_id,season_year,club_name,club_key,list_type,slot_number,player_name,player_key,raw_value,tags) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, runID, e.SeasonYear, e.ClubName, e.ClubKey, e.ListType, e.Slot, e.PlayerName, e.PlayerKey, e.RawValue, nonNilStrings(e.Tags))
		if err != nil {
			return ImportResult{}, err
		}
	}
	for _, club := range snapshot.Clubs {
		_, err = tx.Exec(ctx, `INSERT INTO starred_club_status(import_run_id,season_year,club_name,club_key,list_b_rule,submitted_count,no_form_submitted) VALUES($1,$2,$3,$4,$5,$6,$7)`, runID, snapshot.SeasonYear, club.ClubName, club.ClubKey, nullText(club.ListBRule), club.SubmittedCount, club.NoForm)
		if err != nil {
			return ImportResult{}, err
		}
	}
	for _, a := range snapshot.Amendments {
		status, issue := a.Status, a.Issue
		if rosterIssue := issueByAmendment[a.ClubName+"|"+strconv.Itoa(a.Sequence)]; rosterIssue != "" {
			status, issue = "review", rosterIssue
		}
		_, err = tx.Exec(ctx, `INSERT INTO starred_list_amendments(import_run_id,season_year,club_name,club_key,sequence_number,effective_date,outgoing_name,outgoing_key,incoming_name,incoming_key,raw_value,parse_status,parse_issue) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, runID, a.SeasonYear, a.ClubName, a.ClubKey, a.Sequence, a.Date, nullText(a.Outgoing), nullText(a.OutgoingKey), nullText(a.Incoming), nullText(a.IncomingKey), a.RawValue, status, nullText(issue))
		if err != nil {
			return ImportResult{}, err
		}
	}
	for _, p := range periods {
		_, err = tx.Exec(ctx, `INSERT INTO starred_list_periods(import_run_id,season_year,club_name,club_key,list_type,player_name,player_key,valid_from,valid_to,tags,source_kind,source_sequence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, runID, p.SeasonYear, p.ClubName, p.ClubKey, p.ListType, p.PlayerName, p.PlayerKey, p.ValidFrom, p.ValidTo, nonNilStrings(p.Tags), p.SourceKind, nullInt(p.SourceSequence))
		if err != nil {
			return ImportResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{RunID: runID, Entries: len(snapshot.Entries), Amendments: len(snapshot.Amendments), Issues: len(rosterIssues)}, nil
}

func PendingMatchIDs(ctx context.Context, pool *db.Pool, seasonYear, limit int) ([]int64, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	rows, err := pool.Query(ctx, `
		SELECT lf.play_cricket_match_id
		FROM league_fixtures lf
		LEFT JOIN starred_match_imports sm ON sm.play_cricket_match_id=lf.play_cricket_match_id
		WHERE EXTRACT(YEAR FROM lf.match_date)::int=$1
		  AND (
		    COALESCE(lf.home_team_name,'') ~* '([1-6](st|nd|rd|th)[[:space:]]*XI|top[[:space:]]+guns)'
		    OR COALESCE(lf.away_team_name,'') ~* '([1-6](st|nd|rd|th)[[:space:]]*XI|top[[:space:]]+guns)'
		  )
		  AND (sm.play_cricket_match_id IS NULL OR (
		        COALESCE(lf.payload->>'last_updated','') <> ''
		        AND COALESCE(sm.last_updated,'') <> COALESCE(lf.payload->>'last_updated','')
		      ))
		  AND lf.match_date <= $3::date
		ORDER BY lf.match_date, lf.play_cricket_match_id
		LIMIT $2
	`, seasonYear, limit, ReviewCutoff(seasonYear, time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func PendingMatchCount(ctx context.Context, pool *db.Pool, seasonYear int) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*)::int
		FROM league_fixtures lf
		LEFT JOIN starred_match_imports sm ON sm.play_cricket_match_id=lf.play_cricket_match_id
		WHERE EXTRACT(YEAR FROM lf.match_date)::int=$1
		  AND (
		    COALESCE(lf.home_team_name,'') ~* '([1-6](st|nd|rd|th)[[:space:]]*XI|top[[:space:]]+guns)'
		    OR COALESCE(lf.away_team_name,'') ~* '([1-6](st|nd|rd|th)[[:space:]]*XI|top[[:space:]]+guns)'
		  )
		  AND (sm.play_cricket_match_id IS NULL OR (
		        COALESCE(lf.payload->>'last_updated','') <> ''
		        AND COALESCE(sm.last_updated,'') <> COALESCE(lf.payload->>'last_updated','')
		      ))
		  AND lf.match_date <= $2::date
	`, seasonYear, ReviewCutoff(seasonYear, time.Now())).Scan(&count)
	return count, err
}

func SyncPendingScorecards(ctx context.Context, pool *db.Pool, client *leagueapi.Client, seasonYear, limit int) (ScorecardSyncResult, error) {
	ids, err := PendingMatchIDs(ctx, pool, seasonYear, limit)
	if err != nil {
		return ScorecardSyncResult{}, err
	}
	var result ScorecardSyncResult
	for _, id := range ids {
		detail, raw, fetchErr := client.FetchMatchDetail(ctx, id)
		if fetchErr != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%d: %v", id, fetchErr))
			continue
		}
		n, storeErr := StoreScorecard(ctx, pool, *detail, raw)
		if storeErr != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%d: %v", id, storeErr))
			continue
		}
		result.Matches++
		result.Appearances += n
	}
	return result, nil
}

var teamLevelRE = regexp.MustCompile(`(?i)([1-6])(st|nd|rd|th)\s*XI`)

func TeamLevel(name string) int {
	if strings.Contains(strings.ToLower(name), "top guns") {
		return 2
	}
	m := teamLevelRE.FindStringSubmatch(name)
	if len(m) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func StoreScorecard(ctx context.Context, pool *db.Pool, match leagueapi.ScorecardMatch, raw []byte) (int, error) {
	matchID, err := strconv.ParseInt(strings.TrimSpace(match.MatchID), 10, 64)
	if err != nil || matchID == 0 {
		matchID = match.ID
	}
	if matchID == 0 {
		return 0, fmt.Errorf("scorecard has no match id")
	}
	date, err := leagueapi.ParseMatchDate(match.MatchDate, "")
	if err != nil {
		return 0, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO starred_match_imports(play_cricket_match_id,season_year,match_date,competition_type,competition_name,last_updated,payload,imported_at)
		VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,now())
		ON CONFLICT(play_cricket_match_id) DO UPDATE SET season_year=EXCLUDED.season_year,match_date=EXCLUDED.match_date,competition_type=EXCLUDED.competition_type,competition_name=EXCLUDED.competition_name,last_updated=EXCLUDED.last_updated,payload=EXCLUDED.payload,imported_at=now()
	`, matchID, date.Year(), date, nullText(match.CompetitionType), nullText(match.CompetitionName), nullText(match.LastUpdated), raw)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM starred_appearances WHERE play_cricket_match_id=$1`, matchID); err != nil {
		return 0, err
	}
	count := 0
	insertSide := func(clubName, teamName string, players []leagueapi.ScorecardPlayer) error {
		clubKey, level := NormalizeClub(clubName), TeamLevel(teamName)
		day := date.Weekday().String()
		for _, player := range dedupeScorecardPlayers(players) {
			name := strings.TrimSpace(player.PlayerName)
			if name == "" {
				continue
			}
			var id any
			if player.PlayerID > 0 {
				id = player.PlayerID
			}
			_, e := tx.Exec(ctx, `INSERT INTO starred_appearances(play_cricket_match_id,season_year,match_date,competition_type,competition_name,club_name,club_key,team_name,team_level,playing_day,play_cricket_player_id,player_name,player_key,captain,wicket_keeper) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, matchID, date.Year(), date, nullText(match.CompetitionType), nullText(match.CompetitionName), clubName, clubKey, teamName, nullInt(level), day, id, name, NormalizeName(name), player.Captain, player.WicketKeeper)
			if e != nil {
				return e
			}
			count++
		}
		return nil
	}
	if err = insertSide(match.HomeClubName, match.HomeTeamName, match.Players.HomeTeam); err != nil {
		return 0, err
	}
	if err = insertSide(match.AwayClubName, match.AwayTeamName, match.Players.AwayTeam); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func dedupeScorecardPlayers(players []leagueapi.ScorecardPlayer) []leagueapi.ScorecardPlayer {
	type playerIdentity struct {
		id  int64
		key string
	}
	out := make([]leagueapi.ScorecardPlayer, 0, len(players))
	indexes := make(map[playerIdentity]int, len(players))
	for _, player := range players {
		name := strings.TrimSpace(player.PlayerName)
		if name == "" {
			continue
		}
		player.PlayerName = name
		identity := playerIdentity{id: player.PlayerID, key: NormalizeName(name)}
		if index, exists := indexes[identity]; exists {
			out[index].Captain = out[index].Captain || player.Captain
			out[index].WicketKeeper = out[index].WicketKeeper || player.WicketKeeper
			if out[index].Position == 0 || (player.Position > 0 && player.Position < out[index].Position) {
				out[index].Position = player.Position
			}
			continue
		}
		indexes[identity] = len(out)
		out = append(out, player)
	}
	return out
}

func LoadEvaluationInputs(ctx context.Context, pool *db.Pool, seasonYear int) ([]Period, []Appearance, []IdentityMapping, []RosterIssue, error) {
	var runID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM starred_import_runs WHERE season_year=$1 AND status='complete' ORDER BY imported_at DESC,id DESC LIMIT 1`, seasonYear).Scan(&runID); err != nil {
		return nil, nil, nil, nil, err
	}
	rows, err := pool.Query(ctx, `SELECT season_year,club_name,club_key,list_type,player_name,player_key,valid_from,valid_to,tags,source_kind,COALESCE(source_sequence,0) FROM starred_list_periods WHERE import_run_id=$1`, runID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var periods []Period
	for rows.Next() {
		var p Period
		if err = rows.Scan(&p.SeasonYear, &p.ClubName, &p.ClubKey, &p.ListType, &p.PlayerName, &p.PlayerKey, &p.ValidFrom, &p.ValidTo, &p.Tags, &p.SourceKind, &p.SourceSequence); err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		periods = append(periods, p)
	}
	rows.Close()
	rows, err = pool.Query(ctx, `SELECT play_cricket_match_id,season_year,match_date,COALESCE(competition_type,''),COALESCE(competition_name,''),club_name,club_key,team_name,COALESCE(team_level,0),COALESCE(playing_day,''),COALESCE(play_cricket_player_id,0),player_name,player_key FROM starred_appearances WHERE season_year=$1`, seasonYear)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var apps []Appearance
	for rows.Next() {
		var a Appearance
		if err = rows.Scan(&a.MatchID, &a.SeasonYear, &a.MatchDate, &a.CompetitionType, &a.CompetitionName, &a.ClubName, &a.ClubKey, &a.TeamName, &a.TeamLevel, &a.PlayingDay, &a.PlayerID, &a.PlayerName, &a.PlayerKey); err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		apps = append(apps, a)
	}
	rows.Close()
	rows, err = pool.Query(ctx, `SELECT season_year,club_key,starred_player_key,play_cricket_player_id,COALESCE(play_cricket_name,'') FROM starred_identity_mappings WHERE season_year=$1 AND status='confirmed'`, seasonYear)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var maps []IdentityMapping
	for rows.Next() {
		var m IdentityMapping
		if err = rows.Scan(&m.SeasonYear, &m.ClubKey, &m.StarredPlayerKey, &m.PlayerID, &m.PlayerName); err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		maps = append(maps, m)
	}
	rows.Close()
	rows, err = pool.Query(ctx, `SELECT club_name,sequence_number,raw_value,COALESCE(parse_issue,'review required') FROM starred_list_amendments WHERE import_run_id=$1 AND parse_status<>'parsed' ORDER BY club_name,sequence_number`, runID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var issues []RosterIssue
	for rows.Next() {
		var i RosterIssue
		if err = rows.Scan(&i.ClubName, &i.Sequence, &i.RawValue, &i.Reason); err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		issues = append(issues, i)
	}
	rows.Close()
	return periods, apps, maps, issues, nil
}

func LoadClubStatuses(ctx context.Context, pool *db.Pool, seasonYear int, asOf time.Time) ([]ClubStatus, error) {
	var runID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM starred_import_runs WHERE season_year=$1 AND status='complete' ORDER BY imported_at DESC,id DESC LIMIT 1`, seasonYear).Scan(&runID); err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT cs.club_name,cs.club_key,COALESCE(cs.list_b_rule,''),cs.submitted_count,cs.no_form_submitted,
		       COUNT(p.id)::int
		FROM starred_club_status cs
		LEFT JOIN starred_list_periods p ON p.import_run_id=cs.import_run_id AND p.club_key=cs.club_key AND p.valid_from <= $2 AND (p.valid_to IS NULL OR p.valid_to > $2)
		WHERE cs.import_run_id=$1
		GROUP BY cs.club_name,cs.club_key,cs.list_b_rule,cs.submitted_count,cs.no_form_submitted
		ORDER BY cs.club_name`, runID, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClubStatus
	for rows.Next() {
		var s ClubStatus
		if err := rows.Scan(&s.ClubName, &s.ClubKey, &s.ListBRule, &s.SubmittedCount, &s.NoForm, &s.CurrentCount); err != nil {
			return nil, err
		}
		rule := strings.ToLower(s.ListBRule)
		switch {
		case strings.Contains(rule, "large list b"):
			s.ExpectedCount = 16
			s.Compliant = s.CurrentCount == 16
		case strings.Contains(rule, "reduced list b"):
			s.ExpectedCount = 8
			s.Compliant = s.CurrentCount == 8
		default:
			s.ExpectedCount = 5
			s.Compliant = s.CurrentCount >= 5
		}
		if s.NoForm {
			s.Compliant = false
			s.Reason = "No form submitted"
		} else if !s.Compliant {
			s.Reason = fmt.Sprintf("%d active; expected %d", s.CurrentCount, s.ExpectedCount)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func SaveIdentityMapping(ctx context.Context, pool *db.Pool, seasonYear int, clubKey, playerKey string, playerID int64, playerName string, adminID int32) error {
	_, err := pool.Exec(ctx, `INSERT INTO starred_identity_mappings(season_year,club_key,starred_player_key,play_cricket_player_id,play_cricket_name,confirmed_by,confirmed_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,0),now()) ON CONFLICT(season_year,club_key,starred_player_key) DO UPDATE SET play_cricket_player_id=EXCLUDED.play_cricket_player_id,play_cricket_name=EXCLUDED.play_cricket_name,status='confirmed',confirmed_by=EXCLUDED.confirmed_by,confirmed_at=now()`, seasonYear, clubKey, playerKey, playerID, playerName, adminID)
	return err
}

func nullText(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}
func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func MarshalScorecard(match leagueapi.ScorecardMatch) []byte { b, _ := json.Marshal(match); return b }
