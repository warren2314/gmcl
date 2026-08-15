WITH corrected AS (
    UPDATE sanction_cases
    SET is_test = TRUE,
        updated_at = NOW()
    WHERE id = 194
      AND reference = 'GMCL-2026-001193'
      AND NOT is_test
    RETURNING id
)
INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,after_data)
SELECT id,
       'case_marked_test',
       'system',
       'deployment migration',
       'Corrected case 194 after confirmation that it is the Denver workflow test case',
       jsonb_build_object('is_test',TRUE,'migration','0079_mark_case_194_as_test')
FROM corrected;
