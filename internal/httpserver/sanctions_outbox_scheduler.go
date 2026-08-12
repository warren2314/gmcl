package httpserver

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"
)

const (
	sanctionOutboxStartupDelay = 5 * time.Second
	sanctionOutboxInterval     = time.Minute
)

func sanctionOutboxSchedulerEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SANCTION_OUTBOX_SCHEDULER_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// startSanctionOutboxScheduler makes delivery independent of an external n8n
// schedule. The existing PostgreSQL advisory lock means this runner and n8n
// can safely overlap without sending the same outbox item concurrently.
func (s *Server) startSanctionOutboxScheduler(parent context.Context) context.CancelFunc {
	ctx, cancel := context.WithCancel(parent)
	if !sanctionOutboxSchedulerEnabled() {
		return cancel
	}
	run := func() {
		runCtx, runCancel := context.WithTimeout(ctx, 50*time.Second)
		defer runCancel()
		req := httptest.NewRequest(http.MethodPost, "/internal/process-sanction-outbox", nil).WithContext(runCtx)
		response := httptest.NewRecorder()
		s.handleInternalSanctionOutbox().ServeHTTP(response, req)
		if response.Code != http.StatusOK && response.Code != http.StatusConflict {
			log.Printf("in-process sanctions outbox run failed: status=%d body=%s", response.Code, strings.TrimSpace(response.Body.String()))
		}
	}
	go func() {
		initial := time.NewTimer(sanctionOutboxStartupDelay)
		defer initial.Stop()
		select {
		case <-ctx.Done():
			return
		case <-initial.C:
			run()
		}
		ticker := time.NewTicker(sanctionOutboxInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	log.Printf("in-process sanctions outbox enabled: startup_delay=%s interval=%s", sanctionOutboxStartupDelay, sanctionOutboxInterval)
	return cancel
}
