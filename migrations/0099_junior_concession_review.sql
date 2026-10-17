CREATE TABLE junior_result_imports (
 id BIGSERIAL PRIMARY KEY,
 competition_id BIGINT NOT NULL REFERENCES junior_competitions(id),
 source_name TEXT NOT NULL CHECK(btrim(source_name)<>''),
 content_sha256 TEXT NOT NULL,
 imported_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 imported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(competition_id,source_name,content_sha256)
);
CREATE TABLE junior_result_observations (
 id BIGSERIAL PRIMARY KEY,
 import_id BIGINT NOT NULL REFERENCES junior_result_imports(id),
 fixture_id BIGINT NOT NULL REFERENCES junior_fixtures(id),
 publication_id BIGINT NOT NULL REFERENCES junior_fixture_publications(id),
 outcome TEXT NOT NULL CHECK(outcome IN ('played','conceded_home','conceded_away','possible_concession','abandoned','postponed','bye','unknown')),
 evidence TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 supersedes_id BIGINT REFERENCES junior_result_observations(id),
 observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(import_id,fixture_id)
);
CREATE TABLE junior_concession_candidates (
 id BIGSERIAL PRIMARY KEY,
 fixture_id BIGINT NOT NULL UNIQUE REFERENCES junior_fixtures(id),
 current_observation_id BIGINT NOT NULL REFERENCES junior_result_observations(id),
 state TEXT NOT NULL CHECK(state IN ('awaiting_review','information_requested','confirmed','dismissed','source_changed')),
 revision BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE junior_concession_decisions (
 id BIGSERIAL PRIMARY KEY,
 candidate_id BIGINT NOT NULL REFERENCES junior_concession_candidates(id),
 observation_id BIGINT NOT NULL REFERENCES junior_result_observations(id),
 action TEXT NOT NULL CHECK(action IN ('confirm','dismiss','request_information')),
 conceding_team_id BIGINT REFERENCES junior_teams(id),
 reason TEXT NOT NULL CHECK(btrim(reason)<>''),
 decided_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 decided_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK((action='confirm' AND conceding_team_id IS NOT NULL) OR (action<>'confirm' AND conceding_team_id IS NULL))
);

CREATE TRIGGER junior_result_import_immutable BEFORE UPDATE OR DELETE ON junior_result_imports
 FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();
CREATE TRIGGER junior_result_observation_immutable BEFORE UPDATE OR DELETE ON junior_result_observations
 FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();
CREATE TRIGGER junior_concession_decision_immutable BEFORE UPDATE OR DELETE ON junior_concession_decisions
 FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();
CREATE INDEX junior_concession_decisions_history ON junior_concession_decisions(candidate_id,id DESC);
