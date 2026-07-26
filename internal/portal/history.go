package portal

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ReportObligation struct {
	FixtureID    int64
	MatchID      int64
	TeamID       int32
	TeamName     string
	MatchDate    time.Time
	Opposition   string
	Venue        string
	DeadlineAt   time.Time
	SubmissionID *int64
	SubmittedAt  *time.Time
	Exempt       bool
	ExemptReason string
	Status       string
}

type SanctionLedgerEntry struct {
	ID              int64
	CaseReference   string
	PublicStatus    string
	PublicSummary   string
	TeamID          int32
	TeamName        string
	MatchDate       *time.Time
	YellowDelta     int64
	RedDelta        int64
	PointsDeduction int64
	EntryType       string
	CreatedAt       time.Time
}

func (store *Store) LoadReportObligations(
	ctx context.Context,
	principal Principal,
	seasonID int32,
) ([]ReportObligation, error) {
	if principal.Assignment == nil || seasonID <= 0 {
		return nil, ErrForbidden
	}
	now := store.now()
	scope := Scope{
		ClubID:   principal.Assignment.Scope.ClubID,
		TeamID:   principal.Assignment.Scope.TeamID,
		SeasonID: &seasonID,
	}
	if !Authorize(*principal.Assignment, PermissionReportsView, scope, now) {
		return nil, ErrForbidden
	}

	var obligations []ReportObligation
	err := store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		rows, err := tx.Query(ctx, `
			WITH expected_fixtures AS (
				SELECT
					lf.id AS fixture_id,
					w.id AS week_id,
					t.id AS team_id,
					t.name AS team_name,
					lf.play_cricket_match_id,
					lf.match_date,
					CASE
						WHEN TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
							THEN COALESCE(NULLIF(lf.away_team_name, ''), lf.away_club_name, 'Unknown opposition')
						ELSE COALESCE(NULLIF(lf.home_team_name, ''), lf.home_club_name, 'Unknown opposition')
					END AS opposition,
					COALESCE(lf.ground_name, '') AS venue,
					(
						lf.match_date
						+ (((3 - EXTRACT(DOW FROM lf.match_date)::integer) + 7) % 7)
						+ TIME '23:59:59'
					) AT TIME ZONE 'Europe/London' AS deadline_at,
					ROW_NUMBER() OVER (
						PARTITION BY t.id, lf.match_date
						ORDER BY lf.play_cricket_match_id
					) AS fixture_ordinal
				FROM weeks w
				JOIN league_fixtures lf
				  ON lf.match_date BETWEEN w.start_date AND w.end_date
				JOIN teams t
				  ON TRIM(t.play_cricket_team_id) = TRIM(lf.home_team_pc_id)
				  OR TRIM(t.play_cricket_team_id) = TRIM(lf.away_team_pc_id)
				WHERE w.season_id = $2
				  AND w.start_date <= $4::date
				  AND t.club_id = $1
				  AND ($3::integer IS NULL OR t.id = $3)
				  AND t.play_cricket_team_id IS NOT NULL
				  AND t.play_cricket_team_id <> ''
				  AND EXTRACT(DOW FROM lf.match_date) <> 5
				  AND NOT lf.is_bye
			),
			legacy_submissions AS (
				SELECT
					sub.id,
					sub.week_id,
					sub.team_id,
					sub.match_date,
					sub.submitted_at,
					ROW_NUMBER() OVER (
						PARTITION BY sub.week_id, sub.team_id, sub.match_date
						ORDER BY sub.submitted_at, sub.id
					) AS submission_ordinal
				FROM submissions sub
				JOIN teams t ON t.id = sub.team_id
				WHERE sub.season_id = $2
				  AND sub.play_cricket_match_id IS NULL
				  AND t.club_id = $1
				  AND ($3::integer IS NULL OR t.id = $3)
			)
			SELECT
				ef.fixture_id,
				ef.play_cricket_match_id,
				ef.team_id,
				ef.team_name,
				ef.match_date,
				ef.opposition,
				ef.venue,
				ef.deadline_at,
				COALESCE(exact_sub.id, legacy_sub.id) AS submission_id,
				COALESCE(exact_sub.submitted_at, legacy_sub.submitted_at) AS submitted_at,
				exemption.id IS NOT NULL AS exempt,
				COALESCE(exemption.reason, '')
			FROM expected_fixtures ef
			LEFT JOIN LATERAL (
				SELECT sub.id, sub.submitted_at
				FROM submissions sub
				WHERE sub.week_id = ef.week_id
				  AND sub.team_id = ef.team_id
				  AND sub.play_cricket_match_id = ef.play_cricket_match_id
				ORDER BY sub.submitted_at, sub.id
				LIMIT 1
			) exact_sub ON TRUE
			LEFT JOIN legacy_submissions legacy_sub
			  ON legacy_sub.week_id = ef.week_id
			 AND legacy_sub.team_id = ef.team_id
			 AND legacy_sub.match_date = ef.match_date
			 AND legacy_sub.submission_ordinal = ef.fixture_ordinal
			LEFT JOIN LATERAL (
				SELECT re.id, re.reason
				FROM report_exemptions re
				WHERE re.week_id = ef.week_id
				  AND re.team_id = ef.team_id
				  AND re.match_date = ef.match_date
				  AND (
					re.play_cricket_match_id = ef.play_cricket_match_id
					OR re.play_cricket_match_id IS NULL
				  )
				ORDER BY
					(re.play_cricket_match_id = ef.play_cricket_match_id) DESC,
					re.created_at
				LIMIT 1
			) exemption ON TRUE
			ORDER BY ef.match_date DESC, ef.team_name, ef.play_cricket_match_id
		`, assignment.Scope.ClubID, seasonID, assignment.Scope.TeamID, now)
		if err != nil {
			return fmt.Errorf("load tenant-scoped report obligations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				obligation   ReportObligation
				submissionID pgtype.Int8
				submittedAt  pgtype.Timestamptz
			)
			if err := rows.Scan(
				&obligation.FixtureID,
				&obligation.MatchID,
				&obligation.TeamID,
				&obligation.TeamName,
				&obligation.MatchDate,
				&obligation.Opposition,
				&obligation.Venue,
				&obligation.DeadlineAt,
				&submissionID,
				&submittedAt,
				&obligation.Exempt,
				&obligation.ExemptReason,
			); err != nil {
				return fmt.Errorf("scan tenant-scoped report obligation: %w", err)
			}
			if submissionID.Valid {
				value := submissionID.Int64
				obligation.SubmissionID = &value
			}
			obligation.SubmittedAt = timePtr(submittedAt)
			obligation.Status = reportObligationStatus(obligation, now)
			obligations = append(obligations, obligation)
		}
		return rows.Err()
	})
	return obligations, err
}

