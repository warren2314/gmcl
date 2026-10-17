package junior

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strconv"
	"strings"
)

type Concession struct {
	ID, FixtureID, CompetitionID, SeasonID, ObservationID, Revision, ConcedingTeamID                                                             int64
	HomeTeamID, AwayTeamID                                                                                                                       int64
	HomeClubID, AwayClubID                                                                                                                       int32
	State, Outcome, Evidence, SourceName, CompetitionName, AgeGroup, Division, MatchDate, HomeTeamName, AwayTeamName, HomeClubName, AwayClubName string
	Confirmed                                                                                                                                    bool
}
type ConcessionDecision struct {
	ID, ObservationID, ConcedingTeamID int64
	Action, Reason, DecidedAt          string
	AdminID                            int32
}
type ConcessionFilter struct {
	SeasonID, CompetitionID          int64
	ClubID                           int32
	AgeGroup, Division, State, Query string
	DateFrom, DateTo                 string
}

func (s *Service) ListConcessions(ctx context.Context, p Principal, f ConcessionFilter) ([]Concession, error) {
	if err := Authorize(ctx, s.DB, p, p.ClubID, false); err != nil {
		return nil, err
	}
	if p.ClubID > 0 {
		if f.ClubID != 0 && f.ClubID != p.ClubID {
			return nil, ErrForbidden
		}
	}
	if f.State != "" && f.State != "review" && f.State != "confirmed" && f.State != "awaiting_review" && f.State != "information_requested" && f.State != "dismissed" && f.State != "source_changed" {
		return nil, fixtureValidation("unknown concession state")
	}
	for _, date := range []string{f.DateFrom, f.DateTo} {
		if date != "" {
			if _, err := fixtureDay(date); err != nil {
				return nil, fixtureValidation("enter valid result date filters")
			}
		}
	}
	if f.DateFrom != "" && f.DateTo != "" && f.DateFrom > f.DateTo {
		return nil, fixtureValidation("date filter starts after its end")
	}
	rows, err := s.DB.Query(ctx, `SELECT k.id,k.fixture_id,c.id,c.season_id,o.id,k.revision,COALESCE(decision.conceding_team_id,0),pf.home_team_id,pf.away_team_id,h.club_id,a.club_id,k.state,o.outcome,o.evidence,i.source_name,c.name,c.age_group,c.division,pf.match_date::text,h.name,a.name,hc.name,ac.name,COALESCE(decision.action='confirm',false)
 FROM junior_concession_candidates k JOIN junior_fixtures f ON f.id=k.fixture_id JOIN junior_competitions c ON c.id=f.competition_id JOIN junior_result_observations o ON o.id=k.current_observation_id JOIN junior_publication_fixtures pf ON pf.publication_id=o.publication_id AND pf.fixture_id=o.fixture_id JOIN junior_result_imports i ON i.id=o.import_id JOIN junior_teams h ON h.id=pf.home_team_id JOIN junior_teams a ON a.id=pf.away_team_id JOIN clubs hc ON hc.id=h.club_id JOIN clubs ac ON ac.id=a.club_id
 LEFT JOIN LATERAL(SELECT action,conceding_team_id FROM junior_concession_decisions d WHERE d.candidate_id=k.id AND d.action IN('confirm','dismiss') ORDER BY d.id DESC LIMIT 1)decision ON true
 LEFT JOIN junior_teams conceding ON conceding.id=decision.conceding_team_id
 WHERE ($1::bigint=0 OR c.season_id=$1) AND ($2::bigint=0 OR c.id=$2)
 AND ($3::int=0 OR (decision.action='confirm' AND conceding.club_id=$3)
 OR (COALESCE(decision.action,'')<>'confirm' AND ((o.outcome='conceded_home' AND h.club_id=$3) OR (o.outcome='conceded_away' AND a.club_id=$3) OR (o.outcome NOT IN('conceded_home','conceded_away') AND (h.club_id=$3 OR a.club_id=$3)))))
 AND ($7::int=0 OR h.club_id=$7 OR a.club_id=$7) AND ($4::text='' OR c.age_group=$4)
 AND ($5::text='' OR ($5='confirmed' AND decision.action='confirm') OR ($5='review' AND k.state IN('awaiting_review','information_requested','source_changed')) OR ($5<>'confirmed' AND k.state=$5))
 AND ($6::text='' OR concat_ws(' ',c.name,c.division,h.name,a.name,hc.name,ac.name,o.evidence) ILIKE '%'||$6||'%') AND ($8::text='' OR c.division=$8)
 AND ($9::text='' OR pf.match_date>=NULLIF($9::text,'')::date) AND ($10::text='' OR pf.match_date<=NULLIF($10::text,'')::date)
 ORDER BY k.updated_at DESC,k.id DESC`, f.SeasonID, f.CompetitionID, f.ClubID, f.AgeGroup, f.State, strings.TrimSpace(f.Query), p.ClubID, f.Division, f.DateFrom, f.DateTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Concession{}
	for rows.Next() {
		var c Concession
		if err = rows.Scan(&c.ID, &c.FixtureID, &c.CompetitionID, &c.SeasonID, &c.ObservationID, &c.Revision, &c.ConcedingTeamID, &c.HomeTeamID, &c.AwayTeamID, &c.HomeClubID, &c.AwayClubID, &c.State, &c.Outcome, &c.Evidence, &c.SourceName, &c.CompetitionName, &c.AgeGroup, &c.Division, &c.MatchDate, &c.HomeTeamName, &c.AwayTeamName, &c.HomeClubName, &c.AwayClubName, &c.Confirmed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Service) ConcessionHistory(ctx context.Context, p Principal, candidateID int64) ([]ConcessionDecision, error) {
	if err := Authorize(ctx, s.DB, p, p.ClubID, false); err != nil {
		return nil, err
	}
	var allowed bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_concession_candidates k JOIN junior_fixtures f ON f.id=k.fixture_id JOIN junior_teams h ON h.id=f.home_team_id JOIN junior_teams a ON a.id=f.away_team_id WHERE k.id=$1 AND ($2::int=0 OR h.club_id=$2 OR a.club_id=$2))`, candidateID, p.ClubID).Scan(&allowed)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	rows, err := s.DB.Query(ctx, `SELECT id,observation_id,COALESCE(conceding_team_id,0),action,reason,decided_at::text,decided_by_admin_id FROM junior_concession_decisions WHERE candidate_id=$1 ORDER BY id DESC`, candidateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConcessionDecision{}
	for rows.Next() {
		var d ConcessionDecision
		if err = rows.Scan(&d.ID, &d.ObservationID, &d.ConcedingTeamID, &d.Action, &d.Reason, &d.DecidedAt, &d.AdminID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Service) ReviewConcession(ctx context.Context, p Principal, id, expected int64, action string, teamID int64, informationClubID int32, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 10000 {
		return fixtureValidation("review reason required (maximum10000characters)")
	}
	if action != "confirm" && action != "dismiss" && action != "request_information" {
		return fixtureValidation("unknown review action")
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revision, observationID, home, away, seasonID int64
	var homeClub, awayClub int32
	var outcome string
	err = tx.QueryRow(ctx, `SELECT k.revision,o.id,f.home_team_id,f.away_team_id,h.club_id,a.club_id,o.outcome,comp.season_id FROM junior_concession_candidates k JOIN junior_result_observations o ON o.id=k.current_observation_id JOIN junior_fixtures f ON f.id=k.fixture_id JOIN junior_competitions comp ON comp.id=f.competition_id JOIN junior_teams h ON h.id=f.home_team_id JOIN junior_teams a ON a.id=f.away_team_id WHERE k.id=$1 FOR UPDATE OF k`, id).Scan(&revision, &observationID, &home, &away, &homeClub, &awayClub, &outcome, &seasonID)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = ensureJuniorFixtureSeasonOpen(ctx, tx, seasonID); err != nil {
		return err
	}
	if revision != expected {
		return ErrConflict
	}
	state := "dismissed"
	if action == "confirm" {
		if teamID != home && teamID != away {
			return fixtureValidation("select the conceding participating team")
		}
		if !concessionOutcome(outcome) {
			return fixtureValidation("current result no longer indicates a concession; dismiss or request information")
		}
		state = "confirmed"
	} else {
		teamID = 0
	}
	if action == "request_information" {
		if informationClubID != homeClub && informationClubID != awayClub {
			return fixtureValidation("information recipient must be a participating club")
		}
		state = "information_requested"
	}
	_, err = tx.Exec(ctx, `INSERT INTO junior_concession_decisions(candidate_id,observation_id,action,conceding_team_id,reason,decided_by_admin_id) VALUES($1,$2,$3,NULLIF($4::bigint,0),$5,$6)`, id, observationID, action, teamID, reason, p.AdminID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_concession_candidates SET state=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, state); err != nil {
		return err
	}
	if action == "request_information" {
		if err = QueueConcessionInformation(ctx, tx, p, id, informationClubID, reason); err != nil {
			return err
		}
	}
	if err = Audit(ctx, tx, p, informationClubID, "concession", id, action, map[string]any{"observation_id": observationID, "team_id": teamID, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func ConcessionCSVRows(items []Concession) [][]string {
	out := [][]string{{"candidate_id", "fixture_id", "season_id", "competition_id", "age_group", "competition", "date", "home_club", "home_team", "away_club", "away_team", "review_state", "currently_confirmed", "conceding_team_id", "source", "outcome", "evidence", "confirmed_conceding_club", "confirmed_conceding_team", "division"}}
	for _, c := range items {
		club, team := ConcessionConcedingNames(c)
		out = append(out, []string{strconv.FormatInt(c.ID, 10), strconv.FormatInt(c.FixtureID, 10), strconv.FormatInt(c.SeasonID, 10), strconv.FormatInt(c.CompetitionID, 10), c.AgeGroup, c.CompetitionName, c.MatchDate, c.HomeClubName, c.HomeTeamName, c.AwayClubName, c.AwayTeamName, c.State, strconv.FormatBool(c.Confirmed), strconv.FormatInt(c.ConcedingTeamID, 10), c.SourceName, c.Outcome, c.Evidence, club, team, c.Division})
	}
	return out
}
func ConcessionConcedingNames(c Concession) (string, string) {
	if !c.Confirmed {
		return "", ""
	}
	if c.ConcedingTeamID == c.HomeTeamID {
		return c.HomeClubName, c.HomeTeamName
	}
	if c.ConcedingTeamID == c.AwayTeamID {
		return c.AwayClubName, c.AwayTeamName
	}
	return "", ""
}
