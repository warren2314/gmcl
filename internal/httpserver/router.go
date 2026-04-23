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

	// public captain flow
	r.Get("/", s.handlePublicEntry())
	r.Get("/privacy", s.handlePrivacyNotice())
	r.Get("/retention", s.handleRetentionNotice())
	// Rate limit magic link requests per IP + club/team.
	r.With(middleware.RateLimit(15)).Post("/magic-link/request", s.handleMagicLinkRequest())
	// Magic link confirmation uses GET (intermediate page) then POST to redeem.
	r.Get("/magic-link/confirm", s.handleMagicLinkConfirm())
	r.Post("/magic-link/confirm", s.handleMagicLinkConfirm())

	// Captain form (protected with CSRF)
	r.Group(func(r chi.Router) {
		r.Use(middleware.CSRFMiddleware)
		r.Get("/captain/form", s.handleCaptainForm())
		r.Post("/captain/form/autosave", s.handleCaptainAutosave())
		r.Post("/captain/delegate/invite", s.handleCaptainDelegateInvite())
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
	internalMux.Post("/preview-email", s.handleInternalPreviewEmail())
	r.Mount("/internal", internalMux)

	return r, func() { pool.Close() }, nil
}
