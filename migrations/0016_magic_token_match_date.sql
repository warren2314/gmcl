-- Add match_date to magic_link_tokens so reminders for different fixtures
-- within the same week do not revoke each other.
ALTER TABLE magic_link_tokens ADD COLUMN IF NOT EXISTS match_date date;
