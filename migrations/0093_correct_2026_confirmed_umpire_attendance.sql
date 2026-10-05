-- Official GMCLUA attendance confirmations supplied by Warren on 5 October 2026.
-- Remove only incorrectly attributed captain rating slots; keep original forms.
-- No subjective mark is transferred to an alternative umpire without captain
-- confirmation. Coulding Senior/Junior first-umpire identities are pending.
-- When both slots are affected, both correction records contain the same complete
-- original and corrected forms; update each submission once and audit every slot.
BEGIN;

CREATE TEMP TABLE gmcl_20261005_umpire_candidates (
    submission_id BIGINT NOT NULL,
    umpire_slot SMALLINT NOT NULL,
    expected_match_id BIGINT NOT NULL,
    expected_date DATE NOT NULL,
    expected_name TEXT NOT NULL,
    expected_grade TEXT NOT NULL,
    expected_form_hash TEXT NOT NULL,
    evidence TEXT NOT NULL,
    PRIMARY KEY (submission_id, umpire_slot)
) ON COMMIT DROP;

INSERT INTO gmcl_20261005_umpire_candidates VALUES
    (1168, 2, 7512878, DATE '2026-05-17', 'Muhammad Shahid', 'Good', '314a4485764fd84a9145239b7e9b3841', 'Shahid Ahmed and Mohammed Chowdhury at Werneth v Monton; Muhammad Shahid at Milnrow v Whalley Range.'),
    (1645, 1, 7691745, DATE '2026-05-31', 'MahammedArshad Saiyed', 'Good', 'f28cbea2cf5c0fa4e53f42e67bbb2318', 'Shahid Ahmed and Beverley Wilson at Springhead v Stalybridge; Mahammed Arshad Saiyed at Hindley St Peters v Daisy Hill.'),
    (1671, 1, 7691745, DATE '2026-05-31', 'MahammedArshad Saiyed', 'Poor', 'b98c7b0a0cc35c00bbff0f4a7953e235', 'Shahid Ahmed and Beverley Wilson at Springhead v Stalybridge; Mahammed Arshad Saiyed at Hindley St Peters v Daisy Hill.'),
    (1847, 2, 7239733, DATE '2026-06-06', 'Jayprakash Joshi', 'Average', '0321e731905755cd5c3ad6c12e39d780', 'Thomas George and Beverley Wilson at Austerlands v Whalley Range; Jay Joshi at Glossop v Flowery Field.'),
    (2077, 1, 7239859, DATE '2026-06-20', 'Paul Anthony Higgins', 'Average', 'b363895b86c36b11445319689ba265c8', 'Thomas George and Geoff Greenop at Flixton v Stayley; Paul Higgins at Astley & Tyldesley v Woodhouses.'),
    (2096, 1, 7239859, DATE '2026-06-20', 'Paul Anthony Higgins', 'Good', 'd09ce445bc0133572d2f8322ede1ae7e', 'Thomas George and Geoff Greenop at Flixton v Stayley; Paul Higgins at Astley & Tyldesley v Woodhouses.'),
    (2146, 1, 7239881, DATE '2026-06-20', 'Steve Ward', 'Good', '3b97f941a44adcfeaad2b664c4a30300', 'Steve Ward at Prestwich v Bolton Indians; no panel appointments at Micklehurst v Droylsden. Other club-umpire identities remain unverified.'),
    (3212, 2, 7240178, DATE '2026-07-25', 'Geoff sumner', 'Good', '5d9647a03250ec8ed89cd2ad6b1ef55e', 'Geoff Greenop and Lee Harding at Saddleworth v Flowery Field. The submitted name Geoff sumner is not confirmed; no grade transferred.'),
    (3282, 1, 7240178, DATE '2026-07-25', 'Jayprakash Joshi', 'Good', 'fd39a5ab1142250fd141d693c2e41222', 'Geoff Greenop and Lee Harding at Saddleworth v Flowery Field; neither Jay Joshi nor Roger Richards officiated. Roger withdrew through illness.'),
    (3282, 2, 7240178, DATE '2026-07-25', 'Roger Richards', 'Good', 'fd39a5ab1142250fd141d693c2e41222', 'Geoff Greenop and Lee Harding at Saddleworth v Flowery Field; Roger Richards withdrew through illness.'),
    (3183, 1, 7240194, DATE '2026-07-25', 'Roger Richards', 'Good', '9cd122d50ef76f2bbf414ea23aa78b4d', 'No panel appointments at Heyside v Flixton. Roger Richards withdrew through illness and did not officiate the other reported fixture.'),
    (3583, 2, 7460846, DATE '2026-08-02', 'Alan Naylor', 'Good', 'e22bdb6f2359fef4aab8f42da75d7f63', 'Muhammad Shahid and Alan Wilson at Greenfield v Denton West, not Alan Naylor.'),
    (3576, 1, 7510086, DATE '2026-08-02', 'Alan Wilson', 'Average', '16be104c3de1cf22d0965ecbc6a68882', 'Beverley Wilson and Suhail Rana at Springhead v Ashton Ladysmith; Alan Wilson at Greenfield v Denton West.'),
    (3520, 1, 7728152, DATE '2026-08-02', 'Muhammad Shahid', 'Good', '3fd54558957f7fce05abe1ce951d5743', 'Ramki Kalyan and Mahammed Arshad Saiyed at Deane & Derby v Worsley; Muhammad Shahid at Greenfield v Denton West.'),
    (3520, 2, 7728152, DATE '2026-08-02', 'Asif Lohdi', 'Good', '3fd54558957f7fce05abe1ce951d5743', 'Ramki Kalyan and Mahammed Arshad Saiyed at Deane & Derby v Worsley; Asif Lohdi at Stayley v Greenfield.'),
    (3833, 2, 7240368, DATE '2026-08-15', 'Steve Wilkinson', 'Good', 'b7ba11df4b982e8282765b883fb5055d', 'Damian Grundy officiated alone at Swinton Moorside v Flixton; Steve Wilkinson officiated alone at Ashton v Adlington.'),
    (3940, 2, 7247741, DATE '2026-08-15', 'Richard Unwin', 'Poor', '6453e86de990a8bb4d7d6455f796c76d', 'Richard Unwin confirmed at Flowery Field v Stayley on the same Saturday. Droylsden report 3912 records no second umpire at Glodwick; full Glodwick allocation remains requested.'),
    (4066, 2, 7240444, DATE '2026-08-22', 'Peter McAndrew', 'Average', '199baab1993a5cd6789b2542448b5b7e', 'Wilf Seville and Zohaib Shehzad at Bury v Woodley; Peter McAndrew at Deane & Derby v Prestwich.'),
    (4196, 2, 7240444, DATE '2026-08-22', 'Peter McAndrew', 'Good', '5947978db63183f08e8222912d6e310c', 'Wilf Seville and Zohaib Shehzad at Bury v Woodley; Peter McAndrew at Deane & Derby v Prestwich.'),
    (4127, 2, 7461009, DATE '2026-08-23', 'Stephen', 'Good', '6ac4a54d8f0af2d6509d86151f6e9de0', 'Steve Coulding Senior and Roger Richards at Werneth v Roe Green. The separate second-umpire name Stephen is unconfirmed; Coulding Senior/Junior first-umpire ratings await identity reconciliation.'),
    (4430, 1, 7461057, DATE '2026-08-30', 'Stuart Russell', 'Good', '68c2dbcddf60ec7e767e525d4bc41482', 'Stewart Dobson and Chandan Singh Shekhawat at Prestwich v Greenfield, not Stuart Russell.'),
    (4430, 2, 7461057, DATE '2026-08-30', 'Suhail Rana', 'Good', '68c2dbcddf60ec7e767e525d4bc41482', 'Stewart Dobson and Chandan Singh Shekhawat at Prestwich v Greenfield; Suhail Rana at Glodwick v Whalley Range.'),
    (4804, 1, 7461711, DATE '2026-09-13', 'Bashir Ahmed', 'Good', 'ba02c11b71ebcb6c772efd84e5a98d3b', 'Sarfraz Ismail Ahmad and Adeel Arif at Whalley Range v Prestwich 3rd XI; Bashir Ahmed at Prestwich 4th XI v Astley & Tyldesley.'),
    (4804, 2, 7461711, DATE '2026-09-13', 'Peter Masters', 'Good', 'ba02c11b71ebcb6c772efd84e5a98d3b', 'Sarfraz Ismail Ahmad and Adeel Arif at Whalley Range v Prestwich 3rd XI; Peter Masters at Prestwich 4th XI v Astley & Tyldesley.');

