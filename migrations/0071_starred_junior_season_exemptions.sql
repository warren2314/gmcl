-- Persist accepted junior findings as one durable exemption per player and
-- season. Finding reviews are match-specific, so they cannot by themselves
-- suppress breaches imported after the original decision.
ALTER TABLE starred_exemptions
    ADD COLUMN IF NOT EXISTS identity_key TEXT;

UPDATE starred_exemptions
SET identity_key = CASE
    WHEN COALESCE(play_cricket_player_id, 0) > 0
        THEN 'id:' || play_cricket_player_id::text
    ELSE 'key:' || player_key
END
WHERE exemption_type = 'junior_season'
  AND NULLIF(identity_key, '') IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_starred_junior_exemptions_season_identity
    ON starred_exemptions(season_year, club_key, identity_key)
    WHERE exemption_type = 'junior_season';

WITH resolved_findings AS (
    SELECT
        f.*,
        CASE
            WHEN COALESCE(f.play_cricket_player_id, 0) > 0
                THEN f.play_cricket_player_id
            WHEN related_ids.id_count = 1
                THEN related_ids.only_id
            ELSE NULL
        END AS exemption_player_id,
        COALESCE(f.play_cricket_player_id, 0) = 0
            AND related_ids.id_count > 1 AS ambiguous_identity
    FROM starred_finding_reviews f
    LEFT JOIN LATERAL (
        SELECT
            COUNT(DISTINCT known.player_id) AS id_count,
            MIN(known.player_id) AS only_id
        FROM (
            SELECT appearance.play_cricket_player_id AS player_id
            FROM starred_appearances appearance
            WHERE appearance.season_year = f.season_year
              AND appearance.club_key = f.club_key
              AND appearance.player_key = f.player_key
            UNION ALL
            SELECT related.play_cricket_player_id
            FROM starred_finding_reviews related
            WHERE related.season_year = f.season_year
              AND related.club_key = f.club_key
              AND related.player_key = f.player_key
        ) known
        WHERE COALESCE(known.player_id, 0) > 0
    ) related_ids ON TRUE
),
finding_evidence AS (
    SELECT
        f.season_year,
        f.club_key,
        f.club_name,
        f.exemption_player_id AS play_cricket_player_id,
        f.player_key,
        f.player_name,
        CASE
            WHEN COALESCE(f.exemption_player_id, 0) > 0
                THEN 'id:' || f.exemption_player_id::text
            ELSE 'key:' || f.player_key
        END AS identity_key,
        f.decision_note,
        f.reviewed_by,
        COALESCE(a.created_at, f.reviewed_at, f.updated_at, f.created_at) AS decided_at
    FROM resolved_findings f
    LEFT JOIN LATERAL (
        SELECT log.created_at
        FROM audit_logs log
        WHERE log.action = 'starred_finding_accepted'
          AND log.entity_type = 'starred_finding_review'
          AND log.entity_id = f.id
          AND log.metadata @> '{"junior_bulk": true}'::jsonb
        ORDER BY log.created_at, log.id
        LIMIT 1
    ) a ON TRUE
    WHERE NOT f.ambiguous_identity
      AND (
        a.created_at IS NOT NULL
        OR (
            f.status = 'accepted'
            AND EXISTS (
                SELECT 1
                FROM starred_list_periods p
                WHERE p.import_run_id = (
                        SELECT run.id
                        FROM starred_import_runs run
                        WHERE run.season_year = f.season_year
                          AND run.status = 'complete'
                        ORDER BY run.imported_at DESC, run.id DESC
                        LIMIT 1
                    )
                  AND p.season_year = f.season_year
                  AND p.club_key = f.club_key
                  AND p.list_type = f.list_type
                  AND p.valid_from <= f.match_date
                  AND (p.valid_to IS NULL OR p.valid_to > f.match_date)
                  AND EXISTS (
                        SELECT 1
                        FROM unnest(p.tags) tag
                        WHERE tag ILIKE '%17%' OR tag ILIKE '%18%'
                    )
                  AND CASE
                        WHEN EXISTS (
                            SELECT 1
                            FROM starred_identity_mappings mapping
                            WHERE mapping.season_year = p.season_year
                              AND mapping.club_key = p.club_key
                              AND mapping.starred_player_key = p.player_key
                              AND mapping.status = 'confirmed'
                        )
                        THEN EXISTS (
                            SELECT 1
                            FROM starred_identity_mappings mapping
                            WHERE mapping.season_year = p.season_year
                              AND mapping.club_key = p.club_key
                              AND mapping.starred_player_key = p.player_key
                              AND mapping.status = 'confirmed'
                              AND mapping.play_cricket_player_id = f.exemption_player_id
                        )
                        ELSE p.player_key = f.player_key
                    END
            )
        )
      )
),
junior_decisions AS (
    SELECT DISTINCT ON (season_year, club_key, identity_key)
        *
    FROM finding_evidence
    ORDER BY season_year, club_key, identity_key, decided_at
),
prepared AS (
    SELECT
        j.*,
        COALESCE(
            (
                SELECT MIN(s.start_date)
                FROM seasons s
                WHERE EXTRACT(YEAR FROM s.start_date)::integer = j.season_year
                  AND s.is_archived = FALSE
            ),
            (
                SELECT MIN(s.start_date)
                FROM seasons s
                WHERE EXTRACT(YEAR FROM s.start_date)::integer = j.season_year
            ),
            make_date(j.season_year, 4, 1)
        ) AS valid_from,
        COALESCE(
            (
                SELECT MAX(s.end_date)
                FROM seasons s
                WHERE EXTRACT(YEAR FROM s.start_date)::integer = j.season_year
                  AND s.is_archived = FALSE
            ),
            (
                SELECT MAX(s.end_date)
                FROM seasons s
                WHERE EXTRACT(YEAR FROM s.start_date)::integer = j.season_year
            ),
            make_date(j.season_year, 10, 31)
        ) AS valid_to
    FROM junior_decisions j
)
INSERT INTO starred_exemptions (
    season_year,
    club_key,
    club_name,
    play_cricket_player_id,
    player_key,
    player_name,
    identity_key,
    exemption_type,
    status,
    valid_from,
    valid_to,
    wicket_keeper,
    notes,
    created_by,
    created_at,
    decided_by,
    decided_at
)
SELECT
    p.season_year,
    p.club_key,
    p.club_name,
    p.play_cricket_player_id,
    p.player_key,
    p.player_name,
    p.identity_key,
    'junior_season',
    'approved',
    p.valid_from,
    p.valid_to,
    FALSE,
    'Backfilled from an accepted junior finding review.' ||
        CASE
            WHEN NULLIF(BTRIM(COALESCE(p.decision_note, '')), '') IS NOT NULL
            THEN ' Decision note: ' || BTRIM(p.decision_note)
            ELSE ''
        END,
    p.reviewed_by,
    p.decided_at,
    p.reviewed_by,
    p.decided_at
FROM prepared p
ON CONFLICT (season_year, club_key, identity_key)
    WHERE exemption_type = 'junior_season'
DO NOTHING;