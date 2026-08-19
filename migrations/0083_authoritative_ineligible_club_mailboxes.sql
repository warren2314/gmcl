-- Authoritative official club mailboxes supplied for the ineligible-player
-- workflow on 19 August 2026. A unique deterministic club match is activated
-- automatically. Unmatched or ambiguous addresses remain blocked and are
-- surfaced on the recipient administration page for human review.

CREATE TABLE IF NOT EXISTS sanction_club_mailbox_authority (
    id BIGSERIAL PRIMARY KEY,
    email TEXT NOT NULL,
    source_label TEXT NOT NULL DEFAULT 'Authoritative ineligible-player mailbox list supplied 19 August 2026',
    club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    match_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (match_status IN ('pending','matched','unmatched','ambiguous')),
    candidate_club_ids INTEGER[] NOT NULL DEFAULT '{}'::integer[],
    imported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    matched_at TIMESTAMPTZ,
    reviewed_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    review_note TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_club_mailbox_authority_email
    ON sanction_club_mailbox_authority(LOWER(BTRIM(email)));

CREATE OR REPLACE FUNCTION sanction_club_mailbox_match_key(value TEXT)
RETURNS TEXT AS $$
DECLARE cleaned TEXT;
BEGIN
    cleaned := LOWER(BTRIM(SPLIT_PART(COALESCE(value,''),'@',1)));
    cleaned := REPLACE(cleaned,'&','and');
    cleaned := REGEXP_REPLACE(cleaned,'[^a-z0-9]','','g');
    cleaned := REGEXP_REPLACE(cleaned,
        '(cricketandsocialclub|cricketclub|cclancs|ccclancs|candsc|sportingclub|ccc|cc|club|lancs)$',
        '','i');
    RETURN cleaned;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

INSERT INTO sanction_club_mailbox_authority(email)
VALUES
('brooksbottom@gtrmcrcricket.co.uk'),
('clifton@gtrmcrcricket.co.uk'),
('dentonwestcc@gtrmcrcricket.co.uk'),
('edgworth@gtrmcrcricket.co.uk'),
('egerton@gtrmcrcricket.co.uk'),
('horwichrmi@gtrmcrcricket.co.uk'),
('montoncc@gtrmcrcricket.co.uk'),
('prestwichcc@gtrmcrcricket.co.uk'),
('roegreencc@gtrmcrcricket.co.uk'),
('roytoncc@gtrmcrcricket.co.uk'),
('shawcc@gtrmcrcricket.co.uk'),
('woodbank@gtrmcrcricket.co.uk'),
('a&t@gtrmcrcricket.co.uk'),
('bradshaw@gtrmcrcricket.co.uk'),
('dsl@gtrmcrcricket.co.uk'),
('greenfield@gtrmcrcricket.co.uk'),
('heaton@gtrmcrcricket.co.uk'),
('heyside@gtrmcrcricket.co.uk'),
('heywood@gtrmcrcricket.co.uk'),
('mottramcc@gtrmcrcricket.co.uk'),
('standcc@gtrmcrcricket.co.uk'),
('walshaw@gtrmcrcricket.co.uk'),
('westhoughtoncc@gtrmcrcricket.co.uk'),
('woodhousescc@gtrmcrcricket.co.uk'),
('droylsden@gtrmcrcricket.co.uk'),
('dukinfield@gtrmcrcricket.co.uk'),
('edenfield@gtrmcrcricket.co.uk'),
('elton@gtrmcrcricket.co.uk'),
('flixton@gtrmcrcricket.co.uk'),
('floweryfield@gtrmcrcricket.co.uk'),
('glodwickcc@gtrmcrcricket.co.uk'),
('glossop@gtrmcrcricket.co.uk'),
('milnrow@gtrmcrcricket.co.uk'),
('saddleworthcc@gtrmcrcricket.co.uk'),
('stayleycc@gtrmcrcricket.co.uk'),
('swintonmoorsidecc@gtrmcrcricket.co.uk'),
('austerlands@gtrmcrcricket.co.uk'),
('cheethamhill@gtrmcrcricket.co.uk'),
('friarmere@gtrmcrcricket.co.uk'),
('golborne@gtrmcrcricket.co.uk'),
('hadfieldstandrews@gtrmcrcricket.co.uk'),
('stretfordcc@gtrmcrcricket.co.uk'),
('thornhamcc@gtrmcrcricket.co.uk'),
('uppermillcc@gtrmcrcricket.co.uk'),
('westleighcc@gtrmcrcricket.co.uk'),
('whalleyrangecc@gtrmcrcricket.co.uk'),
('wintoncc@gtrmcrcricket.co.uk'),
('worsley@gtrmcrcricket.co.uk'),
('adlington@gtrmcrcricket.co.uk'),
('ashton@gtrmcrcricket.co.uk'),
('bd&d@gtrmcrcricket.co.uk'),
('bisc@gtrmcrcricket.co.uk'),
('bury@gtrmcrcricket.co.uk'),
('darcylevercc@gtrmcrcricket.co.uk'),
('southwestmanchestercc@gtrmcrcricket.co.uk'),
('tsjcc@gtrmcrcricket.co.uk'),
('werneth@gtrmcrcricket.co.uk'),
('woodley@gtrmcrcricket.co.uk'),
('daisyhill@gtrmcrcricket.co.uk'),
('oldhamcc@gtrmcrcricket.co.uk'),
('wythenshawe@gtrmcrcricket.co.uk'),
('ashtonladysmith@gtrmcrcricket.co.uk'),
('dentoncc@gtrmcrcricket.co.uk'),
('friendssporting@gtrmcrcricket.co.uk'),
('friendsunited@gtrmcrcricket.co.uk'),
('radcliffecc@gtrmcrcricket.co.uk'),
('springheadccc@gtrmcrcricket.co.uk'),
('stalybridgecc@gtrmcrcricket.co.uk'),
('unsworthcc@gtrmcrcricket.co.uk'),
('micklehurst@gtrmcrcricket.co.uk'),
('moorsidecc@gtrmcrcricket.co.uk'),
('elpm@gtrmcrcricket.co.uk'),
('hindleystpeters@gtrmcrcricket.co.uk'),
('blackley@gtrmcrcricket.co.uk'),
('failsworthmacedonia@gtrmcrcricket.co.uk');

WITH raw_candidates AS (
    SELECT authority.id AS authority_id,contact.club_id,1 AS priority
    FROM sanction_club_mailbox_authority authority
    JOIN sanction_club_contacts contact
      ON LOWER(BTRIM(contact.email))=LOWER(BTRIM(authority.email))
    WHERE contact.contact_type='official_mailbox'
    UNION ALL
    SELECT authority.id,club.id,2
    FROM sanction_club_mailbox_authority authority
    JOIN clubs club
      ON sanction_club_mailbox_match_key(club.name)=sanction_club_mailbox_match_key(authority.email)
    WHERE NULLIF(sanction_club_mailbox_match_key(authority.email),'') IS NOT NULL
), best_priority AS (
    SELECT authority_id,MIN(priority) AS priority
    FROM raw_candidates GROUP BY authority_id
), candidates AS (
    SELECT DISTINCT raw.authority_id,raw.club_id
    FROM raw_candidates raw
    JOIN best_priority best ON best.authority_id=raw.authority_id AND best.priority=raw.priority
), aggregate_candidates AS (
    SELECT authority.id,
           COALESCE(ARRAY_AGG(DISTINCT candidate.club_id ORDER BY candidate.club_id)
               FILTER (WHERE candidate.club_id IS NOT NULL),'{}'::integer[]) AS club_ids,
           COUNT(DISTINCT candidate.club_id) AS club_count
    FROM sanction_club_mailbox_authority authority
    LEFT JOIN candidates candidate ON candidate.authority_id=authority.id
    GROUP BY authority.id
)
UPDATE sanction_club_mailbox_authority authority
SET candidate_club_ids=aggregate.club_ids,
    club_id=CASE WHEN aggregate.club_count=1 THEN aggregate.club_ids[1] END,
    match_status=CASE aggregate.club_count WHEN 0 THEN 'unmatched' WHEN 1 THEN 'matched' ELSE 'ambiguous' END,
    matched_at=CASE WHEN aggregate.club_count=1 THEN now() END
FROM aggregate_candidates aggregate
WHERE aggregate.id=authority.id;

WITH duplicate_clubs AS (
    SELECT club_id FROM sanction_club_mailbox_authority
    WHERE match_status='matched' GROUP BY club_id HAVING COUNT(*)>1
)
UPDATE sanction_club_mailbox_authority authority
SET match_status='ambiguous',club_id=NULL,matched_at=NULL
WHERE authority.club_id IN (SELECT club_id FROM duplicate_clubs);

UPDATE sanction_club_contacts contact
SET active=FALSE,updated_at=now()
WHERE contact.contact_type='official_mailbox' AND contact.active
  AND contact.club_id IN (
      SELECT club_id FROM sanction_club_mailbox_authority WHERE match_status='matched'
  );

WITH selected_contacts AS (
    SELECT authority.id AS authority_id,MAX(contact.id) AS contact_id
    FROM sanction_club_mailbox_authority authority
    JOIN sanction_club_contacts contact
      ON contact.club_id=authority.club_id
     AND contact.contact_type='official_mailbox'
     AND LOWER(BTRIM(contact.email))=LOWER(BTRIM(authority.email))
    WHERE authority.match_status='matched'
    GROUP BY authority.id
)
UPDATE sanction_club_contacts contact
SET name=club.name,email=LOWER(BTRIM(authority.email)),active=TRUE,
    verified_at=now(),created_by_admin_id=NULL,updated_at=now()
FROM selected_contacts selected
JOIN sanction_club_mailbox_authority authority ON authority.id=selected.authority_id
JOIN clubs club ON club.id=authority.club_id
WHERE contact.id=selected.contact_id;

INSERT INTO sanction_club_contacts(
    club_id,contact_type,name,email,active,verified_at,created_by_admin_id
)
SELECT authority.club_id,'official_mailbox',club.name,LOWER(BTRIM(authority.email)),TRUE,now(),NULL
FROM sanction_club_mailbox_authority authority
JOIN clubs club ON club.id=authority.club_id
WHERE authority.match_status='matched'
  AND NOT EXISTS (
      SELECT 1 FROM sanction_club_contacts contact
      WHERE contact.club_id=authority.club_id
        AND contact.contact_type='official_mailbox' AND contact.active
        AND LOWER(BTRIM(contact.email))=LOWER(BTRIM(authority.email))
  );

INSERT INTO sanction_configuration_events(
    configuration_type,configuration_key,actor_admin_id,reason,after_data,request_id
)
SELECT 'club_contact','club:' || authority.club_id::text,NULL,
       authority.source_label,
       jsonb_build_object('club_id',authority.club_id,'email',authority.email,
                          'active',TRUE,'verified',TRUE,'source','authoritative_ineligible_player_list'),
       'migration-0083-authoritative-mailbox-' || authority.id::text
FROM sanction_club_mailbox_authority authority
WHERE authority.match_status='matched';
