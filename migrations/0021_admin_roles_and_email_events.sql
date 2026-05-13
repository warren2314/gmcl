ALTER TABLE admin_users
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'admin'
        CHECK (role IN ('admin', 'super_admin'));

UPDATE admin_users
SET role = 'super_admin'
WHERE username = 'admin'
   OR LOWER(email) = 'webmaster@gmcl.co.uk';

CREATE TABLE IF NOT EXISTS email_events (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL DEFAULT 'amazon_ses',
    event_type TEXT NOT NULL,
    notification_type TEXT,
    message_id TEXT,
    ses_message_id TEXT,
    recipient TEXT,
    source_email TEXT,
    subject TEXT,
    bounce_type TEXT,
    bounce_sub_type TEXT,
    complaint_feedback_type TEXT,
    diagnostic_code TEXT,
    occurred_at TIMESTAMPTZ,
    raw_json JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_email_events_type_created
    ON email_events(event_type, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_email_events_recipient
    ON email_events(LOWER(recipient));

CREATE INDEX IF NOT EXISTS idx_email_events_ses_message_id
    ON email_events(ses_message_id);
