package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleInternalSanctionOutbox() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		var globalEnabled bool
		if err := s.DB.QueryRow(ctx, `SELECT enabled FROM sanction_automation_settings WHERE source_type='_global'`).Scan(&globalEnabled); err != nil || !globalEnabled {
			http.Error(w, "sanctions automation kill switch is active", 503)
			return
		}
		// A global advisory lock avoids duplicate SMTP sends by overlapping workers.
		var locked bool
		_ = s.DB.QueryRow(ctx, `SELECT pg_try_advisory_lock(83002)`).Scan(&locked)
		if !locked {
			http.Error(w, "outbox worker already running", 409)
			return
		}
		defer s.DB.Exec(context.Background(), `SELECT pg_advisory_unlock(83002)`)
		rows, err := s.DB.Query(ctx, `SELECT id,recipient,subject,body FROM sanction_notification_outbox WHERE processed_at IS NULL AND available_at<=now() ORDER BY id LIMIT 50`)
		if err != nil {
			http.Error(w, "outbox unavailable", 500)
			return
		}
		type msg struct {
			id                int64
			to, subject, body string
		}
		pending := []msg{}
		for rows.Next() {
			var m msg
			if rows.Scan(&m.id, &m.to, &m.subject, &m.body) == nil {
				pending = append(pending, m)
			}
		}
		rows.Close()
		sent, failed := 0, 0
		mailer := email.NewFromEnv()
		for _, m := range pending {
			var attempt int
			_ = s.DB.QueryRow(ctx, `SELECT COALESCE(MAX(attempt_number),0)+1 FROM sanction_notification_attempts WHERE outbox_id=$1`, m.id).Scan(&attempt)
			sendErr := mailer.Send(m.to, m.subject, m.body)
			if sendErr != nil {
				failed++
				_, _ = s.DB.Exec(ctx, `INSERT INTO sanction_notification_attempts(outbox_id,attempt_number,status,error_message) VALUES($1,$2,'failed',$3) ON CONFLICT DO NOTHING`, m.id, attempt, sendErr.Error())
				continue
			}
			tx, e := s.DB.Begin(ctx)
			if e != nil {
				failed++
				continue
			}
			_, e = tx.Exec(ctx, `INSERT INTO sanction_notification_attempts(outbox_id,attempt_number,status) VALUES($1,$2,'sent')`, m.id, attempt)
			if e == nil {
				_, e = tx.Exec(ctx, `UPDATE sanction_notification_outbox SET processed_at=now() WHERE id=$1 AND processed_at IS NULL`, m.id)
			}
			if e == nil {
				e = tx.Commit(ctx)
			} else {
				tx.Rollback(ctx)
			}
			if e != nil {
				failed++
			} else {
				sent++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"selected": len(pending), "sent": sent, "failed": failed})
	}
}

func (s *Server) handleAdminSanctionAutomation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.DB.Query(r.Context(), `SELECT source_type,mode,enabled,clean_cycles,last_reconciled_at,activated_at FROM sanction_automation_settings ORDER BY source_type`)
		if err != nil {
			http.Error(w, "settings unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions automation")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container py-4" style="max-width:900px"><h1 class="h2">Sanctions automation safety</h1><p class="text-muted">Only deterministic sources can become automatic. Three reconciled clean cycles and an explicit super-admin change are required.</p><div class="row g-3">`)
		for rows.Next() {
			var source, mode string
			var enabled bool
			var cycles int
			var reconciled, activated *time.Time
			if rows.Scan(&source, &mode, &enabled, &cycles, &reconciled, &activated) != nil {
				continue
			}
			checked := ""
			if enabled {
				checked = " checked"
			}
			fmt.Fprintf(w, `<div class="col-md-6"><form method="POST" action="/admin/cases/automation" class="card h-100"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="source_type" value="%s"><div class="card-header"><strong>%s</strong></div><div class="card-body"><p>Mode: <strong>%s</strong><br>Clean cycles: <strong>%d / 3</strong></p><select class="form-select mb-2" name="mode"><option value="shadow"%s>Shadow</option><option value="manual"%s>Manual approval</option><option value="automatic"%s>Automatic</option></select><label class="form-check mb-2"><input class="form-check-input" type="checkbox" name="enabled" value="yes"%s> <span class="form-check-label">Enabled (clear to activate kill switch)</span></label><label class="form-label">Reason</label><input class="form-control" name="reason" required></div><div class="card-footer d-flex gap-2"><button class="btn btn-primary" name="action" value="update">Save</button>`, csrf, escapeHTML(source), escapeHTML(source), escapeHTML(mode), cycles, selectedMode(mode, "shadow"), selectedMode(mode, "manual"), selectedMode(mode, "automatic"), checked)
			if source != "_global" {
				fmt.Fprint(w, `<button class="btn btn-outline-success" name="action" value="clean_cycle">Record clean cycle</button>`)
			}
			fmt.Fprint(w, `</div></form></div>`)
		}
		fmt.Fprint(w, `</div></main>`)
		pageFooter(w)
	}
}

