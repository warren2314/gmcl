package junior

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
)

type TeamEntry struct {
	Name     string `json:"name"`
	AgeGroup string `json:"age_group"`
}
type TeamEntryChoice struct {
	TeamEntry
	TeamID, CompetitionID, CompetitionRevision int64
	Competition, Effect                        string
}
type TeamEntryPlan struct {
	AssignmentID, SubmissionID, SeasonID int64
	ClubID                               int32
	Items                                []TeamEntryChoice
	Hash                                 string
}

func HasTeamEntries(fields []FormField) bool {
	for _, f := range fields {
		if f.Type == "team_entries" {
			return true
		}
	}
	return false
}

func ParseTeamEntries(raw string, required bool) ([]TeamEntry, error) {
	entries := []TeamEntry{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return nil, fixtureValidation("check the team-entry rows")
		}
	}
	if len(entries) > 10 || required && len(entries) == 0 {
		return nil, fixtureValidation("enter between one and ten teams")
	}
	seen := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		e.Name = strings.Join(strings.Fields(e.Name), " ")
		e.AgeGroup = strings.ToUpper(strings.TrimSpace(e.AgeGroup))
		switch e.AgeGroup {
		case "U9", "U11", "U13", "U15", "U18":
		default:
			return nil, fixtureValidation("choose an age group for each team")
		}
		if e.Name == "" || len(e.Name) > 120 || strings.ContainsRune(e.Name, 0) {
			return nil, fixtureValidation("enter a short team name for every row")
		}
		// Team identity includes age, so an A team at U11 cannot become a U13 A team.
		first := strings.ToUpper(strings.Fields(e.Name)[0])
		for _, age := range []string{"U9", "U11", "U13", "U15", "U18"} {
			if first == age && age != e.AgeGroup {
				return nil, fixtureValidation("team name and age group must agree")
			}
		}
		if first != e.AgeGroup {
			e.Name = e.AgeGroup + " " + e.Name
		}
		if len(e.Name) > 120 {
			return nil, fixtureValidation("team name including age must be at most 120 characters")
		}
		key := strings.ToLower(e.Name)
		if seen[key] {
			return nil, fixtureValidation("the same team appears more than once")
		}
		seen[key] = true
	}
	return entries, nil
}

func AssignmentTeamEntries(a FormAssignment) ([]TeamEntry, error) {
	for _, f := range a.Fields {
		if f.Type == "team_entries" {
			return ParseTeamEntries(a.SubmittedValues[f.Key], true)
		}
	}
	return nil, fixtureValidation("this is not a team-entry form")
}

// The same transaction builds the preview and applies approval. Assignment,
// club and competition locks serialise duplicate approvals and competing plans.
func (s *Service) teamEntryPlan(ctx context.Context, tx pgx.Tx, p Principal, assignmentID, submissionID int64, competitions []int64) (TeamEntryPlan, error) {
	plan := TeamEntryPlan{AssignmentID: assignmentID, SubmissionID: submissionID}
	if p.AdminID < 1 || p.ReadOnly {
		return plan, ErrForbidden
	}
	var status string
	var rawFields, rawValues []byte
	var latest int64
	err := tx.QueryRow(ctx, `SELECT a.club_id,f.season_id,a.status,COALESCE(a.latest_submission_id,0),v.fields,s.values FROM junior_form_assignments a JOIN junior_forms f ON f.id=a.form_id JOIN junior_form_versions v ON v.id=a.version_id JOIN junior_form_submissions s ON s.assignment_id=a.id AND s.id=$2 WHERE a.id=$1 FOR UPDATE OF a`, assignmentID, submissionID).Scan(&plan.ClubID, &plan.SeasonID, &status, &latest, &rawFields, &rawValues)
	if errors.Is(err, pgx.ErrNoRows) {
		return plan, ErrNotFound
	}
	if err != nil {
		return plan, err
	}
	if err = Authorize(ctx, tx, p, plan.ClubID, true); err != nil {
		return plan, err
	}
	if status != "submitted" || latest != submissionID {
		return plan, ErrConflict
	}
	if err = lockFormAssignmentSeasonOpen(ctx, tx, assignmentID); err != nil {
		return plan, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('junior-team-creation:'||$1::integer::text,0))`, plan.ClubID); err != nil {
		return plan, err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_club_seasons cs WHERE cs.club_id=$1 AND cs.season_id=$2 AND cs.state='active' AND NOT EXISTS(SELECT 1 FROM junior_club_status st WHERE st.club_id=cs.club_id AND st.inactive))`, plan.ClubID, plan.SeasonID).Scan(&eligible); err != nil {
		return plan, err
	}
	if !eligible {
		return plan, fixtureValidation("club must have active junior participation")
	}
	var fields []FormField
	var values map[string]string
	if err = json.Unmarshal(rawFields, &fields); err != nil {
		return plan, err
	}
	if err = json.Unmarshal(rawValues, &values); err != nil {
		return plan, err
	}
	entries, err := AssignmentTeamEntries(FormAssignment{Fields: fields, SubmittedValues: values})
	if err != nil {
		return plan, err
	}
	if len(competitions) != len(entries) {
		return plan, fixtureValidation("choose a league for every submitted team")
	}
	ordered := append([]int64(nil), competitions...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	comps := map[int64]Competition{}
	for _, id := range ordered {
		if _, ok := comps[id]; ok {
			continue
		}
		c, e := loadCompetition(ctx, tx, id, true)
		if e != nil {
			return plan, e
		}
		if c.SeasonID != plan.SeasonID {
			return plan, fixtureValidation("league must belong to this form's season")
		}
		comps[id] = c
	}
	for i, entry := range entries {
		c := comps[competitions[i]]
		if c.AgeGroup != entry.AgeGroup {
			return plan, fixtureValidation("league age group must match the submitted team")
		}
		item := TeamEntryChoice{TeamEntry: entry, CompetitionID: c.ID, CompetitionRevision: c.Revision, Competition: c.Name + " · " + c.Division, Effect: "Create team and allocate"}
		rows, e := tx.Query(ctx, `SELECT id,archived FROM junior_teams WHERE club_id=$1 AND lower(regexp_replace(btrim(name),'\s+',' ','g'))=lower($2) ORDER BY id FOR UPDATE`, plan.ClubID, entry.Name)
		if e != nil {
			return plan, e
		}
		count := 0
		archived := false
		for rows.Next() {
			count++
			if e = rows.Scan(&item.TeamID, &archived); e != nil {
				rows.Close()
				return plan, e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return plan, e
		}
		if count > 1 {
			return plan, fixtureValidation("ambiguous existing team names; resolve the club record before approval")
		}
		if archived {
			return plan, fixtureValidation("an existing team is archived; review/reactivate it before approval")
		}
		if item.TeamID > 0 {
			item.Effect = "Reuse existing team and allocate"
			var other bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_competition_entries e JOIN junior_competitions c ON c.id=e.competition_id WHERE e.team_id=$1 AND e.active AND c.season_id=$2 AND c.age_group=$3 AND c.id<>$4)`, item.TeamID, plan.SeasonID, entry.AgeGroup, c.ID).Scan(&other); err != nil {
				return plan, err
			}
			if other {
				return plan, fixtureValidation("team is already allocated to another league at this age; review its existing entry before approval")
			}
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_competition_entries WHERE team_id=$1 AND competition_id=$2 AND active)`, item.TeamID, c.ID).Scan(&exists); err != nil {
				return plan, err
			}
			if exists {
				item.Effect = "Already entered - retain existing team and entry"
			}
		}
		plan.Items = append(plan.Items, item)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	sum := sha256.Sum256(raw)
	plan.Hash = hex.EncodeToString(sum[:])
	return plan, nil
}

