-- A case opened from an intake can be retired without deleting its immutable
-- provenance. The event makes the original link historical rather than active.

ALTER TABLE sanction_intake_events
    DROP CONSTRAINT IF EXISTS sanction_intake_events_event_type_check;

ALTER TABLE sanction_intake_events
    ADD CONSTRAINT sanction_intake_events_event_type_check
    CHECK (event_type IN
        ('case_created','case_linked','case_link_refreshed','case_opening_undone',
         'marked_duplicate','ignored'));

CREATE OR REPLACE VIEW sanction_intake_effective_case_links AS
SELECT link.*
FROM sanction_intake_case_links link
WHERE NOT EXISTS (
    SELECT 1
    FROM sanction_case_events event
    WHERE event.case_id=link.case_id
      AND event.event_type='case_opening_undone'
);

