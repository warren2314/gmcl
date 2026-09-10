package sanctions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AddCaseTeam records a further affected team without changing the original
// report mapping, primary team, decision or correspondence.
func (s *Service) AddCaseTeam(ctx context.Context, caseID int64, teamID int32, reason string, actor Actor) error {
	reason = strings.TrimSpace(reason)
	if actor.ID == nil || caseID < 1 || teamID < 1 || reason == "" || len(reason) > 2000 {
		return errors.New("select a team and enter an audit reason (up to 2000 characters)")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var clubID, primaryTeam, owner *int32
	if err = tx.QueryRow(ctx, `SELECT status,club_id,team_id,assigned_admin_id FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&status, &clubID, &primaryTeam, &owner); err != nil {
		return err
	}
	var permitted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users WHERE id=$1 AND is_active AND (role='super_admin' OR id=$2))`, *actor.ID, owner).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return errors.New("only the case owner or a super administrator can add a team")
	}
	if status != "submitted" && status != "triage" && status != "investigating" {
		return errors.New("reopen and amend the decision before adding a team; submitted or completed decisions cannot be changed here")
	}
	var hasDecision bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_decision_revisions d WHERE d.case_id=$1 AND d.status IN ('proposed','approved') AND NOT EXISTS(SELECT 1 FROM sanction_decision_revisions newer WHERE newer.supersedes_id=d.id))`, caseID).Scan(&hasDecision); err != nil {
		return err
	}
	if hasDecision {
		return errors.New("reopen and amend the saved decision before adding a team")
	}
	if clubID == nil {
		return errors.New("map the offending club before adding another team")
	}
	var teamName, clubName string
	if err = tx.QueryRow(ctx, `SELECT t.name,c.name FROM teams t JOIN clubs c ON c.id=t.club_id WHERE t.id=$1 AND t.club_id=$2 AND t.active FOR SHARE OF t`, teamID, *clubID).Scan(&teamName, &clubName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("select an active team belonging to the case's offending club")
		}
		return err
	}
	if primaryTeam != nil && *primaryTeam == teamID {
		return errors.New("this is already the primary offending team")
	}
	var subjectID int64
	err = tx.QueryRow(ctx, `INSERT INTO sanction_case_subjects(case_id,subject_type,team_id,is_primary,metadata) VALUES($1,'team',$2,false,jsonb_build_object('origin','case_team_added')) ON CONFLICT (case_id,team_id) WHERE subject_type='team' DO NOTHING RETURNING id`, caseID, teamID).Scan(&subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		var bridged bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_case_subject_intakes b JOIN sanction_case_subjects s ON s.id=b.subject_id WHERE s.case_id=$1 AND s.subject_type='team' AND s.team_id=$2)`, caseID, teamID).Scan(&bridged); err != nil {
			return err
		}
		if bridged {
			return errors.New("this team already has a source-report mapping; review that mapping rather than replacing it")
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_parties(case_id,party_type,name,team_id,relationship) VALUES($1,'club',$2,$3,'offending_club') ON CONFLICT (case_id,team_id) WHERE relationship='offending_club' AND team_id IS NOT NULL DO NOTHING`, caseID, clubName, teamID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id) VALUES($1,'case_team_added','admin',$2,$3,$4,jsonb_build_object('team_id',$5::integer,'subject_id',$6::bigint,'team_name',$7::text),NULLIF($8,''))`, caseID, *actor.ID, actor.Label, reason, teamID, subjectID, fmt.Sprintf("%s — %s", clubName, teamName), actor.RequestID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET updated_at=now() WHERE id=$1`, caseID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
