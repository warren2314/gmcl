package sanctions

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CasePrivacyQueryer is implemented by both the connection pool and pgx
// transactions. Privacy checks use the transaction when one is already
// locking the case, so a newly linked intake cannot be missed mid-operation.
type CasePrivacyQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// CaseReporterIdentityValues returns reporter details from the case projection,
// reporter parties, and every immutable revision of every linked intake. A
// merged investigation can have more than one reporter; checking only the
// first case projection would allow a later reporter's details to leak.
func CaseReporterIdentityValues(ctx context.Context, queryer CasePrivacyQueryer, caseID int64) ([]string, error) {
	rows, err := queryer.Query(ctx, `
		SELECT DISTINCT private_value
		FROM (
			SELECT value.private_value
			FROM sanction_cases cases
			CROSS JOIN LATERAL (VALUES
				(COALESCE(cases.reporter_name,'')),
				(COALESCE(cases.reporter_email,'')),
				(COALESCE(cases.reporter_role,'')),
				(COALESCE(cases.reporter_phone,''))
			) value(private_value)
			WHERE cases.id=$1

			UNION ALL

			SELECT value.private_value
			FROM sanction_case_parties party
			CROSS JOIN LATERAL (VALUES
				(COALESCE(party.name,'')),
				(COALESCE(party.email,''))
			) value(private_value)
			WHERE party.case_id=$1 AND party.party_type='reporter'

			UNION ALL

			SELECT value.private_value
			FROM sanction_intake_case_links link
			JOIN sanction_intake_revisions revision ON revision.intake_id=link.intake_id
			CROSS JOIN LATERAL (VALUES
				(COALESCE(revision.raw_data->>'Email address','')),
				(COALESCE(revision.raw_data->>'Your Name & Role at Club/League','')),
				(COALESCE(revision.raw_data->>'Your Preferred tel no',''))
			) value(private_value)
			WHERE link.case_id=$1
		) private_values
		WHERE NULLIF(BTRIM(private_value),'') IS NOT NULL
		ORDER BY private_value
	`, caseID)
	if err != nil {
		return nil, fmt.Errorf("load case reporter identities: %w", err)
	}
	defer rows.Close()
	return scanPrivacyValues(rows, "case reporter identities")
}

// CaseReportingClubIdentityValues returns both the current mapped name and the
// literal source aliases used by linked intakes. When excludedClubID is set,
// aliases belonging to that club are omitted so a same-club report can receive
// one combined notice without treating its own club name as private.
func CaseReportingClubIdentityValues(ctx context.Context, queryer CasePrivacyQueryer, caseID int64, excludedClubID *int32) ([]string, error) {
	rows, err := queryer.Query(ctx, `
		SELECT DISTINCT private_value
		FROM (
			SELECT club.name AS private_value
			FROM sanction_case_reporting_club_intakes mapping
			JOIN clubs club ON club.id=mapping.club_id
			WHERE mapping.case_id=$1 AND ($2::integer IS NULL OR mapping.club_id<>$2)

			UNION ALL

			SELECT club.name
			FROM sanction_cases cases
			JOIN clubs club ON club.id=cases.reporting_club_id
			WHERE cases.id=$1 AND ($2::integer IS NULL OR cases.reporting_club_id<>$2)

			UNION ALL

			SELECT intake.reporting_club_text
			FROM sanction_intake_case_links link
			JOIN sanction_intakes intake ON intake.id=link.intake_id
			WHERE link.case_id=$1
			  AND NOT EXISTS (
				SELECT 1 FROM sanction_case_reporting_club_intakes mapping
				WHERE mapping.case_id=$1 AND mapping.intake_id=intake.id
				  AND $2::integer IS NOT NULL AND mapping.club_id=$2
			  )

			UNION ALL

			SELECT revision.raw_data->>'Your Club'
			FROM sanction_intake_case_links link
			JOIN sanction_intakes intake ON intake.id=link.intake_id
			JOIN sanction_intake_revisions revision ON revision.intake_id=intake.id
			WHERE link.case_id=$1
			  AND NOT EXISTS (
				SELECT 1 FROM sanction_case_reporting_club_intakes mapping
				WHERE mapping.case_id=$1 AND mapping.intake_id=intake.id
				  AND $2::integer IS NOT NULL AND mapping.club_id=$2
			  )
		) private_values
		WHERE NULLIF(BTRIM(private_value),'') IS NOT NULL
		ORDER BY private_value
	`, caseID, excludedClubID)
	if err != nil {
		return nil, fmt.Errorf("load case reporting-club identities: %w", err)
	}
	defer rows.Close()
	return scanPrivacyValues(rows, "case reporting-club identities")
}

