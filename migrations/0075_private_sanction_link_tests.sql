-- Synthetic link tests exercise the real sanctions response path without
-- entering ordinary case queues or the public sanctions register.
ALTER TABLE sanction_cases
    ADD COLUMN IF NOT EXISTS is_test BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE sanction_cases
    DROP CONSTRAINT IF EXISTS ck_sanction_case_private_test;

ALTER TABLE sanction_cases
    ADD CONSTRAINT ck_sanction_case_private_test CHECK (
        NOT is_test OR (
            source_type='manual'
            AND public_status='unpublished'
            AND published_at IS NULL
            AND status IN ('response_pending','investigating')
        )
    );

CREATE INDEX IF NOT EXISTS idx_sanction_cases_private_test
    ON sanction_cases(created_at DESC) WHERE is_test;
