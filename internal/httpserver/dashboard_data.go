package httpserver

import (
	"context"
	"fmt"
)

// reportProgress is measured in required team reports, not teams or matches.
// One fixture normally creates two required reports; double-headers create one
// requirement per team and fixture. Approved exemptions satisfy a requirement
// without being mislabelled as a submitted report.
type reportProgress struct {
	Expected  int64
	Submitted int64
	Exempt    int64
	Satisfied int64
	Missing   int64
}

type clubReportProgress struct {
	Club      string
	Teams     int64
	Expected  int64
	Submitted int64
	Exempt    int64
	Satisfied int64
	Missing   int64
	AvgPitch  float64
}

func (p clubReportProgress) completionRate() float64 {
	if p.Expected <= 0 {
		return 0
	}
	return float64(p.Satisfied) / float64(p.Expected) * 100
}

func (p reportProgress) completionRate() float64 {
	if p.Expected <= 0 {
		return 0
	}
	return float64(p.Satisfied) / float64(p.Expected) * 100
}

func (s *Server) loadWeekReportProgress(
	ctx context.Context,
	week competitionWeek,
) (reportProgress, error) {
	var progress reportProgress
	err := s.DB.QueryRow(ctx, `
		WITH expected_fixtures AS (
		    SELECT
		        t.id AS team_id,
		        lf.play_cricket_match_id,
		        lf.match_date,
		        ROW_NUMBER() OVER (
		            PARTITION BY t.id, lf.match_date
		            ORDER BY lf.play_cricket_match_id
		        ) AS fixture_ordinal
		    FROM teams t
		    JOIN league_fixtures lf ON (
		        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
		        OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
		    )
		    WHERE t.active = TRUE
		      AND t.play_cricket_team_id IS NOT NULL
		      AND t.play_cricket_team_id <> ''
		      AND lf.match_date BETWEEN $2 AND $3
		      AND EXTRACT(DOW FROM lf.match_date) <> 5
		      AND NOT lf.is_bye
		),
		legacy_submissions AS (
		    SELECT team_id, match_date, COUNT(*) AS legacy_count
		    FROM submissions
		    WHERE week_id = $1
		      AND play_cricket_match_id IS NULL
		    GROUP BY team_id, match_date
		),
		fixture_status AS (
		    SELECT
		        ef.team_id,
		        ef.play_cricket_match_id,
		        (
		            EXISTS (
		                SELECT 1
		                FROM submissions sub
		                WHERE sub.week_id = $1
		                  AND sub.team_id = ef.team_id
		                  AND sub.play_cricket_match_id = ef.play_cricket_match_id
		            )
		            OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
		        ) AS submitted,
		        EXISTS (
		            SELECT 1
		            FROM report_exemptions re
		            WHERE re.week_id = $1
		              AND re.team_id = ef.team_id
		              AND re.match_date = ef.match_date
		              AND (
		                  re.play_cricket_match_id = ef.play_cricket_match_id
		                  OR re.play_cricket_match_id IS NULL
		              )
		        ) AS exempt
		    FROM expected_fixtures ef
		    LEFT JOIN legacy_submissions ls
		      ON ls.team_id = ef.team_id
		     AND ls.match_date = ef.match_date
		)
		SELECT
		    COUNT(*) AS expected,
		    COUNT(*) FILTER (WHERE submitted) AS submitted,
		    COUNT(*) FILTER (WHERE exempt AND NOT submitted) AS exempt,
		    COUNT(*) FILTER (WHERE submitted OR exempt) AS satisfied,
		    COUNT(*) FILTER (WHERE NOT submitted AND NOT exempt) AS missing
		FROM fixture_status
	`, week.ID, week.StartDate, week.EndDate).Scan(
		&progress.Expected,
		&progress.Submitted,
		&progress.Exempt,
		&progress.Satisfied,
		&progress.Missing,
	)
	if err != nil {
		return reportProgress{}, fmt.Errorf("load week report progress: %w", err)
	}
	return progress, nil
}

