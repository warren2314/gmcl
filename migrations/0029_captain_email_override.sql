-- Temporary email override for a captain.
-- When email_override_until >= CURRENT_DATE, reminder emails go to email_override
-- instead of the captain's permanent email address.
ALTER TABLE captains
    ADD COLUMN IF NOT EXISTS email_override       TEXT,
    ADD COLUMN IF NOT EXISTS email_override_until DATE;
