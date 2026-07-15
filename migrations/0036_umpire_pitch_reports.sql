CREATE TABLE IF NOT EXISTS umpire_pitch_imports (
    id BIGSERIAL PRIMARY KEY,
    source_filename TEXT NOT NULL,
    source_checksum TEXT NOT NULL,
    imported_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    row_count INTEGER NOT NULL DEFAULT 0,
    imported_count INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS umpire_pitch_reports (
    id BIGSERIAL PRIMARY KEY,
    import_id BIGINT NOT NULL REFERENCES umpire_pitch_imports(id) ON DELETE CASCADE,
    play_cricket_match_id BIGINT NOT NULL REFERENCES league_fixtures(play_cricket_match_id) ON DELETE RESTRICT,
    source_timestamp TIMESTAMPTZ,
    match_date DATE NOT NULL,
    division_label TEXT NOT NULL,
    home_club_label TEXT NOT NULL,
    away_club_label TEXT NOT NULL,
    unevenness_mark SMALLINT NOT NULL CHECK (unevenness_mark BETWEEN 1 AND 5),
    seam_mark SMALLINT NOT NULL CHECK (seam_mark BETWEEN 1 AND 5),
    carry_mark SMALLINT NOT NULL CHECK (carry_mark BETWEEN 1 AND 5),
    turn_mark SMALLINT NOT NULL CHECK (turn_mark BETWEEN 1 AND 5),
    source_row_hash TEXT NOT NULL UNIQUE,
    source_row JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_umpire_pitch_reports_match
    ON umpire_pitch_reports(play_cricket_match_id);

CREATE INDEX IF NOT EXISTS idx_umpire_pitch_reports_date
    ON umpire_pitch_reports(match_date);
