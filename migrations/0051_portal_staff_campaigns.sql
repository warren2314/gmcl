-- League-staff assignments and staff-initiated multi-club communication.
--
-- A campaign creates one tenant-scoped case per club. Recipient addresses and
-- delivery outcomes remain system-only so one club can never infer another
-- campaign target or its contacts.

CREATE TABLE IF NOT EXISTS portal_staff_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    role_key TEXT NOT NULL CHECK (role_key IN (
        'club_liaison_officer',
        'junior_administrator'
    )),
    club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    competition_id UUID REFERENCES portal_competitions(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'suspended', 'expired', 'revoked')),
    starts_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at TIMESTAMPTZ,
    granted_by_admin_user_id INTEGER NOT NULL
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    grant_reason TEXT NOT NULL CHECK (length(btrim(grant_reason)) BETWEEN 1 AND 500),
    revoked_at TIMESTAMPTZ,
    revoked_by_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE RESTRICT,
    revocation_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (club_id IS NULL OR competition_id IS NULL),
    CHECK (ends_at IS NULL OR ends_at > starts_at),
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_portal_staff_assignments_admin
    ON portal_staff_assignments(admin_user_id, status, starts_at);
CREATE INDEX IF NOT EXISTS idx_portal_staff_assignments_scope
    ON portal_staff_assignments(role_key, club_id, competition_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS ux_portal_staff_assignments_active_scope
    ON portal_staff_assignments (
        admin_user_id,
        role_key,
        COALESCE(club_id, 0),
        COALESCE(
            competition_id,
            '00000000-0000-0000-0000-000000000000'::uuid
        )
    )
    WHERE status IN ('active', 'suspended');

CREATE TABLE IF NOT EXISTS portal_message_campaigns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sender_admin_user_id INTEGER NOT NULL
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    sender_role_key TEXT NOT NULL CHECK (sender_role_key IN (
        'super_administrator',
        'club_liaison_officer',
        'junior_administrator'
    )),
    competition_id UUID REFERENCES portal_competitions(id) ON DELETE RESTRICT,
    category TEXT NOT NULL CHECK (category IN (
        'general',
        'compliance_sanctions',
        'fixtures',
        'registration',
        'starred_players',
        'junior_administration',
        'contact_correction',
        'player_identity'
    )),
    recipient_role_key TEXT NOT NULL CHECK (recipient_role_key IN (
        'primary_contact',
        'secretary',
        'play_cricket_admin',
        'junior_contact',
        'fixtures_contact',
        'registration_contact'
    )),
    subject TEXT NOT NULL CHECK (length(btrim(subject)) BETWEEN 3 AND 200),
    body TEXT NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 10000),
    priority TEXT NOT NULL DEFAULT 'normal'
        CHECK (priority IN ('normal', 'urgent')),
    status TEXT NOT NULL DEFAULT 'sending'
        CHECK (status IN ('sending', 'sent', 'partially_failed', 'failed')),
    target_count INTEGER NOT NULL CHECK (target_count > 0),
    sent_target_count INTEGER NOT NULL DEFAULT 0 CHECK (sent_target_count >= 0),
    failed_target_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_target_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CHECK (sent_target_count + failed_target_count <= target_count)
);

