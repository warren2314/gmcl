-- Published starred-player lists, dated changes, scorecard appearances and
-- human-confirmed identity mappings used by the Rule 3.5 compliance review.
CREATE TABLE IF NOT EXISTS starred_import_runs (
    id              BIGSERIAL PRIMARY KEY,
    season_year     INTEGER NOT NULL,
    source_url      TEXT NOT NULL,
    source_sha256   TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'complete',
    entry_count     INTEGER NOT NULL DEFAULT 0,
    amendment_count INTEGER NOT NULL DEFAULT 0,
    issue_count     INTEGER NOT NULL DEFAULT 0,
    imported_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS starred_list_entries (
    id                BIGSERIAL PRIMARY KEY,
    import_run_id     BIGINT NOT NULL REFERENCES starred_import_runs(id) ON DELETE CASCADE,
    season_year       INTEGER NOT NULL,
    club_name         TEXT NOT NULL,
    club_key          TEXT NOT NULL,
    list_type         TEXT NOT NULL CHECK (list_type IN ('A', 'B')),
    slot_number       INTEGER,
    player_name       TEXT NOT NULL,
    player_key        TEXT NOT NULL,
    raw_value         TEXT NOT NULL,
    tags              TEXT[] NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_starred_entries_season_club
    ON starred_list_entries(season_year, club_key, list_type);

CREATE TABLE IF NOT EXISTS starred_club_status (
    id                BIGSERIAL PRIMARY KEY,
    import_run_id     BIGINT NOT NULL REFERENCES starred_import_runs(id) ON DELETE CASCADE,
    season_year       INTEGER NOT NULL,
    club_name         TEXT NOT NULL,
    club_key          TEXT NOT NULL,
    list_b_rule       TEXT,
    submitted_count   INTEGER NOT NULL DEFAULT 0,
    no_form_submitted BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE(import_run_id, club_key)
);

CREATE TABLE IF NOT EXISTS starred_list_amendments (
    id                BIGSERIAL PRIMARY KEY,
    import_run_id     BIGINT NOT NULL REFERENCES starred_import_runs(id) ON DELETE CASCADE,
    season_year       INTEGER NOT NULL,
    club_name         TEXT NOT NULL,
    club_key          TEXT NOT NULL,
    sequence_number   INTEGER NOT NULL,
    effective_date    DATE,
    outgoing_name     TEXT,
    outgoing_key      TEXT,
    incoming_name     TEXT,
    incoming_key      TEXT,
    raw_value         TEXT NOT NULL,
    parse_status      TEXT NOT NULL DEFAULT 'parsed',
    parse_issue       TEXT
);

CREATE INDEX IF NOT EXISTS idx_starred_amendments_season_club
    ON starred_list_amendments(season_year, club_key, effective_date, sequence_number);

-- Materialised historical membership periods. valid_to is exclusive.
CREATE TABLE IF NOT EXISTS starred_list_periods (
    id                BIGSERIAL PRIMARY KEY,
    import_run_id     BIGINT NOT NULL REFERENCES starred_import_runs(id) ON DELETE CASCADE,
    season_year       INTEGER NOT NULL,
    club_name         TEXT NOT NULL,
    club_key          TEXT NOT NULL,
    list_type         TEXT NOT NULL CHECK (list_type IN ('A', 'B')),
    player_name       TEXT NOT NULL,
    player_key        TEXT NOT NULL,
    valid_from        DATE NOT NULL,
    valid_to          DATE,
    tags              TEXT[] NOT NULL DEFAULT '{}',
    source_kind       TEXT NOT NULL CHECK (source_kind IN ('base', 'amendment')),
    source_sequence   INTEGER
);

CREATE INDEX IF NOT EXISTS idx_starred_periods_lookup
    ON starred_list_periods(season_year, club_key, player_key, valid_from, valid_to);

CREATE TABLE IF NOT EXISTS starred_identity_mappings (
    id                  BIGSERIAL PRIMARY KEY,
    season_year         INTEGER NOT NULL,
    club_key            TEXT NOT NULL,
    starred_player_key  TEXT NOT NULL,
    play_cricket_player_id BIGINT NOT NULL,
    play_cricket_name   TEXT,
    status              TEXT NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed', 'ignored')),
    confirmed_by        INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    confirmed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (season_year, club_key, starred_player_key)
);

CREATE TABLE IF NOT EXISTS starred_match_imports (
    play_cricket_match_id BIGINT PRIMARY KEY,
    season_year           INTEGER NOT NULL,
    match_date            DATE NOT NULL,
    competition_type      TEXT,
    competition_name      TEXT,
    last_updated          TEXT,
    payload               JSONB NOT NULL DEFAULT '{}',
    imported_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS starred_appearances (
    id                    BIGSERIAL PRIMARY KEY,
    play_cricket_match_id BIGINT NOT NULL REFERENCES starred_match_imports(play_cricket_match_id) ON DELETE CASCADE,
    season_year           INTEGER NOT NULL,
    match_date            DATE NOT NULL,
    competition_type      TEXT,
    competition_name      TEXT,
    club_name             TEXT NOT NULL,
    club_key              TEXT NOT NULL,
    team_name             TEXT NOT NULL,
    team_level            INTEGER,
    playing_day           TEXT,
    play_cricket_player_id BIGINT,
    player_name           TEXT NOT NULL,
    player_key            TEXT NOT NULL,
    captain               BOOLEAN NOT NULL DEFAULT FALSE,
    wicket_keeper         BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (play_cricket_match_id, club_key, team_name, play_cricket_player_id, player_key)
);

CREATE INDEX IF NOT EXISTS idx_starred_appearances_season_player
    ON starred_appearances(season_year, club_key, play_cricket_player_id, player_key);
CREATE INDEX IF NOT EXISTS idx_starred_appearances_match_date
    ON starred_appearances(match_date);

CREATE TABLE IF NOT EXISTS starred_exemptions (
    id                    BIGSERIAL PRIMARY KEY,
    season_year           INTEGER NOT NULL,
    club_key              TEXT NOT NULL,
    play_cricket_player_id BIGINT,
    player_key            TEXT,
    exemption_type        TEXT NOT NULL,
    valid_from            DATE NOT NULL,
    valid_to              DATE,
    notes                 TEXT,
    created_by            INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (play_cricket_player_id IS NOT NULL OR player_key IS NOT NULL)
);
