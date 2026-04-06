package leagueapi

import (
	"context"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"
)

// LookupUmpirePrefill returns umpire names from cached league_fixtures for this team and date.
// team_id must have teams.play_cricket_team_id set to match home_team_pc_id or away_team_pc_id.
func LookupUmpirePrefill(ctx context.Context, pool *db.Pool, teamID int32, matchDate time.Time) (umpire1, umpire2 string, ok bool) {
	var pcID *string
	_ = pool.QueryRow(ctx, `SELECT play_cricket_team_id FROM teams WHERE id = $1`, teamID).Scan(&pcID)
	if pcID == nil || strings.TrimSpace(*pcID) == "" {
		return "", "", false
	}
	id := strings.TrimSpace(*pcID)
	var u1, u2 *string
	err := pool.QueryRow(ctx, `
		SELECT umpire_1_name, umpire_2_name
		FROM league_fixtures
		WHERE match_date = $1::date
		  AND (TRIM(home_team_pc_id) = $2 OR TRIM(away_team_pc_id) = $2)
		ORDER BY fetched_at DESC
		LIMIT 1
	`, matchDate.Format("2006-01-02"), id).Scan(&u1, &u2)
	if err != nil {
		return "", "", false
	}
	if u1 != nil {
		umpire1 = strings.TrimSpace(*u1)
	}
	if u2 != nil {
		umpire2 = strings.TrimSpace(*u2)
	}
	if umpire1 == "" && umpire2 == "" {
		return "", "", false
	}
	return umpire1, umpire2, true
}
