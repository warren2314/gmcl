CREATE TABLE junior_competitions (
 id BIGSERIAL PRIMARY KEY,
 season_id BIGINT NOT NULL REFERENCES junior_seasons(id) ON DELETE RESTRICT,
 name TEXT NOT NULL CHECK (btrim(name)<>''),
 age_group TEXT NOT NULL CHECK(age_group IN ('U9','U11','U13','U15','U18')),
 division TEXT NOT NULL DEFAULT '' CHECK(length(division)<=100),
 format TEXT NOT NULL CHECK(format IN ('once','homeaway','custom')),
 start_date DATE NOT NULL,
 end_date DATE NOT NULL CHECK(end_date>=start_date),
 default_start_time TEXT NOT NULL DEFAULT '18:00',
 revision BIGINT NOT NULL DEFAULT 1,
 current_publication_id BIGINT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(season_id,age_group,name,division)
);
CREATE TABLE junior_competition_entries (
 competition_id BIGINT NOT NULL REFERENCES junior_competitions(id) ON DELETE RESTRICT,
 team_id BIGINT NOT NULL REFERENCES junior_teams(id) ON DELETE RESTRICT,
 active BOOLEAN NOT NULL DEFAULT true,
 PRIMARY KEY(competition_id,team_id)
);
CREATE TABLE junior_fixture_constraints (
 competition_id BIGINT PRIMARY KEY REFERENCES junior_competitions(id) ON DELETE RESTRICT,
 settings JSONB NOT NULL DEFAULT '{}'::jsonb,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE junior_fixture_generations (
 id BIGSERIAL PRIMARY KEY,
 competition_id BIGINT NOT NULL REFERENCES junior_competitions(id) ON DELETE RESTRICT,
 based_on_revision BIGINT NOT NULL,
 inputs JSONB NOT NULL,
 created_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE junior_fixtures (
 id BIGSERIAL PRIMARY KEY,
 competition_id BIGINT NOT NULL REFERENCES junior_competitions(id) ON DELETE RESTRICT,
 generation_id BIGINT REFERENCES junior_fixture_generations(id),
 home_team_id BIGINT NOT NULL REFERENCES junior_teams(id),
 away_team_id BIGINT NOT NULL REFERENCES junior_teams(id),
 match_date DATE NOT NULL,
 start_time TEXT NOT NULL,
 ground_name TEXT NOT NULL DEFAULT '',
 retired BOOLEAN NOT NULL DEFAULT false,
 CHECK(home_team_id<>away_team_id),
 UNIQUE(id,competition_id)
);
CREATE INDEX junior_fixtures_competition ON junior_fixtures(competition_id,match_date,id) WHERE NOT retired;
CREATE TABLE junior_fixture_publications (
 id BIGSERIAL PRIMARY KEY,
 competition_id BIGINT NOT NULL REFERENCES junior_competitions(id),
 revision BIGINT NOT NULL,
 constraints_snapshot JSONB NOT NULL,
 coverage_acknowledged BOOLEAN NOT NULL,
 acknowledgement_reason TEXT NOT NULL,
 published_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(competition_id,revision),
 UNIQUE(id,competition_id)
);
ALTER TABLE junior_competitions ADD CONSTRAINT junior_current_publication_fk
 FOREIGN KEY(current_publication_id,id) REFERENCES junior_fixture_publications(id,competition_id);
CREATE TABLE junior_publication_fixtures (
 publication_id BIGINT NOT NULL REFERENCES junior_fixture_publications(id),
 fixture_id BIGINT NOT NULL REFERENCES junior_fixtures(id),
 home_team_id BIGINT NOT NULL REFERENCES junior_teams(id),
 away_team_id BIGINT NOT NULL REFERENCES junior_teams(id),
 match_date DATE NOT NULL,
 start_time TEXT NOT NULL,
 ground_name TEXT NOT NULL,
 PRIMARY KEY(publication_id,fixture_id)
);

-- Published schedules and generation inputs are evidence, never an editable cache.
CREATE TRIGGER junior_fixture_generation_immutable BEFORE UPDATE OR DELETE ON junior_fixture_generations
 FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();
CREATE TRIGGER junior_fixture_publication_immutable BEFORE UPDATE OR DELETE ON junior_fixture_publications
 FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();
CREATE TRIGGER junior_publication_fixture_immutable BEFORE UPDATE OR DELETE ON junior_publication_fixtures
 FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();
