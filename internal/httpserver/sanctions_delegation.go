package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type investigationAdminOption struct {
	ID   int32
	Name string
}

func (s *Server) loadInvestigationAdmins(ctx context.Context) ([]investigationAdminOption, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT admin.id,admin.username
		FROM admin_users admin
		WHERE admin.is_active AND (
			COALESCE(admin.role,'admin')='super_admin' OR EXISTS(
				SELECT 1 FROM admin_user_permissions permission
				WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_investigate'
			)
		)
		ORDER BY lower(admin.username),admin.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := make([]investigationAdminOption, 0, 8)
	for rows.Next() {
		var option investigationAdminOption
		if err = rows.Scan(&option.ID, &option.Name); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func investigationAdminSelect(options []investigationAdminOption, selected *int32) string {
	var output strings.Builder
	fmt.Fprint(&output, `<option value="">Choose an administrator...</option>`)
	for _, option := range options {
		selectedHTML := ""
		if selected != nil && option.ID == *selected {
			selectedHTML = " selected"
		}
		fmt.Fprintf(&output, `<option value="%d"%s>%s</option>`, option.ID, selectedHTML, escapeHTML(option.Name))
	}
	return output.String()
}

func (s *Server) writeAdminCaseDelegationControls(w http.ResponseWriter, r *http.Request, caseID int64, csrf string, assignedAdminID *int32, assignedAdminName string, currentAdminID *int32) {
	options, err := s.loadInvestigationAdmins(r.Context())
	if err != nil || len(options) == 0 {
		fmt.Fprint(w, adminCaseAssignmentHTML(caseID, csrf, assignedAdminID, assignedAdminName, currentAdminID))
		return
	}
	currentOwner := "Unassigned"
	if strings.TrimSpace(assignedAdminName) != "" {
		currentOwner = assignedAdminName
	}
	fmt.Fprintf(w, `<section class="card mb-3 border-primary"><div class="card-header"><strong>Case owner and help</strong></div><div class="card-body"><p class="mb-1"><strong>Current owner:</strong> %s</p><p class="small text-muted">Change the case owner, or keep the owner and give a separate task to another administrator.</p><form method="POST" action="/admin/cases/%d/assign" class="border rounded p-2 mb-3"><input type="hidden" name="csrf_token" value="%s"><label class="form-label fw-semibold">Assign the whole investigation</label><select class="form-select mb-2" name="admin_id" required>%s</select><label class="form-label">Reason</label><input class="form-control mb-2" name="reason" maxlength="1000" required placeholder="Why is ownership changing?"><button class="btn btn-outline-primary">Save case owner</button></form>`, escapeHTML(currentOwner), caseID, escapeHTML(csrf), investigationAdminSelect(options, assignedAdminID))
	fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/tasks" class="border rounded p-2"><input type="hidden" name="csrf_token" value="%s"><label class="form-label fw-semibold">Give a supporting task to another administrator</label><select class="form-select mb-2" name="admin_id" required>%s</select><label class="form-label">What help is needed?</label><textarea class="form-control mb-2" name="task" rows="3" minlength="5" maxlength="2000" required placeholder="For example: check Play-Cricket registration and add a private note to this case"></textarea><label class="form-label">Due date (optional)</label><input class="form-control mb-2" type="date" name="due_date"><button class="btn btn-outline-secondary">Assign supporting task</button></form></div></section>`, caseID, escapeHTML(csrf), investigationAdminSelect(options, nil))

	rows, queryErr := s.DB.Query(r.Context(), `SELECT task.id,COALESCE(admin.username,''),task.status,COALESCE(task.current_note,''),task.due_at
		FROM sanction_follow_up_tasks task LEFT JOIN admin_users admin ON admin.id=task.assigned_admin_id
		WHERE task.case_id=$1 AND task.task_type='investigation_support' AND task.status IN ('open','in_progress')
		ORDER BY task.due_at NULLS LAST,task.id`, caseID)
	if queryErr != nil {
		return
	}
	defer rows.Close()
	type supportTask struct {
		id                     int64
		assignee, status, note string
		due                    *time.Time
	}
	tasks := []supportTask{}
	for rows.Next() {
		var task supportTask
		if rows.Scan(&task.id, &task.assignee, &task.status, &task.note, &task.due) == nil {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) == 0 {
		return
	}
	fmt.Fprint(w, `<section class="card mb-3"><div class="card-header">Open supporting tasks</div><ul class="list-group list-group-flush">`)
	for _, task := range tasks {
		due := "No due date"
		if task.due != nil {
			due = task.due.In(s.LondonLoc).Format("02 Jan 2006")
		}
		fmt.Fprintf(w, `<li class="list-group-item"><strong>%s</strong><div class="small text-muted">Assigned to %s - %s - %s</div></li>`, escapeHTML(task.note), escapeHTML(defaultString(task.assignee, "unassigned")), escapeHTML(task.status), escapeHTML(due))
	}
	fmt.Fprint(w, `</ul><div class="card-footer"><a href="/admin/cases/tasks" class="btn btn-sm btn-outline-secondary">Open task list</a></div></section>`)
}

func (s *Server) resolveInvestigationAdmin(ctx context.Context, adminID int32) (string, error) {
	var name string
	err := s.DB.QueryRow(ctx, `SELECT admin.username FROM admin_users admin
		WHERE admin.id=$1 AND admin.is_active AND (
			COALESCE(admin.role,'admin')='super_admin' OR EXISTS(
				SELECT 1 FROM admin_user_permissions permission
				WHERE permission.admin_user_id=admin.id AND permission.permission='sanctions_investigate'
			)
		)`, adminID).Scan(&name)
	return name, err
}

func reassignOpenCaseOwnerTasks(ctx context.Context, tx pgx.Tx, caseID int64, previous *int32, targetID, actorID int32, targetName, actorLabel, reason string, requestID any) (int64, error) {
	if previous == nil || *previous == targetID {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `WITH candidates AS MATERIALIZED (
		SELECT task.id,to_jsonb(task) AS before_data
		FROM sanction_follow_up_tasks task
		WHERE task.case_id=$1 AND task.assigned_admin_id=$2
		  AND task.task_type='investigation_support' AND task.status IN ('open','in_progress')
		FOR UPDATE
	), updated AS (
		UPDATE sanction_follow_up_tasks task
		SET assigned_admin_id=$3,updated_at=now()
		FROM candidates
		WHERE task.id=candidates.id
		RETURNING task.id,to_jsonb(task) AS after_data
	)
	INSERT INTO sanction_follow_up_task_events(task_id,actor_admin_id,actor_label,reason,before_data,after_data,request_id)
	SELECT updated.id,$4,$5,$6,candidates.before_data,updated.after_data,$7
	FROM updated JOIN candidates ON candidates.id=updated.id`,
		caseID, *previous, targetID, actorID, actorLabel, "Case ownership changed to "+targetName+": "+reason, requestID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func caseAssignmentSuccess(targetName string, movedTasks int64) string {
	message := "Case assigned to " + targetName
	if movedTasks == 1 {
		return message + "; 1 open investigation task moved with the case"
	}
	if movedTasks > 1 {
		return fmt.Sprintf("%s; %d open investigation tasks moved with the case", message, movedTasks)
	}
	return message
}

func (s *Server) handleAdminCaseAssign() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID < 1 || r.ParseForm() != nil {
			http.Error(w, "invalid case assignment", http.StatusBadRequest)
			return
		}
		adminValue, adminErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("admin_id")), 10, 32)
		reason := strings.TrimSpace(r.FormValue("reason"))
		actor := adminActor(r)
		if adminErr != nil || adminValue < 1 || reason == "" || len(reason) > 1000 || actor.ID == nil {
			http.Error(w, "a valid administrator and reason are required", http.StatusBadRequest)
			return
		}
		targetID := int32(adminValue)
		targetName, err := s.resolveInvestigationAdmin(r.Context(), targetID)
		if err != nil {
			http.Error(w, "the selected administrator cannot investigate sanctions cases", http.StatusBadRequest)
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "assignment failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var previous *int32
		if tx.QueryRow(r.Context(), `SELECT assigned_admin_id FROM sanction_cases WHERE id=$1 FOR UPDATE`, caseID).Scan(&previous) != nil {
			http.NotFound(w, r)
			return
		}
		if sameAdminAssignment(previous, &targetID) {
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", caseID, url.QueryEscape("Case is already assigned to "+targetName)), http.StatusSeeOther)
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET assigned_admin_id=$2,status=CASE WHEN status IN ('submitted','triage') THEN 'investigating' ELSE status END,updated_at=now() WHERE id=$1`, caseID, targetID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,request_id)
				VALUES($1,'investigator_assigned','admin',$2,$3,$4,jsonb_build_object('assigned_admin_id',$5::integer),jsonb_build_object('assigned_admin_id',$6::integer,'assigned_admin',$7::text),$8)`,
				caseID, *actor.ID, actor.Label, reason, previous, targetID, targetName, actor.RequestID)
		}
		var movedTasks int64
		if err == nil {
			movedTasks, err = reassignOpenCaseOwnerTasks(r.Context(), tx, caseID, previous, targetID, *actor.ID, targetName, actor.Label, reason, actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "assignment failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", caseID, url.QueryEscape(caseAssignmentSuccess(targetName, movedTasks))), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminCaseSupportTaskCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || caseID < 1 || r.ParseForm() != nil {
			http.Error(w, "invalid task", http.StatusBadRequest)
			return
		}
		adminValue, adminErr := strconv.ParseInt(strings.TrimSpace(r.FormValue("admin_id")), 10, 32)
		note := strings.TrimSpace(r.FormValue("task"))
		actor := adminActor(r)
		if adminErr != nil || adminValue < 1 || len(note) < 5 || len(note) > 2000 || actor.ID == nil {
			http.Error(w, "a valid administrator and task are required", http.StatusBadRequest)
			return
		}
		targetID := int32(adminValue)
		targetName, err := s.resolveInvestigationAdmin(r.Context(), targetID)
		if err != nil {
			http.Error(w, "the selected administrator cannot investigate sanctions cases", http.StatusBadRequest)
			return
		}
		var due *time.Time
		if value := strings.TrimSpace(r.FormValue("due_date")); value != "" {
			date, parseErr := time.ParseInLocation("2006-01-02", value, s.LondonLoc)
			if parseErr != nil {
				http.Error(w, "the due date is invalid", http.StatusBadRequest)
				return
			}
			deadline := time.Date(date.Year(), date.Month(), date.Day(), 17, 0, 0, 0, s.LondonLoc)
			due = &deadline
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "task could not be assigned", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var caseExists bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sanction_cases WHERE id=$1)`, caseID).Scan(&caseExists); err != nil || !caseExists {
			http.NotFound(w, r)
			return
		}
		var taskID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_follow_up_tasks(case_id,task_type,assigned_admin_id,due_at,current_note)
			VALUES($1,'investigation_support',$2,$3,$4) RETURNING id`, caseID, targetID, due, note).Scan(&taskID)
		if err == nil {
			after, _ := json.Marshal(map[string]any{"case_id": caseID, "task_type": "investigation_support", "assigned_admin_id": targetID, "assigned_admin": targetName, "due_at": due, "current_note": note, "status": "open"})
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_follow_up_task_events(task_id,actor_admin_id,actor_label,reason,after_data,request_id)
				VALUES($1,$2,$3,$4,$5::jsonb,$6)`, taskID, *actor.ID, actor.Label, "Assigned investigation support task to "+targetName, string(after), actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "task could not be assigned", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", caseID, url.QueryEscape("Supporting task assigned to "+targetName)), http.StatusSeeOther)
	}
}
