package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
	sanctiondomain "cricket-ground-feedback/internal/sanctions"
	"github.com/go-chi/chi/v5"
)

type personalWorkCase struct {
	ID        int64
	Reference string
	Status    string
	Player    string
	Club      string
	Team      string
	UpdatedAt time.Time
}

type personalWorkTask struct {
	ID        int64
	CaseID    int64
	Reference string
	Note      string
	Status    string
	DueAt     *time.Time
	NotBefore *time.Time
}

type personalWorkResponse struct {
	CaseID     int64
	Reference  string
	Player     string
	Club       string
	ReceivedAt time.Time
}

type personalWorkQueueItem struct {
	CaseID    int64
	Reference string
	Action    string
	Club      string
	Player    string
	UpdatedAt time.Time
}

type personalWorkDashboard struct {
	AdminName      string
	AssignedCases  []personalWorkCase
	AssignedTotal  int64
	Responses      []personalWorkResponse
	ResponseTotal  int64
	Tasks          []personalWorkTask
	TaskTotal      int64
	DecisionQueue  []personalWorkQueueItem
	DecisionTotal  int64
	TestCases      []personalWorkQueueItem
	TestTotal      int64
	CanApprove     bool
	CanPublish     bool
	CanInvestigate bool
	CanTriage      bool
	ExpandedWork   string
}

const personalAssignedIneligibleCasesQuery = `SELECT cases.id,cases.reference,cases.status,
	COALESCE(cases.player_name,''),COALESCE(club.name,''),COALESCE(team.name,''),
	cases.updated_at,COUNT(*) OVER() ` + personalIneligibleCasesFromSQL + `
	ORDER BY cases.updated_at DESC,cases.id DESC LIMIT $2`

// A case outcome does not complete its remaining tasks. Match the task page's
// mine=1&live=1 population, including future-effective work and completed cases.
const personalOpenTasksQuery = `SELECT task.id,cases.id,cases.reference,COALESCE(task.current_note,''),
	task.status,task.due_at,task.not_before,COUNT(*) OVER()
	FROM sanction_follow_up_tasks task
	JOIN sanction_cases cases ON cases.id=task.case_id
	WHERE task.assigned_admin_id=$1 AND task.status IN ('open','in_progress')
	  AND NOT cases.is_test
	  AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=cases.id AND event.event_type='case_training_designated')
	ORDER BY task.due_at NULLS LAST,task.id LIMIT $2`

// The domain predicate deliberately fixes alias c. Correlate it to this
// module's cases alias rather than allowing another case's stale source to
// make every approved case appear blocked.
func personalApprovedSourceReviewSQL() string {
	return `EXISTS(SELECT 1 FROM sanction_cases c WHERE c.id=cases.id AND ` + sanctiondomain.ApprovedIneligibleSourceReviewPredicate() + `)`
}

func personalIssueReadySQL() string {
	return `(cases.status='approved' AND NOT (` + personalApprovedSourceReviewSQL() + `))`
}

func personalDecisionWorkAction(status string, sourceReview bool) string {
	if sourceReview {
		return "Review changed source evidence"
	}
	if status == "decision_proposed" {
		return "Review decision"
	}
	return "Issue approved outcome"
}

const personalResponsesAwaitingReviewQuery = `SELECT cases.id,cases.reference,COALESCE(cases.player_name,''),
	COALESCE(club.name,''),response.created_at,COUNT(*) OVER()
	FROM sanction_cases cases
	LEFT JOIN clubs club ON club.id=cases.club_id
	JOIN LATERAL (
		SELECT event.id,event.created_at
		FROM sanction_case_events event
		WHERE event.case_id=cases.id
		  AND event.event_type IN ('party_response','external_response_recorded')
		ORDER BY event.id DESC LIMIT 1
	) response ON TRUE
	WHERE cases.assigned_admin_id=$1 AND NOT cases.is_test
	  AND cases.status NOT IN ('published','closed','rejected','withdrawn')
	  AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=cases.id AND event.event_type='case_training_designated')
	  AND NOT EXISTS(
		SELECT 1 FROM sanction_case_events reviewed
		WHERE reviewed.case_id=cases.id AND reviewed.event_type='response_reviewed'
		  AND reviewed.metadata->>'response_event_id'=response.id::text
	  )
	ORDER BY response.created_at DESC,cases.id DESC LIMIT $2`

