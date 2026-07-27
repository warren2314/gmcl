package portal

import (
	"crypto/sha256"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
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

func TestCognitoOIDCConfigValidation(t *testing.T) {
	valid := OIDCConfig{
		Enabled:               true,
		ProviderProfile:       OIDCProviderCognito,
		IssuerURL:             "https://cognito-idp.eu-west-2.amazonaws.com/eu-west-2_GMCL123",
		ClientID:              "gmcl-cognito-client",
		ClientSecret:          "client-secret",
		RedirectURL:           "https://test.gmcl.example/portal/auth/callback",
		CognitoPolicyVerified: true,
		DiscoveryTimeout:      10 * time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Cognito config rejected: %v", err)
	}

	updatedIssuer := valid
	updatedIssuer.IssuerURL =
		"https://issuer-cognito-idp.eu-west-2.amazonaws.com/eu-west-2_GMCL123"
	if err := updatedIssuer.Validate(); err != nil {
		t.Fatalf("valid updated Cognito issuer rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*OIDCConfig)
	}{
		{"unknown profile", func(c *OIDCConfig) { c.ProviderProfile = "unknown" }},
		{"unverified policy", func(c *OIDCConfig) { c.CognitoPolicyVerified = false }},
		{"missing client secret", func(c *OIDCConfig) { c.ClientSecret = "" }},
		{"generic issuer", func(c *OIDCConfig) { c.IssuerURL = "https://identity.example/tenant" }},
		{"region mismatch", func(c *OIDCConfig) {
			c.IssuerURL = "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-2_GMCL123"
		}},
		{"issuer port", func(c *OIDCConfig) {
			c.IssuerURL = "https://cognito-idp.eu-west-2.amazonaws.com:443/eu-west-2_GMCL123"
		}},
		{"malformed pool", func(c *OIDCConfig) {
			c.IssuerURL = "https://cognito-idp.eu-west-2.amazonaws.com/eu-west-2_GMCL-123"
		}},
		{"baseline ACR", func(c *OIDCConfig) { c.RequiredACR = "not-supported" }},
		{"step-up ACR", func(c *OIDCConfig) { c.StepUpACR = "not-supported" }},
		{"insecure override", func(c *OIDCConfig) { c.AllowInsecureIssuer = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			tt.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid Cognito config unexpectedly passed validation")
			}
		})
	}
}

func TestCognitoAuthorizationParameters(t *testing.T) {
	config := OIDCConfig{ProviderProfile: OIDCProviderCognito}
	oauthConfig := oauth2.Config{
		ClientID:    "client",
		RedirectURL: "https://portal.example/portal/auth/callback",
		Endpoint: oauth2.Endpoint{
			AuthURL: "https://login.example/oauth2/authorize",
		},
	}

	baselineURL, err := url.Parse(oauthConfig.AuthCodeURL(
		"state",
		config.authorizationOptions("nonce", strings.Repeat("v", 43), false)...,
	))
	if err != nil {
		t.Fatal(err)
	}
	if baselineURL.Query().Has("acr_values") ||
		baselineURL.Query().Has("prompt") ||
		baselineURL.Query().Has("max_age") {
		t.Fatalf("unexpected Cognito baseline assurance parameters: %s", baselineURL.RawQuery)
	}

	stepUpURL, err := url.Parse(oauthConfig.AuthCodeURL(
		"state",
		config.authorizationOptions("nonce", strings.Repeat("v", 43), true)...,
	))
	if err != nil {
		t.Fatal(err)
	}
	if stepUpURL.Query().Has("acr_values") ||
		stepUpURL.Query().Get("prompt") != "login" ||
		stepUpURL.Query().Get("max_age") != "0" {
		t.Fatalf("unsafe Cognito step-up parameters: %s", stepUpURL.RawQuery)
	}
}

func TestCognitoIssuerShape(t *testing.T) {
	for _, raw := range []string{
		"https://cognito-idp.eu-west-2.amazonaws.com/eu-west-2_Example123",
		"https://issuer-cognito-idp.eu-west-2.amazonaws.com/eu-west-2_Example123",
	} {
		issuer, err := url.Parse(raw)
		if err != nil || !validCognitoIssuer(issuer) {
			t.Fatalf("valid issuer %q rejected", raw)
		}
	}
}

func TestCognitoAuthenticationAssurance(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	client := &OIDCClient{
		store: &Store{now: func() time.Time { return now }},
		config: OIDCConfig{
			ProviderProfile:       OIDCProviderCognito,
			CognitoPolicyVerified: true,
		},
	}
	requestedAt := now.Add(-30 * time.Second)
	validClaims := oidcIdentityClaims{
		TokenUse: "id",
		AuthTime: now.Add(-20 * time.Second).Unix(),
	}

	if stepUp, err := client.assessAuthentication(
		OIDCLoginState{CreatedAt: requestedAt},
		validClaims,
	); err != nil || stepUp {
		t.Fatalf("baseline Cognito assurance = (%v, %v), want (false, nil)", stepUp, err)
	}
	if stepUp, err := client.assessAuthentication(
		OIDCLoginState{StepUpRequested: true, CreatedAt: requestedAt},
		validClaims,
	); err != nil || !stepUp {
		t.Fatalf("fresh Cognito step-up = (%v, %v), want (true, nil)", stepUp, err)
	}

	tests := []struct {
		name   string
		state  OIDCLoginState
		claims oidcIdentityClaims
	}{
		{
			"missing token use",
			OIDCLoginState{},
			oidcIdentityClaims{AuthTime: now.Unix()},
		},
		{
			"missing auth time",
			OIDCLoginState{},
			oidcIdentityClaims{TokenUse: "id"},
		},
		{
			"future auth time",
			OIDCLoginState{},
			oidcIdentityClaims{TokenUse: "id", AuthTime: now.Add(3 * time.Minute).Unix()},
		},
		{
			"missing step-up request time",
			OIDCLoginState{StepUpRequested: true},
			validClaims,
		},
		{
			"stale step-up authentication",
			OIDCLoginState{StepUpRequested: true, CreatedAt: now},
			oidcIdentityClaims{
				TokenUse: "id",
				AuthTime: now.Add(-3 * time.Minute).Unix(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := client.assessAuthentication(tt.state, tt.claims); err == nil {
				t.Fatal("unsafe Cognito assurance unexpectedly passed")
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
