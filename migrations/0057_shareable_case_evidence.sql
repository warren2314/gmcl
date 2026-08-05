-- Evidence is disclosed only through explicit append-only sharing decisions.
-- A later revocation is another event; the retained evidence row never changes.

CREATE TABLE IF NOT EXISTS sanction_evidence_sharing_events (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    evidence_id BIGINT NOT NULL REFERENCES sanction_case_evidence(id) ON DELETE RESTRICT,
    audience TEXT NOT NULL CHECK (audience IN ('offending_club')),
    action TEXT NOT NULL CHECK (action IN ('shared','revoked')),
    reason TEXT NOT NULL CHECK (NULLIF(BTRIM(reason),'') IS NOT NULL),
    actor_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sanction_evidence_sharing_current
    ON sanction_evidence_sharing_events(case_id,evidence_id,audience,id DESC);

DROP TRIGGER IF EXISTS trg_sanction_evidence_sharing_events_immutable ON sanction_evidence_sharing_events;
CREATE TRIGGER trg_sanction_evidence_sharing_events_immutable
BEFORE UPDATE OR DELETE ON sanction_evidence_sharing_events
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
