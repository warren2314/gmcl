-- Evidence supplied with a report or club response is an immutable private
-- source record.  It must never be exposed to the offending club directly.
-- Staff may instead upload a separately reviewed redacted derivative.  The
-- provenance, checksums and reviewer attestation below are append-only.

CREATE TABLE IF NOT EXISTS sanction_case_evidence_derivatives (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    source_evidence_id BIGINT NOT NULL,
    derivative_evidence_id BIGINT NOT NULL UNIQUE,
    source_sha256 TEXT NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    derivative_sha256 TEXT NOT NULL CHECK (derivative_sha256 ~ '^[0-9a-f]{64}$'),
    reviewer_admin_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    attestation_code TEXT NOT NULL CHECK (
        attestation_code='reporter_and_reporting_club_identity_removed_v1'
    ),
    review_notes TEXT NOT NULL CHECK (NULLIF(BTRIM(review_notes),'') IS NOT NULL),
    reviewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (source_evidence_id<>derivative_evidence_id),
    CHECK (source_sha256<>derivative_sha256),
    FOREIGN KEY(source_evidence_id,case_id)
        REFERENCES sanction_case_evidence(id,case_id) ON DELETE RESTRICT,
    FOREIGN KEY(derivative_evidence_id,case_id)
        REFERENCES sanction_case_evidence(id,case_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_sanction_case_evidence_derivative_source
    ON sanction_case_evidence_derivatives(case_id,source_evidence_id,id DESC);

CREATE OR REPLACE FUNCTION validate_sanction_case_evidence_derivative()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_record RECORD;
    derivative_record RECORD;
BEGIN
    SELECT evidence.sha256,evidence.storage_key,evidence.redacted_at
      INTO source_record
      FROM sanction_case_evidence evidence
     WHERE evidence.id=NEW.source_evidence_id AND evidence.case_id=NEW.case_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'redacted evidence derivative requires an available source record'
            USING ERRCODE='23514';
    END IF;
    IF source_record.redacted_at IS NOT NULL THEN
        RAISE EXCEPTION 'redacted evidence derivative requires an available source record'
            USING ERRCODE='23514';
    END IF;

    IF EXISTS (
        SELECT 1 FROM sanction_case_evidence_derivatives prior
        WHERE prior.derivative_evidence_id=NEW.source_evidence_id
    ) THEN
        RAISE EXCEPTION 'a redacted derivative cannot be used as another derivative source'
            USING ERRCODE='23514';
    END IF;
    IF EXISTS (
        SELECT 1 FROM sanction_case_evidence_derivatives prior
        WHERE prior.source_evidence_id=NEW.derivative_evidence_id
    ) THEN
        RAISE EXCEPTION 'an evidence source cannot later be reclassified as a redacted derivative'
            USING ERRCODE='23514';
    END IF;

    SELECT evidence.sha256,evidence.storage_key,evidence.visibility,evidence.uploaded_by_type,
           evidence.uploaded_by_id,evidence.redacted_at
      INTO derivative_record
      FROM sanction_case_evidence evidence
     WHERE evidence.id=NEW.derivative_evidence_id AND evidence.case_id=NEW.case_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'redacted evidence derivative requires an available derivative record'
            USING ERRCODE='23514';
    END IF;
    IF derivative_record.redacted_at IS NOT NULL THEN
        RAISE EXCEPTION 'redacted evidence derivative requires an available derivative record'
            USING ERRCODE='23514';
    END IF;
    IF derivative_record.visibility<>'party'
       OR derivative_record.uploaded_by_type<>'admin'
       OR derivative_record.uploaded_by_id IS DISTINCT FROM NEW.reviewer_admin_id::bigint THEN
        RAISE EXCEPTION 'redacted evidence derivative must be uploaded and reviewed by the recorded administrator'
            USING ERRCODE='23514';
    END IF;
    IF lower(source_record.sha256)<>NEW.source_sha256
       OR lower(derivative_record.sha256)<>NEW.derivative_sha256
       OR NEW.source_sha256=NEW.derivative_sha256
       OR source_record.storage_key=derivative_record.storage_key THEN
        RAISE EXCEPTION 'redacted evidence derivative checksum provenance is invalid'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_sanction_case_evidence_derivative_validate
    ON sanction_case_evidence_derivatives;
CREATE TRIGGER trg_sanction_case_evidence_derivative_validate
BEFORE INSERT ON sanction_case_evidence_derivatives
FOR EACH ROW EXECUTE FUNCTION validate_sanction_case_evidence_derivative();

DROP TRIGGER IF EXISTS trg_sanction_case_evidence_derivatives_immutable
    ON sanction_case_evidence_derivatives;
CREATE TRIGGER trg_sanction_case_evidence_derivatives_immutable
BEFORE UPDATE OR DELETE ON sanction_case_evidence_derivatives
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();

-- This view is the sole database projection permitted for offending-club
-- disclosure.  Imported evidence also has to belong to the currently reviewed
-- immutable intake revision; a later source change fails closed.
CREATE OR REPLACE VIEW sanction_offending_club_evidence_derivatives AS
SELECT provenance.case_id,
       provenance.source_evidence_id,
       provenance.derivative_evidence_id AS evidence_id,
       provenance.reviewer_admin_id,
       provenance.attestation_code,
       provenance.review_notes,
       provenance.reviewed_at
FROM sanction_case_evidence_derivatives provenance
JOIN sanction_case_evidence source_evidence
  ON source_evidence.id=provenance.source_evidence_id AND source_evidence.case_id=provenance.case_id
JOIN sanction_case_evidence derivative_evidence
  ON derivative_evidence.id=provenance.derivative_evidence_id AND derivative_evidence.case_id=provenance.case_id
WHERE source_evidence.redacted_at IS NULL
  AND derivative_evidence.redacted_at IS NULL
  AND lower(source_evidence.sha256)=provenance.source_sha256
  AND lower(derivative_evidence.sha256)=provenance.derivative_sha256
  AND source_evidence.storage_key<>derivative_evidence.storage_key
  AND provenance.attestation_code='reporter_and_reporting_club_identity_removed_v1'
  AND (
      NOT EXISTS (
          SELECT 1 FROM sanction_case_intake_evidence bridge
          WHERE bridge.case_id=provenance.case_id
            AND bridge.evidence_id=provenance.source_evidence_id
      )
      OR EXISTS (
          SELECT 1
          FROM sanction_case_intake_evidence bridge
          JOIN sanction_intakes intake
            ON intake.id=bridge.intake_id AND intake.state='linked'
          JOIN sanction_case_intake_merge_resolutions resolution
            ON resolution.case_id=bridge.case_id
           AND resolution.intake_id=bridge.intake_id
          WHERE bridge.case_id=provenance.case_id
            AND bridge.evidence_id=provenance.source_evidence_id
            AND resolution.revision_id=bridge.revision_id
            AND resolution.id=(
                SELECT latest.id
                FROM sanction_case_intake_merge_resolutions latest
                WHERE latest.case_id=resolution.case_id
                  AND latest.intake_id=resolution.intake_id
                ORDER BY latest.id DESC
                LIMIT 1
            )
      )
  );

-- Fail closed for any source evidence shared before this policy existed.
-- The source and its prior decision remain immutable; revocation is appended.
WITH latest_sharing AS (
    SELECT DISTINCT ON (sharing.case_id,sharing.evidence_id,sharing.audience)
           sharing.case_id,sharing.evidence_id,sharing.audience,sharing.action
    FROM sanction_evidence_sharing_events sharing
    WHERE sharing.audience='offending_club'
    ORDER BY sharing.case_id,sharing.evidence_id,sharing.audience,sharing.id DESC
)
INSERT INTO sanction_evidence_sharing_events(
    case_id,evidence_id,audience,action,reason,request_id
)
SELECT latest.case_id,latest.evidence_id,latest.audience,'revoked',
       'Automatically revoked: offending-club disclosure now requires a reviewed redacted derivative',
       'migration-0066'
FROM latest_sharing latest
WHERE latest.action='shared'
  AND NOT EXISTS (
      SELECT 1 FROM sanction_offending_club_evidence_derivatives allowed
      WHERE allowed.case_id=latest.case_id AND allowed.evidence_id=latest.evidence_id
  );

CREATE OR REPLACE FUNCTION require_redacted_derivative_for_evidence_share()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.action='shared' AND NOT EXISTS (
        SELECT 1
        FROM sanction_offending_club_evidence_derivatives allowed
        WHERE allowed.case_id=NEW.case_id AND allowed.evidence_id=NEW.evidence_id
    ) THEN
        RAISE EXCEPTION 'offending-club evidence sharing requires a reviewed redacted derivative'
            USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_sanction_evidence_share_requires_derivative
    ON sanction_evidence_sharing_events;
CREATE TRIGGER trg_sanction_evidence_share_requires_derivative
BEFORE INSERT ON sanction_evidence_sharing_events
FOR EACH ROW EXECUTE FUNCTION require_redacted_derivative_for_evidence_share();
