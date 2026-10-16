package httpserver

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	sanctiondomain "cricket-ground-feedback/internal/sanctions"

	"github.com/jackc/pgx/v5"
)

const ineligibleCasePageSize = 50

type ineligibleCaseFilter struct {
	View, Scope, Group, Query, FixtureFrom, FixtureTo, CaseStatus string
	Training                                                      bool
	Page                                                          int
}

type ineligibleCaseAccess struct {
	AdminID                                                     int32
	CanInvestigate, CanPropose, CanApprove, CanIssue, CanAppeal bool
	CanReviewSource                                             bool
}

func (access ineligibleCaseAccess) canOpenCase() bool {
	// General approval permission permits case reads even when this viewer is
	// not a designated approver for ineligible-player decisions.
	return access.CanInvestigate || access.CanApprove || access.CanReviewSource
}

type ineligibleCaseWorkRow struct {
	ID, OpenTasks                                        int64
	Reference, Status, Player, Club, Team, Season, Owner string
	UpdatedAt                                            time.Time
	MatchDate, ReplyAt                                   *time.Time
	IsOwner, HasProposed                                 bool
	OwnerReviewRequired, SentForApproval                 bool
	ReplyUnreviewed, DeliveryException                   bool
	SourceReviewRequired                                 bool
	RequestStatus                                        string
	DeliveredAt, ReminderDueAt, DueAt                    *time.Time
}

func parseIneligibleCaseFilter(values url.Values) ineligibleCaseFilter {
	filter := ineligibleCaseFilter{
		View: values.Get("view"), Scope: values.Get("scope"), Group: values.Get("group"),
		Query: strings.TrimSpace(values.Get("q")), CaseStatus: strings.TrimSpace(values.Get("case_status")),
		FixtureFrom: parseIneligibleFixtureDateFilter(values.Get("fixture_from")),
		FixtureTo:   parseIneligibleFixtureDateFilter(values.Get("fixture_to")),
		Training:    values.Get("training") == "1", Page: 1,
	}
	if filter.View != "history" {
		filter.View = "cases"
	}
	if filter.Scope != "mine" && filter.Scope != "action" {
		filter.Scope = "all"
	}
	if filter.Group == "" && values.Get("reply_status") == "unreviewed" {
		filter.Group = "new_replies"
	}
	switch filter.Group {
	case "", "live", "investigating", "new_replies", "owner_review", "awaiting_decision", "awaiting_denver", "responses_due", "responses_overdue", "delivery_exceptions", "closed":
	default:
		filter.Group = ""
	}
	switch filter.CaseStatus {
	case "", "submitted", "triage", "investigating", "response_pending", "decision_proposed", "approved", "published", "appealed", "closed", "rejected", "withdrawn":
	default:
		filter.CaseStatus = ""
	}
	if page, err := strconv.Atoi(values.Get("page")); err == nil && page > 0 && page <= 1000000 {
		filter.Page = page
	}
	if filter.FixtureFrom != "" && filter.FixtureTo != "" && filter.FixtureFrom > filter.FixtureTo {
		filter.FixtureFrom, filter.FixtureTo = filter.FixtureTo, filter.FixtureFrom
	}
	return filter
}

