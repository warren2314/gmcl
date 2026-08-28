package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
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
	AdminName     string
	AssignedCases []personalWorkCase
	AssignedTotal int64
	Responses     []personalWorkResponse
	ResponseTotal int64
	Tasks         []personalWorkTask
	TaskTotal     int64
	DecisionQueue []personalWorkQueueItem
	DecisionTotal int64
	TestCases     []personalWorkQueueItem
	TestTotal     int64
	CanApprove    bool
	CanPublish    bool
}

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
		)
		FROM admin_users admin WHERE admin.id=$1`, adminID).Scan(&role, &data.CanApprove, &data.CanPublish); err != nil {
		return data, err
	}

	caseRows, err := s.DB.Query(ctx, `SELECT cases.id,cases.reference,cases.status,
		COALESCE(cases.player_name,''),COALESCE(club.name,''),COALESCE(team.name,''),
		cases.updated_at,COUNT(*) OVER()
		FROM sanction_cases cases
		LEFT JOIN clubs club ON club.id=cases.club_id
		LEFT JOIN teams team ON team.id=cases.team_id
		WHERE cases.assigned_admin_id=$1 AND NOT cases.is_test
		  AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=cases.id AND event.event_type='case_training_designated')
		  AND cases.status NOT IN ('published','closed','rejected','withdrawn')
		ORDER BY cases.updated_at DESC,cases.id DESC LIMIT $2`, adminID, itemLimit)
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

	responseRows, err := s.DB.Query(ctx, `SELECT cases.id,cases.reference,COALESCE(cases.player_name,''),
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
		  AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=cases.id AND event.event_type='case_training_designated')
		  AND NOT EXISTS(
			SELECT 1 FROM sanction_case_events reviewed
			WHERE reviewed.case_id=cases.id AND reviewed.event_type='response_reviewed'
			  AND reviewed.metadata->>'response_event_id'=response.id::text
		  )
		ORDER BY response.created_at DESC,cases.id DESC LIMIT $2`, adminID, itemLimit)
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

	taskRows, err := s.DB.Query(ctx, `SELECT task.id,cases.id,cases.reference,COALESCE(task.current_note,''),
		task.status,task.due_at,COUNT(*) OVER()
		FROM sanction_follow_up_tasks task
		JOIN sanction_cases cases ON cases.id=task.case_id
		WHERE task.assigned_admin_id=$1 AND task.status IN ('open','in_progress')
		  AND NOT cases.is_test
		  AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=cases.id AND event.event_type='case_training_designated')
		ORDER BY task.due_at NULLS LAST,task.id LIMIT $2`, adminID, itemLimit)
	if err != nil {
		return data, err
	}
	for taskRows.Next() {
		var item personalWorkTask
		if err = taskRows.Scan(&item.ID, &item.CaseID, &item.Reference, &item.Note, &item.Status, &item.DueAt, &data.TaskTotal); err != nil {
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
		queueClauses = append(queueClauses, "(cases.status='decision_proposed' AND (cases.source_type='ineligible_player' OR cases.proposed_by_admin_id IS DISTINCT FROM $1) AND NOT EXISTS(SELECT 1 FROM sanction_case_events required WHERE required.case_id=cases.id AND required.event_type='decision_owner_review_required' AND NOT EXISTS(SELECT 1 FROM sanction_case_events sent WHERE sent.case_id=required.case_id AND sent.event_type='decision_sent_for_approval' AND sent.metadata->>'decision_revision_id'=required.metadata->>'decision_revision_id')))")
	}
	if data.CanPublish {
		queueClauses = append(queueClauses, "(cases.status='approved' AND $1::integer IS NOT NULL)")
	}
	if len(queueClauses) > 0 {
		queueRows, queryErr := s.DB.Query(ctx, `SELECT cases.id,cases.reference,cases.status,
			COALESCE(club.name,''),COALESCE(cases.player_name,''),cases.updated_at,COUNT(*) OVER()
			FROM sanction_cases cases LEFT JOIN clubs club ON club.id=cases.club_id
			WHERE NOT cases.is_test
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
			if err = queueRows.Scan(&item.CaseID, &item.Reference, &status, &item.Club, &item.Player, &item.UpdatedAt, &data.DecisionTotal); err != nil {
				queueRows.Close()
				return data, err
			}
			if status == "decision_proposed" {
				item.Action = "Review decision"
			} else {
				item.Action = "Issue approved outcome"
			}
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
		cases.assigned_admin_id=$1,COUNT(*) OVER()
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
		if err = testRows.Scan(&item.CaseID, &item.Reference, &status, &item.Club, &item.Player, &item.UpdatedAt, &owned, &data.TestTotal); err != nil {
			testRows.Close()
			return data, err
		}
		if owned {
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
	data, err := s.loadPersonalWorkDashboard(r.Context(), *actor.ID, actor.Label)
	if err != nil {
		fmt.Fprint(w, `<section class="alert alert-warning mb-4"><strong>My work is temporarily unavailable.</strong> Use Ineligible-player work or Cases to continue.</section>`)
		return
	}
	writePersonalWorkDashboard(w, data, s.LondonLoc, time.Now())
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
			fmt.Fprintf(w, `<div class="alert alert-warning"><strong>Approval access missing.</strong> Decisions awaiting independent approval will not appear in this administrator's queue. A super administrator can review this under <a href="/admin/users/%d/sanctions-permissions">Sanctions access</a>.</div>`, id)
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
	urgentTotal := data.ResponseTotal + data.DecisionTotal
	decisionCard := ""
	if data.CanApprove || data.CanPublish {
		decisionCard = personalWorkCountCard("Approval / issue queue", data.DecisionTotal, "#my-decisions", map[bool]string{true: "danger", false: "secondary"}[data.DecisionTotal > 0])
	}
	fmt.Fprintf(w, `<section class="card mb-4 border-primary" aria-labelledby="my-work-heading">
<div class="card-header d-flex flex-wrap justify-content-between align-items-center gap-2">
<div><strong id="my-work-heading">My work</strong><div class="small text-muted">%s, %s. Open an item below to go straight to the work.</div></div>
<span class="badge %s">%d need attention</span>
</div><div class="card-body">
<div class="row g-2 mb-3">
%s%s%s
</div>`, escapeHTML(greeting), escapeHTML(name), map[bool]string{true: "text-bg-danger", false: "text-bg-success"}[urgentTotal > 0], urgentTotal,
		personalWorkCountCard("My cases", data.AssignedTotal, "/admin/cases/mine/ineligible", "primary"),
		personalWorkCountCard("Responses to review", data.ResponseTotal, "#my-responses", map[bool]string{true: "danger", false: "secondary"}[data.ResponseTotal > 0]),
		decisionCard)

	fmt.Fprint(w, `<div class="row g-3">`)
	writePersonalCaseList(w, data.AssignedCases, data.AssignedTotal, loc)
	writePersonalResponseList(w, data.Responses, data.ResponseTotal, loc)
	if data.CanApprove || data.CanPublish {
		writePersonalDecisionList(w, data.DecisionQueue, data.DecisionTotal)
	}
	fmt.Fprint(w, `</div></div></section>`)
	writePersonalTestCaseList(w, data.TestCases, data.TestTotal)
}

