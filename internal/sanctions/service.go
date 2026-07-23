package sanctions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
)

var (
	ErrSeparationOfDuties = errors.New("the proposer cannot approve their own decision")
	ErrNotApprovable      = errors.New("case is not awaiting approval")
	ErrNotPublishable     = errors.New("case is not approved for publication")
)

type Actor struct {
	Type      string
	ID        *int32
	Label     string
	RequestID string
}

type CardCaseRequest struct {
	SourceType         string
	SeasonID           int32
	WeekID             int32
	ClubID             int32
	TeamID             int32
	MatchDate          *time.Time
	PlayCricketMatchID *int64
	PublicReason       string
	PrivateReason      string
	RuleReference      string
	CardRequest        CardRequest
	Actor              Actor
	LegacyReason       string
}

type ProposedCase struct {
	CaseID         int64
	Reference      string
	DecisionID     int64
	PolicyID       int64
	Calculation    Calculation
	AutomationMode string
}

type DecisionRequest struct {
	CaseID        int64
	EffectType    string
	PublicReason  string
	PrivateReason string
	RuleReference string
	AmountPence   *int64
	Points        *int
	StartsAt      *time.Time
	EndsAt        *time.Time
	Trigger       string
	Rescindable   bool
	Actor         Actor
}

type Service struct{ DB *db.Pool }

func NewService(pool *db.Pool) *Service { return &Service{DB: pool} }

