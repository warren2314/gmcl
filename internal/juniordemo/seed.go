// Package juniordemo prepares fictional records in the dedicated demo database.
// It is used by cmd/junior-demo, never by the production application.
package juniordemo

import (
	"bytes"
	"context"
	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
	"time"
)

type State struct {
	AdminID            int32   `json:"admin_id"`
	SeasonID           int64   `json:"season_id"`
	Clubs              []int32 `json:"clubs"`
	CompetitionID      int64   `json:"competition_id"`
	DraftCompetitionID int64   `json:"draft_competition_id"`
	FormID             int64   `json:"form_id"`
}

// Seed refuses every database except the separately provisioned demo database.
// Existing demonstrations are retained across restarts; partial seeds are not
// overwritten. No production data, credentials or sender configuration is read.
func Seed(ctx context.Context, pool *db.Pool, password string) (State, error) {
	var out State
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return out, err
	}
	if database != "gmcl_junior_demo" {
		return out, fmt.Errorf("demo seed requires the dedicated gmcl_junior_demo database")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS junior_demo_state(singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), state jsonb NOT NULL)`); err != nil {
		return out, err
	}
	var data []byte
	err := pool.QueryRow(ctx, `SELECT state FROM junior_demo_state WHERE singleton`).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &out)
		if err == nil {
			err = EnsureTeamEntryDemo(ctx, pool, out)
		}
		return out, err
	}
	if err != pgx.ErrNoRows {
		return out, err
	}
	var seasons int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM junior_seasons`).Scan(&seasons); err != nil {
		return out, err
	}
	if seasons != 0 {
		return out, fmt.Errorf("demo seed is incomplete or database is not empty; preserve it and provision a fresh demo volume")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return out, err
	}
	if err = pool.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,email,role) VALUES('joe-demo',$1,'joe@demo.example.invalid','admin') RETURNING id`, hash).Scan(&out.AdminID); err != nil {
		return out, err
	}
	if _, err = pool.Exec(ctx, `INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'junior_manage')`, out.AdminID); err != nil {
		return out, err
	}
	staff := junior.Principal{AdminID: out.AdminID}
	svc := junior.NewService(pool)
	var accessToken string
	svc.AccessMailer = func(_ context.Context, to, subject, body string) error {
		if !strings.HasSuffix(to, "@demo.example.invalid") {
			return fmt.Errorf("demo recipient required")
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "/junior/access?token=") {
				u, e := url.Parse(line)
				if e != nil {
					return e
				}
				accessToken = u.Query().Get("token")
				return nil
			}
		}
		return fmt.Errorf("demo invitation did not contain a sign-in link")
	}
	out.SeasonID, err = svc.SaveSeason(ctx, staff, junior.SeasonInput{Name: "2027 Junior Season · DEMONSTRATION", StartDate: time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2027, 9, 30, 0, 0, 0, 0, time.UTC), State: "open"})
	if err != nil {
		return out, err
	}
	previous, err := svc.SaveSeason(ctx, staff, junior.SeasonInput{Name: "2026 Junior Season · DEMONSTRATION", StartDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), State: "closed"})
	if err != nil {
		return out, err
	}
	names := []string{"Oakfield Juniors CC", "Riverside Juniors CC", "Hillcrest Juniors CC", "Meadowbrook Juniors CC"}
	leads := []string{"Morgan Field", "Taylor Brook", "Alex Hill", "Sam Meadow"}
	teams := map[string][]int64{"U13": {}, "U11": {}}
	grants := []junior.SessionGrant{}
	for i, name := range names {
		club, e := svc.SaveClub(ctx, staff, junior.ClubInput{Name: name, Source: "Fictional Sunday demonstration"})
		if e != nil {
			return out, e
		}
		out.Clubs = append(out.Clubs, club)
		for _, season := range []int64{previous, out.SeasonID} {
			if e = svc.SetParticipation(ctx, staff, club, season, "active", "Fictional demonstration entry"); e != nil {
				return out, e
			}
		}
		for _, age := range []string{"U13", "U11"} {
			team, e := svc.SaveTeam(ctx, staff, junior.TeamInput{ClubID: club, Name: age + " A"})
			if e != nil {
				return out, e
			}
			teams[age] = append(teams[age], team)
		}
		email := fmt.Sprintf("lead%d@demo.example.invalid", i+1)
		if _, e = svc.SaveContact(ctx, staff, junior.ContactInput{ClubID: club, SeasonID: out.SeasonID, FullName: leads[i], Email: email, Role: "junior_lead", StartsOn: time.Now().AddDate(0, -1, 0)}); e != nil {
			return out, e
		}
		if _, e = svc.SaveContact(ctx, staff, junior.ContactInput{ClubID: club, SeasonID: out.SeasonID, FullName: fmt.Sprintf("%s Safeguarding Contact", strings.Fields(name)[0]), Email: fmt.Sprintf("safeguarding%d@demo.example.invalid", i+1), Role: "safeguarding_officer", StartsOn: time.Now().AddDate(0, -1, 0)}); e != nil {
			return out, e
		}
		for _, age := range []string{"U9", "U11", "U13", "U15", "U18"} {
			contact := junior.ContactInput{ClubID: club, SeasonID: out.SeasonID, FullName: fmt.Sprintf("%s %s Coach", strings.Fields(name)[0], age), Email: fmt.Sprintf("%s-%d@demo.example.invalid", strings.ToLower(age), i+1), Role: "coach", AgeGroup: age, StartsOn: time.Now().AddDate(0, -1, 0)}
			if age == "U13" || age == "U11" {
				team := teams[age][i]
				contact.TeamID = &team
			}
			if _, e = svc.SaveContact(ctx, staff, contact); e != nil {
				return out, e
			}
		}
		badge := image.NewRGBA(image.Rect(0, 0, 160, 160))
		colours := []color.RGBA{{160, 25, 55, 255}, {20, 90, 140, 255}, {35, 110, 70, 255}, {115, 60, 150, 255}}
		for y := 0; y < 160; y++ {
			for x := 0; x < 160; x++ {
				c := colours[i]
				if (x-80)*(x-80)+(y-80)*(y-80) < 38*38 {
					c = color.RGBA{245, 235, 215, 255}
				}
				if x > 76 && x < 84 && y > 44 && y < 116 {
					c = colours[i]
				}
				badge.SetRGBA(x, y, c)
			}
		}
		var b bytes.Buffer
		if e = png.Encode(&b, badge); e != nil {
			return out, e
		}
		if _, e = svc.SaveBadge(ctx, staff, club, "fictional-club-badge.png", b.Bytes()); e != nil {
			return out, e
		}
		if e = svc.InviteLead(ctx, staff, club, email); e != nil {
			return out, e
		}
		grant, e := svc.RedeemAccess(ctx, accessToken)
		if e != nil {
			return out, e
		}
		grants = append(grants, grant)
	}
	inactive, err := svc.SaveClub(ctx, staff, junior.ClubInput{Name: "Willow Park Juniors CC", Source: "Fictional historic club"})
	if err != nil {
		return out, err
	}
	if err = svc.SetParticipation(ctx, staff, inactive, previous, "active", "Fictional prior season"); err != nil {
		return out, err
	}
	if err = svc.SetClubInactive(ctx, staff, inactive, true, "Fictional club retaining its history while its junior programme is inactive"); err != nil {
		return out, err
	}
	fields := []junior.FormField{{Key: "confirmed", Label: "Confirm your junior team entries", Type: "yes_no", Required: true}, {Key: "notes", Label: "Notes for the league", Type: "long_text"}}
	out.FormID, err = svc.PublishForm(ctx, staff, junior.FormInput{SeasonID: out.SeasonID, Title: "2027 Junior Club Registration", Description: "DEMONSTRATION: confirm team entries and club contacts.", DueDate: time.Now().AddDate(0, 0, 7), RequiresReview: true, ClubIDs: out.Clubs, Fields: fields})
	if err != nil {
		return out, err
	}
	for i, grant := range grants {
		assignments, e := svc.FormAssignments(ctx, grant.Principal, out.FormID)
		if e != nil || len(assignments) != 1 {
			return out, fmt.Errorf("demo assignments: %v", e)
		}
		if i == 0 {
			continue
		}
		submission, e := svc.SaveFormResponse(ctx, grant.Principal, assignments[0].ID, 0, map[string]string{"confirmed": "yes", "notes": "Fictional demonstration entry confirmed."}, true)
		if e != nil {
			return out, e
		}
		if i == 2 {
			e = svc.ReviewForm(ctx, staff, assignments[0].ID, submission, "complete", "Checked for the Sunday demonstration")
		}
		if i == 3 {
			e = svc.ReviewForm(ctx, staff, assignments[0].ID, submission, "return", "Please confirm which ground the U11 team will use.")
		}
		if e != nil {
			return out, e
		}
	}
	if _, err = svc.PublishForm(ctx, staff, junior.FormInput{SeasonID: out.SeasonID, Title: "Ground Availability Declaration", Description: "DEMONSTRATION: an overdue form example.", DueDate: time.Now().AddDate(0, 0, -2), RequiresReview: true, ClubIDs: out.Clubs[:1], Fields: []junior.FormField{{Key: "notes", Label: "Ground sharing and unavailable dates", Type: "long_text", Required: true}}}); err != nil {
		return out, err
	}
	for _, age := range []string{"U13", "U11"} {
		comp, e := svc.CreateCompetition(ctx, staff, junior.Competition{SeasonID: out.SeasonID, Name: age + " Junior League", AgeGroup: age, Division: "North Division", Format: "once", StartDate: "2027-05-01", EndDate: "2027-06-30", DefaultStartTime: "18:00"})
		if e != nil {
			return out, e
		}
		revision := func() (int64, error) { c, e := svc.GetCompetition(ctx, staff, comp); return c.Revision, e }
		for _, team := range teams[age] {
			rev, e := revision()
			if e != nil {
				return out, e
			}
			if e = svc.SaveCompetitionEntry(ctx, staff, comp, rev, team, true); e != nil {
				return out, e
			}
		}
		constraints := junior.FixtureConstraints{PlayingDates: []string{"2027-05-03", "2027-05-10", "2027-05-17"}, ReserveDates: []string{"2027-05-24"}, DurationMinutes: 120}
		if age == "U11" {
			constraints.PlayingDates = []string{"2027-05-04", "2027-05-11", "2027-05-18"}
			constraints.ReserveDates = []string{"2027-05-25"}
		}
		for i, club := range out.Clubs {
			constraints.Grounds = append(constraints.Grounds, junior.GroundConstraint{ClubID: club, Name: strings.Fields(names[i])[0] + " Cricket Ground", Capacity: 1})
		}
		rev, e := revision()
		if e != nil {
			return out, e
		}
		if e = svc.SaveFixtureConstraints(ctx, staff, comp, rev, constraints); e != nil {
			return out, e
		}
		rev, e = revision()
		if e != nil {
			return out, e
		}
		if e = svc.GenerateFixtures(ctx, staff, comp, rev); e != nil {
			return out, e
		}
		if age == "U11" {
			out.DraftCompetitionID = comp
			continue
		}
		out.CompetitionID = comp
		rev, e = revision()
		if e != nil {
			return out, e
		}
		if _, e = svc.PublishFixtures(ctx, staff, comp, rev, true, "DEMONSTRATION: fictional grounds and dates; no external booking feed connected"); e != nil {
			return out, e
		}
		fixtures, e := svc.ListFixtures(ctx, staff, comp, true)
		if e != nil || len(fixtures) < 3 {
			return out, fmt.Errorf("demo fixtures: %v", e)
		}
		csv := fmt.Sprintf("fixture_id,outcome,evidence\n%d,conceded_home,Fictional home-club concession notice\n%d,possible_concession,Fictional result needing staff clarification\n%d,played,Fictional completed match\n", fixtures[0].ID, fixtures[1].ID, fixtures[2].ID)
		if _, e = svc.ImportFixtureResultsCSV(ctx, staff, comp, "Sunday demonstration results", []byte(csv)); e != nil {
			return out, e
		}
		concessions, e := svc.ListConcessions(ctx, staff, junior.ConcessionFilter{CompetitionID: comp})
		if e != nil {
			return out, e
		}
		for _, c := range concessions {
			if c.Outcome == "conceded_home" {
				if e = svc.ReviewConcession(ctx, staff, c.ID, c.Revision, "confirm", c.HomeTeamID, 0, "Fictional concession confirmed for the demonstration"); e != nil {
					return out, e
				}
			}
		}
	}
	audience := junior.AudienceFilter{SeasonID: out.SeasonID, Role: "junior_lead"}
	preview, err := svc.PreviewAudience(ctx, staff, audience)
	if err != nil {
		return out, err
	}
	if _, err = svc.ComposeMessage(ctx, staff, audience, preview.Hash, "Welcome to the 2027 junior season", "DEMONSTRATION ONLY. This message is queued in the demo and will not be emailed.", "demo:welcome"); err != nil {
		return out, err
	}
	if _, err = svc.RecordIncoming(ctx, staff, out.Clubs[0], "lead1@demo.example.invalid", "Ground sharing question", "DEMONSTRATION: manually recorded example. The shared mailbox is not connected."); err != nil {
		return out, err
	}
	data, err = json.Marshal(out)
	if err != nil {
		return out, err
	}
	_, err = pool.Exec(ctx, `INSERT INTO junior_demo_state(singleton,state) VALUES(true,$1::jsonb)`, string(data))
	if err == nil {
		err = EnsureTeamEntryDemo(ctx, pool, out)
	}
	return out, err
}
