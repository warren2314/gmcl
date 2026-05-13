CREATE TABLE IF NOT EXISTS admin_password_change_otps (
    id BIGSERIAL PRIMARY KEY,
    admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL,
    new_password_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip_address INET
);

CREATE INDEX IF NOT EXISTS idx_admin_password_change_otps_admin
    ON admin_password_change_otps(admin_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_admin_password_change_otps_active
    ON admin_password_change_otps(admin_user_id, expires_at)
    WHERE used_at IS NULL;
