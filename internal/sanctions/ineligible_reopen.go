package sanctions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrIneligibleReopenNotAllowed     = errors.New("approved ineligible-player case cannot be reopened")
	ErrIneligibleReopenNoSourceChange = errors.New("no newer linked intake revision requires review")
	ErrIneligibleReopenAlreadySent    = errors.New("approved outcome or compatibility notice may already have been sent")
	ErrIneligibleReopenActionStarted  = errors.New("an approved follow-up action has already started")
	ErrIneligibleReopenOutboxBusy     = errors.New("the sanctions outbox is currently processing")
)

const staleIneligibleLinkedIntakesSQL = `SELECT EXISTS(
	SELECT 1
	FROM sanction_intake_case_links link
	JOIN sanction_intakes intake ON intake.id=link.intake_id
	JOIN sanction_intake_revisions latest
	  ON latest.intake_id=intake.id AND latest.revision=intake.latest_revision
	WHERE link.case_id=$1
	  AND (
		(link.relationship='duplicate' AND intake.state<>'duplicate')
		OR (link.relationship<>'duplicate' AND intake.state<>'linked')
		OR NOT EXISTS (
			SELECT 1 FROM sanction_case_intake_merge_resolutions merged
			WHERE merged.id=(SELECT current.id FROM sanction_case_intake_merge_resolutions current
				WHERE current.case_id=link.case_id AND current.intake_id=intake.id
				  AND current.relationship=link.relationship
				ORDER BY current.id DESC LIMIT 1)
			  AND merged.revision_id=latest.id AND merged.relationship=link.relationship
		)
	  )
)`

type reopenRowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func staleIneligibleLinkedIntakes(ctx context.Context, queryer reopenRowQueryer, caseID int64) (bool, error) {
	var stale bool
	if err := queryer.QueryRow(ctx, staleIneligibleLinkedIntakesSQL, caseID).Scan(&stale); err != nil {
		return false, fmt.Errorf("check linked intake revisions: %w", err)
	}
	return stale, nil
}

func validateIneligibleReopenState(sourceType, status, publicStatus string, publishedAt *time.Time, stale bool) error {
	if sourceType != "ineligible_player" || status != "approved" || publicStatus != "unpublished" || publishedAt != nil {
		return ErrIneligibleReopenNotAllowed
	}
	if !stale {
		return ErrIneligibleReopenNoSourceChange
	}
	return nil
}

func isOutcomeMessageKind(kind string) bool {
	switch kind {
	case "outcome_offending_club", "outcome_reporting_club", "outcome_official", "no_action_outcome":
		return true
	default:
		return false
	}
}

func outcomeDeliveryPreventsReopen(processedAt, revokedAt *time.Time, uncertainOrSentAttempt bool) bool {
	if uncertainOrSentAttempt {
		return true
	}
	if revokedAt != nil {
		return false
	}
	return processedAt != nil
}

// ApprovedIneligibleCaseNeedsReopen is a read-only UI readiness check. The
// command repeats every condition while holding the case row lock.
func (s *Service) ApprovedIneligibleCaseNeedsReopen(ctx context.Context, caseID int64) (bool, error) {
	var sourceType, status, publicStatus string
	var publishedAt *time.Time
	if err := s.DB.QueryRow(ctx, `SELECT source_type,status,public_status,published_at FROM sanction_cases WHERE id=$1`, caseID).
		Scan(&sourceType, &status, &publicStatus, &publishedAt); err != nil {
		return false, err
	}
	stale, err := staleIneligibleLinkedIntakes(ctx, s.DB, caseID)
	if err != nil {
		return false, err
	}
	return validateIneligibleReopenState(sourceType, status, publicStatus, publishedAt, stale) == nil, nil
}

type reopenTaskSnapshot struct {
	id     int64
	before []byte
}

type reopenLegacySanctionSnapshot struct {
	id     int64
	before []byte
}

