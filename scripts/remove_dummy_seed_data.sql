BEGIN;

DO $$
DECLARE
    seed_club_names TEXT[] := ARRAY[
        'Test Club',
        'Heaton Mersey CC',
        'Denton St Lawrence CC',
        'Woodhouses CC',
        'Prestwich CC',
        'Swinton Moorside CC',
        'Royton CC',
        'Norden CC',
        'Hyde CC'
    ];
    seed_captain_emails TEXT[] := ARRAY[
        'captain@test.local',
        'hm1@gmcl.test',
        'hm2@gmcl.test',
        'dsl1@gmcl.test',
        'dsl2@gmcl.test',
        'wh1@gmcl.test',
        'wh2@gmcl.test',
        'pre1@gmcl.test',
        'pre2@gmcl.test',
        'sm1@gmcl.test',
        'roy1@gmcl.test',
        'roy2@gmcl.test',
        'nor1@gmcl.test',
        'hyd1@gmcl.test',
        'hyd2@gmcl.test'
    ];
    seed_pc_team_ids TEXT[] := ARRAY[
        '10011', '10012', '10021', '10022',
        '10031', '10032', '10041', '10042',
        '10051', '10061', '10062', '10071',
        '10081', '10082'
    ];
    seed_fixture_ids BIGINT[] := ARRAY[90001, 90002, 90003, 90004, 90101, 90102, 90103];
BEGIN
    DELETE FROM sanctions
    WHERE club_id IN (
            SELECT id FROM clubs WHERE name = ANY(seed_club_names)
        )
       OR team_id IN (
            SELECT id
            FROM teams
            WHERE club_id IN (SELECT id FROM clubs WHERE name = ANY(seed_club_names))
               OR play_cricket_team_id = ANY(seed_pc_team_ids)
        )
       OR season_id IN (
            SELECT id FROM seasons WHERE name = 'Test Season'
        )
       OR week_id IN (
            SELECT w.id
            FROM weeks w
            JOIN seasons s ON s.id = w.season_id
            WHERE s.name = 'Test Season'
        );

    DELETE FROM generated_reports
    WHERE season_id IN (
            SELECT id FROM seasons WHERE name = 'Test Season'
        )
       OR week_id IN (
            SELECT w.id
            FROM weeks w
            JOIN seasons s ON s.id = w.season_id
            WHERE s.name = 'Test Season'
        );

    DELETE FROM submissions
    WHERE captain_id IN (
            SELECT id FROM captains WHERE email = ANY(seed_captain_emails)
        )
       OR team_id IN (
            SELECT id
            FROM teams
            WHERE club_id IN (SELECT id FROM clubs WHERE name = ANY(seed_club_names))
               OR play_cricket_team_id = ANY(seed_pc_team_ids)
        )
       OR season_id IN (
            SELECT id FROM seasons WHERE name = 'Test Season'
        )
       OR week_id IN (
            SELECT w.id
            FROM weeks w
            JOIN seasons s ON s.id = w.season_id
            WHERE s.name = 'Test Season'
        );

    DELETE FROM league_fixtures
    WHERE play_cricket_match_id = ANY(seed_fixture_ids)
       OR home_team_pc_id = ANY(seed_pc_team_ids)
       OR away_team_pc_id = ANY(seed_pc_team_ids);

    DELETE FROM captains
    WHERE email = ANY(seed_captain_emails);

    DELETE FROM teams
    WHERE club_id IN (
            SELECT id FROM clubs WHERE name = ANY(seed_club_names)
        )
       OR play_cricket_team_id = ANY(seed_pc_team_ids);

    DELETE FROM clubs
    WHERE name = ANY(seed_club_names);

    DELETE FROM seasons
    WHERE name = 'Test Season';
END $$;

COMMIT;
