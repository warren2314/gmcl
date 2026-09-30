-- Preserve the captain's original report before removing a demonstrably wrong
-- umpire attribution from the calculated 2026 marks. Apply only to matching
-- source reports, so a test or staging database without them remains usable.
-- The corrected identity is
-- deliberately left blank: do not transfer a captain's subjective mark to a
-- different umpire without that captain or the official allocation confirming it.
BEGIN;

CREATE TABLE IF NOT EXISTS umpire_rating_corrections (
    submission_id BIGINT NOT NULL REFERENCES submissions(id),
    umpire_slot SMALLINT NOT NULL CHECK (umpire_slot IN (1, 2)),
    expected_match_id BIGINT NOT NULL,
    expected_umpire_name TEXT NOT NULL,
    original_form_data JSONB NOT NULL,
    corrected_form_data JSONB NOT NULL,
    reason TEXT NOT NULL,
    evidence TEXT NOT NULL,
    corrected_by TEXT NOT NULL,
    corrected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (submission_id, umpire_slot)
);

WITH candidates(submission_id, umpire_slot, expected_match_id, expected_name, reason, evidence) AS (
    VALUES
    (1920, 1, 7247785, 'Stuart Russell',
     'Named at Stand v Mottram on the same Saturday; Woodley opponent and fixture identify a different first umpire.',
     'Stand and Mottram reports 1919, 2007; Woodley report 1920; Tottington St Johns report 1972; fixture 7247785'),
    (2186, 2, 7239856, 'Denver Thornton',
     'Denver officiated at Elton v Glossop on the same Saturday; Edenfield reports no second umpire.',
     'Elton and Glossop reports 2141, 2254; Edenfield report 2150; Flowery Field report 2186; Mick confirmation'),
    (2228, 1, 7239842, 'Nigel Stock',
     'Named at Westhoughton v Heywood on the same Saturday; Springhead reports a different umpire.',
     'Westhoughton and Heywood reports 2201, 2112; Springhead report 2083; Brooksbottom report 2228'),
    (2684, 2, 7239980, 'Peter Thew',
     'Named by both captains at Monton v Horwich RMI on the same Saturday; Heyside names a different second umpire.',
     'Monton and Horwich RMI reports 2620, 2695; Mottram report 2684; Heyside report 2714'),
    (2703, 1, 7239976, 'James Clarke',
     'Named at Heaton v Astley and Tyldesley on the same Saturday; Brooksbottom and the fixture name a different first umpire.',
     'Heaton report 2548; Brooksbottom report 2607; Woodbank report 2703; fixtures 7239976, 7239982'),
    (2736, 2, 7239982, 'Linval Grant',
     'Named by both captains at Saddleworth v Stayley on the same Saturday; Heaton and the fixture name a different second umpire.',
     'Stayley and Saddleworth reports 2571, 2582; Heaton report 2548; Astley and Tyldesley report 2736'),
    (4216, 1, 7240430, 'Stephen Kirkbright',
     'Named by both captains at Westhoughton v Walshaw on the same Saturday; the Heywood fixture names a different first umpire.',
     'Walshaw and Westhoughton reports 4072, 4098; Astley and Tyldesley report 4216; fixtures 7240429, 7240430')
), prepared AS (
    SELECT s.id, c.umpire_slot, c.expected_match_id, c.expected_name,
           s.form_data AS original_form_data,
           s.form_data - CASE c.umpire_slot
               WHEN 1 THEN ARRAY['umpire1_name','umpire1_performance','umpire1_type',
                   'decision_making_umpire1','match_management_umpire1',
                   'player_management_umpire1','presence_image_umpire1','teamwork_umpire1']
               ELSE ARRAY['umpire2_name','umpire2_performance','umpire2_type',
                   'decision_making_umpire2','match_management_umpire2',
                   'player_management_umpire2','presence_image_umpire2','teamwork_umpire2']
           END AS corrected_form_data,
           c.reason,c.evidence
    FROM candidates c
    JOIN submissions s ON s.id=c.submission_id
    WHERE s.play_cricket_match_id=c.expected_match_id
      AND s.form_data->>(CASE c.umpire_slot WHEN 1 THEN 'umpire1_name' ELSE 'umpire2_name' END)=c.expected_name
      AND s.form_data->>(CASE c.umpire_slot WHEN 1 THEN 'umpire1_performance' ELSE 'umpire2_performance' END)
          IN ('Good','Average','Poor')
)
INSERT INTO umpire_rating_corrections (
    submission_id, umpire_slot, expected_match_id, expected_umpire_name,
    original_form_data, corrected_form_data, reason, evidence, corrected_by
)
SELECT id,umpire_slot,expected_match_id,expected_name,
       original_form_data,corrected_form_data,reason,evidence,'2026-09-30-umpire-audit'
FROM prepared
ON CONFLICT (submission_id,umpire_slot) DO NOTHING;

WITH corrected AS (
    UPDATE submissions s
       SET form_data=c.corrected_form_data, updated_at=now()
      FROM umpire_rating_corrections c
     WHERE s.id=c.submission_id
       AND c.corrected_by='2026-09-30-umpire-audit'
       AND s.form_data=c.original_form_data
    RETURNING s.id,c.umpire_slot,c.expected_umpire_name,c.reason,c.evidence
)
INSERT INTO audit_logs (actor_type, action, entity_type, entity_id, metadata)
SELECT 'system', 'umpire_rating_attribution_corrected', 'submission', id,
       jsonb_build_object('umpire_slot',umpire_slot,'removed_name',expected_umpire_name,
                          'reason',reason,'evidence',evidence,'migration','0091')
FROM corrected;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM umpire_rating_corrections c
        JOIN submissions s ON s.id=c.submission_id
        WHERE c.corrected_by='2026-09-30-umpire-audit'
          AND s.form_data IS DISTINCT FROM c.corrected_form_data
    ) THEN
        RAISE EXCEPTION 'A corrected submission no longer matches the reviewed form data';
    END IF;
END $$;

COMMIT;
