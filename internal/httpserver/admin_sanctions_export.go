package httpserver

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type sanctionsExportFilter struct {
	SeasonID int32
	WeekFrom int32
	WeekTo   int32
}

type sanctionsExportRow struct {
	ID              int64
	Season          string
	Week            int32
	Club            string
	Team            string
	Colour          string
	Reason          string
	Notes           string
	Status          string
	EmailStatus     string
	PointsDeduction sql.NullInt32
	IssuedAt        time.Time
	IssuedBy        string
	ResolvedAt      sql.NullTime
	ResolvedBy      string
	EmailApprovedBy string
	EmailApprovedAt sql.NullTime
	EmailSentAt     sql.NullTime
}

func (s *Server) handleAdminSanctionsExportCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		filter, err := s.resolveSanctionsExportFilter(ctx, r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		rows, err := s.loadSanctionsExportRows(ctx, filter)
		if err != nil {
			http.Error(w, "could not export sanctions", http.StatusInternalServerError)
			return
		}

		now := time.Now()
		if s.LondonLoc != nil {
			now = now.In(s.LondonLoc)
		}
		filename := sanctionsExportFilename(filter, now)
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.Header().Set("Cache-Control", "no-store")

		cw := csv.NewWriter(w)
		_ = cw.Write(sanctionsExportHeader())
		for _, row := range rows {
			_ = cw.Write(sanctionsExportRecord(row, s.LondonLoc))
		}
		cw.Flush()
	}
}

func (s *Server) resolveSanctionsExportFilter(ctx context.Context, q url.Values) (sanctionsExportFilter, error) {
	weekFrom, weekTo, err := parseSanctionsExportWeekRange(q)
	if err != nil {
		return sanctionsExportFilter{}, err
	}

	seasonID := int32(parsePositiveInt(q.Get("season_id")))
	if seasonID == 0 {
		seasonID = s.defaultSanctionsExportSeasonID(ctx)
	}
	if seasonID == 0 {
		return sanctionsExportFilter{}, fmt.Errorf("season_id is required")
	}

	return sanctionsExportFilter{
		SeasonID: seasonID,
		WeekFrom: weekFrom,
		WeekTo:   weekTo,
	}, nil
}

func parseSanctionsExportWeekRange(q url.Values) (int32, int32, error) {
	weekFrom := int32(parsePositiveInt(q.Get("week_from")))
	weekTo := int32(parsePositiveInt(q.Get("week_to")))
	if weekFrom == 0 || weekTo == 0 {
		return 0, 0, fmt.Errorf("week_from and week_to are required")
	}
	if weekFrom > weekTo {
		return 0, 0, fmt.Errorf("week_from must be before or equal to week_to")
	}
	return weekFrom, weekTo, nil
}

func (s *Server) defaultSanctionsExportSeasonID(ctx context.Context) int32 {
	var seasonID int32
	if err := s.DB.QueryRow(ctx, `
		SELECT s.id
		FROM weeks w
		JOIN seasons s ON w.season_id = s.id
		WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date
		ORDER BY w.start_date DESC
		LIMIT 1
	`).Scan(&seasonID); err == nil && seasonID > 0 {
		return seasonID
	}
	_ = s.DB.QueryRow(ctx, `
		SELECT id
		FROM seasons
		WHERE is_archived = FALSE
		ORDER BY start_date DESC, id DESC
		LIMIT 1
	`).Scan(&seasonID)
	return seasonID
}

