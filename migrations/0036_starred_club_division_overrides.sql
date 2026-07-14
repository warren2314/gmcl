CREATE TABLE IF NOT EXISTS starred_club_division_overrides (
    season_year  INTEGER NOT NULL,
    club_key     TEXT NOT NULL,
    club_name    TEXT NOT NULL,
    division_name TEXT NOT NULL,
    updated_by   INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (season_year, club_key)
);

CREATE INDEX IF NOT EXISTS idx_starred_club_division_overrides_division
    ON starred_club_division_overrides(season_year, division_name);
