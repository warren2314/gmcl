package middleware

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

const csrfCookieName = "csrf_token"

// CSRFCookieName is exported so handlers can read the cookie name when embedding tokens.
const CSRFCookieName = csrfCookieName

type csrfContextKey struct{}

var csrfKey = csrfContextKey{}

// CSRFToken returns the CSRF token associated with the request, if any.
func CSRFToken(r *http.Request) string {
	if v := r.Context().Value(csrfKey); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if c, err := r.Cookie(csrfCookieName); err == nil {
		return c.Value
	}
	return ""
}

// CSRFMiddleware enforces a double-submit CSRF token for state-changing admin routes.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ensure token cookie exists and store it in context.
		var token string
		if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
			token = c.Value
		} else {
			token = generateCSRFToken()
			http.SetCookie(w, &http.Cookie{
				Name:     csrfCookieName,
				Value:    token,
				Path:     "/",
				Secure:   true,
				HttpOnly: false, // must be readable by forms / JS
				SameSite: http.SameSiteLaxMode,
			})
		}
		ctx := context.WithValue(r.Context(), csrfKey, token)
		r = r.WithContext(ctx)

		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			_ = r.ParseForm() // safe even if already parsed
			expected := CSRFToken(r)
			formToken := r.FormValue("csrf_token")
			if formToken == "" {
				formToken = r.Header.Get("X-CSRF-Token")
			}
			if formToken == "" || expected == "" || formToken != expected {
				http.Error(w, "csrf validation failed", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func generateCSRFToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

