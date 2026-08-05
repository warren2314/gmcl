-- A club's five/seven-day response clock starts only after the initial
-- response-request email has been accepted by SMTP. Queueing a notice must not
-- consume the club's response window while delivery is disabled or failing.

ALTER TABLE sanction_response_requests
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS reminder_correspondence_revision_id BIGINT;

ALTER TABLE sanction_response_requests
    ALTER COLUMN reminder_due_at DROP NOT NULL,
    ALTER COLUMN due_at DROP NOT NULL;

-- Requests created before this migration started their clock when they were
-- queued. Preserve those established windows and record that historical start
-- as the best available delivery timestamp.
UPDATE sanction_response_requests
SET delivered_at=COALESCE(delivered_at,requested_at),
    reminder_queued_at=COALESCE(reminder_queued_at,requested_at)
WHERE status IN ('pending','responded','expired','cancelled')
  AND reminder_due_at IS NOT NULL
  AND due_at IS NOT NULL;

-- Recover the immutable reminder snapshot used by the pre-0064 outbox. The
-- nearest same-case reminder revision is the one created in the request's
-- transaction. Closed history is allowed to remain without a recovered link;
-- a live request is not.
UPDATE sanction_response_requests request
SET reminder_correspondence_revision_id=(
    SELECT correspondence.id
    FROM sanction_correspondence_revisions correspondence
    WHERE correspondence.case_id=request.case_id
      AND correspondence.message_kind='response_reminder'
      AND correspondence.audience='offending_club'
      AND EXISTS (
          SELECT 1 FROM sanction_notification_outbox outbox
          WHERE outbox.case_id=request.case_id
            AND outbox.correspondence_revision_id=correspondence.id
            AND outbox.message_kind='response_reminder'
      )
    ORDER BY ABS(EXTRACT(EPOCH FROM (correspondence.created_at-request.requested_at))),
             correspondence.id DESC
    LIMIT 1
)
WHERE request.reminder_correspondence_revision_id IS NULL;

-- Fail closed if a currently live legacy request has no provable reminder
-- snapshot. Staff can review and issue a fresh request; no email is sent here.
WITH invalid AS (
    SELECT id,case_id,access_token_id
    FROM sanction_response_requests
    WHERE status='pending' AND reminder_correspondence_revision_id IS NULL
), cancelled AS (
    UPDATE sanction_response_requests request
    SET status='cancelled',closed_at=COALESCE(request.closed_at,now())
    FROM invalid
    WHERE request.id=invalid.id
    RETURNING request.case_id,request.access_token_id
), revoked AS (
    UPDATE sanction_case_access_tokens token
    SET revoked_at=COALESCE(token.revoked_at,now())
    FROM cancelled
    WHERE token.id=cancelled.access_token_id
    RETURNING token.id
), notices AS (
    UPDATE sanction_notification_outbox outbox
    SET processed_at=now()
    FROM (SELECT DISTINCT case_id FROM cancelled) stopped
    WHERE outbox.case_id=stopped.case_id
      AND outbox.message_kind IN ('response_request','response_reminder')
      AND outbox.processed_at IS NULL
    RETURNING outbox.id
), resumed AS (
    UPDATE sanction_cases cases
    SET status='investigating',updated_at=now()
    FROM (SELECT DISTINCT case_id FROM cancelled) stopped
    WHERE cases.id=stopped.case_id AND cases.status='response_pending'
    RETURNING cases.id
)
INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason)
SELECT DISTINCT case_id,'legacy_response_request_cancelled','system',
       'Response delivery-window migration',
       'Legacy live request was cancelled because its immutable reminder snapshot could not be recovered; review and reissue it'
FROM cancelled;

ALTER TABLE sanction_response_requests
    DROP CONSTRAINT IF EXISTS sanction_response_requests_status_check,
    DROP CONSTRAINT IF EXISTS sanction_response_requests_check,
    DROP CONSTRAINT IF EXISTS ck_sanction_response_delivery_window;

ALTER TABLE sanction_response_requests
    ADD CONSTRAINT sanction_response_requests_status_check
        CHECK (status IN ('queued','pending','responded','expired','cancelled')),
    ADD CONSTRAINT ck_sanction_response_delivery_window CHECK (
        (
            status='queued'
            AND reminder_correspondence_revision_id IS NOT NULL
            AND delivered_at IS NULL
            AND reminder_due_at IS NULL
            AND due_at IS NULL
            AND reminder_queued_at IS NULL
        ) OR (
            status='pending'
            AND reminder_correspondence_revision_id IS NOT NULL
            AND delivered_at IS NOT NULL
            AND reminder_due_at>delivered_at
            AND due_at>reminder_due_at
            AND reminder_queued_at IS NOT NULL
        ) OR (
            status IN ('responded','expired','cancelled')
            AND (
                (delivered_at IS NULL AND reminder_due_at IS NULL AND due_at IS NULL AND reminder_queued_at IS NULL)
                OR
                (delivered_at IS NOT NULL AND reminder_due_at>delivered_at AND due_at>reminder_due_at AND reminder_queued_at IS NOT NULL)
            )
        )
    );

