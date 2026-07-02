package httpserver

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const adminSubmissionSearchLimit = 200

type adminSubmissionSearchRow struct {
	ID          int64
	Club        string
	Team        string
	Captain     string
	Week        int32
	MatchDate   time.Time
	SubmittedAt time.Time
}

func (s *Server) handleAdminSubmissionsExportCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		club := strings.TrimSpace(r.URL.Query().Get("club"))
		if club == "" {
			http.Error(w, "club is required", http.StatusBadRequest)
			return
		}

		rows, err := s.loadAdminSubmissionSearchRows(ctx, club, adminSubmissionSearchLimit)
		if err != nil {
			http.Error(w, "could not export submissions", http.StatusInternalServerError)
			return
		}

		now := time.Now()
		if s.LondonLoc != nil {
			now = now.In(s.LondonLoc)
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+adminSubmissionsExportFilename(club, now)+`"`)
		w.Header().Set("Cache-Control", "no-store")

		cw := csv.NewWriter(w)
		_ = cw.Write(adminSubmissionsExportHeader())
		for _, row := range rows {
			_ = cw.Write(adminSubmissionsExportRecord(row))
		}
		cw.Flush()
	}
}

func (s *Server) loadAdminSubmissionSearchRows(ctx context.Context, club string, limit int) ([]adminSubmissionSearchRow, error) {
	club = strings.TrimSpace(club)
	if club == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = adminSubmissionSearchLimit
	}

	rows, err := s.DB.Query(ctx, `
		SELECT sub.id, cl.name, t.name, COALESCE(c.full_name, ''),
		       w.week_number, sub.match_date, sub.submitted_at
		FROM submissions sub
		JOIN teams t     ON t.id = sub.team_id
		JOIN clubs cl    ON cl.id = t.club_id
		JOIN captains c  ON c.id = sub.captain_id
		JOIN weeks w     ON w.id = sub.week_id
		WHERE cl.name ILIKE $1
		ORDER BY sub.submitted_at DESC
		LIMIT $2
	`, "%"+club+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []adminSubmissionSearchRow
	for rows.Next() {
		var row adminSubmissionSearchRow
		if err := rows.Scan(&row.ID, &row.Club, &row.Team, &row.Captain, &row.Week, &row.MatchDate, &row.SubmittedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func adminSubmissionsExportButton(club string) string {
	club = strings.TrimSpace(club)
	if club == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="col-auto"><a class="btn btn-outline-primary" href="/admin/submissions/export.csv?club=%s">Export CSV</a></div>`, url.QueryEscape(club))
}

func adminSubmissionsExportHeader() []string {
	return []string{
		"Submission ID",
		"Club",
		"Team",
		"Captain",
		"Week",
		"Match date",
		"Submitted",
	}
}

func adminSubmissionsExportRecord(row adminSubmissionSearchRow) []string {
	return []string{
		strconv.FormatInt(row.ID, 10),
		row.Club,
		row.Team,
		row.Captain,
		strconv.Itoa(int(row.Week)),
		row.MatchDate.Format("2006-01-02"),
		row.SubmittedAt.Format("2006-01-02 15:04"),
	}
}

func adminSubmissionsExportFilename(club string, now time.Time) string {
	return fmt.Sprintf("gmcl-submissions-%s-%s.csv",
		adminSubmissionsExportSlug(club),
		now.Format("20060102-150405"))
}

func adminSubmissionsExportSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "club"
	}
	return slug
}
