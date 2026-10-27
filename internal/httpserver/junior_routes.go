package httpserver

import (
	"net/http"
	"strings"

	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// This runs before CSRF parses multipart uploads. The bounded body also covers
// result CSV imports; handlers apply their narrower per-file validation.
func juniorRequestLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/juniors") || strings.HasPrefix(r.URL.Path, "/junior") {
			r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) juniorAdminRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(s.requireJuniorStaff())
	r.Get("/", s.handleJuniorAdminHome())
	r.Get("/staff-access", s.HandleJuniorStaffAccess())
	r.Post("/staff-access", s.HandleJuniorStaffAccessSave())
	r.Get("/clubs", s.HandleJuniorAdminRecords())
	r.Get("/contacts", s.handleJuniorContactDirectory())
	r.Post("/clubs", s.HandleJuniorAdminClubSave())
	r.Post("/seasons", s.HandleJuniorAdminSeasonSave())
	r.Get("/clubs/{clubID}", s.handleJuniorClubProfile())
	r.Post("/clubs/{clubID}/participation", s.HandleJuniorAdminParticipationSave())
	r.Post("/clubs/{clubID}/status", s.handleJuniorClubStatus())
	r.Post("/clubs/{clubID}/teams", s.HandleJuniorAdminTeamSave())
	r.Post("/clubs/{clubID}/contacts", s.HandleJuniorAdminContactSave())
	r.Post("/clubs/{clubID}/contact-roles/{roleID}/end", s.HandleJuniorAdminContactRoleEnd())
	r.Post("/clubs/{clubID}/badge", s.HandleJuniorBadgeUpload())
	r.Get("/clubs/{clubID}/badges/{badgeID}", s.HandleJuniorBadgeRead())
	r.Post("/clubs/{clubID}/invite", s.handleAdminJuniorInvite())
	r.Post("/clubs/{clubID}/revoke", s.handleAdminJuniorRevoke())
	r.With(s.requireJuniorPreview()).Get("/clubs/{clubID}/preview", s.handleJuniorPreview())
	r.With(s.requireJuniorPreview()).Get("/clubs/{clubID}/preview/badges/{badgeID}", s.HandleJuniorBadgeRead())
	r.Get("/roster", s.HandleJuniorRosterPreview())
	r.Post("/roster", s.HandleJuniorRosterUpload())
	r.Get("/roster/{importID}", s.HandleJuniorRosterPreview())
	r.Post("/roster/{importID}/review", s.HandleJuniorRosterReview())
	r.Post("/roster/{importID}/apply", s.HandleJuniorRosterApply())
	r.Get("/forms", s.handleJuniorForms())
	r.Post("/forms", s.handleJuniorForms())
	r.Get("/forms/{formID}", s.handleJuniorForms())
	r.Post("/forms/{formID}", s.handleJuniorForms())
	r.Get("/forms/assignments/{assignmentID}", s.handleJuniorFormAssignment())
	r.Post("/forms/assignments/{assignmentID}", s.handleJuniorFormAssignment())
	r.Get("/competitions", s.handleJuniorCompetitions())
	r.Post("/competitions", s.handleJuniorCompetitions())
	r.Get("/competitions/{id}", s.handleJuniorCompetition())
	r.Post("/competitions/{id}", s.handleJuniorCompetition())
	r.Get("/fixtures", s.handleJuniorFixtures())
	r.Get("/results", s.handleJuniorResults())
	r.Post("/results", s.handleJuniorResults())
	r.Get("/concessions", s.handleJuniorConcessions())
	r.Post("/concessions", s.handleJuniorConcessions())
	r.Get("/messages", s.handleAdminJuniorMessages())
	r.Post("/messages", s.handleAdminJuniorMessagesPost())
	return r
}

func (s *Server) juniorPortalRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(juniorRequestLimit)
	r.Use(s.requireJuniorPortalEnabled())
	r.Use(middleware.RateLimit(30))
	r.Use(middleware.CSRFMiddleware)
	r.Get("/login", s.handleJuniorLoginGet())
	r.Post("/login", s.handleJuniorLoginPost())
	r.Get("/access", s.handleJuniorAccessGet())
	r.Post("/access", s.handleJuniorAccessPost())
	r.Group(func(r chi.Router) {
		r.Use(s.requireJuniorLead())
		r.Get("/", s.handleJuniorClubProfile())
		r.Post("/logout", s.handleJuniorLogout())
		r.Get("/contacts", s.HandleJuniorPortalContacts())
		r.Post("/contacts", s.HandleJuniorPortalContactSave())
		r.Post("/contact-roles/{roleID}/end", s.HandleJuniorPortalContactRoleEnd())
		r.Post("/teams", s.HandleJuniorAdminTeamSave())
		r.Post("/badge", s.HandleJuniorBadgeUpload())
		r.Get("/badges/{badgeID}", s.HandleJuniorBadgeRead())
		r.Get("/forms", s.handleJuniorClubForms())
		r.Post("/forms", s.handleJuniorClubForms())
		r.Get("/forms/{assignmentID}", s.handleJuniorClubForms())
		r.Post("/forms/{assignmentID}", s.handleJuniorClubForms())
		r.Get("/fixtures", s.handleJuniorFixtures())
		r.Get("/concessions", s.handleJuniorConcessions())
		r.Get("/messages", s.handleJuniorClubMessages())
	})
	return r
}

func (s *Server) handleJuniorPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("view") {
		case "contacts":
			s.HandleJuniorPortalContacts()(w, r)
		case "forms":
			s.handleJuniorClubForms()(w, r)
		case "fixtures":
			s.handleJuniorFixtures()(w, r)
		case "concessions":
			s.handleJuniorConcessions()(w, r)
		case "messages":
			s.handleJuniorClubMessages()(w, r)
		case "":
			s.handleJuniorClubProfile()(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}
