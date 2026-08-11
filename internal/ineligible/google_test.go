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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoogleSheetsSourceUsesServiceAccountAndReadonlyValuesAPI(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal RSA key: %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	var tokenCalls atomic.Int32
	var valuesCalls atomic.Int32

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
				t.Errorf("token content type: %q", got)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
				return
			}
			if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				t.Errorf("grant_type: %q", got)
			}
			assertion := r.Form.Get("assertion")
			parts := strings.Split(assertion, ".")
			if len(parts) != 3 {
				t.Errorf("JWT segments: got %d", len(parts))
				return
			}
			claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Errorf("decode JWT claims: %v", err)
				return
			}
			var claims map[string]any
			if err := json.Unmarshal(claimsRaw, &claims); err != nil {
				t.Errorf("decode claims JSON: %v", err)
				return
			}
			if claims["scope"] != googleReadonlyScopes {
				t.Errorf("scope: %v", claims["scope"])
			}
			if claims["iss"] != "sync@example.test" || claims["aud"] != server.URL+"/token" {
				t.Errorf("unexpected JWT claims: %#v", claims)
			}
			signature, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				t.Errorf("decode JWT signature: %v", err)
				return
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
				t.Errorf("verify JWT: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"private-sheet-token","token_type":"Bearer","expires_in":3600}`))
		default:
			valuesCalls.Add(1)
			if r.URL.Path != "/v4/spreadsheets/sheet-id/values/'Form responses 1'!A:N" {
				t.Errorf("values path: %q", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer private-sheet-token" {
				t.Errorf("authorization: %q", got)
			}
			if got := r.URL.Query().Get("majorDimension"); got != "ROWS" {
				t.Errorf("majorDimension: %q", got)
			}
			if got := r.URL.Query().Get("valueRenderOption"); got != "FORMATTED_VALUE" {
				t.Errorf("valueRenderOption: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"range":          "'Form responses 1'!A1:N2",
				"majorDimension": "ROWS",
				"values":         []any{DefaultGoogleFormSchema().Headers, validSourceRow()},
			})
		}
	}))
	defer server.Close()

	credentials, err := json.Marshal(map[string]string{
		"client_email": "sync@example.test",
		"private_key":  privatePEM,
		"token_uri":    server.URL + "/token",
	})
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	cfg := testConfig()
	cfg.ServiceAccountJSON = string(credentials)
	cfg.SheetsBaseURL = server.URL
	source, err := NewGoogleSheetsSource(cfg, server.Client())
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		data, err := source.Fetch(context.Background())
		if err != nil {
			t.Fatalf("fetch %d: %v", attempt+1, err)
		}
		if len(data.Values) != 2 {
			t.Fatalf("fetch %d row count: %d", attempt+1, len(data.Values))
		}
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls: got %d, want 1", got)
	}
	if got := valuesCalls.Load(); got != 2 {
		t.Fatalf("values calls: got %d, want 2", got)
	}
}

func TestConfigFromEnvUsesReviewedFourteenColumnSchema(t *testing.T) {
	t.Setenv("INELIGIBLE_IMPORT_ENABLED", "true")
	t.Setenv("INELIGIBLE_GOOGLE_SPREADSHEET_ID", "sheet-id")
	t.Setenv("INELIGIBLE_GOOGLE_SHEET_GID", "123")
	t.Setenv("INELIGIBLE_GOOGLE_SHEET_RANGE", "")
	t.Setenv("INELIGIBLE_GOOGLE_SCHEMA_JSON", "")
	t.Setenv("GOOGLE_SERVICE_ACCOUNT_JSON", `{}`)
	t.Setenv("GOOGLE_SERVICE_ACCOUNT_FILE", "")
	t.Setenv("INELIGIBLE_GOOGLE_HTTP_TIMEOUT_SEC", "")
	t.Setenv("INELIGIBLE_UPLOAD_DIR", "")
	t.Setenv("INELIGIBLE_UPLOAD_MAX_FILES", "")
	t.Setenv("INELIGIBLE_UPLOAD_MAX_FILE_BYTES", "")
	t.Setenv("INELIGIBLE_UPLOAD_MAX_TOTAL_BYTES", "")
	t.Setenv("INELIGIBLE_UPLOAD_ALLOWED_CONTENT_TYPES", "")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if len(cfg.Schema.Headers) != 14 {
		t.Fatalf("header count: %d", len(cfg.Schema.Headers))
	}
	if got := cfg.Schema.Headers[8]; got != "Offending Club's Name" {
		t.Fatalf("header 9: %q", got)
	}
	if got := cfg.Schema.Columns[ColumnFixtureDate]; got != 10 {
		t.Fatalf("fixture date index: %d", got)
	}
	if got := cfg.Schema.Columns[ColumnFileUpload]; got != 12 {
		t.Fatalf("file upload index: %d", got)
	}
	if cfg.SheetRange != "'Form responses 1'!A:N" {
		t.Fatalf("range: %q", cfg.SheetRange)
	}
	if cfg.UploadDir != defaultUploadDir || cfg.UploadMaxFiles != 10 || cfg.UploadMaxFileBytes != 10<<20 || cfg.UploadMaxTotalBytes != 25<<20 {
		t.Fatalf("upload defaults: dir=%q files=%d file_bytes=%d total_bytes=%d", cfg.UploadDir, cfg.UploadMaxFiles, cfg.UploadMaxFileBytes, cfg.UploadMaxTotalBytes)
	}
	if !contentTypeAllowed(cfg.UploadContentTypes, "video/mp4") {
		t.Fatal("default upload content types do not permit MP4 evidence")
	}
}

func TestConfigFromEnvRejectsSchemaOverrideWithThirteenHeaders(t *testing.T) {
	t.Setenv("INELIGIBLE_IMPORT_ENABLED", "true")
	t.Setenv("INELIGIBLE_GOOGLE_SPREADSHEET_ID", "sheet-id")
	t.Setenv("INELIGIBLE_GOOGLE_SHEET_GID", "123")
	t.Setenv("GOOGLE_SERVICE_ACCOUNT_JSON", `{}`)
	badSchema := DefaultGoogleFormSchema()
	badSchema.Headers = badSchema.Headers[:13]
	raw, err := json.Marshal(badSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("INELIGIBLE_GOOGLE_SCHEMA_JSON", string(raw))

	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "exactly 14") {
		t.Fatalf("expected exact-header error, got %v", err)
	}
}

func testConfig() Config {
	return Config{
		Enabled: true, SpreadsheetID: "sheet-id", SheetGID: "123",
		SheetRange: "'Form responses 1'!A:N", Schema: DefaultGoogleFormSchema(),
		ServiceAccountJSON: `{}`, SheetsBaseURL: defaultSheetsBaseURL, DriveBaseURL: defaultDriveBaseURL,
		HTTPTimeout: 5 * time.Second, UploadDir: filepath.Join(os.TempDir(), "gmcl-ineligible-test-uploads"),
		UploadMaxFiles: defaultUploadMaxFiles, UploadMaxFileBytes: defaultUploadMaxBytes,
		UploadMaxTotalBytes: defaultUploadTotalBytes,
		UploadContentTypes:  append([]string(nil), defaultUploadContentTypes...),
	}
}

func validSourceRow() []any {
	return []any{
		"04/08/2026 09:30:00",
		"reporter@example.test",
		"Defaulting Player",
		"Registration rule",
		"Further context",
		"Reporting CC",
		"Jane Smith, Secretary",
		"07123 456789",
		"Offending CC",
		"1st XI",
		"01/08/2026",
		"Scorecard evidence",
		"",
		"10",
	}
}
