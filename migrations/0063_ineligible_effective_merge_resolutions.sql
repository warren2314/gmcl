-- Append-only effective projections for source-derived case mappings. A form
-- correction may change player/team/reporting club; the prior mapping remains
-- historical, while the latest resolution per case/intake is authoritative.

CREATE TABLE IF NOT EXISTS sanction_case_intake_merge_resolutions (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    revision_id BIGINT NOT NULL,
    relationship TEXT NOT NULL CHECK (relationship IN ('primary','split','supporting','duplicate')),
    team_id INTEGER REFERENCES teams(id) ON DELETE RESTRICT,
    team_subject_id BIGINT,
    player_subject_id BIGINT,
    match_subject_id BIGINT,
    reporting_club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT,
    reporting_party_id BIGINT,
    league_origin BOOLEAN NOT NULL DEFAULT FALSE,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((league_origin AND reporting_club_id IS NULL AND reporting_party_id IS NULL)
        OR (NOT league_origin AND reporting_club_id IS NOT NULL AND reporting_party_id IS NOT NULL)),
    CHECK (relationship='duplicate' OR
        (team_id IS NOT NULL AND team_subject_id IS NOT NULL AND player_subject_id IS NOT NULL)),
    FOREIGN KEY(revision_id,intake_id)
        REFERENCES sanction_intake_revisions(id,intake_id) ON DELETE RESTRICT,
    FOREIGN KEY(team_subject_id,case_id)
        REFERENCES sanction_case_subjects(id,case_id) ON DELETE RESTRICT,
    FOREIGN KEY(player_subject_id,case_id)
        REFERENCES sanction_case_subjects(id,case_id) ON DELETE RESTRICT,
    FOREIGN KEY(match_subject_id,case_id)
        REFERENCES sanction_case_subjects(id,case_id) ON DELETE RESTRICT,
    FOREIGN KEY(reporting_party_id,case_id,reporting_club_id)
        REFERENCES sanction_case_parties(id,case_id,club_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_sanction_case_merge_resolution_current
    ON sanction_case_intake_merge_resolutions(case_id,intake_id,id DESC);

-- Compatibility seed for any merges completed during a staged 0059 rollout.
WITH linked AS (
    SELECT link.case_id,link.intake_id,link.relationship,
           (SELECT revision.id
            FROM sanction_case_subject_intakes bridge
            JOIN sanction_intake_revisions revision
              ON revision.id=bridge.revision_id AND revision.intake_id=bridge.intake_id
            WHERE bridge.case_id=link.case_id AND bridge.intake_id=link.intake_id
            ORDER BY revision.revision DESC,revision.id DESC LIMIT 1) AS revision_id
    FROM sanction_intake_case_links link
    WHERE link.relationship<>'duplicate'
), resolved AS (
    SELECT linked.*,
           team_subject.id AS team_subject_id,team_subject.team_id,
           player_subject.id AS player_subject_id,
           match_subject.id AS match_subject_id,
           reporting.club_id AS reporting_club_id,reporting.party_id AS reporting_party_id,
           reporting.club_id IS NULL AND EXISTS(
               SELECT 1 FROM sanction_case_parties party
               WHERE party.case_id=linked.case_id AND party.party_type='league'
                 AND party.relationship='league' AND party.name='GMCL Official'
           ) AS league_origin
    FROM linked
    LEFT JOIN LATERAL (
        SELECT subject.id,subject.team_id
        FROM sanction_case_subject_intakes bridge
        JOIN sanction_case_subjects subject ON subject.id=bridge.subject_id AND subject.case_id=bridge.case_id
        WHERE bridge.case_id=linked.case_id AND bridge.intake_id=linked.intake_id
          AND bridge.revision_id=linked.revision_id AND subject.subject_type='team'
        ORDER BY subject.is_primary DESC,subject.id LIMIT 1
    ) team_subject ON TRUE
    LEFT JOIN LATERAL (
        SELECT subject.id
        FROM sanction_case_subject_intakes bridge
        JOIN sanction_case_subjects subject ON subject.id=bridge.subject_id AND subject.case_id=bridge.case_id
        WHERE bridge.case_id=linked.case_id AND bridge.intake_id=linked.intake_id
          AND bridge.revision_id=linked.revision_id AND subject.subject_type='player'
        ORDER BY subject.is_primary DESC,subject.id LIMIT 1
    ) player_subject ON TRUE
    LEFT JOIN LATERAL (
        SELECT subject.id
        FROM sanction_case_subject_intakes bridge
        JOIN sanction_case_subjects subject ON subject.id=bridge.subject_id AND subject.case_id=bridge.case_id
        WHERE bridge.case_id=linked.case_id AND bridge.intake_id=linked.intake_id
          AND bridge.revision_id=linked.revision_id AND subject.subject_type='match'
        ORDER BY subject.is_primary DESC,subject.id LIMIT 1
    ) match_subject ON TRUE
    LEFT JOIN LATERAL (
        SELECT mapping.club_id,mapping.party_id
        FROM sanction_case_reporting_club_intakes mapping
        WHERE mapping.case_id=linked.case_id AND mapping.intake_id=linked.intake_id
        ORDER BY mapping.created_at DESC,mapping.club_id DESC LIMIT 1
    ) reporting ON TRUE
)
INSERT INTO sanction_case_intake_merge_resolutions(
    case_id,intake_id,revision_id,relationship,team_id,team_subject_id,player_subject_id,
    match_subject_id,reporting_club_id,reporting_party_id,league_origin
)
SELECT case_id,intake_id,revision_id,relationship,team_id,team_subject_id,player_subject_id,
       match_subject_id,reporting_club_id,reporting_party_id,league_origin
FROM resolved
WHERE revision_id IS NOT NULL AND team_id IS NOT NULL
  AND team_subject_id IS NOT NULL AND player_subject_id IS NOT NULL
  AND (league_origin OR reporting_club_id IS NOT NULL)
  AND NOT EXISTS (
      SELECT 1 FROM sanction_case_intake_merge_resolutions existing
      WHERE existing.case_id=resolved.case_id AND existing.intake_id=resolved.intake_id
  );

DROP TRIGGER IF EXISTS trg_sanction_case_intake_merge_resolutions_immutable
    ON sanction_case_intake_merge_resolutions;
CREATE TRIGGER trg_sanction_case_intake_merge_resolutions_immutable
BEFORE UPDATE OR DELETE ON sanction_case_intake_merge_resolutions
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();
