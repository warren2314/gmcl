-- CSV preview tokens for single-use, DB-backed previews
CREATE TABLE csv_preview_tokens (
    id UUID PRIMARY KEY,
    token_hash BYTEA NOT NULL UNIQUE,
    admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    checksum TEXT NOT NULL,
    preview_json JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_csv_preview_admin ON csv_preview_tokens(admin_user_id);

-- Magic link send log for per-captain throttling
CREATE TABLE magic_link_send_log (
    id BIGSERIAL PRIMARY KEY,
    captain_id INTEGER NOT NULL REFERENCES captains(id) ON DELETE CASCADE,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    token_id BIGINT REFERENCES magic_link_tokens(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_magic_send_window ON magic_link_send_log (captain_id, season_id, week_id, created_at);

