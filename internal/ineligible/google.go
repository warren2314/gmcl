package ineligible

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	sheetsReadonlyScope  = "https://www.googleapis.com/auth/spreadsheets.readonly"
	driveReadonlyScope   = "https://www.googleapis.com/auth/drive.readonly"
	googleReadonlyScopes = sheetsReadonlyScope + " " + driveReadonlyScope
)

type serviceAccountCredentials struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type oauthToken struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

// SheetData is the row-major values response returned by the protected sheet.
type SheetData struct {
	Range  string
	Values [][]any
}

// GoogleSheetsSource reads only the configured A:N values range and Drive IDs
// explicitly referenced there. Its read-only OAuth token is cached in memory.
type GoogleSheetsSource struct {
	cfg         Config
	httpClient  *http.Client
	credentials serviceAccountCredentials
	privateKey  *rsa.PrivateKey

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewGoogleSheetsSource builds a private Sheets client. A custom HTTP client is
// accepted for deterministic tests; nil uses the configured timeout.
func NewGoogleSheetsSource(cfg Config, httpClient *http.Client) (*GoogleSheetsSource, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, ErrImportDisabled
	}
	credentials, err := loadServiceAccountCredentials(cfg)
	if err != nil {
		return nil, err
	}
	privateKey, err := parseRSAPrivateKey(credentials.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parse Google service-account private key: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	return &GoogleSheetsSource{
		cfg:         cfg,
		httpClient:  httpClient,
		credentials: credentials,
		privateKey:  privateKey,
	}, nil
}

func loadServiceAccountCredentials(cfg Config) (serviceAccountCredentials, error) {
	var raw []byte
	var err error
	switch {
	case cfg.ServiceAccountJSON != "":
		raw = []byte(cfg.ServiceAccountJSON)
	case cfg.ServiceAccountFile != "":
		raw, err = os.ReadFile(cfg.ServiceAccountFile)
		if err != nil {
			return serviceAccountCredentials{}, fmt.Errorf("read Google service-account file: %w", err)
		}
	default:
		return serviceAccountCredentials{}, fmt.Errorf("Google service-account credentials are not configured")
	}
	if len(raw) > 1<<20 {
		return serviceAccountCredentials{}, fmt.Errorf("Google service-account credentials exceed 1 MiB")
	}
	var credentials serviceAccountCredentials
	if err := json.Unmarshal(raw, &credentials); err != nil {
		return serviceAccountCredentials{}, fmt.Errorf("decode Google service-account credentials: %w", err)
	}
	credentials.ClientEmail = strings.TrimSpace(credentials.ClientEmail)
	credentials.TokenURI = strings.TrimSpace(credentials.TokenURI)
	if credentials.ClientEmail == "" || strings.TrimSpace(credentials.PrivateKey) == "" {
		return serviceAccountCredentials{}, fmt.Errorf("Google service-account credentials require client_email and private_key")
	}
	if credentials.TokenURI == "" {
		credentials.TokenURI = "https://oauth2.googleapis.com/token"
	}
	u, err := url.Parse(credentials.TokenURI)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return serviceAccountCredentials{}, fmt.Errorf("Google service-account token_uri is invalid")
	}
	if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return serviceAccountCredentials{}, fmt.Errorf("Google service-account token_uri must use HTTPS")
	}
	return credentials, nil
}

func parseRSAPrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, fmt.Errorf("PEM block not found")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is not RSA")
		}
		return validateRSAPrivateKey(rsaKey)
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("unsupported RSA private-key encoding")
	}
	return validateRSAPrivateKey(key)
}

func validateRSAPrivateKey(key *rsa.PrivateKey) (*rsa.PrivateKey, error) {
	if key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("RSA key must be at least 2048 bits")
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("invalid RSA private key")
	}
	return key, nil
}

// Fetch retrieves the configured protected values range using a short-lived
// OAuth token minted from the service-account JSON.
func (s *GoogleSheetsSource) Fetch(ctx context.Context) (SheetData, error) {
	token, err := s.token(ctx)
	if err != nil {
		return SheetData{}, err
	}
	endpoint := strings.TrimRight(s.cfg.SheetsBaseURL, "/") +
		"/v4/spreadsheets/" + url.PathEscape(s.cfg.SpreadsheetID) +
		"/values/" + url.PathEscape(s.cfg.SheetRange)
	u, err := url.Parse(endpoint)
	if err != nil {
		return SheetData{}, fmt.Errorf("build Google Sheets values URL: %w", err)
	}
	query := u.Query()
	query.Set("majorDimension", "ROWS")
	query.Set("valueRenderOption", "FORMATTED_VALUE")
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SheetData{}, fmt.Errorf("create Google Sheets request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := doGoogleRequest(ctx, s.httpClient, req)
	if err != nil {
		return SheetData{}, fmt.Errorf("Google Sheets values request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return SheetData{}, fmt.Errorf("read Google Sheets values response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SheetData{}, fmt.Errorf("Google Sheets values HTTP %d: %s", resp.StatusCode, truncateForError(body, 500))
	}
	var payload struct {
		Range  string  `json:"range"`
		Values [][]any `json:"values"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return SheetData{}, fmt.Errorf("decode Google Sheets values response: %w", err)
	}
	return SheetData{Range: payload.Range, Values: payload.Values}, nil
}

func (s *GoogleSheetsSource) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accessToken != "" && time.Now().Before(s.tokenExpiry) {
		return s.accessToken, nil
	}
	now := time.Now().UTC()
	assertion, err := signedJWT(s.credentials, s.privateKey, now)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.credentials.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create Google OAuth token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doGoogleRequest(ctx, s.httpClient, req)
	if err != nil {
		return "", fmt.Errorf("Google OAuth token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read Google OAuth token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Google OAuth token HTTP %d: %s", resp.StatusCode, truncateForError(body, 500))
	}
	var token oauthToken
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("decode Google OAuth token response: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", fmt.Errorf("Google OAuth token response omitted access_token")
	}
	if token.ExpiresIn <= 0 {
		token.ExpiresIn = 3600
	}
	margin := 30 * time.Second
	lifetime := time.Duration(token.ExpiresIn) * time.Second
	if lifetime <= margin {
		margin = lifetime / 2
	}
	s.accessToken = token.AccessToken
	s.tokenExpiry = now.Add(lifetime - margin)
	return s.accessToken, nil
}

func signedJWT(credentials serviceAccountCredentials, privateKey *rsa.PrivateKey, now time.Time) (string, error) {
	headerJSON, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{Algorithm: "RS256", Type: "JWT"})
	if err != nil {
		return "", fmt.Errorf("encode Google JWT header: %w", err)
	}
	claimsJSON, err := json.Marshal(struct {
		Issuer   string `json:"iss"`
		Scope    string `json:"scope"`
		Audience string `json:"aud"`
		IssuedAt int64  `json:"iat"`
		Expires  int64  `json:"exp"`
	}{
		Issuer: credentials.ClientEmail, Scope: googleReadonlyScopes,
		Audience: credentials.TokenURI, IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("encode Google JWT claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign Google JWT: %w", err)
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func truncateForError(body []byte, max int) string {
	text := strings.TrimSpace(string(body))
	if len(text) > max {
		return text[:max] + "..."
	}
	return text
}
