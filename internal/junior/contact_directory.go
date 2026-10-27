package junior

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// NormaliseAudienceFilter makes selection order irrelevant to preview freshness.
func NormaliseAudienceFilter(f AudienceFilter) (AudienceFilter, error) {
	if f.Selection == "" {
		f.Selection = "group"
	}
	if f.SeasonID < 0 || f.CompetitionID < 0 || f.ClubID < 0 || len(f.ClubIDs) > 1000 || len(f.ContactIDs) > 2000 ||
		(f.Selection != "group" && f.Selection != "contacts") ||
		(f.Role != "" && f.Role != "junior_lead" && f.Role != "safeguarding_officer" && f.Role != "coach") ||
		(f.AgeGroup != "" && f.AgeGroup != "U9" && f.AgeGroup != "U11" && f.AgeGroup != "U13" && f.AgeGroup != "U15" && f.AgeGroup != "U18") {
		return f, ErrValidation
	}
	clubs := map[int32]bool{}
	contacts := map[int64]bool{}
	for _, id := range f.ClubIDs {
		if id <= 0 {
			return f, ErrValidation
		}
		clubs[id] = true
	}
	for _, id := range f.ContactIDs {
		if id <= 0 {
			return f, ErrValidation
		}
		contacts[id] = true
	}
	f.ClubIDs = make([]int32, 0, len(clubs))
	f.ContactIDs = make([]int64, 0, len(contacts))
	for id := range clubs {
		f.ClubIDs = append(f.ClubIDs, id)
	}
	for id := range contacts {
		f.ContactIDs = append(f.ContactIDs, id)
	}
	sort.Slice(f.ClubIDs, func(i, j int) bool { return f.ClubIDs[i] < f.ClubIDs[j] })
	sort.Slice(f.ContactIDs, func(i, j int) bool { return f.ContactIDs[i] < f.ContactIDs[j] })
	if (f.Selection == "contacts" && len(f.ContactIDs) == 0) || (f.Selection == "group" && len(f.ContactIDs) > 0) {
		return f, fmt.Errorf("%w: choose at least one contact, or use a group audience", ErrValidation)
	}
	return f, nil
}

type DirectoryContact struct {
	ID                                                            int64
	ClubID                                                        int32
	Club, Name, Email, Phone, Role, AgeGroup, Team, Participation string
	Inactive                                                      bool
}

// ContactDirectory includes phone-only and unassigned contacts as well as people
// eligible for email. One row represents one current role, not one email address.
func (s *Service) ContactDirectory(ctx context.Context, p Principal, f AudienceFilter, query string) ([]DirectoryContact, error) {
	if p.AdminID <= 0 || p.ReadOnly {
		return nil, ErrForbidden
	}
	if err := Authorize(ctx, s.DB, p, 0, false); err != nil {
		return nil, err
	}
	var err error
	f, err = NormaliseAudienceFilter(f)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if len(query) > 100 {
		return nil, ErrValidation
	}
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.club_id,cl.name,c.full_name,c.email,c.phone,
 COALESCE(r.role,''),COALESCE(r.age_group,''),COALESCE(t.name,''),COALESCE(cs.state,'not_entered'),COALESCE(st.inactive,false)
 FROM junior_contacts c JOIN clubs cl ON cl.id=c.club_id
 LEFT JOIN junior_club_status st ON st.club_id=c.club_id
 LEFT JOIN junior_club_seasons cs ON cs.club_id=c.club_id AND cs.season_id=$1
 LEFT JOIN junior_contact_roles r ON r.contact_id=c.id AND r.club_id=c.club_id AND r.season_id=$1
 AND r.starts_on<=(now() AT TIME ZONE 'Europe/London')::date AND (r.ends_on IS NULL OR r.ends_on>=(now() AT TIME ZONE 'Europe/London')::date)
 LEFT JOIN junior_teams t ON t.id=r.team_id AND t.club_id=c.club_id
 WHERE NOT c.archived AND ($2::integer=0 OR c.club_id=$2) AND ($3::text='' OR r.role=$3) AND ($4::text='' OR r.age_group=$4)
 AND ($5::bigint=0 OR EXISTS(SELECT 1 FROM junior_competition_entries e JOIN junior_teams et ON et.id=e.team_id
 JOIN junior_competitions co ON co.id=e.competition_id WHERE co.id=$5 AND co.season_id=$1 AND et.club_id=c.club_id AND e.active AND NOT et.archived
 AND (r.role<>'coach' OR (r.age_group=co.age_group AND (r.team_id IS NULL OR r.team_id=et.id)))))
 AND ($6::text='' OR concat_ws(' ',cl.name,c.full_name,c.email,c.phone) ILIKE '%'||$6||'%')
 ORDER BY cl.name,c.full_name,c.id,r.role,r.age_group,r.id`, f.SeasonID, f.ClubID, f.Role, f.AgeGroup, f.CompetitionID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DirectoryContact{}
	for rows.Next() {
		var c DirectoryContact
		if err = rows.Scan(&c.ID, &c.ClubID, &c.Club, &c.Name, &c.Email, &c.Phone, &c.Role, &c.AgeGroup, &c.Team, &c.Participation, &c.Inactive); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *Service) SetClubInactive(ctx context.Context, p Principal, clubID int32, inactive bool, reason string) error {
	if p.AdminID <= 0 {
		return ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	if clubID <= 0 || reason == "" || len(reason) > 300 {
		return ErrValidation
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	var id int32
	if err = tx.QueryRow(ctx, `SELECT id FROM clubs WHERE id=$1 FOR UPDATE`, clubID).Scan(&id); err != nil {
		return recordsError(err)
	}
	var before bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT inactive FROM junior_club_status WHERE club_id=$1),false)`, clubID).Scan(&before); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO junior_club_status(club_id,inactive,reason) VALUES($1,$2,$3)
 ON CONFLICT(club_id) DO UPDATE SET inactive=EXCLUDED.inactive,reason=EXCLUDED.reason,updated_at=now()`, clubID, inactive, reason); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, clubID, "club", int64(clubID), "junior_status_changed", map[string]any{"was_inactive": before, "inactive": inactive, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
