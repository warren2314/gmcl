-- Club Operations Portal foundation.
--
-- This migration is intentionally additive. Existing captain and administrator
-- authentication continues to use its current tables and routes during the
-- controlled portal rollout.

CREATE TABLE IF NOT EXISTS portal_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name TEXT NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 200),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('pending', 'active', 'suspended', 'disabled')),
    security_version BIGINT NOT NULL DEFAULT 1 CHECK (security_version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS portal_identities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE CASCADE,
    issuer TEXT NOT NULL CHECK (issuer = btrim(issuer) AND length(issuer) > 0),
    subject TEXT NOT NULL CHECK (subject = btrim(subject) AND length(subject) > 0),
    verified_email TEXT,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    last_authenticated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject)
);

CREATE INDEX IF NOT EXISTS idx_portal_identities_user
    ON portal_identities(user_id);

CREATE TABLE IF NOT EXISTS portal_competitions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    season_id INTEGER REFERENCES seasons(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    external_source TEXT,
    external_id TEXT,
    starts_at TIMESTAMPTZ,
    ends_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (season_id, external_source, external_id),
    CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at)
);

CREATE TABLE IF NOT EXISTS portal_club_memberships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'suspended', 'expired', 'revoked')),
    starts_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revocation_reason TEXT,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, club_id, user_id),
    CHECK (ends_at IS NULL OR ends_at > starts_at),
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_portal_memberships_user_status
    ON portal_club_memberships(user_id, status);
CREATE INDEX IF NOT EXISTS idx_portal_memberships_club_status
    ON portal_club_memberships(club_id, status);

CREATE UNIQUE INDEX IF NOT EXISTS ux_portal_memberships_active_user_club
    ON portal_club_memberships(user_id, club_id)
    WHERE status IN ('pending', 'active', 'suspended');

CREATE TABLE IF NOT EXISTS portal_role_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    membership_id UUID NOT NULL,
    user_id UUID NOT NULL,
    club_id INTEGER NOT NULL,
    role_key TEXT NOT NULL CHECK (role_key IN (
        'club_primary_admin',
        'club_admin',
        'club_secretary',
        'captain_manager',
        'read_only_club_user',
        'club_junior_officer',
        'club_safeguarding_officer'
    )),
    team_id INTEGER REFERENCES teams(id) ON DELETE RESTRICT,
    competition_id UUID REFERENCES portal_competitions(id) ON DELETE RESTRICT,
    season_id INTEGER REFERENCES seasons(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'suspended', 'expired', 'revoked')),
    starts_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at TIMESTAMPTZ,
    granted_by_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    approved_by_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    grant_reason TEXT NOT NULL CHECK (length(btrim(grant_reason)) > 0),
    revoked_at TIMESTAMPTZ,
    revocation_reason TEXT,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (membership_id, club_id, user_id)
        REFERENCES portal_club_memberships(id, club_id, user_id)
        ON DELETE RESTRICT,
    CHECK (ends_at IS NULL OR ends_at > starts_at),
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_portal_roles_user_status
    ON portal_role_assignments(user_id, status);
CREATE INDEX IF NOT EXISTS idx_portal_roles_club_status
    ON portal_role_assignments(club_id, status);
CREATE INDEX IF NOT EXISTS idx_portal_roles_scope
    ON portal_role_assignments(club_id, season_id, team_id, competition_id);

CREATE UNIQUE INDEX IF NOT EXISTS ux_portal_roles_active_scope
    ON portal_role_assignments (
        membership_id,
        role_key,
        COALESCE(team_id, 0),
        COALESCE(season_id, 0),
        COALESCE(competition_id, '00000000-0000-0000-0000-000000000000'::uuid)
    )
    WHERE status IN ('pending', 'active', 'suspended');

CREATE TABLE IF NOT EXISTS portal_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    selected_membership_id UUID REFERENCES portal_club_memberships(id) ON DELETE SET NULL,
    selected_role_assignment_id UUID REFERENCES portal_role_assignments(id) ON DELETE SET NULL,
    security_version BIGINT NOT NULL CHECK (security_version > 0),
    authenticated_at TIMESTAMPTZ NOT NULL,
    step_up_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    idle_expires_at TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoke_reason TEXT,
    ip_address INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (idle_expires_at <= absolute_expires_at),
    CHECK (absolute_expires_at > authenticated_at)
);