-- Refuse a changed source report or an earlier correction of the same slot.
-- Databases without these production report IDs may apply the migration safely.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM gmcl_20261005_umpire_candidates c
        JOIN submissions s ON s.id=c.submission_id
        LEFT JOIN umpire_rating_corrections a
          ON a.submission_id=c.submission_id AND a.umpire_slot=c.umpire_slot
        WHERE s.play_cricket_match_id IS DISTINCT FROM c.expected_match_id
           OR s.match_date IS DISTINCT FROM c.expected_date
           OR s.season_id IS DISTINCT FROM (SELECT id FROM seasons WHERE name='2026' ORDER BY id DESC LIMIT 1)
           OR (a.submission_id IS NOT NULL AND a.corrected_by <> '2026-10-05-gmclua-attendance')
           OR (md5(s.form_data::text) <> c.expected_form_hash AND NOT COALESCE((
               a.corrected_by='2026-10-05-gmclua-attendance'
               AND s.form_data=a.corrected_form_data),false))
           OR (a.submission_id IS NULL AND (
               s.form_data->>('umpire'||c.umpire_slot||'_name') IS DISTINCT FROM c.expected_name
               OR s.form_data->>('umpire'||c.umpire_slot||'_performance') IS DISTINCT FROM c.expected_grade))
    ) THEN
        RAISE EXCEPTION 'October umpire correction source no longer matches the reviewed report';
    END IF;
