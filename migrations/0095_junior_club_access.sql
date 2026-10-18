-- Junior authentication is deliberately separate from staff and captain accounts.
CREATE TABLE junior_accounts (
    id BIGSERIAL PRIMARY KEY,
    email TEXT NOT NULL UNIQUE CHECK (email = lower(btrim(email)) AND length(email) BETWEEN 3 AND 254),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE junior_memberships (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES junior_accounts(id),
    club_id INTEGER NOT NULL REFERENCES clubs(id),
    role TEXT NOT NULL DEFAULT 'junior_lead' CHECK (role = 'junior_lead'),
    accepted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    revoked_by_admin_id INTEGER REFERENCES admin_users(id),
    CHECK (revoked_at IS NULL OR revoked_at >= accepted_at)
);
CREATE UNIQUE INDEX junior_one_active_lead_per_club ON junior_memberships(club_id) WHERE revoked_at IS NULL;
CREATE INDEX junior_memberships_account ON junior_memberships(account_id, club_id) WHERE revoked_at IS NULL;

CREATE TABLE junior_access_tokens (
    id BIGSERIAL PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    purpose TEXT NOT NULL CHECK (purpose IN ('invite', 'login')),
    account_id BIGINT NOT NULL REFERENCES junior_accounts(id),
    club_id INTEGER NOT NULL REFERENCES clubs(id),
    membership_id BIGINT REFERENCES junior_memberships(id),
    expected_membership_id BIGINT REFERENCES junior_memberships(id),
    created_by_admin_id INTEGER REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    delivery_state TEXT NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending','sent','failed')),
    CHECK (expires_at > created_at),
    CHECK ((purpose = 'invite' AND membership_id IS NULL AND created_by_admin_id IS NOT NULL)
        OR (purpose = 'login' AND membership_id IS NOT NULL AND expected_membership_id IS NULL))
);
CREATE INDEX junior_access_tokens_pending ON junior_access_tokens(club_id,purpose,created_at DESC) WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE TABLE junior_sessions (
    id BIGSERIAL PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    membership_id BIGINT NOT NULL REFERENCES junior_memberships(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    CHECK (expires_at > created_at)
);
CREATE INDEX junior_sessions_membership ON junior_sessions(membership_id) WHERE revoked_at IS NULL;
