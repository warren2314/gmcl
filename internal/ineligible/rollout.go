package ineligible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
)

var (
	ErrBackfillPrerequisite = errors.New("named tracker reconciliation sign-off and application are required before daily import")
	ErrGoogleImportRetired  = errors.New("scheduled Google intake has completed its 30-day grace period")
)

// RolloutStatus is the operational gate shown to administrators and used by
// both scheduled Google intake and the public native form.
type RolloutStatus struct {
	PrerequisiteApplicationID *int64     `json:"prerequisite_application_id,omitempty"`
	SignatoryName             string     `json:"signatory_name,omitempty"`
	PrerequisiteAppliedAt     *time.Time `json:"prerequisite_applied_at,omitempty"`
	CleanDates                int        `json:"clean_dates"`
	LastReconciledAt          *time.Time `json:"last_reconciled_at,omitempty"`
	ActivatedAt               *time.Time `json:"activated_at,omitempty"`
	GoogleGraceUntil          *time.Time `json:"google_grace_until,omitempty"`
	State                     string     `json:"state"`
}

func GetRolloutStatus(ctx context.Context, pool *db.Pool) (RolloutStatus, error) {
	if pool == nil {
		return RolloutStatus{}, fmt.Errorf("database pool is nil")
	}
	var status RolloutStatus
	err := pool.QueryRow(ctx, `
		SELECT prerequisite_application_id,COALESCE(signatory_name,''),prerequisite_applied_at,
		       clean_reconciliation_dates,last_reconciled_at,activated_at,google_grace_until,rollout_state
		FROM sanction_ineligible_rollout_status
	`).Scan(&status.PrerequisiteApplicationID, &status.SignatoryName, &status.PrerequisiteAppliedAt,
		&status.CleanDates, &status.LastReconciledAt, &status.ActivatedAt, &status.GoogleGraceUntil, &status.State)
	if err != nil {
		return RolloutStatus{}, fmt.Errorf("load ineligible-player rollout status: %w", err)
	}
	return status, nil
}

func (s *PGStore) authorizeRollout(ctx context.Context, trigger Trigger, bootstrapEnabled bool) (RolloutStatus, error) {
	status, err := GetRolloutStatus(ctx, s.pool)
	if err != nil {
		return RolloutStatus{}, err
	}
	return status, rolloutGateError(status, trigger, bootstrapEnabled)
}

func rolloutGateError(status RolloutStatus, trigger Trigger, bootstrapEnabled bool) error {
	if status.PrerequisiteApplicationID == nil {
		// Bootstrap is deliberately limited to a named administrator action.
		// It exists only to stage the Google sheet needed to reconcile the
		// historical tracker; the daily scheduler never bypasses the gate.
		if trigger.Type == "admin" && trigger.AdminID != nil && bootstrapEnabled {
			return nil
		}
		return ErrBackfillPrerequisite
	}
	if trigger.Type == "n8n" && status.State == "native_active_google_retired" {
		return ErrGoogleImportRetired
	}
	return nil
}

type effectiveReconciliationDate struct {
	Date       time.Time
	Successful bool
}

// cleanScheduledDateStreak counts distinct, adjacent London calendar dates.
// Its input is one failure-dominant aggregate per date, newest first.
func cleanScheduledDateStreak(days []effectiveReconciliationDate) (int, []string) {
	if len(days) == 0 || !days[0].Successful {
		return 0, nil
	}
	count := 1
	dates := []string{days[0].Date.Format("2006-01-02")}
	previous := days[0].Date
	for _, day := range days[1:] {
		if count == 3 || !day.Successful || !day.Date.Equal(previous.AddDate(0, 0, -1)) {
			break
		}
		count++
		dates = append(dates, day.Date.Format("2006-01-02"))
		previous = day.Date
	}
	return count, dates
}

// recordScheduledRollout records every scheduled result append-only. Multiple
// runs on one London date are collapsed with failure dominance, so retries
// cannot increment the gate and any failed/partial attempt resets the streak.
func recordScheduledRollout(ctx context.Context, tx pgx.Tx, summary Summary) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO sanction_ineligible_scheduled_reconciliations(
			sync_run_id,reconciliation_date,outcome,observed_at
		)
		SELECT id,(completed_at AT TIME ZONE 'Europe/London')::date,status,completed_at
		FROM sanction_intake_sync_runs WHERE id=$1 AND triggered_by_type='n8n'
		ON CONFLICT(sync_run_id) DO NOTHING
	`, summary.RunID)
	if err != nil {
		return fmt.Errorf("record scheduled reconciliation date: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}

	rows, err := tx.Query(ctx, `
		WITH prerequisite AS (
			SELECT application.created_at
			FROM sanction_ineligible_backfill_applications application
			ORDER BY application.created_at DESC,application.id DESC
			LIMIT 1
		)
		SELECT reconciliation_date,BOOL_AND(outcome='succeeded')
		FROM sanction_ineligible_scheduled_reconciliations,prerequisite
		WHERE observed_at>=prerequisite.created_at
		GROUP BY reconciliation_date
		ORDER BY reconciliation_date DESC
		LIMIT 32
	`)
	if err != nil {
		return fmt.Errorf("load scheduled reconciliation dates: %w", err)
	}
	defer rows.Close()
	days := make([]effectiveReconciliationDate, 0, 32)
	for rows.Next() {
		var day effectiveReconciliationDate
		if err = rows.Scan(&day.Date, &day.Successful); err != nil {
			return fmt.Errorf("scan scheduled reconciliation date: %w", err)
		}
		days = append(days, day)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("read scheduled reconciliation dates: %w", err)
	}
	streak, cleanDates := cleanScheduledDateStreak(days)
	if _, err = tx.Exec(ctx, `
		UPDATE sanction_automation_settings
		SET clean_cycles=$1,last_reconciled_at=(SELECT completed_at FROM sanction_intake_sync_runs WHERE id=$2),updated_at=now()
		WHERE source_type='ineligible_player'
	`, streak, summary.RunID); err != nil {
		return fmt.Errorf("project scheduled reconciliation gate: %w", err)
	}
	if streak < 3 {
		return nil
	}

	var applicationID int64
	err = tx.QueryRow(ctx, `
		SELECT application.id
		FROM sanction_ineligible_backfill_applications application
		JOIN sanction_ineligible_backfill_signoffs signoff ON signoff.id=application.signoff_id
		WHERE NULLIF(BTRIM(signoff.signatory_name),'') IS NOT NULL
		ORDER BY application.created_at DESC,application.id DESC
		LIMIT 1
	`).Scan(&applicationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load named backfill prerequisite: %w", err)
	}
	// Present the immutable activation dates oldest-to-newest for operators.
	for left, right := 0, len(cleanDates)-1; left < right; left, right = left+1, right-1 {
		cleanDates[left], cleanDates[right] = cleanDates[right], cleanDates[left]
	}
	datesJSON, err := json.Marshal(cleanDates)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO sanction_ineligible_rollout_activations(
			id,prerequisite_application_id,activation_sync_run_id,clean_reconciliation_dates,activated_at,google_grace_until
		)
		SELECT TRUE,$1,$2,$3::jsonb,completed_at,completed_at+interval '30 days'
		FROM sanction_intake_sync_runs WHERE id=$2
		ON CONFLICT(id) DO NOTHING
	`, applicationID, summary.RunID, string(datesJSON)); err != nil {
		return fmt.Errorf("activate native ineligible-player intake: %w", err)
	}
	return nil
}
