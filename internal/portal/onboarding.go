package portal

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type OnboardingIdentityStatus string

const (
	OnboardingIdentityPending          OnboardingIdentityStatus = "pending"
	OnboardingIdentityCreated          OnboardingIdentityStatus = "created"
	OnboardingIdentityExisting         OnboardingIdentityStatus = "existing"
	OnboardingIdentityConfirmed        OnboardingIdentityStatus = "confirmed"
	OnboardingIdentityInvitationResent OnboardingIdentityStatus = "invitation_resent"
	OnboardingIdentityManualRequired   OnboardingIdentityStatus = "manual_required"
	OnboardingIdentityManualConfirmed  OnboardingIdentityStatus = "manual_confirmed"
	OnboardingIdentityFailed           OnboardingIdentityStatus = "failed"
)

var StandardOnboardingFeatures = []FeatureKey{
	FeaturePortalAccess,
	FeatureReadOnlyDashboard,
	FeatureSecureMessaging,
}

type OnboardingRun struct {
	ID                        uuid.UUID
	ClubID                    int32
	ClubName                  string
	OfficialName              string
	Email                     string
	Role                      RoleKey
	EvidenceReference         string
	FeatureKeys               []FeatureKey
	EnabledFeatureKeys        []FeatureKey
	Status                    string
	IdentityStatus            OnboardingIdentityStatus
	CognitoUsername           string
	CognitoUserStatus         string
	CognitoEmailVerified      *bool
	CognitoLastCheckedAt      *time.Time
	CurrentInvitationID       *uuid.UUID
	CurrentInvitationStatus   string
	CurrentInvitationExpires  *time.Time
	CurrentInvitationRedeemed *time.Time
	LastError                 string
	CreatedByAdminID          int32
	ActivatedAt               *time.Time
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

func (run OnboardingRun) IdentityReady() bool {
	switch run.IdentityStatus {
	case OnboardingIdentityCreated,
		OnboardingIdentityExisting,
		OnboardingIdentityConfirmed,
		OnboardingIdentityInvitationResent,
		OnboardingIdentityManualConfirmed:
		return true
	default:
		return false
	}
}

func (run OnboardingRun) Activated() bool {
	return run.Status == "activated" && run.ActivatedAt != nil
}

type OnboardingRunRequest struct {
	ClubID            int32
	OfficialName      string
	Email             string
	Role              RoleKey
	EvidenceReference string
	CreatedByAdminID  int32
}

func normalizeOnboardingEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(address.Address, value) {
		return "", fmt.Errorf("named official email is invalid")
	}
	return strings.ToLower(address.Address), nil
}

func normalizeOnboardingFeatures(values []FeatureKey) ([]FeatureKey, error) {
	seen := make(map[FeatureKey]struct{}, len(values)+1)
	seen[FeaturePortalAccess] = struct{}{}
	for _, key := range values {
		if _, ok := validFeatureKeys[key]; !ok {
			return nil, fmt.Errorf("unknown portal feature %q", key)
		}
		seen[key] = struct{}{}
	}
	ordered := make([]FeatureKey, 0, len(seen))
	for _, key := range []FeatureKey{
		FeaturePortalAccess,
		FeatureReadOnlyDashboard,
		FeatureSecureMessaging,
		FeatureClubSelfService,
		FeatureJuniorAdministration,
		FeaturePlayerIdentity,
		FeatureRegistration,
		FeatureFixtureOptimisation,
	} {
		if _, ok := seen[key]; ok {
			ordered = append(ordered, key)
		}
	}
	return ordered, nil
}

func onboardingFeatureStrings(features []FeatureKey) []string {
	values := make([]string, 0, len(features))
	for _, feature := range features {
		values = append(values, string(feature))
	}
	return values
}

func featureKeysFromStrings(values []string) []FeatureKey {
	result := make([]FeatureKey, 0, len(values))
	for _, value := range values {
		if key, ok := ParseFeatureKey(value); ok {
			result = append(result, key)
		}
	}
	return result
}

