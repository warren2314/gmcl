package portal

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestOIDCInvitationSessionAndContextLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" || os.Getenv("TEST_DB_DISPOSABLE") != "1" {
		t.Skip("disposable TEST_DB_DSN not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := NewStore(pool, DefaultSessionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeSecurity(ctx); err != nil {
		t.Fatal(err)
	}

	clubID, teamID, adminID := seedOIDCIntegrationApprovals(t, ctx, pool)
	if err := store.SetClubFeature(
		ctx,
		clubID,
		FeaturePortalAccess,
		true,
		adminID,
		"disposable OIDC integration test",
		uuid.NewString(),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.SetClubFeature(
		ctx,
		clubID,
		FeatureReadOnlyDashboard,
		true,
		adminID,
		"disposable OIDC integration test",
		uuid.NewString(),
	); err != nil {
		t.Fatal(err)
	}
	invitation, err := store.CreateInvitation(ctx, InvitationRequest{
		ClubID:                    clubID,
		Email:                     "portal.oidc.test@example.org",
		Role:                      RoleClubPrimaryAdmin,
		OfficialEvidenceReference: "synthetic integration fixture",
		ApprovedByAdminID:         adminID,
		ExpiresAt:                 time.Now().UTC().Add(time.Hour),
	}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	provider := newOIDCTestProvider(t)
	defer provider.Close()
	client, err := NewOIDCClient(store, OIDCConfig{
		Enabled:             true,
		IssuerURL:           provider.URL,
		ClientID:            "gmcl-portal-test",
		ClientSecret:        "test-client-secret",
		RedirectURL:         provider.URL + "/portal/auth/callback",
		RequiredACR:         "urn:gmcl:test:baseline",
		StepUpACR:           "urn:gmcl:test:strong",
		AllowInsecureIssuer: true,
		DiscoveryTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	begin, err := client.BeginLogin(ctx, "/portal", invitation.RawToken)
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	rawState := authorizationURL.Query().Get("state")
	provider.SetExpectedAuthorization(
		authorizationURL.Query().Get("nonce"),
		authorizationURL.Query().Get("code_challenge"),
	)
	if authorizationURL.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE method = %q", authorizationURL.Query().Get("code_challenge_method"))
	}
	provider.SetACR("urn:gmcl:test:baseline")

	completed, err := client.CompleteLogin(ctx, rawState, "valid-code", ClientDetails{
		IPAddress: "192.0.2.10",
		UserAgent: "GMCL OIDC integration test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.ReturnTo != "/portal" || completed.Principal.Assignment != nil {
		t.Fatalf("unexpected initial portal result: %#v", completed)
	}
	if !completed.Principal.RequiresStepUp(time.Now().UTC(), DefaultSessionPolicy()) {
		t.Fatal("baseline authentication was incorrectly recorded as a step-up")
	}
	if _, err := client.CompleteLogin(ctx, rawState, "valid-code", ClientDetails{}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("OIDC state replay error = %v", err)
	}

	authenticated, err := store.Authenticate(ctx, completed.RawSessionToken)
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := store.ListActingContexts(ctx, authenticated)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 || contexts[0].Assignment.Scope.ClubID != clubID ||
		contexts[0].Assignment.Role != RoleClubPrimaryAdmin {
		t.Fatalf("acting contexts = %#v", contexts)
	}

	selected, rotatedToken, err := store.SelectActingContext(
		ctx,
		authenticated,
		contexts[0].Assignment.ID,
		ClientDetails{IPAddress: "192.0.2.10", UserAgent: "GMCL OIDC integration test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Assignment == nil || selected.Assignment.Scope.ClubID != clubID {
		t.Fatalf("selected principal = %#v", selected)
	}
	if _, err := store.Authenticate(ctx, completed.RawSessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("pre-rotation token remained valid: %v", err)
	}
	rotatedPrincipal, err := store.Authenticate(ctx, rotatedToken)
	if err != nil {
		t.Fatal(err)
	}
	readScope, err := store.ResolveReadScope(ctx, rotatedPrincipal, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if readScope.SelectedSeasonID <= 0 || readScope.SelectedTeamID != nil {
		t.Fatalf("unexpected default read scope: %#v", readScope)
	}
	rotatedPrincipal = readScope.Principal
	foreignTeamID := int32(2147483000)
	if _, err := store.ResolveReadScope(
		ctx,
		rotatedPrincipal,
		&readScope.SelectedSeasonID,
		&foreignTeamID,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign team scope error = %v", err)
	}
	var deniedScopeEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM portal_audit_events
		WHERE actor_user_id = $1
		  AND action = 'portal.scope.denied'
		  AND outcome = 'denied'
	`, rotatedPrincipal.UserID).Scan(&deniedScopeEvents); err != nil {
		t.Fatal(err)
	}
	if deniedScopeEvents != 1 {
		t.Fatalf("scope denial audit events = %d, want 1", deniedScopeEvents)
	}
	teamReadScope, err := store.ResolveReadScope(
		ctx,
		rotatedPrincipal,
		&readScope.SelectedSeasonID,
		&teamID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if teamReadScope.SelectedTeamID == nil || *teamReadScope.SelectedTeamID != teamID {
		t.Fatalf("team read scope = %#v", teamReadScope)
	}
	rotatedPrincipal = teamReadScope.Principal
	enabled, err := store.FeatureEnabled(ctx, rotatedPrincipal, FeatureReadOnlyDashboard)
	if err != nil || !enabled {
		t.Fatalf("dashboard feature = %v, %v", enabled, err)
	}
	dashboard, err := store.LoadClubDashboard(ctx, rotatedPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.ClubID != clubID {
		t.Fatalf("dashboard club = %d, want %d", dashboard.ClubID, clubID)
	}
	if dashboard.LastFixtureSyncAt != nil {
		t.Fatalf("dashboard reported fixture freshness without a source sync: %v", dashboard.LastFixtureSyncAt)
	}
	obligations, err := store.LoadReportObligations(ctx, rotatedPrincipal, dashboard.SeasonID)
	if err != nil {
		t.Fatal(err)
	}
	if len(obligations) != 0 {
		t.Fatalf("unexpected synthetic report obligations: %#v", obligations)
	}
	ledger, err := store.LoadSanctionLedger(ctx, rotatedPrincipal, dashboard.SeasonID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 0 {
		t.Fatalf("unexpected synthetic sanction ledger: %#v", ledger)
	}
	if err := store.RevokeAllUserSessions(
		ctx,
		rotatedPrincipal,
		"must require step-up",
	); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("all-session revocation without step-up = %v", err)
	}

	stepUpBegin, err := client.BeginStepUp(ctx, "/portal/sessions", rotatedPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	stepUpURL, err := url.Parse(stepUpBegin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if stepUpURL.Query().Get("prompt") != "login" ||
		stepUpURL.Query().Get("max_age") != "0" ||
		stepUpURL.Query().Get("acr_values") != "urn:gmcl:test:strong" {
		t.Fatalf("step-up authorization parameters = %q", stepUpURL.RawQuery)
	}
	provider.SetExpectedAuthorization(
		stepUpURL.Query().Get("nonce"),
		stepUpURL.Query().Get("code_challenge"),
	)
	provider.SetACR("urn:gmcl:test:strong")
	stepUp, err := client.CompleteLogin(
		ctx,
		stepUpURL.Query().Get("state"),
		"valid-code",
		ClientDetails{IPAddress: "192.0.2.11", UserAgent: "GMCL step-up integration test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stepUp.ReturnTo != "/portal/sessions" ||
		stepUp.Principal.RequiresStepUp(time.Now().UTC(), DefaultSessionPolicy()) {
		t.Fatalf("unexpected step-up result: %#v", stepUp)
	}
	sessions, err := store.ListUserSessions(ctx, stepUp.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("active sessions = %d, want 2", len(sessions))
	}
	if err := store.RevokeUserSession(
		ctx,
		stepUp.Principal,
		rotatedPrincipal.SessionID,
		"OIDC integration test",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, rotatedToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked token remained valid: %v", err)
	}
	if err := store.RevokeAllUserSessions(
		ctx,
		stepUp.Principal,
		"OIDC integration test all-device revocation",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, stepUp.RawSessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("all-device revocation left the step-up token valid: %v", err)
	}

	replacement, replacementToken, err := store.CreateSession(
		ctx,
		stepUp.Principal.UserID,
		ClientDetails{IPAddress: "192.0.2.12", UserAgent: "GMCL role revocation test"},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacementContexts, err := store.ListActingContexts(ctx, replacement)
	if err != nil || len(replacementContexts) != 1 {
		t.Fatalf("replacement acting contexts = %#v, %v", replacementContexts, err)
	}
	replacementSelected, replacementSelectedToken, err := store.SelectActingContext(
		ctx,
		replacement,
		replacementContexts[0].Assignment.ID,
		ClientDetails{IPAddress: "192.0.2.12", UserAgent: "GMCL role revocation test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, replacementToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("replacement pre-rotation token remained valid: %v", err)
	}
	if err := store.RevokeRoleAssignment(
		ctx,
		replacementContexts[0].Assignment.ID,
		adminID,
		"integration test appointment ended",
		uuid.NewString(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, replacementSelectedToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("role revocation left selected session valid: %v", err)
	}
	remainingContexts, err := store.ListActingContexts(ctx, replacementSelected)
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingContexts) != 0 {
		t.Fatalf("revoked role remained selectable: %#v", remainingContexts)
	}
}

func seedOIDCIntegrationApprovals(
	t *testing.T,
	ctx context.Context,
	pool *db.Pool,
) (int32, int32, int32) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1804289383, 9148)`); err != nil {
		t.Fatal(err)
	}
	var clubID int32
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(id), 0) + 2001 FROM clubs
	`).Scan(&clubID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO clubs (id, name, short_name)
		VALUES ($1, $2, 'OIDCT')
	`, clubID, "Portal OIDC integration "+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var teamID int32
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(id), 0) + 3001
		FROM teams
	`).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO teams (
			id, club_id, name, level, active, play_cricket_team_id
		)
		VALUES ($1, $2, 'Portal OIDC Test XI', 1, TRUE, $3)
	`, teamID, clubID, "OIDC-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var adminID int32
	err = tx.QueryRow(ctx, `
		SELECT id FROM admin_users ORDER BY id LIMIT 1
	`).Scan(&adminID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO admin_users (
				username, password_hash, email, is_active, role
			)
			VALUES ($1, $2, $3, TRUE, 'super_admin')
			RETURNING id
		`, "portal-oidc-test-"+uuid.NewString(), []byte("synthetic-not-a-login-hash"),
			"portal-oidc-admin@example.org").Scan(&adminID); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return clubID, teamID, adminID
}

type oidcTestProvider struct {
	*httptest.Server
	privateKey *rsa.PrivateKey

	mu                sync.Mutex
	expectedNonce     string
	expectedChallenge string
	acr               string
}

func newOIDCTestProvider(t *testing.T) *oidcTestProvider {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &oidcTestProvider{privateKey: privateKey}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.ServeHTTP(w, r)
	}))
	provider.Server = server
	return provider
}

func (provider *oidcTestProvider) SetExpectedAuthorization(nonce, challenge string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.expectedNonce = nonce
	provider.expectedChallenge = challenge
}

func (provider *oidcTestProvider) SetACR(acr string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.acr = acr
}

func (provider *oidcTestProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(w, map[string]any{
			"issuer":                                provider.URL,
			"authorization_endpoint":                provider.URL + "/authorize",
			"token_endpoint":                        provider.URL + "/token",
			"jwks_uri":                              provider.URL + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	case "/keys":
		writeJSON(w, map[string]any{
			"keys": []jose.JSONWebKey{{
				Key:       &provider.privateKey.PublicKey,
				KeyID:     "gmcl-oidc-test-key",
				Algorithm: string(jose.RS256),
				Use:       "sig",
			}},
		})
	case "/token":
		provider.handleToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (provider *oidcTestProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil ||
		r.FormValue("code") != "valid-code" ||
		r.FormValue("grant_type") != "authorization_code" {
		http.Error(w, "invalid grant", http.StatusBadRequest)
		return
	}
	provider.mu.Lock()
	nonce := provider.expectedNonce
	challenge := provider.expectedChallenge
	acr := provider.acr
	provider.mu.Unlock()
	if nonce == "" || challenge == "" ||
		pkceS256Challenge(r.FormValue("code_verifier")) != challenge {
		http.Error(w, "invalid PKCE", http.StatusBadRequest)
		return
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: provider.privateKey},
		(&jose.SignerOptions{}).
			WithType("JWT").
			WithHeader("kid", "gmcl-oidc-test-key"),
	)
	if err != nil {
		http.Error(w, "signing failed", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	rawIDToken, err := jwt.Signed(signer).Claims(map[string]any{
		"iss":            provider.URL,
		"sub":            "gmcl-portal-oidc-test-subject",
		"aud":            "gmcl-portal-test",
		"iat":            now.Unix(),
		"exp":            now.Add(5 * time.Minute).Unix(),
		"nonce":          nonce,
		"name":           "Synthetic Club Official",
		"email":          "portal.oidc.test@example.org",
		"email_verified": true,
		"acr":            acr,
		"amr":            []string{"webauthn"},
	}).Serialize()
	if err != nil {
		http.Error(w, "token failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "synthetic-access-token",
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     rawIDToken,
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestOIDCTestProviderNeverAcceptsPlainPKCE(t *testing.T) {
	if strings.EqualFold(pkceS256Challenge("verifier"), "verifier") {
		t.Fatal("PKCE challenge unexpectedly equals its verifier")
	}
}
