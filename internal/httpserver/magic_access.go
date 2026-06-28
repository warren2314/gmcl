package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"cricket-ground-feedback/internal/middleware"
)

func (s *Server) handleMagicAccessCode() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			csrfToken := middleware.CSRFToken(r)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			pageHead(w, "Access Captain Report")
			writeCaptainNav(w)
			fmt.Fprintf(w, `<div class="container" style="max-width:560px">
<div class="card card-gmcl shadow-sm">
  <div class="card-body">
    <h4 class="card-title mb-3">Open captain report</h4>
    <form method="POST" action="/access">
      <input type="hidden" name="csrf_token" value="%s">
      <div class="mb-3">
        <label class="form-label">Access code or full link</label>
        <textarea class="form-control" name="access_code" rows="4" autocomplete="off" spellcheck="false" required></textarea>
      </div>
      <button class="btn btn-primary w-100" type="submit">Continue</button>
    </form>
  </div>
</div>
</div>`, csrfToken)
			pageFooter(w)
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			token := normaliseMagicAccessCode(r.FormValue("access_code"))
			if token == "" {
				http.Error(w, "access code is required", http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, "/magic-link/confirm?token="+url.QueryEscape(token), http.StatusSeeOther)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func normaliseMagicAccessCode(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	compact := strings.Join(strings.Fields(raw), "")
	if token := magicLinkTokenFromURL(compact); token != "" {
		return strings.TrimSpace(token)
	}
	return compact
}
