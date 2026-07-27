package portal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	Enabled             bool
	IssuerURL           string
	ClientID            string
	ClientSecret        string
	RedirectURL         string
	RequiredACR         string
	StepUpACR           string
	AllowInsecureIssuer bool
	DiscoveryTimeout    time.Duration
}

func LoadOIDCConfigFromEnv() OIDCConfig {
	return OIDCConfig{
		Enabled:      envBool("CLUB_PORTAL_OIDC_ENABLED"),
		IssuerURL:    strings.TrimSpace(os.Getenv("CLUB_PORTAL_OIDC_ISSUER")),
		ClientID:     strings.TrimSpace(os.Getenv("CLUB_PORTAL_OIDC_CLIENT_ID")),
		ClientSecret: os.Getenv("CLUB_PORTAL_OIDC_CLIENT_SECRET"),
		RedirectURL:  strings.TrimSpace(os.Getenv("CLUB_PORTAL_OIDC_REDIRECT_URL")),
		RequiredACR:  strings.TrimSpace(os.Getenv("CLUB_PORTAL_OIDC_REQUIRED_ACR")),
		StepUpACR:    strings.TrimSpace(os.Getenv("CLUB_PORTAL_OIDC_STEP_UP_ACR")),
		AllowInsecureIssuer: envBool("CLUB_PORTAL_OIDC_ALLOW_INSECURE") &&
			!strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production"),
		DiscoveryTimeout: 10 * time.Second,
	}
}

func EnabledFromEnv() bool {
	return envBool("CLUB_PORTAL_ENABLED")
}

func (config OIDCConfig) Validate() error {
	if !config.Enabled {
		return nil
	}
	if config.IssuerURL == "" || config.ClientID == "" || config.RedirectURL == "" {
		return fmt.Errorf("portal OIDC issuer, client ID and redirect URL are required")
	}
	issuer, err := url.Parse(config.IssuerURL)
	if err != nil || issuer.Host == "" || issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("portal OIDC issuer URL is invalid")
	}
	redirect, err := url.Parse(config.RedirectURL)
	if err != nil || redirect.Host == "" || redirect.RawQuery != "" || redirect.Fragment != "" {
		return fmt.Errorf("portal OIDC redirect URL is invalid")
	}
	if !config.AllowInsecureIssuer && (issuer.Scheme != "https" || redirect.Scheme != "https") {
		return fmt.Errorf("portal OIDC issuer and redirect URLs must use HTTPS")
	}
	if config.AllowInsecureIssuer && issuer.Scheme != "https" && issuer.Scheme != "http" {
		return fmt.Errorf("portal OIDC issuer URL scheme is invalid")
	}
	if config.AllowInsecureIssuer && redirect.Scheme != "https" && redirect.Scheme != "http" {
		return fmt.Errorf("portal OIDC redirect URL scheme is invalid")
	}
	if redirect.Path != "/portal/auth/callback" {
		return fmt.Errorf("portal OIDC redirect path must be /portal/auth/callback")
	}
	if config.DiscoveryTimeout <= 0 || config.DiscoveryTimeout > 30*time.Second {
		return fmt.Errorf("portal OIDC discovery timeout must be between 1ns and 30s")
	}
	if config.StepUpACR != "" && config.RequiredACR != "" &&
		config.StepUpACR == config.RequiredACR {
		// This is allowed and means every successful login is also a step-up.
		return nil
	}
	return nil
}

