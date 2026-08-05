-- Explicit, post-sign-off application of reviewed tracker history. Application
-- writes private case events and a case-status projection only. It has no
-- foreign keys or triggers into effects, ledgers, correspondence or outbox.

ALTER TABLE sanction_ineligible_backfill_signoffs
    ADD COLUMN IF NOT EXISTS review_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS sanction_ineligible_backfill_applications (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_runs(id) ON DELETE RESTRICT,
    signoff_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_signoffs(id) ON DELETE RESTRICT,
    applied_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    applied_by_name TEXT NOT NULL,
    application_note TEXT NOT NULL,
    accepted_rows INTEGER NOT NULL CHECK (accepted_rows > 0),
    open_rows INTEGER NOT NULL CHECK (open_rows >= 0),
    closed_rows INTEGER NOT NULL CHECK (closed_rows >= 0),
    unmatched_rows INTEGER NOT NULL CHECK (unmatched_rows >= 0),
    excluded_rows INTEGER NOT NULL CHECK (excluded_rows >= 0),
    safety_snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(run_id),
    CHECK (accepted_rows = open_rows + closed_rows)
);

CREATE TABLE IF NOT EXISTS sanction_ineligible_backfill_application_rows (
    id BIGSERIAL PRIMARY KEY,
    application_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_applications(id) ON DELETE RESTRICT,
    backfill_row_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_rows(id) ON DELETE RESTRICT,
    review_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_reviews(id) ON DELETE RESTRICT,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    case_event_id BIGINT NOT NULL REFERENCES sanction_case_events(id) ON DELETE RESTRICT,
    source_row_sha256 TEXT NOT NULL,
    reviewed_case_state TEXT NOT NULL CHECK (reviewed_case_state IN ('open','closed')),
    before_case_state JSONB NOT NULL,
    after_case_state JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(application_id, backfill_row_id),
    UNIQUE(case_event_id),
    UNIQUE(intake_id, source_row_sha256)
);

CREATE INDEX IF NOT EXISTS idx_sanction_ineligible_backfill_application_case
    ON sanction_ineligible_backfill_application_rows(case_id, application_id);

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'sanction_ineligible_backfill_applications',
    'sanction_ineligible_backfill_application_rows'
  ] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_immutable ON %I',t,t);
    EXECUTE format('CREATE TRIGGER trg_%I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change()',t,t);
  END LOOP;
END $$;