END $$;

WITH removal_keys AS (
    SELECT c.submission_id, array_agg(k.key) AS keys
    FROM gmcl_20261005_umpire_candidates c
    CROSS JOIN LATERAL unnest(ARRAY[
        'umpire'||c.umpire_slot||'_name',
        'umpire'||c.umpire_slot||'_performance',
        'umpire'||c.umpire_slot||'_type',
        'decision_making_umpire'||c.umpire_slot,
        'match_management_umpire'||c.umpire_slot,
        'player_management_umpire'||c.umpire_slot,
        'presence_image_umpire'||c.umpire_slot,
        'teamwork_umpire'||c.umpire_slot
    ]) k(key)
    GROUP BY c.submission_id
), prepared AS (
    SELECT c.*,s.form_data AS original_form_data,
           s.form_data-r.keys AS corrected_form_data
    FROM gmcl_20261005_umpire_candidates c
    JOIN submissions s ON s.id=c.submission_id
    JOIN removal_keys r ON r.submission_id=s.id
    WHERE md5(s.form_data::text)=c.expected_form_hash
)
INSERT INTO umpire_rating_corrections (
    submission_id,umpire_slot,expected_match_id,expected_umpire_name,
    original_form_data,corrected_form_data,reason,evidence,corrected_by
)
SELECT submission_id,umpire_slot,expected_match_id,expected_name,
       original_form_data,corrected_form_data,
       'Confirmed GMCLUA attendance contradicts the captain report attribution; exclude this slot pending captain confirmation of any replacement.',
       'Official attendance email supplied by Warren on 5 October 2026. '||evidence,
       '2026-10-05-gmclua-attendance'
FROM prepared
ON CONFLICT (submission_id,umpire_slot) DO NOTHING;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM gmcl_20261005_umpire_candidates c
        JOIN submissions s ON s.id=c.submission_id
        LEFT JOIN umpire_rating_corrections a
          ON a.submission_id=c.submission_id AND a.umpire_slot=c.umpire_slot
         AND a.corrected_by='2026-10-05-gmclua-attendance'
        WHERE a.submission_id IS NULL
    ) THEN
        RAISE EXCEPTION 'An October umpire correction has no preserved original report';
    END IF;
END $$;

WITH forms AS (
    SELECT DISTINCT submission_id,original_form_data,corrected_form_data
    FROM umpire_rating_corrections
    WHERE corrected_by='2026-10-05-gmclua-attendance'
), corrected AS (
    UPDATE submissions s
       SET form_data=f.corrected_form_data,updated_at=now()
      FROM forms f
     WHERE s.id=f.submission_id AND s.form_data=f.original_form_data
    RETURNING s.id
)
INSERT INTO audit_logs (actor_type,action,entity_type,entity_id,metadata)
SELECT 'system','umpire_rating_attribution_corrected','submission',s.id,
       jsonb_build_object('umpire_slot',c.umpire_slot,'removed_name',c.expected_umpire_name,
                          'reason',c.reason,'evidence',c.evidence,'migration','0093')
FROM corrected s
JOIN umpire_rating_corrections c ON c.submission_id=s.id
WHERE c.corrected_by='2026-10-05-gmclua-attendance';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM umpire_rating_corrections c
        JOIN submissions s ON s.id=c.submission_id
        WHERE c.corrected_by='2026-10-05-gmclua-attendance'
          AND s.form_data IS DISTINCT FROM c.corrected_form_data
    ) THEN
        RAISE EXCEPTION 'An October umpire correction does not match the complete corrected form';
    END IF;
END $$;

COMMIT;
