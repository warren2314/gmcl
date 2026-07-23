ALTER TABLE starred_candidate_reviews
    ADD COLUMN IF NOT EXISTS email_recipient TEXT,
    ADD COLUMN IF NOT EXISTS email_sent_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS email_send_error TEXT;