func writePersonalTestCaseList(w http.ResponseWriter, items []personalWorkQueueItem, total int64) {
	if total == 0 {
		return
	}
	fmt.Fprintf(w, `<section class="card mb-4 border-warning" id="test-cases"><div class="card-header d-flex justify-content-between"><strong>Test cases - training only</strong><span class="badge text-bg-warning">%d test</span></div><div class="card-body"><p class="small text-muted">These cases are deliberately excluded from live-work totals and cannot be mistaken for live league work.</p><div class="list-group">`, total)
	for _, item := range items {
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/%d"><div class="d-flex justify-content-between gap-2"><strong>Case %d - %s</strong><span class="badge text-bg-warning">%s</span></div><div class="small">%s - %s</div></a>`, item.CaseID, item.CaseID, escapeHTML(item.Reference), escapeHTML(item.Action), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(item.Club))
	}
	if total > int64(len(items)) {
		fmt.Fprintf(w, `<div class="list-group-item text-muted">Showing %d of %d relevant test cases.</div>`, len(items), total)
	}
	fmt.Fprint(w, `</div></div></section>`)
}

func personalWorkCountCard(label string, count int64, href, colour string) string {
	return fmt.Sprintf(`<div class="col-6 col-lg-3"><a class="border rounded p-3 d-block h-100 text-decoration-none" href="%s"><span class="h3 d-block mb-0 text-%s">%d</span><span class="small text-body">%s</span></a></div>`,
		escapeHTML(href), escapeHTML(colour), count, escapeHTML(label))
}