func (store *Store) CreateOnboardingRun(
	ctx context.Context,
	request OnboardingRunRequest,
	correlationID string,
) (OnboardingRun, error) {
	request.OfficialName = strings.TrimSpace(request.OfficialName)
	request.EvidenceReference = strings.TrimSpace(request.EvidenceReference)
	emailAddress, err := normalizeOnboardingEmail(request.Email)
	if err != nil {
		return OnboardingRun{}, err
	}
	request.Email = emailAddress
	if request.ClubID <= 0 || request.CreatedByAdminID <= 0 ||
		request.OfficialName == "" || len(request.OfficialName) > 200 ||
		request.EvidenceReference == "" || len(request.EvidenceReference) > 500 ||
		!adminPortalOnboardingRoleAllowed(request.Role) {
		return OnboardingRun{}, ErrForbidden
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	run := OnboardingRun{
		ID:                uuid.New(),
		ClubID:            request.ClubID,
		OfficialName:      request.OfficialName,
		Email:             request.Email,
		Role:              request.Role,
		EvidenceReference: request.EvidenceReference,
		FeatureKeys:       append([]FeatureKey(nil), StandardOnboardingFeatures...),
		Status:            "draft",
		IdentityStatus:    OnboardingIdentityPending,
		CreatedByAdminID:  request.CreatedByAdminID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	err = store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubExists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM clubs WHERE id = $1)
		`, request.ClubID).Scan(&clubExists); err != nil {
			return fmt.Errorf("check onboarding club: %w", err)
		}
		if !clubExists {
			return ErrNotFound
		}
		var duplicate bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM portal_onboarding_runs
				WHERE club_id = $1
				  AND lower(email) = lower($2)
				  AND status IN (
				      'draft',
				      'identity_pending',
				      'identity_ready',
				      'invitation_sent'
				  )
			)
		`, request.ClubID, request.Email).Scan(&duplicate); err != nil {
			return fmt.Errorf("check duplicate onboarding: %w", err)
		}
		if duplicate {
			return fmt.Errorf("an active onboarding run already exists for this club and email")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_onboarding_runs (
				id, club_id, official_name, email, role_key,
				official_contact_evidence_reference, feature_keys,
				status, identity_status, created_by_admin_user_id,
				created_at, updated_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				'draft', 'pending', $8, $9, $9
			)
		`, run.ID, request.ClubID, request.OfficialName, request.Email,
			request.Role, request.EvidenceReference,
			onboardingFeatureStrings(run.FeatureKeys),
			request.CreatedByAdminID, now); err != nil {
			return fmt.Errorf("create portal onboarding run: %w", err)
		}
		adminID := request.CreatedByAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &request.ClubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.started",
			TargetType:    "portal_onboarding_run",
			TargetID:      run.ID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"role": request.Role,
			},
			OccurredAt: now,
		})
	})
	return run, err
}

func adminPortalOnboardingRoleAllowed(role RoleKey) bool {
	switch role {
	case RoleClubPrimaryAdmin, RoleClubAdmin, RoleClubSecretary, RoleReadOnlyClubUser:
		return true
	default:
		return false
	}
}

func (store *Store) GetOnboardingRun(
	ctx context.Context,
	runID uuid.UUID,
) (OnboardingRun, error) {
	if runID == uuid.Nil {
		return OnboardingRun{}, ErrNotFound
	}
	var run OnboardingRun
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		var (
			featureValues        []string
			enabledFeatureValues []string
		)
		if err := tx.QueryRow(ctx, `
			SELECT
				run.id,
				run.club_id,
				club.name,
				run.official_name,
				run.email,
				run.role_key,
				run.official_contact_evidence_reference,
				run.feature_keys,
				COALESCE((
					SELECT array_agg(feature.feature_key ORDER BY feature.feature_key)
					FROM portal_club_features feature
					WHERE feature.club_id = run.club_id
					  AND feature.enabled
				), ARRAY[]::text[]),
				run.status,
				run.identity_status,
				COALESCE(run.cognito_username, ''),
				COALESCE(run.cognito_user_status, ''),
				run.cognito_email_verified,
				run.cognito_last_checked_at,
				run.current_invitation_id,
				COALESCE(invitation.status, ''),
				invitation.expires_at,
				invitation.redeemed_at,
				COALESCE(run.last_error, ''),
				run.created_by_admin_user_id,
				run.activated_at,
				run.created_at,
				run.updated_at
			FROM portal_onboarding_runs run
			JOIN clubs club ON club.id = run.club_id
			LEFT JOIN portal_invitations invitation
			  ON invitation.id = run.current_invitation_id
			WHERE run.id = $1
		`, runID).Scan(
			&run.ID,
			&run.ClubID,
			&run.ClubName,
			&run.OfficialName,
			&run.Email,
			&run.Role,
			&run.EvidenceReference,
			&featureValues,
			&enabledFeatureValues,
			&run.Status,
			&run.IdentityStatus,
			&run.CognitoUsername,
			&run.CognitoUserStatus,
			&run.CognitoEmailVerified,
			&run.CognitoLastCheckedAt,
			&run.CurrentInvitationID,
			&run.CurrentInvitationStatus,
			&run.CurrentInvitationExpires,
			&run.CurrentInvitationRedeemed,
			&run.LastError,
			&run.CreatedByAdminID,
			&run.ActivatedAt,
			&run.CreatedAt,
			&run.UpdatedAt,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load portal onboarding run: %w", err)
		}
		run.FeatureKeys = featureKeysFromStrings(featureValues)
		run.EnabledFeatureKeys = featureKeysFromStrings(enabledFeatureValues)
		return nil
	})
	return run, err
}

func (store *Store) ListOnboardingRuns(
	ctx context.Context,
	limit int,
) ([]OnboardingRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var runs []OnboardingRun
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				run.id,
				run.club_id,
				club.name,
				run.official_name,
				run.email,
				run.role_key,
				run.status,
				run.identity_status,
				COALESCE(run.last_error, ''),
				run.created_at,
				run.updated_at,
				run.activated_at
			FROM portal_onboarding_runs run
			JOIN clubs club ON club.id = run.club_id
			ORDER BY run.created_at DESC
			LIMIT $1
		`, limit)
		if err != nil {
			return fmt.Errorf("list portal onboarding runs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var run OnboardingRun
			if err := rows.Scan(
				&run.ID,
				&run.ClubID,
				&run.ClubName,
				&run.OfficialName,
				&run.Email,
				&run.Role,
				&run.Status,
				&run.IdentityStatus,
				&run.LastError,
				&run.CreatedAt,
				&run.UpdatedAt,
				&run.ActivatedAt,
			); err != nil {
				return fmt.Errorf("scan portal onboarding run: %w", err)
			}
			runs = append(runs, run)
		}
		return rows.Err()
	})
	return runs, err
}

