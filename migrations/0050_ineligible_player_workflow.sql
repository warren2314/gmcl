-- Ineligible-player intake, provenance, correspondence, and multi-subject
-- case support. Raw external rows and correspondence revisions are append-only;
-- mutable tables are current-state projections backed by sanction case events.

ALTER TABLE sanction_cases
    ADD COLUMN IF NOT EXISTS reporting_club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS reporter_role TEXT,
    ADD COLUMN IF NOT EXISTS reporter_phone TEXT;

CREATE INDEX IF NOT EXISTS idx_sanction_cases_reporting_club
    ON sanction_cases(reporting_club_id, status, created_at DESC);

ALTER TABLE sanction_case_parties
    ADD COLUMN IF NOT EXISTS relationship TEXT CHECK (relationship IS NULL OR relationship IN
        ('reporting_club','offending_club','league','representative','witness'));

CREATE TABLE IF NOT EXISTS sanction_intake_sync_runs (
    id BIGSERIAL PRIMARY KEY,
    origin TEXT NOT NULL CHECK (origin IN ('google_form','native_form','starred_player','tracker_backfill')),
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed','partial')),
    source_reference TEXT,
    header_sha256 TEXT,
    rows_seen INTEGER NOT NULL DEFAULT 0 CHECK (rows_seen >= 0),
    rows_new INTEGER NOT NULL DEFAULT 0 CHECK (rows_new >= 0),
    rows_changed INTEGER NOT NULL DEFAULT 0 CHECK (rows_changed >= 0),
    rows_errored INTEGER NOT NULL DEFAULT 0 CHECK (rows_errored >= 0),
    error_message TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    triggered_by_type TEXT NOT NULL DEFAULT 'system',
    triggered_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_sanction_intake_sync_runs_recent
    ON sanction_intake_sync_runs(origin, started_at DESC);

-- A run is inserted once as running, finalized once, and then immutable. This
-- retains the operational running -> terminal lifecycle without allowing a
-- completed reconciliation record to be rewritten or deleted later.
CREATE OR REPLACE FUNCTION protect_sanction_intake_sync_run() RETURNS trigger AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'sanction intake sync runs cannot be deleted';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.status<>'running' OR NEW.completed_at IS NOT NULL THEN
            RAISE EXCEPTION 'sanction intake sync runs must start in running state';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status<>'running' OR OLD.completed_at IS NOT NULL OR
       NEW.status NOT IN ('succeeded','failed','partial') OR NEW.completed_at IS NULL THEN
        RAISE EXCEPTION 'sanction intake sync run is already final or has an invalid transition';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR
       NEW.origin IS DISTINCT FROM OLD.origin OR
       NEW.source_reference IS DISTINCT FROM OLD.source_reference OR
       NEW.started_at IS DISTINCT FROM OLD.started_at OR
       NEW.triggered_by_type IS DISTINCT FROM OLD.triggered_by_type OR
       NEW.triggered_by_admin_id IS DISTINCT FROM OLD.triggered_by_admin_id THEN
        RAISE EXCEPTION 'sanction intake sync run identity is immutable';
    END IF;
    IF NEW.completed_at < OLD.started_at THEN
        RAISE EXCEPTION 'sanction intake sync run cannot complete before it starts';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sanction_intake_sync_run_protect ON sanction_intake_sync_runs;
CREATE TRIGGER trg_sanction_intake_sync_run_protect
BEFORE INSERT OR UPDATE OR DELETE ON sanction_intake_sync_runs
FOR EACH ROW EXECUTE FUNCTION protect_sanction_intake_sync_run();

CREATE TABLE IF NOT EXISTS sanction_intakes (
    id BIGSERIAL PRIMARY KEY,
    origin TEXT NOT NULL CHECK (origin IN ('google_form','native_form','starred_player','tracker_backfill')),
    external_key TEXT NOT NULL,
    source_reference TEXT,
    external_created_at TIMESTAMPTZ,
    state TEXT NOT NULL DEFAULT 'new' CHECK (state IN
        ('new','reviewing','linked','duplicate','ignored','exception')),
    reporting_club_text TEXT,
    offending_club_text TEXT,
    team_text TEXT,
    player_text TEXT,
    fixture_date DATE,
    latest_revision INTEGER NOT NULL DEFAULT 0 CHECK (latest_revision >= 0),
    exception_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(origin, external_key)
);

CREATE INDEX IF NOT EXISTS idx_sanction_intakes_queue
    ON sanction_intakes(state, external_created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_sanction_intakes_clubs
    ON sanction_intakes(offending_club_text, reporting_club_text);

CREATE TABLE IF NOT EXISTS sanction_intake_revisions (
    id BIGSERIAL PRIMARY KEY,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    sync_run_id BIGINT REFERENCES sanction_intake_sync_runs(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    source_row_number INTEGER,
    raw_data JSONB NOT NULL,
    raw_sha256 TEXT NOT NULL,
    header_sha256 TEXT,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(intake_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_sanction_intake_revisions_hash
    ON sanction_intake_revisions(raw_sha256);

CREATE TABLE IF NOT EXISTS sanction_intake_case_links (
    id BIGSERIAL PRIMARY KEY,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    relationship TEXT NOT NULL DEFAULT 'primary' CHECK (relationship IN ('primary','duplicate','split','supporting')),
    reason TEXT,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(intake_id, case_id, relationship)
);

CREATE INDEX IF NOT EXISTS idx_sanction_intake_case_links_case
    ON sanction_intake_case_links(case_id, intake_id);

CREATE TABLE IF NOT EXISTS sanction_case_subjects (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('team','player','match')),
    team_id INTEGER REFERENCES teams(id) ON DELETE RESTRICT,
    player_name TEXT,
    play_cricket_player_id BIGINT,
    play_cricket_match_id BIGINT,
    is_primary BOOLEAN NOT NULL DEFAULT FALSE,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (subject_type='team' AND team_id IS NOT NULL) OR
        (subject_type='player' AND NULLIF(BTRIM(player_name),'') IS NOT NULL) OR
        (subject_type='match' AND play_cricket_match_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_subject_team
    ON sanction_case_subjects(case_id, team_id) WHERE subject_type='team';
CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_subject_pc_player
    ON sanction_case_subjects(case_id, play_cricket_player_id) WHERE subject_type='player' AND play_cricket_player_id IS NOT NULL;

ALTER TABLE sanction_effect_revisions
    ADD COLUMN IF NOT EXISTS case_subject_id BIGINT REFERENCES sanction_case_subjects(id) ON DELETE RESTRICT;

CREATE TABLE IF NOT EXISTS sanction_club_contacts (
    id BIGSERIAL PRIMARY KEY,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    contact_type TEXT NOT NULL DEFAULT 'official_mailbox' CHECK (contact_type IN ('official_mailbox')),
    name TEXT,
    email TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    verified_at TIMESTAMPTZ,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_club_contact_active
    ON sanction_club_contacts(club_id, contact_type) WHERE active;

CREATE TABLE IF NOT EXISTS sanction_correspondence_revisions (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    supersedes_id BIGINT REFERENCES sanction_correspondence_revisions(id) ON DELETE RESTRICT,
    message_kind TEXT NOT NULL CHECK (message_kind IN
        ('response_request','response_reminder','outcome_offending_club',
         'outcome_reporting_club','outcome_official','no_action_outcome')),
    audience TEXT NOT NULL CHECK (audience IN ('offending_club','reporting_club','official')),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    status TEXT NOT NULL CHECK (status IN ('draft','approved','queued','superseded')),
    recipients JSONB NOT NULL DEFAULT '[]'::jsonb,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    attachment_manifest JSONB NOT NULL DEFAULT '[]'::jsonb,
    pdf_storage_key TEXT,
    pdf_sha256 TEXT,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    approved_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(case_id, message_kind, audience, revision)
);

CREATE INDEX IF NOT EXISTS idx_sanction_correspondence_case
    ON sanction_correspondence_revisions(case_id, created_at DESC);

ALTER TABLE sanction_notification_outbox
    ADD COLUMN IF NOT EXISTS correspondence_revision_id BIGINT REFERENCES sanction_correspondence_revisions(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS message_kind TEXT NOT NULL DEFAULT 'decision_published';

CREATE TABLE IF NOT EXISTS sanction_response_requests (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    party_id BIGINT REFERENCES sanction_case_parties(id) ON DELETE RESTRICT,
    access_token_id BIGINT NOT NULL REFERENCES sanction_case_access_tokens(id) ON DELETE RESTRICT,
    correspondence_revision_id BIGINT REFERENCES sanction_correspondence_revisions(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','responded','expired','cancelled')),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reminder_due_at TIMESTAMPTZ NOT NULL,
    due_at TIMESTAMPTZ NOT NULL,
    reminder_queued_at TIMESTAMPTZ,
    responded_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    CHECK (reminder_due_at > requested_at AND due_at > reminder_due_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_response_pending
    ON sanction_response_requests(case_id) WHERE status='pending';
CREATE INDEX IF NOT EXISTS idx_sanction_response_due
    ON sanction_response_requests(status, reminder_due_at, due_at);

-- Link an outbox row to the exact correspondence snapshot it sends. Recreate
-- the protection function so the newly added columns remain immutable too.
CREATE OR REPLACE FUNCTION protect_sanction_outbox() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'sanction_notification_outbox is append-only'; END IF;
  IF NEW.case_id IS DISTINCT FROM OLD.case_id OR
     NEW.decision_revision_id IS DISTINCT FROM OLD.decision_revision_id OR
     NEW.policy_version_id IS DISTINCT FROM OLD.policy_version_id OR
     NEW.correspondence_revision_id IS DISTINCT FROM OLD.correspondence_revision_id OR
     NEW.message_kind IS DISTINCT FROM OLD.message_kind OR
     NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR
     NEW.recipient IS DISTINCT FROM OLD.recipient OR NEW.subject IS DISTINCT FROM OLD.subject OR
     NEW.body IS DISTINCT FROM OLD.body OR NEW.attachment_manifest IS DISTINCT FROM OLD.attachment_manifest OR
     NEW.available_at IS DISTINCT FROM OLD.available_at OR NEW.created_at IS DISTINCT FROM OLD.created_at OR
     OLD.processed_at IS NOT NULL OR NEW.processed_at IS NULL THEN
    RAISE EXCEPTION 'outbox message content is immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'sanction_intake_revisions','sanction_intake_case_links',
    'sanction_case_subjects','sanction_correspondence_revisions'
  ] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_immutable ON %I',t,t);
    EXECUTE format('CREATE TRIGGER trg_%I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change()',t,t);
  END LOOP;
END $$;

INSERT INTO admin_user_permissions(admin_user_id,permission)
SELECT a.id,p.permission
FROM admin_users a
CROSS JOIN (VALUES ('sanctions_triage'),('sanctions_investigate'),('sanctions_propose')) AS p(permission)
WHERE LOWER(a.username)='denverthornton'
ON CONFLICT DO NOTHING;
