package juniordemo

import (
	"context"
	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/url"
	"strings"
	"time"
)

// Extend the fictional demonstration once without resetting Joe's existing work.
func EnsureTeamEntryDemo(ctx context.Context, pool *db.Pool, state State) error {
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return err
	}
	if database != "gmcl_junior_demo" {
		return fmt.Errorf("team-entry examples require the dedicated demo database")
	}
	if len(state.Clubs) < 2 {
		return fmt.Errorf("demo clubs are incomplete")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS junior_demo_extensions(name text PRIMARY KEY)`); err != nil {
		return err
	}
	var done bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_demo_extensions WHERE name='team_entries_v1')`).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	svc := junior.NewService(pool)
	staff := junior.Principal{AdminID: state.AdminID}
	var competition int64
	err := pool.QueryRow(ctx, `SELECT id FROM junior_competitions WHERE season_id=$1 AND name='U15 Junior League' AND division='North Division'`, state.SeasonID).Scan(&competition)
	if err == pgx.ErrNoRows {
		competition, err = svc.CreateCompetition(ctx, staff, junior.Competition{SeasonID: state.SeasonID, Name: "U15 Junior League", AgeGroup: "U15", Division: "North Division", Format: "once", StartDate: "2027-05-01", EndDate: "2027-06-30", DefaultStartTime: "18:00"})
	}
	if err != nil {
		return err
	}
	var form int64
	err = pool.QueryRow(ctx, `SELECT id FROM junior_forms WHERE season_id=$1 AND title='2027 Team Entry Form'`, state.SeasonID).Scan(&form)
	if err == pgx.ErrNoRows {
		form, err = svc.PublishForm(ctx, staff, junior.FormInput{SeasonID: state.SeasonID, Title: "2027 Team Entry Form", Description: "DEMONSTRATION: submit team names and ages. League staff approve the entry and choose the leagues; fixtures remain separate.", DueDate: time.Now().AddDate(0, 0, 7), RequiresReview: true, ClubIDs: state.Clubs[:2], Fields: []junior.FormField{{Key: "entries", Label: "Teams entering this season", Type: "team_entries", Required: true}}})
	}
	if err != nil {
		return err
	}
	assignments, err := svc.FormAssignments(ctx, staff, form)
	if err != nil {
		return err
	}
	for _, a := range assignments {
		if a.ClubID != state.Clubs[1] || a.SubmissionID > 0 {
			continue
		}
		token := ""
		svc.AccessMailer = func(_ context.Context, to, subject, body string) error {
			if to != "lead2@demo.example.invalid" {
				return fmt.Errorf("fictional recipient required")
			}
			for _, line := range strings.Split(body, "\n") {
				if strings.Contains(line, "/junior/access?token=") {
					u, e := url.Parse(line)
					if e != nil {
						return e
					}
					token = u.Query().Get("token")
					return nil
				}
			}
			return fmt.Errorf("simulated link missing")
		}
		if err = svc.RequestLogin(ctx, "lead2@demo.example.invalid"); err != nil {
			return err
		}
		// A presenter may have just requested a link. Do not make a normal
		// one-minute throttle prevent the demo starting; retain the new form.
		if token == "" {
			return nil
		}
		grant, e := svc.RedeemAccess(ctx, token)
		if e != nil {
			return e
		}
		_, err = svc.SaveFormResponse(ctx, grant.Principal, a.ID, a.DraftRevision, map[string]string{"entries": `[{"name":"U13 A","age_group":"U13"},{"name":"U15 B","age_group":"U15"}]`}, true)
		if err != nil {
			return err
		}
	}
	_, err = pool.Exec(ctx, `INSERT INTO junior_demo_extensions(name) VALUES('team_entries_v1') ON CONFLICT DO NOTHING`)
	return err
}
