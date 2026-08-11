package ineligible

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// GoogleFormsHeaderCount is the fixed width of the protected response sheet.
	GoogleFormsHeaderCount  = 14
	defaultSheetRange       = "'Form responses 1'!A:N"
	defaultSheetsBaseURL    = "https://sheets.googleapis.com"
	defaultDriveBaseURL     = "https://www.googleapis.com/drive/v3"
	defaultUploadDir        = "/app/data/ineligible-uploads"
	defaultUploadMaxFiles   = 10
	defaultUploadMaxBytes   = int64(10 << 20)
	defaultUploadTotalBytes = int64(25 << 20)
)

// Schema binds the exact source header row to the fields needed for stable
// identity and the intake queue projection. Column indexes are zero based.
// Keeping the mapping in the same value as the headers makes schema changes an
// explicit deployment decision rather than a best-effort header-name guess.
type Schema struct {
	Headers []string       `json:"headers"`
	Columns map[string]int `json:"columns"`
}

const (
	ColumnTimestamp     = "timestamp"
	ColumnReporterEmail = "reporter_email"
	ColumnReportingClub = "reporting_club"
	ColumnOffendingClub = "offending_club"
	ColumnTeam          = "team"
	ColumnPlayer        = "player"
	ColumnFixtureDate   = "fixture_date"
	ColumnFileUpload    = "file_upload"
)

var requiredColumns = []string{
	ColumnTimestamp,
	ColumnReporterEmail,
	ColumnReportingClub,
	ColumnOffendingClub,
	ColumnTeam,
	ColumnPlayer,
	ColumnFixtureDate,
	ColumnFileUpload,
}

var defaultUploadContentTypes = []string{
	"application/pdf",
	"application/msword",
	"application/vnd.ms-excel",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"image/gif",
	"image/heic",
	"image/jpeg",
	"image/png",
	"image/webp",
	"message/rfc822",
	"text/plain",
	"video/mp4",
}

// DefaultGoogleFormSchema is the reviewed A1:N1 contract from the 2026 form.
// It returns a new value on each call so callers cannot mutate process-wide
// configuration while constructing a test or administrative preview.
func DefaultGoogleFormSchema() Schema {
	return Schema{
		Headers: []string{
			"Timestamp",
			"Email address",
			"Name of defaulting player as shown on scorecard",
			"Reason you believe the player is ineligible",
			"Additional Info",
			"Your Club",
			"Your Name & Role at Club/League",
			"Your Preferred tel no",
			"Offending Club's Name",
			"Team in question",
			"Fixture Date",
			"Additional Evidence",
			"File Upload",
			"Score",
		},
		Columns: map[string]int{
			ColumnTimestamp:     0,
			ColumnReporterEmail: 1,
			ColumnPlayer:        2,
			ColumnReportingClub: 5,
			ColumnOffendingClub: 8,
			ColumnTeam:          9,
			ColumnFixtureDate:   10,
			ColumnFileUpload:    12,
		},
	}
}

// Config contains the deployment settings for the private Google Form source.
type Config struct {
	Enabled              bool
	BootstrapEnabled     bool
	PrivateGoogleFormURL string
	SpreadsheetID        string
	SheetGID             string
	SheetRange           string
	Schema               Schema
	ServiceAccountJSON   string
	ServiceAccountFile   string
	SheetsBaseURL        string
	DriveBaseURL         string
	HTTPTimeout          time.Duration
	UploadDir            string
	UploadMaxFiles       int
	UploadMaxFileBytes   int64
	UploadMaxTotalBytes  int64
	UploadContentTypes   []string
}

