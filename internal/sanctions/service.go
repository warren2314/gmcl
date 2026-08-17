package sanctions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
)

const PlayCricketHelpCopyRecipient = "playcrickethelp@gtrmcrcricket.co.uk"

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

// DecisionEffectRequest is one subject-specific effect in an atomic decision
// bundle. CaseSubjectID preserves the player/team provenance even when a card
// effect posts to the subject's team ledger.
type DecisionEffectRequest struct {
	EffectType    string
	CaseSubjectID *int64
	SubjectType   string
	SubjectID     *int64
	PlayerName    string
	AmountPence   *int64
	Points        *int
	StartsAt      *time.Time
	EndsAt        *time.Time
	Trigger       string
	Rescindable   bool
}

// DecisionBundleRequest is approved or rejected as one immutable unit.
type DecisionBundleRequest struct {
	CaseID             int64
	PublicReason       string
	PrivateReason      string
	RuleReference      string
	OutcomeSubject     string
	OutcomeFindings    string
	AppealInstructions string
	Effects            []DecisionEffectRequest
	Actor              Actor
}

type Service struct{ DB *db.Pool }

func NewService(pool *db.Pool) *Service { return &Service{DB: pool} }

// EnsureIneligibleLinkedIntakesCurrent fails closed when a linked source has a
// newer immutable revision than the one investigators merged into this case.
// Duplicate links are audit-only and do not contribute subjects or evidence.
// All callers use serializable transactions, so a concurrent sync cannot race
// an approval/publication snapshot without one transaction being retried.
func EnsureIneligibleLinkedIntakesCurrent(ctx context.Context, tx pgx.Tx, caseID int64) error {
	stale, err := staleIneligibleLinkedIntakes(ctx, tx, caseID)
	if err != nil {
		return fmt.Errorf("validate linked intake revisions: %w", err)
	}
	if stale {
		return errors.New("a linked intake changed after triage; review and merge its latest immutable revision before proposing, approving, or publishing")
	}
	return nil
}

// ProposeDecision preserves the original single-effect API while routing every
// proposal through the atomic bundle command.
func (s *Service) ProposeDecision(ctx context.Context, req DecisionRequest) (int64, error) {
	return s.ProposeDecisionBundle(ctx, DecisionBundleRequest{
		CaseID:        req.CaseID,
		PublicReason:  req.PublicReason,
		PrivateReason: req.PrivateReason,
		RuleReference: req.RuleReference,
		Effects: []DecisionEffectRequest{{
			EffectType: req.EffectType, AmountPence: req.AmountPence, Points: req.Points,
			StartsAt: req.StartsAt, EndsAt: req.EndsAt, Trigger: req.Trigger, Rescindable: req.Rescindable,
		}},
		Actor: req.Actor,
	})
}

type preparedDecisionEffect struct {
	request         DecisionEffectRequest
	caseSubjectID   *int64
	caseSubjectType string
	subjectType     string
	subjectID       *int64
	playerName      string
	teamID          *int32
	clubID          *int32
}

