package httpserver

import (
	"context"
	"log"
	"net/http"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type CleanupFunc func()

type Server struct {
	DB        *db.Pool
	LondonLoc *time.Location
}

func NewServer() (http.Handler, CleanupFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := db.NewFromEnv(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	return NewServerWithPool(pool)
}

func NewServerWithPool(pool *db.Pool) (http.Handler, CleanupFunc, error) {
	londonLoc, err := time.LoadLocation("Europe/London")
	if err != nil {
		log.Printf("warning: Europe/London not found, using UTC for magic link expiry: %v", err)
		londonLoc = time.UTC
	}

	s := &Server{DB: pool, LondonLoc: londonLoc}

	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(middleware.Logger())
	r.Use(middleware.SecurityHeaders())

	// Some clients (PDF copy, iOS "smart" typography) rewrite the "fi" in
	// "confirm" to a Unicode ligature, so magic links arrive as
	// "/magic-link/conﬁrm?token=..." and 404. On any otherwise-unmatched
	// request, normalise ligatures in the path and redirect to the canonical
	// URL, preserving the token query verbatim.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if canonical := canonicalizePath(req.URL.Path); canonical != req.URL.Path {
			target := canonical
			if req.URL.RawQuery != "" {
				target += "?" + req.URL.RawQuery
			}
			log.Printf("[router] normalised ligature path %q -> %q", req.URL.Path, canonical)
			http.Redirect(w, req, target, http.StatusFound)
			return
		}
		http.NotFound(w, req)
	})

	r.Get("/manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeFile(w, r, "static/manifest.webmanifest")
	})
	r.Get("/service-worker.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Service-Worker-Allowed", "/")
		http.ServeFile(w, r, "static/service-worker.js")
	})

	// static assets
	staticFS := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	r.Handle("/static/*", staticFS)
	imagesFS := http.StripPrefix("/images/", http.FileServer(http.Dir("images")))
	r.Handle("/images/*", imagesFS)

	// health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// public lookup API (used by entry page typeahead)
	r.With(middleware.RateLimit(60)).Get("/api/clubs/search", s.handleClubSearch())
	r.With(middleware.RateLimit(60)).Get("/api/teams", s.handleTeamsByClub())
	r.With(middleware.RateLimit(60)).Get("/api/captain", s.handleCaptainByTeam())
	r.With(middleware.RateLimit(60)).Post("/webhooks/aws/ses", s.handleSESEventWebhook())

	// public captain flow
	r.Get("/", s.handlePublicEntry())
	r.With(middleware.RateLimit(60)).Get("/submissions", s.handlePublicSubmissionsStatus())
	r.Get("/privacy", s.handlePrivacyNotice())
	r.Get("/retention", s.handleRetentionNotice())
	// Rate limit magic link requests per IP + club/team.
	r.With(middleware.RateLimit(15)).Post("/magic-link/request", s.handleMagicLinkRequest())
	// Magic link confirmation uses GET (intermediate page) then POST to redeem.
	r.Get("/magic-link/confirm", s.handleMagicLinkConfirm())
	r.Post("/magic-link/confirm", s.handleMagicLinkConfirm())
	r.Group(func(r chi.Router) {
		r.Use(middleware.CSRFMiddleware)
		r.Get("/access", s.handleMagicAccessCode())
		r.With(middleware.RateLimit(15)).Post("/access", s.handleMagicAccessCode())
	})

	// Captain form (protected with CSRF)
	r.Group(func(r chi.Router) {
		r.Use(middleware.CSRFMiddleware)
		r.Get("/captain/form", s.handleCaptainForm())
		r.Post("/captain/form/autosave", s.handleCaptainAutosave())
		r.Post("/captain/delegate/invite", s.handleCaptainDelegateInvite())
		r.Post("/captain/change-request", s.handleCaptainChangeRequest())
		r.Post("/captain/form/submit", s.handleCaptainSubmit())
	})

	// admin portal
	r.Mount("/admin", s.adminRouter())

	// internal n8n endpoints (HMAC protected)
	internalMux := chi.NewRouter()
	internalMux.Use(middleware.HMACVerifier(middleware.HMACConfig{}))
	internalMux.Post("/send-reminders", s.handleInternalSendReminders())
	internalMux.Post("/generate-sanctions", s.handleInternalGenerateSanctions())
	internalMux.Post("/generate-weekly-report", s.handleInternalGenerateWeeklyReport())
	internalMux.Post("/sync-league-fixtures", s.handleInternalSyncLeagueFixtures())
	internalMux.Post("/refresh-umpire-prefills", s.handleInternalRefreshUmpirePrefills())
	internalMux.Post("/preview-email", s.handleInternalPreviewEmail())
	r.Mount("/internal", internalMux)

	return r, func() { pool.Close() }, nil
}
