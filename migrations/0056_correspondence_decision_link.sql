-- Tie locked outcome correspondence to the exact approved decision revision.
-- Response requests and reminders intentionally retain a NULL decision link.

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_decision_id_case
    ON sanction_decision_revisions(id,case_id);

ALTER TABLE sanction_correspondence_revisions
    ADD COLUMN IF NOT EXISTS decision_revision_id BIGINT
        REFERENCES sanction_decision_revisions(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_correspondence_id_case
    ON sanction_correspondence_revisions(id,case_id);

CREATE INDEX IF NOT EXISTS idx_sanction_correspondence_decision
    ON sanction_correspondence_revisions(decision_revision_id, status, audience)
    WHERE decision_revision_id IS NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_sanction_correspondence_decision_case'
          AND conrelid='sanction_correspondence_revisions'::regclass
    ) THEN
        ALTER TABLE sanction_correspondence_revisions
            ADD CONSTRAINT fk_sanction_correspondence_decision_case
            FOREIGN KEY(decision_revision_id,case_id)
            REFERENCES sanction_decision_revisions(id,case_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_sanction_correspondence_supersedes_case'
          AND conrelid='sanction_correspondence_revisions'::regclass
    ) THEN
        ALTER TABLE sanction_correspondence_revisions
            ADD CONSTRAINT fk_sanction_correspondence_supersedes_case
            FOREIGN KEY(supersedes_id,case_id)
            REFERENCES sanction_correspondence_revisions(id,case_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_sanction_outbox_decision_case'
          AND conrelid='sanction_notification_outbox'::regclass
    ) THEN
        ALTER TABLE sanction_notification_outbox
            ADD CONSTRAINT fk_sanction_outbox_decision_case
            FOREIGN KEY(decision_revision_id,case_id)
            REFERENCES sanction_decision_revisions(id,case_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_sanction_outbox_correspondence_case'
          AND conrelid='sanction_notification_outbox'::regclass
    ) THEN
        ALTER TABLE sanction_notification_outbox
            ADD CONSTRAINT fk_sanction_outbox_correspondence_case
            FOREIGN KEY(correspondence_revision_id,case_id)
            REFERENCES sanction_correspondence_revisions(id,case_id) ON DELETE RESTRICT;
    END IF;
END $$;