CREATE INDEX IF NOT EXISTS idx_portal_message_campaigns_sender
    ON portal_message_campaigns(sender_admin_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS portal_message_campaign_targets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id UUID NOT NULL
        REFERENCES portal_message_campaigns(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    case_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sent', 'partially_failed', 'failed')),
    recipient_count INTEGER NOT NULL DEFAULT 0 CHECK (recipient_count >= 0),
    sent_count INTEGER NOT NULL DEFAULT 0 CHECK (sent_count >= 0),
    failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (campaign_id, club_id),
    UNIQUE (case_id),
    CHECK (sent_count + failed_count <= recipient_count)
);

CREATE INDEX IF NOT EXISTS idx_portal_campaign_targets_club
    ON portal_message_campaign_targets(club_id, created_at DESC);

ALTER TABLE portal_message_cases
    ALTER COLUMN created_by_user_id DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS created_by_admin_user_id INTEGER
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS campaign_id UUID
        REFERENCES portal_message_campaigns(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS sender_staff_role_key TEXT
        CHECK (sender_staff_role_key IN (
            'super_administrator',
            'club_liaison_officer',
            'junior_administrator'
        )),
    ADD COLUMN IF NOT EXISTS recipient_role_key TEXT
        CHECK (recipient_role_key IN (
            'primary_contact',
            'secretary',
            'play_cricket_admin',
            'junior_contact',
            'fixtures_contact',
            'registration_contact'
        ));

ALTER TABLE portal_message_cases
    DROP CONSTRAINT IF EXISTS portal_message_cases_creator_check;
ALTER TABLE portal_message_cases
    ADD CONSTRAINT portal_message_cases_creator_check CHECK (
        (
            created_by_user_id IS NOT NULL
            AND created_by_admin_user_id IS NULL
            AND campaign_id IS NULL
            AND sender_staff_role_key IS NULL
            AND recipient_role_key IS NULL
        )
        OR (
            created_by_user_id IS NULL
            AND created_by_admin_user_id IS NOT NULL
            AND campaign_id IS NOT NULL
            AND sender_staff_role_key IS NOT NULL
            AND recipient_role_key IS NOT NULL
        )
    );

ALTER TABLE portal_club_visible_messages
    ADD COLUMN IF NOT EXISTS author_staff_role_key TEXT
        CHECK (author_staff_role_key IN (
            'super_administrator',
            'club_liaison_officer',
            'junior_administrator'
        ));
ALTER TABLE portal_club_visible_messages
    DROP CONSTRAINT IF EXISTS portal_visible_message_staff_role_check;
ALTER TABLE portal_club_visible_messages
    ADD CONSTRAINT portal_visible_message_staff_role_check CHECK (
        author_kind = 'gmcl_admin' OR author_staff_role_key IS NULL
    );

ALTER TABLE portal_message_campaign_targets
    ADD CONSTRAINT portal_campaign_target_case_fk
    FOREIGN KEY (case_id, club_id)
    REFERENCES portal_message_cases(id, club_id)
    ON DELETE RESTRICT;

CREATE TABLE IF NOT EXISTS portal_message_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_target_id UUID NOT NULL
        REFERENCES portal_message_campaign_targets(id) ON DELETE RESTRICT,
    message_id UUID NOT NULL
        REFERENCES portal_club_visible_messages(id) ON DELETE RESTRICT,
    case_id UUID NOT NULL REFERENCES portal_message_cases(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    recipient_role_key TEXT NOT NULL,
    recipient_email TEXT NOT NULL CHECK (length(btrim(recipient_email)) BETWEEN 3 AND 320),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sent', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    sent_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (message_id, recipient_email),
    FOREIGN KEY (case_id, club_id)
        REFERENCES portal_message_cases(id, club_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_portal_message_deliveries_target
    ON portal_message_deliveries(campaign_target_id, status);
CREATE INDEX IF NOT EXISTS idx_portal_message_deliveries_retry
    ON portal_message_deliveries(status, updated_at)
    WHERE status IN ('pending', 'failed');

CREATE OR REPLACE FUNCTION protect_portal_visible_message()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'submitted portal correspondence is append-only';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.case_id IS DISTINCT FROM OLD.case_id
       OR NEW.club_id IS DISTINCT FROM OLD.club_id
       OR NEW.author_kind IS DISTINCT FROM OLD.author_kind
       OR NEW.author_user_id IS DISTINCT FROM OLD.author_user_id
       OR NEW.author_admin_user_id IS DISTINCT FROM OLD.author_admin_user_id
       OR NEW.author_staff_role_key IS DISTINCT FROM OLD.author_staff_role_key
       OR NEW.body IS DISTINCT FROM OLD.body
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'submitted portal correspondence is append-only';
    END IF;
    RETURN NEW;
END;
$$;

ALTER TABLE portal_staff_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_staff_assignments FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_message_campaigns ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_message_campaigns FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_message_campaign_targets ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_message_campaign_targets FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_message_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_message_deliveries FORCE ROW LEVEL SECURITY;

CREATE POLICY portal_staff_assignments_system_only ON portal_staff_assignments
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());
CREATE POLICY portal_message_campaigns_system_only ON portal_message_campaigns
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());
CREATE POLICY portal_campaign_targets_system_only ON portal_message_campaign_targets
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());
CREATE POLICY portal_message_deliveries_system_only ON portal_message_deliveries
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON
            portal_staff_assignments,
            portal_message_campaigns,
            portal_message_campaign_targets,
            portal_message_deliveries
        TO gmcl_portal_runtime;
    END IF;
END;
$$;
