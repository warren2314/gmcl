ALTER TABLE starred_player_replacement_requests
    ADD COLUMN IF NOT EXISTS correction_recipient TEXT,
    ADD COLUMN IF NOT EXISTS correction_status TEXT,
    ADD COLUMN IF NOT EXISTS correction_sent_by INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS correction_sent_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS correction_send_error TEXT;

ALTER TABLE starred_player_replacement_requests
    DROP CONSTRAINT IF EXISTS starred_replacement_correction_status_check;

ALTER TABLE starred_player_replacement_requests
    ADD CONSTRAINT starred_replacement_correction_status_check
    CHECK (correction_status IS NULL OR correction_status IN ('sending', 'send_failed', 'sent'));
