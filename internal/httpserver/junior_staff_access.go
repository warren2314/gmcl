package httpserver

import (
	"fmt"
	"net/http"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
)

func (s *Server) HandleJuniorStaffAccess() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		staff, err := junior.NewService(s.DB).ListStaffAccess(r.Context(), s.juniorPrincipal(r))
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		s.juniorPageStart(w, r, "Junior staff access")
		fmt.Fprint(w, `<p>Super Admin can grant or revoke Junior League administration for active staff accounts. This changes only junior access. Existing super-admin access follows the established super-admin policy.</p><div class="table-responsive"><table class="table"><thead><tr><th>Staff account</th><th>Current junior access</th><th>Change access</th></tr></thead><tbody>`)
		for _, item := range staff {
			label := "No junior access"
			if item.ExplicitGrant {
				label = "Explicit junior access"
			}
			if item.EffectiveSuperAdmin {
				label = "Super Admin (existing policy)"
			}
			fmt.Fprintf(w, `<tr><td>%s<br><span class="small text-muted">%s</span></td><td>%s</td><td>`, escapeHTML(item.Username), escapeHTML(item.Email), label)
			if item.EffectiveSuperAdmin {
				fmt.Fprint(w, `<span class="small text-muted">Super-admin access is managed through the existing staff policy.</span>`)
			} else {
				action, text := "grant", "Grant junior access"
				if item.ExplicitGrant {
					action, text = "revoke", "Revoke junior access"
				}
				fmt.Fprintf(w, `<form method="POST" action="/admin/juniors/staff-access" class="d-flex flex-wrap gap-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="admin_id" value="%d"><input type="hidden" name="action" value="%s"><label class="visually-hidden" for="reason-%d">Reason for access change</label><input class="form-control" id="reason-%d" name="reason" maxlength="300" placeholder="Reason for access change" required><button class="btn btn-outline-primary">%s</button></form>`, escapeHTML(middleware.CSRFToken(r)), item.AdminID, action, item.AdminID, item.AdminID, text)
			}
			fmt.Fprint(w, `</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
		juniorPageEnd(w)
	}
}

func (s *Server) HandleJuniorStaffAccessSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		id, err := juniorRecordID(r.FormValue("admin_id"), true)
		if err != nil || id > 2147483647 {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		action := r.FormValue("action")
		if action != "grant" && action != "revoke" {
			juniorHTTPError(w, r, junior.ErrValidation)
			return
		}
		if err = junior.NewService(s.DB).SetStaffAccess(r.Context(), s.juniorPrincipal(r), int32(id), action == "grant", r.FormValue("reason")); err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, "/admin/juniors/staff-access?success=Junior+staff+access+saved", http.StatusSeeOther)
	}
}
