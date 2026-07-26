package portal

import (
	"crypto/sha256"
	"testing"
	"time"
)

func TestOIDCConfigValidation(t *testing.T) {
	valid := OIDCConfig{
		Enabled:          true,
		IssuerURL:        "https://identity.example",
		ClientID:         "gmcl-portal",
		RedirectURL:      "https://test.gmcl.example/portal/auth/callback",
		DiscoveryTimeout: 10 * time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid OIDC config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*OIDCConfig)
	}{
		{"missing issuer", func(c *OIDCConfig) { c.IssuerURL = "" }},
		{"insecure issuer", func(c *OIDCConfig) { c.IssuerURL = "http://identity.example" }},
		{"insecure redirect", func(c *OIDCConfig) { c.RedirectURL = "http://test.gmcl.example/portal/auth/callback" }},
		{"wrong callback", func(c *OIDCConfig) { c.RedirectURL = "https://test.gmcl.example/callback" }},
		{"issuer query", func(c *OIDCConfig) { c.IssuerURL += "?tenant=1" }},
		{"long discovery", func(c *OIDCConfig) { c.DiscoveryTimeout = time.Minute }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			tt.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid OIDC config unexpectedly passed validation")
			}
		})
	}
}

func TestDisabledOIDCConfigNeedsNoProviderSecrets(t *testing.T) {
	if err := (OIDCConfig{}).Validate(); err != nil {
		t.Fatalf("disabled OIDC config rejected: %v", err)
	}
}

func TestVerifyNonce(t *testing.T) {
	expected := sha256.Sum256([]byte("correct nonce"))
	if !verifyNonce("correct nonce", expected) {
		t.Fatal("correct nonce rejected")
	}
	if verifyNonce("wrong nonce", expected) {
		t.Fatal("wrong nonce accepted")
	}
	if verifyNonce("", expected) {
		t.Fatal("missing nonce accepted")
	}
}

func TestPKCES256ChallengeRFC7636Example(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := pkceS256Challenge(verifier); got != want {
		t.Fatalf("PKCE challenge = %q, want %q", got, want)
	}
}
