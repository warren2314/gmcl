package ineligible

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
)

const MaxWorklistCandidates = 2000

var (
	ErrWorklistRunNotReady      = errors.New("ineligible-player import is not ready for selection")
	ErrWorklistSelectionStale   = errors.New("ineligible-player work-list selection is stale")
	ErrWorklistSelectionInvalid = errors.New("ineligible-player work-list selection is invalid")
)

type WorklistCandidate struct {
	ManifestRowID     int64
	IntakeID          int64
	RevisionID        int64
	SourceRowNumber   int
	State             string
	ReportingClub     string
	OffendingClub     string
	Team              string
	Player            string
	FixtureDate       *time.Time
	ReceivedAt        *time.Time
	EvidenceCount     int64
	ExceptionMessage  string
	CurrentVisibility string
	VisibilityBatchID int64
	Selectable        bool
}

type WorklistUnresolvedRow struct {
	SourceRowNumber int
	Error           string
}

type WorklistRun struct {
	RunID           int64
	Status          string
	SourceReference string
	RowsSeen        int
	RowsNew         int
	RowsChanged     int
	RowsErrored     int
	StartedAt       time.Time
	CompletedAt     *time.Time
	ManifestCount   int
	CurrentBatchID  int64
	CandidateSHA256 string
	Candidates      []WorklistCandidate
	UnresolvedRows  []WorklistUnresolvedRow
}

func (run WorklistRun) Ready() bool {
	if run.CompletedAt == nil ||
		(run.Status != "succeeded" && run.Status != "partial") ||
		run.ManifestCount != run.RowsSeen ||
		len(run.UnresolvedRows) != 0 ||
		len(run.Candidates) == 0 ||
		len(run.Candidates) > MaxWorklistCandidates {
		return false
	}
	for _, candidate := range run.Candidates {
		if candidate.Selectable {
			return true
		}
	}
	return false
}

type WorklistSelectionInput struct {
	RunID           int64
	BaseBatchID     int64
	CandidateSHA256 string
	SelectedIDs     []int64
	Reason          string
	AdminID         int32
	AdminLabel      string
	RequestID       string
}

type WorklistSelectionResult struct {
	BatchID       int64
	SelectedCount int
	DeferredCount int
	Unchanged     bool
}

type worklistQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func LoadWorklistRun(ctx context.Context, pool *db.Pool, runID int64) (WorklistRun, error) {
	if pool == nil {
		return WorklistRun{}, fmt.Errorf("database pool is nil")
	}
	return loadWorklistRun(ctx, pool, runID, false)
}