// ProposeDecisionBundle calculates and records every effect in one serializable
// transaction. A failure in any subject mapping or card calculation rolls back
// the whole proposal.
func (s *Service) ProposeDecisionBundle(ctx context.Context, req DecisionBundleRequest) (int64, error) {
	if req.CaseID == 0 || req.Actor.ID == nil || strings.TrimSpace(req.PublicReason) == "" || len(req.Effects) == 0 {
		return 0, errors.New("case, proposer, public reason, and at least one effect are required")
	}
	allowed := map[string]bool{
		"yellow_card": true, "red_card": true, "suspended_red": true,
		"player_ban": true, "team_ban": true, "fine": true,
		"points_adjustment": true, "warning": true, "no_action": true,
	}
	if len(req.Effects) > 20 {
		return 0, errors.New("a decision bundle may contain at most 20 effects")
	}
	noAction := false
	for i := range req.Effects {
		req.Effects[i].EffectType = strings.TrimSpace(req.Effects[i].EffectType)
		if !allowed[req.Effects[i].EffectType] {
			return 0, fmt.Errorf("unsupported effect %q", req.Effects[i].EffectType)
		}
		noAction = noAction || req.Effects[i].EffectType == "no_action"
		if req.Effects[i].Rescindable && req.Effects[i].EffectType != "yellow_card" {
			return 0, errors.New("only a yellow card may be marked rescindable")
		}
		if err := validateDecisionEffectFields(req.Effects[i]); err != nil {
			return 0, err
		}
	}
	if noAction && len(req.Effects) != 1 {
		return 0, errors.New("no action must be the only effect in its decision bundle")
	}

	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var source, status, reference, currentPublicSummary, currentPrivateSummary string
	var seasonID, weekID, caseClubID, caseTeamID, assignedAdminID *int32
	var casePlayer *string
	var matchDate *time.Time
	if err = tx.QueryRow(ctx, `SELECT c.source_type,c.status,c.reference,c.season_id,c.week_id,c.club_id,c.team_id,c.assigned_admin_id,c.player_name,c.match_date,COALESCE(c.public_summary,''),COALESCE(c.private_summary,'')
		FROM sanction_cases c WHERE c.id=$1 FOR UPDATE OF c`, req.CaseID).
		Scan(&source, &status, &reference, &seasonID, &weekID, &caseClubID, &caseTeamID, &assignedAdminID, &casePlayer, &matchDate, &currentPublicSummary, &currentPrivateSummary); err != nil {
		return 0, err
	}
	if assignedAdminID == nil || *assignedAdminID != *req.Actor.ID {
		return 0, errors.New("only the assigned case owner can decide and submit the sanctions, points and fines")
	}
	_ = source
	_ = weekID
	if !map[string]bool{"submitted": true, "triage": true, "investigating": true}[status] {
		if status == "response_pending" {
			return 0, errors.New("the club response window must be completed or expired before proposing a decision")
		}
		return 0, errors.New("case is not in a state that permits a decision proposal")
	}
	if strings.TrimSpace(req.RuleReference) == "" {
		return 0, errors.New("an outcome requires a reviewed rule reference or an explicit not-applicable determination")
	}
	if source == "ineligible_player" {
		if err = EnsureIneligibleLinkedIntakesCurrent(ctx, tx, req.CaseID); err != nil {
			return 0, err
		}
	}

	prepared := make([]preparedDecisionEffect, 0, len(req.Effects))
	cardTeams := map[int32]bool{}
	for _, effect := range req.Effects {
		p := preparedDecisionEffect{request: effect, caseSubjectID: effect.CaseSubjectID, subjectType: strings.TrimSpace(effect.SubjectType), subjectID: effect.SubjectID, playerName: strings.TrimSpace(effect.PlayerName)}
		if effect.CaseSubjectID != nil {
			var subjectType string
			var subjectTeamID *int32
			var subjectPlayer *string
			var subjectPlayerID *int64
			if err = tx.QueryRow(ctx, `SELECT subject_type,team_id,player_name,play_cricket_player_id
				FROM sanction_case_subjects subject
				WHERE subject.id=$1 AND subject.case_id=$2 AND (
					NOT EXISTS(SELECT 1 FROM sanction_case_subject_intakes bridge WHERE bridge.subject_id=subject.id)
					OR EXISTS(
						SELECT 1 FROM sanction_case_intake_merge_resolutions resolution
						WHERE resolution.case_id=subject.case_id
						  AND resolution.id=(SELECT latest.id FROM sanction_case_intake_merge_resolutions latest
							WHERE latest.case_id=resolution.case_id AND latest.intake_id=resolution.intake_id
							ORDER BY latest.id DESC LIMIT 1)
						  AND subject.id IN (resolution.team_subject_id,resolution.player_subject_id,COALESCE(resolution.match_subject_id,0))
					)
				)`, *effect.CaseSubjectID, req.CaseID).
				Scan(&subjectType, &subjectTeamID, &subjectPlayer, &subjectPlayerID); err != nil {
				return 0, errors.New("selected decision subject does not belong to this case")
			}
			p.subjectType = subjectType
			p.caseSubjectType = subjectType
			p.teamID = subjectTeamID
			if subjectPlayer != nil {
				p.playerName = *subjectPlayer
			}
			if subjectType == "team" && subjectTeamID != nil {
				v := int64(*subjectTeamID)
				p.subjectID = &v
			} else if subjectPlayerID != nil {
				p.subjectID = subjectPlayerID
			}
		}
		if p.subjectType == "" {
			p.subjectType = "case"
			if caseTeamID != nil {
				p.subjectType = "team"
				p.teamID = caseTeamID
				v := int64(*caseTeamID)
				p.subjectID = &v
			}
		}
		if p.subjectType == "match" {
			// The effect schema keeps match provenance through case_subject_id;
			// the enforceable subject itself is the case.
			p.subjectType = "case"
			p.subjectID = nil
		}
		if p.subjectType == "team" && p.teamID == nil && p.subjectID != nil {
			v := int32(*p.subjectID)
			p.teamID = &v
		}
		if p.playerName == "" && casePlayer != nil {
			p.playerName = strings.TrimSpace(*casePlayer)
		}
		isCard := effect.EffectType == "yellow_card" || effect.EffectType == "red_card" || effect.EffectType == "suspended_red"
		if isCard {
			if p.teamID == nil || seasonID == nil {
				return 0, errors.New("card effects require a mapped team and season")
			}
			p.subjectType = "team"
			v := int64(*p.teamID)
			p.subjectID = &v
			cardTeams[*p.teamID] = true
		}
		if p.teamID != nil {
			var club int32
			if err = tx.QueryRow(ctx, `SELECT club_id FROM teams WHERE id=$1`, *p.teamID).Scan(&club); err != nil {
				return 0, errors.New("decision subject team has no club mapping")
			}
			p.clubID = &club
		} else {
			p.clubID = caseClubID
		}
		if effect.EffectType == "player_ban" {
			if p.playerName == "" {
				return 0, errors.New("player ban requires a named player subject")
			}
			if p.caseSubjectID != nil && p.caseSubjectType != "player" {
				return 0, errors.New("player ban must reference a player case subject")
			}
			p.subjectType = "player"
			if p.caseSubjectType != "player" {
				p.subjectID = nil
			}
		}
		if effect.EffectType == "team_ban" {
			if p.teamID == nil {
				return 0, errors.New("team ban requires a mapped team subject")
			}
			if p.caseSubjectID != nil && p.caseSubjectType != "team" {
				return 0, errors.New("team ban must reference a team case subject")
			}
			p.subjectType = "team"
			v := int64(*p.teamID)
			p.subjectID = &v
		}
		if effect.EffectType == "points_adjustment" && (effect.Points == nil || *effect.Points == 0) {
			return 0, errors.New("league-points adjustment requires a non-zero points value")
		}
		if effect.EffectType == "points_adjustment" {
			if p.teamID == nil {
				return 0, errors.New("league-points adjustment requires a mapped team subject")
			}
			p.subjectType = "team"
			v := int64(*p.teamID)
			p.subjectID = &v
		}
		if effect.EffectType == "fine" && (effect.AmountPence == nil || *effect.AmountPence <= 0) {
			return 0, errors.New("fine requires an amount greater than zero")
		}
		if p.subjectType == "team" && !isCard {
			// A case-level player name is provenance, not the subject of a
			// team sanction. Keep player labels only for player/card effects.
			p.playerName = ""
		}
		prepared = append(prepared, p)
	}

	teamIDs := make([]int, 0, len(cardTeams))
	for id := range cardTeams {
		teamIDs = append(teamIDs, int(id))
	}
	sort.Ints(teamIDs)
	for _, id := range teamIDs {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(83001,$1)`, id); err != nil {
			return 0, err
		}
		var open bool
		_ = tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM sanction_cases c
			JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='proposed'
			JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id
			LEFT JOIN sanction_case_subjects cs ON cs.id=e.case_subject_id
			WHERE c.id<>$2 AND c.status IN ('decision_proposed','triage')
			  AND e.effect_type IN ('yellow_card','red_card','suspended_red')
			  AND COALESCE(cs.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END,c.team_id)=$1
		)`, id, req.CaseID).Scan(&open)
		if open {
			return 0, fmt.Errorf("team %d already has an unresolved card proposal", id)
		}
	}

	var policyID *int64
	var ruleReleaseID *int64
	_ = tx.QueryRow(ctx, `SELECT id FROM rule_releases WHERE status='active' ORDER BY id DESC LIMIT 1`).Scan(&ruleReleaseID)
	var policy Policy
	if len(teamIDs) > 0 {
		effectiveDate := time.Now().UTC()
		if matchDate != nil {
			effectiveDate = *matchDate
		}
		var id int64
		if err = tx.QueryRow(ctx, `SELECT id,rules_release_id,yellow_threshold,max_reds_per_match,club_board_red_threshold FROM sanction_policy_versions WHERE effective_from<=$1::date AND (effective_to IS NULL OR effective_to>=$1::date) ORDER BY effective_from DESC LIMIT 1`, effectiveDate).
			Scan(&id, &ruleReleaseID, &policy.YellowThreshold, &policy.MaxRedsPerMatch, &policy.ClubBoardRedThreshold); err != nil {
			return 0, err
		}
		policyID = &id
	}

	outcomeSubject := strings.TrimSpace(req.OutcomeSubject)
	if outcomeSubject == "" {
		outcomeSubject = "GMCL ineligible-player case outcome " + reference
	}
	outcomeFindings := strings.TrimSpace(req.OutcomeFindings)
	if outcomeFindings == "" {
		outcomeFindings = strings.TrimSpace(req.PublicReason)
	}
	appealInstructions := strings.TrimSpace(req.AppealInstructions)
	if appealInstructions == "" {
		appealInstructions = "Any appeal must be submitted to the league in accordance with the current GMCL regulations."
	}
	if source == "ineligible_player" {
		privacyValues, privacyErr := CaseReporterIdentityValues(ctx, tx, req.CaseID)
		if privacyErr != nil {
			return 0, privacyErr
		}
		reportingAliases, privacyErr := CaseReportingClubIdentityValues(ctx, tx, req.CaseID, caseClubID)
		if privacyErr != nil {
			return 0, privacyErr
		}
		privacyValues = append(privacyValues, reportingAliases...)
		if ContainsPrivateIdentity(
			strings.Join([]string{currentPublicSummary, req.PublicReason, outcomeSubject, outcomeFindings, appealInstructions}, "\n"),
			privacyValues...,
		) {
			return 0, errors.New("public decision wording contains reporter or reporting-club identity; redact it before proposing")
		}
		restricted, restrictedErr := CaseReportingOutcomeRestrictedValues(ctx, tx, req.CaseID)
		if restrictedErr != nil {
			return 0, restrictedErr
		}
		if ContainsRestrictedContent(strings.Join([]string{req.PublicReason, outcomeFindings}, "\n"), restricted...) {
			return 0, errors.New("public findings contain an offending-club response, internal note, private rationale, or private evidence label; record a disclosure-safe finding instead")
		}
	}
	var supersedesID *int64
	var latestRevision int
	latestErr := tx.QueryRow(ctx, `SELECT id,revision FROM sanction_decision_revisions WHERE case_id=$1 ORDER BY revision DESC LIMIT 1`, req.CaseID).Scan(&supersedesID, &latestRevision)
	if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
		return 0, latestErr
	}
	nextRevision := latestRevision + 1
	var decisionID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,outcome_subject,outcome_findings,appeal_instructions)
		VALUES($1,$2,$3,'proposed',$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, req.CaseID, nextRevision, supersedesID, req.PublicReason, nullIfBlank(req.PrivateReason), ruleReleaseID, nullIfBlank(req.RuleReference), policyID, *req.Actor.ID, outcomeSubject, outcomeFindings, appealInstructions).Scan(&decisionID); err != nil {
		return 0, err
	}

	ledgerStates := map[int32]LedgerState{}
	clubRedDelta := map[int32]int{}
	afterEffects := make([]map[string]any, 0, len(prepared))
	for _, p := range prepared {
		effect := p.request
		if effect.StartsAt == nil {
			if matchDate != nil {
				effect.StartsAt = matchDate
			} else {
				now := time.Now().UTC()
				effect.StartsAt = &now
			}
		}
		points := effect.Points
		countsForTotting := false
		publicDetails := map[string]any{}
		privateDetails := map[string]any{}
		isCard := effect.EffectType == "yellow_card" || effect.EffectType == "red_card" || effect.EffectType == "suspended_red"
		if isCard {
			state, loaded := ledgerStates[*p.teamID]
			if !loaded {
				state, err = loadLedgerState(ctx, tx, *p.teamID, *p.clubID, *seasonID, matchDate)
				if err != nil {
					return 0, err
				}
			}
			state.ClubRedCount += clubRedDelta[*p.clubID]
			kind := map[string]string{"yellow_card": "yellow", "red_card": "direct_red", "suspended_red": "suspended_red"}[effect.EffectType]
			calc, calcErr := Calculate(policy, state, CardRequest{Kind: kind, Rescindable: effect.Rescindable})
			if calcErr != nil {
				return 0, calcErr
			}
			effect.EffectType = calc.EffectType
			if calc.Suppressed && len(prepared) > 1 {
				return 0, errors.New("a policy-suppressed card cannot be combined with other effects; record an explicit standalone no-action decision")
			}
			if calc.PointsDeduction != 0 {
				v := calc.PointsDeduction
				points = &v
			} else {
				points = nil
			}
			countsForTotting = effect.EffectType == "yellow_card" || effect.EffectType == "red_card"
			publicDetails["explanation"] = calc.Explanation
			publicDetails["calculation_explanation"] = calc.Explanation
			publicDetails["yellow_balance_after"] = calc.YellowBalanceAfter
			publicDetails["team_red_count_after"] = calc.TeamRedCountAfter
			publicDetails["create_board_review_task"] = calc.CreateBoardReviewTask
			privateDetails["consumed_yellows"] = calc.ConsumedYellowCount
			privateDetails["rescindable"] = effect.Rescindable
			if calc.TeamRedCountAfter > state.TeamRedCount {
				clubRedDelta[*p.clubID]++
				state.MatchRedCount++
			}
			state.YellowBalance = calc.YellowBalanceAfter
			state.TeamRedCount = calc.TeamRedCountAfter
			// Club delta is reapplied before the next calculation, avoiding a
			// double increment in the cached per-team state.
			state.ClubRedCount = calc.ClubRedCountAfter - clubRedDelta[*p.clubID]
			ledgerStates[*p.teamID] = state
		}
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_type,status,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id)
			VALUES($1,$2,'pending',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, decisionID, effect.EffectType, p.subjectType, p.subjectID, nullIfBlank(p.playerName), effect.AmountPence, points, effect.StartsAt, effect.EndsAt, nullIfBlank(effect.Trigger), mapJSON(publicDetails), mapJSON(privateDetails), countsForTotting, p.caseSubjectID); err != nil {
			return 0, err
		}
		afterEffects = append(afterEffects, map[string]any{"effect_type": effect.EffectType, "case_subject_id": p.caseSubjectID, "subject_type": p.subjectType, "subject_id": p.subjectID})
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='decision_proposed',public_summary=$2,private_summary=$3,proposed_by_admin_id=$4,current_revision=$5,closed_at=NULL,updated_at=now() WHERE id=$1`, req.CaseID, req.PublicReason, nullIfBlank(req.PrivateReason), *req.Actor.ID, nextRevision); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'decision_proposed','admin',$2,$3,$4,jsonb_build_object('public_summary',$5::text,'private_summary',$6::text),$7,$8)`, req.CaseID, *req.Actor.ID, req.Actor.Label, req.PublicReason, currentPublicSummary, currentPrivateSummary, mapJSON(map[string]any{"decision_revision_id": decisionID, "effects": afterEffects, "public_summary": req.PublicReason, "private_summary": req.PrivateReason}), req.Actor.RequestID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id) VALUES($1,'decision_owner_review_required','admin',$2,$3,'Case owner must review all audience emails before independent approval',jsonb_build_object('decision_revision_id',$4::bigint),$5)`, req.CaseID, *req.Actor.ID, req.Actor.Label, decisionID, req.Actor.RequestID); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return decisionID, nil
}

func validateDecisionEffectFields(effect DecisionEffectRequest) error {
	if effect.EndsAt != nil && effect.EffectType != "player_ban" && effect.EffectType != "team_ban" && effect.EffectType != "suspended_red" {
		return fmt.Errorf("%s cannot carry an end date; end dates are only for bans and suspended red cards", strings.ReplaceAll(effect.EffectType, "_", " "))
	}
	switch effect.EffectType {
	case "fine":
		if effect.AmountPence == nil || *effect.AmountPence <= 0 {
			return errors.New("fine requires an amount greater than zero")
		}
		if effect.Points != nil {
			return errors.New("fine cannot also carry a points value; add a separate points-adjustment effect")
		}
	case "points_adjustment":
		if effect.Points == nil || *effect.Points == 0 {
			return errors.New("league-points adjustment requires a non-zero points value")
		}
		if effect.AmountPence != nil {
			return errors.New("league-points adjustment cannot also carry a fine; add a separate fine effect")
		}
	default:
		if effect.AmountPence != nil {
			return fmt.Errorf("%s cannot carry a fine amount; add a separate fine effect", strings.ReplaceAll(effect.EffectType, "_", " "))
		}
		if effect.Points != nil {
			return fmt.Errorf("%s cannot carry an entered points value; card points are calculated by policy and league points require a separate points-adjustment effect", strings.ReplaceAll(effect.EffectType, "_", " "))
		}
	}
	return nil
}

// proposeDecisionLegacy is retained only to make historical migrations and
// older integration fixtures readable; new callers use ProposeDecisionBundle.
func (s *Service) proposeDecisionLegacy(ctx context.Context, req DecisionRequest) (int64, error) {
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

type ApprovalOptions struct {
	EmergencyReason      string
	AdditionalRecipients []string
}

const decisionApprovalRecipient = "cricketdirector@gtrmcrcricket.co.uk"

func decisionApprovalNotification(caseID, decisionID int64) (recipient, idempotencyKey, subject, body string) {
	return decisionApprovalRecipient,
		fmt.Sprintf("case:%d:decision-approval-request:%d", caseID, decisionID),
		"GMCL sanctions awaiting approval",
		"A sanctions decision is awaiting your approval. Please sign in to GMCL Admin, open the Awaiting decision queue, and review the proposed sanctions before approving or rejecting them."
}

// SubmitDecisionForApproval records the case owner's confirmation that they
// have reviewed the complete offending-club, reporting-club and official
// versions. Older proposals created before this checkpoint remain compatible.
func (s *Service) SubmitDecisionForApproval(ctx context.Context, caseID int64, actor Actor) error {
	if caseID < 1 || actor.ID == nil {
		return errors.New("case and authenticated case owner are required")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var assignedAdminID, proposerID *int32
	if err = tx.QueryRow(ctx, `SELECT status,assigned_admin_id,proposed_by_admin_id FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&status, &assignedAdminID, &proposerID); err != nil {
		return err
	}
	if status != "decision_proposed" || assignedAdminID == nil || proposerID == nil || *assignedAdminID != *actor.ID || *proposerID != *actor.ID {
		return errors.New("only the assigned case owner who prepared this decision can send it for approval")
	}
	var decisionID int64
	if err = tx.QueryRow(ctx, `SELECT id FROM sanction_decision_revisions WHERE case_id=$1 AND status='proposed' ORDER BY revision DESC,id DESC LIMIT 1`, caseID).Scan(&decisionID); err != nil {
		return err
	}
	var reviewRequired, alreadySent bool
	if err = tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM sanction_case_events WHERE case_id=$1 AND event_type='decision_owner_review_required' AND (metadata->>'decision_revision_id')::bigint=$2),
		EXISTS(SELECT 1 FROM sanction_case_events WHERE case_id=$1 AND event_type='decision_sent_for_approval' AND (metadata->>'decision_revision_id')::bigint=$2)`, caseID, decisionID).Scan(&reviewRequired, &alreadySent); err != nil {
		return err
	}
	if !reviewRequired {
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id) VALUES($1,'decision_owner_review_required','admin',$2,$3,'Existing proposed decision brought into the owner-review checkpoint before independent approval',jsonb_build_object('decision_revision_id',$4::bigint),$5)`, caseID, *actor.ID, actor.Label, decisionID, actor.RequestID); err != nil {
			return err
		}
	}
	if alreadySent {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id) VALUES($1,'decision_sent_for_approval','admin',$2,$3,'Case owner reviewed all three complete audience versions and sent the decision for independent approval',jsonb_build_object('decision_revision_id',$4::bigint),$5)`, caseID, *actor.ID, actor.Label, decisionID, actor.RequestID); err != nil {
		return err
	}
	recipient, idempotencyKey, subject, body := decisionApprovalNotification(caseID, decisionID)
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_notification_outbox(case_id,decision_revision_id,message_kind,idempotency_key,recipient,subject,body)
		VALUES($1,$2,'decision_approval_request',$3,$4,$5,$6) ON CONFLICT(idempotency_key) DO NOTHING`, caseID, decisionID, idempotencyKey, recipient, subject, body); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ApproveCase(ctx context.Context, caseID int64, approver Actor, emergencyReason string) error {
	return s.ApproveCaseWithOptions(ctx, caseID, approver, ApprovalOptions{EmergencyReason: emergencyReason})
}

