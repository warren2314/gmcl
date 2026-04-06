-- seasons and weeks
CREATE TABLE seasons (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    is_archived BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ
);

CREATE TABLE weeks (
    id SERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_number INTEGER NOT NULL,
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    UNIQUE (season_id, week_number)
);

-- clubs, teams, umpires, captains
CREATE TABLE clubs (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    short_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE teams (
    id SERIAL PRIMARY KEY,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    level INTEGER,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (club_id, name)
);

CREATE TABLE umpires (
    id SERIAL PRIMARY KEY,
    full_name TEXT NOT NULL,
    email TEXT,
    active BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE TABLE captains (
    id SERIAL PRIMARY KEY,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    full_name TEXT NOT NULL,
    email TEXT NOT NULL,
    active_from DATE NOT NULL DEFAULT CURRENT_DATE,
    active_to DATE,
    UNIQUE (team_id, email, active_from)
);

-- submissions and drafts
CREATE TYPE submission_status AS ENUM ('submitted', 'amended');

CREATE TABLE submissions (
    id BIGSERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE RESTRICT,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE RESTRICT,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    captain_id INTEGER NOT NULL REFERENCES captains(id) ON DELETE RESTRICT,
    umpire_id INTEGER REFERENCES umpires(id) ON DELETE SET NULL,
    match_date DATE NOT NULL,
    opposition TEXT,
    venue TEXT,
    pitch_rating INTEGER NOT NULL CHECK (pitch_rating BETWEEN 1 AND 5),
    outfield_rating INTEGER NOT NULL CHECK (outfield_rating BETWEEN 1 AND 5),
    facilities_rating INTEGER NOT NULL CHECK (facilities_rating BETWEEN 1 AND 5),
    comments TEXT,
    flags JSONB,
    status submission_status NOT NULL DEFAULT 'submitted',
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_submissions_season_week ON submissions(season_id, week_id);
CREATE INDEX idx_submissions_team ON submissions(team_id);
CREATE INDEX idx_submissions_umpire ON submissions(umpire_id);

CREATE TABLE drafts (
    id BIGSERIAL PRIMARY KEY,
    season_id INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    captain_id INTEGER NOT NULL REFERENCES captains(id) ON DELETE CASCADE,
    draft_data JSONB NOT NULL,
    last_autosaved_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ux_drafts_season_week_team ON drafts(season_id, week_id, team_id);