func loadWorklistRun(ctx context.Context, query worklistQuerier, runID int64, lockCandidates bool) (WorklistRun, error) {
	var latestRunID int64
	if err := query.QueryRow(ctx, `
		SELECT run.id
		FROM sanction_intake_sync_runs run
		WHERE run.origin='google_form'
		ORDER BY run.id DESC LIMIT 1
	`).Scan(&latestRunID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorklistRun{}, ErrWorklistRunNotReady
		}
		return WorklistRun{}, err
	}
	if runID < 1 {
		runID = latestRunID
	} else if runID != latestRunID {
		return WorklistRun{}, ErrWorklistSelectionStale
	}

	var run WorklistRun
	err := query.QueryRow(ctx, `
		SELECT id,status,COALESCE(source_reference,''),rows_seen,rows_new,rows_changed,rows_errored,
		       started_at,completed_at
		FROM sanction_intake_sync_runs
		WHERE id=$1 AND origin='google_form'
	`, runID).Scan(&run.RunID, &run.Status, &run.SourceReference, &run.RowsSeen, &run.RowsNew,
		&run.RowsChanged, &run.RowsErrored, &run.StartedAt, &run.CompletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorklistRun{}, ErrWorklistRunNotReady
		}
		return WorklistRun{}, err
	}

	if err = query.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_intake_sync_run_rows WHERE sync_run_id=$1`, runID).Scan(&run.ManifestCount); err != nil {
		return WorklistRun{}, err
	}
	if err = query.QueryRow(ctx, `SELECT id FROM sanction_intake_worklist_batches ORDER BY id DESC LIMIT 1`).Scan(&run.CurrentBatchID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return WorklistRun{}, err
	}

	rowsQuery := `
		SELECT intake.id,latest.id,COALESCE(observed.id,0),
		       COALESCE(observed.source_row_number,latest.source_row_number,0),
		       intake.state,COALESCE(intake.reporting_club_text,''),COALESCE(intake.offending_club_text,''),
		       COALESCE(intake.team_text,''),COALESCE(intake.player_text,''),intake.fixture_date,
		       intake.external_created_at,
		       (SELECT COUNT(*) FROM sanction_intake_attachments attachment WHERE attachment.intake_id=intake.id),
		       COALESCE(intake.exception_message,''),
		       COALESCE(visibility.visibility,'visible'),COALESCE(visibility.batch_id,0),
		       (observed.id IS NOT NULL AND observed.revision_id=latest.id)
		FROM sanction_intakes intake
		JOIN LATERAL (
			SELECT revision.id,revision.source_row_number
			FROM sanction_intake_revisions revision
			WHERE revision.intake_id=intake.id
			ORDER BY revision.revision DESC LIMIT 1
		) latest ON TRUE
		LEFT JOIN LATERAL (
			SELECT manifest.id,manifest.source_row_number,manifest.revision_id
			FROM sanction_intake_sync_run_rows manifest
			WHERE manifest.sync_run_id=$1 AND manifest.resolution_status='resolved'
			  AND manifest.intake_id=intake.id
			ORDER BY manifest.source_row_number LIMIT 1
		) observed ON TRUE
		LEFT JOIN sanction_intake_worklist_current visibility ON visibility.intake_id=intake.id
		WHERE intake.origin='google_form'
		  AND intake.state IN ('new','reviewing','exception')
		  AND NOT EXISTS(
			SELECT 1 FROM sanction_intake_effective_case_links link
			WHERE link.intake_id=intake.id AND link.relationship<>'duplicate'
		  )
		ORDER BY COALESCE(observed.source_row_number,latest.source_row_number,2147483647),intake.id`
	if lockCandidates {
		rowsQuery += ` FOR UPDATE OF intake`
	}
	rows, err := query.Query(ctx, rowsQuery, runID)
	if err != nil {
		return WorklistRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item WorklistCandidate
		if err = rows.Scan(&item.IntakeID, &item.RevisionID, &item.ManifestRowID, &item.SourceRowNumber,
			&item.State, &item.ReportingClub, &item.OffendingClub, &item.Team, &item.Player,
			&item.FixtureDate, &item.ReceivedAt, &item.EvidenceCount, &item.ExceptionMessage, &item.CurrentVisibility,
			&item.VisibilityBatchID, &item.Selectable); err != nil {
			return WorklistRun{}, err
		}
		run.Candidates = append(run.Candidates, item)
		if len(run.Candidates) > MaxWorklistCandidates {
			return WorklistRun{}, fmt.Errorf("%w: more than %d reports require selection", ErrWorklistRunNotReady, MaxWorklistCandidates)
		}
	}
	if err = rows.Err(); err != nil {
		return WorklistRun{}, err
	}

	unresolved, err := query.Query(ctx, `
		SELECT source_row_number,COALESCE(error_message,'Source row could not be matched safely')
		FROM sanction_intake_sync_run_rows
		WHERE sync_run_id=$1 AND resolution_status='unresolved'
		ORDER BY source_row_number
	`, runID)
	if err != nil {
		return WorklistRun{}, err
	}
	defer unresolved.Close()
	for unresolved.Next() {
		var item WorklistUnresolvedRow
		if err = unresolved.Scan(&item.SourceRowNumber, &item.Error); err != nil {
			return WorklistRun{}, err
		}
		run.UnresolvedRows = append(run.UnresolvedRows, item)
	}
	if err = unresolved.Err(); err != nil {
		return WorklistRun{}, err
	}
	run.CandidateSHA256 = worklistCandidateSHA256(run.Candidates)
	return run, nil
}

func worklistCandidateSHA256(candidates []WorklistCandidate) string {
	copyOf := append([]WorklistCandidate(nil), candidates...)
	sort.Slice(copyOf, func(i, j int) bool { return copyOf[i].IntakeID < copyOf[j].IntakeID })
	hash := sha256.New()
	for _, item := range copyOf {
		_, _ = fmt.Fprintf(hash, "%d:%d:%d:%t\n", item.IntakeID, item.RevisionID, item.ManifestRowID, item.Selectable)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func worklistSelectionSHA256(ids []int64) string {
	copyOf := append([]int64(nil), ids...)
	sort.Slice(copyOf, func(i, j int) bool { return copyOf[i] < copyOf[j] })
	hash := sha256.New()
	for _, id := range copyOf {
		_, _ = fmt.Fprintf(hash, "%d\n", id)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func ApplyWorklistSelection(ctx context.Context, pool *db.Pool, input WorklistSelectionInput) (WorklistSelectionResult, error) {
	if pool == nil {
		return WorklistSelectionResult{}, fmt.Errorf("database pool is nil")
	}
	input.Reason = strings.TrimSpace(input.Reason)
	input.AdminLabel = strings.TrimSpace(input.AdminLabel)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.CandidateSHA256 = strings.TrimSpace(input.CandidateSHA256)
	if input.AdminLabel == "" {
		input.AdminLabel = fmt.Sprintf("Administrator #%d", input.AdminID)
	}
	if input.RunID < 1 || input.AdminID < 1 || utf8.RuneCountInString(input.Reason) < 3 || utf8.RuneCountInString(input.Reason) > 200 || len(input.SelectedIDs) == 0 {
		return WorklistSelectionResult{}, ErrWorklistSelectionInvalid
	}
	selected := make(map[int64]struct{}, len(input.SelectedIDs))
	for _, id := range input.SelectedIDs {
		if id < 1 {
			return WorklistSelectionResult{}, ErrWorklistSelectionInvalid
		}
		selected[id] = struct{}{}
	}
	if len(selected) != len(input.SelectedIDs) || len(selected) > MaxWorklistCandidates {
		return WorklistSelectionResult{}, ErrWorklistSelectionInvalid
	}

	selectedIDs := make([]int64, 0, len(selected))
	for id := range selected {
		selectedIDs = append(selectedIDs, id)
	}
	selectionSHA := worklistSelectionSHA256(selectedIDs)
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return WorklistSelectionResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gmcl_ineligible_google_sync'))`); err != nil {
		return WorklistSelectionResult{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gmcl_ineligible_worklist_selection'))`); err != nil {
		return WorklistSelectionResult{}, err
	}

	if input.RequestID != "" {
		var existingBatchID, existingRunID int64
		var existingAdminID int32
		var existingCandidateSHA, existingSelectionSHA, existingReason string
		var existingSelectedCount, existingDeferredCount int
		err = tx.QueryRow(ctx, `
			SELECT id,sync_run_id,created_by_admin_id,candidate_sha256,selection_sha256,
			       reason,selected_count,deferred_count
			FROM sanction_intake_worklist_batches
			WHERE request_id=$1
		`, input.RequestID).Scan(&existingBatchID, &existingRunID, &existingAdminID,
			&existingCandidateSHA, &existingSelectionSHA, &existingReason,
			&existingSelectedCount, &existingDeferredCount)
		switch {
		case err == nil:
			if existingRunID != input.RunID || existingAdminID != input.AdminID ||
				existingCandidateSHA != input.CandidateSHA256 ||
				existingSelectionSHA != selectionSHA || existingReason != input.Reason {
				return WorklistSelectionResult{}, ErrWorklistSelectionInvalid
			}
			return WorklistSelectionResult{
				BatchID: existingBatchID, SelectedCount: existingSelectedCount,
				DeferredCount: existingDeferredCount, Unchanged: true,
			}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return WorklistSelectionResult{}, err
		}
	}
	run, err := loadWorklistRun(ctx, tx, input.RunID, true)
	if err != nil {
		return WorklistSelectionResult{}, err
	}
	if !run.Ready() {
		return WorklistSelectionResult{}, ErrWorklistRunNotReady
	}
	if run.CandidateSHA256 != input.CandidateSHA256 {
		return WorklistSelectionResult{}, ErrWorklistSelectionStale
	}
	selectable := make(map[int64]WorklistCandidate, len(run.Candidates))
	for _, item := range run.Candidates {
		if item.Selectable {
			selectable[item.IntakeID] = item
		}
	}
	for id := range selected {
		if _, ok := selectable[id]; !ok {
			return WorklistSelectionResult{}, ErrWorklistSelectionInvalid
		}
	}

	var priorCandidateSHA, priorSelectionSHA, priorReason string
	var priorRunID int64
	if run.CurrentBatchID > 0 {
		err = tx.QueryRow(ctx, `SELECT sync_run_id,candidate_sha256,selection_sha256,reason
			FROM sanction_intake_worklist_batches WHERE id=$1`, run.CurrentBatchID).
			Scan(&priorRunID, &priorCandidateSHA, &priorSelectionSHA, &priorReason)
		if err != nil {
			return WorklistSelectionResult{}, err
		}
		if priorRunID == input.RunID && priorCandidateSHA == run.CandidateSHA256 &&
			priorSelectionSHA == selectionSHA && priorReason == input.Reason {
			return WorklistSelectionResult{
				BatchID: run.CurrentBatchID, SelectedCount: len(selected),
				DeferredCount: len(run.Candidates) - len(selected), Unchanged: true,
			}, nil
		}
	}
	if run.CurrentBatchID != input.BaseBatchID {
		return WorklistSelectionResult{}, ErrWorklistSelectionStale
	}

	var supersedes any
	if run.CurrentBatchID > 0 {
		supersedes = run.CurrentBatchID
	}
	var requestID any
	if input.RequestID != "" {
		requestID = input.RequestID
	}
	var batchID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO sanction_intake_worklist_batches(
			sync_run_id,supersedes_id,candidate_count,selected_count,deferred_count,
			candidate_sha256,selection_sha256,reason,created_by_admin_id,created_by_label,request_id
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id
	`, input.RunID, supersedes, len(run.Candidates), len(selected), len(run.Candidates)-len(selected),
		run.CandidateSHA256, selectionSHA, input.Reason, input.AdminID, input.AdminLabel, requestID).Scan(&batchID)
	if err != nil {
		return WorklistSelectionResult{}, err
	}
	for _, item := range run.Candidates {
		visibility := "deferred"
		if _, ok := selected[item.IntakeID]; ok {
			visibility = "visible"
		}
		if visibility == "visible" && item.ManifestRowID < 1 {
			return WorklistSelectionResult{}, ErrWorklistSelectionInvalid
		}
		var manifestRow any
		if item.Selectable && item.ManifestRowID > 0 {
			manifestRow = item.ManifestRowID
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO sanction_intake_worklist_decisions(
				batch_id,sync_run_id,intake_id,revision_id,sync_run_row_id,visibility,previous_visibility
			) VALUES($1,$2,$3,$4,$5,$6,$7)
		`, batchID, input.RunID, item.IntakeID, item.RevisionID, manifestRow, visibility, item.CurrentVisibility); err != nil {
			return WorklistSelectionResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return WorklistSelectionResult{}, err
	}
	return WorklistSelectionResult{
		BatchID: batchID, SelectedCount: len(selected), DeferredCount: len(run.Candidates) - len(selected),
	}, nil
}