func (s *Service) PreviewTeamEntry(ctx context.Context, p Principal, a, submission int64, competitions []int64) (TeamEntryPlan, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return TeamEntryPlan{}, err
	}
	defer tx.Rollback(ctx)
	return s.teamEntryPlan(ctx, tx, p, a, submission, competitions)
}

func (s *Service) ApproveTeamEntry(ctx context.Context, p Principal, a, submission int64, competitions []int64, hash string) error {
	if p.AdminID < 1 || p.ReadOnly {
		return ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var club int32
	var latest int64
	if err = tx.QueryRow(ctx, `SELECT club_id,COALESCE(latest_submission_id,0) FROM junior_form_assignments WHERE id=$1 FOR UPDATE`, a).Scan(&club, &latest); err != nil {
		return err
	}
	if err = Authorize(ctx, tx, p, club, true); err != nil {
		return err
	}
	var prior string
	err = tx.QueryRow(ctx, `SELECT preview_hash FROM junior_team_entry_approvals WHERE assignment_id=$1 AND submission_id=$2`, a, submission).Scan(&prior)
	if err == nil {
		if prior != hash || hash == "" || latest != submission {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	plan, err := s.teamEntryPlan(ctx, tx, p, a, submission, competitions)
	if err != nil {
		return err
	}
	if hash == "" || plan.Hash != hash {
		return ErrConflict
	}
	changed := map[int64]bool{}
	for i := range plan.Items {
		item := &plan.Items[i]
		if item.TeamID == 0 {
			// The permanent club/name index also protects races with manual team creation.
			err = tx.QueryRow(ctx, `INSERT INTO junior_teams(club_id,name) VALUES($1,$2) ON CONFLICT DO NOTHING RETURNING id`, club, item.Name).Scan(&item.TeamID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
		}
		tag, e := tx.Exec(ctx, `INSERT INTO junior_competition_entries(competition_id,team_id,active) VALUES($1,$2,true) ON CONFLICT(competition_id,team_id) DO UPDATE SET active=true WHERE NOT junior_competition_entries.active`, item.CompetitionID, item.TeamID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() > 0 {
			changed[item.CompetitionID] = true
		}
	}
	for id := range changed {
		if _, err = tx.Exec(ctx, `UPDATE junior_competitions SET revision=revision+1 WHERE id=$1`, id); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(plan.Items)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO junior_team_entry_approvals(submission_id,assignment_id,preview_hash,items,approved_by_admin_id) VALUES($1,$2,$3,$4::jsonb,$5)`, submission, a, hash, string(raw), p.AdminID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO junior_form_reviews(assignment_id,submission_id,action,reason,reviewed_by_admin_id) VALUES($1,$2,'complete','Team entries approved and allocated; see approval receipt.',$3)`, a, submission, p.AdminID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_form_assignments SET status='completed' WHERE id=$1`, a); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, club, "form_assignment", a, "team_entries_approved", plan); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) TeamEntryReceipt(ctx context.Context, p Principal, a int64) ([]TeamEntryChoice, error) {
	if _, err := s.FormAssignment(ctx, p, a); err != nil {
		return nil, err
	}
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT items FROM junior_team_entry_approvals WHERE assignment_id=$1 ORDER BY approved_at DESC LIMIT 1`, a).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var items []TeamEntryChoice
	err = json.Unmarshal(raw, &items)
	return items, err
}
