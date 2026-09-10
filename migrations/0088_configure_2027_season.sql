-- Playing-season dates confirmed by Warren on 10 September 2026.
-- Configure the target for future sanctions without creating weeks, moving
-- existing cases, or applying any penalties.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM seasons
        WHERE name = '2027' OR EXTRACT(YEAR FROM start_date) = 2027
    ) THEN
        IF NOT EXISTS (
            SELECT 1 FROM seasons
            WHERE name = '2027' AND start_date = DATE '2027-04-17'
              AND end_date = DATE '2027-09-11' AND NOT is_archived
        ) OR (SELECT COUNT(*) FROM seasons
              WHERE name = '2027' OR EXTRACT(YEAR FROM start_date) = 2027) <> 1 THEN
            RAISE EXCEPTION 'Existing 2027 season differs from the confirmed configuration; review it before proceeding';
        END IF;
    ELSE
        INSERT INTO seasons (name, start_date, end_date)
        VALUES ('2027', DATE '2027-04-17', DATE '2027-09-11');
    END IF;
END $$;