CREATE INDEX IF NOT EXISTS idx_portal_sessions_user_active
    ON portal_sessions(user_id, absolute_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS portal_oidc_login_states (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    state_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    nonce_hash BYTEA NOT NULL CHECK (octet_length(nonce_hash) = 32),
    pkce_verifier TEXT NOT NULL CHECK (length(pkce_verifier) BETWEEN 43 AND 128),
    return_to TEXT NOT NULL DEFAULT '/portal',
    invitation_token_hash BYTEA,
    step_up_requested BOOLEAN NOT NULL DEFAULT FALSE,
    expected_user_id UUID REFERENCES portal_users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_portal_oidc_states_expiry
    ON portal_oidc_login_states(expires_at);

CREATE TABLE IF NOT EXISTS portal_invitations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    email TEXT NOT NULL CHECK (length(btrim(email)) BETWEEN 3 AND 320),
    role_key TEXT NOT NULL CHECK (role_key IN (
        'club_primary_admin',
        'club_admin',
        'club_secretary',
        'captain_manager',
        'read_only_club_user',
        'club_junior_officer',
        'club_safeguarding_officer'
    )),
    team_id INTEGER REFERENCES teams(id) ON DELETE RESTRICT,
    competition_id UUID REFERENCES portal_competitions(id) ON DELETE RESTRICT,
    season_id INTEGER REFERENCES seasons(id) ON DELETE RESTRICT,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    status TEXT NOT NULL DEFAULT 'approved'
        CHECK (status IN ('draft', 'approved', 'redeemed', 'expired', 'revoked')),
    official_contact_evidence_reference TEXT NOT NULL
        CHECK (length(btrim(official_contact_evidence_reference)) > 0),
    approved_by_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    approved_by_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    approved_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    redeemed_at TIMESTAMPTZ,
    redeemed_by_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > approved_at),
    CHECK (
        (approved_by_admin_user_id IS NOT NULL AND approved_by_user_id IS NULL)
        OR (approved_by_admin_user_id IS NULL AND approved_by_user_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_portal_invitations_club_status
    ON portal_invitations(club_id, status);

CREATE TABLE IF NOT EXISTS portal_club_features (
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    feature_key TEXT NOT NULL CHECK (feature_key IN (
        'portal_access',
        'read_only_dashboard',
        'secure_messaging',
        'club_self_service',
        'junior_administration',
        'player_identity',
        'registration',
        'fixture_optimisation'
    )),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    enabled_at TIMESTAMPTZ,
    enabled_by_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    enabled_by_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    notes TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (club_id, feature_key),
    CHECK (
        NOT enabled
        OR enabled_at IS NOT NULL
    )
);

CREATE TABLE IF NOT EXISTS portal_attachments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    created_by_user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE RESTRICT,
    classification TEXT NOT NULL CHECK (classification IN (
        'club_private',
        'gmcl_restricted',
        'safeguarding_restricted'
    )),
    status TEXT NOT NULL DEFAULT 'quarantined' CHECK (status IN (
        'quarantined', 'scanning', 'available', 'rejected',
        'withdrawn', 'retention_hold', 'deleted'
    )),
    storage_key TEXT NOT NULL UNIQUE CHECK (length(btrim(storage_key)) > 0),
    original_filename TEXT NOT NULL CHECK (length(btrim(original_filename)) > 0),
    media_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 BYTEA NOT NULL CHECK (octet_length(sha256) = 32),
    scan_provider TEXT,
    scan_reference TEXT,
    scan_completed_at TIMESTAMPTZ,
    rejection_reason TEXT,
    retention_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_portal_attachments_club_status
    ON portal_attachments(club_id, status);

CREATE TABLE IF NOT EXISTS portal_notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    user_id UUID REFERENCES portal_users(id) ON DELETE RESTRICT,
    channel TEXT NOT NULL CHECK (channel IN ('email', 'portal')),
    template_key TEXT NOT NULL,
    recipient TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'cancelled')),
    idempotency_key TEXT NOT NULL UNIQUE,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_portal_notifications_dispatch
    ON portal_notifications(status, available_at)
    WHERE status IN ('pending', 'failed');

CREATE TABLE IF NOT EXISTS portal_outbox_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error TEXT
);

