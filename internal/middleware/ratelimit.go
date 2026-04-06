package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// simple in-memory token bucket keyed by IP+path for MVP.

type rateLimiter struct {
	mu    sync.Mutex
	store map[string]*bucket
}

type bucket struct {
	tokens         float64
	lastRefillTime time.Time
}

// RateLimit returns middleware that limits requests per IP+path.
func RateLimit(maxPerMinute float64) func(http.Handler) http.Handler {
	rl := &rateLimiter{
		store: make(map[string]*bucket),
	}
	if maxPerMinute <= 0 {
		maxPerMinute = 30
	}
	refillRate := maxPerMinute / 60.0

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			key := ip + "|" + r.URL.Path

			rl.mu.Lock()
			b, ok := rl.store[key]
			now := time.Now()
			if !ok {
				b = &bucket{tokens: maxPerMinute, lastRefillTime: now}
				rl.store[key] = b
			}
			elapsed := now.Sub(b.lastRefillTime).Seconds()
			if elapsed > 0 {
				b.tokens += elapsed * refillRate
				if b.tokens > maxPerMinute {
					b.tokens = maxPerMinute
				}
				b.lastRefillTime = now
			}
			if b.tokens < 1 {
				rl.mu.Unlock()
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			b.tokens--
			rl.mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
}

