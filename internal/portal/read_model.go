package portal

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ReportSummary struct {
	Expected  int64
	Submitted int64
	Exempt    int64
	Due       int64
	Missed    int64
	Late      int64
}

type SanctionSummary struct {
	Yellow             int64
	Red                int64
	PointsDeduction    int64
	UnreconciledLegacy int64
}

type TeamSanctionSummary struct {
	TeamID          int32
	TeamName        string
	Yellow          int64
	Red             int64
	PointsDeduction int64
}

type ClubDashboard struct {
	ClubID             int32
	ClubName           string
	SeasonID           int32
	SeasonName         string
	SeasonStart        time.Time
	SeasonEnd          time.Time
	Reports            ReportSummary
	Sanctions          SanctionSummary
	TeamSanctions      []TeamSanctionSummary
	LastFixtureSyncAt  *time.Time
	FixtureSourceStale bool
	CalculatedAt       time.Time
}

func (store *Store) LoadClubDashboard(
	ctx context.Context,
	principal Principal,
) (ClubDashboard, error) {
	if principal.Assignment == nil {
		return ClubDashboard{}, ErrForbidden
	}
	now := store.now()
	assignment := *principal.Assignment
	if !Authorize(assignment, PermissionPortalView, Scope{ClubID: assignment.Scope.ClubID}, now) {
		return ClubDashboard{}, ErrForbidden
	}

	dashboard := ClubDashboard{
		ClubID:       assignment.Scope.ClubID,
		CalculatedAt: now,
	}
	err := store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		if err := tx.QueryRow(ctx, `
			SELECT name FROM clubs WHERE id = $1
		`, assignment.Scope.ClubID).Scan(&dashboard.ClubName); err != nil {
			return fmt.Errorf("load portal club: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT id, name, start_date, end_date
			FROM seasons
			WHERE ($1::integer IS NULL OR id = $1)
			ORDER BY
				CASE WHEN $2::date BETWEEN start_date AND end_date THEN 0 ELSE 1 END,
				is_archived,
				start_date DESC
			LIMIT 1
		`, assignment.Scope.SeasonID, now).Scan(
			&dashboard.SeasonID,
			&dashboard.SeasonName,
			&dashboard.SeasonStart,
			&dashboard.SeasonEnd,
		); err != nil {
			return fmt.Errorf("load portal season: %w", err)
		}

		if Authorize(assignment, PermissionReportsView, Scope{
			ClubID:   assignment.Scope.ClubID,
			TeamID:   assignment.Scope.TeamID,
			SeasonID: &dashboard.SeasonID,
		}, now) {
			if err := loadClubReportSummary(
				ctx,
				tx,
				assignment,
				dashboard.SeasonID,
				now,
				&dashboard,
			); err != nil {
				return err
			}
		}
		if Authorize(assignment, PermissionSanctionsView, Scope{
			ClubID:   assignment.Scope.ClubID,
			TeamID:   assignment.Scope.TeamID,
			SeasonID: &dashboard.SeasonID,
		}, now) {
			if err := loadClubSanctionSummary(
				ctx,
				tx,
				assignment,
				dashboard.SeasonID,
				&dashboard,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ClubDashboard{}, err
	}
	if dashboard.LastFixtureSyncAt != nil {
		dashboard.FixtureSourceStale = now.Sub(*dashboard.LastFixtureSyncAt) > 36*time.Hour
	}
	return dashboard, nil
}

func loadClubReportSummary(
	ctx context.Context,
	tx pgx.Tx,
	assignment Assignment,
	seasonID int32,
	now time.Time,
	dashboard *ClubDashboard,
) error {
	var lastSync pgtype.Timestamptz
	err := tx.QueryRow(ctx, `
		WITH expected_fixtures AS (
			SELECT
				w.id AS week_id,
				t.id AS team_id,
				lf.play_cricket_match_id,
				lf.match_date,
				lf.fetched_at,
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
			  AND t.active = TRUE
			  AND t.play_cricket_team_id IS NOT NULL
			  AND t.play_cricket_team_id <> ''
			  AND EXTRACT(DOW FROM lf.match_date) <> 5
			  AND NOT lf.is_bye
		),
		legacy_submissions AS (
			SELECT
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
		),
		fixture_status AS (
			SELECT
				ef.*,
				COALESCE(exact_sub.submitted_at, legacy_sub.submitted_at) AS submitted_at,
				EXISTS (
					SELECT 1
					FROM report_exemptions re
					WHERE re.week_id = ef.week_id
					  AND re.team_id = ef.team_id
					  AND re.match_date = ef.match_date
					  AND (
						re.play_cricket_match_id = ef.play_cricket_match_id
						OR re.play_cricket_match_id IS NULL
					  )
				) AS exempt
			FROM expected_fixtures ef
			LEFT JOIN LATERAL (
				SELECT MIN(sub.submitted_at) AS submitted_at
				FROM submissions sub
				WHERE sub.week_id = ef.week_id
				  AND sub.team_id = ef.team_id
				  AND sub.play_cricket_match_id = ef.play_cricket_match_id
			) exact_sub ON TRUE
			LEFT JOIN legacy_submissions legacy_sub
			  ON legacy_sub.week_id = ef.week_id
			 AND legacy_sub.team_id = ef.team_id
			 AND legacy_sub.match_date = ef.match_date
			 AND legacy_sub.submission_ordinal = ef.fixture_ordinal
		)
		SELECT
			COUNT(*) AS expected,
			COUNT(*) FILTER (WHERE submitted_at IS NOT NULL) AS submitted,
			COUNT(*) FILTER (WHERE exempt AND submitted_at IS NULL) AS exempt,
			COUNT(*) FILTER (
				WHERE submitted_at IS NULL AND NOT exempt AND deadline_at >= $4
			) AS due,
			COUNT(*) FILTER (
				WHERE submitted_at IS NULL AND NOT exempt AND deadline_at < $4
			) AS missed,
			COUNT(*) FILTER (
				WHERE submitted_at IS NOT NULL AND submitted_at > deadline_at
			) AS late,
			MAX(fetched_at) AS last_fixture_sync_at
		FROM fixture_status
	`, assignment.Scope.ClubID, seasonID, assignment.Scope.TeamID, now).Scan(
		&dashboard.Reports.Expected,
		&dashboard.Reports.Submitted,
		&dashboard.Reports.Exempt,
		&dashboard.Reports.Due,
		&dashboard.Reports.Missed,
		&dashboard.Reports.Late,
		&lastSync,
	)
	if err != nil {
		return fmt.Errorf("load tenant-scoped report summary: %w", err)
	}
	if lastSync.Valid {
		value := lastSync.Time
		dashboard.LastFixtureSyncAt = &value
	}
	return nil
}

func loadClubSanctionSummary(
	ctx context.Context,
	tx pgx.Tx,
	assignment Assignment,
	seasonID int32,
	dashboard *ClubDashboard,
) error {
	if err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(yellow_delta), 0),
			COALESCE(SUM(red_delta), 0),
			COALESCE(SUM(points_deduction), 0)
		FROM sanction_card_ledger_entries
		WHERE club_id = $1
		  AND season_id = $2
		  AND ($3::integer IS NULL OR team_id = $3)
	`, assignment.Scope.ClubID, seasonID, assignment.Scope.TeamID).Scan(
		&dashboard.Sanctions.Yellow,
		&dashboard.Sanctions.Red,
		&dashboard.Sanctions.PointsDeduction,
	); err != nil {
		return fmt.Errorf("load tenant-scoped sanction totals: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM sanctions
		WHERE club_id = $1
		  AND season_id = $2
		  AND ($3::integer IS NULL OR team_id = $3)
		  AND case_id IS NULL
		  AND status <> 'overturned'
	`, assignment.Scope.ClubID, seasonID, assignment.Scope.TeamID).Scan(
		&dashboard.Sanctions.UnreconciledLegacy,
	); err != nil {
		return fmt.Errorf("load tenant-scoped legacy sanction reconciliation: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT
			t.id,
			t.name,
			COALESCE(SUM(l.yellow_delta), 0) AS yellow,
			COALESCE(SUM(l.red_delta), 0) AS red,
			COALESCE(SUM(l.points_deduction), 0) AS points
		FROM teams t
		JOIN sanction_card_ledger_entries l ON l.team_id = t.id
		WHERE t.club_id = $1
		  AND l.club_id = $1
		  AND l.season_id = $2
		  AND ($3::integer IS NULL OR t.id = $3)
		GROUP BY t.id, t.name
		HAVING
			SUM(l.yellow_delta) <> 0
			OR SUM(l.red_delta) <> 0
			OR SUM(l.points_deduction) <> 0
		ORDER BY t.name
	`, assignment.Scope.ClubID, seasonID, assignment.Scope.TeamID)
	if err != nil {
		return fmt.Errorf("load tenant-scoped team sanction totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var team TeamSanctionSummary
		if err := rows.Scan(
			&team.TeamID,
			&team.TeamName,
			&team.Yellow,
			&team.Red,
			&team.PointsDeduction,
		); err != nil {
			return fmt.Errorf("scan tenant-scoped team sanction totals: %w", err)
		}
		dashboard.TeamSanctions = append(dashboard.TeamSanctions, team)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tenant-scoped team sanction totals: %w", err)
	}
	return nil
}

func (summary ReportSummary) Satisfied() int64 {
	return summary.Submitted + summary.Exempt
}

func (summary ReportSummary) CompletionPercent() float64 {
	if summary.Expected == 0 {
		return 0
	}
	return float64(summary.Satisfied()) / float64(summary.Expected) * 100
}
