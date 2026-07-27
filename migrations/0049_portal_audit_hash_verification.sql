-- Version portal audit hashes so new events can be independently recomputed
-- from PostgreSQL's microsecond-precision timestamps. Existing version-1
-- events remain link-verifiable but are reported as legacy by the preflight.

ALTER TABLE portal_audit_events
    ADD COLUMN IF NOT EXISTS hash_version SMALLINT NOT NULL DEFAULT 1;

ALTER TABLE portal_audit_events
    DROP CONSTRAINT IF EXISTS portal_audit_events_hash_version_check;

ALTER TABLE portal_audit_events
    ADD CONSTRAINT portal_audit_events_hash_version_check
    CHECK (hash_version IN (1, 2));

ALTER TABLE portal_audit_events
    ALTER COLUMN hash_version SET DEFAULT 2;

ALTER TABLE portal_audit_events
    ALTER COLUMN chain_position SET NOT NULL,
    ALTER COLUMN event_hash SET NOT NULL,
    ALTER COLUMN hash_version SET NOT NULL;

ALTER TABLE portal_audit_events
    DROP CONSTRAINT IF EXISTS portal_audit_chain_position_check,
    DROP CONSTRAINT IF EXISTS portal_audit_event_hash_length_check,
    DROP CONSTRAINT IF EXISTS portal_audit_previous_hash_shape_check;

ALTER TABLE portal_audit_events
    ADD CONSTRAINT portal_audit_chain_position_check
        CHECK (chain_position > 0),
    ADD CONSTRAINT portal_audit_event_hash_length_check
        CHECK (octet_length(event_hash) = 32),
    ADD CONSTRAINT portal_audit_previous_hash_shape_check
        CHECK (
            (chain_position = 1 AND previous_hash IS NULL)
            OR (
                chain_position > 1
                AND previous_hash IS NOT NULL
                AND octet_length(previous_hash) = 32
            )
        );

COMMENT ON COLUMN portal_audit_events.hash_version IS
    'Version 1 is legacy link-only verification; version 2 is fully recomputable.';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime') THEN
        GRANT SELECT ON schema_migrations TO gmcl_portal_runtime;
    END IF;
END;
$$;
