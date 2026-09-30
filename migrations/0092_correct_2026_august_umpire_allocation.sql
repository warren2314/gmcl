-- GMCLUA confirmed that Linval Grant and Neil Cadd officiated Shaw v Egerton
-- on 1 August 2026, while Damian Grundy and Kamlesh Rajput officiated
-- Bolton Indians v Deane & Derby. Both Bolton captain reports attributed a
-- Good rating and five 5/5 marks to Linval. Preserve those reports and remove
-- only Linval's misattributed rating; do not transfer subjective marks to
-- Kamlesh Rajput without the captains confirming whom they rated.
BEGIN;

WITH candidates(submission_id, expected_match_id, expected_name, evidence) AS (
    VALUES
    (3426, 7240253, 'Linval Grant',
     'Mick confirmed by email on 30 September 2026: Neil Cadd and Linval Grant at Shaw v Egerton; Damian Grundy and Kamlesh Rajput at Bolton Indians v Deane & Derby. Bolton Indians captain report 3426; Shaw and Egerton reports 3387 and 3423.'),
    (3467, 7240253, 'Linval Grant',
     'Mick confirmed by email on 30 September 2026: Neil Cadd and Linval Grant at Shaw v Egerton; Damian Grundy and Kamlesh Rajput at Bolton Indians v Deane & Derby. Deane & Derby captain report 3467; Shaw and Egerton reports 3387 and 3423.')
), prepared AS (
    SELECT s.id, c.expected_match_id, c.expected_name,
           s.form_data AS original_form_data,
           s.form_data - ARRAY[
               'umpire1_name','umpire1_performance','umpire1_type',
               'decision_making_umpire1','match_management_umpire1',
               'player_management_umpire1','presence_image_umpire1','teamwork_umpire1'
           ] AS corrected_form_data,
           c.evidence
    FROM candidates c
    JOIN submissions s ON s.id=c.submission_id
    WHERE s.play_cricket_match_id=c.expected_match_id
      AND s.match_date=DATE '2026-08-01'
      AND s.form_data->>'umpire1_name'=c.expected_name
      AND s.form_data->>'umpire1_performance'='Good'
      AND s.form_data->>'umpire2_name'='Damian Grundy'
)
INSERT INTO umpire_rating_corrections (
    submission_id, umpire_slot, expected_match_id, expected_umpire_name,
    original_form_data, corrected_form_data, reason, evidence, corrected_by
)
SELECT id,1,expected_match_id,expected_name,
       original_form_data,corrected_form_data,
       'Official GMCLUA allocation places Linval at Shaw v Egerton and Kamlesh Rajput, not Linval, at Bolton Indians v Deane & Derby.',
       evidence,'2026-09-30-gmclua-allocation'
FROM prepared
ON CONFLICT (submission_id,umpire_slot) DO NOTHING;

WITH corrected AS (
    UPDATE submissions s
       SET form_data=c.corrected_form_data, updated_at=now()
      FROM umpire_rating_corrections c
     WHERE s.id=c.submission_id
       AND c.corrected_by='2026-09-30-gmclua-allocation'
       AND s.form_data=c.original_form_data
    RETURNING s.id,c.umpire_slot,c.expected_umpire_name,c.reason,c.evidence
)
INSERT INTO audit_logs (actor_type, action, entity_type, entity_id, metadata)
SELECT 'system', 'umpire_rating_attribution_corrected', 'submission', id,
       jsonb_build_object('umpire_slot',umpire_slot,'removed_name',expected_umpire_name,
                          'reason',reason,'evidence',evidence,'migration','0092')
FROM corrected;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM umpire_rating_corrections c
        JOIN submissions s ON s.id=c.submission_id
        WHERE c.corrected_by='2026-09-30-gmclua-allocation'
          AND s.form_data IS DISTINCT FROM c.corrected_form_data
    ) THEN
        RAISE EXCEPTION 'An August allocation correction no longer matches the reviewed form data';
    END IF;
END $$;

COMMIT;
