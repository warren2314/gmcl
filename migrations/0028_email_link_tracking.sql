ALTER TABLE magic_link_send_log
    ADD COLUMN IF NOT EXISTS token_id BIGINT REFERENCES magic_link_tokens(id) ON DELETE SET NULL;

ALTER TABLE captain_reminder_log
    ADD COLUMN IF NOT EXISTS token_id BIGINT REFERENCES magic_link_tokens(id) ON DELETE SET NULL;

ALTER TABLE email_events
    ADD COLUMN IF NOT EXISTS magic_link_token_id BIGINT REFERENCES magic_link_tokens(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS captain_id INTEGER REFERENCES captains(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS team_id INTEGER REFERENCES teams(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS season_id INTEGER REFERENCES seasons(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS week_id INTEGER REFERENCES weeks(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS match_date DATE,
    ADD COLUMN IF NOT EXISTS link_url TEXT,
    ADD COLUMN IF NOT EXISTS click_ip INET,
    ADD COLUMN IF NOT EXISTS click_user_agent TEXT;

CREATE INDEX IF NOT EXISTS idx_magic_send_token
    ON magic_link_send_log(token_id);

CREATE INDEX IF NOT EXISTS idx_captain_reminder_token
    ON captain_reminder_log(token_id);

CREATE INDEX IF NOT EXISTS idx_email_events_team_created
    ON email_events(team_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_email_events_magic_token
    ON email_events(magic_link_token_id);

CREATE INDEX IF NOT EXISTS idx_email_events_captain_created
    ON email_events(captain_id, created_at DESC);