CREATE INDEX IF NOT EXISTS idx_portal_outbox_pending
    ON portal_outbox_events(occurred_at)
    WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS portal_audit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    actor_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    actor_kind TEXT NOT NULL CHECK (actor_kind IN ('portal_user', 'legacy_admin', 'system')),
    legacy_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    acting_role_key TEXT,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT,
    outcome TEXT NOT NULL DEFAULT 'success'
        CHECK (outcome IN ('success', 'denied', 'failure')),
    correlation_id TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    chain_position BIGINT,
    previous_hash BYTEA,
    event_hash BYTEA,
    ip_address INET,
    user_agent TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (chain_position IS NULL AND previous_hash IS NULL AND event_hash IS NULL)
        OR (chain_position IS NOT NULL AND event_hash IS NOT NULL)
    ),
    UNIQUE NULLS NOT DISTINCT (club_id, chain_position)
);

CREATE INDEX IF NOT EXISTS idx_portal_audit_club_occurred
    ON portal_audit_events(club_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_portal_audit_actor_occurred
    ON portal_audit_events(actor_user_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_portal_audit_correlation
    ON portal_audit_events(correlation_id);

CREATE OR REPLACE FUNCTION portal_context_user_id()
RETURNS UUID
LANGUAGE SQL
VOLATILE
AS $$
    SELECT NULLIF(current_setting('app.portal_user_id', true), '')::uuid
$$;

CREATE OR REPLACE FUNCTION portal_context_club_id()
RETURNS INTEGER
LANGUAGE SQL
VOLATILE
AS $$
    SELECT NULLIF(current_setting('app.portal_club_id', true), '')::integer
$$;

CREATE OR REPLACE FUNCTION portal_is_system()
RETURNS BOOLEAN
LANGUAGE SQL
VOLATILE
AS $$
    SELECT COALESCE(NULLIF(current_setting('app.portal_system', true), ''), 'false')::boolean
$$;

ALTER TABLE portal_users ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_users FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_identities FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_club_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_club_memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_role_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_role_assignments FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_oidc_login_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_oidc_login_states FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_invitations FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_club_features ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_club_features FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_attachments FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_notifications FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_outbox_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_outbox_events FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_audit_events FORCE ROW LEVEL SECURITY;

CREATE POLICY portal_users_self_read ON portal_users
    FOR SELECT
    USING (portal_is_system() OR id = portal_context_user_id());
CREATE POLICY portal_users_system_write ON portal_users
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_identities_self_read ON portal_identities
    FOR SELECT
    USING (portal_is_system() OR user_id = portal_context_user_id());
CREATE POLICY portal_identities_system_write ON portal_identities
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_memberships_tenant_read ON portal_club_memberships
    FOR SELECT
    USING (
        portal_is_system()
        OR (
            user_id = portal_context_user_id()
            AND club_id = portal_context_club_id()
        )
    );
CREATE POLICY portal_memberships_system_write ON portal_club_memberships
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_roles_tenant_read ON portal_role_assignments
    FOR SELECT
    USING (
        portal_is_system()
        OR (
            user_id = portal_context_user_id()
            AND club_id = portal_context_club_id()
        )
    );
CREATE POLICY portal_roles_system_write ON portal_role_assignments
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_sessions_self_read ON portal_sessions
    FOR SELECT
    USING (portal_is_system() OR user_id = portal_context_user_id());
CREATE POLICY portal_sessions_system_write ON portal_sessions
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_oidc_states_system_only ON portal_oidc_login_states
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_invitations_tenant_read ON portal_invitations
    FOR SELECT
    USING (
        portal_is_system()
        OR club_id = portal_context_club_id()
    );
CREATE POLICY portal_invitations_system_write ON portal_invitations
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_features_tenant_read ON portal_club_features
    FOR SELECT
    USING (
        portal_is_system()
        OR club_id = portal_context_club_id()
    );
CREATE POLICY portal_features_system_write ON portal_club_features
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_attachments_tenant_read ON portal_attachments
    FOR SELECT
    USING (
        portal_is_system()
        OR (
            club_id = portal_context_club_id()
            AND EXISTS (
                SELECT 1
                FROM portal_club_memberships m
                WHERE m.user_id = portal_context_user_id()
                  AND m.club_id = portal_context_club_id()
                  AND m.status = 'active'
                  AND m.starts_at <= now()
                  AND (m.ends_at IS NULL OR m.ends_at > now())
            )
        )
    );
CREATE POLICY portal_attachments_system_write ON portal_attachments
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_notifications_self_read ON portal_notifications
    FOR SELECT
    USING (
        portal_is_system()
        OR (
            user_id = portal_context_user_id()
            AND (club_id IS NULL OR club_id = portal_context_club_id())
        )
    );
CREATE POLICY portal_notifications_system_write ON portal_notifications
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_outbox_system_only ON portal_outbox_events
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_audit_tenant_read ON portal_audit_events
    FOR SELECT
    USING (
        portal_is_system()
        OR (
            actor_user_id = portal_context_user_id()
            AND (club_id IS NULL OR club_id = portal_context_club_id())
        )
    );
CREATE POLICY portal_audit_system_write ON portal_audit_events
    FOR INSERT
    WITH CHECK (portal_is_system());

-- The existing Docker deployment connects as POSTGRES_USER, which is a
-- PostgreSQL superuser and would otherwise bypass RLS even on FORCE tables.
-- Portal repositories SET LOCAL ROLE to this NOLOGIN/NOBYPASSRLS role for
-- every transaction. Environments already using a non-bypass application role
-- do not need the role switch, but receive the same object grants.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime') THEN
        BEGIN
            CREATE ROLE gmcl_portal_runtime
                NOLOGIN
                NOSUPERUSER
                NOCREATEDB
                NOCREATEROLE
                NOINHERIT
                NOREPLICATION
                NOBYPASSRLS;
        EXCEPTION
            WHEN insufficient_privilege THEN
                RAISE NOTICE 'gmcl_portal_runtime was not created; current runtime role must not bypass RLS';
        END;
    END IF;

    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime') THEN
        EXECUTE 'GRANT gmcl_portal_runtime TO ' || quote_ident(current_user);
        GRANT USAGE ON SCHEMA public TO gmcl_portal_runtime;

        GRANT SELECT ON
            clubs,
            teams,
            captains,
            seasons,
            weeks,
            league_fixtures,
            submissions,
            report_exemptions,
            sanctions,
            sanction_cases,
            sanction_card_ledger_entries,
            admin_users
        TO gmcl_portal_runtime;

        GRANT SELECT, INSERT, UPDATE, DELETE ON
            portal_users,
            portal_identities,
            portal_competitions,
            portal_club_memberships,
            portal_role_assignments,
            portal_sessions,
            portal_oidc_login_states,
            portal_invitations,
            portal_club_features,
            portal_attachments,
            portal_notifications,
            portal_outbox_events
        TO gmcl_portal_runtime;

        GRANT SELECT, INSERT ON portal_audit_events TO gmcl_portal_runtime;
        GRANT EXECUTE ON FUNCTION
            portal_context_user_id(),
            portal_context_club_id(),
            portal_is_system()
        TO gmcl_portal_runtime;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION reject_portal_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'portal audit events are append-only';
END;
$$;

DROP TRIGGER IF EXISTS trg_portal_audit_append_only ON portal_audit_events;
CREATE TRIGGER trg_portal_audit_append_only
BEFORE UPDATE OR DELETE ON portal_audit_events
FOR EACH ROW EXECUTE FUNCTION reject_portal_audit_mutation();
