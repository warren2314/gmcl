BEGIN;

ALTER TABLE portal_competitions
    DROP CONSTRAINT IF EXISTS portal_competitions_season_id_external_source_external_id_key;

ALTER TABLE portal_competitions
    ADD COLUMN IF NOT EXISTS ended_by_admin_user_id INTEGER
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS end_reason TEXT;

ALTER TABLE portal_competitions
    DROP CONSTRAINT IF EXISTS portal_competitions_external_reference_pair_check;

ALTER TABLE portal_competitions
    ADD CONSTRAINT portal_competitions_external_reference_pair_check
    CHECK (
        (NULLIF(btrim(external_source), '') IS NULL) =
        (NULLIF(btrim(external_id), '') IS NULL)
    ) NOT VALID;

CREATE UNIQUE INDEX IF NOT EXISTS ux_portal_competitions_external_reference
    ON portal_competitions (
        COALESCE(season_id, 0),
        lower(btrim(external_source)),
        lower(btrim(external_id))
    )
    WHERE NULLIF(btrim(external_source), '') IS NOT NULL
      AND NULLIF(btrim(external_id), '') IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS ux_portal_competitions_manual_name
    ON portal_competitions (
        COALESCE(season_id, 0),
        lower(btrim(name))
    )
    WHERE NULLIF(btrim(external_source), '') IS NULL
      AND NULLIF(btrim(external_id), '') IS NULL;

CREATE TABLE IF NOT EXISTS portal_competition_clubs (
    competition_id UUID NOT NULL
        REFERENCES portal_competitions(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    created_by_admin_user_id INTEGER NOT NULL
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (competition_id, club_id)
);

CREATE INDEX IF NOT EXISTS idx_portal_competition_clubs_club
    ON portal_competition_clubs(club_id, competition_id);

ALTER TABLE portal_competition_clubs ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_competition_clubs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS portal_competition_clubs_system_only
    ON portal_competition_clubs;

CREATE POLICY portal_competition_clubs_system_only
    ON portal_competition_clubs
    USING (portal_is_system())
    WITH CHECK (portal_is_system());

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON portal_competition_clubs
            TO gmcl_portal_runtime;
    END IF;
END
$$;

COMMIT;