func (store *Store) UpdateOnboardingFeatures(
	ctx context.Context,
	runID uuid.UUID,
	features []FeatureKey,
	legacyAdminID int32,
	correlationID string,
) error {
	if runID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	normalized, err := normalizeOnboardingFeatures(features)
	if err != nil {
		return err
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			UPDATE portal_onboarding_runs
			SET feature_keys = $2,
			    updated_at = $3,
			    last_error = NULL
			WHERE id = $1
			  AND status IN ('draft', 'identity_pending', 'identity_ready', 'failed')
			RETURNING club_id
		`, runID, onboardingFeatureStrings(normalized), now).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("update onboarding features: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.features_selected",
			TargetType:    "portal_onboarding_run",
			TargetID:      runID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"features": onboardingFeatureStrings(normalized),
			},
			OccurredAt: now,
		})
	})
}

type OnboardingIdentityResult struct {
	Status        OnboardingIdentityStatus
	Username      string
	UserStatus    string
	EmailVerified bool
}

func (store *Store) RecordOnboardingIdentity(
	ctx context.Context,
	runID uuid.UUID,
	result OnboardingIdentityResult,
	legacyAdminID int32,
	correlationID string,
) error {
	if runID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	switch result.Status {
	case OnboardingIdentityCreated,
		OnboardingIdentityExisting,
		OnboardingIdentityConfirmed,
		OnboardingIdentityInvitationResent,
		OnboardingIdentityManualRequired,
		OnboardingIdentityManualConfirmed,
		OnboardingIdentityFailed:
	default:
		return fmt.Errorf("invalid onboarding identity status")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	runStatus := "identity_ready"
	if result.Status == OnboardingIdentityManualRequired ||
		result.Status == OnboardingIdentityFailed {
		runStatus = "identity_pending"
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			UPDATE portal_onboarding_runs
			SET status = $2,
			    identity_status = $3,
			    cognito_username = NULLIF($4, ''),
			    cognito_user_status = NULLIF($5, ''),
			    cognito_email_verified = $6,
			    cognito_last_checked_at = $7,
			    last_error = NULL,
			    updated_at = $7
			WHERE id = $1
			  AND status <> 'activated'
			  AND status <> 'cancelled'
			RETURNING club_id
		`, runID, runStatus, result.Status, strings.TrimSpace(result.Username),
			strings.TrimSpace(result.UserStatus), result.EmailVerified, now).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("record onboarding identity: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.identity_checked",
			TargetType:    "portal_onboarding_run",
			TargetID:      runID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"identity_status": result.Status,
				"user_status":     strings.TrimSpace(result.UserStatus),
				"email_verified":  result.EmailVerified,
			},
			OccurredAt: now,
		})
	})
}

