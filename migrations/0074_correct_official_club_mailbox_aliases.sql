-- Correct the system-seeded GMCL mailbox convention to
-- {normalised club name}cc@gtrmcrcricket.co.uk.
-- Verified administrator-managed contacts are deliberately untouched.
WITH aliases AS (
    SELECT c.id AS club_id,
           regexp_replace(
             replace(
               regexp_replace(
                 regexp_replace(
                   regexp_replace(lower(c.name),'\s+cricket\s+club\s*$','','i'),
                   '\s+(c&sc|ccc|cc)\s*$','','i'),
                 '\s+lancs\s*$','','i'),
               '&',' and '),
             '[^a-z0-9]','','g') AS mailbox_base
    FROM clubs c
), corrected AS (
    SELECT club_id,
           mailbox_base || '@gtrmcrcricket.co.uk' AS legacy_email,
           mailbox_base || 'cc@gtrmcrcricket.co.uk' AS corrected_email
    FROM aliases
    WHERE NULLIF(mailbox_base,'') IS NOT NULL
)
UPDATE sanction_club_contacts contact
SET email=corrected.corrected_email,
    updated_at=now()
FROM corrected
WHERE contact.club_id=corrected.club_id
  AND contact.contact_type='official_mailbox'
  AND contact.active=FALSE
  AND contact.verified_at IS NULL
  AND contact.created_by_admin_id IS NULL
  AND LOWER(BTRIM(contact.email))=LOWER(corrected.legacy_email)
  AND NOT EXISTS (
      SELECT 1
      FROM sanction_club_contacts existing
      WHERE existing.club_id=contact.club_id
        AND existing.contact_type='official_mailbox'
        AND LOWER(BTRIM(existing.email))=LOWER(corrected.corrected_email)
  );

-- Add the corrected inactive suggestion where an unresolved club has none.
WITH aliases AS (
    SELECT c.id AS club_id,c.name,
           regexp_replace(
             replace(
               regexp_replace(
                 regexp_replace(
                   regexp_replace(lower(c.name),'\s+cricket\s+club\s*$','','i'),
                   '\s+(c&sc|ccc|cc)\s*$','','i'),
                 '\s+lancs\s*$','','i'),
               '&',' and '),
             '[^a-z0-9]','','g') || 'cc@gtrmcrcricket.co.uk' AS email
    FROM clubs c
    WHERE NULLIF(regexp_replace(lower(c.name),'[^a-z0-9]','','g'),'') IS NOT NULL
)
INSERT INTO sanction_club_contacts(club_id,contact_type,name,email,active)
SELECT alias.club_id,'official_mailbox',alias.name,alias.email,FALSE
FROM aliases alias
WHERE NOT EXISTS (
    SELECT 1
    FROM sanction_club_contacts existing
    WHERE existing.club_id=alias.club_id
      AND existing.contact_type='official_mailbox'
      AND LOWER(BTRIM(existing.email))=LOWER(BTRIM(alias.email))
)
AND NOT EXISTS (
    SELECT 1
    FROM sanction_club_contacts verified
    WHERE verified.club_id=alias.club_id
      AND verified.contact_type='official_mailbox'
      AND verified.active
      AND verified.verified_at IS NOT NULL
)
ON CONFLICT DO NOTHING;
