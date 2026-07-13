CREATE TABLE IF NOT EXISTS ses_webhook_receipts (
    id              BIGSERIAL PRIMARY KEY,
    sns_message_id  TEXT,
    message_type    TEXT,
    delivery_mode   TEXT NOT NULL,
    topic_arn       TEXT,
    status          TEXT NOT NULL,
    detail          TEXT,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_ses_webhook_receipts_message
    ON ses_webhook_receipts(sns_message_id)
    WHERE sns_message_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ses_webhook_receipts_received
    ON ses_webhook_receipts(received_at DESC);
