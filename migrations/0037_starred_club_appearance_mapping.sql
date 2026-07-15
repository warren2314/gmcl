ALTER TABLE starred_club_division_overrides
    ADD COLUMN IF NOT EXISTS appearance_club_key TEXT,
    ADD COLUMN IF NOT EXISTS appearance_club_name TEXT;

CREATE INDEX IF NOT EXISTS idx_starred_club_division_overrides_appearance_club
    ON starred_club_division_overrides(season_year, appearance_club_key)
    WHERE appearance_club_key IS NOT NULL AND appearance_club_key <> '';
