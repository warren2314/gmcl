-- Explicit, audited revocation for outcome items cancelled before delivery.
-- The immutable content remains readable; revocation is a one-way terminal
-- transition and cannot be applied to an already processed item.

ALTER TABLE sanction_notification_outbox
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoked_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS revocation_reason TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='ck_sanction_outbox_revocation_complete'
          AND conrelid='sanction_notification_outbox'::regclass
    ) THEN
        ALTER TABLE sanction_notification_outbox
            ADD CONSTRAINT ck_sanction_outbox_revocation_complete CHECK (
                (revoked_at IS NULL AND revoked_by_admin_id IS NULL AND revocation_reason IS NULL)
                OR
                (revoked_at IS NOT NULL AND processed_at IS NOT NULL
                 AND revoked_by_admin_id IS NOT NULL
                 AND NULLIF(BTRIM(revocation_reason),'') IS NOT NULL)
            );
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION protect_sanction_outbox() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'sanction_notification_outbox is append-only'; END IF;
  IF NEW.case_id IS DISTINCT FROM OLD.case_id OR
     NEW.decision_revision_id IS DISTINCT FROM OLD.decision_revision_id OR
     NEW.policy_version_id IS DISTINCT FROM OLD.policy_version_id OR
     NEW.correspondence_revision_id IS DISTINCT FROM OLD.correspondence_revision_id OR
     NEW.message_kind IS DISTINCT FROM OLD.message_kind OR
     NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR
     NEW.recipient IS DISTINCT FROM OLD.recipient OR NEW.subject IS DISTINCT FROM OLD.subject OR
     NEW.body IS DISTINCT FROM OLD.body OR NEW.attachment_manifest IS DISTINCT FROM OLD.attachment_manifest OR
     NEW.available_at IS DISTINCT FROM OLD.available_at OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'outbox message content is immutable';
  END IF;
  IF OLD.processed_at IS NOT NULL OR OLD.revoked_at IS NOT NULL OR NEW.processed_at IS NULL THEN
    RAISE EXCEPTION 'outbox message is already terminal or has an invalid transition';
  END IF;
  IF NEW.revoked_at IS NULL THEN
    IF NEW.revoked_by_admin_id IS DISTINCT FROM OLD.revoked_by_admin_id OR
       NEW.revocation_reason IS DISTINCT FROM OLD.revocation_reason THEN
      RAISE EXCEPTION 'outbox revocation metadata is invalid';
    END IF;
  ELSIF NEW.revoked_by_admin_id IS NULL OR NULLIF(BTRIM(NEW.revocation_reason),'') IS NULL THEN
    RAISE EXCEPTION 'outbox revocation requires an administrator and reason';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sanction_outbox_protect ON sanction_notification_outbox;
CREATE TRIGGER trg_sanction_outbox_protect
BEFORE UPDATE OR DELETE ON sanction_notification_outbox
FOR EACH ROW EXECUTE FUNCTION protect_sanction_outbox();
