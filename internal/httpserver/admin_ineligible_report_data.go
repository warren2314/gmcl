package httpserver

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func buildIneligibleReportQuery(filter ineligibleQueueFilters, adminID *int32, page int, count bool) (string, []any) {
	query, args := buildIneligibleQueueQueryForAdmin(filter, adminID)
	// The existing query keeps one report row and its representative link.
	// Remove only its legacy cap, preserving all eligibility/filter predicates.
	query = strings.TrimSuffix(query, "\n\t\tLIMIT 2000")
	if count {
		return "SELECT COUNT(*) FROM (" + query + ") matching_reports", args
	}
	if page < 1 {
		page = 1
	}
	args = append(args, ineligibleCasePageSize, (page-1)*ineligibleCasePageSize)
	return query + fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args
}

func (s *Server) loadIneligibleReportQueue(ctx context.Context, filter ineligibleQueueFilters, adminID *int32, page int) ([]ineligibleQueueRow, int64, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	query, args := buildIneligibleReportQuery(filter, adminID, page, true)
	var total int64
	if err := tx.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query, args = buildIneligibleReportQuery(filter, adminID, page, false)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	queue := []ineligibleQueueRow{}
	for rows.Next() {
		var row ineligibleQueueRow
		if err := rows.Scan(&row.ID, &row.Origin, &row.ExternalKey, &row.State, &row.IsTraining, &row.ReportingClubText,
			&row.OffendingClubText, &row.TeamText, &row.PlayerText, &row.FixtureDate, &row.ExternalCreatedAt,
			&row.CaseID, &row.CaseReference, &row.CaseStatus, &row.Assignee, &row.WorklistVisibility, &row.WorklistBatchID,
			&row.AttachmentCount, &row.ReplyCount, &row.LatestReplyAt, &row.ReplyNeedsReview); err != nil {
			rows.Close()
			return nil, 0, err
		}
		queue = append(queue, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return queue, total, nil
}

func ineligibleReportPageURL(values url.Values, page int) string {
	copy := url.Values{}
	for key, items := range values {
		copy[key] = append([]string(nil), items...)
	}
	copy.Set("view", "reports")
	copy.Del("page")
	if page > 1 {
		copy.Set("page", strconv.Itoa(page))
	}
	return "/admin/ineligible?" + copy.Encode()
}

func writeIneligibleReportLeagueSummary(w io.Writer, counts ineligibleDashboardCounts, err error) {
	fmt.Fprint(w, `<section class="mb-3"><h2 class="h6">League totals · live records · all seasons</h2>`)
	if err != nil {
		fmt.Fprint(w, `<p class="text-warning-emphasis mb-0"><strong>League totals unavailable.</strong> The report list below remains usable.</p></section>`)
		return
	}
	fmt.Fprintf(w, `<div class="d-flex flex-wrap gap-3 small"><a href="/admin/ineligible?view=cases">%d active cases</a><a href="/admin/ineligible?view=reports&amp;state=open&amp;worklist=visible">%d reports in the visible queue</a><a href="/admin/ineligible?view=reports&amp;state=open&amp;worklist=deferred">%d deferred reports</a></div><p class="small text-muted mb-0 mt-1">These league totals are separate from the filtered report count.</p></section>`, counts.LiveCases, counts.NewIntakes, counts.HiddenReports)
}
