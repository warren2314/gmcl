-- Bring edits made through the transitional legacy ledger under the same
-- immutable human-action standard as sanction case management.

ALTER TABLE sanction_events ADD COLUMN IF NOT EXISTS actor_label TEXT;
ALTER TABLE sanction_events ADD COLUMN IF NOT EXISTS reason TEXT;
ALTER TABLE sanction_events ADD COLUMN IF NOT EXISTS before_data JSONB;
ALTER TABLE sanction_events ADD COLUMN IF NOT EXISTS after_data JSONB;
ALTER TABLE sanction_events ADD COLUMN IF NOT EXISTS request_id TEXT;

DROP TRIGGER IF EXISTS trg_sanction_events_immutable ON sanction_events;
CREATE TRIGGER trg_sanction_events_immutable
BEFORE UPDATE OR DELETE ON sanction_events
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();

CREATE TABLE IF NOT EXISTS sanction_follow_up_task_events (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES sanction_follow_up_tasks(id) ON DELETE RESTRICT,
    actor_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    actor_label TEXT,
    reason TEXT NOT NULL,
    before_data JSONB,
    after_data JSONB NOT NULL,
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_sanction_follow_up_task_events_immutable ON sanction_follow_up_task_events;
CREATE TRIGGER trg_sanction_follow_up_task_events_immutable
BEFORE UPDATE OR DELETE ON sanction_follow_up_task_events
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();

CREATE TABLE IF NOT EXISTS sanction_import_mapping_events (
    id BIGSERIAL PRIMARY KEY,
    import_row_id BIGINT NOT NULL REFERENCES sanction_import_rows(id) ON DELETE RESTRICT,
    actor_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    actor_label TEXT,
    reason TEXT NOT NULL,
    before_data JSONB,
    after_data JSONB NOT NULL,
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_sanction_import_mapping_events_immutable ON sanction_import_mapping_events;
CREATE TRIGGER trg_sanction_import_mapping_events_immutable
BEFORE UPDATE OR DELETE ON sanction_import_mapping_events
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