func (s *Server) loadPersonalWorkDashboard(ctx context.Context, adminID int32, adminName string) (personalWorkDashboard, error) {
	return s.loadPersonalWorkDashboardWithLimit(ctx, adminID, adminName, 6)
}

func (s *Server) loadPersonalWorkDashboardWithLimit(ctx context.Context, adminID int32, adminName string, itemLimit int) (personalWorkDashboard, error) {
	if itemLimit < 1 {
		itemLimit = 6
	} else if itemLimit > 300 {
		itemLimit = 300
	}
	data := personalWorkDashboard{AdminName: strings.TrimSpace(adminName)}
	var role string
	if err := s.DB.QueryRow(ctx, `SELECT COALESCE(role,'admin'),
		COALESCE(role,'admin')='super_admin' OR EXISTS(
			SELECT 1 FROM admin_user_permissions permission
			WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_approve'
		),
		COALESCE(role,'admin')='super_admin' OR EXISTS(
			SELECT 1 FROM admin_user_permissions permission
			WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_publish'
		),
		COALESCE(role,'admin')='super_admin' OR EXISTS(
			SELECT 1 FROM admin_user_permissions permission
			WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_investigate'
		),
		COALESCE(role,'admin')='super_admin' OR EXISTS(
			SELECT 1 FROM admin_user_permissions permission
			WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_triage'
		)
		FROM admin_users admin WHERE admin.id=$1`, adminID).Scan(&role, &data.CanApprove, &data.CanPublish, &data.CanInvestigate, &data.CanTriage); err != nil {
		return data, err
	}

	caseRows, err := s.DB.Query(ctx, personalAssignedIneligibleCasesQuery, adminID, itemLimit)
	if err != nil {
		return data, err
	}
	for caseRows.Next() {
		var item personalWorkCase
		if err = caseRows.Scan(&item.ID, &item.Reference, &item.Status, &item.Player, &item.Club, &item.Team, &item.UpdatedAt, &data.AssignedTotal); err != nil {
			caseRows.Close()
			return data, err
		}
		data.AssignedCases = append(data.AssignedCases, item)
	}
	if err = caseRows.Err(); err != nil {
		caseRows.Close()
		return data, err
	}
	caseRows.Close()

	responseRows, err := s.DB.Query(ctx, personalResponsesAwaitingReviewQuery, adminID, itemLimit)
	if err != nil {
		return data, err
	}
	for responseRows.Next() {
		var item personalWorkResponse
		if err = responseRows.Scan(&item.CaseID, &item.Reference, &item.Player, &item.Club, &item.ReceivedAt, &data.ResponseTotal); err != nil {
			responseRows.Close()
			return data, err
		}
		data.Responses = append(data.Responses, item)
	}
	if err = responseRows.Err(); err != nil {
		responseRows.Close()
		return data, err
	}
	responseRows.Close()

	taskRows, err := s.DB.Query(ctx, personalOpenTasksQuery, adminID, itemLimit)
	if err != nil {
		return data, err
	}
	for taskRows.Next() {
		var item personalWorkTask
		if err = taskRows.Scan(&item.ID, &item.CaseID, &item.Reference, &item.Note, &item.Status, &item.DueAt, &item.NotBefore, &data.TaskTotal); err != nil {
			taskRows.Close()
			return data, err
		}
		data.Tasks = append(data.Tasks, item)
	}
	if err = taskRows.Err(); err != nil {
		taskRows.Close()
		return data, err
	}
	taskRows.Close()

	queueClauses := make([]string, 0, 2)
	if data.CanApprove {
		queueClauses = append(queueClauses, "(cases.status='decision_proposed' AND (cases.source_type<>'ineligible_player' OR sanction_ineligible_decision_approver($1)) AND (cases.source_type='ineligible_player' OR cases.proposed_by_admin_id IS DISTINCT FROM $1) AND NOT EXISTS(SELECT 1 FROM sanction_case_events required WHERE required.case_id=cases.id AND required.event_type='decision_owner_review_required' AND required.metadata->>'decision_revision_id'=(SELECT id::text FROM sanction_decision_revisions WHERE case_id=cases.id AND status='proposed' ORDER BY revision DESC,id DESC LIMIT 1) AND NOT EXISTS(SELECT 1 FROM sanction_case_events sent WHERE sent.case_id=required.case_id AND sent.event_type='decision_sent_for_approval' AND sent.metadata->>'decision_revision_id'=required.metadata->>'decision_revision_id')))")
	}
	if data.CanPublish {
		queueClauses = append(queueClauses, "("+personalIssueReadySQL()+" AND $1::integer IS NOT NULL AND (cases.source_type<>'ineligible_player' OR sanction_ineligible_final_issuer($1)))")
	}
	if data.CanInvestigate || data.CanApprove {
		queueClauses = append(queueClauses, personalApprovedSourceReviewSQL())
	}
	if len(queueClauses) > 0 {
		queueRows, queryErr := s.DB.Query(ctx, `SELECT cases.id,cases.reference,cases.status,
			COALESCE(club.name,''),COALESCE(cases.player_name,''),cases.updated_at,`+personalApprovedSourceReviewSQL()+`,COUNT(*) OVER()
			FROM sanction_cases cases LEFT JOIN clubs club ON club.id=cases.club_id
			WHERE $1::integer IS NOT NULL AND NOT cases.is_test
			  AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=cases.id AND event.event_type='case_training_designated')
			  AND (`+strings.Join(queueClauses, " OR ")+`)
			ORDER BY CASE cases.status WHEN 'decision_proposed' THEN 0 ELSE 1 END,
			         cases.updated_at,cases.id LIMIT $2`, adminID, itemLimit)
		if queryErr != nil {
			return data, queryErr
		}
		for queueRows.Next() {
			var item personalWorkQueueItem
			var status string
			var sourceReview bool
			if err = queueRows.Scan(&item.CaseID, &item.Reference, &status, &item.Club, &item.Player, &item.UpdatedAt, &sourceReview, &data.DecisionTotal); err != nil {
				queueRows.Close()
				return data, err
			}
			item.Action = personalDecisionWorkAction(status, sourceReview)
			data.DecisionQueue = append(data.DecisionQueue, item)
		}
		if err = queueRows.Err(); err != nil {
			queueRows.Close()
			return data, err
		}
		queueRows.Close()
	}
	testClauses := []string{"(cases.assigned_admin_id=$1 AND cases.status NOT IN ('published','closed','rejected','withdrawn'))"}
	testClauses = append(testClauses, queueClauses...)
	testRows, err := s.DB.Query(ctx, `SELECT cases.id,cases.reference,cases.status,
		COALESCE(club.name,''),COALESCE(cases.player_name,''),cases.updated_at,
		cases.assigned_admin_id=$1,`+personalApprovedSourceReviewSQL()+`,COUNT(*) OVER()
		FROM sanction_cases cases LEFT JOIN clubs club ON club.id=cases.club_id
		WHERE (cases.is_test OR EXISTS(
			SELECT 1 FROM sanction_case_events event
			WHERE event.case_id=cases.id AND event.event_type='case_training_designated'
		)) AND (`+strings.Join(testClauses, " OR ")+`)
		ORDER BY cases.updated_at DESC,cases.id DESC LIMIT $2`, adminID, itemLimit)
	if err != nil {
		return data, err
	}
	for testRows.Next() {
		var item personalWorkQueueItem
		var status string
		var owned bool
		var sourceReview bool
		if err = testRows.Scan(&item.CaseID, &item.Reference, &status, &item.Club, &item.Player, &item.UpdatedAt, &owned, &sourceReview, &data.TestTotal); err != nil {
			testRows.Close()
			return data, err
		}
		if sourceReview {
			item.Action = personalDecisionWorkAction(status, true)
		} else if owned {
			item.Action = "Owned case - " + caseStatusLabel(status)
		} else if status == "decision_proposed" {
			item.Action = "Review decision"
		} else {
			item.Action = "Issue approved outcome"
		}
		data.TestCases = append(data.TestCases, item)
	}
	if err = testRows.Err(); err != nil {
		testRows.Close()
		return data, err
	}
	testRows.Close()
	return data, nil
}

