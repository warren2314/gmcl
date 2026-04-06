-- Admin invite and forced password change support.

-- Track whether the admin must change their password on next login.
ALTER TABLE admin_users
    ADD COLUMN IF NOT EXISTS force_password_change BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS invited_by_admin_id   INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS invited_at            TIMESTAMPTZ;

-- Single-use invite tokens (emailed to new admins).
-- Token is stored as a SHA-256 hash so the plaintext never hits the DB.
CREATE TABLE IF NOT EXISTS admin_invite_tokens (
    id              BIGSERIAL PRIMARY KEY,
    admin_user_id   INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    token_hash      BYTEA   NOT NULL UNIQUE,
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_admin_invite_tokens_user ON admin_invite_tokens(admin_user_id);
