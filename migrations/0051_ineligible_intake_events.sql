-- Append-only audit history for intake triage actions which do not always
-- create a sanction case (for example, marking an irrelevant report ignored).

CREATE TABLE IF NOT EXISTS sanction_intake_events (
    id BIGSERIAL PRIMARY KEY,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    event_type TEXT NOT NULL CHECK (event_type IN
        ('case_created','case_linked','marked_duplicate','ignored')),
    actor_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    actor_label TEXT,
    reason TEXT NOT NULL,
    before_data JSONB,
    after_data JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sanction_intake_events_intake
    ON sanction_intake_events(intake_id, id DESC);

DROP TRIGGER IF EXISTS trg_sanction_intake_events_immutable ON sanction_intake_events;
CREATE TRIGGER trg_sanction_intake_events_immutable
BEFORE UPDATE OR DELETE ON sanction_intake_events
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
