-- Idempotent provenance for merging multiple ineligible-player intakes into
-- one investigation.  Current scalar case fields remain the primary queue
-- projection; these append-only mappings retain every reporting club, subject
-- source, and immutable intake file attached to the case.

ALTER TABLE sanction_case_parties
    ADD COLUMN IF NOT EXISTS club_id INTEGER REFERENCES clubs(id) ON DELETE RESTRICT;

-- 0050-era response requests could create more than one offending party for a
-- team. Consolidate those rows before introducing unique identities. Keep
-- every historical party ID readable (case-event actor IDs are deliberately
-- not foreign-keyed) by demoting duplicates to representatives rather than
-- deleting them. Apply the same protection to manually duplicated reporting
-- or league parties.
CREATE TEMP TABLE sanction_party_merge_map (
    old_id BIGINT PRIMARY KEY,
    canonical_id BIGINT NOT NULL
) ON COMMIT DROP;

INSERT INTO sanction_party_merge_map(old_id,canonical_id)
SELECT party.id,duplicate.canonical_id
FROM sanction_case_parties party
JOIN (
    SELECT case_id,team_id,MIN(id) AS canonical_id
    FROM sanction_case_parties
    WHERE relationship='offending_club' AND team_id IS NOT NULL
    GROUP BY case_id,team_id HAVING COUNT(*)>1
) duplicate ON duplicate.case_id=party.case_id AND duplicate.team_id=party.team_id
WHERE party.relationship='offending_club' AND party.id<>duplicate.canonical_id
ON CONFLICT(old_id) DO NOTHING;

INSERT INTO sanction_party_merge_map(old_id,canonical_id)
SELECT party.id,duplicate.canonical_id
FROM sanction_case_parties party
JOIN (
    SELECT case_id,club_id,MIN(id) AS canonical_id
    FROM sanction_case_parties
    WHERE relationship='reporting_club' AND club_id IS NOT NULL
    GROUP BY case_id,club_id HAVING COUNT(*)>1
) duplicate ON duplicate.case_id=party.case_id AND duplicate.club_id=party.club_id
WHERE party.relationship='reporting_club' AND party.id<>duplicate.canonical_id
ON CONFLICT(old_id) DO NOTHING;

INSERT INTO sanction_party_merge_map(old_id,canonical_id)
SELECT party.id,duplicate.canonical_id
FROM sanction_case_parties party
JOIN (
    SELECT case_id,MIN(id) AS canonical_id
    FROM sanction_case_parties
    WHERE relationship='league' AND party_type='league'
    GROUP BY case_id HAVING COUNT(*)>1
) duplicate ON duplicate.case_id=party.case_id
WHERE party.relationship='league' AND party.party_type='league'
  AND party.id<>duplicate.canonical_id
ON CONFLICT(old_id) DO NOTHING;

UPDATE sanction_case_parties party
SET relationship='representative'
FROM sanction_party_merge_map merge
WHERE party.id=merge.old_id;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_reporting_club_party
    ON sanction_case_parties(case_id,club_id)
    WHERE relationship='reporting_club' AND club_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_offending_team_party
    ON sanction_case_parties(case_id,team_id)
    WHERE relationship='offending_club' AND team_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_league_origin_party
    ON sanction_case_parties(case_id,relationship)
    WHERE relationship='league' AND party_type='league';

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_party_identity
    ON sanction_case_parties(id,case_id,club_id);

CREATE TABLE IF NOT EXISTS sanction_case_reporting_club_intakes (
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    club_id INTEGER NOT NULL REFERENCES clubs(id) ON DELETE RESTRICT,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    party_id BIGINT NOT NULL,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(case_id,club_id,intake_id),
    FOREIGN KEY(party_id,case_id,club_id)
        REFERENCES sanction_case_parties(id,case_id,club_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_sanction_case_reporting_club_recipients
    ON sanction_case_reporting_club_intakes(case_id,club_id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_subject_identity
    ON sanction_case_subjects(id,case_id);

CREATE TABLE IF NOT EXISTS sanction_case_subject_intakes (
    subject_id BIGINT NOT NULL,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    revision_id BIGINT NOT NULL,
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(subject_id,intake_id,revision_id),
    FOREIGN KEY(subject_id,case_id)
        REFERENCES sanction_case_subjects(id,case_id) ON DELETE RESTRICT,
    FOREIGN KEY(revision_id,intake_id)
        REFERENCES sanction_intake_revisions(id,intake_id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_subject_named_player
    ON sanction_case_subjects(case_id,team_id,LOWER(BTRIM(player_name)))
    WHERE subject_type='player' AND play_cricket_player_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_subject_match
    ON sanction_case_subjects(case_id,play_cricket_match_id)
    WHERE subject_type='match' AND play_cricket_match_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_sanction_case_evidence_identity
    ON sanction_case_evidence(id,case_id);

CREATE TABLE IF NOT EXISTS sanction_case_intake_evidence (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    evidence_id BIGINT NOT NULL,
    intake_id BIGINT NOT NULL REFERENCES sanction_intakes(id) ON DELETE RESTRICT,
    revision_id BIGINT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('google_drive','native_upload')),
    source_key TEXT NOT NULL CHECK (NULLIF(BTRIM(source_key),'') IS NOT NULL),
    source_storage_key TEXT NOT NULL CHECK (NULLIF(BTRIM(source_storage_key),'') IS NOT NULL),
    source_sha256 TEXT NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    created_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(case_id,intake_id,revision_id,source_kind,source_key,source_sha256),
    UNIQUE(evidence_id),
    FOREIGN KEY(evidence_id,case_id)
        REFERENCES sanction_case_evidence(id,case_id) ON DELETE RESTRICT,
    FOREIGN KEY(revision_id,intake_id)
        REFERENCES sanction_intake_revisions(id,intake_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_sanction_case_intake_evidence_source
    ON sanction_case_intake_evidence(intake_id,revision_id);

DO $$
DECLARE table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'sanction_case_reporting_club_intakes',
        'sanction_case_subject_intakes',
        'sanction_case_intake_evidence'
    ] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_immutable ON %I', table_name, table_name);
        EXECUTE format(
            'CREATE TRIGGER trg_%I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change()',
            table_name, table_name
        );
    END LOOP;
END $$;