// ReopenApprovedIneligibleCase appends compensating decision, effect,
// correspondence, ledger and task records. It never rewrites the approved
// snapshots and refuses to proceed once any delivery or external task may have
// taken effect.
func (s *Service) ReopenApprovedIneligibleCase(ctx context.Context, caseID int64, actor Actor, reason string) error {
	reason = strings.TrimSpace(reason)
	if actor.ID == nil || caseID < 1 || reason == "" || len(reason) > 4000 {
		return errors.New("case, authorised administrator and a reason of at most 4000 characters are required")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var sourceType, status, publicStatus string
	var publishedAt *time.Time
	if err = tx.QueryRow(ctx, `SELECT source_type,status,public_status,published_at FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).
		Scan(&sourceType, &status, &publicStatus, &publishedAt); err != nil {
		return err
	}
	stale, err := staleIneligibleLinkedIntakes(ctx, tx, caseID)
	if err != nil {
		return err
	}
	if err = validateIneligibleReopenState(sourceType, status, publicStatus, publishedAt, stale); err != nil {
		return err
	}

	// Do not race an SMTP worker that may already have selected an outcome.
	var outboxAvailable bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(83002)`).Scan(&outboxAvailable); err != nil {
		return err
	}
	if !outboxAvailable {
		return ErrIneligibleReopenOutboxBusy
	}

	var approvedDecisionID int64
	var approvedRevision int
	var latestDecisionStatus string
	if err = tx.QueryRow(ctx, `SELECT id,revision,status FROM sanction_decision_revisions WHERE case_id=$1 ORDER BY revision DESC,id DESC LIMIT 1 FOR UPDATE`, caseID).
		Scan(&approvedDecisionID, &approvedRevision, &latestDecisionStatus); err != nil {
		return err
	}
	if latestDecisionStatus != "approved" {
		return ErrIneligibleReopenNotAllowed
	}

	rows, err := tx.Query(ctx, `SELECT outbox.id,outbox.processed_at,outbox.revoked_at,
		EXISTS(SELECT 1 FROM sanction_notification_attempts attempt WHERE attempt.outbox_id=outbox.id AND attempt.status IN ('sending','sent','bounced','complained'))
		FROM sanction_notification_outbox outbox
		WHERE outbox.case_id=$1 AND outbox.message_kind IN ('outcome_offending_club','outcome_reporting_club','outcome_official','no_action_outcome')
		ORDER BY outbox.id FOR UPDATE OF outbox`, caseID)
	if err != nil {
		return err
	}
	var revokeOutboxIDs []int64
	for rows.Next() {
		var outboxID int64
		var processedAt, revokedAt *time.Time
		var uncertainOrSent bool
		if err = rows.Scan(&outboxID, &processedAt, &revokedAt, &uncertainOrSent); err != nil {
			rows.Close()
			return err
		}
		if outcomeDeliveryPreventsReopen(processedAt, revokedAt, uncertainOrSent) {
			rows.Close()
			return ErrIneligibleReopenAlreadySent
		}
		if revokedAt == nil {
			revokeOutboxIDs = append(revokeOutboxIDs, outboxID)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	taskRows, err := tx.Query(ctx, `SELECT id,status,to_jsonb(task)
		FROM sanction_follow_up_tasks task WHERE case_id=$1 ORDER BY id FOR UPDATE`, caseID)
	if err != nil {
		return err
	}
	var tasks []reopenTaskSnapshot
	for taskRows.Next() {
		var task reopenTaskSnapshot
		var taskStatus string
		if err = taskRows.Scan(&task.id, &taskStatus, &task.before); err != nil {
			taskRows.Close()
			return err
		}
		if taskStatus == "in_progress" || taskStatus == "complete" {
			taskRows.Close()
			return ErrIneligibleReopenActionStarted
		}
		if taskStatus == "open" {
			tasks = append(tasks, task)
		}
	}
	if err = taskRows.Err(); err != nil {
		taskRows.Close()
		return err
	}
	taskRows.Close()

	legacyRows, err := tx.Query(ctx, `SELECT sanction.id,to_jsonb(sanction),sanction.status,COALESCE(sanction.email_status,'pending'),sanction.email_sent_at,
		EXISTS(SELECT 1 FROM sanction_events event WHERE event.sanction_id=sanction.id AND event.event_type='email_sent'),
		EXISTS(SELECT 1 FROM sanction_effect_revisions effect
			WHERE effect.decision_revision_id=$2 AND effect.effect_key=sanction.effect_key)
		FROM sanctions sanction WHERE sanction.case_id=$1 ORDER BY sanction.id FOR UPDATE OF sanction`, caseID, approvedDecisionID)
	if err != nil {
		return err
	}
	var legacySanctions []reopenLegacySanctionSnapshot
	for legacyRows.Next() {
		var legacy reopenLegacySanctionSnapshot
		var legacyStatus, emailStatus string
		var emailSentAt *time.Time
		var emailSentEvent, currentEffect bool
		if err = legacyRows.Scan(&legacy.id, &legacy.before, &legacyStatus, &emailStatus, &emailSentAt, &emailSentEvent, &currentEffect); err != nil {
			legacyRows.Close()
			return err
		}
		if emailStatus == "approved" || emailStatus == "sent" || emailSentAt != nil || emailSentEvent || (currentEffect && legacyStatus != "active") || (!currentEffect && legacyStatus != "overturned") {
			legacyRows.Close()
			return ErrIneligibleReopenAlreadySent
		}
		if currentEffect {
			legacySanctions = append(legacySanctions, legacy)
		}
	}
	if err = legacyRows.Err(); err != nil {
		legacyRows.Close()
		return err
	}
	legacyRows.Close()

	var correctionDecisionID int64
	if err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(
		case_id,revision,supersedes_id,status,public_reason,private_reason,rule_release_id,rule_reference,
		policy_version_id,proposed_by_admin_id,approved_by_admin_id,correction_reason,emergency_override,
		outcome_subject,outcome_findings,appeal_instructions
	)
	SELECT case_id,$2,id,'corrected',public_reason,private_reason,rule_release_id,rule_reference,
		policy_version_id,proposed_by_admin_id,approved_by_admin_id,$3,FALSE,
		outcome_subject,outcome_findings,appeal_instructions
	FROM sanction_decision_revisions WHERE id=$1 RETURNING id`, approvedDecisionID, approvedRevision+1, reason).
		Scan(&correctionDecisionID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(
		decision_revision_id,effect_key,supersedes_id,effect_type,status,subject_type,subject_id,player_name,
		amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id
	)
	SELECT $2,effect_key,id,effect_type,'cancelled',subject_type,subject_id,player_name,
		amount_pence,points,starts_at,ends_at,trigger_condition,public_details,private_details,counts_for_totting,case_subject_id
	FROM sanction_effect_revisions WHERE decision_revision_id=$1`, approvedDecisionID, correctionDecisionID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_card_ledger_entries(
		case_id,decision_revision_id,team_id,club_id,season_id,match_date,yellow_delta,red_delta,points_deduction,entry_type,explanation
	)
	SELECT case_id,$2,team_id,club_id,season_id,match_date,-SUM(yellow_delta),-SUM(red_delta),-SUM(points_deduction),'reversal',$3
	FROM sanction_card_ledger_entries WHERE case_id=$1 AND decision_revision_id=$4
	GROUP BY case_id,team_id,club_id,season_id,match_date
	HAVING SUM(yellow_delta)<>0 OR SUM(red_delta)<>0 OR SUM(points_deduction)<>0`, caseID, correctionDecisionID, reason, approvedDecisionID); err != nil {
		return err
	}

	for _, task := range tasks {
		var after []byte
		if err = tx.QueryRow(ctx, `UPDATE sanction_follow_up_tasks task
			SET status='cancelled',current_note=CONCAT_WS(E'\n',NULLIF(task.current_note,''),$2::text),updated_at=now()
			WHERE task.id=$1 AND task.status='open' RETURNING to_jsonb(task)`, task.id, "Cancelled because the approved case was reopened after a source revision change: "+reason).Scan(&after); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_follow_up_task_events(task_id,actor_admin_id,actor_label,reason,before_data,after_data,request_id)
			VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7)`, task.id, *actor.ID, actor.Label, reason, string(task.before), string(after), actor.RequestID); err != nil {
			return err
		}
	}

	var correspondenceIDs []int64
	correspondenceRows, err := tx.Query(ctx, `SELECT correspondence.id
		FROM sanction_correspondence_revisions correspondence
		WHERE correspondence.case_id=$1 AND correspondence.decision_revision_id=$2
		  AND correspondence.status IN ('approved','queued')
		  AND NOT EXISTS(SELECT 1 FROM sanction_correspondence_revisions newer WHERE newer.supersedes_id=correspondence.id)
		ORDER BY correspondence.id FOR UPDATE OF correspondence`, caseID, approvedDecisionID)
	if err != nil {
		return err
	}
	for correspondenceRows.Next() {
		var id int64
		if err = correspondenceRows.Scan(&id); err != nil {
			correspondenceRows.Close()
			return err
		}
		correspondenceIDs = append(correspondenceIDs, id)
	}
	if err = correspondenceRows.Err(); err != nil {
		correspondenceRows.Close()
		return err
	}
	correspondenceRows.Close()
	for _, correspondenceID := range correspondenceIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_correspondence_revisions(
			case_id,decision_revision_id,supersedes_id,message_kind,audience,revision,status,recipients,subject,body,
			attachment_manifest,pdf_storage_key,pdf_sha256,pdf_bytes,created_by_admin_id
		)
		SELECT case_id,$2,id,message_kind,audience,
			(SELECT COALESCE(MAX(revision),0)+1 FROM sanction_correspondence_revisions prior WHERE prior.case_id=source.case_id AND prior.message_kind=source.message_kind AND prior.audience=source.audience),
			'superseded',recipients,subject,body,attachment_manifest,pdf_storage_key,pdf_sha256,pdf_bytes,$3
		FROM sanction_correspondence_revisions source WHERE source.id=$1`, correspondenceID, correctionDecisionID, *actor.ID); err != nil {
			return err
		}
	}

	if len(revokeOutboxIDs) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE sanction_notification_outbox
			SET processed_at=now(),revoked_at=now(),revoked_by_admin_id=$2,revocation_reason=$3
			WHERE id=ANY($1::bigint[]) AND processed_at IS NULL AND revoked_at IS NULL`, revokeOutboxIDs, *actor.ID, reason); err != nil {
			return err
		}
	}
	for _, legacy := range legacySanctions {
		var after []byte
		if err = tx.QueryRow(ctx, `UPDATE sanctions sanction SET status='overturned',resolved_at=now(),resolved_by_admin_id=$2,
			email_status=CASE WHEN COALESCE(sanction.email_status,'pending')='pending' THEN 'skipped' ELSE sanction.email_status END
			WHERE sanction.id=$1 AND sanction.status='active' RETURNING to_jsonb(sanction)`, legacy.id, *actor.ID).Scan(&after); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_events(
			sanction_id,event_type,event_at,notes,created_by_admin_id,actor_label,reason,before_data,after_data,request_id
		) VALUES($1,'case_source_revision_reopened',now(),$2,$3,$4,$2,$5::jsonb,$6::jsonb,$7)`,
			legacy.id, reason, *actor.ID, actor.Label, string(legacy.before), string(after), actor.RequestID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE sanction_cases SET status='investigating',public_status='unpublished',
		proposed_by_admin_id=NULL,approved_by_admin_id=NULL,approved_at=NULL,published_at=NULL,closed_at=NULL,
		emergency_override=FALSE,current_revision=$2,updated_at=now() WHERE id=$1`, caseID, approvedRevision+1); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(
		case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,before_data,after_data,metadata
	) VALUES($1,'approved_decision_reopened_source_changed','admin',$2,$3,$4,$5,
		jsonb_build_object('status','approved','decision_revision_id',$6::bigint),
		jsonb_build_object('status','investigating','decision_revision_id',$7::bigint),
		jsonb_build_object('revoked_outbox_count',$8::integer,'superseded_correspondence_count',$9::integer,
			'cancelled_task_count',$10::integer,'reversed_legacy_sanction_count',$11::integer))`,
		caseID, *actor.ID, actor.Label, reason, actor.RequestID, approvedDecisionID, correctionDecisionID, len(revokeOutboxIDs), len(correspondenceIDs), len(tasks), len(legacySanctions)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
