CREATE TABLE junior_grounds (
 id BIGSERIAL PRIMARY KEY,
 name TEXT NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
 address TEXT NOT NULL CHECK(length(btrim(address)) BETWEEN 1 AND 500),
 postcode TEXT NOT NULL CHECK(length(postcode) BETWEEN 5 AND 10),
 source TEXT NOT NULL CHECK(length(source) BETWEEN 1 AND 500),
 layouts JSONB NOT NULL CHECK(jsonb_typeof(layouts)='array'),
 archived BOOLEAN NOT NULL DEFAULT false,
 revision BIGINT NOT NULL DEFAULT 1,
 updated_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX junior_ground_location ON junior_grounds(lower(regexp_replace(btrim(address),'\s+',' ','g')),postcode);
ALTER TABLE junior_fixtures ADD COLUMN ground_id BIGINT REFERENCES junior_grounds(id);
CREATE INDEX junior_fixture_ground ON junior_fixtures(ground_id) WHERE ground_id IS NOT NULL;
ALTER TABLE junior_publication_fixtures ADD COLUMN ground_id BIGINT REFERENCES junior_grounds(id);
ALTER TABLE junior_publication_fixtures ADD COLUMN ground_postcode TEXT NOT NULL DEFAULT '';
ALTER TABLE junior_publication_fixtures ADD COLUMN ground_address TEXT NOT NULL DEFAULT '';
CREATE TABLE junior_publication_ground_links (
 id BIGSERIAL PRIMARY KEY,
 publication_id BIGINT NOT NULL,
 fixture_id BIGINT NOT NULL,
 ground_id BIGINT NOT NULL REFERENCES junior_grounds(id),
 linked_by_admin_id INTEGER NOT NULL REFERENCES admin_users(id),
 linked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(publication_id,fixture_id) REFERENCES junior_publication_fixtures(publication_id,fixture_id)
);
CREATE TRIGGER junior_publication_ground_link_immutable BEFORE UPDATE OR DELETE ON junior_publication_ground_links
 FOR EACH ROW EXECUTE FUNCTION junior_events_append_only();
CREATE INDEX junior_publication_ground_link_lookup ON junior_publication_ground_links(publication_id,fixture_id,id DESC);
