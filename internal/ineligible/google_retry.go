package ineligible

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const googleRequestAttempts = 3

// doGoogleRequest retries only bounded, transient failures. Every request used
// here is read-only or an idempotent OAuth assertion exchange.
func doGoogleRequest(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < googleRequestAttempts; attempt++ {
		attemptRequest := request.Clone(ctx)
		if attempt > 0 && request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, err
			}
			attemptRequest.Body = body
		}
		response, err := client.Do(attemptRequest)
		if err == nil && !retryableGoogleStatus(response.StatusCode) {
			return response, nil
		}
		if err == nil && attempt == googleRequestAttempts-1 {
			return response, nil
		}
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || attempt == googleRequestAttempts-1 {
				return nil, err
			}
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
			response.Body.Close()
		}
		delay := time.Duration(100*(1<<attempt)) * time.Millisecond
		if err == nil {
			if retryAfter := parseRetryAfter(response.Header.Get("Retry-After")); retryAfter > delay {
				delay = retryAfter
			}
		}
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func retryableGoogleStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError ||
		status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if delay := time.Until(at); delay > 0 {
			return delay
		}
	}
	return 0
}
