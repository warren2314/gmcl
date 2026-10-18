package junior

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func recordsError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		switch postgres.Code {
		case "23505":
			return fmt.Errorf("%w: that name or role assignment already exists", ErrConflict)
		case "23503", "23514":
			return fmt.Errorf("%w: check the selected club, season, team and dates", ErrValidation)
		}
	}
	return err
}

func recordsReadScope(p Principal) int32 {
	if p.AdminID > 0 && !p.ReadOnly {
		return 0
	}
	return p.ClubID
}

func (s *Service) ListSeasons(ctx context.Context, p Principal) ([]Season, error) {
	if err := Authorize(ctx, s.DB, p, recordsReadScope(p), false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id,name,start_date,end_date,state,linked_season_id FROM junior_seasons ORDER BY start_date DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Season
	for rows.Next() {
		var item Season
		if err = rows.Scan(&item.ID, &item.Name, &item.StartDate, &item.EndDate, &item.State, &item.LinkedSeasonID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func recordsSeason(ctx context.Context, q DBTX, id int64) (Season, error) {
	var item Season
	err := q.QueryRow(ctx, `SELECT id,name,start_date,end_date,state,linked_season_id FROM junior_seasons WHERE id=$1`, id).Scan(&item.ID, &item.Name, &item.StartDate, &item.EndDate, &item.State, &item.LinkedSeasonID)
	return item, recordsError(err)
}

func (s *Service) SaveSeason(ctx context.Context, p Principal, in SeasonInput) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		return 0, err
	}
	if p.AdminID == 0 {
		return 0, ErrForbidden
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 || in.StartDate.IsZero() || in.EndDate.IsZero() || in.EndDate.Before(in.StartDate) {
		return 0, fmt.Errorf("%w: enter a season name and a valid date window", ErrValidation)
	}
	if in.State != "open" && in.State != "closed" {
		return 0, fmt.Errorf("%w: choose open or closed", ErrValidation)
	}
	if in.ID > 0 {
		err = tx.QueryRow(ctx, `UPDATE junior_seasons SET name=$2,start_date=$3,end_date=$4,state=$5,linked_season_id=$6,updated_at=now() WHERE id=$1 RETURNING id`, in.ID, in.Name, in.StartDate.Format("2006-01-02"), in.EndDate.Format("2006-01-02"), in.State, in.LinkedSeasonID).Scan(&in.ID)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO junior_seasons(name,start_date,end_date,state,linked_season_id) VALUES($1,$2,$3,$4,$5) RETURNING id`, in.Name, in.StartDate.Format("2006-01-02"), in.EndDate.Format("2006-01-02"), in.State, in.LinkedSeasonID).Scan(&in.ID)
	}
	if err != nil {
		return 0, recordsError(err)
	}
	if err = Audit(ctx, tx, p, 0, "season", in.ID, "season_saved", in); err != nil {
		return 0, err
	}
	return in.ID, tx.Commit(ctx)
}

func (s *Service) ListClubs(ctx context.Context, p Principal, seasonID int64) ([]ClubSummary, error) {
	if err := Authorize(ctx, s.DB, p, 0, false); err != nil {
		return nil, err
	}
	if p.AdminID == 0 {
		return nil, ErrForbidden
	}
	if seasonID > 0 {
		if _, err := recordsSeason(ctx, s.DB, seasonID); err != nil {
			return nil, err
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.name,COALESCE(c.short_name,''),COALESCE(part.state,'not_entered'),
		(SELECT count(*) FROM junior_teams team WHERE team.club_id=c.id AND NOT team.archived),
		(SELECT count(*) FROM junior_contacts contact WHERE contact.club_id=c.id AND NOT contact.archived)
		FROM clubs c LEFT JOIN junior_club_seasons part ON part.club_id=c.id AND part.season_id=$1 ORDER BY c.name,c.id`, seasonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClubSummary
	for rows.Next() {
		var item ClubSummary
		if err = rows.Scan(&item.ID, &item.Name, &item.ShortName, &item.State, &item.TeamCount, &item.ContactCount); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) ClubProfile(ctx context.Context, p Principal, clubID int32, seasonID int64) (ClubProfile, error) {
	var result ClubProfile
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return result, err
	}
	if clubID <= 0 {
		return result, ErrNotFound
	}
	err := s.DB.QueryRow(ctx, `SELECT c.id,c.name,COALESCE(c.short_name,''),COALESCE(part.state,'not_entered') FROM clubs c LEFT JOIN junior_club_seasons part ON part.club_id=c.id AND part.season_id=$2 WHERE c.id=$1`, clubID, seasonID).Scan(&result.Club.ID, &result.Club.Name, &result.Club.ShortName, &result.Club.State)
	if err != nil {
		return result, recordsError(err)
	}
	if seasonID > 0 {
		result.Season, err = recordsSeason(ctx, s.DB, seasonID)
		if err != nil {
			return result, err
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT id,club_id,name,archived FROM junior_teams WHERE club_id=$1 ORDER BY archived,name,id`, clubID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item Team
		if err = rows.Scan(&item.ID, &item.ClubID, &item.Name, &item.Archived); err != nil {
			rows.Close()
			return result, err
		}
		result.Teams = append(result.Teams, item)
		if !item.Archived {
			result.Club.TeamCount++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.DB.Query(ctx, `SELECT id,club_id,full_name,email,phone,archived FROM junior_contacts WHERE club_id=$1 ORDER BY archived,full_name,id`, clubID)
	if err != nil {
		return result, err
	}
	contactIndex := map[int64]int{}
	for rows.Next() {
		var item Contact
		if err = rows.Scan(&item.ID, &item.ClubID, &item.FullName, &item.Email, &item.Phone, &item.Archived); err != nil {
			rows.Close()
			return result, err
		}
		contactIndex[item.ID] = len(result.Contacts)
		result.Contacts = append(result.Contacts, item)
		if !item.Archived {
			result.Club.ContactCount++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.DB.Query(ctx, `SELECT role.id,role.contact_id,role.season_id,role.role,role.age_group,role.team_id,COALESCE(team.name,''),role.starts_on,role.ends_on FROM junior_contact_roles role LEFT JOIN junior_teams team ON team.id=role.team_id AND team.club_id=role.club_id WHERE role.club_id=$1 AND ($2::bigint=0 OR role.season_id=$2) ORDER BY role.starts_on DESC,role.id DESC`, clubID, seasonID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item ContactRole
		if err = rows.Scan(&item.ID, &item.ContactID, &item.SeasonID, &item.Role, &item.AgeGroup, &item.TeamID, &item.TeamName, &item.StartsOn, &item.EndsOn); err != nil {
			rows.Close()
			return result, err
		}
		if index, ok := contactIndex[item.ContactID]; ok {
			result.Contacts[index].Roles = append(result.Contacts[index].Roles, item)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.DB.Query(ctx, `SELECT part.season_id,season.name,part.state,part.provenance FROM junior_club_seasons part JOIN junior_seasons season ON season.id=part.season_id WHERE part.club_id=$1 ORDER BY season.start_date DESC,part.season_id DESC`, clubID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item Participation
		if err = rows.Scan(&item.SeasonID, &item.SeasonName, &item.State, &item.Provenance); err != nil {
			rows.Close()
			return result, err
		}
		result.Participation = append(result.Participation, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.DB.Query(ctx, `SELECT badge.id,badge.club_id,badge.storage_key,badge.sha256,badge.media_type,badge.byte_size,badge.width,badge.height,badge.original_name,badge.created_at,COALESCE(current.revision_id=badge.id,false) FROM junior_badge_revisions badge LEFT JOIN junior_club_badges current ON current.club_id=badge.club_id WHERE badge.club_id=$1 ORDER BY badge.created_at DESC,badge.id DESC`, clubID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item BadgeRevision
		var current bool
		if err = rows.Scan(&item.ID, &item.ClubID, &item.StorageKey, &item.SHA256, &item.MediaType, &item.ByteSize, &item.Width, &item.Height, &item.OriginalName, &item.CreatedAt, &current); err != nil {
			rows.Close()
			return result, err
		}
		result.Badges = append(result.Badges, item)
		if current {
			result.BadgeID = item.ID
		}
	}
	err = rows.Err()
	rows.Close()
	return result, err
}

func (s *Service) SaveClub(ctx context.Context, p Principal, in ClubInput) (int32, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, in.ID, true); err != nil {
		return 0, err
	}
	if p.AdminID == 0 {
		return 0, ErrForbidden
	}
	in.Name, in.ShortName = strings.TrimSpace(in.Name), strings.TrimSpace(in.ShortName)
	if in.Name == "" || len(in.Name) > 200 || len(in.ShortName) > 80 || strings.IndexFunc(in.Name+in.ShortName, unicode.IsControl) >= 0 {
		return 0, fmt.Errorf("%w: enter a club name of at most 200 characters", ErrValidation)
	}
	if in.ID > 0 {
		err = tx.QueryRow(ctx, `UPDATE clubs SET name=$2,short_name=NULLIF($3,'') WHERE id=$1 RETURNING id`, in.ID, in.Name, in.ShortName).Scan(&in.ID)
	} else {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "junior-new-club:"+strings.ToLower(in.Name)); err != nil {
			return 0, err
		}
		var existing int32
		err = tx.QueryRow(ctx, `SELECT id FROM clubs WHERE LOWER(BTRIM(name))=LOWER(BTRIM($1)) ORDER BY id LIMIT 1`, in.Name).Scan(&existing)
		if err == nil {
			return 0, fmt.Errorf("%w: this club already exists; select its existing profile", ErrConflict)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO clubs(name,short_name) VALUES($1,NULLIF($2,'')) RETURNING id`, in.Name, in.ShortName).Scan(&in.ID)
	}
	if err != nil {
		return 0, recordsError(err)
	}
	if err = Audit(ctx, tx, p, in.ID, "club", int64(in.ID), "club_saved", in); err != nil {
		return 0, err
	}
	return in.ID, tx.Commit(ctx)
}

func recordsSeasonWrite(ctx context.Context, q DBTX, p Principal, seasonID int64) error {
	season, err := recordsSeason(ctx, q, seasonID)
	if err != nil {
		return err
	}
	if p.AdminID == 0 && season.State != "open" {
		return fmt.Errorf("%w: this junior season is closed", ErrConflict)
	}
	return nil
}

func (s *Service) SetParticipation(ctx context.Context, p Principal, clubID int32, seasonID int64, state, source string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	if err = recordsSeasonWrite(ctx, tx, p, seasonID); err != nil {
		return err
	}
	if state != "active" && state != "withdrawn" {
		return fmt.Errorf("%w: choose active or withdrawn", ErrValidation)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}
	if len(source) > 300 {
		return fmt.Errorf("%w: provenance is too long", ErrValidation)
	}
	provenance, _ := json.Marshal(map[string]string{"source": source})
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("junior-participation:%d:%d", clubID, seasonID)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO junior_club_seasons(club_id,season_id,state,provenance) VALUES($1,$2,$3,$4::jsonb) ON CONFLICT(club_id,season_id) DO UPDATE SET state=EXCLUDED.state,provenance=EXCLUDED.provenance,updated_at=now()`, clubID, seasonID, state, string(provenance))
	if err != nil {
		return recordsError(err)
	}
	if err = Audit(ctx, tx, p, clubID, "club_participation", seasonID, "participation_saved", map[string]any{"season_id": seasonID, "state": state, "source": source}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) SaveTeam(ctx context.Context, p Principal, in TeamInput) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, in.ClubID, true); err != nil {
		return 0, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return 0, fmt.Errorf("%w: enter a team name of at most 120 characters", ErrValidation)
	}
	if in.ID > 0 {
		err = tx.QueryRow(ctx, `UPDATE junior_teams SET name=$3,archived=$4,updated_at=now() WHERE id=$1 AND club_id=$2 RETURNING id`, in.ID, in.ClubID, in.Name, in.Archived).Scan(&in.ID)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO junior_teams(club_id,name,archived) VALUES($1,$2,$3) RETURNING id`, in.ClubID, in.Name, in.Archived).Scan(&in.ID)
	}
	if err != nil {
		return 0, recordsError(err)
	}
	if err = Audit(ctx, tx, p, in.ClubID, "team", in.ID, "team_saved", in); err != nil {
		return 0, err
	}
	return in.ID, tx.Commit(ctx)
}

func validateRecordContact(in *ContactInput) error {
	in.FullName, in.Email, in.Phone = strings.TrimSpace(in.FullName), strings.TrimSpace(in.Email), strings.TrimSpace(in.Phone)
	if in.FullName == "" || len(in.FullName) > 200 || strings.IndexFunc(in.FullName+in.Phone, unicode.IsControl) >= 0 || len(in.Phone) > 80 {
		return fmt.Errorf("%w: enter a name and a valid email or phone", ErrValidation)
	}
	if in.Email != "" {
		var err error
		in.Email, err = CanonicalEmail(in.Email)
		if err != nil {
			return err
		}
	}
	if in.Email == "" && in.Phone == "" {
		return fmt.Errorf("%w: enter an email address or phone number", ErrValidation)
	}
	if in.Phone != "" {
		digits := 0
		for _, r := range in.Phone {
			if unicode.IsDigit(r) {
				digits++
			}
		}
		if digits < 5 {
			return fmt.Errorf("%w: enter a valid phone number", ErrValidation)
		}
	}
	if in.Role == "" {
		return nil
	}
	switch in.Role {
	case "junior_lead", "safeguarding_officer":
		if in.AgeGroup != "" || in.TeamID != nil {
			return fmt.Errorf("%w: lead and safeguarding roles are club-wide", ErrValidation)
		}
		if in.Role == "junior_lead" && in.Email == "" {
			return fmt.Errorf("%w: the Junior Lead needs an email address", ErrValidation)
		}
	case "coach":
		switch in.AgeGroup {
		case "U9", "U11", "U13", "U15", "U18":
		default:
			return fmt.Errorf("%w: choose the coach's age group", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: choose a recognised contact role", ErrValidation)
	}
	if in.StartsOn.IsZero() || in.EndsOn != nil && in.EndsOn.Before(in.StartsOn) {
		return fmt.Errorf("%w: enter valid role start and end dates", ErrValidation)
	}
	return nil
}

func (s *Service) SaveContact(ctx context.Context, p Principal, in ContactInput) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, in.ClubID, true); err != nil {
		return 0, err
	}
	if err = validateRecordContact(&in); err != nil {
		return 0, err
	}
	if err = recordsSeasonWrite(ctx, tx, p, in.SeasonID); err != nil {
		return 0, err
	}
	if in.ID > 0 && in.Email == "" {
		var currentLead bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_contact_roles WHERE contact_id=$1 AND club_id=$2 AND role='junior_lead' AND (ends_on IS NULL OR ends_on>=(now() AT TIME ZONE 'Europe/London')::date))`, in.ID, in.ClubID).Scan(&currentLead); err != nil {
			return 0, err
		}
		if currentLead {
			return 0, fmt.Errorf("%w: a current or upcoming Junior Lead needs an email address; end the role before removing it", ErrValidation)
		}
	}
	if in.ID > 0 {
		err = tx.QueryRow(ctx, `UPDATE junior_contacts SET full_name=$3,email=$4,phone=$5,archived=$6,updated_at=now() WHERE id=$1 AND club_id=$2 RETURNING id`, in.ID, in.ClubID, in.FullName, in.Email, in.Phone, in.Archived).Scan(&in.ID)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO junior_contacts(club_id,full_name,email,phone,archived) VALUES($1,$2,$3,$4,$5) RETURNING id`, in.ClubID, in.FullName, in.Email, in.Phone, in.Archived).Scan(&in.ID)
	}
	if err != nil {
		return 0, recordsError(err)
	}
	if in.Role != "" {
		if _, _, err = recordsAddRole(ctx, tx, in); err != nil {
			return 0, err
		}
	}
	if err = Audit(ctx, tx, p, in.ClubID, "contact", in.ID, "contact_saved", in); err != nil {
		return 0, err
	}
	return in.ID, tx.Commit(ctx)
}

func (s *Service) EndContactRole(ctx context.Context, p Principal, clubID int32, roleID int64, endDate time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	var seasonID int64
	var startsOn time.Time
	if err = tx.QueryRow(ctx, `SELECT season_id,starts_on FROM junior_contact_roles WHERE id=$1 AND club_id=$2 FOR UPDATE`, roleID, clubID).Scan(&seasonID, &startsOn); err != nil {
		return recordsError(err)
	}
	if err = recordsSeasonWrite(ctx, tx, p, seasonID); err != nil {
		return err
	}
	if endDate.IsZero() || endDate.Before(startsOn) {
		return fmt.Errorf("%w: end date must be on or after the role's start date", ErrValidation)
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_contact_roles SET ends_on=$3 WHERE id=$1 AND club_id=$2`, roleID, clubID, endDate.Format("2006-01-02")); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, clubID, "contact_role", roleID, "contact_role_ended", map[string]any{"ends_on": endDate.Format("2006-01-02")}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