func (s *Server) loadSanctionsExportRows(ctx context.Context, filter sanctionsExportFilter) ([]sanctionsExportRow, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT sa.id,
		       se.name,
		       w.week_number,
		       cl.name,
		       t.name,
		       sa.colour::text,
		       sa.reason,
		       COALESCE(sa.notes, ''),
		       sa.status::text,
		       COALESCE(sa.email_status, 'pending'),
		       sa.points_deduction,
		       sa.issued_at,
		       COALESCE(issued.username, 'system'),
		       sa.resolved_at,
		       COALESCE(resolved.username, ''),
		       COALESCE(sa.email_approved_by, ''),
		       sa.email_approved_at,
		       sa.email_sent_at
		FROM sanctions sa
		JOIN seasons se ON se.id = sa.season_id
		JOIN weeks w ON w.id = sa.week_id
		JOIN clubs cl ON cl.id = sa.club_id
		JOIN teams t ON t.id = sa.team_id
		LEFT JOIN admin_users issued ON issued.id = sa.issued_by_admin_id
		LEFT JOIN admin_users resolved ON resolved.id = sa.resolved_by_admin_id
		WHERE sa.season_id = $1
		  AND w.week_number BETWEEN $2 AND $3
		  AND sa.status <> 'overturned'
		ORDER BY w.week_number, cl.name, t.name, sa.issued_at, sa.id
	`, filter.SeasonID, filter.WeekFrom, filter.WeekTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []sanctionsExportRow
	for rows.Next() {
		var row sanctionsExportRow
		if err := rows.Scan(
			&row.ID,
			&row.Season,
			&row.Week,
			&row.Club,
			&row.Team,
			&row.Colour,
			&row.Reason,
			&row.Notes,
			&row.Status,
			&row.EmailStatus,
			&row.PointsDeduction,
			&row.IssuedAt,
			&row.IssuedBy,
			&row.ResolvedAt,
			&row.ResolvedBy,
			&row.EmailApprovedBy,
			&row.EmailApprovedAt,
			&row.EmailSentAt,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func sanctionsExportHeader() []string {
	return []string{
		"Sanction ID",
		"Season",
		"Week",
		"Club",
		"Team",
		"Card",
		"Reason",
		"Notes",
		"Status",
		"Email",
		"Points deduction",
		"Issued",
		"By",
		"Resolved",
		"Resolved by",
		"Email approved by",
		"Email approved",
		"Email sent",
	}
}

func sanctionsExportRecord(row sanctionsExportRow, loc *time.Location) []string {
	points := ""
	if row.PointsDeduction.Valid {
		points = strconv.Itoa(int(row.PointsDeduction.Int32))
	}

	return []string{
		strconv.FormatInt(row.ID, 10),
		row.Season,
		strconv.Itoa(int(row.Week)),
		row.Club,
		row.Team,
		sanctionsExportCardLabel(row.Colour),
		reasonLabel(row.Reason),
		row.Notes,
		statusLabel(row.Status),
		sanctionsExportEmailStatusLabel(row.EmailStatus),
		points,
		formatSanctionsExportTime(row.IssuedAt, loc),
		row.IssuedBy,
		formatNullableSanctionsExportTime(row.ResolvedAt, loc),
		row.ResolvedBy,
		row.EmailApprovedBy,
		formatNullableSanctionsExportTime(row.EmailApprovedAt, loc),
		formatNullableSanctionsExportTime(row.EmailSentAt, loc),
	}
}

func sanctionsExportCardLabel(colour string) string {
	if colour == "red" {
		return "Red Card"
	}
	return "Yellow Card"
}

func sanctionsExportEmailStatusLabel(status string) string {
	switch status {
	case "approved":
		return "Approved"
	case "sent":
		return "Sent"
	case "skipped":
		return "Skipped"
	default:
		return "Pending"
	}
}

func formatSanctionsExportTime(t time.Time, loc *time.Location) string {
	if loc != nil {
		t = t.In(loc)
	}
	return t.Format("2006-01-02 15:04")
}

func formatNullableSanctionsExportTime(t sql.NullTime, loc *time.Location) string {
	if !t.Valid {
		return ""
	}
	return formatSanctionsExportTime(t.Time, loc)
}

func sanctionsExportFilename(filter sanctionsExportFilter, now time.Time) string {
	return fmt.Sprintf("gmcl-sanctions-season-%d-weeks-%02d-%02d-%s.csv",
		filter.SeasonID,
		filter.WeekFrom,
		filter.WeekTo,
		now.Format("20060102-150405"))
}
