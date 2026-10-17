package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cricket-ground-feedback/internal/junior"
	"cricket-ground-feedback/internal/middleware"
	"github.com/go-chi/chi/v5"
)

const juniorSessionCookie = "junior_sess"

type juniorPrincipalContextKey struct{}

func juniorPrincipalForRequest(r *http.Request) junior.Principal {
	if p, ok := r.Context().Value(juniorPrincipalContextKey{}).(junior.Principal); ok {
		return p
	}
	// A staff cookie cannot silently become a club identity on public routes.
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		return junior.Principal{AdminID: adminIDForRequest(r)}
	}
	return junior.Principal{}
}
func (s *Server) juniorPrincipal(r *http.Request) junior.Principal {
	return juniorPrincipalForRequest(r)
}
func (s *Server) juniorAccessService() *junior.Service {
	service := junior.NewService(s.DB)
	service.AccessMailer = s.JuniorAccessMailer
	return service
}

func (s *Server) requireJuniorPortalEnabled() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			juniorPrivateResponse(w)
			if !junior.PortalEnabled() {
				http.Error(w, "The junior portal is not open yet. Please contact your league administrator.", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) requireJuniorLead() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			juniorPrivateResponse(w)
			if !junior.PortalEnabled() {
				http.Error(w, "The junior portal is not open yet.", http.StatusServiceUnavailable)
				return
			}
			cookie, err := r.Cookie(juniorSessionCookie)
			if err != nil {
				http.Redirect(w, r, "/junior/login", http.StatusSeeOther)
				return
			}
			p, err := s.juniorAccessService().SessionPrincipal(r.Context(), cookie.Value)
			if err != nil {
				if !errors.Is(err, junior.ErrForbidden) {
					juniorHTTPError(w, r, err)
					return
				}
				clearJuniorSession(w)
				http.Redirect(w, r, "/junior/login", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), juniorPrincipalContextKey{}, p)))
		})
	}
}

func (s *Server) requireJuniorStaff() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			juniorPrivateResponse(w)
			p := junior.Principal{AdminID: adminIDForRequest(r)}
			if p.AdminID <= 0 {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			if err := junior.Authorize(r.Context(), s.DB, p, 0, false); err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), juniorPrincipalContextKey{}, p)))
		})
	}
}

