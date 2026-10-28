package juniordemo

import (
	"context"
	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Extend only the fictional database. Do not reset fixtures, approvals or edits
// already made by the presenter. Steps are resumable after a partial startup.
func EnsureGroundDemo(ctx context.Context, pool *db.Pool, state State) error {
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return err
	}
	if database != "gmcl_junior_demo" {
		return fmt.Errorf("grounds examples require the dedicated demo database")
	}
	var done bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_demo_extensions WHERE name='grounds_v2')`).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	if len(state.Clubs) < 4 {
		return fmt.Errorf("four fictional clubs required")
	}
	svc := junior.NewService(pool)
	staff := junior.Principal{AdminID: state.AdminID}
	grounds, err := svc.Grounds(ctx, staff)
	if err != nil {
		return err
	}
	byName := map[string]int64{}
	for _, g := range grounds {
		byName[g.Name] = g.ID
	}
	names := []string{}
	rows, err := pool.Query(ctx, `SELECT DISTINCT name FROM (SELECT ground_name name FROM junior_fixtures UNION SELECT ground_name FROM junior_publication_fixtures UNION SELECT x->>'name' FROM junior_fixture_constraints,jsonb_array_elements(COALESCE(NULLIF(settings->'grounds','null'::jsonb),'[]'::jsonb)) x) n WHERE length(btrim(name))>0 ORDER BY name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// These are explicit aliases of our own fictional examples, not a general
	// name-matching rule for real venues. Unknown demo labels remain separate.
	aliases := map[string]string{"Hillcrest": "Hillcrest Cricket Ground", "HillCrest CC": "Hillcrest Cricket Ground", "Oakfield": "Oakfield Cricket Ground", "Oakfield CC": "Oakfield Cricket Ground", "Riverside": "Riverside Cricket Ground", "Riverside CC": "Riverside Cricket Ground", "Meadowbrook CC": "Meadowbrook Cricket Ground"}
	names = append(names, "Oakfield Cricket Ground", "Riverside Cricket Ground", "Hillcrest Cricket Ground", "Meadowbrook Cricket Ground", "Sunday Demonstration Ground")
	for i, name := range names {
		if aliases[name] != "" {
			continue
		}
		if byName[name] > 0 {
			continue
		}
		g := junior.Ground{Name: name, Address: fmt.Sprintf("%d Fictional Demo Lane, Example Town (not a real address)", i+1), Postcode: fmt.Sprintf("ZZ1 %dAA", (i+1)%10), Source: "FICTIONAL DEMO ONLY. Synthetic postcode and example layouts; verify real club addresses and physical layouts before use.", RulesConfirmed: true, Layouts: []junior.GroundLayout{{"U11": 2}, {"U13": 1}, {"U15": 1}, {"U18": 1}}}
		id, e := svc.SaveGround(ctx, staff, g)
		if e != nil {
			return e
		}
		byName[name] = id
	}
	for alias, canonical := range aliases {
		byName[alias] = byName[canonical]
	}
	// Link original labels explicitly in this synthetic database. Publications
	// retain their immutable original rows; an audited association is appended.
	drafts, err := svc.ListFixtures(ctx, staff, 0, false)
	if err != nil {
		return err
	}
	for _, f := range drafts {
		if f.GroundID == byName[f.GroundName] || f.AgeGroup == "U9" || byName[f.GroundName] == 0 {
			continue
		}
		c, e := svc.GetCompetition(ctx, staff, f.CompetitionID)
		if e != nil {
			return e
		}
		if c.SeasonState == "closed" {
			continue
		}
		f.GroundID = byName[f.GroundName]
		if e = svc.SaveDraftFixture(ctx, staff, c.ID, c.Revision, f); e != nil {
			return e
		}
	}
	published, err := svc.ListFixtures(ctx, staff, 0, true)
	if err != nil {
		return err
	}
	for _, f := range published {
		if f.GroundID != byName[f.GroundName] && byName[f.GroundName] > 0 {
			if err = svc.LinkPublishedGround(ctx, staff, f.PublicationID, f.ID, byName[f.GroundName]); err != nil {
				return err
			}
		}
	}
	competitions, err := svc.ListCompetitions(ctx, staff, state.SeasonID)
	if err != nil {
		return err
	}
	for _, c := range competitions {
		if c.AgeGroup == "U9" || c.SeasonState == "closed" {
			continue
		}
		constraints, e := svc.GetFixtureConstraints(ctx, staff, c.ID)
		if e != nil {
			return e
		}
		changed := false
		complete := true
		for i := range constraints.Grounds {
			g := &constraints.Grounds[i]
			if g.GroundID != byName[g.Name] {
				if byName[g.Name] == 0 {
					complete = false
					break
				}
				g.GroundID = byName[g.Name]
				changed = true
			}
		}
		if changed && complete && constraints.DurationMinutes > 0 {
			fresh, e := svc.GetCompetition(ctx, staff, c.ID)
			if e != nil {
				return e
			}
			if e = svc.SaveFixtureConstraints(ctx, staff, c.ID, fresh.Revision, constraints); e != nil {
				return e
			}
		}
	}
	for _, g := range grounds {
		if canonical := aliases[g.Name]; canonical != "" && !g.Archived {
			g.Archived = true
			g.RulesConfirmed = true
			g.Source = "FICTIONAL DEMO ONLY. Older spelling linked explicitly to " + canonical + "; use the canonical saved venue."
			if _, err = svc.SaveGround(ctx, staff, g); err != nil {
				return err
			}
		}
	}
	neutral := byName["Sunday Demonstration Ground"]
	for _, age := range []string{"U11", "U15"} {
		title := "Venue demonstration — " + age
		var comp int64
		err = pool.QueryRow(ctx, `SELECT id FROM junior_competitions WHERE season_id=$1 AND name=$2 ORDER BY id LIMIT 1`, state.SeasonID, title).Scan(&comp)
		if err == pgx.ErrNoRows {
			comp, err = svc.CreateCompetition(ctx, staff, junior.Competition{SeasonID: state.SeasonID, Name: title, AgeGroup: age, Format: "custom", StartDate: "2027-05-01", EndDate: "2027-06-30", DefaultStartTime: "18:00"})
		}
		if err != nil {
			return err
		}
		teams := []int64{}
		count := 4
		if age == "U15" {
			count = 2
		}
		for _, club := range state.Clubs[:count] {
			var team int64
			name := age + " Venue Demo"
			err = pool.QueryRow(ctx, `SELECT id FROM junior_teams WHERE club_id=$1 AND lower(name)=lower($2) AND NOT archived ORDER BY id LIMIT 1`, club, name).Scan(&team)
			if err == pgx.ErrNoRows {
				team, err = svc.SaveTeam(ctx, staff, junior.TeamInput{ClubID: club, Name: name})
			}
			if err != nil {
				return err
			}
			teams = append(teams, team)
			entries, e := svc.CompetitionEntries(ctx, staff, comp)
			if e != nil {
				return e
			}
			found := false
			for _, entry := range entries {
				found = found || entry.TeamID == team
			}
			if !found {
				c, e := svc.GetCompetition(ctx, staff, comp)
				if e != nil {
					return e
				}
				if e = svc.SaveCompetitionEntry(ctx, staff, comp, c.Revision, team, true); e != nil {
					return e
				}
			}
		}
		c, e := svc.GetCompetition(ctx, staff, comp)
		if e != nil {
			return e
		}
		constraints, e := svc.GetFixtureConstraints(ctx, staff, comp)
		if e != nil {
			return e
		}
		if len(constraints.PlayingDates) == 0 {
			if e = svc.SaveFixtureConstraints(ctx, staff, comp, c.Revision, junior.FixtureConstraints{PlayingDates: []string{"2027-05-29"}, DurationMinutes: 60}); e != nil {
				return e
			}
		}
		existing, e := svc.ListFixtures(ctx, staff, comp, false)
		if e != nil {
			return e
		}
		for i := 0; i < len(teams); i += 2 {
			found := false
			for _, f := range existing {
				found = found || f.HomeTeamID == teams[i] && f.AwayTeamID == teams[i+1]
			}
			if found {
				continue
			}
			c, e = svc.GetCompetition(ctx, staff, comp)
			if e != nil {
				return e
			}
			if e = svc.SaveDraftFixture(ctx, staff, comp, c.Revision, junior.Fixture{HomeTeamID: teams[i], AwayTeamID: teams[i+1], MatchDate: "2027-05-29", StartTime: "18:00", GroundID: neutral}); e != nil {
				return e
			}
		}
	}
	_, err = pool.Exec(ctx, `INSERT INTO junior_demo_extensions(name) VALUES('grounds_v2') ON CONFLICT DO NOTHING`)
	return err
}