func (store *Store) RecordOnboardingError(
	ctx context.Context,
	runID uuid.UUID,
	stage string,
	recordedError error,
	legacyAdminID int32,
	correlationID string,
) error {
	if runID == uuid.Nil || recordedError == nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	stage = strings.TrimSpace(stage)
	if stage != "identity" && stage != "invitation" {
		return fmt.Errorf("invalid onboarding failure stage")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	runStatus := "identity_pending"
	identityStatus := OnboardingIdentityFailed
	if stage == "invitation" {
		runStatus = "identity_ready"
		identityStatus = ""
	}
	message := strings.TrimSpace(recordedError.Error())
	if len(message) > 2000 {
		message = message[:2000]
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			UPDATE portal_onboarding_runs
			SET status = $2,
			    identity_status = CASE
			        WHEN $3 = '' THEN identity_status
			        ELSE $3
			    END,
			    last_error = $4,
			    updated_at = $5
			WHERE id = $1
			  AND status <> 'activated'
			  AND status <> 'cancelled'
			RETURNING club_id
		`, runID, runStatus, identityStatus, message, now).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("record onboarding failure: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.failed",
			TargetType:    "portal_onboarding_run",
			TargetID:      runID.String(),
			Outcome:       "failure",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"stage": stage,
			},
			OccurredAt: now,
		})
	})
}

func (store *Store) EnableOnboardingFeatures(
	ctx context.Context,
	runID uuid.UUID,
	legacyAdminID int32,
	notes string,
	correlationID string,
) error {
	if runID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			clubID       int32
			featureNames []string
		)
		if err := tx.QueryRow(ctx, `
			SELECT club_id, feature_keys
			FROM portal_onboarding_runs
			WHERE id = $1
			  AND status IN ('identity_ready', 'invitation_sent')
			FOR UPDATE
		`, runID).Scan(&clubID, &featureNames); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("load onboarding feature selection: %w", err)
		}
		features, err := normalizeOnboardingFeatures(featureKeysFromStrings(featureNames))
		if err != nil {
			return err
		}
		for _, key := range features {
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_club_features (
					club_id, feature_key, enabled, enabled_at,
					enabled_by_user_id, enabled_by_admin_user_id,
					notes, updated_at
				)
				VALUES ($1, $2, TRUE, $3, NULL, $4, $5, $3)
				ON CONFLICT (club_id, feature_key)
				DO UPDATE SET
					enabled = TRUE,
					enabled_at = COALESCE(portal_club_features.enabled_at, EXCLUDED.enabled_at),
					enabled_by_user_id = NULL,
					enabled_by_admin_user_id = EXCLUDED.enabled_by_admin_user_id,
					notes = EXCLUDED.notes,
					updated_at = EXCLUDED.updated_at
			`, clubID, key, now, legacyAdminID, nullableString(notes)); err != nil {
				return fmt.Errorf("enable onboarding feature %s: %w", key, err)
			}
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.features_enabled",
			TargetType:    "portal_onboarding_run",
			TargetID:      runID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"features": onboardingFeatureStrings(features),
			},
			OccurredAt: now,
		})
	})
}

func (store *Store) PrepareOnboardingInvitation(
	ctx context.Context,
	runID uuid.UUID,
	legacyAdminID int32,
	correlationID string,
) error {
	if runID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			clubID       int32
			invitationID *uuid.UUID
		)
		if err := tx.QueryRow(ctx, `
			SELECT club_id, current_invitation_id
			FROM portal_onboarding_runs
			WHERE id = $1
			  AND status IN ('identity_ready', 'invitation_sent')
			FOR UPDATE
		`, runID).Scan(&clubID, &invitationID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("lock onboarding invitation: %w", err)
		}
		if invitationID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE portal_invitations
				SET status = 'revoked', revoked_at = $2
				WHERE id = $1
				  AND status = 'approved'
				  AND redeemed_at IS NULL
			`, *invitationID, now); err != nil {
				return fmt.Errorf("revoke previous onboarding invitation: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_onboarding_runs
			SET current_invitation_id = NULL,
			    status = 'identity_ready',
			    last_error = NULL,
			    updated_at = $2
			WHERE id = $1
		`, runID, now); err != nil {
			return fmt.Errorf("prepare onboarding invitation: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.invitation_prepared",
			TargetType:    "portal_onboarding_run",
			TargetID:      runID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			OccurredAt:    now,
		})
	})
}