func (s *Server) writeAdminPersonalWork(w http.ResponseWriter, r *http.Request) {
	actor := adminActor(r)
	if actor.ID == nil {
		return
	}
	expandedWork, itemLimit := personalHomeWorkMode(r.URL.Query().Get("work"))
	data, err := s.loadPersonalWorkDashboardWithLimit(r.Context(), *actor.ID, actor.Label, itemLimit)
	if err != nil {
		fmt.Fprint(w, `<section class="alert alert-warning mb-4"><strong>My work is temporarily unavailable.</strong> Use Ineligible-player work or Cases to continue.</section>`)
		return
	}
	data.ExpandedWork = expandedWork
	writePersonalWorkDashboard(w, data, s.LondonLoc, time.Now())
}

// Expanded work stays on the existing authenticated Home route. It uses the
// same authorised populations without granting access to the triage workspace.
func personalHomeWorkMode(value string) (string, int) {
	if value == "responses" || value == "decisions" {
		return value, 300
	}
	return "", 6
}

func (s *Server) handleAdminUserWorkPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 32)
		if err != nil || id < 1 {
			http.NotFound(w, r)
			return
		}
		var username string
		var active bool
		if err = s.DB.QueryRow(r.Context(), `SELECT username,is_active FROM admin_users WHERE id=$1`, id).Scan(&username, &active); err != nil {
			http.NotFound(w, r)
			return
		}
		data, err := s.loadPersonalWorkDashboardWithLimit(r.Context(), int32(id), username, 300)
		if err != nil {
			http.Error(w, "work preview unavailable", http.StatusInternalServerError)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Work preview for "+username)
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4"><div class="d-flex flex-wrap justify-content-between gap-2 mb-3"><div><h1 class="h2">Work preview for %s</h1><p class="text-muted mb-0">This is the personal sanctions work section generated for this administrator.</p></div><a class="btn btn-outline-secondary align-self-start" href="/admin/users">Back to users</a></div>`, escapeHTML(username))
		if !active {
			fmt.Fprint(w, `<div class="alert alert-warning">This administrator account is inactive.</div>`)
		}
		if !data.CanApprove {
			fmt.Fprintf(w, `<div class="alert alert-warning"><strong>Approval access missing.</strong> Decisions awaiting approval will not appear in this administrator's queue. A super administrator can review this under <a href="/admin/users/%d/sanctions-permissions">Sanctions access</a>.</div>`, id)
		}
		fmt.Fprint(w, `<div class="alert alert-info"><strong>Read-only preview.</strong> Nothing here logs you in as this person or changes their work. Links are disabled in preview mode.</div><div class="pe-none" aria-disabled="true">`)
		writePersonalWorkDashboard(w, data, s.LondonLoc, time.Now())
		fmt.Fprint(w, `</div></main>`)
		pageFooter(w)
	}
}

