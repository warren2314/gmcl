-- Sanctions (yellow/red cards for non-submission)
CREATE TYPE sanction_colour AS ENUM ('yellow', 'red');
CREATE TYPE sanction_status AS ENUM ('active', 'appealed', 'overturned', 'served');

CREATE TABLE IF NOT EXISTS sanctions (
    id                      BIGSERIAL PRIMARY KEY,
    season_id               INTEGER NOT NULL REFERENCES seasons(id)     ON DELETE RESTRICT,
    week_id                 INTEGER NOT NULL REFERENCES weeks(id)       ON DELETE RESTRICT,
    team_id                 INTEGER NOT NULL REFERENCES teams(id)       ON DELETE RESTRICT,
    club_id                 INTEGER NOT NULL REFERENCES clubs(id)       ON DELETE RESTRICT,
    colour                  sanction_colour  NOT NULL,
    reason                  TEXT             NOT NULL DEFAULT 'non_submission',
    notes                   TEXT,
    status                  sanction_status  NOT NULL DEFAULT 'active',
    issued_by_admin_id      INTEGER REFERENCES admin_users(id)         ON DELETE SET NULL,
    resolved_by_admin_id    INTEGER REFERENCES admin_users(id)         ON DELETE SET NULL,
    issued_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at             TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sanctions_season_week ON sanctions(season_id, week_id);
CREATE INDEX IF NOT EXISTS idx_sanctions_team        ON sanctions(team_id);
CREATE INDEX IF NOT EXISTS idx_sanctions_status      ON sanctions(status);

-- Umpire performance aggregates (write-through cache, recomputed per week close)
CREATE TABLE IF NOT EXISTS umpire_week_stats (
    id              BIGSERIAL PRIMARY KEY,
    season_id       INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    week_id         INTEGER NOT NULL REFERENCES weeks(id)   ON DELETE CASCADE,
    umpire_name     TEXT NOT NULL,
    umpire_display  TEXT NOT NULL,
    total_ratings   INTEGER NOT NULL DEFAULT 0,
    poor_count      INTEGER NOT NULL DEFAULT 0,
    average_count   INTEGER NOT NULL DEFAULT 0,
    good_count      INTEGER NOT NULL DEFAULT 0,
    score_value     NUMERIC(5,3),
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (season_id, week_id, umpire_name)
);

CREATE TABLE IF NOT EXISTS umpire_season_stats (
    id              BIGSERIAL PRIMARY KEY,
    season_id       INTEGER NOT NULL REFERENCES seasons(id) ON DELETE CASCADE,
    umpire_name     TEXT NOT NULL,
    umpire_display  TEXT NOT NULL,
    total_ratings   INTEGER NOT NULL DEFAULT 0,
    poor_count      INTEGER NOT NULL DEFAULT 0,
    average_count   INTEGER NOT NULL DEFAULT 0,
    good_count      INTEGER NOT NULL DEFAULT 0,
    score_value     NUMERIC(5,3),
    weeks_active    INTEGER NOT NULL DEFAULT 0,
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (season_id, umpire_name)
);

-- Generated reports (weekly / monthly / season-end)
CREATE TYPE report_type   AS ENUM ('weekly', 'monthly', 'season_end');
CREATE TYPE report_status AS ENUM ('generating', 'ready', 'error');

CREATE TABLE IF NOT EXISTS generated_reports (
    id                      BIGSERIAL PRIMARY KEY,
    season_id               INTEGER NOT NULL REFERENCES seasons(id) ON DELETE RESTRICT,
    week_id                 INTEGER          REFERENCES weeks(id)   ON DELETE SET NULL,
    report_type             report_type   NOT NULL,
    report_period           TEXT NOT NULL,
    status                  report_status NOT NULL DEFAULT 'generating',
    payload_json            JSONB,
    error_message           TEXT,
    generated_by_admin_id   INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    generated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at            TIMESTAMPTZ,
    UNIQUE (season_id, report_type, report_period)
);

CREATE INDEX IF NOT EXISTS idx_reports_season_type ON generated_reports(season_id, report_type);
