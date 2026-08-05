-- Immutable local copies of Google Form file uploads. The bytes live below
-- INELIGIBLE_UPLOAD_DIR at the content-addressed storage_key; this table binds
-- each retained object to the exact append-only intake revision that observed it.

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_intake_revision_parent
    ON sanction_intake_revisions(id, intake_id);

CREATE TABLE IF NOT EXISTS sanction_intake_attachments (
    id BIGSERIAL PRIMARY KEY,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    revision_id BIGINT NOT NULL,
    sync_run_id BIGINT REFERENCES sanction_intake_sync_runs(id) ON DELETE RESTRICT,
    google_drive_file_id TEXT NOT NULL CHECK (google_drive_file_id ~ '^[A-Za-z0-9_-]{10,200}$'),
    source_url TEXT,
    original_filename TEXT NOT NULL CHECK (NULLIF(BTRIM(original_filename),'') IS NOT NULL),
    content_type TEXT NOT NULL CHECK (content_type LIKE '%/%'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key TEXT NOT NULL CHECK (storage_key ~ '^sha256/[0-9a-f]{2}/[0-9a-f]{64}$'),
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY(revision_id,intake_id)
        REFERENCES sanction_intake_revisions(id,intake_id) ON DELETE RESTRICT,
    UNIQUE(revision_id, google_drive_file_id, sha256)
);

CREATE INDEX IF NOT EXISTS idx_sanction_intake_attachments_intake
    ON sanction_intake_attachments(intake_id, revision_id, id);
CREATE INDEX IF NOT EXISTS idx_sanction_intake_attachments_sha
    ON sanction_intake_attachments(sha256);

DROP TRIGGER IF EXISTS trg_sanction_intake_attachments_immutable ON sanction_intake_attachments;
CREATE TRIGGER trg_sanction_intake_attachments_immutable
BEFORE UPDATE OR DELETE ON sanction_intake_attachments
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