func (s *Service) ApproveCaseWithOptions(ctx context.Context, caseID int64, approver Actor, options ApprovalOptions) error {
	emergencyReason := options.EmergencyReason
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
	if sourceType == "ineligible_player" {
		if err = EnsureIneligibleLinkedIntakesCurrent(ctx, tx, caseID); err != nil {
			return err
		}
	}
	emergency := strings.TrimSpace(emergencyReason) != ""
	if approver.ID == nil {
		if sourceType == "ineligible_player" {
			return errors.New("ineligible-player decisions always require an authorised human approver")
		}
		var mode string
		var clean int
		var sourceEnabled, globalEnabled bool
		_ = tx.QueryRow(ctx, `SELECT s.mode,s.clean_cycles,s.enabled,g.enabled FROM sanction_automation_settings s JOIN sanction_automation_settings g ON g.source_type='_global' WHERE s.source_type=$1`, sourceType).Scan(&mode, &clean, &sourceEnabled, &globalEnabled)
		if (approver.Type != "system" && approver.Type != "n8n") || mode != "automatic" || clean < 3 || !sourceEnabled || !globalEnabled {
			return errors.New("deterministic automatic approval is not enabled")
		}
	}
	if proposer != nil && approver.ID != nil && *proposer == *approver.ID && (sourceType == "ineligible_player" || !emergency) {
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
	var ownerReviewPending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM sanction_case_events required
		WHERE required.case_id=$1 AND required.event_type='decision_owner_review_required' AND (required.metadata->>'decision_revision_id')::bigint=$2
		  AND NOT EXISTS(SELECT 1 FROM sanction_case_events sent WHERE sent.case_id=required.case_id AND sent.event_type='decision_sent_for_approval' AND sent.metadata->>'decision_revision_id'=required.metadata->>'decision_revision_id')
	)`, caseID, proposedID).Scan(&ownerReviewPending); err != nil {
		return err
	}
	if ownerReviewPending {
		return errors.New("the case owner must review all three complete email versions and send the decision for approval first")
	}
	var approvedID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,approved_by_admin_id,correction_reason,emergency_override,outcome_subject,outcome_findings,appeal_instructions)
		SELECT case_id,$2,id,'approved',public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,$3,$4,$5,outcome_subject,outcome_findings,appeal_instructions FROM sanction_decision_revisions WHERE id=$1 RETURNING id`,
		proposedID, revision+1, actorID(approver), nullIfBlank(emergencyReason), emergency).Scan(&approvedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_key,supersedes_id,effect_type,status,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id)
		SELECT $2,effect_key,id,effect_type,CASE WHEN effect_type='suspended_red' OR COALESCE((private_details->>'rescindable')::boolean,FALSE) THEN 'suspended' WHEN effect_type='no_action' THEN 'cancelled' ELSE 'active' END,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id
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
		SELECT c.id,$2,t.id,t.club_id,c.season_id,c.match_date,
		       CASE WHEN e.effect_type='yellow_card' AND e.status='active' THEN 1 WHEN e.effect_type='red_card' AND COALESCE((e.private_details->>'consumed_yellows')::int,0)>0 THEN 1-(e.private_details->>'consumed_yellows')::int ELSE 0 END,
		       CASE WHEN e.effect_type='red_card' THEN 1 ELSE 0 END,
		       COALESCE(e.points,0),
		       CASE WHEN e.effect_type='red_card' AND COALESCE((e.private_details->>'consumed_yellows')::int,0)>0 THEN 'conversion' ELSE 'issue' END,
		       COALESCE(e.public_details->>'explanation','Approved card effect')
		FROM sanction_cases c
		JOIN sanction_effect_revisions e ON e.decision_revision_id=$2
		LEFT JOIN sanction_case_subjects cs ON cs.id=e.case_subject_id
		JOIN teams t ON t.id=COALESCE(cs.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END,c.team_id)
		WHERE c.id=$1 AND c.season_id IS NOT NULL
		  AND e.status='active' AND e.effect_type IN ('red_card','yellow_card')`, caseID, approvedID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,emergency_override,request_id,after_data)
		VALUES($1,'decision_approved',$2,$3,$4,$5,$6,$7,$8)`, caseID, approver.Type, actorID(approver), approver.Label, nullIfBlank(emergencyReason), emergency, approver.RequestID, mapJSON(map[string]any{"decision_revision_id": approvedID})); err != nil {
		return err
	}

	// Create operational tasks from approved effects.
	var hasLeaguePoints bool
	_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$1 AND effect_type='points_adjustment' AND COALESCE(points,0)<>0)`, approvedID).Scan(&hasLeaguePoints)
	var denverID *int32
	if hasLeaguePoints {
		var id int32
		if err = tx.QueryRow(ctx, `SELECT id FROM admin_users WHERE LOWER(username)='denverthornton' AND is_active ORDER BY id LIMIT 1`).Scan(&id); err != nil {
			return errors.New("active Denver administrator account is required before approving league-points work")
		}
		denverID = &id
	}
	if hasLeaguePoints {
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,assigned_admin_id,due_at,current_note)
			SELECT $1,'play_cricket_points',$3,now()+interval '2 days',
			       'Apply '||e.points||' league-table point adjustment in Play-Cricket for '||COALESCE(t.name,'mapped team '||e.subject_id::text)
			FROM sanction_effect_revisions e
			LEFT JOIN sanction_case_subjects cs ON cs.id=e.case_subject_id
			LEFT JOIN teams t ON t.id=COALESCE(cs.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END)
			WHERE e.decision_revision_id=$2 AND e.effect_type='points_adjustment' AND COALESCE(e.points,0)<>0`, caseID, approvedID, *denverID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,current_note)
		SELECT $1,'fine_recovery','Recover approved fine' WHERE EXISTS (SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND effect_type='fine')`, caseID, approvedID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,current_note)
		SELECT $1,'board_intervention','Club reached the configured red-card review threshold' WHERE EXISTS (SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND COALESCE((public_details->>'create_board_review_task')::boolean,FALSE))`, caseID, approvedID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,due_at,current_note)
		SELECT $1,'suspended_review',ends_at,'Review suspended or rescindable sanction' FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND status='suspended'`, caseID, approvedID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,due_at,current_note)
		SELECT $1,'ban_expiry',ends_at,'Review ban expiry' FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND effect_type IN ('player_ban','team_ban') AND ends_at IS NOT NULL`, caseID, approvedID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_tasks(case_id,task_type,due_at,current_note)
		SELECT $1,'appeal_deadline',appeal_due_at,'Monitor appeal deadline' FROM sanction_decision_revisions WHERE id=$2 AND appeal_due_at IS NOT NULL`, caseID, approvedID)
	if err != nil {
		return err
	}

	// Maintain the old sanctions table as a temporary compatibility projection.
	// One row is projected per card effect; effect_key prevents duplicates.
	var seasonID, weekID, teamID, clubID *int32
	var matchDate *time.Time
	_ = tx.QueryRow(ctx, `SELECT season_id,week_id,team_id,club_id,match_date FROM sanction_cases WHERE id=$1`, caseID).Scan(&seasonID, &weekID, &teamID, &clubID, &matchDate)
	if seasonID != nil && weekID != nil {
		_, err = tx.Exec(ctx, `INSERT INTO sanctions(season_id,week_id,team_id,club_id,colour,reason,notes,points_deduction,status,issued_by_admin_id,offence_date,rule_reference,case_id,effect_key)
			SELECT $3,$4,t.id,t.club_id,CASE WHEN e.effect_type='yellow_card' THEN 'yellow'::sanction_colour ELSE 'red'::sanction_colour END,'case_management',$5,e.points,'active',$6,$7,$8,$1,e.effect_key
			FROM sanction_effect_revisions e
			LEFT JOIN sanction_case_subjects cs ON cs.id=e.case_subject_id
			JOIN sanction_cases c ON c.id=$1
			JOIN teams t ON t.id=COALESCE(cs.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END,c.team_id)
			WHERE e.decision_revision_id=$2 AND e.effect_type IN ('yellow_card','red_card') AND e.status='active'
			ON CONFLICT(effect_key) WHERE effect_key IS NOT NULL DO NOTHING`, caseID, approvedID, *seasonID, *weekID, publicReason, actorID(approver), matchDate, ruleRef)
		if err != nil {
			return err
		}
	}
	_ = teamID
	_ = clubID
	if err = lockApprovedOutcomeCorrespondence(ctx, tx, caseID, approvedID, approver, options.AdditionalRecipients); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type approvedOutcomeEffect struct {
	typeName    string
	subjectType string
	playerName  string
	teamName    string
	amount      *int64
	points      *int
	startsAt    *time.Time
	endsAt      *time.Time
}

type outcomeRenderData struct {
	reference, sourceType, offendingClub, offendingTeam, reportingClub string
	subject, offenceDate, findings, appeal, rule, effectSummary        string
	signatoryName                                                      string
	combined, noAction                                                 bool
}

type renderedOutcomes struct {
	subject, offending, reporting, official string
}

type reportingOutcomeClub struct {
	id   int32
	name string
}

type reportingOutcomeClubQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadReportingOutcomeClubs(ctx context.Context, queryer reportingOutcomeClubQueryer, caseID int64) ([]reportingOutcomeClub, error) {
	rows, err := queryer.Query(ctx, `
		WITH current_resolution AS (
			SELECT DISTINCT ON (resolution.case_id,resolution.intake_id)
			       resolution.case_id,resolution.intake_id,resolution.reporting_club_id,resolution.league_origin
			FROM sanction_case_intake_merge_resolutions resolution
			JOIN sanction_intake_case_links link
			  ON link.case_id=resolution.case_id AND link.intake_id=resolution.intake_id
			 AND link.relationship=resolution.relationship
			WHERE resolution.case_id=$1
			ORDER BY resolution.case_id,resolution.intake_id,resolution.id DESC
		)
		SELECT club.id,club.name
		FROM clubs club
		WHERE club.id IN (
			SELECT current_resolution.reporting_club_id FROM current_resolution
			WHERE NOT current_resolution.league_origin AND current_resolution.reporting_club_id IS NOT NULL
			UNION
			SELECT legacy.reporting_club_id FROM sanction_cases legacy
			WHERE legacy.id=$1 AND legacy.reporting_club_id IS NOT NULL
			  AND NOT EXISTS(SELECT 1 FROM current_resolution)
		)
		ORDER BY club.name,club.id
	`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var clubs []reportingOutcomeClub
	for rows.Next() {
		var club reportingOutcomeClub
		if err = rows.Scan(&club.id, &club.name); err != nil {
			return nil, err
		}
		clubs = append(clubs, club)
	}
	return clubs, rows.Err()
}

func renderOutcomeCommunications(data outcomeRenderData) renderedOutcomes {
	if strings.TrimSpace(data.offenceDate) == "" {
		data.offenceDate = "Not recorded"
	}
	if strings.TrimSpace(data.rule) == "" {
		data.rule = "No specific rule reference recorded."
	}
	if strings.TrimSpace(data.appeal) == "" {
		data.appeal = "Any appeal must follow the current GMCL regulations."
	}
	if strings.TrimSpace(data.subject) == "" {
		data.subject = "GMCL case outcome " + data.reference
	}
	if data.noAction {
		data.subject = "GMCL no-action outcome " + data.reference
	}
	team := outcomeTeamLabel(data.offendingClub, data.offendingTeam)
	signatory := defaultOutcomeValue(data.signatoryName, "Greater Manchester Cricket League")
	offending := fmt.Sprintf("Dear Club Official,\n\nThe League officials have approved the decision for case %s, which is set out below.\n\nOffence date:\n%s\n\nOffending team:\n%s\n\nFindings:\n%s\n\nRule determination:\n%s\n\nDecision and sanctions:\n%s\n\nAppeal instructions:\n%s\n\nRegards,\n\n%s\n\nGMCL Disciplinary Officer", data.reference, data.offenceDate, team, data.findings, data.rule, data.effectSummary, data.appeal, signatory)
	if data.combined {
		offending = strings.Replace(offending, "Dear Club Official,", "Dear Club Official,\n\nThis notice serves as the combined offending-club and reporting-club outcome for your club.", 1)
	}
	reporting := fmt.Sprintf("Dear Club Secretary,\n\nThe GMCL has completed its investigation into the ineligible-player report recorded as case %s.\n\nOffence date:\n%s\n\nConfirmed findings:\n%s\n\nRule determination:\n%s\n\nFinal outcome:\n%s\n\nRegards,\nGreater Manchester Cricket League", data.reference, data.offenceDate, data.findings, data.rule, data.effectSummary)
	official := fmt.Sprintf("Approved league outcome record\n\nCase: %s\nSource: %s\nOffence date: %s\nOffending club: %s\nOffending team: %s\nReporting club: %s\n\nFindings:\n%s\n\nRule determination:\n%s\n\nDecision and sanctions:\n%s\n\nAppeal instructions:\n%s", data.reference, data.sourceType, data.offenceDate, defaultOutcomeValue(data.offendingClub, "Unresolved"), team, defaultOutcomeValue(data.reportingClub, "League-origin / none"), data.findings, data.rule, data.effectSummary, data.appeal)
	return renderedOutcomes{subject: data.subject, offending: offending, reporting: reporting, official: official}
}

func formatOutcomeOffenceDate(matchDate *time.Time) string {
	if matchDate == nil {
		return "Not recorded"
	}
	return matchDate.Format("2 January 2006")
}

func outcomeTeamLabel(club, team string) string {
	club = strings.TrimSpace(club)
	team = strings.TrimSpace(team)
	if club != "" && team != "" {
		displayClub := strings.TrimSuffix(club, " Cricket Club")
		displayClub = strings.TrimSuffix(displayClub, " CC")
		return strings.TrimSpace(displayClub) + " " + team
	}
	return defaultOutcomeValue(club+team, "Unresolved")
}

type outcomeSignatoryQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadOutcomeSignatoryName(ctx context.Context, q outcomeSignatoryQueryer, approverID *int32, fallback string) string {
	var name string
	if approverID != nil {
		if q.QueryRow(ctx, `SELECT COALESCE(NULLIF(trim(directory.name),''),NULLIF(trim(admin.username),''),'')
			FROM admin_users admin LEFT JOIN sanction_recipient_directory directory
			ON lower(directory.email)=lower(admin.email) AND directory.recipient_role='discipline' AND directory.active
			WHERE admin.id=$1`, *approverID).Scan(&name) == nil && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if q.QueryRow(ctx, `SELECT name FROM sanction_recipient_directory WHERE recipient_role='discipline' AND active ORDER BY id LIMIT 1`).Scan(&name) == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return strings.TrimSpace(fallback)
}

func lockApprovedOutcomeCorrespondence(ctx context.Context, tx pgx.Tx, caseID, decisionID int64, approver Actor, additionalRecipients []string) error {
	var reference, sourceType, subject, findings, appeal, publicReason, casePublicSummary string
	var ruleReference *string
	var offendingClubID, reportingClubID *int32
	var offendingClub, offendingTeam, reportingClub string
	var reporterName, reporterEmail, reporterRole, reporterPhone string
	var proposerID *int32
	var proposedDraftDecisionID *int64
	var documentDate time.Time
	var matchDate *time.Time
	var leagueOrigin bool
	if err := tx.QueryRow(ctx, `SELECT c.reference,c.source_type,c.club_id,COALESCE(off.name,''),COALESCE(team.name,''),c.reporting_club_id,COALESCE(rep.name,''),
		COALESCE(d.outcome_subject,'GMCL case outcome '||c.reference),c.match_date,COALESCE(d.outcome_findings,d.public_reason),COALESCE(d.appeal_instructions,''),d.public_reason,d.rule_reference,d.proposed_by_admin_id,d.supersedes_id,
		COALESCE(c.reporter_name,''),COALESCE(c.reporter_email,''),COALESCE(c.reporter_role,''),COALESCE(c.reporter_phone,''),
		COALESCE(c.public_summary,''),
		COALESCE((SELECT effective_at FROM sanction_decision_revisions proposed WHERE proposed.id=d.supersedes_id),d.effective_at),
		EXISTS(SELECT 1 FROM sanction_case_parties party WHERE party.case_id=c.id AND party.party_type='league' AND party.relationship='league' AND party.name='GMCL Official')
		FROM sanction_cases c JOIN sanction_decision_revisions d ON d.id=$2
		LEFT JOIN clubs off ON off.id=c.club_id LEFT JOIN teams team ON team.id=c.team_id LEFT JOIN clubs rep ON rep.id=c.reporting_club_id WHERE c.id=$1`, caseID, decisionID).
		Scan(&reference, &sourceType, &offendingClubID, &offendingClub, &offendingTeam, &reportingClubID, &reportingClub, &subject, &matchDate, &findings, &appeal, &publicReason, &ruleReference, &proposerID, &proposedDraftDecisionID, &reporterName, &reporterEmail, &reporterRole, &reporterPhone, &casePublicSummary, &documentDate, &leagueOrigin); err != nil {
		return err
	}
	if proposedDraftDecisionID == nil {
		return errors.New("approved decision is not linked to the proposed decision whose drafts must be reviewed")
	}
	reportingClubs, err := loadReportingOutcomeClubs(ctx, tx, caseID)
	if err != nil {
		return err
	}
	reportingClubNames := make([]string, 0, len(reportingClubs))
	for _, club := range reportingClubs {
		reportingClubNames = append(reportingClubNames, club.name)
	}
	reportingClub = strings.Join(reportingClubNames, ", ")
	if sourceType == "ineligible_player" && len(reportingClubs) == 0 && !leagueOrigin {
		return errors.New("reporting club is unmapped; only an explicit GMCL Official intake may omit the reporting club")
	}
	approvalPrivacyValues, err := CaseReporterIdentityValues(ctx, tx, caseID)
	if err != nil {
		return err
	}
	reportingAliases, err := CaseReportingClubIdentityValues(ctx, tx, caseID, offendingClubID)
	if err != nil {
		return err
	}
	approvalPrivacyValues = append(approvalPrivacyValues, reportingAliases...)
	if sourceType == "ineligible_player" && ContainsPrivateIdentity(strings.Join([]string{casePublicSummary, subject, findings, publicReason, appeal}, "\n"), approvalPrivacyValues...) {
		return errors.New("approved public outcome wording contains reporter or reporting-club identity; redact it before approval")
	}

	rows, err := tx.Query(ctx, `SELECT e.effect_type,e.subject_type,COALESCE(e.player_name,cs.player_name,''),COALESCE(t.name,''),e.amount_pence,e.points,e.starts_at,e.ends_at
		FROM sanction_effect_revisions e
		LEFT JOIN sanction_case_subjects cs ON cs.id=e.case_subject_id
		LEFT JOIN teams t ON t.id=COALESCE(cs.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END)
		WHERE e.decision_revision_id=$1 ORDER BY e.id`, decisionID)
	if err != nil {
		return err
	}
	var effects []approvedOutcomeEffect
	hasFine := false
	hasAnyPoints := false
	for rows.Next() {
		var effect approvedOutcomeEffect
		if err = rows.Scan(&effect.typeName, &effect.subjectType, &effect.playerName, &effect.teamName, &effect.amount, &effect.points, &effect.startsAt, &effect.endsAt); err != nil {
			rows.Close()
			return err
		}
		hasFine = hasFine || effect.typeName == "fine"
		hasAnyPoints = hasAnyPoints || (effect.points != nil && *effect.points != 0)
		effects = append(effects, effect)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	noAction := len(effects) > 0
	for _, effect := range effects {
		if effect.typeName != "no_action" {
			noAction = false
			break
		}
	}
	rule := "No specific rule reference recorded."
	if ruleReference != nil && strings.TrimSpace(*ruleReference) != "" {
		rule = strings.TrimSpace(*ruleReference)
	}
	combined := false
	if offendingClubID != nil {
		for _, club := range reportingClubs {
			if club.id == *offendingClubID {
				combined = true
				break
			}
		}
	}
	rendered := renderOutcomeCommunications(outcomeRenderData{reference: reference, sourceType: sourceType, offendingClub: offendingClub, offendingTeam: offendingTeam, reportingClub: reportingClub, subject: subject, offenceDate: formatOutcomeOffenceDate(matchDate), findings: findings, appeal: appeal, rule: rule, effectSummary: approvedEffectSummary(effects), signatoryName: loadOutcomeSignatoryName(ctx, tx, approver.ID, approver.Label), combined: combined, noAction: noAction})
	if ContainsPrivateIdentity(rendered.subject+"\n"+rendered.offending, approvalPrivacyValues...) {
		return errors.New("offending-club outcome contains reporter or reporting-club identity; redact it before approval")
	}
	subject = rendered.subject

	type snapshot struct {
		kind, audience, subject, body string
		recipients                    []string
	}
	contactForClub := func(clubID *int32) ([]string, error) {
		if clubID == nil {
			return nil, nil
		}
		var address string
		if tx.QueryRow(ctx, `SELECT email FROM sanction_club_contacts WHERE club_id=$1 AND contact_type='official_mailbox' AND active AND verified_at IS NOT NULL ORDER BY verified_at DESC,id DESC LIMIT 1`, *clubID).Scan(&address) != nil {
			return nil, nil
		}
		canonical, parseErr := canonicalOutcomeRecipient(address)
		if parseErr != nil {
			return nil, fmt.Errorf("club %d has an invalid verified official mailbox: %w", *clubID, parseErr)
		}
		return []string{canonical}, nil
	}
	kindOffending := "outcome_offending_club"
	if noAction {
		kindOffending = "no_action_outcome"
	}
	offendingRecipients, err := contactForClub(offendingClubID)
	if err != nil {
		return err
	}
	if len(offendingRecipients) == 0 {
		return errors.New("offending club official mailbox must be verified before approval")
	}
	offendingRecipientSeen := make(map[string]bool, len(offendingRecipients)+1)
	for _, recipient := range offendingRecipients {
		offendingRecipientSeen[recipient] = true
	}
	offendingRecipients, err = appendUniqueOutcomeRecipient(offendingRecipients, offendingRecipientSeen, PlayCricketHelpCopyRecipient)
	if err != nil {
		return fmt.Errorf("Play-Cricket Help copy recipient is invalid: %w", err)
	}
	snapshots := []snapshot{{kind: kindOffending, audience: "offending_club", recipients: offendingRecipients}}
	var reportingRecipients []string
	reportingRecipientSeen := map[string]bool{}
	if strings.TrimSpace(reporterEmail) != "" {
		reportingRecipients, err = appendUniqueOutcomeRecipient(reportingRecipients, reportingRecipientSeen, reporterEmail)
		if err != nil {
			return fmt.Errorf("reporter email is invalid: %w", err)
		}
	}
	for _, club := range reportingClubs {
		if offendingClubID != nil && club.id == *offendingClubID {
			continue
		}
		clubID := club.id
		clubRecipients, contactErr := contactForClub(&clubID)
		if contactErr != nil {
			return contactErr
		}
		if len(clubRecipients) == 0 {
			return fmt.Errorf("reporting club %s official mailbox must be verified before approval", club.name)
		}
		for _, recipient := range clubRecipients {
			if !reportingRecipientSeen[recipient] {
				reportingRecipientSeen[recipient] = true
				reportingRecipients = append(reportingRecipients, recipient)
			}
		}
	}
	if len(reportingRecipients) > 0 {
		kind := "outcome_reporting_club"
		if noAction {
			kind = "no_action_outcome"
		}
		snapshots = append(snapshots, snapshot{kind: kind, audience: "reporting_club", recipients: reportingRecipients})
	}
	var officialRecipients []string
	officialRoles := map[string]bool{}
	officialRows, err := tx.Query(ctx, `SELECT recipient_role,lower(email) FROM sanction_recipient_directory WHERE active AND (
		recipient_role IN ('executive','discipline')
		OR (recipient_role='play_cricket' AND EXISTS(SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$1 AND COALESCE(points,0)<>0))
		OR (recipient_role='finance' AND EXISTS(SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$1 AND effect_type='fine'))
	) ORDER BY recipient_role,lower(email)`, decisionID)
	if err != nil {
		return err
	}
	for officialRows.Next() {
		var role, address string
		if officialRows.Scan(&role, &address) == nil {
			canonical, parseErr := canonicalOutcomeRecipient(address)
			if parseErr != nil {
				officialRows.Close()
				return fmt.Errorf("%s recipient has an invalid address: %w", role, parseErr)
			}
			officialRecipients = append(officialRecipients, canonical)
			officialRoles[role] = true
		}
	}
	if err = officialRows.Err(); err != nil {
		officialRows.Close()
		return err
	}
	officialRows.Close()
	for _, requiredRole := range []string{"executive", "discipline"} {
		if !officialRoles[requiredRole] {
			return fmt.Errorf("an active %s recipient is required before outcome approval", requiredRole)
		}
	}
	if hasAnyPoints && !officialRoles["play_cricket"] {
		return errors.New("an active Play-Cricket recipient is required for a card or points outcome")
	}
	if hasFine && !officialRoles["finance"] {
		return errors.New("an active finance recipient is required for a fine outcome")
	}
	officialRecipientSeen := make(map[string]bool, len(officialRecipients)+len(additionalRecipients))
	for _, recipient := range officialRecipients {
		officialRecipientSeen[recipient] = true
	}
	for _, recipient := range additionalRecipients {
		officialRecipients, err = appendUniqueOutcomeRecipient(officialRecipients, officialRecipientSeen, recipient)
		if err != nil {
			return fmt.Errorf("additional outcome recipient is invalid: %w", err)
		}
	}
	kindOfficial := "outcome_official"
	if noAction {
		kindOfficial = "no_action_outcome"
	}
	snapshots = append(snapshots, snapshot{kind: kindOfficial, audience: "official", recipients: officialRecipients})

	for _, item := range snapshots {
		var draftID *int64
		expectedBody := map[string]string{
			"offending_club": rendered.offending,
			"reporting_club": rendered.reporting,
			"official":       rendered.official,
		}[item.audience]
		var draftCreatorID *int32
		if err = tx.QueryRow(ctx, `SELECT id,subject,body,created_by_admin_id FROM sanction_correspondence_revisions
			WHERE case_id=$1 AND decision_revision_id=$2 AND message_kind=$3 AND audience=$4 AND status='draft'
			ORDER BY revision DESC,id DESC LIMIT 1`, caseID, *proposedDraftDecisionID, item.kind, item.audience).
			Scan(&draftID, &item.subject, &item.body, &draftCreatorID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				item.subject = rendered.subject
				item.body = expectedBody
				draftCreatorID = approver.ID
				draftID = nil
			} else {
				return err
			}
		}
		if err = validateOutcomeDraftCompleteness(item.audience, item.body); err != nil {
			return fmt.Errorf("saved %s draft is incomplete: %w", strings.ReplaceAll(item.audience, "_", " "), err)
		}
		if !outcomeDraftMatchesGenerated(item.subject, item.body, rendered.subject, expectedBody) {
			return fmt.Errorf("saved %s draft no longer equals the deterministic audience-safe outcome; save the generated replacement before approval", strings.ReplaceAll(item.audience, "_", " "))
		}
		privacyValues, privacyErr := CaseReporterIdentityValues(ctx, tx, caseID)
		if privacyErr != nil {
			return privacyErr
		}
		if item.audience == "offending_club" || item.audience == "reporting_club" {
			var excluded *int32
			if item.audience == "offending_club" {
				excluded = offendingClubID
			}
			aliases, aliasErr := CaseReportingClubIdentityValues(ctx, tx, caseID, excluded)
			if aliasErr != nil {
				return aliasErr
			}
			privacyValues = append(privacyValues, aliases...)
		}
		if ContainsPrivateIdentity(item.subject+"\n"+item.body, privacyValues...) {
			return fmt.Errorf("the saved %s draft contains audience-private identity information", strings.ReplaceAll(item.audience, "_", " "))
		}
		if item.audience == "reporting_club" {
			restricted, restrictedErr := CaseReportingOutcomeRestrictedValues(ctx, tx, caseID)
			if restrictedErr != nil {
				return restrictedErr
			}
			if ContainsRestrictedContent(item.subject+"\n"+item.body, restricted...) {
				return errors.New("the saved reporting-club draft contains an offending-club response, internal note, private rationale, or private evidence label")
			}
		}
		pdf := BuildOutcomeLetterPDF(OutcomeLetter{Reference: reference, Audience: item.audience, Subject: item.subject, Body: item.body, ApprovedAt: documentDate})
		sum := fmt.Sprintf("%x", sha256.Sum256(pdf))
		storageKey := fmt.Sprintf("sanction-outcomes/%s/%s-r1.pdf", reference, item.audience)
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_correspondence_revisions(case_id,decision_revision_id,supersedes_id,message_kind,audience,revision,status,recipients,subject,body,attachment_manifest,pdf_storage_key,pdf_sha256,pdf_bytes,created_by_admin_id,approved_by_admin_id)
			VALUES($1,$2,$14,
			 $3,$4,(SELECT COALESCE(MAX(prior.revision),0)+1 FROM sanction_correspondence_revisions prior WHERE prior.case_id=$1 AND prior.message_kind=$3 AND prior.audience=$4),
			 'approved',$5,$6,$7,$8,$9,$10,$11,$12,$13)`, caseID, decisionID, item.kind, item.audience, mapJSON(item.recipients), item.subject, item.body,
			mapJSON([]map[string]any{{"filename": reference + "-outcome.pdf", "content_type": "application/pdf", "sha256": sum}}), storageKey, sum, pdf, draftCreatorID, actorID(approver), draftID); err != nil {
			return err
		}
	}
	_ = publicReason
	return nil
}

