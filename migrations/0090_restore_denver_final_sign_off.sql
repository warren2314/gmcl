-- Denver's live login is "denver"; older installations used "denverthornton".
-- Migration 0089 only recognised the older login, leaving the live approval
-- handover without an eligible final issuer. Require the existing active
-- Play-Cricket directory link before granting either login the issuer role.
INSERT INTO admin_user_permissions(admin_user_id,permission)
SELECT admin.id,permission
FROM admin_users admin
CROSS JOIN (VALUES ('sanctions_publish'),('sanctions_ineligible_issue')) permissions(permission)
WHERE LOWER(BTRIM(admin.username)) IN ('denver','denverthornton')
  AND admin.is_active
  AND BTRIM(admin.email)<>''
  AND EXISTS (
      SELECT 1 FROM sanction_recipient_directory recipient
      WHERE recipient.active AND recipient.recipient_role='play_cricket'
        AND LOWER(BTRIM(recipient.email))=LOWER(BTRIM(admin.email))
  )
ON CONFLICT DO NOTHING;
