package ineligible

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore persists the migration-0050 intake model.
type PGStore struct {
	pool *db.Pool
}

func NewPGStore(pool *db.Pool) *PGStore { return &PGStore{pool: pool} }

const googleSourceIdentityExceptionPrefix = "Google source-row identity is uncertain:"

type googleSourceRowAnchor struct {
	IntakeID    int64
	ExternalKey string
}

type googleSourceAnchorResolution struct {
	TargetIntakeIDs []int64
	ConflictKind    string
}

// resolveGoogleSourceAnchor deliberately prefers immutable row provenance over
// mutable identity fields. It never chooses between two intakes which already
// claim the same source row.
func resolveGoogleSourceAnchor(anchors []googleSourceRowAnchor, externalMatchID int64, incomingExternalKey string) googleSourceAnchorResolution {
	if len(anchors) > 1 {
		targets := make([]int64, 0, len(anchors))
		for _, anchor := range anchors {
			targets = append(targets, anchor.IntakeID)
		}
		return googleSourceAnchorResolution{TargetIntakeIDs: targets, ConflictKind: "ambiguous_source_row"}
	}
	if len(anchors) == 1 {
		anchor := anchors[0]
		if anchor.ExternalKey == incomingExternalKey {
			return googleSourceAnchorResolution{TargetIntakeIDs: []int64{anchor.IntakeID}}
		}
		kind := "identity_changed"
		if externalMatchID > 0 && externalMatchID != anchor.IntakeID {
			kind = "source_row_reordered"
		}
		return googleSourceAnchorResolution{TargetIntakeIDs: []int64{anchor.IntakeID}, ConflictKind: kind}
	}
	if externalMatchID > 0 {
		return googleSourceAnchorResolution{TargetIntakeIDs: []int64{externalMatchID}, ConflictKind: "identity_moved_rows"}
	}
	return googleSourceAnchorResolution{}
}

type googleIntakeCurrent struct {
	ID                     int64
	LatestRevision         int
	State                  string
	ExceptionMessage       string
	HasPriorResolution     bool
	LatestRevisionID       int64
	LatestSHA              string
	LatestIdentityConflict bool
}

type pgSyncLock struct {
	conn *pgxpool.Conn
	mu   sync.Mutex
	done bool
}

func (l *pgSyncLock) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return nil
	}
	l.done = true
	defer l.conn.Release()
	var unlocked bool
	if err := l.conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtext('gmcl_ineligible_google_sync'))`).Scan(&unlocked); err != nil {
		return err
	}
	if !unlocked {
		return fmt.Errorf("ineligible-player advisory lock was not held")
	}
	return nil
}

func (s *PGStore) TrySyncLock(ctx context.Context) (SyncLock, bool, error) {
	if s == nil || s.pool == nil {
		return nil, false, fmt.Errorf("database pool is nil")
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('gmcl_ineligible_google_sync'))`).Scan(&locked); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !locked {
		conn.Release()
		return nil, false, nil
	}
	return &pgSyncLock{conn: conn}, true, nil
}

