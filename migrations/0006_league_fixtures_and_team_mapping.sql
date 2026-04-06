-- Map local teams to Play-Cricket team IDs for fixture matching.
ALTER TABLE teams
    ADD COLUMN IF NOT EXISTS play_cricket_team_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS ux_teams_play_cricket_team_id
    ON teams (play_cricket_team_id)
    WHERE play_cricket_team_id IS NOT NULL AND play_cricket_team_id <> '';

-- Cached league fixtures from Play-Cricket / league information API (umpires, ground, etc.).
CREATE TABLE IF NOT EXISTS league_fixtures (
    id BIGSERIAL PRIMARY KEY,
    play_cricket_match_id BIGINT NOT NULL UNIQUE,
    season_id INTEGER REFERENCES seasons(id) ON DELETE SET NULL,
    match_date DATE NOT NULL,
    league_id TEXT,
    competition_id TEXT,
    home_team_pc_id TEXT,
    away_team_pc_id TEXT,
    home_club_name TEXT,
    away_club_name TEXT,
    home_team_name TEXT,
    away_team_name TEXT,
    ground_name TEXT,
    umpire_1_name TEXT,
    umpire_2_name TEXT,
    payload JSONB NOT NULL DEFAULT '{}',
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_league_fixtures_match_date ON league_fixtures (match_date);
CREATE INDEX IF NOT EXISTS idx_league_fixtures_home_team ON league_fixtures (home_team_pc_id);
CREATE INDEX IF NOT EXISTS idx_league_fixtures_away_team ON league_fixtures (away_team_pc_id);
