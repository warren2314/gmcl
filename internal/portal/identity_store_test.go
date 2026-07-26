package portal

import (
	"crypto/sha256"
	"testing"
	"time"
)

func TestIdentityClaimsValidation(t *testing.T) {
	valid := IdentityClaims{
		Issuer:        "https://identity.example",
		Subject:       "subject-123",
		DisplayName:   "A Club Official",
		Email:         "official@example.org",
		EmailVerified: true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid claims rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*IdentityClaims)
	}{
		{"missing issuer", func(c *IdentityClaims) { c.Issuer = "" }},
		{"missing subject", func(c *IdentityClaims) { c.Subject = "" }},
		{"missing display name", func(c *IdentityClaims) { c.DisplayName = "" }},
		{"invalid email", func(c *IdentityClaims) { c.Email = "not an email" }},
		{"display formatted email", func(c *IdentityClaims) { c.Email = "Official <official@example.org>" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := valid
			tt.mutate(&claims)
			if err := claims.Validate(); err == nil {
				t.Fatal("invalid claims unexpectedly passed validation")
			}
		})
	}
}

func TestSafePortalReturnTo(t *testing.T) {
	for _, value := range []string{"/portal", "/portal/", "/portal/reports?season=2026"} {
		if !safePortalReturnTo(value) {
			t.Fatalf("safe return path %q rejected", value)
		}
	}
	for _, value := range []string{"", "/", "//evil.example", "https://evil.example", "/admin"} {
		if safePortalReturnTo(value) {
			t.Fatalf("unsafe return path %q accepted", value)
		}
	}
}

func TestOIDCLoginStateShape(t *testing.T) {
	state := OIDCLoginState{
		NonceHash:    sha256.Sum256([]byte("nonce")),
		PKCEVerifier: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_",
		ReturnTo:     "/portal",
	}
	if len(state.NonceHash) != sha256.Size {
		t.Fatal("nonce digest has unexpected length")
	}
	if len(state.PKCEVerifier) < 43 || !safePortalReturnTo(state.ReturnTo) {
		t.Fatal("test login state is invalid")
	}
}

func TestInvitationExpiryBounds(t *testing.T) {
	policy := DefaultSessionPolicy()
	if policy.AbsoluteLifetime >= 7*24*time.Hour {
		t.Fatal("session policy unexpectedly overlaps maximum invitation lifetime")
	}
}