func (s *Server) loadSeasonReportProgress(
	ctx context.Context,
	seasonID int32,
	fromWeek int32,
) (reportProgress, error) {
	var progress reportProgress
	err := s.DB.QueryRow(ctx, `
		WITH tracked_weeks AS (
		    SELECT id, start_date, end_date
		    FROM weeks
		    WHERE season_id = $1
		      AND week_number >= $2
		      AND start_date <= $3::date
		),
		expected_fixtures AS (
		    SELECT
		        tw.id AS week_id,
		        t.id AS team_id,
		        lf.play_cricket_match_id,
		        lf.match_date,
		        ROW_NUMBER() OVER (
		            PARTITION BY tw.id, t.id, lf.match_date
		            ORDER BY lf.play_cricket_match_id
		        ) AS fixture_ordinal
		    FROM tracked_weeks tw
		    JOIN league_fixtures lf
		      ON lf.match_date BETWEEN tw.start_date AND tw.end_date
		    JOIN teams t ON (
		        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
		        OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
		    )
		    WHERE t.active = TRUE
		      AND t.play_cricket_team_id IS NOT NULL
		      AND t.play_cricket_team_id <> ''
		      AND EXTRACT(DOW FROM lf.match_date) <> 5
		      AND NOT lf.is_bye
		),
		legacy_submissions AS (
		    SELECT week_id, team_id, match_date, COUNT(*) AS legacy_count
		    FROM submissions
		    WHERE season_id = $1
		      AND play_cricket_match_id IS NULL
		    GROUP BY week_id, team_id, match_date
		),
		fixture_status AS (
		    SELECT
		        (
		            EXISTS (
		                SELECT 1
		                FROM submissions sub
		                WHERE sub.week_id = ef.week_id
		                  AND sub.team_id = ef.team_id
		                  AND sub.play_cricket_match_id = ef.play_cricket_match_id
		            )
		            OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
		        ) AS submitted,
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
		    LEFT JOIN legacy_submissions ls
		      ON ls.week_id = ef.week_id
		     AND ls.team_id = ef.team_id
		     AND ls.match_date = ef.match_date
		)
		SELECT
		    COUNT(*) AS expected,
		    COUNT(*) FILTER (WHERE submitted) AS submitted,
		    COUNT(*) FILTER (WHERE exempt AND NOT submitted) AS exempt,
		    COUNT(*) FILTER (WHERE submitted OR exempt) AS satisfied,
		    COUNT(*) FILTER (WHERE NOT submitted AND NOT exempt) AS missing
		FROM fixture_status
	`, seasonID, fromWeek, s.londonDate()).Scan(
		&progress.Expected,
		&progress.Submitted,
		&progress.Exempt,
		&progress.Satisfied,
		&progress.Missing,
	)
	if err != nil {
		return reportProgress{}, fmt.Errorf("load season report progress: %w", err)
	}
	return progress, nil
}

