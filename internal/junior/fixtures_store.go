package junior

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strings"
)

func (s *Service) fixtureAdminTx(ctx context.Context, p Principal) (pgx.Tx, error) {
	if p.AdminID <= 0 {
		return nil, ErrForbidden
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func loadCompetition(ctx context.Context, q DBTX, id int64, lock bool) (Competition, error) {
	var c Competition
	sql := `SELECT id,season_id,name,age_group,format,start_date::text,end_date::text,default_start_time,revision,COALESCE(current_publication_id,0),COALESCE((SELECT revision FROM junior_fixture_publications WHERE id=junior_competitions.current_publication_id),0),division,(SELECT state FROM junior_seasons WHERE id=junior_competitions.season_id) FROM junior_competitions WHERE id=$1`
	if lock {
		sql += ` FOR UPDATE`
	}
	err := q.QueryRow(ctx, sql, id).Scan(&c.ID, &c.SeasonID, &c.Name, &c.AgeGroup, &c.Format, &c.StartDate, &c.EndDate, &c.DefaultStartTime, &c.Revision, &c.PublicationID, &c.PublicationRevision, &c.Division, &c.SeasonState)
	if err == pgx.ErrNoRows {
		return c, ErrNotFound
	}
	if err == nil && lock {
		err = ensureJuniorFixtureSeasonOpen(ctx, q, c.SeasonID)
	}
	return c, err
}
func ensureJuniorFixtureSeasonOpen(ctx context.Context, q DBTX, seasonID int64) error {
	var open bool
	err := q.QueryRow(ctx, "SELECT state='open' FROM junior_seasons WHERE id=$1 FOR SHARE", seasonID).Scan(&open)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !open {
		return fixtureValidation("this junior season is closed; fixture and concession records are read-only")
	}
	return nil
}
func (s *Service) CreateCompetition(ctx context.Context, p Principal, c Competition) (int64, error) {
	if c.SeasonID < 1 || strings.TrimSpace(c.Name) == "" || len(c.Name) > 200 || len(c.Division) > 100 || strings.ContainsAny(c.Name+c.Division, "\r\n\x00") || !validFixtureTime(c.DefaultStartTime) {
		return 0, fixtureValidation("name and valid start time required")
	}
	if c.AgeGroup != "U9" && c.AgeGroup != "U11" && c.AgeGroup != "U13" && c.AgeGroup != "U15" && c.AgeGroup != "U18" {
		return 0, fixtureValidation("invalid age group")
	}
	if c.Format != "once" && c.Format != "homeaway" && c.Format != "custom" {
		return 0, fixtureValidation("invalid fixture format")
	}
	start, e := fixtureDay(c.StartDate)
	end, e2 := fixtureDay(c.EndDate)
	if e != nil || e2 != nil || end.Before(start) {
		return 0, fixtureValidation("valid competition window required")
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = ensureJuniorFixtureSeasonOpen(ctx, tx, c.SeasonID); err != nil {
		return 0, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO junior_competitions(season_id,name,age_group,format,start_date,end_date,default_start_time,division) VALUES($1,$2,$3,$4,$5::date,$6::date,$7,$8) RETURNING id`, c.SeasonID, strings.TrimSpace(c.Name), c.AgeGroup, c.Format, c.StartDate, c.EndDate, c.DefaultStartTime, strings.TrimSpace(c.Division)).Scan(&c.ID)
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, p, 0, "competition", c.ID, "created", c); err != nil {
		return 0, err
	}
	return c.ID, tx.Commit(ctx)
}
func (s *Service) ListCompetitions(ctx context.Context, p Principal, seasonID int64) ([]Competition, error) {
	if err := Authorize(ctx, s.DB, p, p.ClubID, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.season_id,c.name,c.age_group,c.format,c.start_date::text,c.end_date::text,c.default_start_time,c.revision,COALESCE(c.current_publication_id,0),COALESCE((SELECT revision FROM junior_fixture_publications WHERE id=c.current_publication_id),0),c.division,(SELECT state FROM junior_seasons WHERE id=c.season_id) FROM junior_competitions c WHERE ($1::bigint=0 OR c.season_id=$1) AND ($2::int=0 OR EXISTS(SELECT 1 FROM junior_competition_entries e JOIN junior_teams t ON t.id=e.team_id WHERE e.competition_id=c.id AND t.club_id=$2)) ORDER BY c.season_id DESC,c.age_group,c.division,c.name,c.id`, seasonID, p.ClubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Competition{}
	for rows.Next() {
		var c Competition
		if err = rows.Scan(&c.ID, &c.SeasonID, &c.Name, &c.AgeGroup, &c.Format, &c.StartDate, &c.EndDate, &c.DefaultStartTime, &c.Revision, &c.PublicationID, &c.PublicationRevision, &c.Division, &c.SeasonState); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Service) GetCompetition(ctx context.Context, p Principal, id int64) (Competition, error) {
	if err := Authorize(ctx, s.DB, p, p.ClubID, false); err != nil {
		return Competition{}, err
	}
	if p.AdminID == 0 || p.ReadOnly {
		var allowed bool
		err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_competition_entries e JOIN junior_teams t ON t.id=e.team_id WHERE e.competition_id=$1 AND t.club_id=$2)`, id, p.ClubID).Scan(&allowed)
		if err != nil {
			return Competition{}, err
		}
		if !allowed {
			return Competition{}, ErrNotFound
		}
	}
	return loadCompetition(ctx, s.DB, id, false)
}
func competitionEntries(ctx context.Context, q DBTX, id int64) ([]CompetitionEntry, error) {
	rows, err := q.Query(ctx, `SELECT t.id,t.club_id,t.name,c.name FROM junior_competition_entries e JOIN junior_competitions co ON co.id=e.competition_id JOIN junior_teams t ON t.id=e.team_id JOIN junior_club_seasons cs ON cs.club_id=t.club_id AND cs.season_id=co.season_id JOIN clubs c ON c.id=t.club_id WHERE e.competition_id=$1 AND e.active AND NOT t.archived AND cs.state='active' ORDER BY t.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompetitionEntry{}
	for rows.Next() {
		var e CompetitionEntry
		if err = rows.Scan(&e.TeamID, &e.ClubID, &e.TeamName, &e.ClubName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Service) CompetitionEntries(ctx context.Context, p Principal, id int64) ([]CompetitionEntry, error) {
	if _, err := s.GetCompetition(ctx, p, id); err != nil {
		return nil, err
	}
	out, err := competitionEntries(ctx, s.DB, id)
	if p.AdminID == 0 || p.ReadOnly {
		filtered := []CompetitionEntry{}
		for _, e := range out {
			if e.ClubID == p.ClubID {
				filtered = append(filtered, e)
			}
		}
		out = filtered
	}
	return out, err
}
func (s *Service) SaveCompetitionEntry(ctx context.Context, p Principal, id, expected, teamID int64, active bool) error {
	if teamID < 1 {
		return fixtureValidation("select a participating team")
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := loadCompetition(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if c.Revision != expected {
		return ErrConflict
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_teams t JOIN junior_club_seasons cs ON cs.club_id=t.club_id AND cs.season_id=$2 WHERE t.id=$1 AND NOT t.archived AND cs.state='active')`, teamID, c.SeasonID).Scan(&eligible)
	if err != nil {
		return err
	}
	if active && !eligible {
		return fixtureValidation("team requires active junior club participation in this season")
	}
	if active {
		_, err = tx.Exec(ctx, `INSERT INTO junior_competition_entries(competition_id,team_id,active) VALUES($1,$2,true) ON CONFLICT(competition_id,team_id) DO UPDATE SET active=true`, id, teamID)
	} else {
		tag, updateErr := tx.Exec(ctx, `UPDATE junior_competition_entries SET active=false WHERE competition_id=$1 AND team_id=$2`, id, teamID)
		err = updateErr
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "competition", id, "entry_updated", map[string]any{"team_id": teamID, "active": active}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func loadFixtureConstraints(ctx context.Context, q DBTX, id int64) (FixtureConstraints, error) {
	var raw []byte
	var f FixtureConstraints
	err := q.QueryRow(ctx, `SELECT settings FROM junior_fixture_constraints WHERE competition_id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(raw, &f)
	return f, err
}
func (s *Service) GetFixtureConstraints(ctx context.Context, p Principal, id int64) (FixtureConstraints, error) {
	if _, err := s.GetCompetition(ctx, p, id); err != nil {
		return FixtureConstraints{}, err
	}
	if p.AdminID == 0 || p.ReadOnly {
		return FixtureConstraints{}, ErrForbidden
	}
	return loadFixtureConstraints(ctx, s.DB, id)
}
func (s *Service) SaveFixtureConstraints(ctx context.Context, p Principal, id, expected int64, f FixtureConstraints) error {
	if len(f.PlayingDates) > 500 || len(f.ReserveDates) > 500 || len(f.Grounds) > 1000 || len(f.Unavailable) > 10000 {
		return fixtureValidation("too many fixture constraints")
	}
	if f.DurationMinutes < 1 || f.DurationMinutes > 720 {
		return fixtureValidation("duration must be 1–720 minutes")
	}
	for _, d := range append(append([]string{}, f.PlayingDates...), f.ReserveDates...) {
		if _, err := fixtureDay(d); err != nil {
			return fixtureValidation("invalid playing/reserve date")
		}
	}
	for _, g := range f.Grounds {
		if g.ClubID <= 0 || strings.TrimSpace(g.Name) == "" || len(g.Name) > 200 || strings.ContainsAny(g.Name, "\r\n\x00") || g.Capacity < 1 || g.Capacity > 20 {
			return fixtureValidation("ground club, name and capacity1–20 required")
		}
	}
	for _, u := range f.Unavailable {
		if _, err := fixtureDay(u.Date); err != nil || u.ClubID <= 0 {
			return fixtureValidation("invalid club unavailability")
		}
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := loadCompetition(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if c.Revision != expected {
		return ErrConflict
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO junior_fixture_constraints(competition_id,settings) VALUES($1,$2::jsonb) ON CONFLICT(competition_id) DO UPDATE SET settings=EXCLUDED.settings,updated_at=now()`, id, raw)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "competition", id, "constraints_updated", f); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) GenerateFixtures(ctx context.Context, p Principal, id, expected int64, replaceExisting ...bool) error {
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := loadCompetition(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if c.Revision != expected {
		return ErrConflict
	}
	if c.PublicationID != 0 {
		return fixtureValidation("published schedules require manual draft corrections; regeneration disabled")
	}
	var existing int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM junior_fixtures WHERE competition_id=$1 AND NOT retired`, id).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 && (len(replaceExisting) == 0 || !replaceExisting[0]) {
		return fixtureValidation("acknowledge replacing the existing draft; retired draft history is retained")
	}
	entries, err := competitionEntries(ctx, tx, id)
	if err != nil {
		return err
	}
	constraints, err := loadFixtureConstraints(ctx, tx, id)
	if err != nil {
		return err
	}
	fixtures, err := GenerateFixtureSchedule(c, entries, constraints)
	if err != nil {
		return err
	}
	otherPublished, err := loadFixtureRows(ctx, tx, 0, true, 0)
	if err != nil {
		return err
	}
	fixtures, err = ScheduleGeneratedFixtureDates(c, constraints, fixtures, otherPublished)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"competition": c, "entries": entries, "constraints": constraints, "known_junior_bookings": otherPublished})
	if err != nil {
		return err
	}
	var generationID int64
	err = tx.QueryRow(ctx, `INSERT INTO junior_fixture_generations(competition_id,based_on_revision,inputs,created_by_admin_id) VALUES($1,$2,$3::jsonb,$4) RETURNING id`, id, c.Revision, raw, p.AdminID).Scan(&generationID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_fixtures SET retired=true WHERE competition_id=$1 AND NOT retired`, id); err != nil {
		return err
	}
	for _, f := range fixtures {
		if _, err = tx.Exec(ctx, `INSERT INTO junior_fixtures(competition_id,generation_id,home_team_id,away_team_id,match_date,start_time,ground_name) VALUES($1,$2,$3,$4,$5::date,$6,$7)`, id, generationID, f.HomeTeamID, f.AwayTeamID, f.MatchDate, f.StartTime, f.GroundName); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "competition", id, "fixtures_generated", map[string]any{"generation_id": generationID, "fixture_count": len(fixtures), "retired_draft_count": existing}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) SaveDraftFixture(ctx context.Context, p Principal, id, expected int64, f Fixture) error {
	f.CompetitionID = id
	if _, err := fixtureDay(f.MatchDate); err != nil || !validFixtureTime(f.StartTime) || len(f.GroundName) > 200 || strings.ContainsAny(f.GroundName, "\r\n\x00") || f.HomeTeamID <= 0 || f.HomeTeamID == f.AwayTeamID {
		return fixtureValidation("valid distinct teams, date and time required")
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := loadCompetition(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if c.Revision != expected {
		return ErrConflict
	}
	entries, err := competitionEntries(ctx, tx, id)
	if err != nil {
		return err
	}
	valid := map[int64]bool{}
	for _, e := range entries {
		valid[e.TeamID] = true
	}
	if !valid[f.HomeTeamID] || !valid[f.AwayTeamID] {
		return fixtureValidation("both teams must be active entries")
	}
	if f.MatchDate < c.StartDate || f.MatchDate > c.EndDate {
		return fixtureValidation("fixture outside competition window")
	}
	if f.ID == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO junior_fixtures(competition_id,home_team_id,away_team_id,match_date,start_time,ground_name) VALUES($1,$2,$3,$4::date,$5,$6) RETURNING id`, id, f.HomeTeamID, f.AwayTeamID, f.MatchDate, f.StartTime, strings.TrimSpace(f.GroundName)).Scan(&f.ID)
	} else {
		var oldHome, oldAway int64
		var observed bool
		err = tx.QueryRow(ctx, `SELECT home_team_id,away_team_id,EXISTS(SELECT 1 FROM junior_result_observations o WHERE o.fixture_id=f.id) FROM junior_fixtures f WHERE id=$1 AND competition_id=$2 AND NOT retired`, f.ID, id).Scan(&oldHome, &oldAway, &observed)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if observed && (oldHome != f.HomeTeamID || oldAway != f.AwayTeamID) {
			return fixtureValidation("teams cannot change after result evidence exists")
		}
		_, err = tx.Exec(ctx, `UPDATE junior_fixtures SET home_team_id=$3,away_team_id=$4,match_date=$5::date,start_time=$6,ground_name=$7 WHERE id=$1 AND competition_id=$2`, f.ID, id, f.HomeTeamID, f.AwayTeamID, f.MatchDate, f.StartTime, strings.TrimSpace(f.GroundName))
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "fixture", f.ID, "draft_saved", f); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func loadFixtureRows(ctx context.Context, q DBTX, id int64, published bool, clubID int32, selectedPublication ...int64) ([]Fixture, error) {
	publicationID := int64(0)
	if len(selectedPublication) > 0 {
		publicationID = selectedPublication[0]
	}
	table, predicate, publication, fixtureID, compID := `junior_fixtures f`, `NOT f.retired`, `0::bigint`, `f.id`, `f.competition_id`
	constraintsJoin := ` LEFT JOIN junior_fixture_constraints fc ON fc.competition_id=f.competition_id `
	constraintJSON := `COALESCE(fc.settings,'{}'::jsonb)`
	if published {
		table = `junior_competitions pc JOIN junior_publication_fixtures f ON f.publication_id=pc.current_publication_id`
		predicate = `true`
		publication = `f.publication_id`
		fixtureID = `f.fixture_id`
		compID = `pc.id`
		constraintsJoin = ` JOIN junior_fixture_publications pub ON pub.id=f.publication_id `
		constraintJSON = `COALESCE(pub.constraints_snapshot->'constraints','{}'::jsonb)`
		if publicationID > 0 {
			table = `junior_fixture_publications selected_pub JOIN junior_competitions pc ON pc.id=selected_pub.competition_id JOIN junior_publication_fixtures f ON f.publication_id=selected_pub.id`
			predicate = `selected_pub.id=$3`
		}
	}
	query := `SELECT ` + fixtureID + `,` + compID + `,` + publication + `,f.home_team_id,f.away_team_id,h.club_id,a.club_id,f.match_date::text,f.start_time,f.ground_name,h.name,a.name,hc.name,ac.name,` + constraintJSON + `,co.season_id,co.name,co.age_group,co.division FROM ` + table + constraintsJoin + ` JOIN junior_competitions co ON co.id=` + compID + ` JOIN junior_teams h ON h.id=f.home_team_id JOIN junior_teams a ON a.id=f.away_team_id JOIN clubs hc ON hc.id=h.club_id JOIN clubs ac ON ac.id=a.club_id WHERE ` + predicate + ` AND ($1::bigint=0 OR ` + compID + `=$1) AND ($2::int=0 OR h.club_id=$2 OR a.club_id=$2) ORDER BY f.match_date,f.start_time,` + fixtureID
	args := []any{id, clubID}
	if published && publicationID > 0 {
		args = append(args, publicationID)
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Fixture{}
	for rows.Next() {
		var f Fixture
		var raw []byte
		if err = rows.Scan(&f.ID, &f.CompetitionID, &f.PublicationID, &f.HomeTeamID, &f.AwayTeamID, &f.HomeClubID, &f.AwayClubID, &f.MatchDate, &f.StartTime, &f.GroundName, &f.HomeTeamName, &f.AwayTeamName, &f.HomeClubName, &f.AwayClubName, &raw, &f.SeasonID, &f.CompetitionName, &f.AgeGroup, &f.Division); err != nil {
			return nil, err
		}
		var snapshot FixtureConstraints
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return nil, err
		}
		f.DurationMinutes = snapshot.DurationMinutes
		for _, g := range snapshot.Grounds {
			if strings.EqualFold(strings.TrimSpace(g.Name), strings.TrimSpace(f.GroundName)) && (f.GroundCapacity == 0 || g.Capacity < f.GroundCapacity) {
				f.GroundCapacity = g.Capacity
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *Service) ListFixtures(ctx context.Context, p Principal, id int64, published bool) ([]Fixture, error) {
	if err := Authorize(ctx, s.DB, p, p.ClubID, false); err != nil {
		return nil, err
	}
	if p.AdminID == 0 || p.ReadOnly {
		published = true
	}
	return loadFixtureRows(ctx, s.DB, id, published, p.ClubID)
}
func (s *Service) FixtureChecks(ctx context.Context, p Principal, id int64) ([]FixtureIssue, error) {
	if p.AdminID == 0 || p.ReadOnly {
		return nil, ErrForbidden
	}
	c, err := s.GetCompetition(ctx, p, id)
	if err != nil {
		return nil, err
	}
	constraints, err := loadFixtureConstraints(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	drafts, err := loadFixtureRows(ctx, s.DB, id, false, 0)
	if err != nil {
		return nil, err
	}
	all, err := loadFixtureRows(ctx, s.DB, 0, true, 0)
	if err != nil {
		return nil, err
	}
	others := []Fixture{}
	for _, f := range all {
		if f.CompetitionID != id {
			others = append(others, f)
		}
	}
	issues := CheckFixtureSchedule(c, constraints, drafts, others)
	entries, err := competitionEntries(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	if err = validateFixturePairings(c, entries, drafts); err != nil {
		issues = append(issues, FixtureIssue{"pairings", err.Error(), true})
	}
	return issues, nil
}
func (s *Service) PublishFixtures(ctx context.Context, p Principal, id, expected int64, acknowledge bool, reason string) (int64, error) {
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Serialise all junior publications while checking cross-competition bookings.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(764320097)`); err != nil {
		return 0, err
	}
	c, err := loadCompetition(ctx, tx, id, true)
	if err != nil {
		return 0, err
	}
	if c.Revision != expected {
		return 0, ErrConflict
	}
	constraints, err := loadFixtureConstraints(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	drafts, err := loadFixtureRows(ctx, tx, id, false, 0)
	if err != nil {
		return 0, err
	}
	all, err := loadFixtureRows(ctx, tx, 0, true, 0)
	if err != nil {
		return 0, err
	}
	others := []Fixture{}
	for _, f := range all {
		if f.CompetitionID != id {
			others = append(others, f)
		}
	}
	entries, err := competitionEntries(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	active := map[int64]bool{}
	for _, e := range entries {
		active[e.TeamID] = true
	}
	for _, f := range drafts {
		if !active[f.HomeTeamID] || !active[f.AwayTeamID] {
			return 0, fixtureValidation("draft contains a withdrawn team entry")
		}
	}
	issues := CheckFixtureSchedule(c, constraints, drafts, others)
	if err = validateFixturePairings(c, entries, drafts); err != nil {
		return 0, err
	}
	for _, issue := range issues {
		if issue.Blocking {
			return 0, fixtureValidation(issue.Message)
		}
	}
	if !acknowledge || strings.TrimSpace(reason) == "" || len(reason) > 10000 {
		return 0, fixtureValidation("record acknowledgement of ground/source coverage gaps before publication")
	}
	raw, err := json.Marshal(map[string]any{"constraints": constraints, "issues": issues})
	if err != nil {
		return 0, err
	}
	var publicationID int64
	err = tx.QueryRow(ctx, `INSERT INTO junior_fixture_publications(competition_id,revision,constraints_snapshot,coverage_acknowledged,acknowledgement_reason,published_by_admin_id) VALUES($1,$2,$3::jsonb,true,$4,$5) RETURNING id`, id, c.Revision+1, raw, strings.TrimSpace(reason), p.AdminID).Scan(&publicationID)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO junior_publication_fixtures(publication_id,fixture_id,home_team_id,away_team_id,match_date,start_time,ground_name) SELECT $2,id,home_team_id,away_team_id,match_date,start_time,ground_name FROM junior_fixtures WHERE competition_id=$1 AND NOT retired`, id, publicationID)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET current_publication_id=$2,revision=revision+1 WHERE id=$1`, id, publicationID); err != nil {
		return 0, err
	}
	if err = QueueFixturePublication(ctx, tx, p, publicationID, id); err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, p, 0, "competition", id, "fixtures_published", map[string]any{"publication_id": publicationID, "fixture_count": len(drafts), "acknowledgement": reason}); err != nil {
		return 0, err
	}
	return publicationID, tx.Commit(ctx)
}

func validateFixturePairings(c Competition, entries []CompetitionEntry, fixtures []Fixture) error {
	if c.Format == "custom" {
		return nil
	}
	counts := map[[2]int64]int{}
	for _, f := range fixtures {
		home, away := f.HomeTeamID, f.AwayTeamID
		if c.Format == "once" && home > away {
			home, away = away, home
		}
		counts[[2]int64{home, away}]++
	}
	for i, h := range entries {
		for j, a := range entries {
			if i == j || (c.Format == "once" && i > j) {
				continue
			}
			home, away := h.TeamID, a.TeamID
			if c.Format == "once" && home > away {
				home, away = away, home
			}
			if counts[[2]int64{home, away}] != 1 {
				return fixtureValidation("schedule must contain each required pairing exactly once for its format")
			}
		}
	}
	return nil
}

func (s *Service) RetireDraftFixture(ctx context.Context, p Principal, id, expected, fixtureID int64, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fixtureValidation("reason required when removing a draft fixture")
	}
	tx, err := s.fixtureAdminTx(ctx, p)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := loadCompetition(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if c.Revision != expected {
		return ErrConflict
	}
	var observed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_result_observations o WHERE o.fixture_id=f.id) FROM junior_fixtures f WHERE id=$1 AND competition_id=$2 AND NOT retired`, fixtureID, id).Scan(&observed)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if observed {
		return fixtureValidation("a fixture with result evidence cannot be removed")
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_fixtures SET retired=true WHERE id=$1`, fixtureID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, 0, "fixture", fixtureID, "draft_retired", map[string]any{"reason": strings.TrimSpace(reason)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