func approvedEffectSummary(effects []approvedOutcomeEffect) string {
	if len(effects) == 0 {
		return "No action."
	}
	lines := make([]string, 0, len(effects))
	for _, effect := range effects {
		label := strings.ReplaceAll(effect.typeName, "_", " ")
		label = strings.ToUpper(label[:1]) + label[1:]
		subject := outcomeEffectSubject(effect)
		if subject != "" {
			label += " - " + subject
		}
		if effect.amount != nil {
			label += fmt.Sprintf(" (fine: GBP %.2f)", float64(*effect.amount)/100)
		}
		if effect.points != nil {
			pointsKind := "league-table points"
			if effect.typeName == "yellow_card" || effect.typeName == "red_card" || effect.typeName == "card_points" {
				pointsKind = "card-system points"
			}
			label += fmt.Sprintf(" (%d %s)", *effect.points, pointsKind)
		}
		showDates := effect.endsAt != nil && (effect.typeName == "player_ban" || effect.typeName == "team_ban" || effect.typeName == "suspended_red")
		if showDates && effect.startsAt != nil {
			label += "; effective " + effect.startsAt.Format("2 January 2006")
		}
		if showDates && effect.endsAt != nil {
			label += " to " + effect.endsAt.Format("2 January 2006")
		}
		lines = append(lines, "- "+label)
	}
	return strings.Join(lines, "\n")
}

