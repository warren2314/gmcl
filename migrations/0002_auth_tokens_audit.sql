-- magic link tokens
CREATE TABLE magic_link_tokens (
    id BIGSERIAL PRIMARY KEY,
    captain_id INTEGER NOT NULL REFERENCES captains(id) ON DELETE CASCADE,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_ip INET,
    request_user_agent TEXT
);

CREATE UNIQUE INDEX ux_magic_link_token_hash ON magic_link_tokens(token_hash);
CREATE INDEX idx_magic_link_captain_season_week ON magic_link_tokens(captain_id, season_id, week_id, expires_at);

-- admin users and 2FA
CREATE TABLE admin_users (
    id SERIAL PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash BYTEA NOT NULL,
    email TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ,
    failed_login_attempts INTEGER NOT NULL DEFAULT 0,
    locked_until TIMESTAMPTZ
);

CREATE TABLE admin_2fa_codes (
    id BIGSERIAL PRIMARY KEY,
    admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip_address INET
);

CREATE INDEX idx_admin_2fa_admin ON admin_2fa_codes(admin_user_id);
CREATE UNIQUE INDEX ux_admin_2fa_unused ON admin_2fa_codes(admin_user_id, used_at) WHERE used_at IS NULL;

-- configuration
CREATE TABLE ranking_config (
    id SERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    settings JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (season_id)
);

CREATE TABLE report_config (
    id SERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    settings JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (season_id)
);

CREATE TABLE reminder_schedules (
    id SERIAL PRIMARY KEY,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    day_of_week INTEGER NOT NULL CHECK (day_of_week BETWEEN 0 AND 6),
    time_of_day TIME NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE INDEX idx_reminder_team_season ON reminder_schedules(team_id, season_id);

-- audit logs
CREATE TYPE actor_type AS ENUM ('admin', 'system', 'n8n');

CREATE TABLE audit_logs (
    id BIGSERIAL PRIMARY KEY,
    actor_type actor_type NOT NULL,
    actor_id INTEGER,
    action TEXT NOT NULL,
    entity_type TEXT,
    entity_id BIGINT,
    metadata JSONB,
    ip_address INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_created_at ON audit_logs(created_at);
CREATE INDEX idx_audit_actor_type ON audit_logs(actor_type);

-- AI summaries
CREATE TYPE ai_summary_status AS ENUM ('draft', 'approved', 'rejected');

CREATE TABLE ai_summaries (
    id BIGSERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    summary_json JSONB NOT NULL,
    status ai_summary_status NOT NULL DEFAULT 'draft',
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    approved_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX ux_ai_summaries_season_week ON ai_summaries(season_id, week_id);

