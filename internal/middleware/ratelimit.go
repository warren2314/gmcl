package middleware

import (
	"container/list"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	defaultRateLimitMaxBuckets = 10_000
	defaultRateLimitBucketTTL  = 10 * time.Minute
)

type rateLimiter struct {
	mu           sync.Mutex
	store        map[string]*bucket
	recency      *list.List
	maxBuckets   int
	bucketTTL    time.Duration
	maxTokens    float64
	refillPerSec float64
	now          func() time.Time
}

type bucket struct {
	tokens         float64
	lastRefillTime time.Time
	recencyElement *list.Element
}

// RateLimit returns middleware that limits requests per IP+path.
func RateLimit(maxPerMinute float64) func(http.Handler) http.Handler {
	return newRateLimitMiddleware(
		maxPerMinute,
		defaultRateLimitMaxBuckets,
		defaultRateLimitBucketTTL,
		time.Now,
	)
}

func newRateLimitMiddleware(
	maxPerMinute float64,
	maxBuckets int,
	bucketTTL time.Duration,
	now func() time.Time,
) func(http.Handler) http.Handler {
	if maxPerMinute <= 0 {
		maxPerMinute = 30
	}
	if maxBuckets <= 0 {
		maxBuckets = defaultRateLimitMaxBuckets
	}
	if bucketTTL <= 0 {
		bucketTTL = defaultRateLimitBucketTTL
	}
	if now == nil {
		now = time.Now
	}
	rl := &rateLimiter{
		store:        make(map[string]*bucket),
		recency:      list.New(),
		maxBuckets:   maxBuckets,
		bucketTTL:    bucketTTL,
		maxTokens:    maxPerMinute,
		refillPerSec: maxPerMinute / 60.0,
		now:          now,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			key := ip + "|" + r.URL.Path

			rl.mu.Lock()
			currentTime := rl.now()
			rl.removeExpiredLocked(currentTime)
			b, ok := rl.store[key]
			if !ok {
				if len(rl.store) >= rl.maxBuckets {
					rl.removeOldestLocked()
				}
				b = &bucket{
					tokens:         rl.maxTokens,
					lastRefillTime: currentTime,
				}
				b.recencyElement = rl.recency.PushBack(key)
				rl.store[key] = b
			} else {
				rl.recency.MoveToBack(b.recencyElement)
			}
			elapsed := currentTime.Sub(b.lastRefillTime).Seconds()
			if elapsed > 0 {
				b.tokens += elapsed * rl.refillPerSec
				if b.tokens > rl.maxTokens {
					b.tokens = rl.maxTokens
				}
				b.lastRefillTime = currentTime
			} else if elapsed < 0 {
				// Recover safely if the wall clock moves backwards.
				b.lastRefillTime = currentTime
			}
			if b.tokens < 1 {
				retryAfter := int(math.Ceil((1 - b.tokens) / rl.refillPerSec))
				if retryAfter < 1 {
					retryAfter = 1
				}
				rl.mu.Unlock()
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			b.tokens--
			rl.mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
}

func (limiter *rateLimiter) removeExpiredLocked(now time.Time) {
	for {
		oldest := limiter.recency.Front()
		if oldest == nil {
			return
		}
		key, _ := oldest.Value.(string)
		entry := limiter.store[key]
		if entry != nil && now.Sub(entry.lastRefillTime) <= limiter.bucketTTL {
			return
		}
		limiter.recency.Remove(oldest)
		delete(limiter.store, key)
	}
}

func (limiter *rateLimiter) removeOldestLocked() {
	oldest := limiter.recency.Front()
	if oldest == nil {
		return
	}
	key, _ := oldest.Value.(string)
	limiter.recency.Remove(oldest)
	delete(limiter.store, key)
}
