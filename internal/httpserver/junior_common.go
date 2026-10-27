package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
)

func juniorSeasonURL(r *http.Request, href string) string {
	id := juniorQueryID(r, "season_id")
	if id == 0 {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	q := u.Query()
	q.Set("season_id", strconv.FormatInt(id, 10))
	u.RawQuery = q.Encode()
	return u.String()
}

func juniorQueryID(r *http.Request, key string) int64 {
	id, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil || id < 1 {
		return 0
	}
	return id
}

func juniorHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusInternalServerError, "Junior information could not be loaded. Please try again."
	switch {
	case errors.Is(err, junior.ErrForbidden):
		status, message = http.StatusForbidden, "You do not have access to this club or action."
	case errors.Is(err, junior.ErrNotFound):
		status, message = http.StatusNotFound, "This junior record was not found."
	case errors.Is(err, junior.ErrConflict):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, junior.ErrValidation):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, junior.ErrTransportUnavailable):
		status, message = http.StatusServiceUnavailable, "Junior email is not yet configured. The action has not been reported as delivered."
	default:
		slog.Error("junior request failed", "path", r.URL.Path, "error", err)
	}
	http.Error(w, message, status)
}

func (s *Server) juniorPageStart(w http.ResponseWriter, r *http.Request, title string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	pageHead(w, title)
	p := s.juniorPrincipal(r)
	if p.AdminID > 0 {
		s.writeAdminWorkspaceNav(w, r)
	}
	fmt.Fprint(w, `<main class="container py-4" style="max-width:1280px">`)
	if s.JuniorDemo {
		fmt.Fprint(w, `<div class="alert alert-warning"><strong>DEMONSTRATION — fictional clubs and records.</strong> Changes affect this demo only. No emails are sent. <a href="/admin/demo-guide">Demo guide and simulated sign-in inbox</a>.</div>`)
	}
	if p.AdminID > 0 && !p.ReadOnly {
		fmt.Fprint(w, `<nav class="d-flex flex-wrap gap-2 mb-4" aria-label="Junior administration">`)
		for _, link := range []struct{ path, label string }{{"", "Junior Home"}, {"clubs", "Clubs and contacts"}, {"contacts", "Contact directory"}, {"forms", "Official forms"}, {"competitions", "Competitions and fixtures"}, {"concessions", "Concession review"}, {"messages", "Messages"}} {
			href := "/admin/juniors"
			if link.path != "" {
				href += "/" + link.path
			}
			fmt.Fprintf(w, `<a class="btn btn-outline-secondary" href="%s">%s</a>`, escapeHTML(juniorSeasonURL(r, href)), link.label)
		}
		fmt.Fprint(w, `</nav>`)
	} else {
		base := "/junior"
		if p.ReadOnly {
			fmt.Fprint(w, `<div class="alert alert-info"><strong>View as Club — read-only staff preview.</strong> Your staff identity is retained. No club action or message can be submitted from this preview.</div>`)
			base = fmt.Sprintf("/admin/juniors/clubs/%d/preview", p.ClubID)
		}
		fmt.Fprint(w, `<nav class="d-flex flex-wrap gap-2 mb-4" aria-label="Club portal">`)
		for _, link := range []struct{ path, label string }{{"", "Club Home"}, {"contacts", "Contacts and teams"}, {"forms", "Official forms"}, {"fixtures", "Official fixtures"}, {"concessions", "Concessions"}, {"messages", "Messages"}} {
			href := base
			if link.path != "" {
				if p.ReadOnly {
					href += "?view=" + link.path
				} else {
					href += "/" + link.path
				}
			}
			fmt.Fprintf(w, `<a class="btn btn-outline-secondary" href="%s">%s</a>`, escapeHTML(juniorSeasonURL(r, href)), link.label)
		}
		if !p.ReadOnly {
			fmt.Fprintf(w, `<form method="POST" action="/junior/logout"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-outline-secondary">Sign out</button></form>`, escapeHTML(middleware.CSRFToken(r)))
		}
		fmt.Fprint(w, `</nav>`)
	}
	fmt.Fprintf(w, `<h1 class="h2 mb-3">%s</h1>`, escapeHTML(title))
	if message := strings.TrimSpace(r.URL.Query().Get("success")); message != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(message))
	}
}

func juniorPageEnd(w http.ResponseWriter) { fmt.Fprint(w, `</main>`); pageFooter(w) }
