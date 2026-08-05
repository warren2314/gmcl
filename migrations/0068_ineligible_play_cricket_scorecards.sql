-- Immutable Play-Cricket scorecard snapshots collected for ineligible-player
-- investigations. The evidence row retains the original API response bytes;
-- this table records the exact upstream match and checksum provenance.

CREATE TABLE IF NOT EXISTS sanction_case_scorecard_evidence (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    evidence_id BIGINT NOT NULL,
    play_cricket_match_id BIGINT NOT NULL CHECK (play_cricket_match_id > 0),
    source_last_updated TEXT,
    source_sha256 TEXT NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    snapshot_payload JSONB NOT NULL,
    fetched_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(case_id,play_cricket_match_id,source_sha256),
    UNIQUE(evidence_id),
    FOREIGN KEY(evidence_id,case_id)
        REFERENCES sanction_case_evidence(id,case_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_sanction_case_scorecard_match
    ON sanction_case_scorecard_evidence(play_cricket_match_id,fetched_at DESC);

DROP TRIGGER IF EXISTS trg_sanction_case_scorecard_evidence_immutable
    ON sanction_case_scorecard_evidence;
CREATE TRIGGER trg_sanction_case_scorecard_evidence_immutable
BEFORE UPDATE OR DELETE ON sanction_case_scorecard_evidence
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