func outcomeEffectSubject(effect approvedOutcomeEffect) string {
	teamFirst := effect.typeName == "team_ban" || effect.typeName == "points_adjustment" || effect.subjectType == "team" && effect.typeName != "yellow_card" && effect.typeName != "red_card" && effect.typeName != "suspended_red"
	if teamFirst {
		if team := strings.TrimSpace(effect.teamName); team != "" {
			return team
		}
	}
	if player := strings.TrimSpace(effect.playerName); player != "" {
		return player
	}
	return strings.TrimSpace(effect.teamName)
}

func appendUniqueOutcomeRecipient(recipients []string, seen map[string]bool, value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return recipients, nil
	}
	canonical, err := canonicalOutcomeRecipient(value)
	if err != nil {
		return recipients, err
	}
	if !seen[canonical] {
		seen[canonical] = true
		recipients = append(recipients, canonical)
	}
	return recipients, nil
}

func canonicalOutcomeRecipient(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil || parsed.Address == "" || !strings.EqualFold(parsed.Address, trimmed) {
		return "", errors.New("must be a single plain email address")
	}
	return strings.ToLower(parsed.Address), nil
}

func outcomeContainsPrivateIdentity(body string, values ...string) bool {
	canonicalBody := strings.ToLower(strings.Join(strings.Fields(body), " "))
	compactBody := compactIdentityText(body)
	for _, value := range values {
		canonicalValue := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
		if len([]rune(compactIdentityText(canonicalValue))) >= 4 && containsIdentityPhrase(canonicalBody, canonicalValue) {
			return true
		}
		compactValue := compactIdentityText(value)
		if len([]rune(compactValue)) >= 5 && strings.Contains(compactBody, compactValue) {
			return true
		}
	}
	return false
}

