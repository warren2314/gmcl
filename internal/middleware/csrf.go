package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
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
			var err error
			token, err = generateCSRFToken()
			if err != nil {
				http.Error(w, "could not establish request protection", http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     csrfCookieName,
				Value:    token,
				Path:     "/",
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
		}
		ctx := context.WithValue(r.Context(), csrfKey, token)
		r = r.WithContext(ctx)

		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				_ = r.ParseMultipartForm(5 << 20)
			} else {
				_ = r.ParseForm() // safe even if already parsed
			}
			expected := CSRFToken(r)
			formToken := r.FormValue("csrf_token")
			if formToken == "" {
				formToken = r.Header.Get("X-CSRF-Token")
			}
			if !csrfTokenEqual(formToken, expected) {
				http.Error(w, "csrf validation failed", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func generateCSRFToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func csrfTokenEqual(actual, expected string) bool {
	if actual == "" || expected == "" || len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}
