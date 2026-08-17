-- A withdrawn case is historical work. Its source intake must not return to
-- the selected-report queue after the effective case link is retired.

WITH candidates AS (
    SELECT DISTINCT ON (intake.id)
           intake.id,
           intake.state,
           cases.id AS case_id,
           cases.reference
    FROM sanction_intakes intake
    JOIN sanction_intake_case_links link ON link.intake_id=intake.id
    JOIN sanction_cases cases ON cases.id=link.case_id
    WHERE intake.state IN ('new','reviewing')
      AND cases.status='withdrawn'
      AND EXISTS (
          SELECT 1
          FROM sanction_case_events event
          WHERE event.case_id=cases.id
            AND event.event_type='case_opening_undone'
      )
      AND NOT EXISTS (
          SELECT 1
          FROM sanction_intake_effective_case_links active_link
          WHERE active_link.intake_id=intake.id
      )
    ORDER BY intake.id,cases.id DESC
)
INSERT INTO sanction_intake_events(
    intake_id,event_type,actor_label,reason,before_data,after_data
)
SELECT candidate.id,
       'ignored',
       'System correction',
       'Retired source report because its case was withdrawn',
       jsonb_build_object(
           'state',candidate.state,
           'withdrawn_case_id',candidate.case_id,
           'withdrawn_case_reference',candidate.reference
       ),
       jsonb_build_object(
           'state','ignored',
           'withdrawn_case_id',candidate.case_id,
           'withdrawn_case_reference',candidate.reference
       )
FROM candidates candidate
WHERE NOT EXISTS (
    SELECT 1
    FROM sanction_intake_events existing
    WHERE existing.intake_id=candidate.id
      AND existing.event_type='ignored'
      AND existing.reason='Retired source report because its case was withdrawn'
);

UPDATE sanction_intakes intake
SET state='ignored',
    exception_message=NULL,
    updated_at=now()
WHERE intake.state IN ('new','reviewing')
  AND EXISTS (
      SELECT 1
      FROM sanction_intake_case_links link
      JOIN sanction_cases cases ON cases.id=link.case_id
      WHERE link.intake_id=intake.id
        AND cases.status='withdrawn'
        AND EXISTS (
            SELECT 1
            FROM sanction_case_events event
            WHERE event.case_id=cases.id
              AND event.event_type='case_opening_undone'
        )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM sanction_intake_effective_case_links active_link
      WHERE active_link.intake_id=intake.id
  );
