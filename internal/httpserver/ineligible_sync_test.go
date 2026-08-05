package httpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

func TestInternalIneligibleSyncRouteRequiresHMACAndHonorsKillSwitch(t *testing.T) {
	t.Setenv("N8N_HMAC_SECRET", "test-internal-secret")
	t.Setenv("INELIGIBLE_IMPORT_ENABLED", "false")
	s := &Server{}
	router := chi.NewRouter()
	router.Use(middleware.HMACVerifier(middleware.HMACConfig{}))
	router.Post("/internal/sync-ineligible-reports", s.handleInternalSyncIneligibleReports())

	unsigned := httptest.NewRequest(http.MethodPost, "/internal/sync-ineligible-reports", nil)
	unsignedResult := httptest.NewRecorder()
	router.ServeHTTP(unsignedResult, unsigned)
	if unsignedResult.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned status: got %d, want %d", unsignedResult.Code, http.StatusUnauthorized)
	}

	signed := httptest.NewRequest(http.MethodPost, "/internal/sync-ineligible-reports", nil)
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := "sync-test-nonce"
	mac := hmac.New(sha256.New, []byte("test-internal-secret"))
	_, _ = mac.Write([]byte(timestamp + "|" + nonce + "|POST|/internal/sync-ineligible-reports||"))
	signed.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	signed.Header.Set("X-Timestamp", timestamp)
	signed.Header.Set("X-Nonce", nonce)
	signedResult := httptest.NewRecorder()
	router.ServeHTTP(signedResult, signed)
	if signedResult.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status: got %d, want %d; body=%s", signedResult.Code, http.StatusServiceUnavailable, signedResult.Body.String())
	}
	var response ineligibleSyncResponse
	if err := json.Unmarshal(signedResult.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Message != "ineligible-player import is disabled" {
		t.Fatalf("message: %q", response.Message)
	}
}