DROP INDEX IF EXISTS uq_sanction_response_pending;
DROP INDEX IF EXISTS uq_sanction_response_live_token;

CREATE UNIQUE INDEX uq_sanction_response_live_case
    ON sanction_response_requests(case_id)
    WHERE status IN ('queued','pending');

CREATE UNIQUE INDEX uq_sanction_response_live_token
    ON sanction_response_requests(access_token_id)
    WHERE status IN ('queued','pending');

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='fk_sanction_response_reminder_correspondence_case'
          AND conrelid='sanction_response_requests'::regclass
    ) THEN
        ALTER TABLE sanction_response_requests
            ADD CONSTRAINT fk_sanction_response_reminder_correspondence_case
            FOREIGN KEY(reminder_correspondence_revision_id,case_id)
            REFERENCES sanction_correspondence_revisions(id,case_id)
            ON DELETE RESTRICT;
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
       NEW.reminder_correspondence_revision_id IS DISTINCT FROM OLD.reminder_correspondence_revision_id OR
       NEW.allegation_snapshot IS DISTINCT FROM OLD.allegation_snapshot OR
       NEW.requested_at IS DISTINCT FROM OLD.requested_at THEN
        RAISE EXCEPTION 'sanction response request snapshot is immutable';
    END IF;

    IF OLD.status='queued' THEN
        IF NEW.status='pending' THEN
            IF OLD.delivered_at IS NOT NULL OR OLD.reminder_due_at IS NOT NULL OR
               OLD.due_at IS NOT NULL OR OLD.reminder_queued_at IS NOT NULL OR
               NEW.delivered_at IS NULL OR NEW.delivered_at<NEW.requested_at OR
               NEW.reminder_due_at<=NEW.delivered_at OR NEW.due_at<=NEW.reminder_due_at OR
               NEW.reminder_queued_at IS NULL OR NEW.responded_at IS NOT NULL OR NEW.closed_at IS NOT NULL THEN
                RAISE EXCEPTION 'queued response request must be activated by one complete delivery transition';
            END IF;
        ELSIF NEW.status='responded' THEN
            IF NEW.delivered_at IS NOT NULL OR NEW.reminder_due_at IS NOT NULL OR
               NEW.due_at IS NOT NULL OR NEW.reminder_queued_at IS NOT NULL OR
               NEW.responded_at IS NULL OR NEW.closed_at IS NULL THEN
                RAISE EXCEPTION 'queued response request may only be closed as an undelivered manual response';
            END IF;
        ELSIF NEW.status='cancelled' THEN
            IF NEW.delivered_at IS NOT NULL OR NEW.reminder_due_at IS NOT NULL OR
               NEW.due_at IS NOT NULL OR NEW.reminder_queued_at IS NOT NULL OR
               NEW.responded_at IS NOT NULL OR NEW.closed_at IS NULL THEN
                RAISE EXCEPTION 'queued response request cancellation is invalid';
            END IF;
        ELSE
            RAISE EXCEPTION 'invalid queued response request transition: % to %',OLD.status,NEW.status;
        END IF;
    ELSIF OLD.status='pending' THEN
        IF NEW.delivered_at IS DISTINCT FROM OLD.delivered_at OR
           NEW.reminder_due_at IS DISTINCT FROM OLD.reminder_due_at OR
           NEW.due_at IS DISTINCT FROM OLD.due_at OR
           NEW.reminder_queued_at IS DISTINCT FROM OLD.reminder_queued_at THEN
            RAISE EXCEPTION 'delivered response window is immutable';
        END IF;
        IF NEW.status='responded' THEN
            IF NEW.responded_at IS NULL OR NEW.closed_at IS NULL THEN
                RAISE EXCEPTION 'responded response request requires response and closure timestamps';
            END IF;
        ELSIF NEW.status IN ('expired','cancelled') THEN
            IF NEW.responded_at IS NOT NULL OR NEW.closed_at IS NULL THEN
                RAISE EXCEPTION 'closed response request transition is invalid';
            END IF;
        ELSE
            RAISE EXCEPTION 'invalid pending response request transition: % to %',OLD.status,NEW.status;
        END IF;
    ELSIF OLD.status='expired' THEN
        IF NEW.delivered_at IS DISTINCT FROM OLD.delivered_at OR
           NEW.reminder_due_at IS DISTINCT FROM OLD.reminder_due_at OR
           NEW.due_at IS DISTINCT FROM OLD.due_at OR
           NEW.reminder_queued_at IS DISTINCT FROM OLD.reminder_queued_at OR
           NEW.status<>'responded' OR NEW.responded_at IS NULL OR NEW.closed_at IS NULL THEN
            RAISE EXCEPTION 'expired response request may only record a late response without changing its delivery window';
        END IF;
    ELSE
        RAISE EXCEPTION 'terminal response request is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sanction_response_request_protect ON sanction_response_requests;
CREATE TRIGGER trg_sanction_response_request_protect
BEFORE UPDATE OR DELETE ON sanction_response_requests
FOR EACH ROW EXECUTE FUNCTION protect_sanction_response_request();
