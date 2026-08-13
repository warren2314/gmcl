package ineligible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
)

var ErrBackfillApplicationNotReady = errors.New("tracker backfill application is not ready")

// BackfillReviewSnapshotEntry pins the exact latest human review included in a
// named sign-off. Any later review requires a new sign-off before application.
type BackfillReviewSnapshotEntry struct {
	RowID    int64 `json:"row_id"`
	ReviewID int64 `json:"review_id"`
}

type BackfillApplyCase struct {
	ID                        int64
	Reference                 string
	SourceType                string
	Status                    string
	PublicStatus              string
	ClosedAt                  *time.Time
	ApprovedAt                *time.Time
	PublishedAt               *time.Time
	HasDecisionRevisions      bool
	HasEffectRevisions        bool
	HasCorrespondence         bool
	HasOutboxMessages         bool
	HasPendingResponseRequest bool
	HasPendingResponseToken   bool
}

type BackfillApplyRow struct {
	BackfillRowID        int64
	SourceRowNumber      int
	RowSHA256            string
	ReviewID             int64
	Disposition          string
	IntakeID             int64
	ReviewedCaseState    string
	EffectsReviewStatus  string
	EffectInterpretation string
	ReviewReason         string
	ManualHistory        map[string]string
	Case                 *BackfillApplyCase
}

type BackfillApplyPreview struct {
	RunID          int64
	SourceFilename string
	SourceSHA256   string
	SignoffID      int64
	SignatoryName  string
	SignedOffAt    *time.Time
	ApplicationID  int64
	AlreadyApplied bool
	AppliedByName  string
	AppliedAt      *time.Time
	AcceptedRows   int
	OpenRows       int
	ClosedRows     int
	UnmatchedRows  int
	ExcludedRows   int
	Rows           []BackfillApplyRow
	Issues         []string
}

func (p BackfillApplyPreview) Ready() bool {
	return !p.AlreadyApplied && p.SignoffID > 0 && p.AcceptedRows > 0 && len(p.Issues) == 0
}

type BackfillApplicationSummary struct {
	ApplicationID  int64
	RunID          int64
	AcceptedRows   int
	OpenRows       int
	ClosedRows     int
	UnmatchedRows  int
	ExcludedRows   int
	AlreadyApplied bool
}

type backfillApplyQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// LoadTrackerBackfillApplyPreview performs every readiness check without
// mutating state. ApplyTrackerBackfill repeats the same checks under locks.
func LoadTrackerBackfillApplyPreview(ctx context.Context, pool *db.Pool, runID int64) (BackfillApplyPreview, error) {
	if pool == nil {
		return BackfillApplyPreview{}, fmt.Errorf("database pool is nil")
	}
	return loadTrackerBackfillApplyPreview(ctx, pool, runID, false)
}

