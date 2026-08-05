package middleware

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"crypto/hmac"
	"crypto/sha256"
)

func TestHMACVerifier(t *testing.T) {
	os.Setenv("N8N_HMAC_SECRET", "testsecret")

	body := []byte(`{"season_id":1}`)
	ts := time.Now().Unix()
	nonce := "abc12345"

	tsStr := fmt.Sprintf("%d", ts)
	ct := ""
	mac := hmac.New(sha256.New, []byte("testsecret"))
	mac.Write([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s", tsStr, nonce, http.MethodPost, "/internal/test", ct, string(body))))
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/internal/test", bytes.NewReader(body))
	req.Header.Set("X-Signature", sig)
	req.Header.Set("X-Timestamp", tsStr)
	req.Header.Set("X-Nonce", nonce)

	rr := httptest.NewRecorder()
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	mw := HMACVerifier(HMACConfig{})(handler)
	mw.ServeHTTP(rr, req)

	if !called {
		t.Fatalf("expected handler to be called")
	}
}

func TestHMACVerifierRejectsBearerUnlessExplicitlyLegacy(t *testing.T) {
	t.Setenv("N8N_HMAC_SECRET", "testsecret")
	req := httptest.NewRequest(http.MethodPost, "/internal/test", nil)
	req.Header.Set("Authorization", "Bearer testsecret")
	rr := httptest.NewRecorder()
	HMACVerifier(HMACConfig{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("signature-only endpoint accepted a reusable bearer token")
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHMACVerifierInvalidSignatureDoesNotConsumeNonce(t *testing.T) {
	t.Setenv("N8N_HMAC_SECRET", "testsecret")
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := "retry-safe-nonce"
	bad := httptest.NewRequest(http.MethodPost, "/internal/retry", nil)
	bad.Header.Set("X-Signature", "00")
	bad.Header.Set("X-Timestamp", timestamp)
	bad.Header.Set("X-Nonce", nonce)
	handler := HMACVerifier(HMACConfig{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	badResult := httptest.NewRecorder()
	handler.ServeHTTP(badResult, bad)
	if badResult.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature status: %d", badResult.Code)
	}
	mac := hmac.New(sha256.New, []byte("testsecret"))
	mac.Write([]byte(timestamp + "|" + nonce + "|POST|/internal/retry||"))
	good := httptest.NewRequest(http.MethodPost, "/internal/retry", nil)
	good.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	good.Header.Set("X-Timestamp", timestamp)
	good.Header.Set("X-Nonce", nonce)
	goodResult := httptest.NewRecorder()
	handler.ServeHTTP(goodResult, good)
	if goodResult.Code != http.StatusNoContent {
		t.Fatalf("valid retry status: got %d, want %d", goodResult.Code, http.StatusNoContent)
	}
}

func TestHMACNonceExpiryCoversAllowedFutureClockSkew(t *testing.T) {
	now := int64(1_000)
	futureTimestamp := now + 240
	if got, want := hmacNonceExpiry(now, futureTimestamp, 5*time.Minute), futureTimestamp+300; got != want {
		t.Fatalf("expiry: got %d, want %d", got, want)
	}
}
