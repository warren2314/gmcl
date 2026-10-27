package junior

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
)

// Workflow messages use current contacts and retain one immutable audience
// snapshot. A verified lead account is a fallback, not a contact-role grant.
func workflowRecipients(ctx context.Context, q DBTX, clubID int32, seasonID int64, ageGroup string) ([]Recipient, error) {
	var inactive bool
	if err := q.QueryRow(ctx, `SELECT COALESCE((SELECT inactive FROM junior_club_status WHERE club_id=$1),false)`, clubID).Scan(&inactive); err != nil {
		return nil, err
	}
	if inactive {
		return []Recipient{}, nil
	}
	rows, err := q.Query(ctx, `SELECT c.id,c.email,c.full_name FROM junior_contacts c
 WHERE c.club_id=$1 AND NOT c.archived AND c.email<>'' AND EXISTS(
 SELECT 1 FROM junior_contact_roles r WHERE r.contact_id=c.id AND r.club_id=c.club_id AND r.season_id=$2
 AND r.starts_on<=(now() AT TIME ZONE 'Europe/London')::date
 AND (r.ends_on IS NULL OR r.ends_on>=(now() AT TIME ZONE 'Europe/London')::date)
 AND (r.role='junior_lead' OR ($3<>'' AND r.role='coach' AND r.age_group=$3)))
 ORDER BY c.id`, clubID, seasonID, ageGroup)
	if err != nil {
		return nil, err
	}
	recipients := []Recipient{}
	seen := map[string]bool{}
	for rows.Next() {
		var r Recipient
		r.ClubID = clubID
		if err = rows.Scan(&r.ContactID, &r.Email, &r.Name); err != nil {
			rows.Close()
			return nil, err
		}
		canonical, valid := CanonicalEmail(r.Email)
		if valid != nil {
			continue
		}
		r.Email = canonical
		if !seen[canonical] {
			seen[canonical] = true
			recipients = append(recipients, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT a.email FROM junior_memberships m JOIN junior_accounts a ON a.id=m.account_id
 WHERE m.club_id=$1 AND m.revoked_at IS NULL AND m.accepted_at IS NOT NULL AND a.is_active`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var address string
		if err = rows.Scan(&address); err != nil {
			return nil, err
		}
		canonical, valid := CanonicalEmail(address)
		if valid != nil {
			continue
		}
		if !seen[canonical] {
			seen[canonical] = true
			recipients = append(recipients, Recipient{ClubID: clubID, Email: canonical, Name: "Junior Lead"})
		}
	}
	return recipients, rows.Err()
}

func QueueFixturePublication(ctx context.Context, q DBTX, p Principal, publicationID, competitionID int64) error {
	if err := Authorize(ctx, q, p, 0, true); err != nil {
		return err
	}
	var seasonID int64
	var name, ageGroup, division string
	if err := q.QueryRow(ctx, "SELECT season_id,name,age_group,division FROM junior_competitions WHERE id=$1", competitionID).Scan(&seasonID, &name, &ageGroup, &division); err != nil {
		return err
	}
	if division != "" {
		name += " · " + division
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT t.club_id FROM junior_publication_fixtures f
 JOIN junior_teams t ON t.id=f.home_team_id OR t.id=f.away_team_id
 WHERE f.publication_id=$1 ORDER BY t.club_id`, publicationID)
	if err != nil {
		return err
	}
	clubs := []int32{}
	for rows.Next() {
		var id int32
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		clubs = append(clubs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, clubID := range clubs {
		recipients, err := workflowRecipients(ctx, q, clubID, seasonID, ageGroup)
		if err != nil {
			return err
		}
		fixtures, err := q.Query(ctx, `SELECT home.name,away.name,f.match_date::text,f.start_time::text,COALESCE(f.ground_name,'')
  FROM junior_publication_fixtures f JOIN junior_teams home ON home.id=f.home_team_id JOIN junior_teams away ON away.id=f.away_team_id
  WHERE f.publication_id=$1 AND (home.club_id=$2 OR away.club_id=$2) ORDER BY f.match_date,f.start_time,f.fixture_id`, publicationID, clubID)
		if err != nil {
			return err
		}
		var body strings.Builder
		fmt.Fprintf(&body, "Official junior fixtures have been published for %s (%s).\nPublication reference: %d.\n\n", name, ageGroup, publicationID)
		for fixtures.Next() {
			var home, away, date, start, ground string
			if err = fixtures.Scan(&home, &away, &date, &start, &ground); err != nil {
				fixtures.Close()
				return err
			}
			if ground == "" {
				ground = "Ground to be confirmed"
			}
			fmt.Fprintf(&body, "%s %s — %s v %s — %s\n", date, start, home, away, ground)
		}
		err = fixtures.Err()
		fixtures.Close()
		if err != nil {
			return err
		}
		body.WriteString("\nSign into your Junior Lead portal to view the official fixtures. Any correction will be published as a new revision.\n")
		_, err = Enqueue(ctx, q, p, MessageIntent{SeasonID: seasonID, ClubID: clubID, Kind: "fixture_publication", RecordType: "fixture_publication", RecordID: publicationID, Key: fmt.Sprintf("fixtures:%d:club:%d", publicationID, clubID), Subject: "Junior fixtures published: " + name, Body: body.String(), Recipients: recipients})
		if err != nil {
			return err
		}
	}
	return nil
}

func QueueConcessionInformation(ctx context.Context, q DBTX, p Principal, candidateID int64, clubID int32, message string) error {
	if err := Authorize(ctx, q, p, clubID, true); err != nil {
		return err
	}
	var seasonID int64
	var name, ageGroup, division string
	var linked bool
	var revision int
	err := q.QueryRow(ctx, `SELECT comp.season_id,comp.name,comp.age_group,comp.division,(home.club_id=$2 OR away.club_id=$2),candidate.revision
 FROM junior_concession_candidates candidate JOIN junior_fixtures f ON f.id=candidate.fixture_id
 JOIN junior_competitions comp ON comp.id=f.competition_id
 JOIN junior_teams home ON home.id=f.home_team_id JOIN junior_teams away ON away.id=f.away_team_id
 WHERE candidate.id=$1`, candidateID, clubID).Scan(&seasonID, &name, &ageGroup, &division, &linked, &revision)
	if err != nil {
		return err
	}
	if !linked {
		return ErrForbidden
	}
	if division != "" {
		name += " · " + division
	}
	recipients, err := workflowRecipients(ctx, q, clubID, seasonID, ageGroup)
	if err != nil {
		return err
	}
	_, err = Enqueue(ctx, q, p, MessageIntent{SeasonID: seasonID, ClubID: clubID, Kind: "concession_information", RecordType: "concession", RecordID: candidateID, Key: fmt.Sprintf("concession:%d:revision:%d:club:%d", candidateID, revision, clubID), Subject: "Information requested: " + name, Body: "The league is reviewing a possible junior concession. No finding is made by this information request.\n\n" + message + "\n\nPlease reply using the league contact details shown in your portal.", Recipients: recipients})
	return err
}

func (s *Service) QueueFormReminders(ctx context.Context, p Principal, formID int64) (int64, error) {
	if p.AdminID < 1 || p.ReadOnly {
		return 0, ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		return 0, err
	}
	var state string
	err = tx.QueryRow(ctx, `SELECT season.state FROM junior_forms f JOIN junior_seasons season ON season.id=f.season_id WHERE f.id=$1 FOR SHARE OF season`, formID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if state != "open" {
		return 0, fmt.Errorf("%w: this junior season is closed", ErrValidation)
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.club_id,f.season_id,v.title,v.due_date::text,
 (now() AT TIME ZONE 'Europe/London')::date::text
 FROM junior_form_assignments a JOIN junior_forms f ON f.id=a.form_id JOIN junior_form_versions v ON v.id=a.version_id
 WHERE a.form_id=$1 AND a.status IN('assigned','returned') ORDER BY a.id FOR UPDATE OF a`, formID)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id, season        int64
		club              int32
		title, due, today string
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.club, &item.season, &item.title, &item.due, &item.today); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var count int64
	for _, item := range items {
		recipients, err := workflowRecipients(ctx, tx, item.club, item.season, "")
		if err != nil {
			return 0, err
		}
		messageID, err := Enqueue(ctx, tx, p, MessageIntent{SeasonID: item.season, ClubID: item.club, Kind: "form_reminder", RecordType: "form_assignment", RecordID: item.id, Key: fmt.Sprintf("form-reminder:%d:%s", item.id, item.today), Subject: "Junior form reminder: " + item.title, Body: fmt.Sprintf("Your club still needs to complete %s.\nDeadline: %s.\n\nSign into your Junior Lead portal to save or submit the form. Clubs that have submitted are not included in this reminder.", item.title, item.due), Recipients: recipients})
		if err != nil {
			return 0, err
		}
		var addressed bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_message_recipients WHERE message_id=$1)`, messageID).Scan(&addressed); err != nil {
			return 0, err
		}
		if addressed {
			count++
		}
	}
	if err = Audit(ctx, tx, p, 0, "form", formID, "form_reminders_prepared", map[string]any{"outstanding_assignments": len(items), "addressed_assignments": count}); err != nil {
		return 0, err
	}
	return count, tx.Commit(ctx)
}
