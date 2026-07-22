-- Identity corrections must be reversible without losing who changed what.
CREATE TABLE IF NOT EXISTS starred_identity_mapping_events (
    id                  BIGSERIAL PRIMARY KEY,
    mapping_id          BIGINT NOT NULL REFERENCES starred_identity_mappings(id) ON DELETE RESTRICT,
    season_year         INTEGER NOT NULL,
    club_key            TEXT NOT NULL,
    starred_player_key  TEXT NOT NULL,
    action              TEXT NOT NULL CHECK (action IN ('confirmed','revoked')),
    before_data         JSONB,
    after_data          JSONB NOT NULL,
    reason              TEXT NOT NULL,
    actor_admin_id      INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_starred_identity_mapping_events_mapping
    ON starred_identity_mapping_events(mapping_id,created_at DESC);

DROP TRIGGER IF EXISTS trg_starred_identity_mapping_events_immutable ON starred_identity_mapping_events;
CREATE TRIGGER trg_starred_identity_mapping_events_immutable
BEFORE UPDATE OR DELETE ON starred_identity_mapping_events
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