// ProposeDecision adds the first immutable decision revision to an existing
// intake/investigation case. Card effects use the same policy calculation as
// automated and direct card proposals.
func (s *Service) ProposeDecision(ctx context.Context, req DecisionRequest) (int64, error) {
	if req.CaseID == 0 || req.Actor.ID == nil || strings.TrimSpace(req.PublicReason) == "" {
		return 0, errors.New("case, proposer, and public reason are required")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var source, status string
	var seasonID, weekID, clubID, teamID *int32
	var matchDate *time.Time
	if err = tx.QueryRow(ctx, `SELECT source_type,status,season_id,week_id,club_id,team_id,match_date FROM sanction_cases WHERE id=$1 FOR UPDATE`, req.CaseID).
		Scan(&source, &status, &seasonID, &weekID, &clubID, &teamID, &matchDate); err != nil {
		return 0, err
	}
	_ = source
	_ = weekID
	if req.StartsAt == nil {
		if matchDate != nil {
			req.StartsAt = matchDate
		} else {
			now := time.Now().UTC()
			req.StartsAt = &now
		}
	}
	if status == "decision_proposed" || status == "approved" || status == "published" {
		return 0, errors.New("case already has a live decision")
	}

	var policyID *int64
	var ruleReleaseID *int64
	_ = tx.QueryRow(ctx, `SELECT id FROM rule_releases WHERE status='active' LIMIT 1`).Scan(&ruleReleaseID)
	var calc *Calculation
	countsForTotting := false
	effectStatus := "pending"
	points := req.Points
	publicDetails := map[string]any{}
	privateDetails := map[string]any{}
	if req.EffectType == "yellow_card" || req.EffectType == "red_card" || req.EffectType == "suspended_red" {
		if seasonID == nil || clubID == nil || teamID == nil {
			return 0, errors.New("card decisions require season, club, and team")
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(83001,$1)`, *teamID); err != nil {
			return 0, err
		}
		var openCardProposal bool
		_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_cases c JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='proposed' JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id WHERE c.team_id=$1 AND c.id<>$2 AND c.status IN ('decision_proposed','triage') AND e.effect_type IN ('yellow_card','red_card','suspended_red'))`, *teamID, req.CaseID).Scan(&openCardProposal)
		if openCardProposal {
			return 0, errors.New("team already has an unresolved card proposal; resolve it before calculating another")
		}
		date := time.Now().UTC()
		if matchDate != nil {
			date = *matchDate
		}
		var pid int64
		var p Policy
		if err = tx.QueryRow(ctx, `SELECT id,rules_release_id,yellow_threshold,max_reds_per_match,club_board_red_threshold FROM sanction_policy_versions WHERE effective_from<=$1::date AND (effective_to IS NULL OR effective_to>=$1::date) ORDER BY effective_from DESC LIMIT 1`, date).
			Scan(&pid, &ruleReleaseID, &p.YellowThreshold, &p.MaxRedsPerMatch, &p.ClubBoardRedThreshold); err != nil {
			return 0, err
		}
		policyID = &pid
		state, loadErr := loadLedgerState(ctx, tx, *teamID, *clubID, *seasonID, matchDate)
		if loadErr != nil {
			return 0, loadErr
		}
		kind := "yellow"
		if req.EffectType == "red_card" {
			kind = "direct_red"
		}
		if req.EffectType == "suspended_red" {
			kind = "suspended_red"
		}
		calculated, calcErr := Calculate(p, state, CardRequest{Kind: kind, Rescindable: req.Rescindable})
		if calcErr != nil {
			return 0, calcErr
		}
		calc = &calculated
		req.EffectType = calculated.EffectType
		pval := calculated.PointsDeduction
		if pval != 0 {
			points = &pval
		}
		countsForTotting = req.EffectType == "yellow_card" || req.EffectType == "red_card"
		publicDetails["calculation_explanation"] = calculated.Explanation
		publicDetails["yellow_balance_after"] = calculated.YellowBalanceAfter
		publicDetails["team_red_count_after"] = calculated.TeamRedCountAfter
		publicDetails["create_board_review_task"] = calculated.CreateBoardReviewTask
		privateDetails["consumed_yellows"] = calculated.ConsumedYellowCount
		privateDetails["rescindable"] = req.Rescindable
	}

	var decisionID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id)
		VALUES($1,1,'proposed',$2,$3,$4,$5,$6,$7) RETURNING id`, req.CaseID, req.PublicReason, nullIfBlank(req.PrivateReason), ruleReleaseID, nullIfBlank(req.RuleReference), policyID, *req.Actor.ID).Scan(&decisionID); err != nil {
		return 0, err
	}
	subjectType := "case"
	var subjectID any
	if teamID != nil {
		subjectType = "team"
		subjectID = *teamID
	}
	if req.EffectType == "player_ban" {
		subjectType = "player"
		subjectID = nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_type,status,subject_type,subject_id,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, decisionID, req.EffectType, effectStatus, subjectType, subjectID, req.AmountPence, points, req.StartsAt, req.EndsAt, nullIfBlank(req.Trigger), mapJSON(publicDetails), mapJSON(privateDetails), countsForTotting); err != nil {
		return 0, err
	}
	after := map[string]any{"decision_revision_id": decisionID, "effect_type": req.EffectType}
	if calc != nil {
		after["calculation"] = calc
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='decision_proposed',public_summary=$2,private_summary=$3,proposed_by_admin_id=$4,updated_at=now() WHERE id=$1`, req.CaseID, req.PublicReason, nullIfBlank(req.PrivateReason), *req.Actor.ID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id) VALUES($1,'decision_proposed','admin',$2,$3,$4,$5,$6)`, req.CaseID, *req.Actor.ID, req.Actor.Label, req.PublicReason, mapJSON(after), req.Actor.RequestID); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return decisionID, nil
}

func actorID(a Actor) any {
	if a.ID == nil {
		return nil
	}
	return *a.ID
}

func (s *Service) ProposeCardCase(ctx context.Context, req CardCaseRequest) (ProposedCase, error) {
	if req.TeamID == 0 || req.ClubID == 0 || req.SeasonID == 0 || strings.TrimSpace(req.PublicReason) == "" {
		return ProposedCase{}, errors.New("team, club, season, and public reason are required")
	}
	if req.SourceType == "" {
		req.SourceType = "manual"
	}
	if req.LegacyReason == "" {
		req.LegacyReason = req.SourceType
	}

	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ProposedCase{}, err
	}
	defer tx.Rollback(ctx)

	// Totting for one team is serial even when a scheduled and manual command race.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(83001,$1)`, req.TeamID); err != nil {
		return ProposedCase{}, err
	}
	var openCardProposal bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_cases c JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='proposed' JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id WHERE c.team_id=$1 AND c.status IN ('decision_proposed','triage') AND e.effect_type IN ('yellow_card','red_card','suspended_red'))`, req.TeamID).Scan(&openCardProposal)
	if openCardProposal {
		return ProposedCase{}, errors.New("team already has an unresolved card proposal; approve, reject, or correct it before calculating another")
	}

	effectiveDate := time.Now().UTC()
	if req.MatchDate != nil {
		effectiveDate = *req.MatchDate
	}
	var policyID int64
	var ruleReleaseID *int64
	var p Policy
	if err = tx.QueryRow(ctx, `
		SELECT id,rules_release_id,yellow_threshold,max_reds_per_match,club_board_red_threshold
		FROM sanction_policy_versions
		WHERE effective_from <= $1::date AND (effective_to IS NULL OR effective_to >= $1::date)
		ORDER BY effective_from DESC LIMIT 1`, effectiveDate).Scan(&policyID, &ruleReleaseID, &p.YellowThreshold, &p.MaxRedsPerMatch, &p.ClubBoardRedThreshold); err != nil {
		return ProposedCase{}, fmt.Errorf("load sanction policy: %w", err)
	}

	state, err := loadLedgerState(ctx, tx, req.TeamID, req.ClubID, req.SeasonID, req.MatchDate)
	if err != nil {
		return ProposedCase{}, err
	}
	calc, err := Calculate(p, state, req.CardRequest)
	if err != nil {
		return ProposedCase{}, err
	}

	mode := "manual"
	_ = tx.QueryRow(ctx, `SELECT mode FROM sanction_automation_settings WHERE source_type=$1 AND enabled`, req.SourceType).Scan(&mode)
	status := "decision_proposed"
	if mode == "shadow" {
		status = "triage"
	}

	var caseID int64
	var reference string
	if err = tx.QueryRow(ctx, `
		INSERT INTO sanction_cases
		(source_type,status,season_id,week_id,club_id,team_id,match_date,play_cricket_match_id,
		 public_summary,private_summary,proposed_by_admin_id)
		VALUES($1,$2,$3,NULLIF($4,0),$5,$6,$7,$8,$9,$10,$11)
		RETURNING id,reference`, req.SourceType, status, req.SeasonID, req.WeekID, req.ClubID, req.TeamID,
		req.MatchDate, req.PlayCricketMatchID, req.PublicReason, req.PrivateReason, actorID(req.Actor)).Scan(&caseID, &reference); err != nil {
		return ProposedCase{}, err
	}

	after, _ := json.Marshal(map[string]any{"status": status, "calculation": calc, "policy_version_id": policyID})
	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id,metadata)
		VALUES($1,'decision_proposed',$2,$3,$4,$5,$6,$7,$8)`, caseID, req.Actor.Type, actorID(req.Actor), req.Actor.Label,
		req.PublicReason, after, req.Actor.RequestID, mapJSON(map[string]any{"calculation_explanation": calc.Explanation})); err != nil {
		return ProposedCase{}, err
	}

	var decisionID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO sanction_decision_revisions
		(case_id,revision,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id)
		VALUES($1,1,'proposed',$2,$3,$4,$5,$6,$7) RETURNING id`, caseID, req.PublicReason, req.PrivateReason,
		ruleReleaseID, nullIfBlank(req.RuleReference), policyID, actorID(req.Actor)).Scan(&decisionID); err != nil {
		return ProposedCase{}, err
	}

	counts := calc.EffectType == "yellow_card" || calc.EffectType == "red_card"
	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_effect_revisions
		(decision_revision_id,effect_type,status,subject_type,subject_id,points,starts_at,public_details,private_details,counts_for_totting)
		VALUES($1,$2,'pending','team',$3,$4,$5,$6,$7,$8)`, decisionID, calc.EffectType, req.TeamID,
		nullIfZero(calc.PointsDeduction), effectiveDate, mapJSON(map[string]any{
			"explanation": calc.Explanation, "yellow_balance_after": calc.YellowBalanceAfter,
			"team_red_count_after": calc.TeamRedCountAfter, "create_board_review_task": calc.CreateBoardReviewTask,
		}), mapJSON(map[string]any{"legacy_reason": req.LegacyReason, "consumed_yellows": calc.ConsumedYellowCount, "rescindable": req.CardRequest.Rescindable}), counts); err != nil {
		return ProposedCase{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return ProposedCase{}, err
	}
	return ProposedCase{CaseID: caseID, Reference: reference, DecisionID: decisionID, PolicyID: policyID, Calculation: calc, AutomationMode: mode}, nil
}

