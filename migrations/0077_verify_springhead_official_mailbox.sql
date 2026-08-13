-- The authoritative GMCL Team Contact Lookup displays this exact official
-- mailbox for Springhead CCC.  Migration 0074 created only an inactive
-- suggestion and incorrectly collapsed CCC to CC.  Preserve any independently
-- administrator-verified contact; otherwise activate this exact sheet value
-- and retain an immutable audit event.

WITH target AS (
    SELECT id,name FROM clubs
    WHERE lower(btrim(name))='springhead ccc'
)
UPDATE sanction_club_contacts contact
SET active=FALSE,updated_at=now()
WHERE contact.club_id IN (SELECT id FROM target)
  AND contact.contact_type='official_mailbox'
  AND contact.active
  AND contact.verified_at IS NULL;

WITH target AS (
    SELECT id,name FROM clubs
    WHERE lower(btrim(name))='springhead ccc'
), inserted AS (
    INSERT INTO sanction_club_contacts(
        club_id,contact_type,name,email,active,verified_at,created_by_admin_id
    )
    SELECT target.id,'official_mailbox',target.name,
           'springheadccc@gtrmcrcricket.co.uk',TRUE,now(),NULL
    FROM target
    WHERE NOT EXISTS (
        SELECT 1 FROM sanction_club_contacts verified
        WHERE verified.club_id=target.id
          AND verified.contact_type='official_mailbox'
          AND verified.active
          AND verified.verified_at IS NOT NULL
    )
    RETURNING *
)
INSERT INTO sanction_configuration_events(
    configuration_type,configuration_key,actor_admin_id,reason,before_data,after_data,request_id
)
SELECT 'club_contact','club:' || inserted.club_id::text,NULL,
       'Verified against the exact CLUB GMCL E-MAIL displayed by GMCL Team Contact Lookup spreadsheet 1an3lSmifqh2FFKCHkjDoyXji_bo0ZLGvhE6YIQZr9ts on 13 August 2026',
       (SELECT to_jsonb(previous) FROM sanction_club_contacts previous
        WHERE previous.club_id=inserted.club_id AND previous.id<>inserted.id
        ORDER BY previous.id DESC LIMIT 1),
       to_jsonb(inserted),
       'migration-0077-sheet-contact-verification'
FROM inserted;

