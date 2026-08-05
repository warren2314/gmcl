-- Lock approved outcome wording/PDFs and allow the legacy sanctions read-model
-- to project every card in an atomic multi-effect decision bundle.

ALTER TABLE sanction_decision_revisions
    ADD COLUMN IF NOT EXISTS outcome_subject TEXT,
    ADD COLUMN IF NOT EXISTS outcome_findings TEXT,
    ADD COLUMN IF NOT EXISTS appeal_instructions TEXT;

ALTER TABLE sanction_correspondence_revisions
    ADD COLUMN IF NOT EXISTS pdf_bytes BYTEA;

ALTER TABLE sanctions
    ADD COLUMN IF NOT EXISTS effect_key UUID;

DROP INDEX IF EXISTS uq_sanctions_case_id;
CREATE UNIQUE INDEX IF NOT EXISTS uq_sanctions_effect_key
    ON sanctions(effect_key) WHERE effect_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sanctions_case_id
    ON sanctions(case_id) WHERE case_id IS NOT NULL;

-- Seed the long-standing GMCL club alias convention. These remain
-- administrator-manageable directory rows; sending is blocked if a case has
-- no active official mailbox after resolution.
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
             '[^a-z0-9]','','g') || '@gtrmcrcricket.co.uk' AS email
    FROM clubs c
    WHERE NULLIF(regexp_replace(lower(c.name),'[^a-z0-9]','','g'),'') IS NOT NULL
)
INSERT INTO sanction_club_contacts(club_id,contact_type,name,email,active)
SELECT alias.club_id,'official_mailbox',alias.name,alias.email,FALSE
FROM aliases alias
WHERE NOT EXISTS (
    SELECT 1 FROM sanction_club_contacts existing
    WHERE existing.club_id=alias.club_id
      AND existing.contact_type='official_mailbox'
      AND LOWER(BTRIM(existing.email))=LOWER(BTRIM(alias.email))
)
ON CONFLICT DO NOTHING;

INSERT INTO sanction_automation_settings(source_type,mode,enabled)
VALUES ('ineligible_player','manual',TRUE)
ON CONFLICT DO NOTHING;
