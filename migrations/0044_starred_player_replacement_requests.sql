CREATE TABLE IF NOT EXISTS starred_player_replacement_requests (
    id                     BIGSERIAL PRIMARY KEY,
    season_year            INTEGER NOT NULL,
    club_name              TEXT NOT NULL,
    club_key               TEXT NOT NULL,
    player_name            TEXT NOT NULL,
    player_key             TEXT NOT NULL,
    play_cricket_player_id BIGINT,
    list_type              TEXT NOT NULL CHECK (list_type IN ('A', 'B')),
    review_signal          TEXT NOT NULL CHECK (review_signal IN ('green', 'orange', 'red')),
    total_games            INTEGER NOT NULL DEFAULT 0,
    rule_games             INTEGER NOT NULL DEFAULT 0,
    rule_percentage        NUMERIC(5,2) NOT NULL DEFAULT 0,
    captain_id             INTEGER REFERENCES captains(id) ON DELETE SET NULL,
    captain_name           TEXT,
    captain_email          TEXT,
    email_subject          TEXT NOT NULL,
    email_body             TEXT NOT NULL,
    status                 TEXT NOT NULL DEFAULT 'draft'
                           CHECK (status IN ('draft', 'sending', 'send_failed', 'sent')),
    created_by             INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    sent_by                INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    sent_at                TIMESTAMPTZ,
    send_error             TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_starred_replacement_requests_status_created
    ON starred_player_replacement_requests(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_starred_replacement_requests_player
    ON starred_player_replacement_requests(season_year, club_key, player_key);
