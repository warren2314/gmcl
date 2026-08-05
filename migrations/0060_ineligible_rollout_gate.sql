-- Fail-closed rollout state for the ineligible-player intake replacement.
-- Scheduled observations and activation are append-only audit records. The
-- mutable clean_cycles column remains a dashboard projection only.

CREATE TABLE IF NOT EXISTS sanction_ineligible_scheduled_reconciliations (
    id BIGSERIAL PRIMARY KEY,
    sync_run_id BIGINT NOT NULL UNIQUE REFERENCES sanction_intake_sync_runs(id) ON DELETE RESTRICT,
    reconciliation_date DATE NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('succeeded','failed','partial')),
    observed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ineligible_scheduled_reconciliation_date
    ON sanction_ineligible_scheduled_reconciliations(reconciliation_date DESC, id DESC);

CREATE TABLE IF NOT EXISTS sanction_ineligible_rollout_activations (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    prerequisite_application_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_applications(id) ON DELETE RESTRICT,
    activation_sync_run_id BIGINT NOT NULL UNIQUE REFERENCES sanction_intake_sync_runs(id) ON DELETE RESTRICT,
    clean_reconciliation_dates JSONB NOT NULL,
    activated_at TIMESTAMPTZ NOT NULL,
    google_grace_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (google_grace_until = activated_at + interval '30 days'),
    CHECK (jsonb_typeof(clean_reconciliation_dates) = 'array' AND jsonb_array_length(clean_reconciliation_dates) = 3)
);

CREATE OR REPLACE VIEW sanction_ineligible_rollout_status AS
SELECT
    application.id AS prerequisite_application_id,
    signoff.signatory_name,
    application.created_at AS prerequisite_applied_at,
    CASE
      WHEN application.id IS NULL OR settings.last_reconciled_at IS NULL
           OR settings.last_reconciled_at < application.created_at THEN 0
      ELSE COALESCE(settings.clean_cycles,0)
    END AS clean_reconciliation_dates,
    settings.last_reconciled_at,
    activation.activated_at,
    activation.google_grace_until,
    CASE
      WHEN activation.id IS NULL THEN 'pending'
      WHEN now() < activation.google_grace_until THEN 'active_google_grace'
      ELSE 'native_active_google_retired'
    END AS rollout_state
FROM (SELECT 1 AS singleton) singleton
LEFT JOIN LATERAL (
    SELECT applied.* FROM sanction_ineligible_backfill_applications applied
    ORDER BY applied.created_at DESC,applied.id DESC LIMIT 1
) application ON TRUE
LEFT JOIN sanction_ineligible_backfill_signoffs signoff ON signoff.id=application.signoff_id
LEFT JOIN sanction_automation_settings settings ON settings.source_type='ineligible_player'
LEFT JOIN sanction_ineligible_rollout_activations activation ON activation.id=TRUE;

DROP TRIGGER IF EXISTS trg_sanction_ineligible_scheduled_reconciliations_immutable
    ON sanction_ineligible_scheduled_reconciliations;
CREATE TRIGGER trg_sanction_ineligible_scheduled_reconciliations_immutable
BEFORE UPDATE OR DELETE ON sanction_ineligible_scheduled_reconciliations
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();

DROP TRIGGER IF EXISTS trg_sanction_ineligible_rollout_activations_immutable
    ON sanction_ineligible_rollout_activations;
CREATE TRIGGER trg_sanction_ineligible_rollout_activations_immutable
BEFORE UPDATE OR DELETE ON sanction_ineligible_rollout_activations
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
