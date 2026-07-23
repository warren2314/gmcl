-- Historical card imports and the transitional sanctions read-model can
-- describe the same card. Link one-to-one matches so loadLedgerState counts
-- the canonical card ledger entry and not the legacy projection as well.
DO $$
DECLARE
    matched RECORD;
    before_row JSONB;
    after_row JSONB;
BEGIN
    FOR matched IN
        WITH imported_cards AS (
            SELECT c.id AS case_id,
                   c.team_id,
                   c.week_id,
                   CASE e.effect_type
                       WHEN 'yellow_card' THEN 'yellow'
                       WHEN 'red_card' THEN 'red'
                   END AS colour,
                   ROW_NUMBER() OVER (
                       PARTITION BY c.team_id,c.week_id,e.effect_type
                       ORDER BY c.id
                   ) AS ordinal
            FROM sanction_cases c
            JOIN sanction_decision_revisions d
              ON d.case_id=c.id
             AND d.status='approved'
            JOIN sanction_effect_revisions e
              ON e.decision_revision_id=d.id
             AND e.effect_type IN ('yellow_card','red_card')
            JOIN sanction_card_ledger_entries l
              ON l.case_id=c.id
             AND l.decision_revision_id=d.id
             AND l.entry_type='import'
             AND (
                 (e.effect_type='yellow_card' AND l.yellow_delta > 0)
                 OR (e.effect_type='red_card' AND l.red_delta > 0)
             )
            WHERE c.source_type='historical_import'
              AND c.team_id IS NOT NULL
              AND c.week_id IS NOT NULL
              AND NOT EXISTS (
                  SELECT 1
                  FROM sanction_effect_revisions newer
                  WHERE newer.supersedes_id=e.id
              )
        ),
        legacy_cards AS (
            SELECT sa.id AS sanction_id,
                   sa.team_id,
                   sa.week_id,
                   sa.colour::text AS colour,
                   ROW_NUMBER() OVER (
                       PARTITION BY sa.team_id,sa.week_id,sa.colour
                       ORDER BY sa.id
                   ) AS ordinal
            FROM sanctions sa
            WHERE sa.case_id IS NULL
              AND sa.status IN ('active','served')
        )
        SELECT legacy.sanction_id, imported.case_id
        FROM legacy_cards legacy
        JOIN imported_cards imported
          ON imported.team_id=legacy.team_id
         AND imported.week_id=legacy.week_id
         AND imported.colour=legacy.colour
         AND imported.ordinal=legacy.ordinal
        ORDER BY legacy.sanction_id
    LOOP
        SELECT to_jsonb(sa) INTO before_row
        FROM sanctions sa
        WHERE sa.id=matched.sanction_id
        FOR UPDATE;

        UPDATE sanctions
        SET case_id=matched.case_id
        WHERE id=matched.sanction_id
          AND case_id IS NULL
        RETURNING to_jsonb(sanctions) INTO after_row;

        IF FOUND THEN
            INSERT INTO sanction_events
                (sanction_id,event_type,event_at,notes,actor_label,reason,before_data,after_data,request_id)
            VALUES
                (matched.sanction_id,'historical_import_linked',now(),
                 'Legacy read-model card linked to its canonical imported case',
                 'sanctions ledger reconciliation migration',
                 'Prevent duplicate card totting after historical import',
                 before_row,after_row,'migration:0046_reconcile_imported_legacy_sanctions');
        END IF;
    END LOOP;
END
$$;
