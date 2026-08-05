-- The offending-club portal must render the exact allegation that passed the
-- privacy gate when its seven-day response request was created. It must not
-- read later mutable case wording.

ALTER TABLE sanction_response_requests
    ADD COLUMN IF NOT EXISTS allegation_snapshot TEXT;

-- A request created before this column existed has no provable immutable
-- allegation snapshot. Fail closed: revoke the token, cancel queued notices,
-- reopen the investigation, and require staff to review and reissue it.
WITH legacy AS (
    SELECT id,case_id,access_token_id
    FROM sanction_response_requests
    WHERE allegation_snapshot IS NULL
), revoked AS (
    UPDATE sanction_case_access_tokens token
    SET revoked_at=COALESCE(token.revoked_at,now())
    FROM legacy WHERE token.id=legacy.access_token_id
    RETURNING token.id
), cancelled AS (
    UPDATE sanction_response_requests request
    SET status='cancelled',closed_at=COALESCE(request.closed_at,now()),
        allegation_snapshot=''
    FROM legacy WHERE request.id=legacy.id
    RETURNING request.case_id
), resumed AS (
    UPDATE sanction_cases cases
    SET status='investigating',updated_at=now()
    FROM (SELECT DISTINCT case_id FROM cancelled) affected
    WHERE cases.id=affected.case_id AND cases.status='response_pending'
    RETURNING cases.id
), cancelled_outbox AS (
    UPDATE sanction_notification_outbox outbox
    SET processed_at=now()
    FROM (SELECT DISTINCT case_id FROM cancelled) affected
    WHERE outbox.case_id=affected.case_id
      AND outbox.message_kind IN ('response_request','response_reminder')
      AND outbox.processed_at IS NULL
    RETURNING outbox.id
)
INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason)
SELECT DISTINCT case_id,'legacy_response_request_cancelled','system',
       'Response allegation snapshot migration',
       'Legacy response request was cancelled because its allegation wording was not an immutable privacy-reviewed snapshot; review and reissue it'
FROM cancelled;

ALTER TABLE sanction_response_requests
    ALTER COLUMN allegation_snapshot SET NOT NULL;

-- Repair any legacy cross-case references before adding relational guards.
UPDATE sanction_response_requests request
SET case_id=token.case_id
FROM sanction_case_access_tokens token
WHERE token.id=request.access_token_id AND request.case_id<>token.case_id;

UPDATE sanction_case_access_tokens token
SET party_id=NULL
WHERE party_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sanction_case_parties party
    WHERE party.id=token.party_id AND party.case_id=token.case_id
);

UPDATE sanction_response_requests request
SET party_id=NULL
WHERE party_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sanction_case_parties party
    WHERE party.id=request.party_id AND party.case_id=request.case_id
);

UPDATE sanction_response_requests request
SET correspondence_revision_id=NULL
WHERE correspondence_revision_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sanction_correspondence_revisions correspondence
    WHERE correspondence.id=request.correspondence_revision_id
      AND correspondence.case_id=request.case_id
);

-- Only one live request may own a bearer token. Preserve older corrupt rows as
-- cancelled audit history rather than deleting them.
WITH ranked AS (
    SELECT id,ROW_NUMBER() OVER (PARTITION BY access_token_id ORDER BY id DESC) AS position
    FROM sanction_response_requests WHERE status='pending'
)
UPDATE sanction_response_requests request
SET status='cancelled',closed_at=COALESCE(request.closed_at,now())
FROM ranked WHERE request.id=ranked.id AND ranked.position>1;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_access_token_id_case
    ON sanction_case_access_tokens(id,case_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_party_id_case
    ON sanction_case_parties(id,case_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_correspondence_id_case
    ON sanction_correspondence_revisions(id,case_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_response_live_token
    ON sanction_response_requests(access_token_id) WHERE status='pending';

DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='fk_sanction_access_token_party_case') THEN
		ALTER TABLE sanction_case_access_tokens ADD CONSTRAINT fk_sanction_access_token_party_case
			FOREIGN KEY(party_id,case_id)
			REFERENCES sanction_case_parties(id,case_id) ON DELETE RESTRICT;
	END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='fk_sanction_response_token_case') THEN
        ALTER TABLE sanction_response_requests ADD CONSTRAINT fk_sanction_response_token_case
            FOREIGN KEY(access_token_id,case_id)
            REFERENCES sanction_case_access_tokens(id,case_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='fk_sanction_response_party_case') THEN
        ALTER TABLE sanction_response_requests ADD CONSTRAINT fk_sanction_response_party_case
            FOREIGN KEY(party_id,case_id)
            REFERENCES sanction_case_parties(id,case_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='fk_sanction_response_correspondence_case') THEN
        ALTER TABLE sanction_response_requests ADD CONSTRAINT fk_sanction_response_correspondence_case
            FOREIGN KEY(correspondence_revision_id,case_id)
            REFERENCES sanction_correspondence_revisions(id,case_id) ON DELETE RESTRICT;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION protect_sanction_response_request() RETURNS trigger AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'sanction response requests cannot be deleted';
    END IF;
    IF NEW.case_id IS DISTINCT FROM OLD.case_id OR
       NEW.party_id IS DISTINCT FROM OLD.party_id OR
       NEW.access_token_id IS DISTINCT FROM OLD.access_token_id OR
       NEW.correspondence_revision_id IS DISTINCT FROM OLD.correspondence_revision_id OR
       NEW.allegation_snapshot IS DISTINCT FROM OLD.allegation_snapshot OR
       NEW.requested_at IS DISTINCT FROM OLD.requested_at OR
       NEW.reminder_due_at IS DISTINCT FROM OLD.reminder_due_at OR
       NEW.due_at IS DISTINCT FROM OLD.due_at THEN
        RAISE EXCEPTION 'sanction response request snapshot is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sanction_response_request_protect ON sanction_response_requests;
CREATE TRIGGER trg_sanction_response_request_protect
BEFORE UPDATE OR DELETE ON sanction_response_requests
FOR EACH ROW EXECUTE FUNCTION protect_sanction_response_request();