func loadTrackerBackfillApplyPreview(ctx context.Context, query backfillApplyQuerier, runID int64, lockCases bool) (BackfillApplyPreview, error) {
	var preview BackfillApplyPreview
	preview.RunID = runID
	var signoffID, applicationID *int64
	var signedOffAt, appliedAt *time.Time
	var signatory, snapshotJSON, appliedBy string
	err := query.QueryRow(ctx, `
		SELECT r.id,r.source_filename,r.source_sha256,
		       signoff.id,COALESCE(signoff.signatory_name,''),signoff.created_at,
		       COALESCE(signoff.review_snapshot,'[]'::jsonb)::text,
		       application.id,COALESCE(application.applied_by_name,''),application.created_at
		FROM sanction_ineligible_backfill_runs r
		LEFT JOIN LATERAL (
			SELECT id,signatory_name,created_at,review_snapshot
			FROM sanction_ineligible_backfill_signoffs s
			WHERE s.run_id=r.id ORDER BY s.id DESC LIMIT 1
		) signoff ON TRUE
		LEFT JOIN sanction_ineligible_backfill_applications application ON application.run_id=r.id
		WHERE r.id=$1
	`, runID).Scan(&preview.RunID, &preview.SourceFilename, &preview.SourceSHA256,
		&signoffID, &signatory, &signedOffAt, &snapshotJSON,
		&applicationID, &appliedBy, &appliedAt)
	if err != nil {
		return BackfillApplyPreview{}, err
	}
	if signoffID != nil {
		preview.SignoffID = *signoffID
		preview.SignatoryName = signatory
		preview.SignedOffAt = signedOffAt
	}
	if applicationID != nil {
		preview.ApplicationID = *applicationID
		preview.AlreadyApplied = true
		preview.AppliedByName = appliedBy
		preview.AppliedAt = appliedAt
		if err = query.QueryRow(ctx, `
			SELECT accepted_rows,open_rows,closed_rows,unmatched_rows,excluded_rows
			FROM sanction_ineligible_backfill_applications WHERE id=$1
		`, *applicationID).Scan(&preview.AcceptedRows, &preview.OpenRows, &preview.ClosedRows,
			&preview.UnmatchedRows, &preview.ExcludedRows); err != nil {
			return BackfillApplyPreview{}, err
		}
		return preview, nil
	}
	if signoffID == nil {
		preview.Issues = append(preview.Issues, "a completed named reconciliation sign-off is required")
		return preview, nil
	}

	signedReviews := make(map[int64]int64)
	var snapshot []BackfillReviewSnapshotEntry
	if err = json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil || len(snapshot) == 0 {
		preview.Issues = append(preview.Issues, "the latest sign-off does not contain a valid review snapshot")
	} else {
		for _, entry := range snapshot {
			if entry.RowID <= 0 || entry.ReviewID <= 0 {
				preview.Issues = append(preview.Issues, "the latest sign-off contains an invalid review snapshot entry")
				continue
			}
			if _, duplicate := signedReviews[entry.RowID]; duplicate {
				preview.Issues = append(preview.Issues, fmt.Sprintf("the sign-off contains duplicate snapshot entries for reconciliation row %d", entry.RowID))
			}
			signedReviews[entry.RowID] = entry.ReviewID
		}
	}

	rows, err := query.Query(ctx, `
		SELECT br.id,br.source_row_number,br.row_sha256,br.requires_effect_review,
		       br.manual_history::text,review.id,COALESCE(review.disposition,''),
		       COALESCE(review.reviewed_intake_id,0),COALESCE(review.reviewed_case_state,''),
		       COALESCE(review.effects_review_status,''),COALESCE(review.effect_interpretation,''),
		       COALESCE(review.review_reason,'')
		FROM sanction_ineligible_backfill_rows br
		LEFT JOIN LATERAL (
			SELECT id,disposition,reviewed_intake_id,reviewed_case_state,
			       effects_review_status,effect_interpretation,review_reason
			FROM sanction_ineligible_backfill_reviews rv
			WHERE rv.backfill_row_id=br.id ORDER BY rv.id DESC LIMIT 1
		) review ON TRUE
		WHERE br.run_id=$1 ORDER BY br.source_row_number
	`, runID)
	if err != nil {
		return BackfillApplyPreview{}, err
	}
	type stagedBackfillApplyRow struct {
		row                  BackfillApplyRow
		requiresEffectReview bool
	}
	stagedRows := make([]stagedBackfillApplyRow, 0)
	seenRows := make(map[int64]bool)
	for rows.Next() {
		var row BackfillApplyRow
		var requiresEffectReview bool
		var manualJSON []byte
		if err = rows.Scan(&row.BackfillRowID, &row.SourceRowNumber, &row.RowSHA256,
			&requiresEffectReview, &manualJSON, &row.ReviewID, &row.Disposition,
			&row.IntakeID, &row.ReviewedCaseState, &row.EffectsReviewStatus,
			&row.EffectInterpretation, &row.ReviewReason); err != nil {
			return BackfillApplyPreview{}, err
		}
		seenRows[row.BackfillRowID] = true
		_ = json.Unmarshal(manualJSON, &row.ManualHistory)
		stagedRows = append(stagedRows, stagedBackfillApplyRow{
			row:                  row,
			requiresEffectReview: requiresEffectReview,
		})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return BackfillApplyPreview{}, err
	}
	rows.Close()

	// A pgx transaction uses one connection. Fully consume and close the
	// staged-row result set before issuing the per-row readiness queries below.
	caseState := make(map[int64]string)
	for _, staged := range stagedRows {
		row := staged.row
		requiresEffectReview := staged.requiresEffectReview
		if signedReviewID, ok := signedReviews[row.BackfillRowID]; !ok || signedReviewID != row.ReviewID {
			preview.Issues = append(preview.Issues, fmt.Sprintf("source row %d changed after the latest named sign-off; sign off again", row.SourceRowNumber))
		}
		switch row.Disposition {
		case "exclude_tracker_row":
			preview.ExcludedRows++
			continue
		case "leave_unmatched":
			preview.UnmatchedRows++
			continue
		case "accept_match":
			preview.AcceptedRows++
		default:
			preview.Issues = append(preview.Issues, fmt.Sprintf("source row %d has no completed reconciliation disposition", row.SourceRowNumber))
			continue
		}
		if row.IntakeID <= 0 {
			preview.Issues = append(preview.Issues, fmt.Sprintf("source row %d has no accepted intake", row.SourceRowNumber))
			continue
		}
		switch row.ReviewedCaseState {
		case "open":
			preview.OpenRows++
		case "closed":
			preview.ClosedRows++
		default:
			preview.Issues = append(preview.Issues, fmt.Sprintf("source row %d has no final open/closed review", row.SourceRowNumber))
		}
		if requiresEffectReview && row.EffectsReviewStatus != "manually_interpreted" && row.EffectsReviewStatus != "confirmed_no_effect" {
			preview.Issues = append(preview.Issues, fmt.Sprintf("source row %d still has uninterpreted points/cards text", row.SourceRowNumber))
		}
		var previouslyApplied bool
		if err = query.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM sanction_ineligible_backfill_application_rows
				WHERE intake_id=$1 AND source_row_sha256=$2
			)
		`, row.IntakeID, row.RowSHA256).Scan(&previouslyApplied); err != nil {
			return BackfillApplyPreview{}, err
		}
		if previouslyApplied {
			preview.Issues = append(preview.Issues, fmt.Sprintf("source row %d was already applied from an equivalent reconciliation run", row.SourceRowNumber))
		}

		caseQuery := `
			SELECT c.id,c.reference,c.source_type,c.status,c.public_status,c.closed_at,c.approved_at,c.published_at,
			       EXISTS(SELECT 1 FROM sanction_decision_revisions d WHERE d.case_id=c.id),
			       EXISTS(SELECT 1 FROM sanction_effect_revisions e JOIN sanction_decision_revisions d ON d.id=e.decision_revision_id WHERE d.case_id=c.id),
			       EXISTS(SELECT 1 FROM sanction_correspondence_revisions correspondence WHERE correspondence.case_id=c.id),
			       EXISTS(SELECT 1 FROM sanction_notification_outbox outbox WHERE outbox.case_id=c.id),
			       EXISTS(SELECT 1 FROM sanction_response_requests request WHERE request.case_id=c.id AND request.status IN ('queued','pending')),
			       EXISTS(SELECT 1 FROM sanction_case_access_tokens token WHERE token.case_id=c.id AND token.purpose='respond' AND token.revoked_at IS NULL AND token.expires_at>now())
			FROM sanction_intake_effective_case_links link
			JOIN sanction_cases c ON c.id=link.case_id
			WHERE link.intake_id=$1
			ORDER BY c.id`
		if lockCases {
			caseQuery += ` FOR UPDATE OF c`
		}
		caseRows, caseErr := query.Query(ctx, caseQuery, row.IntakeID)
		if caseErr != nil {
			return BackfillApplyPreview{}, caseErr
		}
		linked := make(map[int64]BackfillApplyCase)
		for caseRows.Next() {
			var linkedCase BackfillApplyCase
			if caseErr = caseRows.Scan(&linkedCase.ID, &linkedCase.Reference, &linkedCase.SourceType,
				&linkedCase.Status, &linkedCase.PublicStatus, &linkedCase.ClosedAt, &linkedCase.ApprovedAt,
				&linkedCase.PublishedAt, &linkedCase.HasDecisionRevisions, &linkedCase.HasEffectRevisions,
				&linkedCase.HasCorrespondence, &linkedCase.HasOutboxMessages,
				&linkedCase.HasPendingResponseRequest, &linkedCase.HasPendingResponseToken); caseErr != nil {
				caseRows.Close()
				return BackfillApplyPreview{}, caseErr
			}
			linked[linkedCase.ID] = linkedCase
		}
		caseErr = caseRows.Err()
		caseRows.Close()
		if caseErr != nil {
			return BackfillApplyPreview{}, caseErr
		}
		linkedCases := make([]BackfillApplyCase, 0, len(linked))
		for _, linkedCase := range linked {
			linkedCases = append(linkedCases, linkedCase)
		}
		sort.Slice(linkedCases, func(i, j int) bool { return linkedCases[i].ID < linkedCases[j].ID })
		selectedCase, caseIssues := validateBackfillCaseSelection(row.SourceRowNumber, row.IntakeID, row.ReviewedCaseState, linkedCases, caseState)
		preview.Issues = append(preview.Issues, caseIssues...)
		if selectedCase == nil {
			preview.Rows = append(preview.Rows, row)
			continue
		}
		row.Case = selectedCase
		preview.Rows = append(preview.Rows, row)
	}
	for rowID := range signedReviews {
		if !seenRows[rowID] {
			preview.Issues = append(preview.Issues, fmt.Sprintf("the sign-off references missing reconciliation row %d", rowID))
		}
	}
	if len(seenRows) != len(signedReviews) {
		preview.Issues = append(preview.Issues, "the signed review snapshot does not cover every staged row")
	}
	if preview.AcceptedRows == 0 {
		preview.Issues = append(preview.Issues, "there are no accepted matched rows to apply")
	}
	sort.Strings(preview.Issues)
	return preview, nil
}

func validateBackfillCaseSelection(sourceRow int, intakeID int64, reviewedState string, linked []BackfillApplyCase, stateByCase map[int64]string) (*BackfillApplyCase, []string) {
	if len(linked) != 1 {
		return nil, []string{fmt.Sprintf("source row %d intake %d is linked to %d cases; exactly one is required", sourceRow, intakeID, len(linked))}
	}
	selected := linked[0]
	issues := make([]string, 0, 5)
	if selected.SourceType != "ineligible_player" {
		issues = append(issues, fmt.Sprintf("source row %d is linked to non-ineligible case %s", sourceRow, selected.Reference))
	}
	if selected.PublicStatus != "unpublished" || selected.PublishedAt != nil {
		issues = append(issues, fmt.Sprintf("case %s has public history and cannot be changed by the private tracker backfill", selected.Reference))
	}
	if !backfillCurrentStatusAllowed(reviewedState, selected.Status) {
		issues = append(issues, fmt.Sprintf("case %s has protected or inappropriate status %q for a reviewed %s tracker row", selected.Reference, selected.Status, reviewedState))
	}
	if selected.ApprovedAt != nil {
		issues = append(issues, fmt.Sprintf("case %s has approval history and cannot be changed by the tracker backfill", selected.Reference))
	}
	activity := make([]string, 0, 6)
	if selected.HasDecisionRevisions {
		activity = append(activity, "decision revisions")
	}
	if selected.HasEffectRevisions {
		activity = append(activity, "decision effects")
	}
	if selected.HasCorrespondence {
		activity = append(activity, "correspondence")
	}
	if selected.HasOutboxMessages {
		activity = append(activity, "outbox messages")
	}
	if selected.HasPendingResponseRequest {
		activity = append(activity, "a queued or pending response request")
	}
	if selected.HasPendingResponseToken {
		activity = append(activity, "an active response token")
	}
	if len(activity) > 0 {
		issues = append(issues, fmt.Sprintf("case %s already has %s and cannot be changed by the tracker backfill", selected.Reference, strings.Join(activity, ", ")))
	}
	if prior, exists := stateByCase[selected.ID]; exists && prior != reviewedState {
		issues = append(issues, fmt.Sprintf("case %s has conflicting open/closed tracker reviews", selected.Reference))
	} else {
		stateByCase[selected.ID] = reviewedState
	}
	copy := selected
	return &copy, issues
}

func backfillCurrentStatusAllowed(reviewedState, currentStatus string) bool {
	switch reviewedState {
	case "open":
		return currentStatus == "submitted" || currentStatus == "triage" || currentStatus == "investigating"
	case "closed":
		return currentStatus == "submitted" || currentStatus == "triage" || currentStatus == "investigating" || currentStatus == "closed"
	default:
		return false
	}
}

type backfillSafetyCounts struct {
	DecisionRevisions      int64 `json:"decision_revisions"`
	EffectRevisions        int64 `json:"effect_revisions"`
	CardLedgerEntries      int64 `json:"card_ledger_entries"`
	LegacySanctions        int64 `json:"legacy_sanctions"`
	FollowUpTasks          int64 `json:"follow_up_tasks"`
	Correspondence         int64 `json:"correspondence_revisions"`
	OutboxMessages         int64 `json:"outbox_messages"`
	PendingResponseRequest int64 `json:"pending_response_requests"`
	PendingResponseTokens  int64 `json:"pending_response_tokens"`
}

func loadBackfillSafetyCounts(ctx context.Context, query backfillApplyQuerier, caseIDs []int64) (backfillSafetyCounts, error) {
	var counts backfillSafetyCounts
	err := query.QueryRow(ctx, `
		SELECT
		 (SELECT COUNT(*) FROM sanction_decision_revisions d WHERE d.case_id=ANY($1::bigint[])),
		 (SELECT COUNT(*) FROM sanction_effect_revisions e JOIN sanction_decision_revisions d ON d.id=e.decision_revision_id WHERE d.case_id=ANY($1::bigint[])),
		 (SELECT COUNT(*) FROM sanction_card_ledger_entries l WHERE l.case_id=ANY($1::bigint[])),
		 (SELECT COUNT(*) FROM sanctions s WHERE s.case_id=ANY($1::bigint[])),
		 (SELECT COUNT(*) FROM sanction_follow_up_tasks t WHERE t.case_id=ANY($1::bigint[])),
		 (SELECT COUNT(*) FROM sanction_correspondence_revisions c WHERE c.case_id=ANY($1::bigint[])),
		 (SELECT COUNT(*) FROM sanction_notification_outbox o WHERE o.case_id=ANY($1::bigint[])),
		 (SELECT COUNT(*) FROM sanction_response_requests r WHERE r.case_id=ANY($1::bigint[]) AND r.status IN ('queued','pending')),
		 (SELECT COUNT(*) FROM sanction_case_access_tokens t WHERE t.case_id=ANY($1::bigint[]) AND t.purpose='respond' AND t.revoked_at IS NULL AND t.expires_at>now())
	`, caseIDs).Scan(&counts.DecisionRevisions, &counts.EffectRevisions, &counts.CardLedgerEntries,
		&counts.LegacySanctions, &counts.FollowUpTasks, &counts.Correspondence, &counts.OutboxMessages,
		&counts.PendingResponseRequest, &counts.PendingResponseTokens)
	return counts, err
}

// ApplyTrackerBackfill appends private historical events and restores only the
// reviewed case status. It verifies all safety-sensitive tables are unchanged
// before committing, and a unique application record makes retries idempotent.
func ApplyTrackerBackfill(ctx context.Context, pool *db.Pool, runID, adminID int64, appliedByName, note string) (BackfillApplicationSummary, error) {
	if pool == nil {
		return BackfillApplicationSummary{}, fmt.Errorf("database pool is nil")
	}
	appliedByName = strings.TrimSpace(appliedByName)
	note = strings.TrimSpace(note)
	if adminID <= 0 || appliedByName == "" {
		return BackfillApplicationSummary{}, fmt.Errorf("a named applying administrator is required")
	}
	if len(appliedByName) > 200 || note == "" || len(note) > 5000 {
		return BackfillApplicationSummary{}, fmt.Errorf("a valid application note of at most 5,000 characters is required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return BackfillApplicationSummary{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gmcl_ineligible_tracker_backfill_review'))`); err != nil {
		return BackfillApplicationSummary{}, fmt.Errorf("lock tracker application: %w", err)
	}
	preview, err := loadTrackerBackfillApplyPreview(ctx, tx, runID, true)
	if err != nil {
		return BackfillApplicationSummary{}, err
	}
	if preview.AlreadyApplied {
		return BackfillApplicationSummary{
			ApplicationID: preview.ApplicationID, RunID: runID, AcceptedRows: preview.AcceptedRows,
			OpenRows: preview.OpenRows, ClosedRows: preview.ClosedRows, UnmatchedRows: preview.UnmatchedRows,
			ExcludedRows: preview.ExcludedRows, AlreadyApplied: true,
		}, nil
	}
	if !preview.Ready() {
		return BackfillApplicationSummary{}, fmt.Errorf("%w: %s", ErrBackfillApplicationNotReady, strings.Join(preview.Issues, "; "))
	}

	caseIDs := make([]int64, 0, len(preview.Rows))
	uniqueCases := make(map[int64]BackfillApplyCase)
	targetStates := make(map[int64]string)
	for _, row := range preview.Rows {
		if row.Disposition != "accept_match" || row.Case == nil {
			continue
		}
		if _, seen := uniqueCases[row.Case.ID]; !seen {
			uniqueCases[row.Case.ID] = *row.Case
			caseIDs = append(caseIDs, row.Case.ID)
		}
		targetStates[row.Case.ID] = row.ReviewedCaseState
	}
	sort.Slice(caseIDs, func(i, j int) bool { return caseIDs[i] < caseIDs[j] })
	safetyBefore, err := loadBackfillSafetyCounts(ctx, tx, caseIDs)
	if err != nil {
		return BackfillApplicationSummary{}, fmt.Errorf("record pre-application safety snapshot: %w", err)
	}
	var appliedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&appliedAt); err != nil {
		return BackfillApplicationSummary{}, err
	}
	type caseStatePair struct {
		before map[string]any
		after  map[string]any
	}
	caseStates := make(map[int64]caseStatePair, len(uniqueCases))
	for _, caseID := range caseIDs {
		beforeCase := uniqueCases[caseID]
		before := map[string]any{"status": beforeCase.Status, "public_status": beforeCase.PublicStatus, "closed_at": beforeCase.ClosedAt}
		target := targetStates[caseID]
		var targetStatus string
		var targetClosedAt *time.Time
		if target == "open" {
			targetStatus = "investigating"
		} else {
			targetStatus = "closed"
			targetClosedAt = beforeCase.ClosedAt
			if targetClosedAt == nil {
				value := appliedAt
				targetClosedAt = &value
			}
		}
		if _, err = tx.Exec(ctx, `
			UPDATE sanction_cases
			SET status=$2,public_status='unpublished',
			    closed_at=CASE WHEN $2='closed' THEN COALESCE(closed_at,$3) ELSE NULL END,
			    updated_at=now()
			WHERE id=$1
		`, caseID, targetStatus, targetClosedAt); err != nil {
			return BackfillApplicationSummary{}, fmt.Errorf("restore reviewed state for case %d: %w", caseID, err)
		}
		after := map[string]any{"status": targetStatus, "public_status": "unpublished", "closed_at": targetClosedAt}
		caseStates[caseID] = caseStatePair{before: before, after: after}
	}

	type appliedRow struct {
		row     BackfillApplyRow
		eventID int64
		before  []byte
		after   []byte
	}
	appliedRows := make([]appliedRow, 0, preview.AcceptedRows)
	for _, row := range preview.Rows {
		if row.Disposition != "accept_match" || row.Case == nil {
			continue
		}
		state := caseStates[row.Case.ID]
		beforeJSON, _ := json.Marshal(state.before)
		afterJSON, _ := json.Marshal(state.after)
		metadata := map[string]any{
			"visibility":                   "private",
			"backfill_run_id":              runID,
			"backfill_row_id":              row.BackfillRowID,
			"source_row_number":            row.SourceRowNumber,
			"source_sha256":                preview.SourceSHA256,
			"source_row_sha256":            row.RowSHA256,
			"intake_id":                    row.IntakeID,
			"review_id":                    row.ReviewID,
			"signoff_id":                   preview.SignoffID,
			"signatory_name":               preview.SignatoryName,
			"manual_history_o_to_z":        row.ManualHistory,
			"reviewed_case_state":          row.ReviewedCaseState,
			"effects_review_status":        row.EffectsReviewStatus,
			"effect_interpretation":        row.EffectInterpretation,
			"reconciliation_review_reason": row.ReviewReason,
			"safety":                       "history and case state only; no effect, ledger, correspondence or outbox write",
		}
		metadataJSON, marshalErr := json.Marshal(metadata)
		if marshalErr != nil {
			return BackfillApplicationSummary{}, marshalErr
		}
		var eventID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO sanction_case_events(
				case_id,event_type,actor_type,actor_id,actor_label,reason,
				before_data,after_data,metadata
			) VALUES($1,'ineligible_tracker_history_imported','import',$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb)
			RETURNING id
		`, row.Case.ID, adminID, appliedByName,
			"Applied signed-off 2026 tracker history: "+note,
			string(beforeJSON), string(afterJSON), string(metadataJSON)).Scan(&eventID)
		if err != nil {
			return BackfillApplicationSummary{}, fmt.Errorf("append private tracker history for source row %d: %w", row.SourceRowNumber, err)
		}
		appliedRows = append(appliedRows, appliedRow{row: row, eventID: eventID, before: beforeJSON, after: afterJSON})
	}
	if len(appliedRows) != preview.AcceptedRows {
		return BackfillApplicationSummary{}, fmt.Errorf("application row count changed during the locked preview")
	}
	safetyAfter, err := loadBackfillSafetyCounts(ctx, tx, caseIDs)
	if err != nil {
		return BackfillApplicationSummary{}, fmt.Errorf("record post-application safety snapshot: %w", err)
	}
	if safetyBefore != safetyAfter {
		return BackfillApplicationSummary{}, fmt.Errorf("safety check failed: tracker application changed a decision, effect, ledger, legacy sanction, task, correspondence or outbox row")
	}
	safetyJSON, _ := json.Marshal(map[string]any{"before": safetyBefore, "after": safetyAfter})
	var applicationID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO sanction_ineligible_backfill_applications(
			run_id,signoff_id,applied_by_admin_id,applied_by_name,application_note,
			accepted_rows,open_rows,closed_rows,unmatched_rows,excluded_rows,safety_snapshot
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
		RETURNING id
	`, runID, preview.SignoffID, adminID, appliedByName, note, preview.AcceptedRows,
		preview.OpenRows, preview.ClosedRows, preview.UnmatchedRows, preview.ExcludedRows,
		string(safetyJSON)).Scan(&applicationID)
	if err != nil {
		return BackfillApplicationSummary{}, fmt.Errorf("record tracker application: %w", err)
	}
	for _, item := range appliedRows {
		if _, err = tx.Exec(ctx, `
			INSERT INTO sanction_ineligible_backfill_application_rows(
				application_id,backfill_row_id,review_id,intake_id,case_id,case_event_id,
				source_row_sha256,reviewed_case_state,before_case_state,after_case_state
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb)
		`, applicationID, item.row.BackfillRowID, item.row.ReviewID, item.row.IntakeID,
			item.row.Case.ID, item.eventID, item.row.RowSHA256, item.row.ReviewedCaseState,
			string(item.before), string(item.after)); err != nil {
			return BackfillApplicationSummary{}, fmt.Errorf("record applied tracker row %d: %w", item.row.SourceRowNumber, err)
		}
		if item.row.ReviewedCaseState == "closed" {
			historyJSON, marshalErr := json.Marshal(item.row.ManualHistory)
			if marshalErr != nil {
				return BackfillApplicationSummary{}, fmt.Errorf("encode historical outcome for tracker row %d: %w", item.row.SourceRowNumber, marshalErr)
			}
			if _, err = tx.Exec(ctx, `
				INSERT INTO sanction_historical_outcome_snapshots(
					case_id,application_id,backfill_row_id,source_row_number,source_row_sha256,
					manual_history,effects_review_status,effect_interpretation,recorded_by_admin_id
				) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,NULLIF($8,''),$9)
			`, item.row.Case.ID, applicationID, item.row.BackfillRowID, item.row.SourceRowNumber,
				item.row.RowSHA256, string(historyJSON), item.row.EffectsReviewStatus,
				item.row.EffectInterpretation, adminID); err != nil {
				return BackfillApplicationSummary{}, fmt.Errorf("record non-operative historical outcome for tracker row %d: %w", item.row.SourceRowNumber, err)
			}
		}
	}
	finalSafety, err := loadBackfillSafetyCounts(ctx, tx, caseIDs)
	if err != nil || finalSafety != safetyBefore {
		return BackfillApplicationSummary{}, fmt.Errorf("final tracker application safety check failed")
	}
	if err = tx.Commit(ctx); err != nil {
		return BackfillApplicationSummary{}, err
	}
	return BackfillApplicationSummary{
		ApplicationID: applicationID, RunID: runID, AcceptedRows: preview.AcceptedRows,
		OpenRows: preview.OpenRows, ClosedRows: preview.ClosedRows,
		UnmatchedRows: preview.UnmatchedRows, ExcludedRows: preview.ExcludedRows,
	}, nil
}
