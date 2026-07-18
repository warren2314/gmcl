-- Sanctions and case management.  Immutable tables are append-only; mutable
-- projection tables may only be changed by application commands which also
-- append a corresponding event in the same transaction.

CREATE SEQUENCE IF NOT EXISTS sanction_case_reference_seq START 1000;

CREATE TABLE IF NOT EXISTS sanction_policy_versions (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    effective_from DATE NOT NULL,
    effective_to DATE,
    rules_release_id BIGINT REFERENCES rule_releases(id) ON DELETE SET NULL,
    yellow_threshold INTEGER NOT NULL DEFAULT 3 CHECK (yellow_threshold >= 2),
    carry_yellows BOOLEAN NOT NULL DEFAULT TRUE,
    max_reds_per_match INTEGER NOT NULL DEFAULT 1 CHECK (max_reds_per_match >= 1),
    club_board_red_threshold INTEGER NOT NULL DEFAULT 3 CHECK (club_board_red_threshold >= 1),
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (effective_to IS NULL OR effective_to >= effective_from)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_policy_effective_from
    ON sanction_policy_versions(effective_from);

INSERT INTO sanction_policy_versions(name,effective_from,config)
SELECT 'GMCL card system 2026', DATE '2026-01-01',
       '{"red_points":"ordinal","yellow_carry_over":true,"source":"published_rules"}'::jsonb
WHERE NOT EXISTS (SELECT 1 FROM sanction_policy_versions);

CREATE TABLE IF NOT EXISTS sanction_cases (
    id BIGSERIAL PRIMARY KEY,
    reference TEXT NOT NULL UNIQUE DEFAULT
        ('GMCL-' || to_char(CURRENT_DATE,'YYYY') || '-' || lpad(nextval('sanction_case_reference_seq')::text,6,'0')),
    source_type TEXT NOT NULL CHECK (source_type IN
        ('captain_report','discipline','ineligible_player','grounds_facilities',
         'forfeit','play_cricket','starred_player','manual','historical_import')),
    status TEXT NOT NULL DEFAULT 'submitted' CHECK (status IN
        ('submitted','triage','investigating','response_pending','decision_proposed',
         'approved','published','appealed','closed','rejected','withdrawn')),
    public_status TEXT NOT NULL DEFAULT 'unpublished' CHECK (public_status IN
        ('unpublished','active','suspended','served','overturned','expired')),
    season_id INTEGER REFERENCES seasons(id) ON DELETE RESTRICT,
    week_id INTEGER REFERENCES weeks(id) ON DELETE RESTRICT,
    club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    team_id INTEGER REFERENCES teams(id) ON DELETE RESTRICT,
    player_name TEXT,
    match_date DATE,
    play_cricket_match_id BIGINT,
    submission_id BIGINT REFERENCES submissions(id) ON DELETE RESTRICT,
    public_summary TEXT,
    private_summary TEXT,
    reporter_name TEXT,
    reporter_email TEXT,
    reporter_verified_at TIMESTAMPTZ,
    assigned_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    proposed_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    approved_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    emergency_override BOOLEAN NOT NULL DEFAULT FALSE,
    approved_at TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    current_revision INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (approved_by_admin_id IS NULL OR proposed_by_admin_id IS NULL OR approved_by_admin_id <> proposed_by_admin_id OR emergency_override)
);

CREATE INDEX IF NOT EXISTS idx_sanction_cases_queue ON sanction_cases(status,source_type,created_at);
CREATE INDEX IF NOT EXISTS idx_sanction_cases_team ON sanction_cases(team_id,season_id);
CREATE INDEX IF NOT EXISTS idx_sanction_cases_public ON sanction_cases(public_status,published_at);
CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_submission_source ON sanction_cases(submission_id,source_type) WHERE submission_id IS NOT NULL;

-- The legacy table remains a read-model during the transition. Rows created
-- from approved cases are linked so ledger queries never count both models.
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS case_id BIGINT REFERENCES sanction_cases(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX IF NOT EXISTS uq_sanctions_case_id ON sanctions(case_id) WHERE case_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS sanction_case_events (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    event_type TEXT NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('admin','captain','reporter','system','n8n','import')),
    actor_id BIGINT,
    actor_label TEXT,
    reason TEXT,
    before_data JSONB,
    after_data JSONB,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_id TEXT,
    emergency_override BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sanction_case_events_case ON sanction_case_events(case_id,id);

CREATE TABLE IF NOT EXISTS sanction_case_parties (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    party_type TEXT NOT NULL CHECK (party_type IN ('reporter','club','team','player','captain','witness','league')),
    name TEXT,
    email TEXT,
    team_id INTEGER REFERENCES teams(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sanction_case_access_tokens (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    party_id BIGINT REFERENCES sanction_case_parties(id) ON DELETE RESTRICT,
    token_hash BYTEA NOT NULL UNIQUE,
    purpose TEXT NOT NULL DEFAULT 'respond',
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sanction_case_evidence (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    event_id BIGINT REFERENCES sanction_case_events(id) ON DELETE RESTRICT,
    visibility TEXT NOT NULL CHECK (visibility IN ('private','party','public')),
    original_name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    byte_size BIGINT NOT NULL CHECK (byte_size >= 0),
    storage_key TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    uploaded_by_type TEXT NOT NULL,
    uploaded_by_id BIGINT,
    redacted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sanction_evidence_tombstones (
    id BIGSERIAL PRIMARY KEY,
    evidence_id BIGINT NOT NULL REFERENCES sanction_case_evidence(id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action IN ('redacted','expired','legal_hold_released')),
    reason TEXT NOT NULL,
    authorised_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    original_sha256 TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sanction_decision_revisions (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL,
    supersedes_id BIGINT REFERENCES sanction_decision_revisions(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('proposed','approved','rejected','corrected','overturned')),
    public_reason TEXT NOT NULL,
    private_reason TEXT,
    rule_release_id BIGINT REFERENCES rule_releases(id) ON DELETE SET NULL,
    rule_reference TEXT,
    policy_version_id BIGINT REFERENCES sanction_policy_versions(id) ON DELETE RESTRICT,
    proposed_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    approved_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    emergency_override BOOLEAN NOT NULL DEFAULT FALSE,
    correction_reason TEXT,
    effective_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    appeal_due_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(case_id,revision),
    CHECK (approved_by_admin_id IS NULL OR proposed_by_admin_id IS NULL OR approved_by_admin_id <> proposed_by_admin_id OR emergency_override)
);

CREATE TABLE IF NOT EXISTS sanction_effect_revisions (
    id BIGSERIAL PRIMARY KEY,
    decision_revision_id BIGINT NOT NULL REFERENCES sanction_decision_revisions(id) ON DELETE RESTRICT,
    effect_key UUID NOT NULL DEFAULT gen_random_uuid(),
    supersedes_id BIGINT REFERENCES sanction_effect_revisions(id) ON DELETE RESTRICT,
    effect_type TEXT NOT NULL CHECK (effect_type IN
        ('yellow_card','red_card','suspended_red','player_ban','team_ban','fine',
         'card_points','points_adjustment','warning','no_action')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN
        ('pending','active','suspended','served','overturned','expired','cancelled')),
    subject_type TEXT NOT NULL CHECK (subject_type IN ('club','team','player','case')),
    subject_id BIGINT,
    player_name TEXT,
    amount_pence BIGINT,
    points INTEGER,
    starts_at TIMESTAMPTZ,
    ends_at TIMESTAMPTZ,
    trigger_condition TEXT,
    public_details JSONB NOT NULL DEFAULT '{}'::jsonb,
    private_details JSONB NOT NULL DEFAULT '{}'::jsonb,
    counts_for_totting BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sanction_effects_decision ON sanction_effect_revisions(decision_revision_id);
CREATE INDEX IF NOT EXISTS idx_sanction_effects_key ON sanction_effect_revisions(effect_key,id DESC);

CREATE TABLE IF NOT EXISTS sanction_card_ledger_entries (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    decision_revision_id BIGINT NOT NULL REFERENCES sanction_decision_revisions(id) ON DELETE RESTRICT,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE RESTRICT,
    match_date DATE,
    yellow_delta INTEGER NOT NULL DEFAULT 0,
    red_delta INTEGER NOT NULL DEFAULT 0,
    points_deduction INTEGER NOT NULL DEFAULT 0,
    entry_type TEXT NOT NULL CHECK (entry_type IN ('issue','conversion','activation','reversal','correction','import')),
    explanation TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (yellow_delta <> 0 OR red_delta <> 0 OR points_deduction <> 0)
);

CREATE INDEX IF NOT EXISTS idx_sanction_card_ledger_team ON sanction_card_ledger_entries(team_id,season_id,id);
CREATE INDEX IF NOT EXISTS idx_sanction_card_ledger_club ON sanction_card_ledger_entries(club_id,season_id,id);

CREATE TABLE IF NOT EXISTS sanction_follow_up_tasks (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    task_type TEXT NOT NULL CHECK (task_type IN
        ('play_cricket_points','fine_recovery','board_intervention','suspended_review',
         'appeal_deadline','ban_expiry','notice_failure','migration_exception')),
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','complete','cancelled')),
    assigned_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    due_at TIMESTAMPTZ,
    current_note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sanction_notification_policy_versions (
    id BIGSERIAL PRIMARY KEY,
    source_type TEXT NOT NULL,
    event_type TEXT NOT NULL,
    version INTEGER NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    recipient_rules JSONB NOT NULL DEFAULT '[]'::jsonb,
    subject_template TEXT NOT NULL,
    body_template TEXT NOT NULL,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(source_type,event_type,version)
);

CREATE TABLE IF NOT EXISTS sanction_recipient_directory (
    id BIGSERIAL PRIMARY KEY,
    recipient_role TEXT NOT NULL CHECK (recipient_role IN ('executive','finance','play_cricket','discipline','other')),
    name TEXT NOT NULL,
    email TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(recipient_role,email)
);

INSERT INTO sanction_recipient_directory(recipient_role,name,email)
SELECT 'executive',name,email FROM disciplinary_recipients WHERE active
ON CONFLICT DO NOTHING;

INSERT INTO sanction_notification_policy_versions(source_type,event_type,version,recipient_rules,subject_template,body_template)
SELECT '*','decision_published',1,
       '[{"role":"captain"},{"role":"executive"},{"role":"finance","when_effect":"fine"},{"role":"play_cricket","when_points":true}]'::jsonb,
       'GMCL sanction decision {{reference}}',
       '{{public_summary}}\n\nCase reference: {{reference}}'
WHERE NOT EXISTS (SELECT 1 FROM sanction_notification_policy_versions WHERE source_type='*' AND event_type='decision_published');

CREATE TABLE IF NOT EXISTS sanction_notification_outbox (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    decision_revision_id BIGINT REFERENCES sanction_decision_revisions(id) ON DELETE RESTRICT,
    policy_version_id BIGINT REFERENCES sanction_notification_policy_versions(id) ON DELETE RESTRICT,
    idempotency_key TEXT NOT NULL UNIQUE,
    recipient TEXT NOT NULL,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    attachment_manifest JSONB NOT NULL DEFAULT '[]'::jsonb,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sanction_notification_attempts (
    id BIGSERIAL PRIMARY KEY,
    outbox_id BIGINT NOT NULL REFERENCES sanction_notification_outbox(id) ON DELETE RESTRICT,
    attempt_number INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('sending','sent','failed','bounced','complained')),
    provider_message_id TEXT,
    error_message TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(outbox_id,attempt_number)
);

CREATE TABLE IF NOT EXISTS sanction_automation_settings (
    source_type TEXT PRIMARY KEY,
    mode TEXT NOT NULL DEFAULT 'shadow' CHECK (mode IN ('shadow','manual','automatic')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    clean_cycles INTEGER NOT NULL DEFAULT 0,
    last_reconciled_at TIMESTAMPTZ,
    activated_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    activated_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (mode <> 'automatic' OR clean_cycles >= 3)
);

INSERT INTO sanction_automation_settings(source_type,mode)
VALUES ('_global','manual'),('captain_report','manual'),('play_cricket','shadow')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS sanction_configuration_events (
    id BIGSERIAL PRIMARY KEY,
    configuration_type TEXT NOT NULL,
    configuration_key TEXT NOT NULL,
    actor_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    reason TEXT NOT NULL,
    before_data JSONB,
    after_data JSONB NOT NULL,
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sanction_import_batches (
    id BIGSERIAL PRIMARY KEY,
    source_name TEXT NOT NULL,
    source_url TEXT,
    original_filename TEXT NOT NULL,
    storage_key TEXT NOT NULL,
    byte_size BIGINT NOT NULL CHECK (byte_size >= 0),
    sha256 TEXT NOT NULL,
    extracted_at TIMESTAMPTZ,
    imported_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'staged' CHECK (status IN ('staged','reconciling','approved','applied','rejected')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(source_name,sha256)
);

CREATE TABLE IF NOT EXISTS sanction_import_rows (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES sanction_import_batches(id) ON DELETE RESTRICT,
    row_number INTEGER NOT NULL,
    raw_data JSONB NOT NULL,
    raw_sha256 TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(batch_id,row_number)
);

CREATE TABLE IF NOT EXISTS sanction_import_mappings (
    import_row_id BIGINT PRIMARY KEY REFERENCES sanction_import_rows(id) ON DELETE RESTRICT,
    case_id BIGINT REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    mapping JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','mapped','duplicate','exception','applied')),
    reviewed_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    reviewed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS sanction_import_exceptions (
    id BIGSERIAL PRIMARY KEY,
    import_row_id BIGINT NOT NULL REFERENCES sanction_import_rows(id) ON DELETE RESTRICT,
    exception_type TEXT NOT NULL,
    details TEXT NOT NULL,
    resolved_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    resolution TEXT,
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Immutable records cannot be rewritten or deleted.  Corrections are new rows.
CREATE OR REPLACE FUNCTION reject_immutable_sanction_change() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only; record a new revision/event', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'sanction_policy_versions','sanction_case_events','sanction_case_evidence',
    'sanction_evidence_tombstones',
    'sanction_decision_revisions','sanction_effect_revisions',
    'sanction_card_ledger_entries',
    'sanction_notification_policy_versions','sanction_notification_attempts',
    'sanction_import_rows','sanction_configuration_events'
  ] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_immutable ON %I',t,t);
    EXECUTE format('CREATE TRIGGER trg_%I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change()',t,t);
  END LOOP;
END $$;

-- Outbox content is immutable; only processed_at may advance once.
CREATE OR REPLACE FUNCTION protect_sanction_outbox() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'sanction_notification_outbox is append-only'; END IF;
  IF NEW.case_id IS DISTINCT FROM OLD.case_id OR
     NEW.decision_revision_id IS DISTINCT FROM OLD.decision_revision_id OR
     NEW.policy_version_id IS DISTINCT FROM OLD.policy_version_id OR
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

DROP TRIGGER IF EXISTS trg_sanction_outbox_protect ON sanction_notification_outbox;
CREATE TRIGGER trg_sanction_outbox_protect BEFORE UPDATE OR DELETE ON sanction_notification_outbox
FOR EACH ROW EXECUTE FUNCTION protect_sanction_outbox();

-- Granular permissions are opt-in for ordinary admins; super-admins retain all.
CREATE TABLE IF NOT EXISTS sanction_permission_catalog (
    permission TEXT PRIMARY KEY,
    description TEXT NOT NULL
);

INSERT INTO sanction_permission_catalog(permission,description) VALUES
('sanctions_triage','Triage submitted cases'),
('sanctions_investigate','Investigate and request responses'),
('sanctions_propose','Propose decisions'),
('sanctions_approve','Approve another investigator''s decision'),
('sanctions_finance','Manage fine recovery tasks'),
('sanctions_publish','Publish approved decisions'),
('sanctions_appeals','Manage appeals'),
('sanctions_audit','View private immutable history'),
('sanctions_automation','Configure deterministic automation'),
('sanctions_import','Stage and reconcile historical imports')
ON CONFLICT DO NOTHING;
