package portal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const sessionTokenBytes = 32

// NewOpaqueToken returns a high-entropy bearer token and the one-way digest
// that may be persisted. Raw session or invitation tokens are never stored.
func NewOpaqueToken() (string, [sha256.Size]byte, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", [sha256.Size]byte{}, fmt.Errorf("generate secure token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, sha256.Sum256([]byte(token)), nil
}

func HashOpaqueToken(token string) ([sha256.Size]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != sessionTokenBytes {
		return [sha256.Size]byte{}, fmt.Errorf("invalid opaque token")
	}
	return sha256.Sum256([]byte(token)), nil
}
