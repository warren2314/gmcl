package junior

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type Dashboard struct {
	ActiveClubs, MissingLead, MissingSafeguarding, OutstandingForms, OverdueForms, ReviewForms int64
	UnpublishedCompetitions, ConcessionReview, ConfirmedConcessions, MessageProblems           int64
}

func (s *Service) Dashboard(ctx context.Context, p Principal, seasonID int64) (Dashboard, error) {
	var d Dashboard
	if p.AdminID <= 0 || p.ReadOnly {
		return d, ErrForbidden
	}
	if err := Authorize(ctx, s.DB, p, 0, false); err != nil {
		return d, err
	}
	if seasonID == 0 {
		return d, nil
	}
	err := s.DB.QueryRow(ctx, `WITH participating AS (SELECT club_id FROM junior_club_seasons WHERE season_id=$1 AND state='active'),
 roles AS (SELECT DISTINCT r.club_id,r.role FROM junior_contact_roles r JOIN junior_contacts c ON c.id=r.contact_id
 WHERE r.season_id=$1 AND NOT c.archived AND r.starts_on<=(now() AT TIME ZONE 'Europe/London')::date AND (r.ends_on IS NULL OR r.ends_on>=(now() AT TIME ZONE 'Europe/London')::date)),
 forms AS (SELECT a.status,v.due_date FROM junior_form_assignments a JOIN junior_form_versions v ON v.id=a.version_id
 JOIN junior_forms f ON f.id=a.form_id WHERE f.season_id=$1)
 SELECT (SELECT count(*) FROM participating),
 (SELECT count(*) FROM participating p WHERE NOT EXISTS(SELECT 1 FROM roles r WHERE r.club_id=p.club_id AND r.role='junior_lead')),
 (SELECT count(*) FROM participating p WHERE NOT EXISTS(SELECT 1 FROM roles r WHERE r.club_id=p.club_id AND r.role='safeguarding_officer')),
 (SELECT count(*) FROM forms WHERE status IN('assigned','returned')),
 (SELECT count(*) FROM forms WHERE status IN('assigned','returned') AND due_date<(now() AT TIME ZONE 'Europe/London')::date),
 (SELECT count(*) FROM forms WHERE status='submitted'),
 (SELECT count(*) FROM junior_competitions c WHERE c.season_id=$1 AND (c.current_publication_id IS NULL OR
 EXISTS(SELECT 1 FROM junior_fixture_publications p WHERE p.id=c.current_publication_id AND p.revision<>c.revision))),
 (SELECT count(*) FROM junior_concession_candidates k JOIN junior_fixtures f ON f.id=k.fixture_id
 JOIN junior_competitions c ON c.id=f.competition_id WHERE c.season_id=$1 AND k.state IN('awaiting_review','information_requested','source_changed')),
 (SELECT count(*) FROM junior_concession_candidates k JOIN junior_fixtures f ON f.id=k.fixture_id
 JOIN junior_competitions c ON c.id=f.competition_id WHERE c.season_id=$1 AND
 (SELECT action FROM junior_concession_decisions d WHERE d.candidate_id=k.id AND d.action IN('confirm','dismiss') ORDER BY d.id DESC LIMIT 1)='confirm'),
 (SELECT count(*) FROM junior_messages m WHERE m.season_id=$1 AND
 (NOT EXISTS(SELECT 1 FROM junior_message_recipients r WHERE r.message_id=m.id) OR
 EXISTS(SELECT 1 FROM junior_message_recipients r WHERE r.message_id=m.id AND r.status='failed')))`, seasonID).Scan(
		&d.ActiveClubs, &d.MissingLead, &d.MissingSafeguarding, &d.OutstandingForms, &d.OverdueForms, &d.ReviewForms,
		&d.UnpublishedCompetitions, &d.ConcessionReview, &d.ConfirmedConcessions, &d.MessageProblems)
	return d, err
}

type SearchResult struct{ Kind, Label, Detail, URL string }

func (s *Service) Search(ctx context.Context, p Principal, seasonID int64, query string) ([]SearchResult, error) {
	if p.AdminID <= 0 || p.ReadOnly {
		return nil, ErrForbidden
	}
	if err := Authorize(ctx, s.DB, p, 0, false); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return []SearchResult{}, nil
	}
	if len(query) > 100 {
		return nil, ErrValidation
	}
	rows, err := s.DB.Query(ctx, `SELECT kind,id,club_id,label,detail FROM (
 SELECT 'Club'::text kind,c.id::bigint id,c.id club_id,c.name label,'Club record'::text detail FROM clubs c
 WHERE EXISTS(SELECT 1 FROM junior_club_seasons p WHERE p.club_id=c.id AND ($1::bigint=0 OR p.season_id=$1))
 UNION ALL SELECT 'Contact',c.id,c.club_id,c.full_name,concat_ws(' · ',cl.name,c.email) FROM junior_contacts c JOIN clubs cl ON cl.id=c.club_id WHERE NOT c.archived
 AND EXISTS(SELECT 1 FROM junior_contact_roles r WHERE r.contact_id=c.id AND ($1::bigint=0 OR r.season_id=$1))
 UNION ALL SELECT 'Team',t.id,t.club_id,t.name,c.name FROM junior_teams t JOIN clubs c ON c.id=t.club_id WHERE NOT t.archived
 AND EXISTS(SELECT 1 FROM junior_club_seasons p WHERE p.club_id=t.club_id AND ($1::bigint=0 OR p.season_id=$1))
 UNION ALL SELECT 'Competition',id,0,name,concat_ws(' · ',age_group,division) FROM junior_competitions WHERE ($1::bigint=0 OR season_id=$1)
 UNION ALL SELECT 'Fixture',f.competition_id,0,concat_ws(' v ',h.name,a.name),concat_ws(' · ',c.name,c.division,f.match_date::text,f.ground_name)
 FROM junior_fixtures f JOIN junior_competitions c ON c.id=f.competition_id JOIN junior_teams h ON h.id=f.home_team_id JOIN junior_teams a ON a.id=f.away_team_id
 WHERE NOT f.retired AND ($1::bigint=0 OR c.season_id=$1)
 UNION ALL SELECT 'Form',id,0,title,'Official form' FROM junior_forms WHERE ($1::bigint=0 OR season_id=$1)
 )q WHERE concat_ws(' ',label,detail) ILIKE '%'||$2||'%' ORDER BY kind,label,id LIMIT 50`, seasonID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchResult{}
	for rows.Next() {
		var item SearchResult
		var id int64
		var clubID int32
		if err = rows.Scan(&item.Kind, &id, &clubID, &item.Label, &item.Detail); err != nil {
			return nil, err
		}
		switch item.Kind {
		case "Club", "Contact", "Team":
			item.URL = fmt.Sprintf("/admin/juniors/clubs/%d", clubID)
		case "Competition":
			item.URL = fmt.Sprintf("/admin/juniors/competitions/%d", id)
		case "Fixture":
			item.URL = fmt.Sprintf("/admin/juniors/fixtures?competition_id=%d&view=draft", id)
		case "Form":
			item.URL = fmt.Sprintf("/admin/juniors/forms/%d", id)
		}
		if seasonID > 0 {
			sep := "?"
			if strings.Contains(item.URL, "?") {
				sep = "&"
			}
			item.URL += sep + url.Values{"season_id": {fmt.Sprint(seasonID)}}.Encode()
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
