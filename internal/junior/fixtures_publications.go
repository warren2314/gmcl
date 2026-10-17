package junior

import "context"

type FixturePublication struct {
	ID, CompetitionID, Revision, FixtureCount                int64
	CompetitionName, AgeGroup, Division, PublishedAt, Reason string
	Current                                                  bool
}

func (s *Service) FixturePublications(ctx context.Context, p Principal, competitionID int64) ([]FixturePublication, error) {
	if err := Authorize(ctx, s.DB, p, p.ClubID, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT pub.id,pub.competition_id,pub.revision,comp.name,comp.age_group,comp.division,pub.published_at::text,pub.acknowledgement_reason,COALESCE(comp.current_publication_id=pub.id,false),
 (SELECT count(*) FROM junior_publication_fixtures f JOIN junior_teams home ON home.id=f.home_team_id JOIN junior_teams away ON away.id=f.away_team_id WHERE f.publication_id=pub.id AND ($2::int=0 OR home.club_id=$2 OR away.club_id=$2))
 FROM junior_fixture_publications pub JOIN junior_competitions comp ON comp.id=pub.competition_id
 WHERE ($1::bigint=0 OR comp.id=$1) AND ($2::int=0 OR EXISTS(SELECT 1 FROM junior_publication_fixtures f JOIN junior_teams home ON home.id=f.home_team_id JOIN junior_teams away ON away.id=f.away_team_id WHERE f.publication_id=pub.id AND (home.club_id=$2 OR away.club_id=$2)))
 ORDER BY pub.published_at DESC,pub.id DESC`, competitionID, p.ClubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FixturePublication{}
	for rows.Next() {
		var item FixturePublication
		if err = rows.Scan(&item.ID, &item.CompetitionID, &item.Revision, &item.CompetitionName, &item.AgeGroup, &item.Division, &item.PublishedAt, &item.Reason, &item.Current, &item.FixtureCount); err != nil {
			return nil, err
		}
		if p.AdminID == 0 || p.ReadOnly {
			item.Reason = ""
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Service) PublicationFixtures(ctx context.Context, p Principal, competitionID, publicationID int64) ([]Fixture, error) {
	if err := Authorize(ctx, s.DB, p, p.ClubID, false); err != nil {
		return nil, err
	}
	if publicationID < 1 {
		return nil, ErrNotFound
	}
	items, err := loadFixtureRows(ctx, s.DB, competitionID, true, p.ClubID, publicationID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return items, nil
}