func writePersonalWorkDashboard(w http.ResponseWriter, data personalWorkDashboard, loc *time.Location, now time.Time) {
	if loc == nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	greeting := "Good evening"
	if localNow.Hour() < 12 {
		greeting = "Good morning"
	} else if localNow.Hour() < 18 {
		greeting = "Good afternoon"
	}
	name := strings.TrimSpace(data.AdminName)
	if name == "" {
		name = "administrator"
	}
	canReadCases := data.CanInvestigate || data.CanApprove
	canReadIndex := canReadCases || data.CanTriage
	assignedHref := ""
	if canReadIndex {
		assignedHref = "/admin/cases/mine/ineligible"
	}
	decisionCard := ""
	if data.CanApprove || data.CanPublish {
		decisionCard = personalWorkCountCard("Decision work", data.DecisionTotal, "#my-decisions", map[bool]string{true: "danger", false: "secondary"}[data.DecisionTotal > 0])
	}
	taskCard := ""
	if data.CanInvestigate {
		taskCard = personalWorkCountCard("Open tasks", data.TaskTotal, "/admin/cases/tasks?mine=1&live=1", map[bool]string{true: "warning", false: "secondary"}[data.TaskTotal > 0])
	}
	if data.CanInvestigate && !data.CanApprove && !data.CanPublish {
		decisionCard = personalWorkCountCard("Decision work", data.DecisionTotal, "#my-decisions", map[bool]string{true: "warning", false: "secondary"}[data.DecisionTotal > 0])
	}
	fmt.Fprintf(w, `<section class="card mb-4 admin-workspace" aria-labelledby="my-work-heading">
<div class="card-header d-flex flex-wrap justify-content-between align-items-center gap-2">
<div><h2 class="h5 mb-1" id="my-work-heading">My work</h2><div class="small text-muted">%s, %s. Your next actions across all seasons.</div></div>
<span class="badge text-bg-light border">Live work</span>
</div><div class="card-body">
<p class="small text-muted">Assigned cases below are ineligible-player cases. Replies, approvals and tasks cover all discipline sources. These counts can overlap; they are not a total of distinct cases.</p>
<div class="row g-2 mb-3">
%s%s%s%s
</div>`, escapeHTML(greeting), escapeHTML(name),
		personalWorkCountCard("My ineligible cases", data.AssignedTotal, assignedHref, "primary"),
		personalWorkCountCard("Responses to review", data.ResponseTotal, "#my-responses", map[bool]string{true: "danger", false: "secondary"}[data.ResponseTotal > 0]),
		decisionCard, taskCard)
	if !canReadCases && (data.AssignedTotal > 0 || data.ResponseTotal > 0 || data.DecisionTotal > 0 || data.TestTotal > 0) {
		fmt.Fprint(w, `<p class="small text-muted">Case stages are shown for context. Opening case details requires investigation or approval access; this page does not grant that access.</p>`)
	}
	if data.ExpandedWork != "" {
		label := "assigned responses"
		if data.ExpandedWork == "decisions" {
			label = "decision, issue and source-review work"
		}
		fmt.Fprintf(w, `<div class="alert alert-info py-2">Expanded %s · up to 300 items per section. Counts still show the full matching population. <a class="alert-link" href="/admin/dashboard">Return to the short Home view</a></div>`, escapeHTML(label))
	}

	fmt.Fprint(w, `<div class="row g-3">`)
	writePersonalResponseList(w, data.Responses, data.ResponseTotal, loc, canReadCases, data.ExpandedWork)
	if data.CanApprove || data.CanPublish || data.CanInvestigate {
		writePersonalDecisionList(w, data.DecisionQueue, data.DecisionTotal, canReadCases, data.ExpandedWork)
	}
	if data.CanInvestigate {
		writePersonalTaskList(w, data.Tasks, data.TaskTotal, loc, now)
	} else if data.TaskTotal > 0 {
		fmt.Fprintf(w, `<div class="col-12"><div class="alert alert-warning mb-0">%d open tasks are assigned to you, but your account does not have investigation access. Ask a league administrator to review task ownership or access.</div></div>`, data.TaskTotal)
	}
	fmt.Fprint(w, `</div><details class="mt-3"><summary class="fw-semibold">My assigned ineligible cases, including work waiting on others</summary><div class="row g-3 mt-1">`)
	writePersonalCaseList(w, data.AssignedCases, data.AssignedTotal, loc, canReadCases, canReadIndex, data.ExpandedWork)
	fmt.Fprint(w, `</div></details>`)
	fmt.Fprint(w, `</div></section>`)
	writePersonalTestCaseList(w, data.TestCases, data.TestTotal, canReadCases, data.ExpandedWork)
}