func writePersonalResponseList(w http.ResponseWriter, items []personalWorkResponse, total int64, loc *time.Location) {
	fmt.Fprint(w, `<div class="col-12 col-xl-6" id="my-responses"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Responses awaiting review</strong></div><div class="list-group list-group-flush">`)
	if len(items) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted">No new responses assigned to you.</div>`)
	}
	for _, item := range items {
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/%d#club-response"><div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-danger">Review reply</span></div><div class="small">%s</div><div class="small text-muted">%s · received %s</div></a>`,
			item.CaseID, escapeHTML(item.Reference), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(item.Club), escapeHTML(item.ReceivedAt.In(loc).Format("02 Jan 15:04")))
	}
	if total > int64(len(items)) {
		fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/ineligible?scope=mine&amp;state=all">View all assigned responses</a>`)
	}
	fmt.Fprint(w, `</div></div></div>`)
}

func writePersonalTaskList(w http.ResponseWriter, items []personalWorkTask, total int64, loc *time.Location, now time.Time) {
	fmt.Fprint(w, `<div class="col-12 col-xl-6" id="my-tasks"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Tasks assigned to me</strong></div><div class="list-group list-group-flush">`)
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
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/tasks?mine=1#task-%d"><div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge %s">%s</span></div><div class="small">%s</div></a>`,
			item.ID, escapeHTML(item.Reference), badge, escapeHTML(due), escapeHTML(defaultString(item.Note, "Open supporting task")))
	}
	if total > int64(len(items)) {
		fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/cases/tasks?mine=1">View all my tasks</a>`)
	}
	fmt.Fprint(w, `</div></div></div>`)
}

func writePersonalCaseList(w http.ResponseWriter, items []personalWorkCase, total int64, loc *time.Location) {
	fmt.Fprint(w, `<div class="col-12 col-xl-6" id="my-cases"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Cases I own</strong></div><div class="list-group list-group-flush">`)
	if len(items) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted">No active cases are assigned to you.</div>`)
	}
	for _, item := range items {
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/%d"><div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-light border">%s</span></div><div class="small">%s</div><div class="small text-muted">%s · updated %s</div></a>`,
			item.ID, escapeHTML(item.Reference), escapeHTML(caseStatusLabel(item.Status)), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(defaultString(item.Club, item.Team)), escapeHTML(item.UpdatedAt.In(loc).Format("02 Jan 15:04")))
	}
	if total > int64(len(items)) {
		fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/cases/mine/ineligible">View all my cases</a>`)
	}
	fmt.Fprint(w, `</div></div></div>`)
}

func writePersonalDecisionList(w http.ResponseWriter, items []personalWorkQueueItem, total int64) {
	fmt.Fprint(w, `<div class="col-12" id="my-decisions"><div class="border rounded h-100"><div class="p-3 border-bottom"><strong>Decisions needing my role</strong></div><div class="list-group list-group-flush">`)
	if len(items) == 0 {
		fmt.Fprint(w, `<div class="list-group-item text-muted">No decisions currently need your approval or issue permission.</div>`)
	}
	for _, item := range items {
		fmt.Fprintf(w, `<a class="list-group-item list-group-item-action" href="/admin/cases/%d"><div class="d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-danger">%s</span></div><div class="small">%s · %s</div></a>`,
			item.CaseID, escapeHTML(item.Reference), escapeHTML(item.Action), escapeHTML(defaultString(item.Player, "Player not recorded")), escapeHTML(item.Club))
	}
	if total > int64(len(items)) {
		fmt.Fprint(w, `<a class="list-group-item text-center" href="/admin/cases">View the full decision queue</a>`)
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
		return "Awaiting approval"
	case "approved":
		return "Ready to issue"
	default:
		return strings.ReplaceAll(status, "_", " ")
	}
}