func selectedMode(current, want string) string {
	if current == want {
		return " selected"
	}
	return ""
}

func (s *Server) handleAdminSanctionAutomationPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		source := r.FormValue("source_type")
		mode := r.FormValue("mode")
		reason := strings.TrimSpace(r.FormValue("reason"))
		action := r.FormValue("action")
		allowed := map[string]bool{"_global": true, "captain_report": true, "play_cricket": true}
		if !allowed[source] || reason == "" {
			http.Error(w, "valid source and reason required", 400)
			return
		}
		sess, _ := getAdminSessionFromRequest(r)
		if sess == nil || s.effectiveAdminRole(r.Context(), sess.AdminID) != "super_admin" {
			http.Error(w, "super-admin required", 403)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "update failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var oldMode string
		var oldEnabled bool
		var cycles int
		if tx.QueryRow(r.Context(), `SELECT mode,enabled,clean_cycles FROM sanction_automation_settings WHERE source_type=$1 FOR UPDATE`, source).Scan(&oldMode, &oldEnabled, &cycles) != nil {
			http.Error(w, "source not found", 404)
			return
		}
		newMode, newEnabled := oldMode, oldEnabled
		if action == "clean_cycle" {
			cycles++
		} else {
			if mode != "shadow" && mode != "manual" && mode != "automatic" {
				http.Error(w, "invalid mode", 400)
				return
			}
			if source == "_global" && mode == "automatic" {
				mode = "manual"
			}
			if mode == "automatic" && cycles < 3 {
				http.Error(w, "three clean cycles are required before automatic mode", 400)
				return
			}
			newMode = mode
			newEnabled = r.FormValue("enabled") == "yes"
		}
		before, _ := json.Marshal(map[string]any{"mode": oldMode, "enabled": oldEnabled, "clean_cycles": func() int {
			if action == "clean_cycle" {
				return cycles - 1
			}
			return cycles
		}()})
		after, _ := json.Marshal(map[string]any{"mode": newMode, "enabled": newEnabled, "clean_cycles": cycles})
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_configuration_events(configuration_type,configuration_key,actor_admin_id,reason,before_data,after_data,request_id) VALUES('automation',$1,$2,$3,$4,$5,$6)`, source, sess.AdminID, reason, before, after, requestID(r))
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_automation_settings SET mode=$2,enabled=$3,clean_cycles=$4,last_reconciled_at=CASE WHEN $5='clean_cycle' THEN now() ELSE last_reconciled_at END,activated_by_admin_id=CASE WHEN $2='automatic' THEN $6 ELSE activated_by_admin_id END,activated_at=CASE WHEN $2='automatic' THEN now() ELSE activated_at END,updated_at=now() WHERE source_type=$1`, source, newMode, newEnabled, cycles, action, sess.AdminID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "update failed", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/automation", 303)
	}
}

