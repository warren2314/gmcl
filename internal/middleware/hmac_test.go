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
	nonce := "abc123"

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