func personalHomeCaseURL(caseID int64, caseAnchor, homeAnchor string, workMode ...string) string {
	destination := "/admin/dashboard"
	if len(workMode) > 0 {
		if mode, _ := personalHomeWorkMode(workMode[0]); mode != "" {
			destination += "?work=" + mode
		}
	}
	destination += homeAnchor
	return fmt.Sprintf("/admin/cases/%d?return_to=%s%s", caseID, url.QueryEscape(destination), caseAnchor)
}

func personalWorkItemTags(canOpen bool, href string) (string, string) {
	if !canOpen {
		return `<div class="list-group-item">`, `</div>`
	}
	return `<a class="list-group-item list-group-item-action" href="` + escapeHTML(href) + `">`, `</a>`
}

func writePersonalTestCaseList(w http.ResponseWriter, items []personalWorkQueueItem, total int64, canReadCases bool, workMode ...string) {
	if total == 0 {
		return
	}
	fmt.Fprintf(w, `<details class="card mb-4 border-warning" id="test-cases"><summary class="card-header"><strong>Test cases - training only</strong> <span class="badge text-bg-warning">%d test</span></summary><div class="card-body"><p class="small text-muted">Excluded from live-work totals. Training cases can send real email; check recipients before using their workflow.</p><div class="list-group">`, total)
	for _, item := range items {
		start, end := personalWorkItemTags(canReadCases, personalHomeCaseURL(item.CaseID, "", "#test-cases", workMode...))
		fmt.Fprintf(w, `%s<div class="d-flex justify-content-between gap-2"><strong>Case %d - %s</strong><span class="badge text-bg-warning">%s</span></div><div class="small">%s - %s</div>%s`, start, item.CaseID, escapeHTML(item.Reference), escapeHTML(item.Action), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(item.Club), end)
	}
	if total > int64(len(items)) {
		fmt.Fprintf(w, `<div class="list-group-item text-muted">Showing %d of %d relevant test cases.</div>`, len(items), total)
	}
	fmt.Fprint(w, `</div></div></details>`)
}

