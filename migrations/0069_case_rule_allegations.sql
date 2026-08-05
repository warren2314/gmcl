-- The rule alleged during an investigation is not the final rule finding.
-- Keep each selection as an immutable revision tied to the published rules
-- release that the investigator reviewed.

CREATE TABLE IF NOT EXISTS sanction_case_rule_allegation_revisions (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL REFERENCES sanction_cases(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    supersedes_id BIGINT REFERENCES sanction_case_rule_allegation_revisions(id) ON DELETE RESTRICT,
    rule_release_id BIGINT NOT NULL REFERENCES rule_releases(id) ON DELETE RESTRICT,
    rule_reference TEXT NOT NULL CHECK (NULLIF(BTRIM(rule_reference),'') IS NOT NULL),
    heading_path TEXT NOT NULL,
    rule_text TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    source_title TEXT NOT NULL,
    selection_reason TEXT NOT NULL,
    selected_by_admin_id INTEGER REFERENCES admin_users(id) ON DELETE SET NULL,
    selected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(case_id,revision),
    UNIQUE(id,case_id)
);

CREATE INDEX IF NOT EXISTS idx_sanction_case_rule_allegation_latest
    ON sanction_case_rule_allegation_revisions(case_id,revision DESC,id DESC);

DROP TRIGGER IF EXISTS trg_sanction_case_rule_allegation_revisions_immutable
    ON sanction_case_rule_allegation_revisions;
CREATE TRIGGER trg_sanction_case_rule_allegation_revisions_immutable
BEFORE UPDATE OR DELETE ON sanction_case_rule_allegation_revisions
FOR EACH ROW EXECUTE FUNCTION reject_immutable_sanction_change();

-- Starred-player cases have one deterministic alleged rule: Rule 3.5. Seed
-- existing linked cases from the active published release without inventing a
-- rule for ordinary form reports, which still require investigator review.
WITH published_rule AS (
    SELECT release.id AS release_id,chunk.rule_reference,chunk.heading_path,chunk.content,
           document.canonical_url,document.title
    FROM rule_releases release
    JOIN rule_chunks chunk ON chunk.release_id=release.id
    JOIN rule_documents document ON document.id=chunk.document_id
    WHERE release.status='active' AND LOWER(BTRIM(chunk.rule_reference))='3.5'
    ORDER BY chunk.ordinal,chunk.id LIMIT 1
), inserted AS (
    INSERT INTO sanction_case_rule_allegation_revisions(
        case_id,revision,rule_release_id,rule_reference,heading_path,rule_text,
        canonical_url,source_title,selection_reason
    )
    SELECT DISTINCT link.case_id,1,published_rule.release_id,published_rule.rule_reference,
           published_rule.heading_path,published_rule.content,published_rule.canonical_url,
           published_rule.title,'Seeded from revalidated starred-player finding provenance'
    FROM sanction_intake_case_links link
    JOIN sanction_intakes intake ON intake.id=link.intake_id AND intake.origin='starred_player'
    CROSS JOIN published_rule
    WHERE link.relationship<>'duplicate'
      AND NOT EXISTS(SELECT 1 FROM sanction_case_rule_allegation_revisions existing WHERE existing.case_id=link.case_id)
    RETURNING id,case_id,rule_release_id,rule_reference,canonical_url
)
INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,after_data)
SELECT case_id,'alleged_rule_recorded','system','Starred-player workflow',
       'Rule 3.5 seeded from revalidated starred-player finding provenance',
       jsonb_build_object('allegation_revision_id',id,'rule_release_id',rule_release_id,
                          'rule_reference',rule_reference,'canonical_url',canonical_url)
FROM inserted;
