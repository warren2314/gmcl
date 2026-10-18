-- Junior operations share permanent clubs, not senior reporting activation,
-- captains, reporting weeks or the externally refreshed senior fixture cache.
CREATE TABLE junior_seasons (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE CHECK (BTRIM(name) <> ''),
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed')),
    linked_season_id INTEGER REFERENCES seasons(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (end_date >= start_date)
);

CREATE TABLE junior_club_seasons (
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    season_id BIGINT NOT NULL REFERENCES junior_seasons(id) ON DELETE RESTRICT,
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','withdrawn')),
    provenance JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (club_id,season_id)
);

CREATE TABLE junior_teams (
    id BIGSERIAL PRIMARY KEY,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (BTRIM(name) <> ''),
    archived BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id,club_id)
);
CREATE UNIQUE INDEX ux_junior_team_club_name ON junior_teams(club_id,LOWER(BTRIM(name)));

CREATE TABLE junior_contacts (
    id BIGSERIAL PRIMARY KEY,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    full_name TEXT NOT NULL CHECK (BTRIM(full_name) <> ''),
    email TEXT NOT NULL DEFAULT '',
    phone TEXT NOT NULL DEFAULT '',
    archived BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id,club_id),
    CHECK (BTRIM(email) <> '' OR BTRIM(phone) <> '')
);

CREATE TABLE junior_contact_roles (
    id BIGSERIAL PRIMARY KEY,
    contact_id BIGINT NOT NULL,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    season_id BIGINT NOT NULL REFERENCES junior_seasons(id) ON DELETE RESTRICT,
    role TEXT NOT NULL CHECK (role IN ('junior_lead','safeguarding_officer','coach')),
    age_group TEXT NOT NULL DEFAULT '',
    team_id BIGINT,
    starts_on DATE NOT NULL DEFAULT CURRENT_DATE,
    ends_on DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id,club_id),
    FOREIGN KEY (contact_id,club_id) REFERENCES junior_contacts(id,club_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id,club_id) REFERENCES junior_teams(id,club_id) ON DELETE RESTRICT,
    CHECK (ends_on IS NULL OR ends_on >= starts_on),
    CHECK ((role='coach' AND age_group IN ('U9','U11','U13','U15','U18'))
        OR (role IN ('junior_lead','safeguarding_officer') AND age_group='' AND team_id IS NULL))
);
CREATE UNIQUE INDEX ux_junior_contact_role_assignment
    ON junior_contact_roles(contact_id,season_id,role,age_group,COALESCE(team_id,0),starts_on);
CREATE INDEX ix_junior_contact_roles_directory
    ON junior_contact_roles(season_id,club_id,role,age_group,starts_on,ends_on);

CREATE TABLE junior_events (
    id BIGSERIAL PRIMARY KEY,
    club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    actor_admin_id INTEGER REFERENCES admin_users(id) ON DELETE RESTRICT,
    actor_account_id BIGINT,
    actor_session_id BIGINT,
    entity_type TEXT NOT NULL,
    entity_id BIGINT NOT NULL,
    action TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ix_junior_events_club_history ON junior_events(club_id,created_at DESC,id DESC);

CREATE FUNCTION junior_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'junior events are append-only';
END;
$$;
CREATE TRIGGER junior_events_no_update_delete BEFORE UPDATE OR DELETE ON junior_events
    FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();

CREATE TABLE junior_badge_revisions (
    id BIGSERIAL PRIMARY KEY,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    storage_key TEXT NOT NULL UNIQUE CHECK (storage_key ~ '^badges/[0-9]+/[a-f0-9]{64}\.(png|jpg)$'),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    media_type TEXT NOT NULL CHECK (media_type IN ('image/png','image/jpeg')),
    byte_size INTEGER NOT NULL CHECK (byte_size > 0 AND byte_size <= 8388608),
    width INTEGER NOT NULL CHECK (width BETWEEN 1 AND 2048),
    height INTEGER NOT NULL CHECK (height BETWEEN 1 AND 2048),
    original_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id,club_id)
);
CREATE TABLE junior_club_badges (
    club_id INTEGER PRIMARY KEY REFERENCES clubs(id) ON DELETE RESTRICT,
    revision_id BIGINT NOT NULL,
    FOREIGN KEY (revision_id,club_id) REFERENCES junior_badge_revisions(id,club_id) ON DELETE RESTRICT
);
CREATE TRIGGER junior_badges_no_update_delete BEFORE UPDATE OR DELETE ON junior_badge_revisions
    FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();

CREATE TABLE junior_roster_imports (
    id BIGSERIAL PRIMARY KEY,
    season_id BIGINT NOT NULL REFERENCES junior_seasons(id) ON DELETE RESTRICT,
    source_name TEXT NOT NULL,
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    source_rows JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'preview' CHECK (state IN ('preview','reviewed','applied')),
    review_decisions JSONB NOT NULL DEFAULT '[]',
    apply_result JSONB NOT NULL DEFAULT '{}',
    created_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_at TIMESTAMPTZ,
    UNIQUE (season_id,sha256)
);
CREATE TABLE junior_roster_applied_rows (
    import_id BIGINT NOT NULL REFERENCES junior_roster_imports(id) ON DELETE RESTRICT,
    row_number INTEGER NOT NULL CHECK (row_number > 0),
    club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    contact_id BIGINT,
    team_id BIGINT,
    role_id BIGINT,
    skipped BOOLEAN NOT NULL DEFAULT false,
    reason TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (contact_id,club_id) REFERENCES junior_contacts(id,club_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id,club_id) REFERENCES junior_teams(id,club_id) ON DELETE RESTRICT,
    FOREIGN KEY (role_id,club_id) REFERENCES junior_contact_roles(id,club_id) ON DELETE RESTRICT,
    PRIMARY KEY (import_id,row_number)
);
CREATE TRIGGER junior_roster_rows_no_update_delete BEFORE UPDATE OR DELETE ON junior_roster_applied_rows
    FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();

CREATE FUNCTION junior_roster_source_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'junior roster sources are retained';
    END IF;
    IF NEW.season_id IS DISTINCT FROM OLD.season_id
       OR NEW.source_name IS DISTINCT FROM OLD.source_name
       OR NEW.sha256 IS DISTINCT FROM OLD.sha256
       OR NEW.source_rows IS DISTINCT FROM OLD.source_rows
       OR NEW.created_by_admin_id IS DISTINCT FROM OLD.created_by_admin_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR (OLD.state='applied' AND NEW IS DISTINCT FROM OLD) THEN
        RAISE EXCEPTION 'junior roster sources and applied receipts are immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER junior_roster_source_no_change BEFORE UPDATE OR DELETE ON junior_roster_imports
    FOR EACH ROW EXECUTE FUNCTION junior_roster_source_immutable();
