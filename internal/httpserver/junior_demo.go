package httpserver

import (
	"context"
	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/middleware"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"github.com/go-chi/chi/v5"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type JuniorDemoOptions struct {
	Password  string
	BaseURL   string
	AssetDir  string
	ExpiresAt time.Time
}

// NewJuniorDemoHandler does not call the production constructor or start jobs.
// It refuses the live database and exposes only junior routes and normal login.
// This constructor is called only by the separate cmd/junior-demo executable.
func NewJuniorDemoHandler(ctx context.Context, pool *db.Pool, options JuniorDemoOptions) (http.Handler, error) {
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return nil, err
	}
	if database != "gmcl_junior_demo" || len(options.Password) < 20 || options.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("isolated demo database, strong access password and expiry are required")
	}
	origin, err := url.Parse(options.BaseURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return nil, fmt.Errorf("canonical HTTPS demo origin required")
	}
	if os.Getenv("JUNIOR_OUTBOUND_EMAIL_ENABLED") != "false" || os.Getenv("SMTP_HOST") != "" {
		return nil, fmt.Errorf("demo must have outbound transport disabled")
	}
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		return nil, err
	}
	type mail struct {
		To, Subject, URL string
		At               time.Time
	}
	var mailboxMu sync.Mutex
	mailbox := []mail{}
	server := &Server{DB: pool, LondonLoc: london, JuniorDemo: true}
	server.JuniorAccessMailer = func(_ context.Context, to, subject, body string) error {
		if !strings.HasSuffix(to, "@demo.example.invalid") {
			return fmt.Errorf("demo mail accepts only fictional recipients")
		}
		link := ""
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, strings.TrimRight(options.BaseURL, "/")+"/junior/access?token=") {
				link = line
				break
			}
		}
		if link == "" {
			return fmt.Errorf("demo sign-in link missing")
		}
		mailboxMu.Lock()
		defer mailboxMu.Unlock()
		mailbox = append([]mail{{To: to, Subject: subject, URL: link, At: time.Now()}}, mailbox...)
		if len(mailbox) > 50 {
			mailbox = mailbox[:50]
		}
		return nil
	}
	router := chi.NewRouter()
	router.Use(middleware.SecurityHeaders())
	router.Get("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/_demo/", http.StatusSeeOther) })
	router.Get("/_demo/", func(w http.ResponseWriter, r *http.Request) {
		pageHead(w, "Joe's junior portal demonstration")
		fmt.Fprint(w, `<main class="container py-4" style="max-width:1000px"><div class="alert alert-warning"><strong>DEMONSTRATION ONLY</strong> · fictional clubs · isolated database · no real email</div><h1>Joe's junior portal demonstration</h1><p class="lead">Sunday 1 November · a complete junior administration journey</p><div class="d-flex flex-wrap gap-2 mb-4"><a class="btn btn-primary" href="/admin/juniors">Open League Admin</a><a class="btn btn-outline-primary" href="/junior/login">Open Club Junior Lead sign-in</a></div><p>League Admin username: <strong>joe-demo</strong>. Use the demonstration password supplied with this link. It is separate from all live accounts.</p><ol><li>Open Junior Home: inspect the overdue form, response awaiting review, fixture draft and concession review.</li><li>Open Oakfield Juniors CC: see contacts, team entries, forms, official fixtures and communication history together.</li><li>Use Contact directory to filter coaches by age and competition, export contacts, then select people for an audience preview.</li><li>Show the U13 published fixtures and U11 draft. Inspect checks before publishing any changes.</li><li>Review the sample concession evidence: confirmed and awaiting-review examples are distinct.</li><li>Switch to the club journey: request a sign-in for <strong>lead1@demo.example.invalid</strong>, then open its simulated email below. Confirm the one-use link, complete the registration form and return to League Admin to review it.</li></ol><p>Other demo leads: lead2@demo.example.invalid, lead3@demo.example.invalid and lead4@demo.example.invalid. Existing forms include outstanding, submitted, completed, returned and overdue examples.</p><h2 class="h4 mt-4">Simulated sign-in inbox</h2><p>No email provider is connected. Only sign-in messages for these fictional addresses appear here. Links expire and can be used once; request a fresh link after signing out.</p>`)
		mailboxMu.Lock()
		items := append([]mail(nil), mailbox...)
		mailboxMu.Unlock()
		for _, m := range items {
			fmt.Fprintf(w, `<article class="card card-body mb-3"><strong>%s</strong><p>%s · %s</p><a href="%s">Open this demonstration sign-in link</a></article>`, html.EscapeString(m.Subject), html.EscapeString(m.To), m.At.In(london).Format("2 Jan 15:04 MST"), html.EscapeString(m.URL))
		}
		if len(items) == 0 {
			fmt.Fprint(w, `<p class="text-muted">No new simulated emails yet. Request a club sign-in, then refresh this page.</p>`)
		}
		fmt.Fprint(w, `<p class="small text-muted">Changes persist while this demo runs. No live club records, production accounts or inbox connections are available here. Access ends after the demonstration period.</p></main>`)
		pageFooter(w)
	})
	router.Mount("/admin", server.adminRouter())
	router.Mount("/junior", server.juniorPortalRouter())
	router.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(options.AssetDir, "static")))))
	router.Handle("/images/*", http.StripPrefix("/images/", http.FileServer(http.Dir(filepath.Join(options.AssetDir, "images")))))
	expected := sha256.Sum256([]byte(options.Password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == "/health" {
			check, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := pool.Ping(check); err != nil {
				http.Error(w, "Unavailable", 503)
				return
			}
			fmt.Fprint(w, "ok")
			return
		}
		if r.Host != origin.Host {
			http.Error(w, "Unknown demo host", 404)
			return
		}
		if time.Now().After(options.ExpiresAt) {
			http.Error(w, "This demonstration period has ended. Please contact the league administrator.", 410)
			return
		}
		username, password, ok := r.BasicAuth()
		provided := sha256.Sum256([]byte(password))
		if !ok || username != "joe-demo" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="GMCL junior demonstration", charset="UTF-8"`)
			http.Error(w, "Demonstration access required", 401)
			return
		}
		if r.URL.Path == "/admin/dashboard" {
			http.Redirect(w, r, "/admin/juniors", http.StatusSeeOther)
			return
		}
		allowed := strings.HasPrefix(r.URL.Path, "/admin/juniors") || strings.HasPrefix(r.URL.Path, "/junior/") || r.URL.Path == "/junior" || strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/images/") || r.URL.Path == "/admin/login" || r.URL.Path == "/admin/2fa" || r.URL.Path == "/admin/logout" || r.URL.Path == "/" || r.URL.Path == "/_demo/"
		if !allowed {
			http.Error(w, "This demonstration includes junior administration only.", 404)
			return
		}
		router.ServeHTTP(w, r)
	}), nil
}