func reportObligationStatus(obligation ReportObligation, now time.Time) string {
	if obligation.SubmittedAt != nil {
		if obligation.SubmittedAt.After(obligation.DeadlineAt) {
			return "late"
		}
		return "submitted"
	}
	if obligation.Exempt {
		return "exempt"
	}
	if obligation.DeadlineAt.Before(now) {
		return "missed"
	}
	return "due"
}

func (store *Store) LoadSanctionLedger(
	ctx context.Context,
	principal Principal,
	seasonID int32,
) ([]SanctionLedgerEntry, error) {
	if principal.Assignment == nil || seasonID <= 0 {
		return nil, ErrForbidden
	}
	now := store.now()
	scope := Scope{
		ClubID:   principal.Assignment.Scope.ClubID,
		TeamID:   principal.Assignment.Scope.TeamID,
		SeasonID: &seasonID,
	}
	if !Authorize(*principal.Assignment, PermissionSanctionsView, scope, now) {
		return nil, ErrForbidden
	}

	var entries []SanctionLedgerEntry
	err := store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		rows, err := tx.Query(ctx, `
			SELECT
				l.id,
				sc.reference,
				sc.public_status,
				COALESCE(sc.public_summary, ''),
				l.team_id,
				t.name,
				l.match_date,
				l.yellow_delta,
				l.red_delta,
				l.points_deduction,
				l.entry_type,
				l.created_at
			FROM sanction_card_ledger_entries l
			JOIN sanction_cases sc
			  ON sc.id = l.case_id
			 AND sc.club_id = l.club_id
			JOIN teams t
			  ON t.id = l.team_id
			 AND t.club_id = l.club_id
			WHERE l.club_id = $1
			  AND l.season_id = $2
			  AND ($3::integer IS NULL OR l.team_id = $3)
			ORDER BY COALESCE(l.match_date, l.created_at::date) DESC, l.id DESC
		`, assignment.Scope.ClubID, seasonID, assignment.Scope.TeamID)
		if err != nil {
			return fmt.Errorf("load tenant-scoped sanction ledger: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				entry     SanctionLedgerEntry
				matchDate pgtype.Date
			)
			if err := rows.Scan(
				&entry.ID,
				&entry.CaseReference,
				&entry.PublicStatus,
				&entry.PublicSummary,
				&entry.TeamID,
				&entry.TeamName,
				&matchDate,
				&entry.YellowDelta,
				&entry.RedDelta,
				&entry.PointsDeduction,
				&entry.EntryType,
				&entry.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan tenant-scoped sanction ledger entry: %w", err)
			}
			if matchDate.Valid {
				value := matchDate.Time
				entry.MatchDate = &value
			}
			entries = append(entries, entry)
		}
		return rows.Err()
	})
	return entries, err
}
