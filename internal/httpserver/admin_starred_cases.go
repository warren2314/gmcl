package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/starred"

	"github.com/jackc/pgx/v5"
)

type starredFixtureSide struct {
	Side     string
	TeamPCID string
	ClubName string
	TeamName string
}

type starredCaseTarget struct {
	SeasonID int32
	ClubID   int32
	TeamID   int32
	ClubName string
	TeamName string
	TeamPCID string
	Side     string
}

type starredCaseResult struct {
	CaseID    int64
	Reference string
	IntakeID  int64
	Created   bool
}

// starredCaseExceptionError represents a data/configuration problem that must
// be shown in the intake exception queue. It is deliberately distinct from a
// database failure, which rolls the transaction back instead.
type starredCaseExceptionError struct {
	message string
}

func (e *starredCaseExceptionError) Error() string { return e.message }

func starredCaseExceptionf(format string, args ...any) error {
	return &starredCaseExceptionError{message: fmt.Sprintf(format, args...)}
}

func sameStarredCaseDate(left, right time.Time) bool {
	return left.Year() == right.Year() && left.Month() == right.Month() && left.Day() == right.Day()
}

func starredCaseClubMatches(left, right string) bool {
	if starred.NormalizeClub(left) == starred.NormalizeClub(right) {
		return true
	}
	return starredClubDataMatchKey(left) == starredClubDataMatchKey(right)
}

func selectStarredFixtureSide(breach starred.Breach, home, away starredFixtureSide) (starredFixtureSide, error) {
	clubMatches := func(side starredFixtureSide) bool {
		return starred.NormalizeClub(side.ClubName) == breach.Appearance.ClubKey ||
			starredCaseClubMatches(side.ClubName, breach.Appearance.ClubName)
	}
	exactMatches := func(side starredFixtureSide) bool {
		return clubMatches(side) && starred.NormalizeName(side.TeamName) == starred.NormalizeName(breach.Appearance.TeamName)
	}
	candidates := make([]starredFixtureSide, 0, 2)
	for _, side := range []starredFixtureSide{home, away} {
		if exactMatches(side) {
			candidates = append(candidates, side)
		}
	}
	if len(candidates) == 0 {
		for _, side := range []starredFixtureSide{home, away} {
			if clubMatches(side) {
				candidates = append(candidates, side)
			}
		}
	}
	if len(candidates) == 0 {
		return starredFixtureSide{}, starredCaseExceptionf(
			"Play-Cricket match %d does not have an exact side for %s / %s",
			breach.Appearance.MatchID, breach.Appearance.ClubName, breach.Appearance.TeamName,
		)
	}
	if len(candidates) > 1 {
		return starredFixtureSide{}, starredCaseExceptionf(
			"Play-Cricket match %d has more than one possible side for %s / %s",
			breach.Appearance.MatchID, breach.Appearance.ClubName, breach.Appearance.TeamName,
		)
	}
	selected := candidates[0]
	selected.TeamPCID = strings.TrimSpace(selected.TeamPCID)
	if selected.TeamPCID == "" {
		return starredFixtureSide{}, starredCaseExceptionf(
			"Play-Cricket match %d has no team ID for the %s side",
			breach.Appearance.MatchID, selected.Side,
		)
	}
	return selected, nil
}

func starredCaseSourceReference(breach starred.Breach) string {
	identity := breach.Appearance.PlayerKey
	if breach.Appearance.PlayerID > 0 {
		identity = strconv.FormatInt(breach.Appearance.PlayerID, 10)
	}
	return fmt.Sprintf("play-cricket:match:%d:player:%s", breach.Appearance.MatchID, identity)
}

