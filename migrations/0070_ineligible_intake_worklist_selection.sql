-- Reversible, append-only selection of Google Form reports for the normal
-- ineligible-player work queue. Source rows and intake lifecycle state remain
-- untouched: a deferred report is hidden from the default queue, not deleted,
-- ignored, resolved, or made inaccessible for audit.

CREATE TABLE IF NOT EXISTS sanction_intake_sync_run_rows (
    id BIGSERIAL PRIMARY KEY,
    sync_run_id BIGINT NOT NULL REFERENCES sanction_intake_sync_runs(id) ON DELETE RESTRICT,
    source_row_number INTEGER NOT NULL CHECK (source_row_number >= 2),
    external_key TEXT NOT NULL CHECK (NULLIF(BTRIM(external_key),'') IS NOT NULL),
    raw_sha256 TEXT NOT NULL CHECK (raw_sha256 ~ '^[0-9a-f]{64}$'),
    resolution_status TEXT NOT NULL CHECK (resolution_status IN ('resolved','unresolved')),
    apply_disposition TEXT NOT NULL CHECK (apply_disposition IN ('new','changed','unchanged','exception')),
    intake_id BIGINT,
    revision_id BIGINT,
    error_message TEXT,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(sync_run_id, source_row_number),
    UNIQUE(id, sync_run_id, intake_id, revision_id),
    FOREIGN KEY (revision_id, intake_id)
        REFERENCES sanction_intake_revisions(id, intake_id) ON DELETE RESTRICT,
    CHECK (
        (resolution_status='resolved' AND intake_id IS NOT NULL AND revision_id IS NOT NULL AND error_message IS NULL)
        OR
        (resolution_status='unresolved' AND intake_id IS NULL AND revision_id IS NULL AND NULLIF(BTRIM(error_message),'') IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_sanction_intake_sync_run_rows_intake
    ON sanction_intake_sync_run_rows(intake_id, sync_run_id DESC)
    WHERE intake_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_intake_sync_run_rows_resolved_intake
    ON sanction_intake_sync_run_rows(sync_run_id, intake_id)
    WHERE resolution_status='resolved';

CREATE TABLE IF NOT EXISTS sanction_intake_worklist_batches (
    id BIGSERIAL PRIMARY KEY,
    sync_run_id BIGINT NOT NULL REFERENCES sanction_intake_sync_runs(id) ON DELETE RESTRICT,
    supersedes_id BIGINT REFERENCES sanction_intake_worklist_batches(id) ON DELETE RESTRICT,
    candidate_count INTEGER NOT NULL CHECK (candidate_count > 0),
    selected_count INTEGER NOT NULL CHECK (selected_count > 0),
    deferred_count INTEGER NOT NULL CHECK (deferred_count >= 0),
    candidate_sha256 TEXT NOT NULL CHECK (candidate_sha256 ~ '^[0-9a-f]{64}$'),
    selection_sha256 TEXT NOT NULL CHECK (selection_sha256 ~ '^[0-9a-f]{64}$'),
    reason TEXT NOT NULL CHECK (char_length(BTRIM(reason)) BETWEEN 3 AND 200),
    created_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_by_label TEXT NOT NULL CHECK (NULLIF(BTRIM(created_by_label),'') IS NOT NULL),
    request_id TEXT CHECK (request_id IS NULL OR NULLIF(BTRIM(request_id),'') IS NOT NULL),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(id, sync_run_id),
    CHECK (selected_count + deferred_count = candidate_count),
    CHECK (supersedes_id IS NULL OR supersedes_id < id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_intake_worklist_batches_request
    ON sanction_intake_worklist_batches(request_id)
    WHERE request_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS sanction_intake_worklist_decisions (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL,
    sync_run_id BIGINT NOT NULL,
    intake_id BIGINT NOT NULL,
    revision_id BIGINT NOT NULL,
    sync_run_row_id BIGINT,
    visibility TEXT NOT NULL CHECK (visibility IN ('visible','deferred')),
    previous_visibility TEXT NOT NULL CHECK (previous_visibility IN ('visible','deferred')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(batch_id, intake_id),
    FOREIGN KEY (revision_id, intake_id)
        REFERENCES sanction_intake_revisions(id, intake_id) ON DELETE RESTRICT,
    FOREIGN KEY (batch_id, sync_run_id)
        REFERENCES sanction_intake_worklist_batches(id, sync_run_id) ON DELETE RESTRICT,
    FOREIGN KEY (sync_run_row_id, sync_run_id, intake_id, revision_id)
        REFERENCES sanction_intake_sync_run_rows(id, sync_run_id, intake_id, revision_id) ON DELETE RESTRICT,
    CHECK (visibility='deferred' OR sync_run_row_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_sanction_intake_worklist_decisions_current
    ON sanction_intake_worklist_decisions(intake_id, id DESC);

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'sanction_intake_sync_run_rows',
    'sanction_intake_worklist_batches',
    'sanction_intake_worklist_decisions'
  ] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_immutable ON %I',t,t);
    EXECUTE format('CREATE TRIGGER trg_%I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change()',t,t);
  END LOOP;
END $$;

CREATE OR REPLACE VIEW sanction_intake_worklist_current AS
SELECT intake.id AS intake_id,
       COALESCE(latest.visibility, 'visible') AS visibility,
       latest.batch_id,
       latest.reason,
       latest.selected_at,
       latest.selected_by
FROM sanction_intakes intake
LEFT JOIN LATERAL (
    SELECT decision.visibility,
           batch.id AS batch_id,
           batch.reason,
           batch.created_at AS selected_at,
           COALESCE(NULLIF(batch.created_by_label,''), admin.username, 'Administrator') AS selected_by
    FROM sanction_intake_worklist_decisions decision
    JOIN sanction_intake_worklist_batches batch ON batch.id=decision.batch_id
    LEFT JOIN admin_users admin ON admin.id=batch.created_by_admin_id
    WHERE decision.intake_id=intake.id
    ORDER BY decision.id DESC
    LIMIT 1
) latest ON TRUE;
