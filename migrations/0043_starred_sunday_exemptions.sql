ALTER TABLE starred_exemptions
    ADD COLUMN IF NOT EXISTS club_name TEXT,
    ADD COLUMN IF NOT EXISTS player_name TEXT,
    ADD COLUMN IF NOT EXISTS play_cricket_match_id BIGINT,
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'approved',
    ADD COLUMN IF NOT EXISTS wicket_keeper BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS decided_by INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS decided_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoked_by INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE starred_exemptions
SET status = 'approved',
    decided_at = COALESCE(decided_at, created_at)
WHERE status IS NULL OR status = '';

CREATE INDEX IF NOT EXISTS idx_starred_exemptions_season_status
    ON starred_exemptions(season_year, status, club_key);

CREATE INDEX IF NOT EXISTS idx_starred_exemptions_match
    ON starred_exemptions(play_cricket_match_id)
    WHERE play_cricket_match_id IS NOT NULL;
