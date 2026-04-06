package leagueapi

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"cricket-ground-feedback/internal/db"
)

// UpsertFixtureRow stores or updates one match from the league API.
func UpsertFixtureRow(ctx context.Context, pool *db.Pool, seasonID *int32, d MatchDetail) error {
	if strings.TrimSpace(d.MatchID) == "" {
		return fmt.Errorf("missing match_id")
	}
	mid, err := strconv.ParseInt(strings.TrimSpace(d.MatchID), 10, 64)
	if err != nil {
		return fmt.Errorf("match_id: %w", err)
	}
	mt, err := ParseMatchDate(d.MatchDate, "")
	if err != nil {
		return fmt.Errorf("match_date: %w", err)
	}
	payload := DetailToJSON(d)
	_, err = pool.Exec(ctx, `
		INSERT INTO league_fixtures (
			play_cricket_match_id, season_id, match_date,
			league_id, competition_id,
			home_team_pc_id, away_team_pc_id,
			home_club_name, away_club_name, home_team_name, away_team_name,
			ground_name, umpire_1_name, umpire_2_name,
			payload, fetched_at, updated_at
		)
		VALUES ($1, $2, $3::date, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb, now(), now())
		ON CONFLICT (play_cricket_match_id)
		DO UPDATE SET
			season_id = COALESCE(EXCLUDED.season_id, league_fixtures.season_id),
			match_date = EXCLUDED.match_date,
			league_id = EXCLUDED.league_id,
			competition_id = EXCLUDED.competition_id,
			home_team_pc_id = EXCLUDED.home_team_pc_id,
			away_team_pc_id = EXCLUDED.away_team_pc_id,
			home_club_name = EXCLUDED.home_club_name,
			away_club_name = EXCLUDED.away_club_name,
			home_team_name = EXCLUDED.home_team_name,
			away_team_name = EXCLUDED.away_team_name,
			ground_name = EXCLUDED.ground_name,
			umpire_1_name = EXCLUDED.umpire_1_name,
			umpire_2_name = EXCLUDED.umpire_2_name,
			payload = EXCLUDED.payload,
			fetched_at = now(),
			updated_at = now()
	`, mid, seasonID, mt.Format("2006-01-02"),
		nullString(d.LeagueID), nullString(d.CompetitionID),
		nullString(d.HomeTeamID), nullString(d.AwayTeamID),
		nullString(d.HomeClubName), nullString(d.AwayClubName), nullString(d.HomeTeamName), nullString(d.AwayTeamName),
		nullString(d.GroundName), nullString(d.Umpire1Name), nullString(d.Umpire2Name),
		payload,
	)
	return err
}

func nullString(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

// UpsertFixtureBatch upserts many details sequentially.
func UpsertFixtureBatch(ctx context.Context, pool *db.Pool, seasonID *int32, details []MatchDetail) error {
	for _, d := range details {
		if err := UpsertFixtureRow(ctx, pool, seasonID, d); err != nil {
			return err
		}
	}
	return nil
}
