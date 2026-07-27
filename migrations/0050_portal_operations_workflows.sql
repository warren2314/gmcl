-- Testable operational workflows for the Club Operations Portal.
--
-- The migration remains additive and keeps every club-private table behind
-- forced RLS. Club-visible correspondence and GMCL internal notes are
-- deliberately stored in different tables with different policies.

CREATE TABLE IF NOT EXISTS portal_message_cases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
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
    subject TEXT NOT NULL CHECK (length(btrim(subject)) BETWEEN 3 AND 200),
    status TEXT NOT NULL DEFAULT 'new' CHECK (status IN (
        'new',
        'awaiting_gmcl',
        'awaiting_club',
        'in_progress',
        'resolved',
        'closed',
        'reopened'
    )),
    priority TEXT NOT NULL DEFAULT 'normal'
        CHECK (priority IN ('normal', 'urgent')),
    deadline_at TIMESTAMPTZ,
    created_by_user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE RESTRICT,
    assigned_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at TIMESTAMPTZ,
    CHECK ((status = 'closed') = (closed_at IS NOT NULL)),
    UNIQUE (id, club_id)
);

CREATE INDEX IF NOT EXISTS idx_portal_message_cases_club_status
    ON portal_message_cases(club_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_portal_message_cases_admin_queue
    ON portal_message_cases(assigned_admin_user_id, status, priority, updated_at DESC);

CREATE TABLE IF NOT EXISTS portal_club_visible_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id UUID NOT NULL REFERENCES portal_message_cases(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    author_kind TEXT NOT NULL CHECK (author_kind IN (
        'club_user', 'gmcl_admin', 'system'
    )),
    author_user_id UUID REFERENCES portal_users(id) ON DELETE SET NULL,
    author_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    body TEXT NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 10000),
    email_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (email_status IN ('pending', 'sent', 'failed', 'not_required')),
    email_sent_at TIMESTAMPTZ,
    email_last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (case_id, club_id)
        REFERENCES portal_message_cases(id, club_id)
        ON DELETE RESTRICT,
    CHECK (
        (author_kind = 'club_user' AND author_user_id IS NOT NULL
            AND author_admin_user_id IS NULL)
        OR (author_kind = 'gmcl_admin' AND author_user_id IS NULL
            AND author_admin_user_id IS NOT NULL)
        OR (author_kind = 'system' AND author_user_id IS NULL
            AND author_admin_user_id IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_portal_visible_messages_case
    ON portal_club_visible_messages(case_id, created_at, id);

CREATE TABLE IF NOT EXISTS portal_internal_notes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id UUID NOT NULL REFERENCES portal_message_cases(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    author_admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    body TEXT NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 10000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (case_id, club_id)
        REFERENCES portal_message_cases(id, club_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_portal_internal_notes_case
    ON portal_internal_notes(case_id, created_at, id);

CREATE TABLE IF NOT EXISTS portal_case_watchers (
    case_id UUID NOT NULL REFERENCES portal_message_cases(id) ON DELETE CASCADE,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (case_id, user_id),
    FOREIGN KEY (case_id, club_id)
        REFERENCES portal_message_cases(id, club_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_portal_case_watchers_user
    ON portal_case_watchers(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS portal_club_contacts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    role_key TEXT NOT NULL CHECK (role_key IN (
        'primary_contact',
        'secretary',
        'play_cricket_admin',
        'junior_contact',
        'fixtures_contact',
        'registration_contact'
    )),
    display_name TEXT NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 200),
    email TEXT NOT NULL CHECK (length(btrim(email)) BETWEEN 3 AND 320),
    phone TEXT,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'verified', 'rejected', 'superseded')),
    evidence_reference TEXT NOT NULL
        CHECK (length(btrim(evidence_reference)) BETWEEN 1 AND 500),
    submitted_by_user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE RESTRICT,
    verified_by_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    verified_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    effective_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (effective_until IS NULL OR effective_until > effective_from),
    CHECK ((status = 'verified') = (verified_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_portal_club_contacts_club_role
    ON portal_club_contacts(club_id, role_key, status, effective_from DESC);

CREATE TABLE IF NOT EXISTS portal_module_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    case_id UUID REFERENCES portal_message_cases(id) ON DELETE SET NULL,
    request_type TEXT NOT NULL CHECK (request_type IN (
        'record_correction',
        'starred_player_review',
        'junior_administration',
        'player_identity_reconciliation',
        'registration_handoff'
    )),
    title TEXT NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 200),
    external_reference TEXT,
    payload JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'submitted' CHECK (status IN (
        'draft',
        'submitted',
        'under_review',
        'awaiting_club',
        'approved',
        'rejected',
        'withdrawn',
        'completed'
    )),
    submitted_by_user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE RESTRICT,
    reviewed_by_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    review_note TEXT,
    rule_release TEXT,
    human_review_required BOOLEAN NOT NULL DEFAULT TRUE,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at TIMESTAMPTZ,
    FOREIGN KEY (case_id, club_id)
        REFERENCES portal_message_cases(id, club_id)
        ON DELETE RESTRICT,
    CHECK (jsonb_typeof(payload) = 'object'),
    CHECK (
        request_type <> 'starred_player_review'
        OR human_review_required
    )
);

CREATE INDEX IF NOT EXISTS idx_portal_module_requests_club_type
    ON portal_module_requests(club_id, request_type, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS portal_fixture_constraints (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    team_id INTEGER REFERENCES teams(id) ON DELETE RESTRICT,
    season_id INTEGER REFERENCES seasons(id) ON DELETE RESTRICT,
    constraint_type TEXT NOT NULL CHECK (constraint_type IN (
        'venue_unavailable',
        'team_unavailable',
        'shared_ground',
        'travel_preference',
        'paired_team',
        'date_preference',
        'other'
    )),
    description TEXT NOT NULL CHECK (length(btrim(description)) BETWEEN 3 AND 1000),
    starts_on DATE,
    ends_on DATE,
    hard_constraint BOOLEAN NOT NULL DEFAULT TRUE,
    status TEXT NOT NULL DEFAULT 'submitted'
        CHECK (status IN ('draft', 'submitted', 'accepted', 'rejected', 'superseded')),
    submitted_by_user_id UUID NOT NULL REFERENCES portal_users(id) ON DELETE RESTRICT,
    reviewed_by_admin_user_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    review_note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (ends_on IS NULL OR starts_on IS NULL OR ends_on >= starts_on)
);

CREATE INDEX IF NOT EXISTS idx_portal_fixture_constraints_scope
    ON portal_fixture_constraints(club_id, season_id, team_id, status);

ALTER TABLE portal_message_cases ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_message_cases FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_club_visible_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_club_visible_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_internal_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_internal_notes FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_case_watchers ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_case_watchers FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_club_contacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_club_contacts FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_module_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_module_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_fixture_constraints ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_fixture_constraints FORCE ROW LEVEL SECURITY;

CREATE POLICY portal_message_cases_tenant_read ON portal_message_cases
    FOR SELECT
    USING (portal_is_system() OR club_id = portal_context_club_id());
CREATE POLICY portal_message_cases_system_write ON portal_message_cases
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_visible_messages_tenant_read ON portal_club_visible_messages
    FOR SELECT
    USING (portal_is_system() OR club_id = portal_context_club_id());
CREATE POLICY portal_visible_messages_system_write ON portal_club_visible_messages
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

-- Internal notes are never tenant-readable. The only policy is the explicit
-- system policy used by separately authenticated GMCL administration routes.
CREATE POLICY portal_internal_notes_system_only ON portal_internal_notes
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_case_watchers_tenant_read ON portal_case_watchers
    FOR SELECT
    USING (
        portal_is_system()
        OR (
            club_id = portal_context_club_id()
            AND user_id = portal_context_user_id()
        )
    );
CREATE POLICY portal_case_watchers_system_write ON portal_case_watchers
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_club_contacts_tenant_read ON portal_club_contacts
    FOR SELECT
    USING (portal_is_system() OR club_id = portal_context_club_id());
CREATE POLICY portal_club_contacts_system_write ON portal_club_contacts
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_module_requests_tenant_read ON portal_module_requests
    FOR SELECT
    USING (portal_is_system() OR club_id = portal_context_club_id());
CREATE POLICY portal_module_requests_system_write ON portal_module_requests
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

CREATE POLICY portal_fixture_constraints_tenant_read ON portal_fixture_constraints
    FOR SELECT
    USING (portal_is_system() OR club_id = portal_context_club_id());
CREATE POLICY portal_fixture_constraints_system_write ON portal_fixture_constraints
    FOR ALL
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON
            portal_message_cases,
            portal_club_visible_messages,
            portal_internal_notes,
            portal_case_watchers,
            portal_club_contacts,
            portal_module_requests,
            portal_fixture_constraints
        TO gmcl_portal_runtime;
    END IF;
END;
$$;

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
       OR NEW.body IS DISTINCT FROM OLD.body
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'submitted portal correspondence is append-only';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION reject_portal_correspondence_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'submitted portal correspondence is append-only';
END;
$$;

DROP TRIGGER IF EXISTS trg_portal_visible_message_append_only
    ON portal_club_visible_messages;
CREATE TRIGGER trg_portal_visible_message_append_only
BEFORE UPDATE OR DELETE ON portal_club_visible_messages
FOR EACH ROW EXECUTE FUNCTION protect_portal_visible_message();

DROP TRIGGER IF EXISTS trg_portal_internal_note_append_only
    ON portal_internal_notes;
CREATE TRIGGER trg_portal_internal_note_append_only
BEFORE UPDATE OR DELETE ON portal_internal_notes
FOR EACH ROW EXECUTE FUNCTION reject_portal_correspondence_mutation();
