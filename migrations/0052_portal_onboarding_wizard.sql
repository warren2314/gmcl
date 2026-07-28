-- Recoverable club onboarding wizard.
--
-- Each run records the approved named official, requested pilot features,
-- Cognito provisioning checkpoint and the current single-use portal
-- invitation. The table is system-only: clubs cannot inspect other onboarding
-- records or Cognito administration metadata.

CREATE TABLE IF NOT EXISTS portal_onboarding_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    official_name TEXT NOT NULL
        CHECK (length(btrim(official_name)) BETWEEN 1 AND 200),
    email TEXT NOT NULL
        CHECK (length(btrim(email)) BETWEEN 3 AND 320),
    role_key TEXT NOT NULL CHECK (role_key IN (
        'club_primary_admin',
        'club_admin',
        'club_secretary',
        'read_only_club_user'
    )),
    official_contact_evidence_reference TEXT NOT NULL
        CHECK (length(btrim(official_contact_evidence_reference)) BETWEEN 1 AND 500),
    feature_keys TEXT[] NOT NULL DEFAULT ARRAY[
        'portal_access',
        'read_only_dashboard',
        'secure_messaging'
    ]::TEXT[],
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN (
        'draft',
        'identity_pending',
        'identity_ready',
        'invitation_sent',
        'activated',
        'failed',
        'cancelled'
    )),
    identity_status TEXT NOT NULL DEFAULT 'pending' CHECK (identity_status IN (
        'pending',
        'created',
        'existing',
        'confirmed',
        'invitation_resent',
        'manual_required',
        'manual_confirmed',
        'failed'
    )),
    cognito_username TEXT,
    cognito_user_status TEXT,
    cognito_email_verified BOOLEAN,
    cognito_last_checked_at TIMESTAMPTZ,
    current_invitation_id UUID REFERENCES portal_invitations(id) ON DELETE RESTRICT,
    last_error TEXT,
    created_by_admin_user_id INTEGER NOT NULL
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    activated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        feature_keys <@ ARRAY[
            'portal_access',
            'read_only_dashboard',
            'secure_messaging',
            'club_self_service',
            'junior_administration',
            'player_identity',
            'registration',
            'fixture_optimisation'
        ]::TEXT[]
    ),
    CHECK ('portal_access' = ANY(feature_keys)),
    CHECK ((status = 'activated') = (activated_at IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_portal_onboarding_active_official
    ON portal_onboarding_runs (club_id, lower(email))
    WHERE status IN (
        'draft',
        'identity_pending',
        'identity_ready',
        'invitation_sent'
    );

CREATE INDEX IF NOT EXISTS idx_portal_onboarding_recent
    ON portal_onboarding_runs (created_at DESC);

ALTER TABLE portal_invitations
    ADD COLUMN IF NOT EXISTS onboarding_run_id UUID
        REFERENCES portal_onboarding_runs(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX IF NOT EXISTS ux_portal_invitation_onboarding_active
    ON portal_invitations (onboarding_run_id)
    WHERE onboarding_run_id IS NOT NULL
      AND status = 'approved'
      AND redeemed_at IS NULL;

ALTER TABLE portal_onboarding_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_onboarding_runs FORCE ROW LEVEL SECURITY;

CREATE POLICY portal_onboarding_runs_system_only ON portal_onboarding_runs
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON portal_onboarding_runs
        TO gmcl_portal_runtime;
    END IF;
END;
$$;