// CaseReportingOutcomeRestrictedValues returns exact private source text that
// must never be copied into the reporting-club version of an outcome. Public
// findings may summarise the investigation, but the offending club's response,
// private rationale, internal notes and private evidence labels stay confined
// to investigators and league officials.
func CaseReportingOutcomeRestrictedValues(ctx context.Context, queryer CasePrivacyQueryer, caseID int64) ([]string, error) {
	rows, err := queryer.Query(ctx, `
		SELECT DISTINCT restricted_value
		FROM (
			SELECT COALESCE(cases.private_summary,'') AS restricted_value
			FROM sanction_cases cases WHERE cases.id=$1

			UNION ALL

			SELECT COALESCE(decision.private_reason,'')
			FROM sanction_decision_revisions decision WHERE decision.case_id=$1

			UNION ALL

			SELECT COALESCE(event.reason,'')
			FROM sanction_case_events event
			WHERE event.case_id=$1 AND event.event_type IN
				('party_response','external_response_recorded','appeal_submitted','investigation_note')

			UNION ALL

			SELECT COALESCE(event.metadata->>'respondent','')
			FROM sanction_case_events event
			WHERE event.case_id=$1 AND event.event_type='external_response_recorded'

			UNION ALL

			SELECT COALESCE(event.before_data->>'private_summary','')
			FROM sanction_case_events event
			WHERE event.case_id=$1

			UNION ALL

			SELECT COALESCE(event.after_data->>'private_summary','')
			FROM sanction_case_events event
			WHERE event.case_id=$1

			UNION ALL

			SELECT COALESCE(evidence.original_name,'')
			FROM sanction_case_evidence evidence
			WHERE evidence.case_id=$1 AND evidence.visibility IN ('private','party')
		) restricted_values
		WHERE NULLIF(BTRIM(restricted_value),'') IS NOT NULL
		ORDER BY restricted_value
	`, caseID)
	if err != nil {
		return nil, fmt.Errorf("load reporting-outcome restricted content: %w", err)
	}
	defer rows.Close()
	return scanPrivacyValues(rows, "reporting-outcome restricted content")
}

// ContainsRestrictedContent performs normalized exact-fragment detection
// without the reporter-name splitting rules used for identity checks.
func ContainsRestrictedContent(body string, values ...string) bool {
	return outcomeContainsPrivateIdentity(body, restrictedContentVariants(values...)...)
}

func restrictedContentVariants(values ...string) []string {
	seen := map[string]bool{}
	variants := make([]string, 0, len(values)*2)
	add := func(value string) {
		value = strings.TrimSpace(value)
		key := strings.ToLower(strings.Join(strings.Fields(value), " "))
		if len([]rune(compactIdentityText(key))) < 5 || seen[key] {
			return
		}
		seen[key] = true
		variants = append(variants, value)
	}
	for _, value := range values {
		add(value)
		for _, fragment := range strings.FieldsFunc(value, func(r rune) bool {
			return r == '\n' || r == '\r' || r == '.' || r == '!' || r == '?' || r == ';'
		}) {
			if len(strings.Fields(fragment)) >= 3 {
				add(fragment)
			}
		}
		words := strings.Fields(value)
		const window = 8
		for index := 0; index+window <= len(words); index++ {
			add(strings.Join(words[index:index+window], " "))
		}
	}
	return variants
}

func scanPrivacyValues(rows pgx.Rows, label string) ([]string, error) {
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	return values, nil
}
