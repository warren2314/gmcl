package portal

import (
	"slices"
	"testing"
	"time"
)

func TestSchemaPreflightAllowsDisabledExternalServices(t *testing.T) {
	t.Setenv("CLUB_PORTAL_ENABLED", "false")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("N8N_HMAC_SECRET", "")
	report, issues := InspectPortalEnvironment(
		PortalPreflightModeSchema,
		DefaultSessionPolicy(),
		OIDCConfig{},
	)
	if len(issues) != 0 {
		t.Fatalf("schema issues = %v", issues)
	}
	if report.PortalEnabled || report.OIDCEnabled {
		t.Fatalf("disabled environment reported enabled: %#v", report)
	}
}

func TestPilotPreflightRequiresCompleteFailClosedConfiguration(t *testing.T) {
	t.Setenv("CLUB_PORTAL_ENABLED", "true")
	t.Setenv("PUBLIC_BASE_URL", "https://portal-test.gmcl.example")
	t.Setenv("SMTP_HOST", "smtp.example")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_FROM", "GMCL Portal <portal@example.test>")
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("SMTP_REPLY_TO", "")
	t.Setenv("N8N_HMAC_SECRET", "01234567890123456789012345678901")
	config := OIDCConfig{
		Enabled:          true,
		IssuerURL:        "https://identity.example/tenant",
		ClientID:         "gmcl-portal-test",
		RedirectURL:      "https://portal-test.gmcl.example/portal/auth/callback",
		RequiredACR:      "urn:gmcl:normal",
		StepUpACR:        "urn:gmcl:strong",
		DiscoveryTimeout: 10 * time.Second,
	}
	report, issues := InspectPortalEnvironment(
		PortalPreflightModePilot,
		DefaultSessionPolicy(),
		config,
	)
	if len(issues) != 0 {
		t.Fatalf("pilot issues = %v", issues)
	}
	if !report.PortalEnabled || !report.OIDCEnabled ||
		!report.OIDCConfigurationValid || !report.BaselineACRConfigured ||
		!report.StepUpConfigured ||
		!report.HTTPSPublicBaseURL || !report.OIDCCallbackMatches ||
		!report.SMTPConfigured || !report.WorkerAuthConfigured {
		t.Fatalf("pilot report = %#v", report)
	}
}

func TestPilotPreflightReportsEveryUnsafeConfigurationGap(t *testing.T) {
	t.Setenv("CLUB_PORTAL_ENABLED", "false")
	t.Setenv("PUBLIC_BASE_URL", "http://portal-test.gmcl.example/path")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("N8N_HMAC_SECRET", "short")
	config := OIDCConfig{
		Enabled:          true,
		IssuerURL:        "https://identity.example/tenant",
		ClientID:         "gmcl-portal-test",
		RedirectURL:      "https://different.example/portal/auth/callback",
		DiscoveryTimeout: 10 * time.Second,
	}
	_, issues := InspectPortalEnvironment(
		PortalPreflightModePilot,
		DefaultSessionPolicy(),
		config,
	)
	for _, expected := range []string{
		"CLUB_PORTAL_ENABLED must be true for a pilot",
		"CLUB_PORTAL_OIDC_REQUIRED_ACR is required for pilot authentication",
		"CLUB_PORTAL_OIDC_STEP_UP_ACR is required for sensitive pilot actions",
		"PUBLIC_BASE_URL must be an origin-only HTTPS URL",
		"OIDC redirect URL must match PUBLIC_BASE_URL plus /portal/auth/callback",
		"SMTP_HOST is required for portal security notifications",
		"N8N_HMAC_SECRET must contain at least 32 bytes",
	} {
		if !slices.Contains(issues, expected) {
			t.Fatalf("missing issue %q in %v", expected, issues)
		}
	}
}

func TestPortalPublicBaseURLValidation(t *testing.T) {
	for _, value := range []string{
		"https://portal.gmcl.example",
		"https://portal.gmcl.example/",
	} {
		if _, err := validatePortalPublicBaseURL(value); err != nil {
			t.Fatalf("valid URL %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{
		"",
		"http://portal.gmcl.example",
		"https://user@portal.gmcl.example",
		"https://portal.gmcl.example/path",
		"https://portal.gmcl.example?query=1",
		"https://portal.gmcl.example/#fragment",
	} {
		if _, err := validatePortalPublicBaseURL(value); err == nil {
			t.Fatalf("invalid URL %q accepted", value)
		}
	}
}

func TestPortalDatabasePreflightIssues(t *testing.T) {
	report := PortalDatabasePreflight{
		ExpectedRLSTables:          12,
		RLSEnabledTables:           11,
		RLSForcedTables:            10,
		AppendOnlyTriggerReady:     false,
		AuditShapeConstraintsReady: false,
	}
	issues := PortalDatabasePreflightIssues(report)
	if len(issues) != 5 {
		t.Fatalf("issues = %v", issues)
	}

	ready := PortalDatabasePreflight{
		MigrationApplied:           true,
		ExpectedRLSTables:          12,
		RLSEnabledTables:           12,
		RLSForcedTables:            12,
		AppendOnlyTriggerReady:     true,
		AuditShapeConstraintsReady: true,
		Audit:                      &AuditIntegrityReport{},
	}
	if issues := PortalDatabasePreflightIssues(ready); len(issues) != 0 {
		t.Fatalf("ready issues = %v", issues)
	}
}