func loadLedgerState(ctx context.Context, tx pgx.Tx, teamID, clubID, seasonID int32, matchDate *time.Time) (LedgerState, error) {
	var st LedgerState
	// New-model balances come only from append-only ledger deltas. Legacy rows
	// supply the opening balance until the historical import is reconciled.
	err := tx.QueryRow(ctx, `
		WITH legacy AS (
		  SELECT colour::text AS effect_type,status::text AS status,season_id,club_id,offence_date
		  FROM sanctions WHERE team_id=$1 AND case_id IS NULL AND status IN ('active','served')
		)
		SELECT
		 COALESCE((SELECT SUM(yellow_delta) FROM sanction_card_ledger_entries WHERE team_id=$1),0)
		 +(SELECT COUNT(*) FROM legacy WHERE effect_type='yellow' AND status='active'),
		 COALESCE((SELECT SUM(red_delta) FROM sanction_card_ledger_entries WHERE team_id=$1 AND season_id=$3),0)
		 +(SELECT COUNT(*) FROM legacy WHERE effect_type='red' AND season_id=$3),
		 COALESCE((SELECT SUM(red_delta) FROM sanction_card_ledger_entries WHERE club_id=$2 AND season_id=$3),0)
		 +(SELECT COUNT(*) FROM sanctions WHERE club_id=$2 AND season_id=$3 AND colour='red' AND case_id IS NULL AND status IN ('active','served'))`, teamID, clubID, seasonID).Scan(&st.YellowBalance, &st.TeamRedCount, &st.ClubRedCount)
	if err != nil {
		return st, fmt.Errorf("load card ledger: %w", err)
	}
	if matchDate != nil {
		_ = tx.QueryRow(ctx, `SELECT COALESCE((SELECT SUM(red_delta) FROM sanction_card_ledger_entries WHERE team_id=$1 AND match_date=$2::date),0) + (SELECT COUNT(*) FROM sanctions WHERE team_id=$1 AND case_id IS NULL AND colour='red' AND status IN ('active','served') AND offence_date=$2::date)`, teamID, *matchDate).Scan(&st.MatchRedCount)
	}
	return st, nil
}