// ConfigFromEnv reads and validates the ineligible-player import settings.
// Deployments can override the reviewed schema atomically, but an override is
// never inferred from whatever header row happens to arrive from Google.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Enabled:              envBool("INELIGIBLE_IMPORT_ENABLED"),
		BootstrapEnabled:     envBool("INELIGIBLE_BOOTSTRAP_IMPORT_ENABLED"),
		PrivateGoogleFormURL: strings.TrimSpace(os.Getenv("INELIGIBLE_PRIVATE_GOOGLE_FORM_URL")),
		SpreadsheetID:        strings.TrimSpace(os.Getenv("INELIGIBLE_GOOGLE_SPREADSHEET_ID")),
		SheetGID:             strings.TrimSpace(os.Getenv("INELIGIBLE_GOOGLE_SHEET_GID")),
		SheetRange:           strings.TrimSpace(os.Getenv("INELIGIBLE_GOOGLE_SHEET_RANGE")),
		ServiceAccountJSON:   strings.TrimSpace(os.Getenv("GOOGLE_SERVICE_ACCOUNT_JSON")),
		ServiceAccountFile:   strings.TrimSpace(os.Getenv("GOOGLE_SERVICE_ACCOUNT_FILE")),
		SheetsBaseURL:        defaultSheetsBaseURL,
		DriveBaseURL:         defaultDriveBaseURL,
		HTTPTimeout:          45 * time.Second,
		Schema:               DefaultGoogleFormSchema(),
		UploadDir:            strings.TrimSpace(os.Getenv("INELIGIBLE_UPLOAD_DIR")),
		UploadMaxFiles:       defaultUploadMaxFiles,
		UploadMaxFileBytes:   defaultUploadMaxBytes,
		UploadMaxTotalBytes:  defaultUploadTotalBytes,
		UploadContentTypes:   append([]string(nil), defaultUploadContentTypes...),
	}
	if cfg.SheetRange == "" {
		cfg.SheetRange = defaultSheetRange
	}
	if cfg.UploadDir == "" {
		cfg.UploadDir = defaultUploadDir
	}
	if raw := strings.TrimSpace(os.Getenv("INELIGIBLE_GOOGLE_SCHEMA_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Schema); err != nil {
			return Config{}, fmt.Errorf("parse INELIGIBLE_GOOGLE_SCHEMA_JSON: %w", err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("INELIGIBLE_GOOGLE_HTTP_TIMEOUT_SEC")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 120 {
			return Config{}, fmt.Errorf("INELIGIBLE_GOOGLE_HTTP_TIMEOUT_SEC must be an integer from 1 to 120")
		}
		cfg.HTTPTimeout = time.Duration(seconds) * time.Second
	}
	if raw := strings.TrimSpace(os.Getenv("INELIGIBLE_UPLOAD_MAX_FILES")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 20 {
			return Config{}, fmt.Errorf("INELIGIBLE_UPLOAD_MAX_FILES must be an integer from 0 to 20")
		}
		cfg.UploadMaxFiles = value
	}
	if raw := strings.TrimSpace(os.Getenv("INELIGIBLE_UPLOAD_MAX_FILE_BYTES")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > 100<<20 {
			return Config{}, fmt.Errorf("INELIGIBLE_UPLOAD_MAX_FILE_BYTES must be from 1 to 104857600")
		}
		cfg.UploadMaxFileBytes = value
	}
	if raw := strings.TrimSpace(os.Getenv("INELIGIBLE_UPLOAD_MAX_TOTAL_BYTES")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > 250<<20 {
			return Config{}, fmt.Errorf("INELIGIBLE_UPLOAD_MAX_TOTAL_BYTES must be from 1 to 262144000")
		}
		cfg.UploadMaxTotalBytes = value
	}
	if raw := strings.TrimSpace(os.Getenv("INELIGIBLE_UPLOAD_ALLOWED_CONTENT_TYPES")); raw != "" {
		cfg.UploadContentTypes = splitCommaList(raw)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects incomplete or ambiguous source configuration. Disabled
// imports remain valid so deployments can retain credentials while using the
// independent intake kill switch.
func (c Config) Validate() error {
	if c.PrivateGoogleFormURL != "" {
		parsed, err := url.Parse(c.PrivateGoogleFormURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("INELIGIBLE_PRIVATE_GOOGLE_FORM_URL must be an absolute HTTPS URL")
		}
	}
	if !c.Enabled {
		return nil
	}
	if c.SpreadsheetID == "" {
		return fmt.Errorf("INELIGIBLE_GOOGLE_SPREADSHEET_ID is required")
	}
	if strings.ContainsAny(c.SpreadsheetID, "/?#") {
		return fmt.Errorf("INELIGIBLE_GOOGLE_SPREADSHEET_ID must be an ID, not a URL")
	}
	if c.SheetGID == "" {
		return fmt.Errorf("INELIGIBLE_GOOGLE_SHEET_GID is required")
	}
	if _, err := strconv.ParseUint(c.SheetGID, 10, 64); err != nil {
		return fmt.Errorf("INELIGIBLE_GOOGLE_SHEET_GID must be numeric")
	}
	if c.SheetRange == "" || !strings.HasSuffix(strings.ToUpper(c.SheetRange), "!A:N") {
		return fmt.Errorf("INELIGIBLE_GOOGLE_SHEET_RANGE must select exactly A:N")
	}
	if c.ServiceAccountJSON == "" && c.ServiceAccountFile == "" {
		return fmt.Errorf("GOOGLE_SERVICE_ACCOUNT_JSON or GOOGLE_SERVICE_ACCOUNT_FILE is required")
	}
	if c.ServiceAccountJSON != "" && c.ServiceAccountFile != "" {
		return fmt.Errorf("set only one of GOOGLE_SERVICE_ACCOUNT_JSON and GOOGLE_SERVICE_ACCOUNT_FILE")
	}
	if err := c.Schema.Validate(); err != nil {
		return fmt.Errorf("INELIGIBLE_GOOGLE_SCHEMA_JSON: %w", err)
	}
	if err := validateGoogleAPIBaseURL("Google Sheets", c.SheetsBaseURL); err != nil {
		return err
	}
	if err := validateGoogleAPIBaseURL("Google Drive", c.DriveBaseURL); err != nil {
		return err
	}
	if c.HTTPTimeout <= 0 {
		return fmt.Errorf("Google API HTTP timeout must be positive")
	}
	if c.UploadDir == "" || (!filepath.IsAbs(c.UploadDir) && !path.IsAbs(c.UploadDir)) {
		return fmt.Errorf("INELIGIBLE_UPLOAD_DIR must be an absolute path")
	}
	if c.UploadMaxFiles < 0 || c.UploadMaxFiles > 20 {
		return fmt.Errorf("ineligible upload max files must be from 0 to 20")
	}
	if c.UploadMaxFileBytes < 1 || c.UploadMaxFileBytes > 100<<20 || c.UploadMaxTotalBytes < 1 || c.UploadMaxTotalBytes > 250<<20 {
		return fmt.Errorf("ineligible upload byte limits are invalid")
	}
	if len(c.UploadContentTypes) == 0 {
		return fmt.Errorf("at least one ineligible upload content type is required")
	}
	seenContentTypes := make(map[string]struct{}, len(c.UploadContentTypes))
	for _, contentType := range c.UploadContentTypes {
		normalized := strings.ToLower(strings.TrimSpace(contentType))
		if normalized == "" || !strings.Contains(normalized, "/") {
			return fmt.Errorf("invalid ineligible upload content type %q", contentType)
		}
		if _, exists := seenContentTypes[normalized]; exists {
			return fmt.Errorf("duplicate ineligible upload content type %q", contentType)
		}
		seenContentTypes[normalized] = struct{}{}
	}
	return nil
}

// Validate ensures both the exact 14-column contract and all semantic field
// bindings are present before a row can be interpreted.
func (s Schema) Validate() error {
	if len(s.Headers) != GoogleFormsHeaderCount {
		return fmt.Errorf("headers must contain exactly %d values (got %d)", GoogleFormsHeaderCount, len(s.Headers))
	}
	seenHeaders := make(map[string]struct{}, len(s.Headers))
	for i, header := range s.Headers {
		if header == "" {
			return fmt.Errorf("header %d is blank", i+1)
		}
		if _, exists := seenHeaders[header]; exists {
			return fmt.Errorf("header %q is duplicated", header)
		}
		seenHeaders[header] = struct{}{}
	}
	seenColumns := make(map[int]string, len(requiredColumns))
	for _, name := range requiredColumns {
		index, ok := s.Columns[name]
		if !ok {
			return fmt.Errorf("columns.%s is required", name)
		}
		if index < 0 || index >= len(s.Headers) {
			return fmt.Errorf("columns.%s index %d is outside A:N", name, index)
		}
		if prior, exists := seenColumns[index]; exists {
			return fmt.Errorf("columns.%s and columns.%s both use index %d", prior, name, index)
		}
		seenColumns[index] = name
	}
	return nil
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, strings.ToLower(trimmed))
		}
	}
	return result
}

func validateGoogleAPIBaseURL(label, value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("%s base URL is invalid", label)
	}
	if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return fmt.Errorf("%s base URL must use HTTPS", label)
	}
	return nil
}