func personalWorkCountCard(label string, count int64, href, colour string) string {
	if href == "" {
		return fmt.Sprintf(`<div class="col-6 col-lg-3"><div class="border rounded p-3 h-100"><span class="h3 d-block mb-0 text-%s">%d</span><span class="small text-body">%s</span></div></div>`, escapeHTML(colour), count, escapeHTML(label))
	}
	return fmt.Sprintf(`<div class="col-6 col-lg-3"><a class="border rounded p-3 d-block h-100 text-decoration-none" href="%s"><span class="h3 d-block mb-0 text-%s">%d</span><span class="small text-body">%s</span></a></div>`,
		escapeHTML(href), escapeHTML(colour), count, escapeHTML(label))
}

func writePersonalResponseList(w http.ResponseWriter, items []personalWorkResponse, total int64, loc *time.Location, canReadCases bool, workMode ...string) {
	fmt.Fprint(w, `<div class="col-12" id="my-responses"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Responses awaiting review</strong><div class="small text-muted">Cases assigned to you · all discipline sources</div></div><div class="list-group list-group-flush">`)
	if len(items) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted">No new responses assigned to you.</div>`)
	}
	for _, item := range items {
		start, end := personalWorkItemTags(canReadCases, personalHomeCaseURL(item.CaseID, "#club-response", "#my-responses", workMode...))
		fmt.Fprintf(w, `%s<div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-danger">Reply awaiting review</span></div><div class="small">%s</div><div class="small text-muted">%s · received %s</div>%s`,
			start, escapeHTML(item.Reference), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(item.Club), escapeHTML(item.ReceivedAt.In(loc).Format("02 Jan 15:04")), end)
	}
	if total > int64(len(items)) {
		fmt.Fprintf(w, `<div class="list-group-item text-muted">Showing %d of %d assigned responses.</div>`, len(items), total)
		if len(items) < 300 {
			fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/dashboard?work=responses#my-responses">Expand assigned responses</a>`)
		}
	}
	fmt.Fprint(w, `</div></div></div>`)
}

