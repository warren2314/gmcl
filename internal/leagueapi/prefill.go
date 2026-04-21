package leagueapi

import (
	"context"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"
)

// FixturePrefill holds data pre-populated from league_fixtures for the captain form.
type FixturePrefill struct {
	MatchDate   string // YYYY-MM-DD
	Opposition  string // opposing club + team name
	Venue       string // ground name
	Umpire1     string
	Umpire2     string
	IsHome      bool
}

// LookupFixturePrefill finds the fixture for this team within the given week date range
// and returns pre-fill data for the captain form. Returns ok=false if no fixture is found.
func LookupFixturePrefill(ctx context.Context, pool *db.Pool, teamID int32, weekStart, weekEnd time.Time) (FixturePrefill, bool) {
	var pcID *string
	_ = pool.QueryRow(ctx, `SELECT play_cricket_team_id FROM teams WHERE id = $1`, teamID).Scan(&pcID)
	if pcID == nil || strings.TrimSpace(*pcID) == "" {
		return FixturePrefill{}, false
	}
	id := strings.TrimSpace(*pcID)

	var (
		matchDate    time.Time
		homeTeamPCID *string
		awayTeamPCID *string
		homeClubName *string
		homeTeamName *string
		awayClubName *string
		awayTeamName *string
		groundName   *string
		umpire1      *string
		umpire2      *string
	)
	err := pool.QueryRow(ctx, `
		SELECT match_date,
		       home_team_pc_id, away_team_pc_id,
		       home_club_name, home_team_name,
		       away_club_name, away_team_name,
		       ground_name, umpire_1_name, umpire_2_name
		FROM league_fixtures
		WHERE match_date BETWEEN $1 AND $2
		  AND (TRIM(home_team_pc_id) = $3 OR TRIM(away_team_pc_id) = $3)
		ORDER BY fetched_at DESC
		LIMIT 1
	`, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"), id).Scan(
		&matchDate,
		&homeTeamPCID, &awayTeamPCID,
		&homeClubName, &homeTeamName,
		&awayClubName, &awayTeamName,
		&groundName, &umpire1, &umpire2,
	)
	if err != nil {
		return FixturePrefill{}, false
	}

	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return strings.TrimSpace(*s)
	}

	isHome := str(homeTeamPCID) == id
	var opposition, venue string
	if isHome {
		opposition = strings.TrimSpace(str(awayClubName) + " " + str(awayTeamName))
		venue = str(groundName)
		if venue == "" {
			venue = str(homeClubName)
		}
	} else {
		opposition = strings.TrimSpace(str(homeClubName) + " " + str(homeTeamName))
		venue = str(groundName)
		if venue == "" {
			venue = str(homeClubName)
		}
	}

	return FixturePrefill{
		MatchDate:  matchDate.Format("2006-01-02"),
		Opposition: opposition,
		Venue:      venue,
		Umpire1:    str(umpire1),
		Umpire2:    str(umpire2),
		IsHome:     isHome,
	}, true
}

// LookupHomeClubID returns the club ID of the home team for a given match.
// It joins league_fixtures → teams → clubs via play_cricket_team_id.
// Returns 0 if the fixture cannot be found or the home team is not mapped locally.
func LookupHomeClubID(ctx context.Context, pool *db.Pool, teamID int32, matchDate time.Time) int32 {
	var pcID *string
	_ = pool.QueryRow(ctx, `SELECT play_cricket_team_id FROM teams WHERE id = $1`, teamID).Scan(&pcID)
	if pcID == nil || strings.TrimSpace(*pcID) == "" {
		return 0
	}
	id := strings.TrimSpace(*pcID)

	var homeClubID *int32
	_ = pool.QueryRow(ctx, `
		SELECT cl.id
		FROM league_fixtures lf
		JOIN teams ht ON TRIM(ht.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
		JOIN clubs cl ON cl.id = ht.club_id
		WHERE lf.match_date = $1::date
		  AND (TRIM(lf.home_team_pc_id) = $2 OR TRIM(lf.away_team_pc_id) = $2)
		  AND TRIM(COALESCE(lf.home_team_pc_id, '')) <> ''
		ORDER BY lf.fetched_at DESC
		LIMIT 1
	`, matchDate.Format("2006-01-02"), id).Scan(&homeClubID)

	if homeClubID == nil {
		return 0
	}
	return *homeClubID
}

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
