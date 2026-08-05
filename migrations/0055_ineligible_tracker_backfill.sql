-- Immutable reconciliation ledger for the 2026 ineligible-player tracker.
-- Uploading or reviewing a tracker never creates a sanction effect, changes a
-- case status, or queues correspondence.  Any later operational migration must
-- use the explicitly reviewed state and manually interpreted effect notes.

CREATE TABLE IF NOT EXISTS sanction_ineligible_backfill_runs (
    id BIGSERIAL PRIMARY KEY,
    source_filename TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    byte_size BIGINT NOT NULL CHECK (byte_size > 0),
    source_sha256 TEXT NOT NULL,
    source_sheet TEXT NOT NULL CHECK (source_sheet = 'Form responses 1'),
    header_sha256 TEXT NOT NULL,
    rows_total INTEGER NOT NULL CHECK (rows_total >= 0),
    rows_matched_exact INTEGER NOT NULL DEFAULT 0 CHECK (rows_matched_exact >= 0),
    rows_matched_normalized INTEGER NOT NULL DEFAULT 0 CHECK (rows_matched_normalized >= 0),
    rows_unmatched INTEGER NOT NULL DEFAULT 0 CHECK (rows_unmatched >= 0),
    rows_ambiguous INTEGER NOT NULL DEFAULT 0 CHECK (rows_ambiguous >= 0),
    rows_invalid INTEGER NOT NULL DEFAULT 0 CHECK (rows_invalid >= 0),
    rows_with_manual_history INTEGER NOT NULL DEFAULT 0 CHECK (rows_with_manual_history >= 0),
    rows_requiring_effect_review INTEGER NOT NULL DEFAULT 0 CHECK (rows_requiring_effect_review >= 0),
    uploaded_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        rows_total = rows_matched_exact + rows_matched_normalized +
                     rows_unmatched + rows_ambiguous + rows_invalid
    )
);

CREATE INDEX IF NOT EXISTS idx_sanction_ineligible_backfill_runs_recent
    ON sanction_ineligible_backfill_runs(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_sanction_ineligible_backfill_runs_sha
    ON sanction_ineligible_backfill_runs(source_sha256, id DESC);

CREATE TABLE IF NOT EXISTS sanction_ineligible_backfill_rows (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_runs(id) ON DELETE RESTRICT,
    source_row_number INTEGER NOT NULL CHECK (source_row_number >= 2),
    row_sha256 TEXT NOT NULL,
    form_data JSONB NOT NULL,
    manual_history JSONB NOT NULL,
    submitted_at TIMESTAMPTZ,
    fixture_date DATE,
    player_text TEXT,
    offending_club_text TEXT,
    team_text TEXT,
    match_status TEXT NOT NULL CHECK (match_status IN
        ('matched_exact','matched_normalized','unmatched','ambiguous','invalid')),
    matched_intake_id BIGINT REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    candidate_intake_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    exception_message TEXT,
    tracker_state_hint TEXT NOT NULL DEFAULT 'unknown' CHECK (tracker_state_hint IN
        ('open','closed','unknown')),
    points_text TEXT,
    cards_text TEXT,
    requires_effect_review BOOLEAN NOT NULL DEFAULT FALSE,
    has_manual_history BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(run_id, source_row_number),
    CHECK (
        (match_status IN ('matched_exact','matched_normalized') AND matched_intake_id IS NOT NULL) OR
        (match_status NOT IN ('matched_exact','matched_normalized') AND matched_intake_id IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_sanction_ineligible_backfill_rows_run
    ON sanction_ineligible_backfill_rows(run_id, source_row_number);
CREATE INDEX IF NOT EXISTS idx_sanction_ineligible_backfill_rows_match
    ON sanction_ineligible_backfill_rows(match_status, run_id);

-- Corrections are new review rows.  The latest review is the current human
-- interpretation, while every earlier interpretation remains auditable.
CREATE TABLE IF NOT EXISTS sanction_ineligible_backfill_reviews (
    id BIGSERIAL PRIMARY KEY,
    backfill_row_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_rows(id) ON DELETE RESTRICT,
    supersedes_id BIGINT REFERENCES sanction_ineligible_backfill_reviews(id) ON DELETE RESTRICT,
    disposition TEXT NOT NULL CHECK (disposition IN
        ('accept_match','leave_unmatched','exclude_tracker_row')),
    reviewed_intake_id BIGINT REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    reviewed_case_state TEXT NOT NULL CHECK (reviewed_case_state IN
        ('open','closed','needs_interpretation')),
    effects_review_status TEXT NOT NULL CHECK (effects_review_status IN
        ('not_applicable','pending_manual_interpretation','manually_interpreted','confirmed_no_effect')),
    effect_interpretation TEXT,
    review_reason TEXT NOT NULL,
    reviewed_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    reviewed_by_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (disposition='accept_match' AND reviewed_intake_id IS NOT NULL) OR
        (disposition<>'accept_match' AND reviewed_intake_id IS NULL)
    ),
    CHECK (
        effects_review_status NOT IN ('manually_interpreted','confirmed_no_effect') OR
        NULLIF(BTRIM(effect_interpretation),'') IS NOT NULL
    )
);

CREATE INDEX IF NOT EXISTS idx_sanction_ineligible_backfill_reviews_latest
    ON sanction_ineligible_backfill_reviews(backfill_row_id, id DESC);

CREATE TABLE IF NOT EXISTS sanction_ineligible_backfill_signoffs (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES sanction_ineligible_backfill_runs(id) ON DELETE RESTRICT,
    signatory_name TEXT NOT NULL,
    signoff_statement TEXT NOT NULL,
    reconciliation_totals JSONB NOT NULL,
    signed_off_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sanction_ineligible_backfill_signoffs_run
    ON sanction_ineligible_backfill_signoffs(run_id, id DESC);

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'sanction_ineligible_backfill_runs',
    'sanction_ineligible_backfill_rows',
    'sanction_ineligible_backfill_reviews',
    'sanction_ineligible_backfill_signoffs'
  ] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_immutable ON %I',t,t);
    EXECUTE format('CREATE TRIGGER trg_%I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change()',t,t);
  END LOOP;
END $$;
