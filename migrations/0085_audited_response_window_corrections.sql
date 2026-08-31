-- Allow an investigator to move a live response-window start earlier when the
-- club was first contacted outside the system. The correction is attributed
-- here and is also recorded with before/after values in sanction_case_events.

ALTER TABLE sanction_response_requests
    ADD COLUMN IF NOT EXISTS window_corrected_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS window_corrected_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS window_correction_reason TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='ck_sanction_response_window_correction_complete'
          AND conrelid='sanction_response_requests'::regclass
    ) THEN
        ALTER TABLE sanction_response_requests
            ADD CONSTRAINT ck_sanction_response_window_correction_complete CHECK (
                (window_corrected_at IS NULL
                 AND window_corrected_by_admin_id IS NULL
                 AND window_correction_reason IS NULL)
                OR
                (window_corrected_at IS NOT NULL
                 AND window_corrected_by_admin_id IS NOT NULL
                 AND NULLIF(BTRIM(window_correction_reason),'') IS NOT NULL)
            );
    END IF;
END
$$;

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
        IF NEW.window_corrected_at IS DISTINCT FROM OLD.window_corrected_at OR
           NEW.window_corrected_by_admin_id IS DISTINCT FROM OLD.window_corrected_by_admin_id OR
           NEW.window_correction_reason IS DISTINCT FROM OLD.window_correction_reason THEN
            RAISE EXCEPTION 'an undelivered response window cannot be corrected';
        END IF;
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
        IF NEW.status='pending' THEN
            IF NEW.delivered_at IS NULL OR NEW.delivered_at>=OLD.delivered_at OR
               NEW.reminder_due_at<=NEW.delivered_at OR NEW.due_at<=NEW.reminder_due_at OR
               NEW.reminder_queued_at IS DISTINCT FROM OLD.reminder_queued_at OR
               NEW.responded_at IS DISTINCT FROM OLD.responded_at OR
               NEW.closed_at IS DISTINCT FROM OLD.closed_at OR
               NEW.window_corrected_at IS NULL OR
               NEW.window_corrected_at IS NOT DISTINCT FROM OLD.window_corrected_at OR
               (OLD.window_corrected_at IS NOT NULL AND NEW.window_corrected_at<=OLD.window_corrected_at) OR
               NEW.window_corrected_by_admin_id IS NULL OR
               NULLIF(BTRIM(NEW.window_correction_reason),'') IS NULL THEN
                RAISE EXCEPTION 'delivered response window correction is invalid';
            END IF;
        ELSE
            IF NEW.delivered_at IS DISTINCT FROM OLD.delivered_at OR
               NEW.reminder_due_at IS DISTINCT FROM OLD.reminder_due_at OR
               NEW.due_at IS DISTINCT FROM OLD.due_at OR
               NEW.reminder_queued_at IS DISTINCT FROM OLD.reminder_queued_at OR
               NEW.window_corrected_at IS DISTINCT FROM OLD.window_corrected_at OR
               NEW.window_corrected_by_admin_id IS DISTINCT FROM OLD.window_corrected_by_admin_id OR
               NEW.window_correction_reason IS DISTINCT FROM OLD.window_correction_reason THEN
                RAISE EXCEPTION 'delivered response window is immutable outside an attributed earlier-date correction';
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
        END IF;
    ELSIF OLD.status='expired' THEN
        IF NEW.delivered_at IS DISTINCT FROM OLD.delivered_at OR
           NEW.reminder_due_at IS DISTINCT FROM OLD.reminder_due_at OR
           NEW.due_at IS DISTINCT FROM OLD.due_at OR
           NEW.reminder_queued_at IS DISTINCT FROM OLD.reminder_queued_at OR
           NEW.window_corrected_at IS DISTINCT FROM OLD.window_corrected_at OR
           NEW.window_corrected_by_admin_id IS DISTINCT FROM OLD.window_corrected_by_admin_id OR
           NEW.window_correction_reason IS DISTINCT FROM OLD.window_correction_reason OR
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
