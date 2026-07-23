-- Import the approvals published at:
-- https://www.gtrmcrcricket.co.uk/pages/exemption-requests
--
-- The published "Date of Entry" is used as the start of a season-long
-- approval. For single-game approvals, the valid date is the immediately
-- following Sunday named by the request context.
WITH published (
    decision_date,
    club_key,
    club_name,
    player_key,
    player_name,
    exemption_type,
    valid_from,
    valid_to,
    wicket_keeper,
    restrictions,
    reasoning
) AS (
    VALUES
        (
            DATE '2026-04-18', 'stretford', 'Stretford CC', 'trucefernandez', 'Truce Fernandez',
            'sunday_development', DATE '2026-04-18', DATE '2026-08-31', FALSE,
            'Sundays only; the player may not play on Saturdays. The league may rescind the exemption if the player is clearly playing below his true standard.',
            'The player is a Category 3 exemption and appears to be a recreational-level cricketer.'
        ),
        (
            DATE '2026-04-21', 'boltonindians', 'Bolton Indians Cricket Club', 'mohammadgani', 'Mohammad Gani',
            'sunday_development', DATE '2026-04-21', DATE '2026-08-31', TRUE,
            'The player may not bowl or bat higher than seven (or above an under-18 player), but may keep wicket in Sunday cricket. Their main role is mentoring and coaching younger players.',
            'The player is providing mentorship to younger players in the team.'
        ),
        (
            DATE '2026-04-21', 'boltonindians', 'Bolton Indians Cricket Club', 'firozbhaiyat', 'Firoz Bhaiyat',
            'sunday_development', DATE '2026-04-21', DATE '2026-08-31', TRUE,
            'The player may not bowl or bat higher than seven (or above an under-18 player), but may keep wicket in Sunday cricket. Their main role is mentoring and coaching younger players.',
            'The player is providing mentorship to younger players in the team.'
        ),
        (
            DATE '2026-04-21', 'whalleyrange', 'Whalley Range Cricket Club', 'aryanmungla', 'Aryan Mungla',
            'sunday_development', DATE '2026-04-21', DATE '2026-08-31', FALSE,
            'Sundays only; the player may not play on Saturdays. The league may rescind the exemption if the player is clearly playing below his true standard.',
            'The player is a Category 3 exemption and appears to be a recreational-level cricketer.'
        ),
        (
            DATE '2026-05-23', 'glodwick', 'Glodwick CC', 'matthewconnolly', 'Matthew Connolly',
            'sunday_single_match', DATE '2026-05-24', DATE '2026-05-24', FALSE,
            'The player may not bowl during this game.',
            'The team was short of players and needed to raise a side; the player is a regular bowler at a higher level but was approved as a batsman only.'
        ),
        (
            DATE '2026-06-12', 'glossop', 'Glossop CC', 'benwilson', 'Ben Wilson',
            'sunday_development', DATE '2026-06-12', DATE '2026-08-31', FALSE,
            'The player may bat no higher than seven and bowl no more than four overs per innings.',
            'A season-long exemption is appropriate to the player''s development.'
        ),
        (
            DATE '2026-06-12', 'unsworth', 'Unsworth CC', 'muhammadtaqueer', 'Muhammad Taqueer',
            'sunday_development', DATE '2026-06-12', DATE '2026-08-31', FALSE,
            'Sundays only; the player may not play on Saturdays. The league may rescind the exemption if the player is clearly playing below his true standard.',
            'The player is a Category 3 exemption and appears to be a recreational-level cricketer.'
        ),
        (
            DATE '2026-06-12', 'whalleyrange', 'Whalley Range CC', 'dhuruvparikh', 'Dhuruv Parikh',
            'sunday_development', DATE '2026-06-12', DATE '2026-08-31', FALSE,
            'Sundays only; the player may not play on Saturdays. The league may rescind the exemption if the player is clearly playing below his true standard.',
            'The player is a Category 3 exemption and appears to be a recreational-level cricketer.'
        ),
        (
            DATE '2026-06-18', 'monton', 'Monton CC', 'chrishindle', 'Chris Hindle',
            'sunday_single_match', DATE '2026-06-21', DATE '2026-06-21', FALSE,
            'The player may not keep wicket or bat above seven.',
            'Approved for a player shortage and to allow the player to play with his children on Father''s Day; this was a special-circumstances approval for a first-XI player.'
        ),
        (
            DATE '2026-06-18', 'heyside', 'Heyside CC', 'robmcnichol', 'Rob McNichol',
            'sunday_development', DATE '2026-06-18', DATE '2026-08-31', FALSE,
            'Sundays only; the player may not play on Saturdays. The league may rescind the exemption if the player is clearly playing below his true standard.',
            'The player is a Category 3 exemption and appears to be a recreational-level cricketer.'
        ),
        (
            DATE '2026-07-02', 'darcylever', 'Darcy Lever', 'hasanaliwaqas', 'Hasan Ali Waqas',
            'sunday_development', DATE '2026-07-02', DATE '2026-08-31', FALSE,
            'Sundays only; the player may not play on Saturdays. The league may rescind the exemption if the player is clearly playing below his true standard.',
            'The player is a Category 3 exemption and appears to be a recreational-level cricketer.'
        ),
        (
            DATE '2026-07-03', 'greenfield', 'Greenfield', 'matthewjones', 'Matthew Jones',
            'sunday_single_match', DATE '2026-07-05', DATE '2026-07-05', FALSE,
            'For this game, the normal loan rule preventing a player from appearing for their parent club on the same weekend is overridden.',
            'Both clubs agreed, the player is a junior, and the player was considered to be playing at an appropriate standard.'
        ),
        (
            DATE '2026-07-09', 'whalleyrange', 'Whalley Range CC', 'ralfsyrett', 'Ralf Syrett',
            'sunday_development', DATE '2026-07-09', DATE '2026-08-31', FALSE,
            'The player may not bowl and is advised not to bat higher than five.',
            'The player is a first-team bowler who normally bats at eleven and is developing captaincy and coaching skills. The exemption will be monitored so batting contributions do not skew games.'
        ),
        (
            DATE '2026-07-16', 'egerton', 'Egerton CC', 'lewisrawsthorne', 'Lewis Rawsthorne',
            'sunday_development', DATE '2026-07-16', DATE '2026-08-31', FALSE,
            'The player is ineligible to play on Saturdays for the rest of the season.',
            'The player is available only on Sundays for the remainder of the season. The exemption will be monitored.'
        )
),
prepared AS (
    SELECT
        p.*,
        (
            SELECT NULLIF(a.play_cricket_player_id, 0)
            FROM starred_appearances a
            WHERE a.season_year = 2026
              AND a.club_key = p.club_key
              AND a.player_key = p.player_key
              AND a.play_cricket_player_id IS NOT NULL
            ORDER BY a.match_date DESC
            LIMIT 1
        ) AS play_cricket_player_id,
        CASE
            WHEN p.exemption_type = 'sunday_single_match' THEN (
                SELECT a.play_cricket_match_id
                FROM starred_appearances a
                WHERE a.season_year = 2026
                  AND a.club_key = p.club_key
                  AND a.player_key = p.player_key
                  AND a.match_date = p.valid_from
                ORDER BY a.play_cricket_match_id
                LIMIT 1
            )
        END AS play_cricket_match_id,
        'Imported from the published 2026 exemption register. Restrictions: ' ||
            p.restrictions || ' Reasoning: ' || p.reasoning ||
            ' Source: https://www.gtrmcrcricket.co.uk/pages/exemption-requests' AS notes
    FROM published p
),
updated AS (
    UPDATE starred_exemptions e
    SET club_name = p.club_name,
        play_cricket_player_id = COALESCE(p.play_cricket_player_id, e.play_cricket_player_id),
        player_name = p.player_name,
        play_cricket_match_id = p.play_cricket_match_id,
        exemption_type = p.exemption_type,
        status = 'approved',
        valid_from = p.valid_from,
        valid_to = p.valid_to,
        wicket_keeper = p.wicket_keeper,
        notes = p.notes,
        decided_at = COALESCE(e.decided_at, p.decision_date::timestamptz),
        revoked_by = NULL,
        revoked_at = NULL,
        updated_at = now()
    FROM prepared p
    WHERE e.season_year = 2026
      AND e.club_key = p.club_key
      AND e.player_key = p.player_key
    RETURNING e.club_key, e.player_key
)
INSERT INTO starred_exemptions (
    season_year,
    club_key,
    club_name,
    play_cricket_player_id,
    player_key,
    player_name,
    play_cricket_match_id,
    exemption_type,
    status,
    valid_from,
    valid_to,
    wicket_keeper,
    notes,
    created_at,
    decided_at
)
SELECT
    2026,
    p.club_key,
    p.club_name,
    p.play_cricket_player_id,
    p.player_key,
    p.player_name,
    p.play_cricket_match_id,
    p.exemption_type,
    'approved',
    p.valid_from,
    p.valid_to,
    p.wicket_keeper,
    p.notes,
    p.decision_date::timestamptz,
    p.decision_date::timestamptz
FROM prepared p
WHERE NOT EXISTS (
    SELECT 1
    FROM updated u
    WHERE u.club_key = p.club_key
      AND u.player_key = p.player_key
);
