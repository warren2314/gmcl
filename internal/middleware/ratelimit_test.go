package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimitRefillsAndReturnsRetryAfter(t *testing.T) {
	currentTime := time.Date(2026, time.July, 27, 8, 30, 0, 0, time.UTC)
	handler := newRateLimitMiddleware(
		2,
		100,
		time.Minute,
		func() time.Time { return currentTime },
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for attempt := 1; attempt <= 2; attempt++ {
		if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusNoContent {
			t.Fatalf("attempt %d status = %d", attempt, status)
		}
	}
	limited := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status = %d", limited.Code)
	}
	if got := limited.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("retry after = %q, want 30", got)
	}

	currentTime = currentTime.Add(30 * time.Second)
	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusNoContent {
		t.Fatalf("refilled status = %d", status)
	}
}

func TestRateLimitIsolatesIPAndPath(t *testing.T) {
	currentTime := time.Date(2026, time.July, 27, 8, 30, 0, 0, time.UTC)
	handler := newRateLimitMiddleware(
		1,
		100,
		time.Minute,
		func() time.Time { return currentTime },
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, request := range []struct {
		remote string
		path   string
	}{
		{"192.0.2.1:1000", "/portal/login"},
		{"192.0.2.1:1000", "/portal/auth/callback"},
		{"192.0.2.2:1000", "/portal/login"},
	} {
		if status := rateLimitRequest(handler, request.remote, request.path).Code; status != http.StatusNoContent {
			t.Fatalf("%s %s status = %d", request.remote, request.path, status)
		}
	}
	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusTooManyRequests {
		t.Fatalf("repeated key status = %d", status)
	}
}

func TestRateLimitEvictsLeastRecentlyUsedBucketAtCapacity(t *testing.T) {
	currentTime := time.Date(2026, time.July, 27, 8, 30, 0, 0, time.UTC)
	handler := newRateLimitMiddleware(
		1,
		2,
		time.Hour,
		func() time.Time { return currentTime },
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for index, path := range []string{"/one", "/two", "/three"} {
		recorder := rateLimitRequest(handler, "192.0.2.1:1000", path)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("path %d status = %d", index, recorder.Code)
		}
		currentTime = currentTime.Add(time.Second)
	}
	// /one was the least recently used entry and must have been evicted,
	// making this a new full bucket rather than an indefinitely retained denial.
	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/one").Code; status != http.StatusNoContent {
		t.Fatalf("evicted key status = %d", status)
	}
}

func TestRateLimitExpiresIdleBuckets(t *testing.T) {
	currentTime := time.Date(2026, time.July, 27, 8, 30, 0, 0, time.UTC)
	handler := newRateLimitMiddleware(
		1,
		100,
		10*time.Second,
		func() time.Time { return currentTime },
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusNoContent {
		t.Fatalf("initial status = %d", status)
	}
	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusTooManyRequests {
		t.Fatalf("pre-expiry status = %d", status)
	}
	currentTime = currentTime.Add(11 * time.Second)
	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusNoContent {
		t.Fatalf("post-expiry status = %d", status)
	}
}

func TestRateLimitRecoversAfterClockMovesBackwards(t *testing.T) {
	currentTime := time.Date(2026, time.July, 27, 8, 30, 0, 0, time.UTC)
	handler := newRateLimitMiddleware(
		1,
		100,
		time.Hour,
		func() time.Time { return currentTime },
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusNoContent {
		t.Fatalf("initial status = %d", status)
	}
	currentTime = currentTime.Add(-5 * time.Minute)
	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusTooManyRequests {
		t.Fatalf("rollback status = %d", status)
	}
	currentTime = currentTime.Add(time.Minute)
	if status := rateLimitRequest(handler, "192.0.2.1:1000", "/portal/login").Code; status != http.StatusNoContent {
		t.Fatalf("post-rollback refill status = %d", status)
	}
}

func TestRateLimitSerializesConcurrentRequests(t *testing.T) {
	currentTime := time.Date(2026, time.July, 27, 8, 30, 0, 0, time.UTC)
	handler := newRateLimitMiddleware(
		10,
		100,
		time.Minute,
		func() time.Time { return currentTime },
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	var allowed atomic.Int64
	var limited atomic.Int64
	var unexpected atomic.Int64
	var waitGroup sync.WaitGroup
	for index := 0; index < 100; index++ {
		waitGroup.Add(1)
		go func(port int) {
			defer waitGroup.Done()
			recorder := rateLimitRequest(
				handler,
				"192.0.2.1:"+strconv.Itoa(10_000+port),
				"/portal/login",
			)
			switch recorder.Code {
			case http.StatusNoContent:
				allowed.Add(1)
			case http.StatusTooManyRequests:
				limited.Add(1)
			default:
				unexpected.Add(1)
			}
		}(index)
	}
	waitGroup.Wait()
	if allowed.Load() != 10 || limited.Load() != 90 || unexpected.Load() != 0 {
		t.Fatalf(
			"allowed=%d limited=%d unexpected=%d",
			allowed.Load(),
			limited.Load(),
			unexpected.Load(),
		)
	}
}

func rateLimitRequest(handler http.Handler, remoteAddr, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