func (s *Server) loadSeasonClubReportProgress(
	ctx context.Context,
	seasonID int32,
	fromWeek int32,
) ([]clubReportProgress, error) {
	rows, err := s.DB.Query(ctx, `
		WITH tracked_weeks AS (
		    SELECT id, start_date, end_date
		    FROM weeks
		    WHERE season_id = $1
		      AND week_number >= $2
		      AND start_date <= $3::date
		),
		club_base AS (
		    SELECT cl.id AS club_id, cl.name, COUNT(*) AS team_count
		    FROM clubs cl
		    JOIN teams t ON t.club_id = cl.id
		    WHERE t.active = TRUE
		    GROUP BY cl.id, cl.name
		),
		expected_fixtures AS (
		    SELECT
		        tw.id AS week_id,
		        cl.id AS club_id,
		        t.id AS team_id,
		        lf.play_cricket_match_id,
		        lf.match_date,
		        ROW_NUMBER() OVER (
		            PARTITION BY tw.id, t.id, lf.match_date
		            ORDER BY lf.play_cricket_match_id
		        ) AS fixture_ordinal
		    FROM tracked_weeks tw
		    JOIN league_fixtures lf
		      ON lf.match_date BETWEEN tw.start_date AND tw.end_date
		    JOIN teams t ON (
		        TRIM(lf.home_team_pc_id) = TRIM(t.play_cricket_team_id)
		        OR TRIM(lf.away_team_pc_id) = TRIM(t.play_cricket_team_id)
		    )
		    JOIN clubs cl ON cl.id = t.club_id
		    WHERE t.active = TRUE
		      AND t.play_cricket_team_id IS NOT NULL
		      AND t.play_cricket_team_id <> ''
		      AND EXTRACT(DOW FROM lf.match_date) <> 5
		      AND NOT lf.is_bye
		),
		legacy_submissions AS (
		    SELECT week_id, team_id, match_date, COUNT(*) AS legacy_count
		    FROM submissions
		    WHERE season_id = $1
		      AND play_cricket_match_id IS NULL
		    GROUP BY week_id, team_id, match_date
		),
		fixture_status AS (
		    SELECT
		        ef.club_id,
		        (
		            EXISTS (
		                SELECT 1
		                FROM submissions sub
		                WHERE sub.week_id = ef.week_id
		                  AND sub.team_id = ef.team_id
		                  AND sub.play_cricket_match_id = ef.play_cricket_match_id
		            )
		            OR ef.fixture_ordinal <= COALESCE(ls.legacy_count, 0)
		        ) AS submitted,
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
		    LEFT JOIN legacy_submissions ls
		      ON ls.week_id = ef.week_id
		     AND ls.team_id = ef.team_id
		     AND ls.match_date = ef.match_date
		),
		progress AS (
		    SELECT
		        club_id,
		        COUNT(*) AS expected,
		        COUNT(*) FILTER (WHERE submitted) AS submitted,
		        COUNT(*) FILTER (WHERE exempt AND NOT submitted) AS exempt,
		        COUNT(*) FILTER (WHERE submitted OR exempt) AS satisfied,
		        COUNT(*) FILTER (WHERE NOT submitted AND NOT exempt) AS missing
		    FROM fixture_status
		    GROUP BY club_id
		),
		pitch AS (
		    SELECT
		        t.club_id,
		        COALESCE(ROUND(AVG(sub.pitch_rating)::numeric, 2), 0) AS avg_pitch
		    FROM submissions sub
		    JOIN teams t ON t.id = sub.team_id
		    WHERE sub.season_id = $1
		      AND sub.pitch_rating IS NOT NULL
		    GROUP BY t.club_id
		)
		SELECT
		    cb.name,
		    cb.team_count,
		    COALESCE(p.expected, 0),
		    COALESCE(p.submitted, 0),
		    COALESCE(p.exempt, 0),
		    COALESCE(p.satisfied, 0),
		    COALESCE(p.missing, 0),
		    COALESCE(pi.avg_pitch, 0)
		FROM club_base cb
		LEFT JOIN progress p ON p.club_id = cb.club_id
		LEFT JOIN pitch pi ON pi.club_id = cb.club_id
		ORDER BY cb.name
	`, seasonID, fromWeek, s.londonDate())
	if err != nil {
		return nil, fmt.Errorf("load club report progress: %w", err)
	}
	defer rows.Close()

	var clubs []clubReportProgress
	for rows.Next() {
		var club clubReportProgress
		if err := rows.Scan(
			&club.Club,
			&club.Teams,
			&club.Expected,
			&club.Submitted,
			&club.Exempt,
			&club.Satisfied,
			&club.Missing,
			&club.AvgPitch,
		); err != nil {
			return nil, fmt.Errorf("scan club report progress: %w", err)
		}
		clubs = append(clubs, club)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate club report progress: %w", err)
	}
	return clubs, nil
}
