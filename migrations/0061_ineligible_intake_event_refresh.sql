-- A repeated link command is recorded as an immutable refresh event after it
-- revalidates the existing relationship. Keep the audit vocabulary explicit.

ALTER TABLE sanction_intake_events
    DROP CONSTRAINT IF EXISTS sanction_intake_events_event_type_check;

ALTER TABLE sanction_intake_events
    ADD CONSTRAINT sanction_intake_events_event_type_check
    CHECK (event_type IN
        ('case_created','case_linked','case_link_refreshed','marked_duplicate','ignored'));
