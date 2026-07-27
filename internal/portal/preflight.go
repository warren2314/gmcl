package portal

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"

	"github.com/jackc/pgx/v5"
)

const (
	PortalPreflightModeSchema = "schema"
	PortalPreflightModePilot  = "pilot"

	requiredPortalMigration = "0049_portal_audit_hash_verification.sql"
)

var portalRLSTables = []string{
	"portal_users",
	"portal_identities",
	"portal_club_memberships",
	"portal_role_assignments",
	"portal_sessions",
	"portal_oidc_login_states",
	"portal_invitations",
	"portal_club_features",
	"portal_attachments",
	"portal_notifications",
	"portal_outbox_events",
	"portal_audit_events",
}

type PortalEnvironmentPreflight struct {
	Mode                   string `json:"mode"`
	PortalEnabled          bool   `json:"portal_enabled"`
	OIDCEnabled            bool   `json:"oidc_enabled"`
	OIDCConfigurationValid bool   `json:"oidc_configuration_valid"`
	OIDCProviderProfile    string `json:"oidc_provider_profile"`
	BaselineACRConfigured  bool   `json:"baseline_acr_configured"`
	BaselineAssuranceReady bool   `json:"baseline_assurance_ready"`
	StepUpConfigured       bool   `json:"step_up_configured"`
	CognitoPolicyVerified  bool   `json:"cognito_policy_verified"`
	HTTPSPublicBaseURL     bool   `json:"https_public_base_url"`
	OIDCCallbackMatches    bool   `json:"oidc_callback_matches_public_url"`
	SMTPConfigured         bool   `json:"smtp_configured"`
	WorkerAuthConfigured   bool   `json:"worker_auth_configured"`
	SessionIdleMinutes     int64  `json:"session_idle_minutes"`
	SessionAbsoluteHours   int64  `json:"session_absolute_hours"`
	StepUpMinutes          int64  `json:"step_up_minutes"`
}

type PortalDatabasePreflight struct {
	RequiredMigration          string                `json:"required_migration"`
	MigrationApplied           bool                  `json:"migration_applied"`
	ExpectedRLSTables          int                   `json:"expected_rls_tables"`
	RLSEnabledTables           int                   `json:"rls_enabled_tables"`
	RLSForcedTables            int                   `json:"rls_forced_tables"`
	AppendOnlyTriggerReady     bool                  `json:"append_only_trigger_ready"`
	AuditShapeConstraintsReady bool                  `json:"audit_shape_constraints_ready"`
	Audit                      *AuditIntegrityReport `json:"audit,omitempty"`
}

func InspectPortalEnvironment(
	mode string,
	policy SessionPolicy,
	oidcConfig OIDCConfig,
) (PortalEnvironmentPreflight, []string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	profile := oidcConfig.normalizedProviderProfile()
	cognitoProfile := profile == OIDCProviderCognito
	report := PortalEnvironmentPreflight{
		Mode:                  mode,
		PortalEnabled:         EnabledFromEnv(),
		OIDCEnabled:           oidcConfig.Enabled,
		OIDCProviderProfile:   string(profile),
		BaselineACRConfigured: strings.TrimSpace(oidcConfig.RequiredACR) != "",
		BaselineAssuranceReady: (cognitoProfile && oidcConfig.CognitoPolicyVerified) ||
			(!cognitoProfile && strings.TrimSpace(oidcConfig.RequiredACR) != ""),
		StepUpConfigured:      oidcConfig.stepUpConfigured(),
		CognitoPolicyVerified: cognitoProfile && oidcConfig.CognitoPolicyVerified,
		SMTPConfigured:        email.NewFromEnv().ValidateSensitiveDeliveryConfig() == nil,
		WorkerAuthConfigured:  len(strings.TrimSpace(os.Getenv("N8N_HMAC_SECRET"))) >= 32,
		SessionIdleMinutes:    int64(policy.IdleLifetime / time.Minute),
		SessionAbsoluteHours:  int64(policy.AbsoluteLifetime / time.Hour),
		StepUpMinutes:         int64(policy.StepUpLifetime / time.Minute),
	}
	var issues []string
	if mode != PortalPreflightModeSchema && mode != PortalPreflightModePilot {
		issues = append(issues, "preflight mode must be schema or pilot")
		return report, issues
	}

	if err := oidcConfig.Validate(); err != nil {
		issues = append(issues, "portal OIDC configuration is invalid")
	} else {
		report.OIDCConfigurationValid = true
	}

	publicURL, publicErr := validatePortalPublicBaseURL(os.Getenv("PUBLIC_BASE_URL"))
	report.HTTPSPublicBaseURL = publicErr == nil
	if oidcConfig.Enabled && publicErr == nil {
		expected := *publicURL
		expected.Path = "/portal/auth/callback"
		expected.RawPath = ""
		expected.RawQuery = ""
		expected.Fragment = ""
		redirect, err := url.Parse(oidcConfig.RedirectURL)
		report.OIDCCallbackMatches = err == nil && redirect.String() == expected.String()
	}

	if mode == PortalPreflightModeSchema {
		return report, issues
	}
	if !report.PortalEnabled {
		issues = append(issues, "CLUB_PORTAL_ENABLED must be true for a pilot")
	}
	if !report.OIDCEnabled {
		issues = append(issues, "CLUB_PORTAL_OIDC_ENABLED must be true for a pilot")
	}
	if cognitoProfile && !report.CognitoPolicyVerified {
		issues = append(
			issues,
			"CLUB_PORTAL_COGNITO_POLICY_VERIFIED must confirm the fail-closed Cognito policy checks",
		)
	}
	if !cognitoProfile && !report.BaselineACRConfigured {
		issues = append(
			issues,
			"CLUB_PORTAL_OIDC_REQUIRED_ACR is required for pilot authentication",
		)
	}
	if !report.StepUpConfigured {
		issues = append(issues, "CLUB_PORTAL_OIDC_STEP_UP_ACR is required for sensitive pilot actions")
	}
	if !report.HTTPSPublicBaseURL {
		issues = append(issues, "PUBLIC_BASE_URL must be an origin-only HTTPS URL")
	}
	if report.OIDCEnabled && !report.OIDCCallbackMatches {
		issues = append(issues, "OIDC redirect URL must match PUBLIC_BASE_URL plus /portal/auth/callback")
	}
	if !report.SMTPConfigured {
		issues = append(issues, "SMTP_HOST is required for portal security notifications")
	}
	if !report.WorkerAuthConfigured {
		issues = append(issues, "N8N_HMAC_SECRET must contain at least 32 bytes")
	}
	return report, issues
}

func validatePortalPublicBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("portal public base URL must be an origin-only HTTPS URL")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func (store *Store) InspectDatabasePreflight(
	ctx context.Context,
) (PortalDatabasePreflight, error) {
	report := PortalDatabasePreflight{
		RequiredMigration: requiredPortalMigration,
		ExpectedRLSTables: len(portalRLSTables),
	}
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM schema_migrations
				WHERE filename = $1
			)
		`, requiredPortalMigration).Scan(&report.MigrationApplied); err != nil {
			return fmt.Errorf("check portal preflight migration: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE class.relrowsecurity),
				COUNT(*) FILTER (WHERE class.relforcerowsecurity)
			FROM pg_class class
			JOIN pg_namespace namespace ON namespace.oid = class.relnamespace
			WHERE namespace.nspname = 'public'
			  AND class.relname = ANY($1::text[])
		`, portalRLSTables).Scan(
			&report.RLSEnabledTables,
			&report.RLSForcedTables,
		); err != nil {
			return fmt.Errorf("inspect portal RLS tables: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_trigger trigger
				JOIN pg_class class ON class.oid = trigger.tgrelid
				JOIN pg_namespace namespace ON namespace.oid = class.relnamespace
				WHERE namespace.nspname = 'public'
				  AND class.relname = 'portal_audit_events'
				  AND trigger.tgname = 'trg_portal_audit_append_only'
				  AND NOT trigger.tgisinternal
				  AND trigger.tgenabled IN ('O', 'A')
			)
		`).Scan(&report.AppendOnlyTriggerReady); err != nil {
			return fmt.Errorf("inspect portal audit trigger: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT
				COUNT(DISTINCT attribute.attname) FILTER (
					WHERE attribute.attname IN (
						'chain_position',
						'event_hash',
						'hash_version'
					)
					  AND attribute.attnotnull
				) = 3
				AND COUNT(DISTINCT con.conname) FILTER (
					WHERE con.conname IN (
						'portal_audit_chain_position_check',
						'portal_audit_event_hash_length_check',
						'portal_audit_previous_hash_shape_check',
						'portal_audit_events_hash_version_check'
					)
					  AND con.convalidated
				) = 4
			FROM pg_class class
			JOIN pg_namespace namespace ON namespace.oid = class.relnamespace
			LEFT JOIN pg_attribute attribute
				ON attribute.attrelid = class.oid
			   AND attribute.attnum > 0
			   AND NOT attribute.attisdropped
			LEFT JOIN pg_constraint con
				ON con.conrelid = class.oid
			WHERE namespace.nspname = 'public'
			  AND class.relname = 'portal_audit_events'
		`).Scan(&report.AuditShapeConstraintsReady); err != nil {
			return fmt.Errorf("inspect portal audit constraints: %w", err)
		}
		return nil
	})
	if err != nil {
		return PortalDatabasePreflight{}, err
	}
	if report.MigrationApplied {
		audit, err := store.VerifyAuditIntegrity(ctx)
		if err != nil {
			return PortalDatabasePreflight{}, err
		}
		report.Audit = &audit
	}
	return report, nil
}

func PortalDatabasePreflightIssues(report PortalDatabasePreflight) []string {
	var issues []string
	if !report.MigrationApplied {
		issues = append(issues, "required portal migration is not applied")
	}
	if report.RLSEnabledTables != report.ExpectedRLSTables {
		issues = append(issues, "one or more portal-private tables do not have RLS enabled")
	}
	if report.RLSForcedTables != report.ExpectedRLSTables {
		issues = append(issues, "one or more portal-private tables do not force RLS")
	}
	if !report.AppendOnlyTriggerReady {
		issues = append(issues, "portal audit append-only trigger is unavailable")
	}
	if !report.AuditShapeConstraintsReady {
		issues = append(issues, "portal audit chain constraints are incomplete")
	}
	if report.MigrationApplied && report.Audit == nil {
		issues = append(issues, "portal audit integrity was not verified")
	}
	return issues
}