func ineligibleCaseFilterURL(filter ineligibleCaseFilter, page int) string {
	values := url.Values{"view": {filter.View}, "scope": {filter.Scope}}
	for key, value := range map[string]string{
		"group": filter.Group, "q": filter.Query, "case_status": filter.CaseStatus,
		"fixture_from": filter.FixtureFrom, "fixture_to": filter.FixtureTo,
	} {
		if value != "" {
			values.Set(key, value)
		}
	}
	if filter.Training {
		values.Set("training", "1")
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	return "/admin/ineligible?" + values.Encode()
}

// Start at cases, never intake links: manual and split/merged casework retains
// exactly one navigable row per case.
var ineligibleCaseWorkCTE = `
WITH viewer AS (
	SELECT $1::integer AS id,$2::boolean AS can_investigate,$3::boolean AS can_propose,
	       $4::boolean AS can_approve,$5::boolean AS can_issue,$6::boolean AS can_appeal,$7::boolean AS can_review_source
), case_work AS (
	SELECT c.id,c.reference,c.status,COALESCE(c.player_name,'') AS player,
	       COALESCE(cl.name,'') AS club,COALESCE(t.name,'') AS team,COALESCE(season.name,'') AS season,
	       COALESCE(owner.username,'') AS owner,c.updated_at,c.match_date,
	       (` + sanctiondomain.ApprovedIneligibleSourceReviewPredicate() + `) AS source_review_required,
	       COALESCE(c.assigned_admin_id=viewer.id,FALSE) AS is_owner,
	       EXISTS(SELECT 1 FROM sanction_decision_revisions proposed
	              WHERE proposed.case_id=c.id AND proposed.status='proposed'
	              AND NOT EXISTS(SELECT 1 FROM sanction_decision_revisions newer WHERE newer.supersedes_id=proposed.id)) AS has_proposed,
	       EXISTS(SELECT 1 FROM sanction_case_events required
	              WHERE required.case_id=c.id AND required.event_type='decision_owner_review_required'
	              AND required.metadata->>'decision_revision_id'=decision.id::text) AS owner_review_required,
	       EXISTS(SELECT 1 FROM sanction_case_events sent
	              WHERE sent.case_id=c.id AND sent.event_type='decision_sent_for_approval'
	              AND sent.metadata->>'decision_revision_id'=decision.id::text) AS sent_for_approval,
	       COALESCE(reply.needs_review,FALSE) AS reply_unreviewed,reply.created_at AS reply_at,
	       COALESCE(request.status,'') AS request_status,request.delivered_at,request.reminder_due_at,request.due_at,
	       EXISTS(SELECT 1 FROM sanction_notification_outbox outbox
	              JOIN LATERAL (SELECT attempt.status FROM sanction_notification_attempts attempt
	                WHERE attempt.outbox_id=outbox.id ORDER BY attempt.attempt_number DESC,attempt.id DESC LIMIT 1) attempt ON TRUE
	              WHERE outbox.case_id=c.id AND outbox.revoked_at IS NULL
	                AND attempt.status IN ('failed','bounced','complained')) AS delivery_exception,
	       (SELECT COUNT(*) FROM sanction_follow_up_tasks task
	        WHERE task.case_id=c.id AND task.status IN ('open','in_progress')) AS open_tasks,
	       (c.is_test OR EXISTS(SELECT 1 FROM sanction_case_events training
	        WHERE training.case_id=c.id AND training.event_type='case_training_designated')) AS is_training,
	       viewer.can_investigate,viewer.can_propose,viewer.can_approve,viewer.can_issue,viewer.can_appeal,viewer.can_review_source
	FROM sanction_cases c
	CROSS JOIN viewer
	LEFT JOIN clubs cl ON cl.id=c.club_id
	LEFT JOIN teams t ON t.id=c.team_id
	LEFT JOIN seasons season ON season.id=c.season_id
	LEFT JOIN admin_users owner ON owner.id=c.assigned_admin_id
	LEFT JOIN LATERAL (
		SELECT d.id FROM sanction_decision_revisions d
		WHERE d.case_id=c.id AND d.status='proposed'
		ORDER BY d.revision DESC,d.id DESC LIMIT 1
	) decision ON TRUE
	LEFT JOIN LATERAL (
		SELECT response.id,response.created_at,
		       NOT EXISTS(SELECT 1 FROM sanction_case_events reviewed
		                  WHERE reviewed.case_id=c.id AND reviewed.event_type='response_reviewed'
		                  AND reviewed.metadata->>'response_event_id'=response.id::text) AS needs_review
		FROM sanction_case_events response
		WHERE response.case_id=c.id AND response.event_type IN ('party_response','external_response_recorded')
		ORDER BY response.id DESC LIMIT 1
	) reply ON TRUE
	LEFT JOIN LATERAL (
		SELECT rr.status,rr.delivered_at,rr.reminder_due_at,rr.due_at
		FROM sanction_response_requests rr WHERE rr.case_id=c.id ORDER BY rr.id DESC LIMIT 1
	) request ON TRUE
	WHERE c.source_type='ineligible_player'
)
`

const ineligibleCaseNeedsActionSQL = `(
	(w.status='approved' AND ((w.source_review_required AND w.can_review_source) OR (NOT w.source_review_required AND w.can_issue)))
	OR (w.status='decision_proposed' AND (
		(w.owner_review_required AND NOT w.sent_for_approval AND w.is_owner AND w.can_propose)
		OR ((NOT w.owner_review_required OR w.sent_for_approval) AND w.can_approve)
	))
	OR (w.status='appealed' AND w.can_appeal)
	OR (w.status NOT IN ('decision_proposed','approved','published','closed','rejected','withdrawn','appealed')
	    AND w.is_owner AND (
	        ((w.reply_unreviewed OR w.delivery_exception) AND w.can_investigate)
	        OR (NOT w.reply_unreviewed AND NOT w.delivery_exception AND w.request_status<>'expired'
	            AND w.status<>'response_pending' AND (w.can_investigate OR w.can_propose))
	        OR (w.can_investigate AND (w.request_status='expired'
	            OR (w.status='response_pending' AND w.request_status='pending' AND (w.due_at IS NULL OR w.due_at<=now()))))
	    ))
)`

func buildIneligibleCaseQuery(filter ineligibleCaseFilter, access ineligibleCaseAccess, count bool) (string, []any) {
	args := []any{access.AdminID, access.CanInvestigate, access.CanPropose, access.CanApprove, access.CanIssue, access.CanAppeal, access.CanReviewSource}
	where := []string{"NOT w.is_training"}
	if filter.Training {
		where[0] = "w.is_training"
	}
	if filter.CaseStatus != "" {
		args = append(args, filter.CaseStatus)
		where = append(where, fmt.Sprintf("w.status=$%d", len(args)))
	} else if filter.Group == "delivery_exceptions" {
		// Delivery remains actionable after publication or closure.
	} else if filter.View == "history" || filter.Group == "closed" {
		where = append(where, "w.status IN ('published','closed','rejected','withdrawn')")
	} else {
		where = append(where, "w.status NOT IN ('published','closed','rejected','withdrawn')")
	}
	switch filter.Scope {
	case "mine":
		where = append(where, "w.is_owner")
	case "action":
		where = append(where, ineligibleCaseNeedsActionSQL)
	}
	switch filter.Group {
	case "investigating":
		where = append(where, "w.status='investigating'")
	case "new_replies":
		where = append(where, "w.reply_unreviewed AND w.status NOT IN ('published','closed','rejected','withdrawn')")
	case "owner_review":
		where = append(where, "w.status='decision_proposed' AND w.owner_review_required AND NOT w.sent_for_approval")
	case "awaiting_decision":
		where = append(where, "w.status='decision_proposed' AND (NOT w.owner_review_required OR w.sent_for_approval)")
	case "awaiting_denver":
		where = append(where, "w.status='approved' AND NOT w.source_review_required")
	case "responses_due":
		where = append(where, "w.status='response_pending' AND w.request_status='pending' AND w.due_at>now()")
	case "responses_overdue":
		where = append(where, "((w.status='response_pending' AND w.request_status='pending' AND w.due_at<=now()) OR (w.status='investigating' AND w.request_status='expired'))")
	case "delivery_exceptions":
		where = append(where, "w.delivery_exception")
	case "live":
		where = append(where, "w.status NOT IN ('published','closed','rejected','withdrawn')")
	}
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		where = append(where, fmt.Sprintf("(w.reference ILIKE $%d OR w.player ILIKE $%d OR w.club ILIKE $%d OR w.team ILIKE $%d OR w.owner ILIKE $%d)", len(args), len(args), len(args), len(args), len(args)))
	}
	for _, item := range []struct{ value, operator string }{{filter.FixtureFrom, ">="}, {filter.FixtureTo, "<="}} {
		if item.value != "" {
			args = append(args, item.value)
			where = append(where, fmt.Sprintf("w.match_date%s$%d::date", item.operator, len(args)))
		}
	}
	base := ineligibleCaseWorkCTE + " SELECT "
	if count {
		return base + "COUNT(*) FROM case_work w WHERE " + strings.Join(where, " AND "), args
	}
	page := filter.Page
	if page < 1 {
		page = 1
	}
	args = append(args, ineligibleCasePageSize, (page-1)*ineligibleCasePageSize)
	return base + `w.id,w.reference,w.status,w.player,w.club,w.team,w.season,w.owner,w.updated_at,w.match_date,
		w.is_owner,w.has_proposed,w.owner_review_required,w.sent_for_approval,w.reply_unreviewed,w.reply_at,
		w.request_status,w.delivered_at,w.reminder_due_at,w.due_at,w.delivery_exception,w.open_tasks,w.source_review_required
		FROM case_work w WHERE ` + strings.Join(where, " AND ") +
		fmt.Sprintf(" ORDER BY w.updated_at DESC,w.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args
}

func (s *Server) ineligibleCaseAccessForAdmin(ctx context.Context, adminID int32) (ineligibleCaseAccess, error) {
	access := ineligibleCaseAccess{AdminID: adminID}
	access.CanInvestigate = s.adminHasPermission(ctx, adminID, "sanctions_investigate")
	access.CanPropose = s.adminHasPermission(ctx, adminID, "sanctions_propose")
	access.CanAppeal = s.adminHasPermission(ctx, adminID, "sanctions_appeals")
	access.CanReviewSource = access.CanInvestigate || s.adminHasPermission(ctx, adminID, "sanctions_approve")
	if err := s.DB.QueryRow(ctx, `SELECT sanction_ineligible_decision_approver($1),sanction_ineligible_final_issuer($1)`, adminID).
		Scan(&access.CanApprove, &access.CanIssue); err != nil {
		return access, err
	}
	return access, nil
}

func (s *Server) loadIneligibleCaseWork(ctx context.Context, filter ineligibleCaseFilter, access ineligibleCaseAccess) ([]ineligibleCaseWorkRow, int64, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	query, args := buildIneligibleCaseQuery(filter, access, true)
	var total int64
	if err := tx.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query, args = buildIneligibleCaseQuery(filter, access, false)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	items := []ineligibleCaseWorkRow{}
	for rows.Next() {
		var row ineligibleCaseWorkRow
		if err := rows.Scan(&row.ID, &row.Reference, &row.Status, &row.Player, &row.Club, &row.Team, &row.Season, &row.Owner,
			&row.UpdatedAt, &row.MatchDate, &row.IsOwner, &row.HasProposed, &row.OwnerReviewRequired, &row.SentForApproval,
			&row.ReplyUnreviewed, &row.ReplyAt, &row.RequestStatus, &row.DeliveredAt, &row.ReminderDueAt, &row.DueAt,
			&row.DeliveryException, &row.OpenTasks, &row.SourceReviewRequired); err != nil {
			rows.Close()
			return nil, 0, err
		}
		items = append(items, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (row ineligibleCaseWorkRow) actionContext(access ineligibleCaseAccess) adminCaseActionContext {
	return adminCaseActionContext{
		Source: "ineligible_player", Status: row.Status, OwnerName: row.Owner,
		IsOwner: row.IsOwner, HasProposed: row.HasProposed, OwnerReviewRequired: row.OwnerReviewRequired,
		SentForApproval: row.SentForApproval, ReplyUnreviewed: row.ReplyUnreviewed,
		SourceReviewRequired: row.SourceReviewRequired, CanReviewSource: access.CanReviewSource,
		DeliveryException:  row.DeliveryException,
		HasResponseRequest: row.RequestStatus != "",
		CanInvestigate:     access.CanInvestigate, CanPropose: access.CanPropose,
		CanApprove: access.CanApprove, CanIssue: access.CanIssue, CanAppeal: access.CanAppeal,
		ResponseWindow: adminCaseResponseWindowView{Status: row.RequestStatus, DeliveredAt: row.DeliveredAt, ReminderDueAt: row.ReminderDueAt, DueAt: row.DueAt},
	}
}
