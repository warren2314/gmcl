package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// HMACConfig holds settings for internal endpoint verification.
type HMACConfig struct {
	HeaderSignature string
	HeaderTimestamp string
	HeaderNonce     string
	MaxAge          time.Duration
}

var nonceCache sync.Map
var nonceCount int64

// HMACVerifier verifies an HMAC signature for internal n8n calls.
func HMACVerifier(cfg HMACConfig) func(http.Handler) http.Handler {
	if cfg.HeaderSignature == "" {
		cfg.HeaderSignature = "X-Signature"
	}
	if cfg.HeaderTimestamp == "" {
		cfg.HeaderTimestamp = "X-Timestamp"
	}
	if cfg.HeaderNonce == "" {
		cfg.HeaderNonce = "X-Nonce"
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 5 * time.Minute
	}

	go func(maxAge time.Duration) {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now().Unix()
			nonceCache.Range(func(key, value any) bool {
				if exp, ok := value.(int64); ok && exp < now {
					nonceCache.Delete(key)
					atomic.AddInt64(&nonceCount, -1)
				}
				return true
			})
		}
	}(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			secret := os.Getenv("N8N_HMAC_SECRET")
			if secret == "" {
				http.Error(w, "hmac not configured", http.StatusInternalServerError)
				return
			}

			// Accept simple bearer token as alternative to full HMAC (for n8n HTTP nodes).
			if auth := r.Header.Get("Authorization"); auth == "Bearer "+secret {
				next.ServeHTTP(w, r)
				return
			}

			sigHex := r.Header.Get(cfg.HeaderSignature)
			tsStr := r.Header.Get(cfg.HeaderTimestamp)
			nonce := r.Header.Get(cfg.HeaderNonce)
			if sigHex == "" || tsStr == "" || nonce == "" {
				http.Error(w, "missing signature", http.StatusUnauthorized)
				return
			}

			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				http.Error(w, "invalid timestamp", http.StatusUnauthorized)
				return
			}

			now := time.Now().Unix()
			if abs(now-ts) > int64(cfg.MaxAge.Seconds()) {
				http.Error(w, "request too old", http.StatusUnauthorized)
				return
			}

			// Simple nonce replay cache within window.
			nonceKey := nonce + "|" + tsStr
			if _, found := nonceCache.Load(nonceKey); found {
				http.Error(w, "replayed request", http.StatusUnauthorized)
				return
			}
			nonceCache.Store(nonceKey, now+int64(cfg.MaxAge.Seconds()))
			atomic.AddInt64(&nonceCount, 1)

			// enforce a soft cap on nonce entries
			if atomic.LoadInt64(&nonceCount) > 10000 {
				http.Error(w, "nonce cache capacity exceeded", http.StatusTooManyRequests)
				return
			}

			// Limit body size to avoid memory DoS in HMAC verification.
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			ct := r.Header.Get("Content-Type")
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s", tsStr, nonce, r.Method, r.URL.Path, ct, string(bodyBytes))))
			expected := mac.Sum(nil)

			sig, err := hex.DecodeString(sigHex)
			if err != nil {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}

			if !hmac.Equal(sig, expected) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