// A preview retains the staff identity. Services also reject ReadOnly writes,
// including forged POSTs; no junior account/session cookie is created here.
func (s *Server) requireJuniorPreview() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			juniorPrivateResponse(w)
			if r.Method != http.MethodGet {
				http.Error(w, "Read-only preview", http.StatusMethodNotAllowed)
				return
			}
			p := junior.Principal{AdminID: adminIDForRequest(r)}
			clubID := juniorAccessClubID(r)
			if p.AdminID <= 0 || clubID <= 0 {
				juniorHTTPError(w, r, junior.ErrForbidden)
				return
			}
			if err := junior.Authorize(r.Context(), s.DB, p, clubID, false); err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			var exists bool
			if err := s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM clubs WHERE id=$1)`, clubID).Scan(&exists); err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			if !exists {
				juniorHTTPError(w, r, junior.ErrNotFound)
				return
			}
			p.ClubID, p.ReadOnly = clubID, true
			if err := junior.Audit(r.Context(), s.DB, p, clubID, "club", int64(clubID), "staff_preview", map[string]any{"view": r.URL.Query().Get("view")}); err != nil {
				juniorHTTPError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), juniorPrincipalContextKey{}, p)))
		})
	}
}

func juniorAccessClubID(r *http.Request) int32 {
	id, err := strconv.ParseInt(chi.URLParam(r, "clubID"), 10, 32)
	if err != nil || id <= 0 {
		return 0
	}
	return int32(id)
}
func juniorPrivateResponse(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
func clearJuniorSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: juniorSessionCookie, Value: "", Path: "/junior", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func juniorAccessPageStart(w http.ResponseWriter, title string) {
	juniorPrivateResponse(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Access-link pages need no JavaScript. Do not expose raw confirmation
	// tokens to the third-party scripts used by the general page layout.
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s — GMCL</title><link href="%s" rel="stylesheet"><link href="/static/css/brand.css?v=%s" rel="stylesheet"></head><body>`, escapeHTML(title), bootstrapCSS, brandAssetVersion)
	fmt.Fprintf(w, `<main class="container py-5" style="max-width:680px"><h1 class="h2 mb-3">%s</h1>`, escapeHTML(title))
}
func juniorAccessPageEnd(w http.ResponseWriter) { fmt.Fprint(w, `</main></body></html>`) }
func (s *Server) handleJuniorLoginGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		juniorAccessPageStart(w, "Junior Lead sign-in")
		fmt.Fprint(w, `<p>Use the email address invited by your league administrator. We will email a one-use sign-in link for your club.</p>`)
		fmt.Fprintf(w, `<form method="POST" action="/junior/login"><input type="hidden" name="csrf_token" value="%s"><label class="form-label" for="junior-email">Email address</label><input class="form-control mb-3" id="junior-email" name="email" type="email" maxlength="254" autocomplete="email" required><button class="btn btn-primary">Email sign-in link</button></form><p class="text-muted mt-3">If you have changed roles or need an invitation, contact your league administrator.</p>`, escapeHTML(middleware.CSRFToken(r)))
		juniorAccessPageEnd(w)
	}
}
func (s *Server) handleJuniorLoginPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The same response covers unknown accounts, throttling and delivery errors.
		// Staff can inspect delivery failures; public users cannot enumerate leads.
		_ = s.juniorAccessService().RequestLogin(r.Context(), r.FormValue("email"))
		juniorAccessPageStart(w, "Check your email")
		fmt.Fprint(w, `<p>If this address has active Junior Lead access and email is available, a sign-in link has been sent. Check your junk folder. Please wait a minute before trying again.</p><p>If no message arrives, contact your league administrator to check your access and email setup.</p><a class="btn btn-outline-secondary" href="/junior/login">Return to sign-in</a>`)
		juniorAccessPageEnd(w)
	}
}
func (s *Server) handleJuniorAccessGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("token")
		if len(raw) != 43 {
			juniorHTTPError(w, r, junior.ErrForbidden)
			return
		}
		// A GET never consumes a token: mail scanners may visit links automatically.
		juniorAccessPageStart(w, "Confirm junior portal access")
		fmt.Fprintf(w, `<p>Continue only if you requested this sign-in link or were invited to be your club's Junior Lead. The link can be used once.</p><form method="POST" action="/junior/access"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="token" value="%s"><button class="btn btn-primary">Confirm and continue</button></form>`, escapeHTML(middleware.CSRFToken(r)), escapeHTML(raw))
		juniorAccessPageEnd(w)
	}
}
func (s *Server) handleJuniorAccessPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		juniorPrivateResponse(w)
		grant, err := s.juniorAccessService().RedeemAccess(r.Context(), r.FormValue("token"))
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: juniorSessionCookie, Value: grant.Token, Path: "/junior", Expires: grant.ExpiresAt, MaxAge: int(junior.SessionLifetime.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/junior", http.StatusSeeOther)
	}
}
func (s *Server) handleJuniorLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(juniorSessionCookie); err == nil {
			if err = s.juniorAccessService().Logout(r.Context(), cookie.Value); err != nil {
				juniorHTTPError(w, r, err)
				return
			}
		}
		clearJuniorSession(w)
		http.Redirect(w, r, "/junior/login", http.StatusSeeOther)
	}
}

func (s *Server) handleAdminJuniorInvite() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clubID := juniorAccessClubID(r)
		if clubID == 0 {
			juniorHTTPError(w, r, junior.ErrNotFound)
			return
		}
		if err := s.juniorAccessService().InviteLead(r.Context(), s.juniorPrincipal(r), clubID, r.FormValue("email")); err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/juniors/clubs/%d?success=%s", clubID, url.QueryEscape("Invitation sent. Existing access stays active until the new lead accepts.")), http.StatusSeeOther)
	}
}
func (s *Server) handleAdminJuniorRevoke() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clubID := juniorAccessClubID(r)
		if clubID == 0 {
			juniorHTTPError(w, r, junior.ErrNotFound)
			return
		}
		id, err := strconv.ParseInt(r.FormValue("membership_id"), 10, 64)
		service := s.juniorAccessService()
		if err == nil && id > 0 {
			err = service.RevokeLead(r.Context(), s.juniorPrincipal(r), clubID, id)
		} else {
			id, err = strconv.ParseInt(r.FormValue("invite_id"), 10, 64)
			if err == nil && id > 0 {
				err = service.RevokeInvite(r.Context(), s.juniorPrincipal(r), clubID, id)
			} else {
				err = junior.ErrValidation
			}
		}
		if err != nil {
			juniorHTTPError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/juniors/clubs/%d?success=%s", clubID, url.QueryEscape("Access revoked.")), http.StatusSeeOther)
	}
}