func writePersonalTaskList(w http.ResponseWriter, items []personalWorkTask, total int64, loc *time.Location, now time.Time) {
	fmt.Fprint(w, `<div class="col-12" id="my-tasks"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Tasks assigned to me</strong><div class="small text-muted">Open and in progress · includes completed cases and future-effective tasks</div></div><div class="list-group list-group-flush">`)
	if len(items) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted">No open supporting tasks assigned to you.</div>`)
	}
	for _, item := range items {
		due := "No due date"
		badge := "text-bg-secondary"
		if item.DueAt != nil {
			due = "Due " + item.DueAt.In(loc).Format("02 Jan 2006")
			if item.DueAt.Before(now) {
				due = "Overdue · " + due
				badge = "text-bg-danger"
			}
		}
		if item.NotBefore != nil && item.NotBefore.After(now) {
			due = "Available from " + item.NotBefore.In(loc).Format("02 Jan 2006")
			badge = "text-bg-info"
		}
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/tasks?mine=1&amp;live=1#task-%d"><div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge %s">%s</span></div><div class="small">%s</div></a>`,
			item.ID, escapeHTML(item.Reference), badge, escapeHTML(due), escapeHTML(defaultString(item.Note, "Open supporting task")))
	}
	if total > int64(len(items)) {
		fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/cases/tasks?mine=1&amp;live=1">View all my tasks</a>`)
	}
	fmt.Fprint(w, `</div></div></div>`)
}

func writePersonalCaseList(w http.ResponseWriter, items []personalWorkCase, total int64, loc *time.Location, canReadCases, canReadIndex bool, workMode ...string) {
	fmt.Fprint(w, `<div class="col-12" id="my-cases"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Ineligible cases I own</strong></div><div class="list-group list-group-flush">`)
	if len(items) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted">No active cases are assigned to you.</div>`)
	}
	for _, item := range items {
		start, end := personalWorkItemTags(canReadCases, personalHomeCaseURL(item.ID, "", "#my-cases", workMode...))
		fmt.Fprintf(w, `%s<div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-light border">%s</span></div><div class="small">%s</div><div class="small text-muted">%s · updated %s</div>%s`,
			start, escapeHTML(item.Reference), escapeHTML(caseStatusLabel(item.Status)), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(defaultString(item.Club, item.Team)), escapeHTML(item.UpdatedAt.In(loc).Format("02 Jan 15:04")), end)
	}
	if total > int64(len(items)) && canReadIndex {
		fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/cases/mine/ineligible">View all my ineligible cases</a>`)
	}
	fmt.Fprint(w, `</div></div></div>`)
}

func writePersonalDecisionList(w http.ResponseWriter, items []personalWorkQueueItem, total int64, canReadCases bool, workMode ...string) {
	fmt.Fprint(w, `<div class="col-12" id="my-decisions"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Decisions needing my role</strong><div class="small text-muted">Source review, decision approval and final issue follow their separate permissions.</div></div><div class="list-group list-group-flush">`)
	if len(items) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted">No decision, issue or changed-source review work currently needs your role.</div>`)
	}
	for _, item := range items {
		caseAnchor := ""
		if item.Action == "Review changed source evidence" {
			caseAnchor = "#source-change-review"
		}
		start, end := personalWorkItemTags(canReadCases, personalHomeCaseURL(item.CaseID, caseAnchor, "#my-decisions", workMode...))
		fmt.Fprintf(w, `%s<div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-danger">%s</span></div><div class="small">%s · %s</div>%s`,
			start, escapeHTML(item.Reference), escapeHTML(item.Action), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(item.Club), end)
	}
	if total > int64(len(items)) {
		fmt.Fprintf(w, `<div class="list-group-item text-muted">Showing %d of %d decision work items.</div>`, len(items), total)
		if len(items) < 300 {
			fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/dashboard?work=decisions#my-decisions">Expand decision work</a>`)
		}
	}
	fmt.Fprint(w, `</div></div></div>`)
}

func caseStatusLabel(status string) string {
	switch status {
	case "submitted":
		return "New"
	case "triage":
		return "Triage"
	case "investigating":
		return "Investigating"
	case "response_pending":
		return "Awaiting response"
	case "decision_proposed":
		return "Decision proposed"
	case "approved":
		return "Approved"
	default:
		return strings.ReplaceAll(status, "_", " ")
	}
}