func (s *Service) ApproveCase(ctx context.Context, caseID int64, approver Actor, emergencyReason string) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var proposer *int32
	var status, sourceType string
	if err = tx.QueryRow(ctx, `SELECT proposed_by_admin_id,status,source_type FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&proposer, &status, &sourceType); err != nil {
		return err
	}
	emergency := strings.TrimSpace(emergencyReason) != ""
	if approver.ID == nil {
		var mode string
		var clean int
		var sourceEnabled, globalEnabled bool
		_ = tx.QueryRow(ctx, `SELECT s.mode,s.clean_cycles,s.enabled,g.enabled FROM sanction_automation_settings s JOIN sanction_automation_settings g ON g.source_type='_global' WHERE s.source_type=$1`, sourceType).Scan(&mode, &clean, &sourceEnabled, &globalEnabled)
		if (approver.Type != "system" && approver.Type != "n8n") || mode != "automatic" || clean < 3 || !sourceEnabled || !globalEnabled {
			return errors.New("deterministic automatic approval is not enabled")
		}
	}
	if proposer != nil && approver.ID != nil && *proposer == *approver.ID && !emergency {
		return ErrSeparationOfDuties
	}
	if status != "decision_proposed" && status != "triage" {
		return ErrNotApprovable
	}

	var proposedID int64
	var revision int
	var publicReason string
	var privateReason *string
	var ruleRelease *int64
	var ruleRef *string
	var policyID *int64
	if err = tx.QueryRow(ctx, `SELECT id,revision,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id FROM sanction_decision_revisions WHERE case_id=$1 AND status='proposed' ORDER BY revision DESC LIMIT 1`, caseID).
		Scan(&proposedID, &revision, &publicReason, &privateReason, &ruleRelease, &ruleRef, &policyID); err != nil {
		return err
	}
	var approvedID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,approved_by_admin_id,correction_reason,emergency_override)
		SELECT case_id,$2,id,'approved',public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,$3,$4,$5 FROM sanction_decision_revisions WHERE id=$1 RETURNING id`,
		proposedID, revision+1, actorID(approver), nullIfBlank(emergencyReason), emergency).Scan(&approvedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_key,supersedes_id,effect_type,status,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting)
		SELECT $2,effect_key,id,effect_type,CASE WHEN effect_type='suspended_red' OR COALESCE((private_details->>'rescindable')::boolean,FALSE) THEN 'suspended' WHEN effect_type='no_action' THEN 'cancelled' ELSE 'active' END,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting
		FROM sanction_effect_revisions WHERE decision_revision_id=$1`, proposedID, approvedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='approved',approved_by_admin_id=$2,approved_at=now(),current_revision=$3,emergency_override=$4,updated_at=now() WHERE id=$1`, caseID, actorID(approver), revision+1, emergency); err != nil {
		return err
	}
	// Append the authoritative balance delta. A conversion consumes the two
	// existing yellows plus the new offence: delta = 1 - threshold.
	_, err = tx.Exec(ctx, `
		INSERT INTO sanction_card_ledger_entries(case_id,decision_revision_id,team_id,club_id,season_id,match_date,yellow_delta,red_delta,points_deduction,entry_type,explanation)
		SELECT c.id,$2,c.team_id,c.club_id,c.season_id,c.match_date,
		       CASE WHEN e.effect_type='yellow_card' AND e.status='active' THEN 1 WHEN e.effect_type='red_card' AND COALESCE((e.private_details->>'consumed_yellows')::int,0)>0 THEN 1-(e.private_details->>'consumed_yellows')::int ELSE 0 END,
		       CASE WHEN e.effect_type='red_card' THEN 1 ELSE 0 END,
		       COALESCE(e.points,0),
		       CASE WHEN e.effect_type='red_card' AND COALESCE((e.private_details->>'consumed_yellows')::int,0)>0 THEN 'conversion' ELSE 'issue' END,
		       COALESCE(e.public_details->>'explanation','Approved card effect')
		FROM sanction_cases c
		JOIN sanction_effect_revisions e ON e.decision_revision_id=$2
		WHERE c.id=$1 AND c.team_id IS NOT NULL AND c.club_id IS NOT NULL AND c.season_id IS NOT NULL
		  AND (e.effect_type='red_card' OR (e.effect_type='yellow_card' AND e.status='active'))`, caseID, approvedID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,emergency_override,request_id,after_data)
		VALUES($1,'decision_approved',$2,$3,$4,$5,$6,$7,$8)`, caseID, approver.Type, actorID(approver), approver.Label, nullIfBlank(emergencyReason), emergency, approver.RequestID, mapJSON(map[string]any{"decision_revision_id": approvedID})); err != nil {
		return err
	}

	// Create operational tasks from approved effects.
	_, _ = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,due_at,current_note)
		SELECT $1,'play_cricket_points',now()+interval '2 days','Apply approved points deduction' WHERE EXISTS (SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND COALESCE(points,0)<>0)`, caseID, approvedID)
	_, _ = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,current_note)
		SELECT $1,'fine_recovery','Recover approved fine' WHERE EXISTS (SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND effect_type='fine')`, caseID, approvedID)
	_, _ = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,current_note)
		SELECT $1,'board_intervention','Club reached the configured red-card review threshold' WHERE EXISTS (SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND COALESCE((public_details->>'create_board_review_task')::boolean,FALSE))`, caseID, approvedID)
	_, _ = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,due_at,current_note)
		SELECT $1,'suspended_review',ends_at,'Review suspended or rescindable sanction' FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND status='suspended'`, caseID, approvedID)
	_, _ = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,due_at,current_note)
		SELECT $1,'ban_expiry',ends_at,'Review ban expiry' FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND effect_type IN ('player_ban','team_ban') AND ends_at IS NOT NULL`, caseID, approvedID)
	_, _ = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,due_at,current_note)
		SELECT $1,'appeal_deadline',appeal_due_at,'Monitor appeal deadline' FROM sanction_decision_revisions WHERE id=$2 AND appeal_due_at IS NOT NULL`, caseID, approvedID)

	// Maintain the old sanctions table as a temporary compatibility projection.
	var seasonID, weekID, teamID, clubID *int32
	var matchDate *time.Time
	_ = tx.QueryRow(ctx, `SELECT season_id,week_id,team_id,club_id,match_date FROM sanction_cases WHERE id=$1`, caseID).Scan(&seasonID, &weekID, &teamID, &clubID, &matchDate)
	if seasonID != nil && weekID != nil && teamID != nil && clubID != nil {
		var effectType string
		var points *int
		if tx.QueryRow(ctx, `SELECT effect_type,points FROM sanction_effect_revisions WHERE decision_revision_id=$1 AND effect_type IN ('yellow_card','red_card') LIMIT 1`, approvedID).Scan(&effectType, &points) == nil {
			colour := strings.TrimSuffix(effectType, "_card")
			_, err = tx.Exec(ctx, `INSERT INTO sanctions(season_id,week_id,team_id,club_id,colour,reason,notes,points_deduction,status,issued_by_admin_id,offence_date,rule_reference,case_id)
				VALUES($1,$2,$3,$4,$5,'case_management',$6,$7,'active',$8,$9,$10,$11) ON CONFLICT(case_id) DO NOTHING`, *seasonID, *weekID, *teamID, *clubID, colour, publicReason, points, actorID(approver), matchDate, ruleRef, caseID)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) PublishCase(ctx context.Context, caseID int64, actor Actor) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&status); err != nil {
		return err
	}
	if status != "approved" {
		return ErrNotPublishable
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='published',public_status=CASE WHEN EXISTS(SELECT 1 FROM sanction_effect_revisions e JOIN sanction_decision_revisions d ON d.id=e.decision_revision_id WHERE d.case_id=$1 AND d.status='approved' AND e.status='suspended') THEN 'suspended' ELSE 'active' END,published_at=now(),updated_at=now() WHERE id=$1`, caseID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,request_id) VALUES($1,'published',$2,$3,$4,$5)`, caseID, actor.Type, actorID(actor), actor.Label, actor.RequestID); err != nil {
		return err
	}

	var decisionID int64
	_ = tx.QueryRow(ctx, `SELECT id FROM sanction_decision_revisions WHERE case_id=$1 AND status='approved' ORDER BY revision DESC LIMIT 1`, caseID).Scan(&decisionID)
	// Resolve the versioned recipient policy into immutable per-recipient rows.
	_, err = tx.Exec(ctx, `INSERT INTO sanction_notification_outbox(case_id,decision_revision_id,policy_version_id,idempotency_key,recipient,subject,body)
		WITH recipients AS (
		  SELECT cap.email FROM sanction_cases c JOIN captains cap ON cap.team_id=c.team_id AND cap.active_from<=CURRENT_DATE AND (cap.active_to IS NULL OR cap.active_to>=CURRENT_DATE) WHERE c.id=$1
		  UNION
		  SELECT rd.email FROM sanction_recipient_directory rd WHERE rd.active AND (
		    rd.recipient_role IN ('executive','discipline')
		    OR (rd.recipient_role='finance' AND EXISTS(SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND effect_type='fine'))
		    OR (rd.recipient_role='play_cricket' AND EXISTS(SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND COALESCE(points,0)<>0))
		  )
		), policy AS (SELECT id FROM sanction_notification_policy_versions WHERE active AND source_type='*' AND event_type='decision_published' ORDER BY version DESC LIMIT 1)
		SELECT c.id,$2,policy.id,'case:'||c.id||':decision:'||$2||':recipient:'||lower(r.email),r.email,
		       'GMCL sanction decision '||c.reference,
		       c.public_summary||E'\n\nCase reference: '||c.reference
		FROM sanction_cases c CROSS JOIN recipients r CROSS JOIN policy
		WHERE c.id=$1 ON CONFLICT(idempotency_key) DO NOTHING`, caseID, decisionID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) RejectProposedCase(ctx context.Context, caseID int64, actor Actor, reason string) error {
	if actor.ID == nil || strings.TrimSpace(reason) == "" {
		return errors.New("actor and rejection reason are required")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&status); err != nil {
		return err
	}
	if status != "decision_proposed" && status != "triage" {
		return errors.New("case is not awaiting a decision")
	}
	var priorID int64
	var revision int
	if err = tx.QueryRow(ctx, `SELECT id,revision FROM sanction_decision_revisions WHERE case_id=$1 AND status='proposed' ORDER BY revision DESC LIMIT 1`, caseID).Scan(&priorID, &revision); err != nil {
		return err
	}
	var rejectedID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,approved_by_admin_id,correction_reason) SELECT case_id,$2,id,'rejected',public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,$3,$4 FROM sanction_decision_revisions WHERE id=$1 RETURNING id`, priorID, revision+1, *actor.ID, reason).Scan(&rejectedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_key,supersedes_id,effect_type,status,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting) SELECT $2,effect_key,id,effect_type,'cancelled',subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting FROM sanction_effect_revisions WHERE decision_revision_id=$1`, priorID, rejectedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='rejected',current_revision=$2,closed_at=now(),updated_at=now() WHERE id=$1`, caseID, revision+1); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data) VALUES($1,'decision_rejected','admin',$2,$3,$4,$5,$6)`, caseID, *actor.ID, actor.Label, reason, actor.RequestID, mapJSON(map[string]any{"decision_revision_id": rejectedID})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OverturnCase records a compensating revision and ledger entry. It never
// deletes or rewrites the original decision or card balance entry.
func (s *Service) OverturnCase(ctx context.Context, caseID int64, actor Actor, reason string) error {
	if actor.ID == nil || strings.TrimSpace(reason) == "" {
		return errors.New("actor and overturn reason are required")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&status); err != nil {
		return err
	}
	if status != "approved" && status != "published" && status != "appealed" && status != "closed" {
		return errors.New("case has no effective decision to overturn")
	}
	var priorID int64
	var priorRevision int
	if err = tx.QueryRow(ctx, `SELECT id,revision FROM sanction_decision_revisions WHERE case_id=$1 AND status='approved' ORDER BY revision DESC LIMIT 1`, caseID).Scan(&priorID, &priorRevision); err != nil {
		return err
	}
	var overturnedID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,approved_by_admin_id,correction_reason)
		SELECT case_id,$2,id,'overturned',public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,$3,$4 FROM sanction_decision_revisions WHERE id=$1 RETURNING id`, priorID, priorRevision+1, *actor.ID, reason).Scan(&overturnedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_key,supersedes_id,effect_type,status,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting)
		SELECT $2,effect_key,id,effect_type,'overturned',subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting FROM sanction_effect_revisions WHERE decision_revision_id=$1`, priorID, overturnedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_card_ledger_entries(case_id,decision_revision_id,team_id,club_id,season_id,match_date,yellow_delta,red_delta,points_deduction,entry_type,explanation)
		SELECT case_id,$2,team_id,club_id,season_id,match_date,-SUM(yellow_delta),-SUM(red_delta),-SUM(points_deduction),'reversal',$3 FROM sanction_card_ledger_entries WHERE case_id=$1 GROUP BY case_id,team_id,club_id,season_id,match_date HAVING SUM(yellow_delta)<>0 OR SUM(red_delta)<>0 OR SUM(points_deduction)<>0`, caseID, overturnedID, reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='closed',public_status='overturned',closed_at=now(),current_revision=$2,updated_at=now() WHERE id=$1`, caseID, priorRevision+1); err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `UPDATE sanctions SET status='overturned',resolved_at=now(),resolved_by_admin_id=$2 WHERE case_id=$1 AND status IN ('active','served')`, caseID, *actor.ID)
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data) VALUES($1,'decision_overturned','admin',$2,$3,$4,$5,$6)`, caseID, *actor.ID, actor.Label, reason, actor.RequestID, mapJSON(map[string]any{"decision_revision_id": overturnedID})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func mapJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func nullIfBlank(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.TrimSpace(v)
}
func nullIfZero(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
