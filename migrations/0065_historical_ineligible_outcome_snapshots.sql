-- Closed tracker rows are retained as readable, non-operative historical
-- outcomes. They deliberately do not populate decision/effect/ledger/outbox
-- tables: free-text points/cards must still be interpreted through the normal
-- independently approved decision workflow before affecting any balance.

CREATE UNIQUE INDEX IF NOT EXISTS uq_ineligible_backfill_row_snapshot_parent
    ON sanction_ineligible_backfill_rows(id,source_row_number,row_sha256);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ineligible_application_row_snapshot_parent
    ON sanction_ineligible_backfill_application_rows(
        application_id,backfill_row_id,case_id,source_row_sha256
    );

CREATE TABLE IF NOT EXISTS sanction_historical_outcome_snapshots (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    application_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_applications(id) ON DELETE RESTRICT,
    backfill_row_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_rows(id) ON DELETE RESTRICT,
    source_row_number INTEGER NOT NULL CHECK (source_row_number >= 2),
    source_row_sha256 TEXT NOT NULL CHECK (source_row_sha256 ~ '^[0-9a-f]{64}$'),
    manual_history JSONB NOT NULL,
    effects_review_status TEXT NOT NULL CHECK (effects_review_status IN
        ('not_applicable','pending_manual_interpretation','manually_interpreted','confirmed_no_effect')),
    effect_interpretation TEXT,
    recorded_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(backfill_row_id),
    CHECK (jsonb_typeof(manual_history) = 'object')
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_historical_outcome_application_row'
          AND conrelid='sanction_historical_outcome_snapshots'::regclass
    ) THEN
        ALTER TABLE sanction_historical_outcome_snapshots
            ADD CONSTRAINT fk_historical_outcome_application_row
            FOREIGN KEY(application_id,backfill_row_id,case_id,source_row_sha256)
            REFERENCES sanction_ineligible_backfill_application_rows(
                application_id,backfill_row_id,case_id,source_row_sha256
            ) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_historical_outcome_source_row'
          AND conrelid='sanction_historical_outcome_snapshots'::regclass
    ) THEN
        ALTER TABLE sanction_historical_outcome_snapshots
            ADD CONSTRAINT fk_historical_outcome_source_row
            FOREIGN KEY(backfill_row_id,source_row_number,source_row_sha256)
            REFERENCES sanction_ineligible_backfill_rows(id,source_row_number,row_sha256)
            ON DELETE RESTRICT;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_sanction_historical_outcome_case
    ON sanction_historical_outcome_snapshots(case_id, id DESC);

DROP TRIGGER IF EXISTS trg_sanction_historical_outcome_snapshots_immutable
    ON sanction_historical_outcome_snapshots;
CREATE TRIGGER trg_sanction_historical_outcome_snapshots_immutable
BEFORE UPDATE OR DELETE ON sanction_historical_outcome_snapshots
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
