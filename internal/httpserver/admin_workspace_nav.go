package httpserver

import (
	"io"
	"log/slog"
	"net/http"

	"cricket-ground-feedback/internal/middleware"
)

// Reveal a linked section even when its enclosing native disclosure is closed.
// This also handles bookmarked fragments and repeated clicks on the same link.
const adminWorkspaceDisclosureJS = `(function(){function reveal(hash){if(!hash||hash.charAt(0)!=='#')return;var target=document.getElementById(hash.slice(1));for(var node=target;node;node=node.parentElement){if(node.tagName==='DETAILS')node.open=true;}}document.addEventListener('click',function(event){var link=event.target.closest('a[href^="#"]');if(link)reveal(link.getAttribute('href'));});window.addEventListener('hashchange',function(){reveal(window.location.hash);});reveal(window.location.hash);})();`

// Limit this capability-aware navbar to the current workspace pages. Older
// pages keep their existing renderer until their own workflows are reviewed.
func (s *Server) writeAdminWorkspaceNav(w io.Writer, r *http.Request) {
	adminID := adminIDForRequest(r)
	role := s.effectiveAdminRole(r.Context(), adminID)
	permissions := map[string]bool{}
	if role != "super_admin" && adminID > 0 {
		rows, err := s.DB.Query(r.Context(), `SELECT permission FROM admin_user_permissions WHERE admin_user_id=$1`, adminID)
		if err == nil {
			for rows.Next() {
				var permission string
				if err = rows.Scan(&permission); err != nil {
					break
				}
				permissions[permission] = true
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err != nil {
			slog.Error("load workspace navigation permissions", "admin_id", adminID, "error", err)
			permissions = map[string]bool{}
		}
	}
	writeAdminNavWithPermissions(w, middleware.CSRFToken(r), r.URL.Path, role, permissions)
}
