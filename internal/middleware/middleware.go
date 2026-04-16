package middleware

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Logger provides simple structured request logging.
func Logger() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, ww.status, time.Since(start))
		})
	}
}

// SecurityHeaders sets a baseline of secure HTTP headers.
func SecurityHeaders() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-XSS-Protection", "0")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			if os.Getenv("ENABLE_HSTS") != "0" {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://unpkg.com https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data:; font-src 'self' https://cdn.jsdelivr.net; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
			if isSensitivePath(r.URL.Path) {
				w.Header().Set("Cache-Control", "no-store, max-age=0")
				w.Header().Set("Pragma", "no-cache")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isSensitivePath(path string) bool {
	return strings.HasPrefix(path, "/admin") ||
		strings.HasPrefix(path, "/captain") ||
		strings.HasPrefix(path, "/magic-link")
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

