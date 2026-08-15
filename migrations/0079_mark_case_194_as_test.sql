INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,after_data)
SELECT cases.id,
       'case_training_designated',
       'system',
       'deployment migration',
       'Designated case 194 for the Denver approval demonstration without changing its correspondence classification',
       jsonb_build_object('training_case',TRUE,'migration','0079_mark_case_194_as_test')
FROM sanction_cases cases
WHERE cases.id = 194
  AND cases.reference = 'GMCL-2026-001193'
  AND NOT EXISTS (
      SELECT 1 FROM sanction_case_events event
      WHERE event.case_id = cases.id
        AND event.event_type = 'case_training_designated'
  );