func containsIdentityPhrase(body, value string) bool {
	start := 0
	for {
		index := strings.Index(body[start:], value)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !isIdentityWordByte(body[index-1])
		after := index + len(value)
		afterOK := after == len(body) || !isIdentityWordByte(body[after])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
	}
}

func isIdentityWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value >= 0x80
}

// ContainsPrivateIdentity checks disclosure-safe text against both separate
// reporter fields and the legacy Google Form's combined name/role value.
func ContainsPrivateIdentity(body string, values ...string) bool {
	if outcomeContainsPrivateIdentity(body, privateIdentityVariants(values...)...) {
		return true
	}
	return containsReporterRoleDisclosure(body, values...)
}

func compactIdentityText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

// privateIdentityVariants covers the combined "name and role" value used by
// the legacy Google Form and the separate fields used by the native form.
func privateIdentityVariants(values ...string) []string {
	seen := map[string]bool{}
	variants := make([]string, 0, len(values)*2)
	add := func(value string) {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if len([]rune(compactIdentityText(value))) < 4 || seen[key] {
			return
		}
		seen[key] = true
		variants = append(variants, value)
	}
	for _, value := range values {
		if !isGenericReporterRole(value) {
			add(value)
		}
		parts := []string{value}
		for _, separator := range []string{" — ", " – ", " - ", " | ", " / ", "/", "(", ")", ",", ";"} {
			var split []string
			for _, part := range parts {
				split = append(split, strings.Split(part, separator)...)
			}
			parts = split
		}
		if len(parts) > 1 {
			for _, part := range parts {
				// A common role word is not identifying by itself and frequently
				// appears in legitimate allegation/outcome text (for example,
				// "player" or "club secretary"). The complete combined value and
				// the person's name remain protected; specific role descriptions
				// such as "Club Welfare Officer" are still checked.
				if isGenericReporterRole(part) {
					continue
				}
				add(part)
			}
		}
		lowerValue := strings.ToLower(strings.TrimSpace(value))
		for role := range genericReporterRoles {
			if prefix, found := strings.CutSuffix(lowerValue, "-"+role); found {
				add(strings.TrimSpace(value[:len(prefix)]))
			}
		}
	}
	return variants
}

