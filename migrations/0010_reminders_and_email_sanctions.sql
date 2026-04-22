-- Dedup guard for captain reminder emails (one row per team+match_date+type)
CREATE TABLE IF NOT EXISTS captain_reminder_log (
    id              BIGSERIAL PRIMARY KEY,
    team_id         INTEGER NOT NULL REFERENCES teams(id)   ON DELETE CASCADE,
    week_id         INTEGER NOT NULL REFERENCES weeks(id)   ON DELETE CASCADE,
    match_date      DATE    NOT NULL,
    reminder_type   TEXT    NOT NULL,   -- 'game_day' | 'monday' | 'wednesday'
    captain_email   TEXT    NOT NULL,
    sent_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, match_date, reminder_type)
);

-- People who receive every disciplinary email (card letters)
CREATE TABLE IF NOT EXISTS disciplinary_recipients (
    id          SERIAL PRIMARY KEY,
    name        TEXT    NOT NULL,
    email       TEXT    NOT NULL UNIQUE,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Extend sanctions with email draft / approval columns
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS points_deduction  INTEGER;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS email_subject     TEXT;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS email_body        TEXT;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS email_status      TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS email_approved_by TEXT;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS email_approved_at TIMESTAMPTZ;
ALTER TABLE sanctions ADD COLUMN IF NOT EXISTS email_sent_at     TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_sanctions_email_status ON sanctions(email_status)
    WHERE email_status IN ('pending', 'approved');