type OIDCClient struct {
	store      *Store
	config     OIDCConfig
	httpClient *http.Client

	mu       sync.Mutex
	provider *oidc.Provider
	oauth2   *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func NewOIDCClient(store *Store, config OIDCConfig) (*OIDCClient, error) {
	if store == nil {
		return nil, fmt.Errorf("OIDC client requires a portal store")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &OIDCClient{
		store:  store,
		config: config,
		httpClient: &http.Client{
			Timeout: config.DiscoveryTimeout,
		},
	}, nil
}

func (client *OIDCClient) Enabled() bool {
	return client != nil && client.config.Enabled
}

// CheckProvider performs read-only OIDC discovery and verifier construction.
// It is used by the staging preflight without creating a login state or
// exposing provider configuration.
func (client *OIDCClient) CheckProvider(ctx context.Context) error {
	if client == nil || !client.Enabled() {
		return fmt.Errorf("portal OIDC is disabled")
	}
	if _, _, err := client.ensureProvider(ctx); err != nil {
		return fmt.Errorf("portal OIDC discovery failed: %w", err)
	}
	return nil
}

func (client *OIDCClient) ensureProvider(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	if !client.Enabled() {
		return nil, nil, fmt.Errorf("portal OIDC is disabled")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.provider != nil && client.oauth2 != nil && client.verifier != nil {
		return client.oauth2, client.verifier, nil
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, client.config.DiscoveryTimeout)
	defer cancel()
	discoveryCtx = oidc.ClientContext(discoveryCtx, client.httpClient)
	if client.config.AllowInsecureIssuer {
		discoveryCtx = oidc.InsecureIssuerURLContext(discoveryCtx, client.config.IssuerURL)
	}
	provider, err := oidc.NewProvider(discoveryCtx, client.config.IssuerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("discover portal identity provider: %w", err)
	}
	oauthConfig := &oauth2.Config{
		ClientID:     client.config.ClientID,
		ClientSecret: client.config.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  client.config.RedirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: client.config.ClientID})
	client.provider = provider
	client.oauth2 = oauthConfig
	client.verifier = verifier
	return oauthConfig, verifier, nil
}

type OIDCBeginResult struct {
	AuthorizationURL string
	ExpiresAt        time.Time
}

func (client *OIDCClient) BeginLogin(
	ctx context.Context,
	returnTo string,
	invitationToken string,
) (OIDCBeginResult, error) {
	return client.beginLogin(ctx, returnTo, invitationToken, false, uuid.Nil)
}

func (client *OIDCClient) BeginStepUp(
	ctx context.Context,
	returnTo string,
	principal Principal,
) (OIDCBeginResult, error) {
	if strings.TrimSpace(client.config.StepUpACR) == "" {
		return OIDCBeginResult{}, fmt.Errorf("portal step-up ACR is not configured")
	}
	if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil {
		return OIDCBeginResult{}, ErrUnauthenticated
	}
	return client.beginLogin(ctx, returnTo, "", true, principal.UserID)
}

func (client *OIDCClient) beginLogin(
	ctx context.Context,
	returnTo string,
	invitationToken string,
	stepUpRequested bool,
	expectedUserID uuid.UUID,
) (OIDCBeginResult, error) {
	oauthConfig, _, err := client.ensureProvider(ctx)
	if err != nil {
		return OIDCBeginResult{}, err
	}
	if returnTo == "" {
		returnTo = "/portal"
	}
	if !safePortalReturnTo(returnTo) {
		return OIDCBeginResult{}, fmt.Errorf("unsafe portal return path")
	}

	rawState, stateHash, err := NewOpaqueToken()
	if err != nil {
		return OIDCBeginResult{}, err
	}
	rawNonce, nonceHash, err := NewOpaqueToken()
	if err != nil {
		return OIDCBeginResult{}, err
	}
	pkceVerifier, _, err := NewOpaqueToken()
	if err != nil {
		return OIDCBeginResult{}, err
	}
	var invitationHash []byte
	if strings.TrimSpace(invitationToken) != "" {
		digest, err := HashOpaqueToken(invitationToken)
		if err != nil {
			return OIDCBeginResult{}, ErrUnauthenticated
		}
		invitationHash = append(invitationHash, digest[:]...)
	}
	expiresAt := client.store.now().Add(10 * time.Minute)
	if err := client.store.SaveOIDCLoginState(
		ctx,
		stateHash,
		nonceHash,
		pkceVerifier,
		returnTo,
		invitationHash,
		stepUpRequested,
		expectedUserID,
		expiresAt,
	); err != nil {
		return OIDCBeginResult{}, err
	}

	options := []oauth2.AuthCodeOption{
		oidc.Nonce(rawNonce),
		oauth2.S256ChallengeOption(pkceVerifier),
	}
	if stepUpRequested {
		options = append(
			options,
			oauth2.SetAuthURLParam("acr_values", client.config.StepUpACR),
			oauth2.SetAuthURLParam("prompt", "login"),
			oauth2.SetAuthURLParam("max_age", "0"),
		)
	} else if client.config.RequiredACR != "" {
		options = append(options, oauth2.SetAuthURLParam("acr_values", client.config.RequiredACR))
	}
	return OIDCBeginResult{
		AuthorizationURL: oauthConfig.AuthCodeURL(rawState, options...),
		ExpiresAt:        expiresAt,
	}, nil
}

type OIDCCompleteResult struct {
	Principal       Principal
	RawSessionToken string
	ReturnTo        string
}

type oidcIdentityClaims struct {
	Nonce             string   `json:"nonce"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	EmailVerified     bool     `json:"email_verified"`
	ACR               string   `json:"acr"`
	AMR               []string `json:"amr"`
}

func (client *OIDCClient) CompleteLogin(
	ctx context.Context,
	rawState string,
	code string,
	details ClientDetails,
) (OIDCCompleteResult, error) {
	if strings.TrimSpace(code) == "" {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}
	loginState, err := client.store.ConsumeOIDCLoginState(ctx, rawState)
	if err != nil {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}
	oauthConfig, verifier, err := client.ensureProvider(ctx)
	if err != nil {
		return OIDCCompleteResult{}, err
	}

	exchangeCtx := oidc.ClientContext(ctx, client.httpClient)
	token, err := oauthConfig.Exchange(
		exchangeCtx,
		code,
		oauth2.VerifierOption(loginState.PKCEVerifier),
	)
	if err != nil {
		return OIDCCompleteResult{}, fmt.Errorf("exchange portal authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}
	idToken, err := verifier.Verify(exchangeCtx, rawIDToken)
	if err != nil {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}
	var claims oidcIdentityClaims
	if err := idToken.Claims(&claims); err != nil {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}
	if !verifyNonce(claims.Nonce, loginState.NonceHash) {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}
	if client.config.RequiredACR != "" && claims.ACR != client.config.RequiredACR &&
		claims.ACR != client.config.StepUpACR {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}
	if loginState.StepUpRequested && claims.ACR != client.config.StepUpACR {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}

	displayName := strings.TrimSpace(claims.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(claims.PreferredUsername)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(claims.Email)
	}
	identity := IdentityClaims{
		Issuer:        idToken.Issuer,
		Subject:       idToken.Subject,
		DisplayName:   displayName,
		Email:         strings.TrimSpace(claims.Email),
		EmailVerified: claims.EmailVerified,
	}
	if err := identity.Validate(); err != nil {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}

	var resolvedUserID uuid.UUID
	if len(loginState.InvitationTokenHash) == sha256.Size {
		resolvedUserID, err = client.store.RedeemInvitation(
			ctx,
			loginState.InvitationTokenHash,
			identity,
			rawState,
		)
	} else {
		resolvedUserID, err = client.store.ResolveIdentity(ctx, identity)
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) ||
			errors.Is(err, ErrUnauthenticated) {
			return OIDCCompleteResult{}, ErrUnauthenticated
		}
		return OIDCCompleteResult{}, err
	}
	if loginState.ExpectedUserID != uuid.Nil && resolvedUserID != loginState.ExpectedUserID {
		return OIDCCompleteResult{}, ErrUnauthenticated
	}

	stepUp := client.config.StepUpACR != "" && claims.ACR == client.config.StepUpACR
	principal, rawSessionToken, err := client.store.CreateSession(
		ctx,
		resolvedUserID,
		details,
		stepUp,
	)
	if err != nil {
		return OIDCCompleteResult{}, err
	}
	return OIDCCompleteResult{
		Principal:       principal,
		RawSessionToken: rawSessionToken,
		ReturnTo:        loginState.ReturnTo,
	}, nil
}

func verifyNonce(nonce string, expected [sha256.Size]byte) bool {
	if nonce == "" {
		return false
	}
	actual := sha256.Sum256([]byte(nonce))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

func pkceS256Challenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
