package portal

import (
	"bytes"
	"testing"
)

func TestOpaqueTokenRoundTrip(t *testing.T) {
	token, storedHash, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("token was empty")
	}
	calculated, err := HashOpaqueToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedHash[:], calculated[:]) {
		t.Fatal("persisted and calculated hashes differ")
	}
}

func TestOpaqueTokenRejectsMalformedInput(t *testing.T) {
	for _, token := range []string{"", "not-base64!", "c2hvcnQ"} {
		if _, err := HashOpaqueToken(token); err == nil {
			t.Fatalf("HashOpaqueToken(%q) unexpectedly succeeded", token)
		}
	}
}