func parseIntOrNil(v string) *int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func (s *Server) handleAdminSanctionTasks() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		includeClosed := r.URL.Query().Get("archive") == "1"
		where := `t.status IN ('open','in_progress')`
		if includeClosed {
			where = `TRUE`
		}
		rows, err := s.DB.Query(r.Context(), `SELECT t.id,c.id,c.reference,t.task_type,t.status,COALESCE(t.current_note,''),t.due_at,t.created_at,COALESCE(a.username,'') FROM sanction_follow_up_tasks t JOIN sanction_cases c ON c.id=t.case_id LEFT JOIN admin_users a ON a.id=t.assigned_admin_id WHERE `+where+` ORDER BY CASE t.status WHEN 'open' THEN 0 WHEN 'in_progress' THEN 1 ELSE 2 END,t.due_at NULLS LAST,t.id`)
		if err != nil {
			http.Error(w, "tasks unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions follow-up tasks")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container py-4" style="max-width:1000px"><div class="d-flex flex-column flex-sm-row justify-content-between gap-2 mb-3"><div><h1 class="h2">Sanctions follow-up tasks</h1><p class="text-muted mb-0">Points deductions, fine recovery, Board intervention, suspension reviews, appeals and ban expiry.</p></div><a class="btn btn-outline-secondary align-self-sm-start" href="/admin/cases">Back to cases</a></div><form method="GET" class="mb-3"><label class="form-check"><input class="form-check-input" type="checkbox" name="archive" value="1"`)
		if includeClosed {
			fmt.Fprint(w, ` checked`)
		}
		fmt.Fprint(w, ` onchange="this.form.submit()"> <span class="form-check-label">Include completed and cancelled tasks</span></label></form><div class="row g-3">`)
		count := 0
		for rows.Next() {
			var taskID, caseID int64
			var ref, taskType, status, note, assigned string
			var due *time.Time
			var created time.Time
			if rows.Scan(&taskID, &caseID, &ref, &taskType, &status, &note, &due, &created, &assigned) != nil {
				continue
			}
			count++
			dueLabel := "No due date"
			if due != nil {
				dueLabel = due.In(s.LondonLoc).Format("02 Jan 2006 15:04")
			}
			fmt.Fprintf(w, `<div class="col-12"><article class="card"><div class="card-header d-flex flex-wrap justify-content-between gap-2"><div><a href="/admin/cases/%d"><strong>%s</strong></a> <span class="badge text-bg-secondary">%s</span></div><span>%s</span></div><form method="POST" action="/admin/cases/tasks/%d"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-3"><label class="form-label">Status</label><select class="form-select" name="status"><option value="open"%s>Open</option><option value="in_progress"%s>In progress</option><option value="complete"%s>Complete</option><option value="cancelled"%s>Cancelled</option></select></div><div class="col-md-4"><label class="form-label">Operational note</label><input class="form-control" name="note" value="%s"></div><div class="col-md-5"><label class="form-label">Reason for change</label><input class="form-control" name="reason" required placeholder="Why is this task changing?"></div></div><div class="card-footer d-flex flex-column flex-sm-row justify-content-between gap-2"><small class="text-muted">Due: %s · Assigned: %s · Created: %s</small><button class="btn btn-primary">Record task update</button></div></form></article></div>`, caseID, escapeHTML(ref), escapeHTML(strings.ReplaceAll(taskType, "_", " ")), escapeHTML(status), taskID, csrf, selectedMode(status, "open"), selectedMode(status, "in_progress"), selectedMode(status, "complete"), selectedMode(status, "cancelled"), escapeHTML(note), escapeHTML(dueLabel), escapeHTML(defaultString(assigned, "unassigned")), created.In(s.LondonLoc).Format("02 Jan 2006"))
		}
		if count == 0 {
			fmt.Fprint(w, `<div class="col-12"><div class="alert alert-success">There are no follow-up tasks in this view.</div></div>`)
		}
		fmt.Fprint(w, `</div></main>`)
		pageFooter(w)
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *Server) handleAdminSanctionTaskUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
		if err != nil || taskID < 1 || r.ParseForm() != nil {
			http.Error(w, "invalid task", 400)
			return
		}
		status := strings.TrimSpace(r.FormValue("status"))
		reason := strings.TrimSpace(r.FormValue("reason"))
		note := strings.TrimSpace(r.FormValue("note"))
		if !map[string]bool{"open": true, "in_progress": true, "complete": true, "cancelled": true}[status] || reason == "" {
			http.Error(w, "valid status and reason are required", 400)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", 401)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "task update failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var beforeData []byte
		if tx.QueryRow(r.Context(), `SELECT to_jsonb(t) FROM sanction_follow_up_tasks t WHERE id=$1 FOR UPDATE`, taskID).Scan(&beforeData) != nil {
			http.NotFound(w, r)
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE sanction_follow_up_tasks SET status=$2,current_note=$3,assigned_admin_id=COALESCE(assigned_admin_id,$4),updated_at=now() WHERE id=$1`, taskID, status, nullIfEmptyHTTP(note), *actor.ID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_follow_up_task_events(task_id,actor_admin_id,actor_label,reason,before_data,after_data,request_id) SELECT id,$2,$3,$4,$5::jsonb,to_jsonb(t),$6 FROM sanction_follow_up_tasks t WHERE id=$1`, taskID, *actor.ID, actor.Label, reason, string(beforeData), actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "task update failed", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/tasks", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminSanctionRecipients() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.DB.Query(r.Context(), `SELECT recipient_role,name,email,active FROM sanction_recipient_directory ORDER BY recipient_role,name`)
		if err != nil {
			http.Error(w, "recipients unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanction recipients")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><h1 class="h2">Sanction notice recipients</h1><p class="text-muted">Captains are resolved automatically. These role addresses receive notices when the versioned policy calls for them.</p><form method="POST" action="/admin/cases/recipients" class="card mb-4"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-3"><select class="form-select" name="recipient_role"><option value="executive">Executive</option><option value="discipline">Discipline</option><option value="finance">Finance</option><option value="play_cricket">Play-Cricket</option><option value="other">Other</option></select></div><div class="col-md-3"><input class="form-control" name="name" required placeholder="Name"></div><div class="col-md-3"><input class="form-control" type="email" name="email" required placeholder="Email"></div><div class="col-md-3"><input class="form-control" name="reason" required placeholder="Reason for change"></div></div><div class="card-footer"><button class="btn btn-primary">Add or reactivate</button></div></form><table class="table"><thead><tr><th>Role</th><th>Name</th><th>Email</th><th>Status</th></tr></thead><tbody>`, csrf)
		for rows.Next() {
			var role, name, emailAddr string
			var active bool
			if rows.Scan(&role, &name, &emailAddr, &active) == nil {
				status := "inactive"
				if active {
					status = "active"
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, escapeHTML(role), escapeHTML(name), escapeHTML(emailAddr), status)
			}
		}
		fmt.Fprint(w, `</tbody></table></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSanctionRecipientDirectoryPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		role := r.FormValue("recipient_role")
		name := strings.TrimSpace(r.FormValue("name"))
		emailAddr := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		reason := strings.TrimSpace(r.FormValue("reason"))
		allowed := map[string]bool{"executive": true, "discipline": true, "finance": true, "play_cricket": true, "other": true}
		if !allowed[role] || name == "" || !strings.Contains(emailAddr, "@") || reason == "" {
			http.Error(w, "role, name, email and reason are required", 400)
			return
		}
		sess, _ := getAdminSessionFromRequest(r)
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "save failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var before []byte
		var oldName string
		var oldActive bool
		if tx.QueryRow(r.Context(), `SELECT name,active FROM sanction_recipient_directory WHERE recipient_role=$1 AND email=$2`, role, emailAddr).Scan(&oldName, &oldActive) == nil {
			before, _ = json.Marshal(map[string]any{"name": oldName, "active": oldActive})
		}
		after, _ := json.Marshal(map[string]any{"name": name, "email": emailAddr, "role": role, "active": true})
		var admin any
		if sess != nil {
			admin = sess.AdminID
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_configuration_events(configuration_type,configuration_key,actor_admin_id,reason,before_data,after_data,request_id) VALUES('recipient',$1,$2,$3,$4,$5,$6)`, role+":"+emailAddr, admin, reason, before, after, requestID(r))
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES($1,$2,$3) ON CONFLICT(recipient_role,email) DO UPDATE SET name=EXCLUDED.name,active=TRUE`, role, name, emailAddr)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "save failed", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/recipients", 303)
	}
}