func starredCaseProvenance(breach starred.Breach) ([]byte, string, error) {
	ruleReference := breach.RuleReference
	if ruleReference == "" {
		ruleReference = "3.5"
	}
	payload := map[string]any{
		"origin":      "starred_player",
		"finding_key": starredFindingKey(breach),
		"list": map[string]any{
			"type":                  breach.ListType,
			"published_player_name": breach.StarredName,
			"club_key":              breach.Appearance.ClubKey,
		},
		"match": map[string]any{
			"play_cricket_match_id": breach.Appearance.MatchID,
			"date":                  breach.Appearance.MatchDate.Format("2006-01-02"),
			"competition_type":      breach.Appearance.CompetitionType,
			"competition_name":      breach.Appearance.CompetitionName,
			"team_name":             breach.Appearance.TeamName,
			"team_level":            breach.Appearance.TeamLevel,
			"playing_day":           breach.Appearance.PlayingDay,
			"scorecard_record": fmt.Sprintf(
				"starred_match_imports/play_cricket_match_id/%d", breach.Appearance.MatchID,
			),
			"scorecard_admin_path": fmt.Sprintf(
				"/admin/starred-players?season=%d&view=scorecard&match_id=%d#card-detail",
				breach.Appearance.SeasonYear, breach.Appearance.MatchID,
			),
		},
		"player": map[string]any{
			"play_cricket_player_id": breach.Appearance.PlayerID,
			"name":                   breach.Appearance.PlayerName,
			"player_key":             breach.Appearance.PlayerKey,
		},
		"evaluation": map[string]any{
			"rule":                   ruleReference,
			"first_xi_league":        breach.FirstXILeague,
			"second_xi_league":       breach.SecondXILeague,
			"needs_exemption_review": breach.NeedsExemptionReview,
			"revalidated":            true,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("encode starred-player provenance: %w", err)
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func (s *Server) resolveStarredCaseTarget(ctx context.Context, tx pgx.Tx, breach starred.Breach) (starredCaseTarget, error) {
	var fixtureDate time.Time
	var fixtureSeasonID int32
	var home, away starredFixtureSide
	home.Side, away.Side = "home", "away"
	err := tx.QueryRow(ctx, `
		SELECT match_date,COALESCE(season_id,0),
		       COALESCE(home_team_pc_id,''),COALESCE(home_club_name,''),COALESCE(home_team_name,''),
		       COALESCE(away_team_pc_id,''),COALESCE(away_club_name,''),COALESCE(away_team_name,'')
		FROM league_fixtures
		WHERE play_cricket_match_id=$1`, breach.Appearance.MatchID).Scan(
		&fixtureDate, &fixtureSeasonID,
		&home.TeamPCID, &home.ClubName, &home.TeamName,
		&away.TeamPCID, &away.ClubName, &away.TeamName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return starredCaseTarget{}, starredCaseExceptionf(
			"Play-Cricket fixture %d is not present in the fixture cache", breach.Appearance.MatchID,
		)
	}
	if err != nil {
		return starredCaseTarget{}, fmt.Errorf("load Play-Cricket fixture: %w", err)
	}
	if !sameStarredCaseDate(fixtureDate, breach.Appearance.MatchDate) {
		return starredCaseTarget{}, starredCaseExceptionf(
			"Play-Cricket fixture %d is dated %s, not the revalidated scorecard date %s",
			breach.Appearance.MatchID, fixtureDate.Format("2006-01-02"), breach.Appearance.MatchDate.Format("2006-01-02"),
		)
	}
	selected, err := selectStarredFixtureSide(breach, home, away)
	if err != nil {
		return starredCaseTarget{}, err
	}

	type mappedTeam struct {
		teamID, clubID     int32
		teamName, clubName string
	}
	mapped := make([]mappedTeam, 0, 2)
	rows, err := tx.Query(ctx, `
		SELECT t.id,c.id,t.name,c.name
		FROM teams t
		JOIN clubs c ON c.id=t.club_id
		WHERE TRIM(COALESCE(t.play_cricket_team_id,''))=TRIM($1)
		  AND t.active
		ORDER BY t.id`, selected.TeamPCID)
	if err != nil {
		return starredCaseTarget{}, fmt.Errorf("resolve Play-Cricket team ID: %w", err)
	}
	for rows.Next() {
		var team mappedTeam
		if scanErr := rows.Scan(&team.teamID, &team.clubID, &team.teamName, &team.clubName); scanErr != nil {
			rows.Close()
			return starredCaseTarget{}, fmt.Errorf("read Play-Cricket team mapping: %w", scanErr)
		}
		mapped = append(mapped, team)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return starredCaseTarget{}, fmt.Errorf("read Play-Cricket team mappings: %w", err)
	}
	if len(mapped) == 0 {
		return starredCaseTarget{}, starredCaseExceptionf(
			"Play-Cricket team ID %s is not mapped to an active GMCL team", selected.TeamPCID,
		)
	}
	if len(mapped) > 1 {
		return starredCaseTarget{}, starredCaseExceptionf(
			"Play-Cricket team ID %s maps to more than one active GMCL team", selected.TeamPCID,
		)
	}
	team := mapped[0]
	if !starredCaseClubMatches(team.clubName, selected.ClubName) &&
		!starredCaseClubMatches(team.clubName, breach.Appearance.ClubName) {
		return starredCaseTarget{}, starredCaseExceptionf(
			"Play-Cricket team ID %s maps to %s, not the fixture club %s",
			selected.TeamPCID, team.clubName, selected.ClubName,
		)
	}

	if fixtureSeasonID == 0 {
		seasonRows, queryErr := tx.Query(ctx, `
			SELECT id FROM seasons
			WHERE $1::date BETWEEN start_date AND end_date
			ORDER BY id`, fixtureDate)
		if queryErr != nil {
			return starredCaseTarget{}, fmt.Errorf("resolve fixture season: %w", queryErr)
		}
		seasonIDs := make([]int32, 0, 2)
		for seasonRows.Next() {
			var seasonID int32
			if scanErr := seasonRows.Scan(&seasonID); scanErr != nil {
				seasonRows.Close()
				return starredCaseTarget{}, fmt.Errorf("read fixture season: %w", scanErr)
			}
			seasonIDs = append(seasonIDs, seasonID)
		}
		seasonRows.Close()
		if len(seasonIDs) != 1 {
			return starredCaseTarget{}, starredCaseExceptionf(
				"fixture date %s maps to %d GMCL seasons", fixtureDate.Format("2006-01-02"), len(seasonIDs),
			)
		}
		fixtureSeasonID = seasonIDs[0]
	} else {
		var seasonMatches bool
		if err = tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM seasons
				WHERE id=$1 AND $2::date BETWEEN start_date AND end_date
			)`, fixtureSeasonID, fixtureDate).Scan(&seasonMatches); err != nil {
			return starredCaseTarget{}, fmt.Errorf("validate fixture season: %w", err)
		}
		if !seasonMatches {
			return starredCaseTarget{}, starredCaseExceptionf(
				"fixture %d has an invalid season mapping for %s",
				breach.Appearance.MatchID, fixtureDate.Format("2006-01-02"),
			)
		}
	}

	return starredCaseTarget{
		SeasonID: fixtureSeasonID,
		ClubID:   team.clubID,
		TeamID:   team.teamID,
		ClubName: team.clubName,
		TeamName: team.teamName,
		TeamPCID: selected.TeamPCID,
		Side:     selected.Side,
	}, nil
}

func resolveStarredCaseAssignee(ctx context.Context, tx pgx.Tx) (int32, string, error) {
	username := strings.TrimSpace(os.Getenv("INELIGIBLE_DEFAULT_ASSIGNEE_USERNAME"))
	if username == "" {
		return 0, "", starredCaseExceptionf("INELIGIBLE_DEFAULT_ASSIGNEE_USERNAME is not configured")
	}
	var id int32
	var canonical string
	err := tx.QueryRow(ctx, `
		SELECT id,username
		FROM admin_users
		WHERE LOWER(username)=LOWER($1) AND is_active
		ORDER BY id
		LIMIT 1`, username).Scan(&id, &canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", starredCaseExceptionf(
			"configured ineligible-player assignee %q is not an active admin", username,
		)
	}
	if err != nil {
		return 0, "", fmt.Errorf("resolve ineligible-player assignee: %w", err)
	}
	return id, canonical, nil
}

func finishStarredCaseException(ctx context.Context, tx pgx.Tx, intakeID, syncRunID int64, cause error) error {
	message := cause.Error()
	if _, err := tx.Exec(ctx, `
		UPDATE sanction_intakes
		SET state='exception',exception_message=$2,updated_at=now()
		WHERE id=$1`, intakeID, message); err != nil {
		return fmt.Errorf("record starred-player intake exception: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sanction_intake_sync_runs
		SET status='partial',rows_errored=1,error_message=$2,completed_at=now()
		WHERE id=$1`, syncRunID, message); err != nil {
		return fmt.Errorf("finish starred-player exception run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit starred-player intake exception: %w", err)
	}
	return cause
}

func (s *Server) createStarredIneligibleCase(ctx context.Context, breach starred.Breach, actorID *int32, actorLabel, reqID string) (starredCaseResult, error) {
	var result starredCaseResult
	raw, rawHash, err := starredCaseProvenance(breach)
	if err != nil {
		return result, err
	}
	findingKey := starredFindingKey(breach)
	sourceReference := starredCaseSourceReference(breach)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin starred-player case: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var latestRevision int
	err = tx.QueryRow(ctx, `
		INSERT INTO sanction_intakes(
			origin,external_key,source_reference,external_created_at,state,
			offending_club_text,team_text,player_text,fixture_date
		)
		VALUES('starred_player',$1,$2,$3,'reviewing',$4,$5,$6,$7)
		ON CONFLICT(origin,external_key) DO UPDATE
		SET updated_at=sanction_intakes.updated_at
		RETURNING id,latest_revision`, findingKey, sourceReference, breach.Appearance.MatchDate,
		breach.Appearance.ClubName, breach.Appearance.TeamName, breach.Appearance.PlayerName,
		breach.Appearance.MatchDate).Scan(&result.IntakeID, &latestRevision)
	if err != nil {
		return result, fmt.Errorf("stage starred-player intake: %w", err)
	}
	initialRevision := latestRevision

	err = tx.QueryRow(ctx, `
		SELECT c.id,c.reference
		FROM sanction_intake_case_links l
		JOIN sanction_cases c ON c.id=l.case_id
		WHERE l.intake_id=$1 AND l.relationship='primary'
		ORDER BY l.id
		LIMIT 1`, result.IntakeID).Scan(&result.CaseID, &result.Reference)
	if err == nil {
		if err = tx.Commit(ctx); err != nil {
			return starredCaseResult{}, fmt.Errorf("finish idempotent starred-player case lookup: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, fmt.Errorf("find linked starred-player case: %w", err)
	}

	var syncRunID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO sanction_intake_sync_runs(
			origin,status,source_reference,rows_seen,triggered_by_type,triggered_by_admin_id
		)
		VALUES('starred_player','running',$1,1,'admin',$2)
		RETURNING id`, sourceReference, actorID).Scan(&syncRunID)
	if err != nil {
		return result, fmt.Errorf("start starred-player intake run: %w", err)
	}

	appendRevision := latestRevision == 0
	if latestRevision > 0 {
		var previousHash string
		err = tx.QueryRow(ctx, `
			SELECT raw_sha256
			FROM sanction_intake_revisions
			WHERE intake_id=$1 AND revision=$2`, result.IntakeID, latestRevision).Scan(&previousHash)
		if err != nil {
			return result, fmt.Errorf("load starred-player intake revision: %w", err)
		}
		appendRevision = previousHash != rawHash
	}
	if appendRevision {
		latestRevision++
		if _, err = tx.Exec(ctx, `
			INSERT INTO sanction_intake_revisions(
				intake_id,sync_run_id,revision,raw_data,raw_sha256
			)
			VALUES($1,$2,$3,$4::jsonb,$5)`, result.IntakeID, syncRunID, latestRevision, string(raw), rawHash); err != nil {
			return result, fmt.Errorf("append starred-player intake provenance: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			UPDATE sanction_intakes SET latest_revision=$2,updated_at=now() WHERE id=$1`,
			result.IntakeID, latestRevision); err != nil {
			return result, fmt.Errorf("project starred-player intake revision: %w", err)
		}
	}
	var intakeRevisionID int64
	if err = tx.QueryRow(ctx, `SELECT id FROM sanction_intake_revisions WHERE intake_id=$1 AND revision=$2`, result.IntakeID, latestRevision).Scan(&intakeRevisionID); err != nil {
		return result, fmt.Errorf("resolve current starred-player intake revision: %w", err)
	}

	target, err := s.resolveStarredCaseTarget(ctx, tx, breach)
	if err != nil {
		var exception *starredCaseExceptionError
		if errors.As(err, &exception) {
			return result, finishStarredCaseException(ctx, tx, result.IntakeID, syncRunID, err)
		}
		return result, err
	}
	assigneeID, assigneeName, err := resolveStarredCaseAssignee(ctx, tx)
	if err != nil {
		var exception *starredCaseExceptionError
		if errors.As(err, &exception) {
			return result, finishStarredCaseException(ctx, tx, result.IntakeID, syncRunID, err)
		}
		return result, err
	}

	publicSummary := fmt.Sprintf(
		"Investigation into a potential Rule 3.5 ineligible-player appearance by %s for %s on %s.",
		breach.Appearance.PlayerName, target.TeamName, breach.Appearance.MatchDate.Format("02 January 2006"),
	)
	privateSummary := fmt.Sprintf(
		"Revalidated starred-player finding: List %s player %s (Play-Cricket player %d) appeared for %s in match %d. Source intake %d; no correspondence sent.",
		breach.ListType, breach.Appearance.PlayerName, breach.Appearance.PlayerID, target.TeamName,
		breach.Appearance.MatchID, result.IntakeID,
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO sanction_cases(
			source_type,status,season_id,club_id,team_id,player_name,match_date,
			play_cricket_match_id,public_summary,private_summary,assigned_admin_id
		)
		VALUES('ineligible_player','investigating',$1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id,reference`, target.SeasonID, target.ClubID, target.TeamID,
		breach.Appearance.PlayerName, breach.Appearance.MatchDate, breach.Appearance.MatchID,
		publicSummary, privateSummary, assigneeID).Scan(&result.CaseID, &result.Reference)
	if err != nil {
		return result, fmt.Errorf("create ineligible-player case: %w", err)
	}
	if err = recordStarredCaseAllegedRule(ctx, tx, result.CaseID, actorID, actorLabel, reqID); err != nil {
		return result, fmt.Errorf("record Rule 3.5 allegation: %w", err)
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_case_parties(case_id,party_type,name,team_id,relationship)
		VALUES($1,'club',$2,$3,'offending_club')`, result.CaseID, target.ClubName, target.TeamID); err != nil {
		return result, fmt.Errorf("add offending club to case: %w", err)
	}
	// A starred-player finding is raised by the league rather than an external
	// reporting club. Keep that origin explicit for approval and outcome routing.
	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_case_parties(case_id,party_type,name,relationship)
		VALUES($1,'league','GMCL Official','league')`, result.CaseID); err != nil {
		return result, fmt.Errorf("add league origin to case: %w", err)
	}
	teamMetadata, _ := json.Marshal(map[string]any{
		"fixture_side": target.Side, "play_cricket_team_id": target.TeamPCID, "origin": "starred_player",
	})
	var teamSubjectID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO sanction_case_subjects(case_id,subject_type,team_id,is_primary,metadata)
		VALUES($1,'team',$2,false,$3::jsonb) RETURNING id`, result.CaseID, target.TeamID, string(teamMetadata)).Scan(&teamSubjectID); err != nil {
		return result, fmt.Errorf("add team subject to case: %w", err)
	}
	playerMetadata, _ := json.Marshal(map[string]any{
		"list_type": breach.ListType, "published_starred_name": breach.StarredName,
		"player_key": breach.Appearance.PlayerKey, "club_key": breach.Appearance.ClubKey,
	})
	var playerSubjectID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO sanction_case_subjects(
			case_id,subject_type,team_id,player_name,play_cricket_player_id,is_primary,metadata
		)
		VALUES($1,'player',$2,$3,NULLIF($4,0),true,$5::jsonb) RETURNING id`, result.CaseID,
		target.TeamID, breach.Appearance.PlayerName, breach.Appearance.PlayerID, string(playerMetadata)).Scan(&playerSubjectID); err != nil {
		return result, fmt.Errorf("add player subject to case: %w", err)
	}
	matchMetadata, _ := json.Marshal(map[string]any{
		"competition_type": breach.Appearance.CompetitionType,
		"competition_name": breach.Appearance.CompetitionName,
		"match_date":       breach.Appearance.MatchDate.Format("2006-01-02"),
		"scorecard_path": fmt.Sprintf(
			"/admin/starred-players?season=%d&view=scorecard&match_id=%d#card-detail",
			breach.Appearance.SeasonYear, breach.Appearance.MatchID,
		),
	})
	var matchSubjectID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO sanction_case_subjects(
			case_id,subject_type,play_cricket_match_id,is_primary,metadata
		)
		VALUES($1,'match',$2,false,$3::jsonb) RETURNING id`, result.CaseID,
		breach.Appearance.MatchID, string(matchMetadata)).Scan(&matchSubjectID); err != nil {
		return result, fmt.Errorf("add match subject to case: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_subject_intakes(subject_id,case_id,intake_id,revision_id,created_by_admin_id)
		SELECT subject_id,$1,$2,$3,$4 FROM unnest($5::bigint[]) subject_id`, result.CaseID, result.IntakeID, intakeRevisionID, actorID, []int64{teamSubjectID, playerSubjectID, matchSubjectID}); err != nil {
		return result, fmt.Errorf("retain starred-player subject provenance: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_intake_merge_resolutions(
		case_id,intake_id,revision_id,relationship,team_id,team_subject_id,player_subject_id,match_subject_id,league_origin,created_by_admin_id
	) VALUES($1,$2,$3,'primary',$4,$5,$6,$7,true,$8)`, result.CaseID, result.IntakeID, intakeRevisionID,
		target.TeamID, teamSubjectID, playerSubjectID, matchSubjectID, actorID); err != nil {
		return result, fmt.Errorf("record effective starred-player merge: %w", err)
	}

	afterData, _ := json.Marshal(map[string]any{
		"reference":               result.Reference,
		"source_origin":           "starred_player",
		"source_intake_id":        result.IntakeID,
		"finding_key":             findingKey,
		"play_cricket_match_id":   breach.Appearance.MatchID,
		"play_cricket_player_id":  breach.Appearance.PlayerID,
		"play_cricket_team_id":    target.TeamPCID,
		"list_type":               breach.ListType,
		"assigned_admin_id":       assigneeID,
		"assigned_admin_username": assigneeName,
		"correspondence_sent":     false,
	})
	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_case_events(
			case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id,metadata
		)
		VALUES(
			$1,'starred_player_case_created','admin',$2,NULLIF($3,''),
			'Revalidated starred-player finding escalated for investigation',$4::jsonb,NULLIF($5,''),
			jsonb_build_object('origin','starred_player','finding_key',$6::text)
		)`, result.CaseID, actorID, strings.TrimSpace(actorLabel), string(afterData),
		reqID, findingKey); err != nil {
		return result, fmt.Errorf("append starred-player case event: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_intake_case_links(
			intake_id,case_id,relationship,reason,created_by_admin_id
		)
		VALUES($1,$2,'primary','Created from revalidated starred-player finding',$3)`,
		result.IntakeID, result.CaseID, actorID); err != nil {
		return result, fmt.Errorf("link starred-player intake to case: %w", err)
	}
	intakeAfter, _ := json.Marshal(map[string]any{
		"case_id": result.CaseID, "case_reference": result.Reference,
		"relationship": "primary", "state": "linked",
	})
	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_intake_events(
			intake_id,event_type,actor_admin_id,actor_label,reason,after_data,request_id
		)
		VALUES(
			$1,'case_created',$2,NULLIF($3,''),
			'Revalidated starred-player finding created as an ineligible-player case',
			$4::jsonb,NULLIF($5,'')
		)`, result.IntakeID, actorID, strings.TrimSpace(actorLabel), string(intakeAfter), reqID); err != nil {
		return result, fmt.Errorf("append starred-player intake event: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE sanction_intakes
		SET state='linked',exception_message=NULL,updated_at=now()
		WHERE id=$1`, result.IntakeID); err != nil {
		return result, fmt.Errorf("complete starred-player intake: %w", err)
	}
	rowsNew := 0
	if initialRevision == 0 {
		rowsNew = 1
	}
	if _, err = tx.Exec(ctx, `
		UPDATE sanction_intake_sync_runs
		SET status='succeeded',rows_new=$2,rows_changed=$3,completed_at=now()
		WHERE id=$1`, syncRunID, rowsNew, boolToInt(appendRevision && initialRevision > 0)); err != nil {
		return result, fmt.Errorf("finish starred-player intake run: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return starredCaseResult{}, fmt.Errorf("commit starred-player case: %w", err)
	}
	result.Created = true
	return result, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Server) existingStarredIneligibleCase(ctx context.Context, findingKey string) (starredCaseResult, error) {
	var result starredCaseResult
	err := s.DB.QueryRow(ctx, `
		SELECT c.id,c.reference,i.id
		FROM sanction_intakes i
		JOIN sanction_intake_case_links l ON l.intake_id=i.id AND l.relationship='primary'
		JOIN sanction_cases c ON c.id=l.case_id
		WHERE i.origin='starred_player' AND i.external_key=$1
		ORDER BY l.id
		LIMIT 1`, findingKey).Scan(&result.CaseID, &result.Reference, &result.IntakeID)
	if err != nil {
		return starredCaseResult{}, err
	}
	return result, nil
}

func (s *Server) handleAdminStarredFindingCaseCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, matchID, playerID, clubKey, playerKey, listType, err := parseStarredFindingForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		findingKey := starredFindingIdentityKey(matchID, playerID, clubKey, playerKey, listType)

		// Draft/approved/sent records pre-date the case workflow. Keep them
		// readable and do not silently replace their historical correspondence.
		var legacyID int64
		legacyErr := s.DB.QueryRow(ctx, `
			SELECT id FROM starred_finding_reviews WHERE finding_key=$1`,
			findingKey).Scan(&legacyID)
		if legacyErr == nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/starred-players/findings/%d", legacyID), http.StatusSeeOther)
			return
		}
		if !errors.Is(legacyErr, pgx.ErrNoRows) {
			redirectStarredFinding(w, r, year, "", "Could not check the legacy finding record: "+legacyErr.Error(), nil)
			return
		}
		existing, existingErr := s.existingStarredIneligibleCase(ctx, findingKey)
		if existingErr == nil {
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", existing.CaseID), http.StatusSeeOther)
			return
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			redirectStarredFinding(w, r, year, "", "Could not check for an existing ineligible-player case: "+existingErr.Error(), nil)
			return
		}

		breach, err := s.verifiedStarredBreach(ctx, year, matchID, playerID, clubKey, playerKey, listType)
		if err != nil {
			redirectStarredFinding(w, r, year, "", err.Error(), nil)
			return
		}

		actor := adminActor(r)
		result, err := s.createStarredIneligibleCase(ctx, breach, actor.ID, actor.Label, actor.RequestID)
		if err != nil {
			redirectStarredFinding(w, r, year, "", "Could not create ineligible-player case: "+err.Error(), &breach)
			return
		}
		if result.Created {
			s.audit(ctx, r, "admin", actor.ID, "starred_finding_case_created", "sanction_case", &result.CaseID, map[string]any{
				"case_reference": result.Reference,
				"intake_id":      result.IntakeID,
				"match_id":       breach.Appearance.MatchID,
				"player_id":      breach.Appearance.PlayerID,
			})
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", result.CaseID), http.StatusSeeOther)
	}
}