func (store *Store) AttachOnboardingInvitation(
	ctx context.Context,
	runID, invitationID uuid.UUID,
	legacyAdminID int32,
	correlationID string,
) error {
	if runID == uuid.Nil || invitationID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			UPDATE portal_onboarding_runs run
			SET current_invitation_id = $2,
			    status = 'identity_ready',
			    last_error = NULL,
			    updated_at = $3
			WHERE run.id = $1
			  AND EXISTS (
			      SELECT 1
			      FROM portal_invitations invitation
			      WHERE invitation.id = $2
			        AND invitation.onboarding_run_id = run.id
			        AND invitation.club_id = run.club_id
			  )
			RETURNING run.club_id
		`, runID, invitationID, now).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("attach onboarding invitation: %w", err)
		}
		return nil
	})
}

func (store *Store) MarkOnboardingInvitationSent(
	ctx context.Context,
	runID, invitationID uuid.UUID,
	legacyAdminID int32,
	correlationID string,
) error {
	if runID == uuid.Nil || invitationID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			UPDATE portal_onboarding_runs
			SET status = 'invitation_sent',
			    last_error = NULL,
			    updated_at = $3
			WHERE id = $1
			  AND current_invitation_id = $2
			  AND status = 'identity_ready'
			RETURNING club_id
		`, runID, invitationID, now).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("mark onboarding invitation sent: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.invitation_sent",
			TargetType:    "portal_onboarding_run",
			TargetID:      runID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			OccurredAt:    now,
		})
	})
}

func (store *Store) CorrectOnboardingEmail(
	ctx context.Context,
	runID uuid.UUID,
	newEmail string,
	legacyAdminID int32,
	correlationID string,
) error {
	if runID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	emailAddress, err := normalizeOnboardingEmail(newEmail)
	if err != nil {
		return err
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			clubID       int32
			currentEmail string
			invitationID *uuid.UUID
		)
		if err := tx.QueryRow(ctx, `
			SELECT club_id, email, current_invitation_id
			FROM portal_onboarding_runs
			WHERE id = $1
			  AND status <> 'activated'
			  AND status <> 'cancelled'
			FOR UPDATE
		`, runID).Scan(&clubID, &currentEmail, &invitationID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("lock onboarding email correction: %w", err)
		}
		if invitationID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE portal_invitations
				SET status = 'revoked', revoked_at = $2
				WHERE id = $1
				  AND status = 'approved'
				  AND redeemed_at IS NULL
			`, *invitationID, now); err != nil {
				return fmt.Errorf("revoke invitation for email correction: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_onboarding_runs
			SET email = $2,
			    status = 'draft',
			    identity_status = 'pending',
			    cognito_username = NULL,
			    cognito_user_status = NULL,
			    cognito_email_verified = NULL,
			    cognito_last_checked_at = NULL,
			    current_invitation_id = NULL,
			    last_error = NULL,
			    updated_at = $3
			WHERE id = $1
		`, runID, emailAddress, now); err != nil {
			return fmt.Errorf("correct onboarding email: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.onboarding.email_corrected",
			TargetType:    "portal_onboarding_run",
			TargetID:      runID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"email_changed": !strings.EqualFold(currentEmail, emailAddress),
			},
			OccurredAt: now,
		})
	})
}

func OnboardingFeaturesComplete(run OnboardingRun) bool {
	enabled := make(map[FeatureKey]struct{}, len(run.EnabledFeatureKeys))
	for _, feature := range run.EnabledFeatureKeys {
		enabled[feature] = struct{}{}
	}
	for _, required := range run.FeatureKeys {
		if _, ok := enabled[required]; !ok {
			return false
		}
	}
	return true
}

func SortedOnboardingFeatures(features []FeatureKey) []FeatureKey {
	result := append([]FeatureKey(nil), features...)
	sort.SliceStable(result, func(i, j int) bool {
		return string(result[i]) < string(result[j])
	})
	return result
}
