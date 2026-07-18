package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleAdminSanctionPermissionsGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(chi.URLParam(r, "id"))
		var username, role string
		if s.DB.QueryRow(r.Context(), `SELECT username,role FROM admin_users WHERE id=$1`, id).Scan(&username, &role) != nil {
			http.NotFound(w, r)
			return
		}
		rows, err := s.DB.Query(r.Context(), `SELECT c.permission,c.description,EXISTS(SELECT 1 FROM admin_user_permissions p WHERE p.admin_user_id=$1 AND p.permission=c.permission) FROM sanction_permission_catalog c ORDER BY c.permission`, id)
		if err != nil {
			http.Error(w, "permissions unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions permissions")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:760px"><h1 class="h2">Sanctions permissions — %s</h1>`, escapeHTML(username))
		if role == "super_admin" {
			fmt.Fprint(w, `<div class="alert alert-info">Super-admins have every permission by role.</div></main>`)
			pageFooter(w)
			return
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/users/%d/sanctions-permissions" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="list-group list-group-flush">`, id, csrf)
		for rows.Next() {
			var permission, description string
			var granted bool
			if rows.Scan(&permission, &description, &granted) == nil {
				checked := ""
				if granted {
					checked = " checked"
				}
				fmt.Fprintf(w, `<label class="list-group-item"><input class="form-check-input me-2" type="checkbox" name="permission" value="%s"%s><strong>%s</strong><div class="small text-muted ms-4">%s</div></label>`, escapeHTML(permission), checked, escapeHTML(strings.TrimPrefix(permission, "sanctions_")), escapeHTML(description))
			}
		}
		fmt.Fprint(w, `</div><div class="card-body"><label class="form-label">Reason for change</label><input class="form-control" name="reason" required></div><div class="card-footer"><button class="btn btn-primary">Save permissions</button></div></form></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSanctionPermissionsPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(chi.URLParam(r, "id"))
		_ = r.ParseForm()
		reason := strings.TrimSpace(r.FormValue("reason"))
		if id == 0 || reason == "" {
			http.Error(w, "user and reason required", 400)
			return
		}
		sess, _ := getAdminSessionFromRequest(r)
		if sess == nil {
			http.Error(w, "unauthorised", 401)
			return
		}
		allowedRows, err := s.DB.Query(r.Context(), `SELECT permission FROM sanction_permission_catalog`)
		if err != nil {
			http.Error(w, "permissions unavailable", 500)
			return
		}
		allowed := map[string]bool{}
		for allowedRows.Next() {
			var p string
			if allowedRows.Scan(&p) == nil {
				allowed[p] = true
			}
		}
		allowedRows.Close()
		chosen := []string{}
		for _, p := range r.Form["permission"] {
			if allowed[p] {
				chosen = append(chosen, p)
			}
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "save failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var before []string
		oldRows, _ := tx.Query(r.Context(), `SELECT permission FROM admin_user_permissions WHERE admin_user_id=$1 AND permission LIKE 'sanctions_%' ORDER BY permission`, id)
		if oldRows != nil {
			for oldRows.Next() {
				var p string
				if oldRows.Scan(&p) == nil {
					before = append(before, p)
				}
			}
			oldRows.Close()
		}
		_, err = tx.Exec(r.Context(), `DELETE FROM admin_user_permissions WHERE admin_user_id=$1 AND permission LIKE 'sanctions_%'`, id)
		if err == nil {
			for _, p := range chosen {
				_, err = tx.Exec(r.Context(), `INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,$2)`, id, p)
				if err != nil {
					break
				}
			}
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_configuration_events(configuration_type,configuration_key,actor_admin_id,reason,before_data,after_data,request_id) VALUES('permissions',$1,$2,$3,to_jsonb($4::text[]),to_jsonb($5::text[]),$6)`, strconv.Itoa(id), sess.AdminID, reason, before, chosen, requestID(r))
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "save failed", 500)
			return
		}
		http.Redirect(w, r, "/admin/users", 303)
	}
}
