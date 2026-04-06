-- Weekly captain delegation support and explicit club/panel umpire flags.

ALTER TABLE magic_link_tokens
    ADD COLUMN IF NOT EXISTS delegate_name TEXT,
    ADD COLUMN IF NOT EXISTS delegate_email TEXT;

CREATE TABLE IF NOT EXISTS captain_delegations (
    id BIGSERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    captain_id INTEGER NOT NULL REFERENCES captains(id) ON DELETE CASCADE,
    delegate_name TEXT,
    delegate_email TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_captain_delegations_week_team
    ON captain_delegations (season_id, week_id, team_id, created_at DESC);

ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS submitted_by_name TEXT,
    ADD COLUMN IF NOT EXISTS submitted_by_email TEXT,
    ADD COLUMN IF NOT EXISTS submitted_by_role TEXT NOT NULL DEFAULT 'captain'
        CHECK (submitted_by_role IN ('captain', 'delegate')),
    ADD COLUMN IF NOT EXISTS umpire1_type TEXT NOT NULL DEFAULT 'panel'
        CHECK (umpire1_type IN ('panel', 'club')),
    ADD COLUMN IF NOT EXISTS umpire2_type TEXT NOT NULL DEFAULT 'panel'
        CHECK (umpire2_type IN ('panel', 'club'));
