CREATE TABLE IF NOT EXISTS captain_reminder_failures (
    id             BIGSERIAL PRIMARY KEY,
    team_id        INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    week_id        INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    match_date     DATE NOT NULL,
    reminder_type  TEXT NOT NULL,
    captain_email  TEXT NOT NULL,
    stage          TEXT NOT NULL,
    error_message  TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_captain_reminder_failures_created
    ON captain_reminder_failures(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_captain_reminder_failures_team_date
    ON captain_reminder_failures(team_id, match_date, reminder_type);