var genericReporterRoles = map[string]bool{
	"admin": true, "administrator": true,
	"captain": true, "club captain": true, "team captain": true,
	"chair": true, "chairman": true, "chairperson": true,
	"coach": true, "director": true, "manager": true,
	"member": true, "committee": true, "committee member": true,
	"official": true, "club official": true, "league official": true,
	"player": true, "parent": true, "scorer": true, "supporter": true,
	"secretary": true, "club secretary": true,
	"treasurer": true, "umpire": true, "league": true,
}

func isGenericReporterRole(value string) bool {
	value = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	return genericReporterRoles[value]
}

func containsReporterRoleDisclosure(body string, values ...string) bool {
	roles := map[string]bool{}
	for _, value := range values {
		canonical := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
		if genericReporterRoles[canonical] {
			roles[canonical] = true
		}
		words := strings.FieldsFunc(canonical, func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r))
		})
		for start := range words {
			for size := 1; size <= 3 && start+size <= len(words); size++ {
				candidate := strings.Join(words[start:start+size], " ")
				if genericReporterRoles[candidate] {
					roles[candidate] = true
				}
			}
		}
	}
	canonicalBody := strings.Join(strings.FieldsFunc(strings.ToLower(body), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}), " ")
	for role := range roles {
		for _, pattern := range []string{
			"reporter was " + role,
			"reporter was the " + role,
			"reporter is " + role,
			"reporter is the " + role,
			"reporter role was " + role,
			"reported by " + role,
			"reported by the " + role,
			role + " made the report",
			role + " submitted the report",
			role + " lodged the report",
			role + " lodged the complaint",
			role + " raised the complaint",
			role + " raised the allegation",
			role + " made the complaint",
			role + " made the allegation",
			role + " reported the matter",
			role + " reported the incident",
			"report was lodged by " + role,
			"report was lodged by the " + role,
			"complaint was lodged by " + role,
			"complaint was lodged by the " + role,
			"complaint was raised by " + role,
			"complaint was raised by the " + role,
			"allegation was made by " + role,
			"allegation was made by the " + role,
			"allegation was raised by " + role,
			"allegation was raised by the " + role,
		} {
			if containsIdentityPhrase(canonicalBody, pattern) {
				return true
			}
		}
	}
	return false
}

func defaultOutcomeValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// PreviewOutcomeLetter returns the locked PDF after approval, or a
// deterministic preview of the proposed audience-safe wording beforehand.
func (s *Service) PreviewOutcomeLetter(ctx context.Context, caseID int64, audience string) ([]byte, string, error) {
	if audience != "offending_club" && audience != "reporting_club" && audience != "official" {
		return nil, "", errors.New("invalid outcome audience")
	}
	var locked []byte
	var lockedKey *string
	err := s.DB.QueryRow(ctx, `SELECT correspondence.pdf_bytes,correspondence.pdf_storage_key
		FROM sanction_correspondence_revisions correspondence
		JOIN sanction_decision_revisions decision ON decision.id=correspondence.decision_revision_id
		WHERE correspondence.case_id=$1 AND correspondence.audience=$2 AND correspondence.status='approved' AND correspondence.pdf_bytes IS NOT NULL
		  AND decision.id=(SELECT current_decision.id FROM sanction_decision_revisions current_decision WHERE current_decision.case_id=$1 ORDER BY current_decision.revision DESC,current_decision.id DESC LIMIT 1)
		  AND decision.status='approved'
		ORDER BY correspondence.revision DESC LIMIT 1`, caseID, audience).Scan(&locked, &lockedKey)
	if err == nil && len(locked) > 0 {
		filename := fmt.Sprintf("GMCL-case-%d-%s-outcome.pdf", caseID, audience)
		if lockedKey != nil && strings.TrimSpace(*lockedKey) != "" {
			parts := strings.Split(strings.ReplaceAll(*lockedKey, "\\", "/"), "/")
			filename = parts[len(parts)-1]
		}
		return locked, filename, nil
	}

	var decisionID int64
	var reference, sourceType, offendingClub, offendingTeam, reportingClub, subject, findings, appeal string
	var offendingClubID, reportingClubID *int32
	var approvedByAdminID *int32
	var rule *string
	var documentDate time.Time
	var matchDate *time.Time
	if err = s.DB.QueryRow(ctx, `SELECT d.id,c.reference,c.source_type,c.club_id,COALESCE(off.name,''),COALESCE(team.name,''),c.reporting_club_id,COALESCE(rep.name,''),COALESCE(d.outcome_subject,'GMCL case outcome '||c.reference),COALESCE(d.outcome_findings,d.public_reason),COALESCE(d.appeal_instructions,''),d.rule_reference,d.approved_by_admin_id,
		c.match_date,
		COALESCE((SELECT effective_at FROM sanction_decision_revisions proposed WHERE proposed.id=d.supersedes_id),d.effective_at)
		FROM sanction_cases c JOIN sanction_decision_revisions d ON d.case_id=c.id
		LEFT JOIN clubs off ON off.id=c.club_id LEFT JOIN teams team ON team.id=c.team_id LEFT JOIN clubs rep ON rep.id=c.reporting_club_id
		WHERE c.id=$1 AND d.id=(SELECT current_decision.id FROM sanction_decision_revisions current_decision
			WHERE current_decision.case_id=c.id ORDER BY current_decision.revision DESC,current_decision.id DESC LIMIT 1)
		  AND d.status IN ('proposed','approved')`, caseID).
		Scan(&decisionID, &reference, &sourceType, &offendingClubID, &offendingClub, &offendingTeam, &reportingClubID, &reportingClub, &subject, &findings, &appeal, &rule, &approvedByAdminID, &matchDate, &documentDate); err != nil {
		return nil, "", err
	}
	var savedSubject, savedBody string
	var savedDraft bool
	if draftErr := s.DB.QueryRow(ctx, `SELECT subject,body FROM sanction_correspondence_revisions
		WHERE case_id=$1 AND decision_revision_id=$2 AND audience=$3 AND status='draft'
		ORDER BY revision DESC,id DESC LIMIT 1`, caseID, decisionID, audience).Scan(&savedSubject, &savedBody); draftErr == nil {
		savedDraft = true
	} else if !errors.Is(draftErr, pgx.ErrNoRows) {
		return nil, "", draftErr
	}
	rows, err := s.DB.Query(ctx, `SELECT e.effect_type,e.subject_type,COALESCE(e.player_name,cs.player_name,''),COALESCE(t.name,''),e.amount_pence,e.points,e.starts_at,e.ends_at
		FROM sanction_effect_revisions e LEFT JOIN sanction_case_subjects cs ON cs.id=e.case_subject_id
		LEFT JOIN teams t ON t.id=COALESCE(cs.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END)
		WHERE e.decision_revision_id=$1 ORDER BY e.id`, decisionID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var effects []approvedOutcomeEffect
	for rows.Next() {
		var effect approvedOutcomeEffect
		if err = rows.Scan(&effect.typeName, &effect.subjectType, &effect.playerName, &effect.teamName, &effect.amount, &effect.points, &effect.startsAt, &effect.endsAt); err != nil {
			return nil, "", err
		}
		effects = append(effects, effect)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	rows.Close()
	reportingClubs, err := loadReportingOutcomeClubs(ctx, s.DB, caseID)
	if err != nil {
		return nil, "", err
	}
	reportingClubNames := make([]string, 0, len(reportingClubs))
	combined := false
	for _, club := range reportingClubs {
		reportingClubNames = append(reportingClubNames, club.name)
		if offendingClubID != nil && club.id == *offendingClubID {
			combined = true
		}
	}
	reportingClub = strings.Join(reportingClubNames, ", ")
	ruleText := "No specific rule reference recorded."
	if rule != nil && strings.TrimSpace(*rule) != "" {
		ruleText = strings.TrimSpace(*rule)
	}
	noAction := len(effects) > 0
	for _, effect := range effects {
		if effect.typeName != "no_action" {
			noAction = false
			break
		}
	}
	rendered := renderOutcomeCommunications(outcomeRenderData{reference: reference, sourceType: sourceType, offendingClub: offendingClub, offendingTeam: offendingTeam, reportingClub: reportingClub, subject: subject, offenceDate: formatOutcomeOffenceDate(matchDate), findings: findings, appeal: appeal, rule: ruleText, effectSummary: approvedEffectSummary(effects), signatoryName: loadOutcomeSignatoryName(ctx, s.DB, approvedByAdminID, ""), combined: combined, noAction: noAction})
	body := rendered.offending
	if audience == "reporting_club" {
		body = rendered.reporting
	} else if audience == "official" {
		body = rendered.official
	}
	if savedDraft && outcomeDraftMatchesGenerated(savedSubject, savedBody, rendered.subject, body) {
		rendered.subject = savedSubject
		body = savedBody
	}
	pdf := BuildOutcomeLetterPDF(OutcomeLetter{Reference: reference, Audience: audience, Subject: rendered.subject, Body: body, ApprovedAt: documentDate, Draft: true})
	return pdf, reference + "-" + audience + "-preview.pdf", nil
}

// PublishCase queues the exact correspondence and PDF bytes locked by the
// independent approver. No-action decisions follow the same delivery path but
// close unpublished and never enter the public register.
func (s *Service) PublishCase(ctx context.Context, caseID int64, actor Actor) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status, sourceType, casePublicSummary string
	var offendingClubID *int32
	if err = tx.QueryRow(ctx, `SELECT status,source_type,COALESCE(public_summary,''),club_id FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&status, &sourceType, &casePublicSummary, &offendingClubID); err != nil {
		return err
	}
	if status != "approved" {
		return ErrNotPublishable
	}
	if sourceType == "ineligible_player" {
		if err = EnsureIneligibleLinkedIntakesCurrent(ctx, tx, caseID); err != nil {
			return err
		}
		privacyValues, privacyErr := CaseReporterIdentityValues(ctx, tx, caseID)
		if privacyErr != nil {
			return privacyErr
		}
		reportingAliases, privacyErr := CaseReportingClubIdentityValues(ctx, tx, caseID, offendingClubID)
		if privacyErr != nil {
			return privacyErr
		}
		privacyValues = append(privacyValues, reportingAliases...)
		if ContainsPrivateIdentity(casePublicSummary, privacyValues...) {
			return errors.New("case public wording contains reporter or reporting-club identity; publication is blocked")
		}
	}
	var decisionID int64
	if err = tx.QueryRow(ctx, `SELECT id FROM sanction_decision_revisions WHERE case_id=$1 AND status='approved' ORDER BY revision DESC LIMIT 1`, caseID).Scan(&decisionID); err != nil {
		return err
	}
	var noAction bool
	_ = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$1 AND effect_type<>'no_action')`, decisionID).Scan(&noAction)

	rows, err := tx.Query(ctx, `SELECT correspondence.id,correspondence.message_kind,correspondence.audience,correspondence.revision,correspondence.recipients,correspondence.subject,correspondence.body,correspondence.attachment_manifest
		FROM sanction_correspondence_revisions correspondence
		WHERE correspondence.case_id=$1 AND correspondence.decision_revision_id=$2 AND correspondence.status='approved'
		  AND NOT EXISTS(SELECT 1 FROM sanction_correspondence_revisions newer WHERE newer.supersedes_id=correspondence.id AND newer.status='approved')
		ORDER BY correspondence.id`, caseID, decisionID)
	if err != nil {
		return err
	}
	type approvedMessage struct {
		id                            int64
		kind, audience, subject, body string
		revision                      int
		recipients                    []string
		manifest                      []byte
	}
	var approved []approvedMessage
	for rows.Next() {
		var item approvedMessage
		var recipientsJSON []byte
		if err = rows.Scan(&item.id, &item.kind, &item.audience, &item.revision, &recipientsJSON, &item.subject, &item.body, &item.manifest); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(recipientsJSON, &item.recipients); err != nil {
			rows.Close()
			return errors.New("approved outcome recipient snapshot is invalid")
		}
		if len(item.recipients) == 0 && (item.audience == "offending_club" || item.audience == "reporting_club" || item.audience == "official") {
			rows.Close()
			return fmt.Errorf("%s official outcome address is unresolved; update the contact directory before publication", strings.ReplaceAll(item.audience, "_", " "))
		}
		approved = append(approved, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(approved) == 0 {
		return errors.New("approved outcome email and PDF snapshots are missing")
	}

	var policyID *int64
	_ = tx.QueryRow(ctx, `SELECT id FROM sanction_notification_policy_versions WHERE active AND source_type='*' AND event_type='decision_published' ORDER BY version DESC LIMIT 1`).Scan(&policyID)
	for _, item := range approved {
		var queuedID int64
		if err = tx.QueryRow(ctx, `INSERT INTO sanction_correspondence_revisions(case_id,decision_revision_id,supersedes_id,message_kind,audience,revision,status,recipients,subject,body,attachment_manifest,pdf_storage_key,pdf_sha256,pdf_bytes,created_by_admin_id,approved_by_admin_id)
			SELECT case_id,decision_revision_id,id,message_kind,audience,$2,'queued',recipients,subject,body,attachment_manifest,pdf_storage_key,pdf_sha256,pdf_bytes,created_by_admin_id,approved_by_admin_id
			FROM sanction_correspondence_revisions WHERE id=$1 RETURNING id`, item.id, item.revision+1).Scan(&queuedID); err != nil {
			return err
		}
		for _, recipient := range item.recipients {
			recipient = strings.ToLower(strings.TrimSpace(recipient))
			if recipient == "" {
				return errors.New("approved outcome contains an empty recipient")
			}
			key := fmt.Sprintf("case:%d:decision:%d:correspondence:%d:recipient:%s", caseID, decisionID, queuedID, recipient)
			if _, err = tx.Exec(ctx, `INSERT INTO sanction_notification_outbox(case_id,decision_revision_id,policy_version_id,correspondence_revision_id,message_kind,idempotency_key,recipient,subject,body,attachment_manifest)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(idempotency_key) DO NOTHING`, caseID, decisionID, policyID, queuedID, item.kind, key, recipient, item.subject, item.body, item.manifest); err != nil {
				return err
			}
		}
	}

	eventType := "published"
	if noAction {
		eventType = "no_action_outcomes_queued"
		if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='closed',public_status='unpublished',closed_at=now(),updated_at=now() WHERE id=$1`, caseID); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='published',public_status=CASE WHEN EXISTS(SELECT 1 FROM sanction_effect_revisions WHERE decision_revision_id=$2 AND status='suspended') THEN 'suspended' ELSE 'active' END,published_at=now(),updated_at=now() WHERE id=$1`, caseID, decisionID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,request_id,after_data) VALUES($1,$2,$3,$4,$5,$6,$7)`, caseID, eventType, actor.Type, actorID(actor), actor.Label, actor.RequestID, mapJSON(map[string]any{"decision_revision_id": decisionID, "no_action": noAction, "source_type": sourceType})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) publishCaseLegacy(ctx context.Context, caseID int64, actor Actor) error {
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
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,approved_by_admin_id,correction_reason,outcome_subject,outcome_findings,appeal_instructions) SELECT case_id,$2,id,'rejected',public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,$3,$4,outcome_subject,outcome_findings,appeal_instructions FROM sanction_decision_revisions WHERE id=$1 RETURNING id`, priorID, revision+1, *actor.ID, reason).Scan(&rejectedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_key,supersedes_id,effect_type,status,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id) SELECT $2,effect_key,id,effect_type,'cancelled',subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id FROM sanction_effect_revisions WHERE decision_revision_id=$1`, priorID, rejectedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='investigating',current_revision=$2,proposed_by_admin_id=NULL,closed_at=NULL,updated_at=now() WHERE id=$1`, caseID, revision+1); err != nil {
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
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,approved_by_admin_id,correction_reason,outcome_subject,outcome_findings,appeal_instructions)
		SELECT case_id,$2,id,'overturned',public_reason,private_reason,rule_release_id,rule_reference,policy_version_id,proposed_by_admin_id,$3,$4,outcome_subject,outcome_findings,appeal_instructions FROM sanction_decision_revisions WHERE id=$1 RETURNING id`, priorID, priorRevision+1, *actor.ID, reason).Scan(&overturnedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_key,supersedes_id,effect_type,status,subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id)
		SELECT $2,effect_key,id,effect_type,'overturned',subject_type,subject_id,player_name,amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id FROM sanction_effect_revisions WHERE decision_revision_id=$1`, priorID, overturnedID); err != nil {
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
