package seed

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/db"
)

// RunIfEnabled inserts the bootstrap admin user when SEED=1 and no admin users exist.
// Username and email are taken from SEED_ADMIN_EMAIL (default "webmaster@gmcl.co.uk").
// Password MUST be set via SEED_ADMIN_PASSWORD — no insecure default is provided.
// force_password_change is set to TRUE so the first login requires an immediate password reset.
func RunIfEnabled(ctx context.Context, pool *db.Pool) error {
	if os.Getenv("SEED") != "1" {
		return nil
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	password := os.Getenv("SEED_ADMIN_PASSWORD")
	if password == "" {
		// Refuse to create an account with a hardcoded insecure password.
		// Set SEED_ADMIN_PASSWORD in your environment to bootstrap the first admin.
		return nil
	}

	email := os.Getenv("SEED_ADMIN_EMAIL")
	if email == "" {
		email = "webmaster@gmcl.co.uk"
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO admin_users (username, password_hash, email, is_active, force_password_change)
		VALUES ($1, $2, $1, true, true)
	`, email, hash)
	if err != nil {
		return err
	}
	return nil
}

// RunSeedData runs 0004_seed.sql-style data: season, week, club, team, captain.
// Only runs if no seasons exist. Week is set so CURRENT_DATE is between start_date and end_date.
func RunSeedData(ctx context.Context, pool *db.Pool) error {
	if os.Getenv("SEED") != "1" {
		return nil
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM seasons`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	// One season, one week covering CURRENT_DATE, one club, one team, one captain.
	_, err := pool.Exec(ctx, `
		INSERT INTO seasons (name, start_date, end_date)
		VALUES ('Test Season', (CURRENT_DATE - interval '1 month')::date, (CURRENT_DATE + interval '1 year')::date)
	`)
	if err != nil {
		return err
	}

	var seasonID int32
	err = pool.QueryRow(ctx, `SELECT id FROM seasons WHERE name = 'Test Season' LIMIT 1`).Scan(&seasonID)
	if err != nil {
		return err
	}

	// Week that contains today so "current week" lookup succeeds
	_, err = pool.Exec(ctx, `
		INSERT INTO weeks (season_id, week_number, start_date, end_date)
		VALUES ($1, 1, CURRENT_DATE - 3, CURRENT_DATE + 5)
	`, seasonID)
	if err != nil {
		return err
	}

	var weekID int32
	err = pool.QueryRow(ctx, `SELECT id FROM weeks WHERE season_id = $1 AND week_number = 1`, seasonID).Scan(&weekID)
	if err != nil {
		return err
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO clubs (name, short_name) VALUES ('Test Club', 'TC') ON CONFLICT (name) DO NOTHING
	`)
	if err != nil {
		return err
	}

	var clubID int32
	err = pool.QueryRow(ctx, `SELECT id FROM clubs WHERE name = 'Test Club'`).Scan(&clubID)
	if err != nil {
		return err
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO teams (club_id, name, active) VALUES ($1, 'First XI', true) ON CONFLICT (club_id, name) DO NOTHING
	`, clubID)
	if err != nil {
		return err
	}

	var teamID int32
	err = pool.QueryRow(ctx, `SELECT id FROM teams WHERE club_id = $1 AND name = 'First XI'`, clubID).Scan(&teamID)
	if err != nil {
		return err
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO captains (team_id, full_name, email, active_from)
		VALUES ($1, 'Test Captain', 'captain@test.local', '2000-01-01')
		ON CONFLICT (team_id, email, active_from) DO NOTHING
	`, teamID)
	if err != nil {
		return err
	}

	// Add test submissions and extra clubs/teams so admin has data to view
	RunSeedTestSubmissions(ctx, pool, seasonID, weekID)
	// Seed league fixtures for today so umpire prefill works immediately
	RunSeedLeagueFixtures(ctx, pool, seasonID)
	return nil
}

// gmclClub describes a GMCL club and its teams for seed data.
type gmclClub struct {
	Name  string
	Short string
	Teams []gmclTeam
}

type gmclTeam struct {
	Name         string
	PCTeamID     string // Play-Cricket team ID (fake, for fixture-sync testing)
	CaptainName  string
	CaptainEmail string
}

// GMCLClubs contains representative clubs from the Greater Manchester Cricket League.
// Play-Cricket team IDs (PCTeamID) are fictional stand-ins used until the real API
// team list is available; they are used to test fixture-sync and umpire prefill locally.
var GMCLClubs = []gmclClub{
	{
		Name:  "Heaton Mersey CC",
		Short: "HM",
		Teams: []gmclTeam{
			{Name: "First XI",  PCTeamID: "10011", CaptainName: "James Holden",   CaptainEmail: "hm1@gmcl.test"},
			{Name: "Second XI", PCTeamID: "10012", CaptainName: "Paul Whitworth", CaptainEmail: "hm2@gmcl.test"},
		},
	},
	{
		Name:  "Denton St Lawrence CC",
		Short: "DSL",
		Teams: []gmclTeam{
			{Name: "First XI",  PCTeamID: "10021", CaptainName: "Amir Rashid",   CaptainEmail: "dsl1@gmcl.test"},
			{Name: "Second XI", PCTeamID: "10022", CaptainName: "Connor Finney", CaptainEmail: "dsl2@gmcl.test"},
		},
	},
	{
		Name:  "Woodhouses CC",
		Short: "WH",
		Teams: []gmclTeam{
			{Name: "First XI",  PCTeamID: "10031", CaptainName: "Thomas Briggs", CaptainEmail: "wh1@gmcl.test"},
			{Name: "Second XI", PCTeamID: "10032", CaptainName: "Declan Walsh",  CaptainEmail: "wh2@gmcl.test"},
		},
	},
	{
		Name:  "Prestwich CC",
		Short: "PRE",
		Teams: []gmclTeam{
			{Name: "First XI",  PCTeamID: "10041", CaptainName: "Ravi Patel",   CaptainEmail: "pre1@gmcl.test"},
			{Name: "Second XI", PCTeamID: "10042", CaptainName: "Simon Clarke", CaptainEmail: "pre2@gmcl.test"},
		},
	},
	{
		Name:  "Swinton Moorside CC",
		Short: "SM",
		Teams: []gmclTeam{
			{Name: "First XI", PCTeamID: "10051", CaptainName: "Mark Thornton", CaptainEmail: "sm1@gmcl.test"},
		},
	},
	{
		Name:  "Royton CC",
		Short: "ROY",
		Teams: []gmclTeam{
			{Name: "First XI",  PCTeamID: "10061", CaptainName: "Nathan Webb", CaptainEmail: "roy1@gmcl.test"},
			{Name: "Second XI", PCTeamID: "10062", CaptainName: "Craig Doyle", CaptainEmail: "roy2@gmcl.test"},
		},
	},
	{
		Name:  "Norden CC",
		Short: "NOR",
		Teams: []gmclTeam{
			{Name: "First XI", PCTeamID: "10071", CaptainName: "Oliver Ramshaw", CaptainEmail: "nor1@gmcl.test"},
		},
	},
	{
		Name:  "Hyde CC",
		Short: "HYD",
		Teams: []gmclTeam{
			{Name: "First XI",  PCTeamID: "10081", CaptainName: "Darren Lees", CaptainEmail: "hyd1@gmcl.test"},
			{Name: "Second XI", PCTeamID: "10082", CaptainName: "Bhav Sharma", CaptainEmail: "hyd2@gmcl.test"},
		},
	},
}

// RunSeedTestSubmissions adds GMCL clubs/teams/captains and sample submissions when SEED=1.
func RunSeedTestSubmissions(ctx context.Context, pool *db.Pool, seasonID, weekID int32) {
	if os.Getenv("SEED") != "1" {
		return
	}

	// Already have submissions? Skip to avoid duplicate data on restart.
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM submissions WHERE week_id = $1`, weekID).Scan(&n); err != nil || n > 0 {
		return
	}

	// Insert GMCL clubs and their teams/captains with Play-Cricket team IDs.
	type seededEntry struct {
		teamID    int32
		captainID int32
	}
	var seeded []seededEntry

	for _, c := range GMCLClubs {
		pool.Exec(ctx, `INSERT INTO clubs (name, short_name) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`, c.Name, c.Short)

		var clubID int32
		if err := pool.QueryRow(ctx, `SELECT id FROM clubs WHERE name = $1`, c.Name).Scan(&clubID); err != nil || clubID == 0 {
			continue
		}

		for _, t := range c.Teams {
			pool.Exec(ctx, `
				INSERT INTO teams (club_id, name, active, play_cricket_team_id)
				VALUES ($1, $2, true, $3)
				ON CONFLICT (club_id, name) DO UPDATE SET play_cricket_team_id = EXCLUDED.play_cricket_team_id
			`, clubID, t.Name, t.PCTeamID)

			var teamID int32
			if err := pool.QueryRow(ctx, `SELECT id FROM teams WHERE club_id = $1 AND name = $2`, clubID, t.Name).Scan(&teamID); err != nil || teamID == 0 {
				continue
			}

			pool.Exec(ctx, `
				INSERT INTO captains (team_id, full_name, email, active_from)
				VALUES ($1, $2, $3, '2000-01-01')
				ON CONFLICT (team_id, email, active_from) DO NOTHING
			`, teamID, t.CaptainName, t.CaptainEmail)

			var captainID int32
			if err := pool.QueryRow(ctx, `SELECT id FROM captains WHERE team_id = $1 AND email = $2`, teamID, t.CaptainEmail).Scan(&captainID); err != nil || captainID == 0 {
				continue
			}

			seeded = append(seeded, seededEntry{teamID: teamID, captainID: captainID})
		}
	}

	// Also include Test Club captain/team so it appears in submission history.
	var testCaptainID, testTeamID int32
	_ = pool.QueryRow(ctx, `
		SELECT c.id, c.team_id FROM captains c
		JOIN teams t ON c.team_id = t.id
		JOIN clubs cl ON t.club_id = cl.id
		WHERE cl.name = 'Test Club' AND t.name = 'First XI' LIMIT 1
	`).Scan(&testCaptainID, &testTeamID)
	if testCaptainID != 0 && testTeamID != 0 {
		seeded = append([]seededEntry{{teamID: testTeamID, captainID: testCaptainID}}, seeded...)
	}

	if len(seeded) == 0 {
		return
	}

	// Sample submissions with GMCL form_data.
	type formTemplate struct {
		data map[string]any
		// pre-computed ratings so we don't rely on float64 map assertions at runtime
		pitch, outfield, facilities int
		comments                    string
	}
	templates := []formTemplate{
		{
			data: map[string]any{
				"match_date": "2025-06-14", "umpire1_name": "R. Patel", "umpire2_name": "S. Khan",
				"your_team": "1st XI", "umpire1_performance": "Good", "umpire2_performance": "Good",
				"umpire_comments": "Both umpires were excellent.", "unevenness_of_bounce": 1, "seam_movement": 2, "carry_bounce": 1, "turn": 2,
			},
			pitch: 5, outfield: 5, facilities: 5,
			comments: "Pitch played well. Good pace and carry throughout.",
		},
		{
			data: map[string]any{
				"match_date": "2025-06-14", "umpire1_name": "A. Jones", "umpire2_name": "B. Brown",
				"your_team": "2nd XI", "umpire1_performance": "Average", "umpire2_performance": "Good",
				"umpire_comments": "No significant issues.", "unevenness_of_bounce": 2, "seam_movement": 2, "carry_bounce": 2, "turn": 3,
			},
			pitch: 4, outfield: 4, facilities: 4,
			comments: "Decent surface. Some variable bounce late on.",
		},
		{
			data: map[string]any{
				"match_date": "2025-06-15", "umpire1_name": "T. Briggs", "umpire2_name": "D. Walsh",
				"your_team": "1st XI", "umpire1_performance": "Average", "umpire2_performance": "Good",
				"umpire_comments": "Panel umpire very professional.", "unevenness_of_bounce": 3, "seam_movement": 3, "carry_bounce": 2, "turn": 2,
			},
			pitch: 3, outfield: 4, facilities: 4,
			comments: "Surface played slowly. Outfield in good condition.",
		},
		{
			data: map[string]any{
				"match_date": "2025-06-21", "umpire1_name": "M. Thornton", "umpire2_name": "J. Clarke",
				"your_team": "1st XI", "umpire1_performance": "Good", "umpire2_performance": "Good",
				"umpire_comments": "Both very competent.", "unevenness_of_bounce": 1, "seam_movement": 1, "carry_bounce": 1, "turn": 1,
			},
			pitch: 5, outfield: 5, facilities: 5,
			comments: "Excellent pitch. Consistent throughout the day.",
		},
		{
			data: map[string]any{
				"match_date": "2025-06-21", "umpire1_name": "N. Webb", "umpire2_name": "O. Ramshaw",
				"your_team": "1st XI", "umpire1_performance": "Poor", "umpire2_performance": "Average",
				"umpire_comments": "One poor lbw decision.", "unevenness_of_bounce": 4, "seam_movement": 3, "carry_bounce": 3, "turn": 4,
			},
			pitch: 2, outfield: 3, facilities: 3,
			comments: "Difficult surface. Uneven bounce caused problems.",
		},
	}

	for i := 0; i < len(seeded); i++ {
		entry := seeded[i]
		tpl := templates[i%len(templates)]
		fdJSON, _ := json.Marshal(tpl.data)
		matchDate := time.Now().AddDate(0, 0, -(i % 5)).Format("2006-01-02")
		pool.Exec(ctx, `
			INSERT INTO submissions (season_id, week_id, team_id, captain_id, match_date,
			                        pitch_rating, outfield_rating, facilities_rating, comments, form_data)
			VALUES ($1, $2, $3, $4, $5::date, $6, $7, $8, $9, $10)
		`, seasonID, weekID, entry.teamID, entry.captainID, matchDate,
			tpl.pitch, tpl.outfield, tpl.facilities, tpl.comments, fdJSON)
	}
}

// gmclFixtures pairs home/away PC team IDs with umpire names for seed league_fixtures rows.
// These use today's date so umpire prefill works on a fresh local stack without needing
// to call the fixture-sync endpoint manually.
var gmclFixtures = []struct {
	matchID               int64
	homeTeamID, awayTeamID string
	homeClubName, awayClubName string
	homeTeamName, awayTeamName string
	groundName            string
	umpire1, umpire2      string
}{
	{90001, "10011", "10021", "Heaton Mersey CC", "Denton St Lawrence CC",
		"Heaton Mersey CC - 1st XI", "Denton St Lawrence CC - 1st XI",
		"Heaton Mersey CC Ground", "R. Patel", "S. Khan"},
	{90002, "10031", "10041", "Woodhouses CC", "Prestwich CC",
		"Woodhouses CC - 1st XI", "Prestwich CC - 1st XI",
		"Woodhouses CC Ground", "T. Briggs", "D. Walsh"},
	{90003, "10061", "10071", "Royton CC", "Norden CC",
		"Royton CC - 1st XI", "Norden CC - 1st XI",
		"Royton CC Ground", "M. Thornton", "J. Clarke"},
	{90004, "10051", "10081", "Swinton Moorside CC", "Hyde CC",
		"Swinton Moorside CC - 1st XI", "Hyde CC - 1st XI",
		"Swinton Moorside CC Ground", "N. Webb", "O. Ramshaw"},
	// 2nd XI Sunday fixtures
	{90101, "10012", "10042", "Heaton Mersey CC", "Prestwich CC",
		"Heaton Mersey CC - 2nd XI", "Prestwich CC - 2nd XI",
		"Heaton Mersey CC Ground", "N. Webb", "O. Ramshaw"},
	{90102, "10082", "10062", "Hyde CC", "Royton CC",
		"Hyde CC - 2nd XI", "Royton CC - 2nd XI",
		"Hyde CC Ground", "B. Sharma", "C. Doyle"},
	{90103, "10022", "10032", "Denton St Lawrence CC", "Woodhouses CC",
		"Denton St Lawrence CC - 2nd XI", "Woodhouses CC - 2nd XI",
		"Denton St Lawrence CC Ground", "", ""},
}

// RunSeedLeagueFixtures inserts league_fixtures rows for today's date so umpire prefill
// works on a fresh local stack without needing to call the sync endpoint first.
// Rows are upserted idempotently; safe to call on every restart.
func RunSeedLeagueFixtures(ctx context.Context, pool *db.Pool, seasonID int32) {
	if os.Getenv("SEED") != "1" {
		return
	}
	today := time.Now().Format("2006-01-02")
	for _, f := range gmclFixtures {
		pool.Exec(ctx, `
			INSERT INTO league_fixtures (
				play_cricket_match_id, season_id, match_date,
				league_id, competition_id,
				home_team_pc_id, away_team_pc_id,
				home_club_name, away_club_name,
				home_team_name, away_team_name,
				ground_name, umpire_1_name, umpire_2_name,
				payload
			) VALUES ($1, $2, $3::date, '5501', '88101',
				$4, $5, $6, $7, $8, $9, $10, $11, $12, '{}')
			ON CONFLICT (play_cricket_match_id) DO UPDATE SET
				match_date     = EXCLUDED.match_date,
				umpire_1_name  = EXCLUDED.umpire_1_name,
				umpire_2_name  = EXCLUDED.umpire_2_name,
				updated_at     = now()
		`, f.matchID, seasonID, today,
			f.homeTeamID, f.awayTeamID,
			f.homeClubName, f.awayClubName,
			f.homeTeamName, f.awayTeamName,
			f.groundName, f.umpire1, f.umpire2)
	}
}
