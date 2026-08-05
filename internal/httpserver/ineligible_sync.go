package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"cricket-ground-feedback/internal/ineligible"
)

type ineligibleSyncResponse struct {
	ineligible.Summary
	Message string `json:"message,omitempty"`
}

// handleInternalSyncIneligibleReports is mounted only below the existing HMAC
// middleware. It calls the same exported service entry point used by an admin
// action; neither path creates a case or sends correspondence.
func (s *Server) handleInternalSyncIneligibleReports() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		summary, err := ineligible.SyncFromEnv(ctx, s.DB, ineligible.Trigger{Type: "n8n"})
		statusCode := http.StatusOK
		message := ""
		if err != nil {
			switch {
			case errors.Is(err, ineligible.ErrImportDisabled):
				statusCode = http.StatusServiceUnavailable
				message = "ineligible-player import is disabled"
			case errors.Is(err, ineligible.ErrSyncInProgress):
				statusCode = http.StatusConflict
				message = "an ineligible-player sync is already running"
			case errors.Is(err, ineligible.ErrBackfillPrerequisite):
				statusCode = http.StatusLocked
				message = "daily import is blocked until the named tracker reconciliation is signed off and applied"
			case errors.Is(err, ineligible.ErrGoogleImportRetired):
				statusCode = http.StatusOK
				message = "scheduled Google intake is retired after the 30-day native-form grace period"
			default:
				statusCode = http.StatusBadGateway
				message = "ineligible-player sync failed"
			}
			log.Printf("ineligible-player Google Form sync failed: run_id=%d status=%s: %v", summary.RunID, summary.Status, err)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(ineligibleSyncResponse{Summary: summary, Message: message})
	}
}
