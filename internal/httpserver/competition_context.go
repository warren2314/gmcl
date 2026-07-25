package httpserver

import (
	"context"
	"fmt"
	"time"
)

type competitionWeekStatus string

const (
	competitionWeekActive    competitionWeekStatus = "active"
	competitionWeekUpcoming  competitionWeekStatus = "upcoming"
	competitionWeekCompleted competitionWeekStatus = "completed"
)

type competitionWeekResolution int

const (
	// competitionWeekActiveOnly is used when a workflow must not operate outside
	// a scheduled week.
	competitionWeekActiveOnly competitionWeekResolution = iota
	// competitionWeekForDisplay keeps planning screens useful between scheduled
	// weeks by showing the next week, then the most recently completed week.
	competitionWeekForDisplay
	// competitionWeekForSubmission preserves the established captain-link
	// behaviour: active week, then the most recently completed week, then the
	// next scheduled week.
	competitionWeekForSubmission
	// competitionWeekForPlanning is used by work that prepares the active or
	// next scheduled week and must not fall back to a completed week.
	competitionWeekForPlanning
	// competitionWeekForSupportLink preserves the support-link rule: the active
	// week, or the most recently completed week when the season is between weeks.
	competitionWeekForSupportLink
)

type competitionWeek struct {
	ID                  int32
	SeasonID            int32
	SeasonName          string
	Number              int32
	StartDate           time.Time
	EndDate             time.Time
	ComplianceStartWeek int32
	Status              competitionWeekStatus
}

func competitionDate(now time.Time, london *time.Location) string {
	if london == nil {
		london = time.UTC
	}
	return now.In(london).Format("2006-01-02")
}

func (s *Server) londonDate() string {
	return competitionDate(time.Now(), s.LondonLoc)
}

// resolveCompetitionWeek is the authoritative season/week lookup. All callers
// use an explicit Europe/London calendar date, so results do not depend on the
// PostgreSQL session timezone. The policy only controls behaviour when today is
// not inside a scheduled week; an active week always wins.
func (s *Server) resolveCompetitionWeek(
	ctx context.Context,
	resolution competitionWeekResolution,
) (competitionWeek, error) {
	var week competitionWeek

	upcomingPriority, completedPriority := 1, 2
	allowUpcoming, allowCompleted := true, true
	if resolution == competitionWeekForSubmission {
		upcomingPriority, completedPriority = 2, 1
	}
	switch resolution {
	case competitionWeekActiveOnly:
		allowUpcoming, allowCompleted = false, false
	case competitionWeekForPlanning:
		allowUpcoming, allowCompleted = true, false
	case competitionWeekForSupportLink:
		allowUpcoming, allowCompleted = false, true
	}

	err := s.DB.QueryRow(ctx, `
		SELECT
		    w.id,
		    s.id,
		    s.name,
		    w.week_number,
		    w.start_date,
		    w.end_date,
		    s.compliance_start_week,
		    CASE
		        WHEN $1::date BETWEEN w.start_date AND w.end_date THEN 'active'
		        WHEN w.start_date > $1::date THEN 'upcoming'
		        ELSE 'completed'
		    END AS week_status
		FROM weeks w
		JOIN seasons s ON s.id = w.season_id
		WHERE s.is_archived = FALSE
		  AND (
		      $1::date BETWEEN w.start_date AND w.end_date
		      OR ($4::boolean AND w.start_date > $1::date)
		      OR ($5::boolean AND w.end_date < $1::date)
		  )
		ORDER BY
		    CASE
		        WHEN $1::date BETWEEN w.start_date AND w.end_date THEN 0
		        WHEN w.start_date > $1::date THEN $2::integer
		        ELSE $3::integer
		    END,
		    CASE
		        WHEN $1::date BETWEEN w.start_date AND w.end_date
		        THEN s.start_date
		    END DESC,
		    CASE
		        WHEN w.start_date > $1::date
		        THEN w.start_date
		    END ASC,
		    CASE
		        WHEN w.end_date < $1::date
		        THEN w.end_date
		    END DESC,
		    w.week_number DESC
		LIMIT 1
	`, s.londonDate(), upcomingPriority, completedPriority, allowUpcoming, allowCompleted).Scan(
		&week.ID,
		&week.SeasonID,
		&week.SeasonName,
		&week.Number,
		&week.StartDate,
		&week.EndDate,
		&week.ComplianceStartWeek,
		&week.Status,
	)
	if err != nil {
		return competitionWeek{}, fmt.Errorf("resolve competition week: %w", err)
	}
	return week, nil
}

func competitionWeekStatusLabel(status competitionWeekStatus) string {
	switch status {
	case competitionWeekActive:
		return "Current competition week"
	case competitionWeekUpcoming:
		return "Next scheduled week"
	case competitionWeekCompleted:
		return "Most recently completed week"
	default:
		return "Competition week"
	}
}
