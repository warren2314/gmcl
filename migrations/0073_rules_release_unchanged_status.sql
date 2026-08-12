ALTER TABLE rule_releases
    DROP CONSTRAINT IF EXISTS rule_releases_status_check;

ALTER TABLE rule_releases
    ADD CONSTRAINT rule_releases_status_check
    CHECK (status IN ('building','active','archived','failed','unchanged'));
