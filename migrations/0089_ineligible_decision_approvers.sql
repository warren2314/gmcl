-- Investigation access and general sanctions approval do not nominate an
-- ineligible-player decision reviewer. Keep that responsibility explicit.
INSERT INTO sanction_permission_catalog(permission,description)
VALUES ('sanctions_ineligible_approve','Review and approve ineligible-player decisions (Dave / Warren stage)')
ON CONFLICT DO NOTHING;

INSERT INTO sanction_permission_catalog(permission,description)
VALUES ('sanctions_ineligible_issue','Give final sign-off and issue ineligible-player outcomes (Denver stage)')
ON CONFLICT DO NOTHING;

INSERT INTO admin_user_permissions(admin_user_id,permission)
SELECT admin.id,permission
FROM admin_users admin
CROSS JOIN (VALUES ('sanctions_publish'),('sanctions_ineligible_issue')) permissions(permission)
WHERE LOWER(BTRIM(admin.username))='denverthornton'
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION sanction_ineligible_final_issuer(admin_id INTEGER)
RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM admin_users admin
        WHERE admin.id=admin_id AND admin.is_active
          AND EXISTS(SELECT 1 FROM admin_user_permissions permission
                     WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_ineligible_issue')
          AND (COALESCE(admin.role,'admin')='super_admin' OR EXISTS(
              SELECT 1 FROM admin_user_permissions permission
              WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_publish'
          ))
          AND EXISTS (
              SELECT 1 FROM sanction_recipient_directory recipient
              WHERE recipient.active AND recipient.recipient_role='play_cricket'
                AND LOWER(BTRIM(recipient.email))=LOWER(BTRIM(admin.email))
          )
    );
$$;

INSERT INTO admin_user_permissions(admin_user_id,permission)
SELECT admin.id,permission
FROM admin_users admin
CROSS JOIN (VALUES ('sanctions_approve'),('sanctions_ineligible_approve')) permissions(permission)
WHERE LOWER(BTRIM(admin.email))='rogers@gtrmcrcricket.co.uk'
   OR LOWER(BTRIM(admin.username))='warren2314'
ON CONFLICT DO NOTHING;

-- Shared by notification routing, the personal queue and decision actions.
-- Denver's final issue/sign-off remains a separate responsibility.
CREATE OR REPLACE FUNCTION sanction_ineligible_decision_approver(admin_id INTEGER)
RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM admin_users admin
        WHERE admin.id=admin_id AND admin.is_active
          AND (COALESCE(admin.role,'admin')='super_admin' OR EXISTS(
              SELECT 1 FROM admin_user_permissions permission
              WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_approve'
          ))
          AND EXISTS(SELECT 1 FROM admin_user_permissions permission
                     WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_ineligible_approve')
          AND NOT EXISTS (
              SELECT 1 FROM sanction_recipient_directory recipient
              WHERE recipient.active AND recipient.recipient_role='play_cricket'
                AND LOWER(BTRIM(recipient.email))=LOWER(BTRIM(admin.email))
          )
    );
$$;

-- Automated cleanup must identify itself as system work, not impersonate an
-- administrator. Only the two workflow request kinds allow system revocation.
ALTER TABLE sanction_notification_outbox
    ADD COLUMN IF NOT EXISTS revoked_by_system BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE sanction_notification_outbox
    DROP CONSTRAINT IF EXISTS ck_sanction_outbox_revocation_complete;
ALTER TABLE sanction_notification_outbox
    ADD CONSTRAINT ck_sanction_outbox_revocation_complete CHECK (
        (revoked_at IS NULL AND revoked_by_admin_id IS NULL AND revocation_reason IS NULL AND NOT revoked_by_system)
        OR
        (revoked_at IS NOT NULL AND processed_at IS NOT NULL
         AND NULLIF(BTRIM(revocation_reason),'') IS NOT NULL
         AND ((revoked_by_admin_id IS NOT NULL AND NOT revoked_by_system)
              OR (revoked_by_admin_id IS NULL AND revoked_by_system
                  AND message_kind IN ('decision_approval_request','final_sign_off_request'))))
    );

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
       NEW.revoked_by_system IS DISTINCT FROM OLD.revoked_by_system OR
       NEW.revocation_reason IS DISTINCT FROM OLD.revocation_reason THEN
      RAISE EXCEPTION 'outbox revocation metadata is invalid';
    END IF;
  ELSIF NULLIF(BTRIM(NEW.revocation_reason),'') IS NULL OR NOT (
      (NEW.revoked_by_admin_id IS NOT NULL AND NOT NEW.revoked_by_system)
      OR (NEW.revoked_by_admin_id IS NULL AND NEW.revoked_by_system
          AND NEW.message_kind IN ('decision_approval_request','final_sign_off_request'))
  ) THEN
    RAISE EXCEPTION 'outbox revocation requires an administrator or workflow system actor and reason';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Cancel undelivered alerts to people who are not designated reviewers.
-- Preserve sent messages and immutable message content for the audit trail.
UPDATE sanction_notification_outbox outbox
SET processed_at=now(),revoked_at=now(),revoked_by_system=TRUE,
    revocation_reason='Recipient is not a designated ineligible-player decision approver'
FROM sanction_cases cases
WHERE outbox.case_id=cases.id AND cases.source_type='ineligible_player'
  AND outbox.message_kind='decision_approval_request'
  AND outbox.processed_at IS NULL AND outbox.revoked_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM admin_users admin
      WHERE LOWER(BTRIM(admin.email))=LOWER(BTRIM(outbox.recipient))
        AND sanction_ineligible_decision_approver(admin.id)
  );
