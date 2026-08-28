-- Stuart is the operational Play-Cricket recipient for card-system and
-- league-table points. Denver remains the separate final issuer and task owner.
INSERT INTO sanction_recipient_directory(recipient_role,name,email,active)
VALUES ('play_cricket','Stuart Russell','playcrickethelp@gtrmcrcricket.co.uk',TRUE)
ON CONFLICT (recipient_role,email) DO UPDATE
SET name=EXCLUDED.name,active=TRUE;

-- Queue one official, already-approved outcome for each published live
-- ineligible-player case with points that has never been addressed to Stuart.
-- Reuse the locked official correspondence/PDF; do not generate or edit it.
WITH point_cases AS (
    SELECT cases.id AS case_id,decision.id AS decision_revision_id
    FROM sanction_cases cases
    JOIN LATERAL (
        SELECT revision.id
        FROM sanction_decision_revisions revision
        WHERE revision.case_id=cases.id AND revision.status='approved'
        ORDER BY revision.revision DESC,revision.id DESC
        LIMIT 1
    ) decision ON TRUE
    WHERE cases.source_type='ineligible_player'
      AND cases.status='published'
      AND NOT cases.is_test
      AND NOT EXISTS(
          SELECT 1 FROM sanction_case_events training
          WHERE training.case_id=cases.id AND training.event_type='case_training_designated'
      )
      AND EXISTS(
          SELECT 1 FROM sanction_effect_revisions effect
          WHERE effect.decision_revision_id=decision.id AND COALESCE(effect.points,0)<>0
      )
), official AS (
    SELECT DISTINCT ON (point.case_id)
           point.case_id,point.decision_revision_id,outbox.policy_version_id,
           outbox.correspondence_revision_id,outbox.subject,outbox.body,
           outbox.attachment_manifest
    FROM point_cases point
    JOIN sanction_notification_outbox outbox
      ON outbox.case_id=point.case_id
     AND outbox.decision_revision_id=point.decision_revision_id
     AND outbox.message_kind='outcome_official'
     AND outbox.revoked_at IS NULL
    JOIN sanction_correspondence_revisions correspondence
      ON correspondence.id=outbox.correspondence_revision_id
     AND correspondence.case_id=outbox.case_id
     AND correspondence.audience='official'
    ORDER BY point.case_id,outbox.id DESC
), queued AS (
    INSERT INTO sanction_notification_outbox(
        case_id,decision_revision_id,policy_version_id,correspondence_revision_id,
        message_kind,idempotency_key,recipient,subject,body,attachment_manifest
    )
    SELECT official.case_id,official.decision_revision_id,official.policy_version_id,
           official.correspondence_revision_id,'outcome_official',
           'case:'||official.case_id||':decision:'||official.decision_revision_id||
             ':play-cricket-backfill:playcrickethelp@gtrmcrcricket.co.uk',
           'playcrickethelp@gtrmcrcricket.co.uk',official.subject,official.body,
           official.attachment_manifest
    FROM official
    WHERE NOT EXISTS(
        SELECT 1 FROM sanction_notification_outbox existing
        WHERE existing.case_id=official.case_id
          AND LOWER(BTRIM(existing.recipient))='playcrickethelp@gtrmcrcricket.co.uk'
          AND existing.revoked_at IS NULL
    )
    ON CONFLICT(idempotency_key) DO NOTHING
    RETURNING case_id,id
)
INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,after_data)
SELECT queued.case_id,'play_cricket_outcome_backfill_queued','system','Stuart points-email repair',
       'Queued the previously approved official points outcome for Stuart; no correspondence was changed',
       jsonb_build_object('outbox_id',queued.id,'recipient','playcrickethelp@gtrmcrcricket.co.uk')
FROM queued;