func (s *PGStore) StartSyncRun(ctx context.Context, trigger Trigger, sourceReference, headerSHA string) (int64, error) {
	var runID int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sanction_intake_sync_runs(
			origin,status,source_reference,header_sha256,triggered_by_type,triggered_by_admin_id
		) VALUES($1,'running',$2,NULLIF($3,''),$4,$5)
		RETURNING id
	`, googleFormOrigin, sourceReference, headerSHA, trigger.Type, trigger.AdminID).Scan(&runID)
	return runID, err
}

func (s *PGStore) FinishSyncRun(ctx context.Context, summary Summary, headerSHA, errorMessage string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE sanction_intake_sync_runs
		SET status=$2,
			header_sha256=COALESCE(NULLIF($3,''),header_sha256),
			rows_seen=$4,rows_new=$5,rows_changed=$6,rows_errored=$7,
			error_message=NULLIF($8,''),completed_at=now()
		WHERE id=$1 AND status='running'
	`, summary.RunID, summary.Status, headerSHA, summary.Seen, summary.New, summary.Changed, summary.Errors, errorMessage)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("sync run %d is missing or no longer running", summary.RunID)
	}
	// Only immutable scheduled observations advance rollout. The projection is
	// recomputed by distinct Europe/London dates with failure dominance.
	if err = recordScheduledRollout(ctx, tx, summary); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PGStore) ApplyRow(ctx context.Context, runID int64, row IntakeRow) (ApplyDisposition, error) {
	if strings.TrimSpace(row.SourceReference) == "" || row.SourceRowNumber < 2 {
		return ApplyUnchanged, fmt.Errorf("Google intake row is missing stable source provenance")
	}
	rawJSON, err := json.Marshal(row.RawData)
	if err != nil {
		return ApplyUnchanged, fmt.Errorf("encode raw intake row: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ApplyUnchanged, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "gmcl_google_source_row|"+row.SourceReference+"|"+fmt.Sprint(row.SourceRowNumber)); err != nil {
		return ApplyUnchanged, err
	}
	anchorRows, err := tx.Query(ctx, `SELECT intake.id,intake.external_key
		FROM sanction_intakes intake
		WHERE intake.origin=$1 AND intake.source_reference=$2
		  AND EXISTS(SELECT 1 FROM sanction_intake_revisions revision
			WHERE revision.intake_id=intake.id AND revision.source_row_number=$3)
		ORDER BY intake.id
		FOR UPDATE OF intake`, googleFormOrigin, row.SourceReference, row.SourceRowNumber)
	if err != nil {
		return ApplyUnchanged, err
	}
	anchors := make([]googleSourceRowAnchor, 0, 1)
	for anchorRows.Next() {
		var anchor googleSourceRowAnchor
		if err = anchorRows.Scan(&anchor.IntakeID, &anchor.ExternalKey); err != nil {
			anchorRows.Close()
			return ApplyUnchanged, err
		}
		anchors = append(anchors, anchor)
	}
	err = anchorRows.Err()
	anchorRows.Close()
	if err != nil {
		return ApplyUnchanged, err
	}

	var externalMatchID int64
	err = tx.QueryRow(ctx, `SELECT id FROM sanction_intakes
		WHERE origin=$1 AND external_key=$2 FOR UPDATE`, googleFormOrigin, row.ExternalKey).Scan(&externalMatchID)
	if err != nil && !errorsIsNoRows(err) {
		return ApplyUnchanged, err
	}
	if errorsIsNoRows(err) {
		externalMatchID = 0
	}
	anchorResolution := resolveGoogleSourceAnchor(anchors, externalMatchID, row.ExternalKey)
	if anchorResolution.ConflictKind != "" {
		for _, targetID := range anchorResolution.TargetIntakeIDs {
			current, loadErr := loadGoogleIntakeCurrent(ctx, tx, targetID)
			if loadErr != nil {
				return ApplyUnchanged, loadErr
			}
			result, recordErr := recordGoogleIdentityException(ctx, tx, runID, row, rawJSON, current, anchorResolution.ConflictKind)
			if recordErr != nil {
				return ApplyUnchanged, recordErr
			}
			if len(anchorResolution.TargetIntakeIDs) == 1 && result == ApplyUnchanged &&
				googleIdentityConflictAlreadyResolved(current, row) {
				return commitGoogleResolvedRow(ctx, tx, runID, row, current.ID, current.LatestRevisionID, ApplyUnchanged)
			}
		}
		return commitGoogleUnresolvedRow(ctx, tx, runID, row,
			googleIdentityExceptionMessage(anchorResolution.ConflictKind, row.SourceRowNumber))
	}

	var current googleIntakeCurrent
	if len(anchorResolution.TargetIntakeIDs) == 1 {
		current, err = loadGoogleIntakeCurrent(ctx, tx, anchorResolution.TargetIntakeIDs[0])
		if err != nil {
			return ApplyUnchanged, err
		}
	}
	if current.ID != 0 && current.State == "exception" && strings.Contains(current.ExceptionMessage, googleSourceIdentityExceptionPrefix) {
		_, recordErr := recordGoogleIdentityException(ctx, tx, runID, row, rawJSON, current, "unresolved_identity_exception")
		if recordErr != nil {
			return ApplyUnchanged, recordErr
		}
		return commitGoogleUnresolvedRow(ctx, tx, runID, row,
			googleIdentityExceptionMessage("unresolved_identity_exception", row.SourceRowNumber))
	}

	intakeID := current.ID
	latestRevision := current.LatestRevision
	currentState := current.State
	currentException := current.ExceptionMessage
	hasPriorResolution := current.HasPriorResolution
	latestRevisionID := current.LatestRevisionID
	latestSHA := current.LatestSHA
	if intakeID == 0 {
		err = tx.QueryRow(ctx, `
			INSERT INTO sanction_intakes(
				origin,external_key,source_reference,external_created_at,state,
				reporting_club_text,offending_club_text,team_text,player_text,
				fixture_date,latest_revision,exception_message
			) VALUES(
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,NULLIF($11,'')
			) RETURNING id
		`, googleFormOrigin, row.ExternalKey, row.SourceReference, row.ExternalCreatedAt,
			row.State, row.ReportingClubText, row.OffendingClubText, row.TeamText,
			row.PlayerText, row.FixtureDate, row.ExceptionMessage).Scan(&intakeID)
		if err != nil {
			return ApplyUnchanged, err
		}
		var revisionID int64
		if err = tx.QueryRow(ctx, `
			INSERT INTO sanction_intake_revisions(
				intake_id,sync_run_id,revision,source_row_number,raw_data,raw_sha256,header_sha256
			) VALUES($1,$2,1,$3,$4::jsonb,$5,$6)
			RETURNING id
		`, intakeID, runID, row.SourceRowNumber, string(rawJSON), row.RawSHA256, row.HeaderSHA256).Scan(&revisionID); err != nil {
			return ApplyUnchanged, err
		}
		if _, err = insertAttachments(ctx, tx, intakeID, revisionID, runID, row.Attachments); err != nil {
			return ApplyUnchanged, err
		}
		return commitGoogleResolvedRow(ctx, tx, runID, row, intakeID, revisionID, ApplyNew)
	}

	if latestSHA == row.RawSHA256 {
		if latestRevisionID == 0 {
			return ApplyUnchanged, fmt.Errorf("intake %d has no current revision", intakeID)
		}
		resolvedState := hasPriorResolution
		changedFiles, attachmentErr := insertAttachments(ctx, tx, intakeID, latestRevisionID, runID, row.Attachments)
		if attachmentErr != nil {
			return ApplyUnchanged, attachmentErr
		}
		if len(changedFiles) > 0 {
			message := truncateMessage(appendException(row.ExceptionMessage, "immutable Google Drive upload content changed for file ID(s): "+strings.Join(changedFiles, ", ")), 2000)
			if resolvedState {
				message = truncateMessage("source changed after prior triage resolution; "+message, 2000)
			}
			if _, err = tx.Exec(ctx, `
				UPDATE sanction_intakes
				SET state='exception',exception_message=$2,updated_at=now()
				WHERE id=$1
			`, intakeID, message); err != nil {
				return ApplyUnchanged, err
			}
			if resolvedState {
				if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,metadata)
					SELECT DISTINCT link.case_id,'linked_intake_revision_changed','system','Daily ineligible-player sync',$2,
						jsonb_build_object('intake_id',$1::bigint,'intake_revision',$3::integer,'attachment_only',true)
					FROM sanction_intake_effective_case_links link WHERE link.intake_id=$1`, intakeID, message, latestRevision); err != nil {
					return ApplyUnchanged, err
				}
				if err = invalidateLinkedCaseResponseWindows(ctx, tx, intakeID, message, latestRevision); err != nil {
					return ApplyUnchanged, err
				}
			}
			return commitGoogleResolvedRow(ctx, tx, runID, row, intakeID, latestRevisionID, ApplyException)
		}
		if row.State == "exception" && (currentState != "exception" || currentException != row.ExceptionMessage) {
			message := row.ExceptionMessage
			if resolvedState {
				message = truncateMessage(appendException(message, "source validation failed after prior triage resolution"), 2000)
			}
			if _, err = tx.Exec(ctx, `
				UPDATE sanction_intakes
				SET state='exception',exception_message=NULLIF($2,''),updated_at=now()
				WHERE id=$1
			`, intakeID, message); err != nil {
				return ApplyUnchanged, err
			}
			if resolvedState {
				if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,metadata)
					SELECT DISTINCT link.case_id,'linked_intake_revision_changed','system','Daily ineligible-player sync',$2,
						jsonb_build_object('intake_id',$1::bigint,'intake_revision',$3::integer,'validation_exception',true)
					FROM sanction_intake_effective_case_links link WHERE link.intake_id=$1`, intakeID, message, latestRevision); err != nil {
					return ApplyUnchanged, err
				}
				if err = invalidateLinkedCaseResponseWindows(ctx, tx, intakeID, message, latestRevision); err != nil {
					return ApplyUnchanged, err
				}
			}
		} else if row.State == "new" && currentState == "exception" && !hasPriorResolution && !strings.Contains(currentException, "source changed after prior triage resolution") {
			if _, err = tx.Exec(ctx, `
				UPDATE sanction_intakes SET state='new',exception_message=NULL,updated_at=now() WHERE id=$1
			`, intakeID); err != nil {
				return ApplyUnchanged, err
			}
		}
		return commitGoogleResolvedRow(ctx, tx, runID, row, intakeID, latestRevisionID, ApplyUnchanged)
	}
	nextRevision := latestRevision + 1
	var revisionID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO sanction_intake_revisions(
			intake_id,sync_run_id,revision,source_row_number,raw_data,raw_sha256,header_sha256
		) VALUES($1,$2,$3,$4,$5::jsonb,$6,$7)
		RETURNING id
	`, intakeID, runID, nextRevision, row.SourceRowNumber, string(rawJSON), row.RawSHA256, row.HeaderSHA256).Scan(&revisionID); err != nil {
		return ApplyUnchanged, err
	}
	if _, err = insertAttachments(ctx, tx, intakeID, revisionID, runID, row.Attachments); err != nil {
		return ApplyUnchanged, err
	}
	resolvedChange := hasPriorResolution
	resolvedChangeMessage := ""
	if resolvedChange {
		resolvedChangeMessage = fmt.Sprintf("source changed after prior triage resolution; review immutable revision %d before proceeding", nextRevision)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE sanction_intakes
		SET source_reference=$2,external_created_at=$3,
			state=CASE
				WHEN $13::boolean THEN 'exception'
				WHEN $4='exception' THEN 'exception'
				WHEN state='exception' THEN 'new'
				ELSE state
			END,
			reporting_club_text=$5,offending_club_text=$6,team_text=$7,
			player_text=$8,fixture_date=$9,latest_revision=$10,
			exception_message=CASE
				WHEN $13::boolean THEN $12
				WHEN $4='exception' THEN NULLIF($11,'')
				ELSE NULL
			END,
			updated_at=now()
		WHERE id=$1
	`, intakeID, row.SourceReference, row.ExternalCreatedAt, row.State,
		row.ReportingClubText, row.OffendingClubText, row.TeamText, row.PlayerText,
		row.FixtureDate, nextRevision, row.ExceptionMessage, resolvedChangeMessage, hasPriorResolution); err != nil {
		return ApplyUnchanged, err
	}
	if resolvedChange {
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,metadata)
			SELECT DISTINCT link.case_id,'linked_intake_revision_changed','system','Daily ineligible-player sync',$2,
				jsonb_build_object('intake_id',$1::bigint,'intake_revision',$3::integer)
			FROM sanction_intake_effective_case_links link WHERE link.intake_id=$1`, intakeID, resolvedChangeMessage, nextRevision); err != nil {
			return ApplyUnchanged, err
		}
		if err = invalidateLinkedCaseResponseWindows(ctx, tx, intakeID, resolvedChangeMessage, nextRevision); err != nil {
			return ApplyUnchanged, err
		}
	}
	return commitGoogleResolvedRow(ctx, tx, runID, row, intakeID, revisionID, ApplyChanged)
}

func loadGoogleIntakeCurrent(ctx context.Context, tx pgx.Tx, intakeID int64) (googleIntakeCurrent, error) {
	var current googleIntakeCurrent
	err := tx.QueryRow(ctx, `SELECT intake.id,intake.latest_revision,intake.state,COALESCE(intake.exception_message,''),
		intake.state IN ('linked','duplicate','ignored') OR EXISTS(
			SELECT 1 FROM sanction_intake_effective_case_links link WHERE link.intake_id=intake.id
		),COALESCE(latest.id,0),COALESCE(latest.raw_sha256,''),
		COALESCE(latest.raw_data ? '_gmcl_identity_anchor_exception',FALSE)
		FROM sanction_intakes intake
		LEFT JOIN LATERAL (
			SELECT revision.id,revision.raw_sha256,revision.raw_data
			FROM sanction_intake_revisions revision
			WHERE revision.intake_id=intake.id
			ORDER BY revision.revision DESC LIMIT 1
		) latest ON TRUE
		WHERE intake.origin=$1 AND intake.id=$2
		FOR UPDATE OF intake`, googleFormOrigin, intakeID).Scan(
		&current.ID, &current.LatestRevision, &current.State, &current.ExceptionMessage,
		&current.HasPriorResolution, &current.LatestRevisionID, &current.LatestSHA,
		&current.LatestIdentityConflict,
	)
	return current, err
}

func googleIdentityExceptionMessage(kind string, rowNumber int) string {
	detail := "the row's identity fields changed after its first observation"
	switch kind {
	case "source_row_reordered":
		detail = "the row now matches a different previously observed response, indicating reordering or identity replacement"
	case "identity_moved_rows":
		detail = "a previously observed response identity appeared at a different unanchored row, indicating reordering"
	case "ambiguous_source_row":
		detail = "multiple intakes already claim this source row, so no intake identity can be selected safely"
	case "unresolved_identity_exception":
		detail = "a prior source-row identity exception has not been manually resolved"
	}
	return fmt.Sprintf("%s source row %d: %s; manual triage is required", googleSourceIdentityExceptionPrefix, rowNumber, detail)
}

func googleIdentityConflictAlreadyResolved(current googleIntakeCurrent, row IntakeRow) bool {
	resolvedState := current.State == "linked" || current.State == "duplicate" || current.State == "ignored"
	return current.LatestSHA == row.RawSHA256 && current.LatestIdentityConflict && resolvedState && current.ExceptionMessage == "" && row.State != "exception"
}

func recordGoogleIdentityException(ctx context.Context, tx pgx.Tx, runID int64, row IntakeRow, rawJSON []byte, current googleIntakeCurrent, conflictKind string) (ApplyDisposition, error) {
	if current.LatestRevisionID == 0 || current.LatestRevision < 1 {
		return ApplyUnchanged, fmt.Errorf("intake %d has no current revision for source-row identity exception", current.ID)
	}
	message := truncateMessage(appendException(row.ExceptionMessage, googleIdentityExceptionMessage(conflictKind, row.SourceRowNumber)), 2000)
	if googleIdentityConflictAlreadyResolved(current, row) {
		return ApplyUnchanged, nil
	}

	appendRevision := current.LatestSHA != row.RawSHA256 || !current.LatestIdentityConflict
	nextRevision := current.LatestRevision
	latestRevisionID := current.LatestRevisionID
	if appendRevision {
		nextRevision++
		var rawData map[string]any
		if err := json.Unmarshal(rawJSON, &rawData); err != nil {
			return ApplyUnchanged, fmt.Errorf("decode identity-conflict intake snapshot: %w", err)
		}
		rawData["_gmcl_identity_anchor_exception"] = map[string]any{
			"conflict_kind":         conflictKind,
			"incoming_external_key": row.ExternalKey,
			"source_reference":      row.SourceReference,
			"source_row_number":     row.SourceRowNumber,
		}
		identityRawJSON, err := json.Marshal(rawData)
		if err != nil {
			return ApplyUnchanged, fmt.Errorf("encode identity-conflict intake snapshot: %w", err)
		}
		if err = tx.QueryRow(ctx, `INSERT INTO sanction_intake_revisions(
				intake_id,sync_run_id,revision,source_row_number,raw_data,raw_sha256,header_sha256
			) VALUES($1,$2,$3,$4,$5::jsonb,$6,$7) RETURNING id`,
			current.ID, runID, nextRevision, row.SourceRowNumber, string(identityRawJSON), row.RawSHA256, row.HeaderSHA256).Scan(&latestRevisionID); err != nil {
			return ApplyUnchanged, err
		}
		if _, err = insertAttachments(ctx, tx, current.ID, latestRevisionID, runID, row.Attachments); err != nil {
			return ApplyUnchanged, err
		}
	}

	projectionChanged := current.State != "exception" || current.ExceptionMessage != message || appendRevision
	if !projectionChanged {
		return ApplyException, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE sanction_intakes
		SET state='exception',latest_revision=$2,exception_message=$3,updated_at=now()
		WHERE id=$1`, current.ID, nextRevision, message); err != nil {
		return ApplyUnchanged, err
	}
	if current.HasPriorResolution {
		if _, err := tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,metadata)
			SELECT DISTINCT link.case_id,'linked_intake_revision_changed','system','Daily ineligible-player sync',$2,
				jsonb_build_object('intake_id',$1::bigint,'intake_revision',$3::integer,
					'identity_change',true,'identity_conflict_kind',$4::text,'source_row_number',$5::integer)
			FROM sanction_intake_effective_case_links link WHERE link.intake_id=$1`,
			current.ID, message, nextRevision, conflictKind, row.SourceRowNumber); err != nil {
			return ApplyUnchanged, err
		}
		if err := invalidateLinkedCaseResponseWindows(ctx, tx, current.ID, message, nextRevision); err != nil {
			return ApplyUnchanged, err
		}
	}
	return ApplyException, nil
}

func invalidateLinkedCaseResponseWindows(ctx context.Context, tx pgx.Tx, intakeID int64, reason string, revision int) error {
	_, err := tx.Exec(ctx, `WITH affected AS (
		SELECT DISTINCT link.case_id
		FROM sanction_intake_effective_case_links link
		WHERE link.intake_id=$1 AND link.relationship<>'duplicate'
	), cancelled AS (
		UPDATE sanction_response_requests request
		SET status='cancelled',closed_at=COALESCE(request.closed_at,now())
		FROM affected
		WHERE request.case_id=affected.case_id AND request.status IN ('queued','pending')
		RETURNING request.case_id,request.access_token_id
	), revoked AS (
		UPDATE sanction_case_access_tokens token
		SET revoked_at=COALESCE(token.revoked_at,now())
		FROM cancelled WHERE token.id=cancelled.access_token_id
		RETURNING token.id
	), notices AS (
		UPDATE sanction_notification_outbox outbox
		SET processed_at=now()
		FROM (SELECT DISTINCT case_id FROM cancelled) stopped
		WHERE outbox.case_id=stopped.case_id
		  AND outbox.message_kind IN ('response_request','response_reminder')
		  AND outbox.processed_at IS NULL
		RETURNING outbox.id
	), resumed AS (
		UPDATE sanction_cases cases
		SET status='investigating',updated_at=now()
		FROM (SELECT DISTINCT case_id FROM cancelled) stopped
		WHERE cases.id=stopped.case_id AND cases.status='response_pending'
		RETURNING cases.id
	)
	INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,metadata)
	SELECT DISTINCT case_id,'response_request_cancelled_source_changed','system','Ineligible-player intake sync',$2,
		jsonb_build_object('intake_id',$1::bigint,'intake_revision',$3::integer)
	FROM cancelled`, intakeID, reason, revision)
	if err != nil {
		return fmt.Errorf("cancel stale response request for changed intake: %w", err)
	}
	return nil
}

func insertAttachments(ctx context.Context, tx pgx.Tx, intakeID, revisionID, runID int64, attachments []StoredAttachment) ([]string, error) {
	changedFiles := make([]string, 0)
	for _, attachment := range attachments {
		var versionCount int
		var exactSHAExists bool
		err := tx.QueryRow(ctx, `
			SELECT COUNT(DISTINCT sha256),COALESCE(BOOL_OR(sha256=$3),FALSE)
			FROM sanction_intake_attachments
			WHERE revision_id=$1 AND google_drive_file_id=$2
		`, revisionID, attachment.DriveFileID, attachment.SHA256).Scan(&versionCount, &exactSHAExists)
		if err != nil {
			return nil, fmt.Errorf("inspect immutable intake attachment: %w", err)
		}
		if versionCount > 1 || (versionCount == 1 && !exactSHAExists) {
			changedFiles = append(changedFiles, attachment.DriveFileID)
		}
		if exactSHAExists {
			continue
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO sanction_intake_attachments(
				intake_id,revision_id,sync_run_id,google_drive_file_id,source_url,
				original_filename,content_type,size_bytes,sha256,storage_key
			) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10)
		`, intakeID, revisionID, runID, attachment.DriveFileID, attachment.SourceURL,
			attachment.OriginalName, attachment.ContentType, attachment.SizeBytes,
			attachment.SHA256, attachment.StorageKey); err != nil {
			return nil, fmt.Errorf("record immutable intake attachment: %w", err)
		}
	}
	return changedFiles, nil
}

func errorsIsNoRows(err error) bool {
	return err == pgx.ErrNoRows
}
