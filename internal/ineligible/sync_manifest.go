package ineligible

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func applyDispositionName(disposition ApplyDisposition) string {
	switch disposition {
	case ApplyNew:
		return "new"
	case ApplyChanged:
		return "changed"
	case ApplyException:
		return "exception"
	default:
		return "unchanged"
	}
}

// commitGoogleResolvedRow retains one immutable manifest entry for every
// successfully resolved source row, including rows whose content was
// unchanged. The manifest and intake write share the same transaction so a
// selection page can never be built from partial provenance.
func commitGoogleResolvedRow(ctx context.Context, tx pgx.Tx, runID int64, row IntakeRow, intakeID, revisionID int64, disposition ApplyDisposition) (ApplyDisposition, error) {
	if intakeID < 1 || revisionID < 1 {
		return ApplyUnchanged, fmt.Errorf("resolved Google row is missing intake revision provenance")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sanction_intake_sync_run_rows(
			sync_run_id,source_row_number,external_key,raw_sha256,resolution_status,
			apply_disposition,intake_id,revision_id
		) VALUES($1,$2,$3,$4,'resolved',$5,$6,$7)
	`, runID, row.SourceRowNumber, row.ExternalKey, row.RawSHA256,
		applyDispositionName(disposition), intakeID, revisionID); err != nil {
		return ApplyUnchanged, fmt.Errorf("record resolved Google sync row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ApplyUnchanged, err
	}
	return disposition, nil
}

// commitGoogleUnresolvedRow records the source row without claiming that it
// belongs to a particular intake. This keeps identity conflicts visible while
// preventing them from being selected or hidden as if the match were certain.
func commitGoogleUnresolvedRow(ctx context.Context, tx pgx.Tx, runID int64, row IntakeRow, message string) (ApplyDisposition, error) {
	message = truncateMessage(strings.TrimSpace(message), 2000)
	if message == "" {
		message = "Google source-row identity could not be resolved"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sanction_intake_sync_run_rows(
			sync_run_id,source_row_number,external_key,raw_sha256,resolution_status,
			apply_disposition,error_message
		) VALUES($1,$2,$3,$4,'unresolved',$5,$6)
	`, runID, row.SourceRowNumber, row.ExternalKey, row.RawSHA256,
		applyDispositionName(ApplyException), message); err != nil {
		return ApplyUnchanged, fmt.Errorf("record unresolved Google sync row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ApplyUnchanged, err
	}
	return ApplyException, nil
}
