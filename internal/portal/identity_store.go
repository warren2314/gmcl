package portal

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type OIDCLoginState struct {
	NonceHash           [sha256.Size]byte
	PKCEVerifier        string
	ReturnTo            string
	InvitationTokenHash []byte
	StepUpRequested     bool
	ExpectedUserID      uuid.UUID
}

func (store *Store) SaveOIDCLoginState(
	ctx context.Context,
	stateHash [sha256.Size]byte,
	nonceHash [sha256.Size]byte,
	pkceVerifier string,
	returnTo string,
	invitationTokenHash []byte,
	stepUpRequested bool,
	expectedUserID uuid.UUID,
	expiresAt time.Time,
) error {
	if len(pkceVerifier) < 43 || len(pkceVerifier) > 128 {
		return fmt.Errorf("invalid PKCE verifier length")
	}
	if expiresAt.Before(store.now()) {
		return fmt.Errorf("OIDC login state expiry is in the past")
	}
	if !safePortalReturnTo(returnTo) {
		return fmt.Errorf("unsafe portal return path")
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_oidc_login_states (
				state_hash, nonce_hash, pkce_verifier, return_to,
				invitation_token_hash, step_up_requested, expected_user_id,
				expires_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, stateHash[:], nonceHash[:], pkceVerifier, returnTo,
			nullableBytes(invitationTokenHash), stepUpRequested,
			nullableUUID(expectedUserID), expiresAt); err != nil {
			return fmt.Errorf("save OIDC login state: %w", err)
		}
		return nil
	})
}

func (store *Store) ConsumeOIDCLoginState(
	ctx context.Context,
	rawState string,
) (OIDCLoginState, error) {
	stateHash, err := HashOpaqueToken(rawState)
	if err != nil {
		return OIDCLoginState{}, ErrUnauthenticated
	}
	var state OIDCLoginState
	now := store.now()
	err = store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var nonceHash []byte
		if err := tx.QueryRow(ctx, `
			UPDATE portal_oidc_login_states
			SET used_at = $2
			WHERE state_hash = $1
			  AND used_at IS NULL
			  AND expires_at > $2
			RETURNING nonce_hash, pkce_verifier, return_to,
				COALESCE(invitation_token_hash, ''::bytea),
				step_up_requested,
				COALESCE(expected_user_id, '00000000-0000-0000-0000-000000000000'::uuid)
		`, stateHash[:], now).Scan(
			&nonceHash,
			&state.PKCEVerifier,
			&state.ReturnTo,
			&state.InvitationTokenHash,
			&state.StepUpRequested,
			&state.ExpectedUserID,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnauthenticated
			}
			return fmt.Errorf("consume OIDC login state: %w", err)
		}
		if len(nonceHash) != sha256.Size {
			return ErrUnauthenticated
		}
		copy(state.NonceHash[:], nonceHash)
		return nil
	})
	if err != nil {
		return OIDCLoginState{}, err
	}
	return state, nil
}

type IdentityClaims struct {
	Issuer        string
	Subject       string
	DisplayName   string
	Email         string
	EmailVerified bool
}

func (claims IdentityClaims) Validate() error {
	if strings.TrimSpace(claims.Issuer) == "" || strings.TrimSpace(claims.Subject) == "" {
		return ErrUnauthenticated
	}
	if strings.TrimSpace(claims.DisplayName) == "" {
		return fmt.Errorf("identity has no display name")
	}
	if claims.Email != "" {
		address, err := mail.ParseAddress(claims.Email)
		if err != nil || !strings.EqualFold(address.Address, strings.TrimSpace(claims.Email)) {
			return fmt.Errorf("identity email is invalid")
		}
	}
	return nil
}

func (store *Store) ResolveIdentity(
	ctx context.Context,
	claims IdentityClaims,
) (uuid.UUID, error) {
	if err := claims.Validate(); err != nil {
		return uuid.Nil, err
	}
	var userID uuid.UUID
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT u.id, u.status
			FROM portal_identities i
			JOIN portal_users u ON u.id = i.user_id
			WHERE i.issuer = $1 AND i.subject = $2
		`, claims.Issuer, claims.Subject).Scan(&userID, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("resolve portal identity: %w", err)
		}
		if status != "active" {
			return ErrUnauthenticated
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_identities
			SET verified_email = CASE WHEN $4 THEN NULLIF($3, '') ELSE verified_email END,
			    email_verified = CASE WHEN $4 THEN TRUE ELSE email_verified END,
			    last_authenticated_at = $5
			WHERE issuer = $1 AND subject = $2
		`, claims.Issuer, claims.Subject, strings.ToLower(strings.TrimSpace(claims.Email)),
			claims.EmailVerified, store.now()); err != nil {
			return fmt.Errorf("update portal identity authentication: %w", err)
		}
		return nil
	})
	return userID, err
}

type InvitationRequest struct {
	ClubID                    int32
	Email                     string
	Role                      RoleKey
	TeamID                    *int32
	SeasonID                  *int32
	CompetitionID             *uuid.UUID
	OfficialEvidenceReference string
	ApprovedByAdminID         int32
	ExpiresAt                 time.Time
}

type Invitation struct {
	ID        uuid.UUID
	RawToken  string
	ExpiresAt time.Time
}

func (store *Store) CreateInvitation(
	ctx context.Context,
	request InvitationRequest,
	correlationID string,
) (Invitation, error) {
	if request.ClubID <= 0 || request.ApprovedByAdminID <= 0 || !validRole(request.Role) {
		return Invitation{}, ErrForbidden
	}
	address, err := mail.ParseAddress(strings.TrimSpace(request.Email))
	if err != nil || !strings.EqualFold(address.Address, strings.TrimSpace(request.Email)) {
		return Invitation{}, fmt.Errorf("invitation email is invalid")
	}
	request.Email = strings.ToLower(address.Address)
	request.OfficialEvidenceReference = strings.TrimSpace(request.OfficialEvidenceReference)
	if request.OfficialEvidenceReference == "" {
		return Invitation{}, fmt.Errorf("official-contact evidence reference is required")
	}
	now := store.now()
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = now.Add(72 * time.Hour)
	}
	if request.ExpiresAt.Before(now.Add(15*time.Minute)) || request.ExpiresAt.After(now.Add(7*24*time.Hour)) {
		return Invitation{}, fmt.Errorf("invitation expiry must be between 15 minutes and 7 days")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	rawToken, tokenHash, err := NewOpaqueToken()
	if err != nil {
		return Invitation{}, err
	}
	invitation := Invitation{ID: uuid.New(), RawToken: rawToken, ExpiresAt: request.ExpiresAt}

	err = store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_invitations (
				id, club_id, email, role_key, team_id, season_id,
				competition_id, token_hash, status,
				official_contact_evidence_reference,
				approved_by_admin_user_id, approved_at, expires_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, 'approved',
				$9, $10, $11, $12
			)
		`, invitation.ID, request.ClubID, request.Email, request.Role,
			request.TeamID, request.SeasonID, request.CompetitionID, tokenHash[:],
			request.OfficialEvidenceReference, request.ApprovedByAdminID, now,
			request.ExpiresAt); err != nil {
			return fmt.Errorf("create portal invitation: %w", err)
		}
		clubID := request.ClubID
		adminID := request.ApprovedByAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.invitation.approved",
			TargetType:    "portal_invitation",
			TargetID:      invitation.ID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"role":               request.Role,
				"expires_at":         request.ExpiresAt.UTC().Format(time.RFC3339),
				"evidence_reference": request.OfficialEvidenceReference,
			},
			OccurredAt: now,
		})
	})
	if err != nil {
		return Invitation{}, err
	}
	return invitation, nil
}

func (store *Store) RedeemInvitation(
	ctx context.Context,
	invitationTokenHash []byte,
	claims IdentityClaims,
	correlationID string,
) (uuid.UUID, error) {
	if len(invitationTokenHash) != sha256.Size || !claims.EmailVerified {
		return uuid.Nil, ErrUnauthenticated
	}
	if err := claims.Validate(); err != nil {
		return uuid.Nil, err
	}
	if strings.TrimSpace(claims.Email) == "" {
		return uuid.Nil, ErrUnauthenticated
	}
	now := store.now()
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	var userID uuid.UUID

	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			invitationID      uuid.UUID
			clubID            int32
			invitedEmail      string
			role              RoleKey
			teamID            *int32
			seasonID          *int32
			competitionID     *uuid.UUID
			approvedByAdminID *int32
			approvedByUserID  *uuid.UUID
		)
		if err := tx.QueryRow(ctx, `
			SELECT
				id, club_id, email, role_key, team_id, season_id,
				competition_id, approved_by_admin_user_id, approved_by_user_id
			FROM portal_invitations
			WHERE token_hash = $1
			  AND status = 'approved'
			  AND redeemed_at IS NULL
			  AND revoked_at IS NULL
			  AND expires_at > $2
			FOR UPDATE
		`, invitationTokenHash, now).Scan(
			&invitationID, &clubID, &invitedEmail, &role, &teamID, &seasonID,
			&competitionID, &approvedByAdminID, &approvedByUserID,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnauthenticated
			}
			return fmt.Errorf("load portal invitation: %w", err)
		}
		if !strings.EqualFold(invitedEmail, strings.TrimSpace(claims.Email)) {
			return ErrUnauthenticated
		}

		err := tx.QueryRow(ctx, `
			SELECT user_id
			FROM portal_identities
			WHERE issuer = $1 AND subject = $2
			FOR UPDATE
		`, claims.Issuer, claims.Subject).Scan(&userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("resolve invited identity: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			userID = uuid.New()
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_users (id, display_name, status)
				VALUES ($1, $2, 'active')
			`, userID, strings.TrimSpace(claims.DisplayName)); err != nil {
				return fmt.Errorf("create invited portal user: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_identities (
					user_id, issuer, subject, verified_email,
					email_verified, last_authenticated_at
				)
				VALUES ($1, $2, $3, $4, TRUE, $5)
			`, userID, claims.Issuer, claims.Subject,
				strings.ToLower(strings.TrimSpace(claims.Email)), now); err != nil {
				return fmt.Errorf("link invited portal identity: %w", err)
			}
		} else {
			var userStatus string
			if err := tx.QueryRow(ctx, `
				SELECT status FROM portal_users WHERE id = $1 FOR UPDATE
			`, userID).Scan(&userStatus); err != nil {
				return fmt.Errorf("load invited portal user: %w", err)
			}
			if userStatus != "active" {
				return ErrUnauthenticated
			}
			if _, err := tx.Exec(ctx, `
				UPDATE portal_identities
				SET verified_email = $3, email_verified = TRUE,
				    last_authenticated_at = $4
				WHERE issuer = $1 AND subject = $2
			`, claims.Issuer, claims.Subject,
				strings.ToLower(strings.TrimSpace(claims.Email)), now); err != nil {
				return fmt.Errorf("update invited portal identity: %w", err)
			}
		}

		var membershipID uuid.UUID
		var membershipStatus string
		err = tx.QueryRow(ctx, `
			SELECT id, status
			FROM portal_club_memberships
			WHERE user_id = $1
			  AND club_id = $2
			  AND status IN ('pending', 'active', 'suspended')
			FOR UPDATE
		`, userID, clubID).Scan(&membershipID, &membershipStatus)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			membershipID = uuid.New()
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_club_memberships (
					id, user_id, club_id, status, approved_at
				)
				VALUES ($1, $2, $3, 'active', $4)
			`, membershipID, userID, clubID, now); err != nil {
				return fmt.Errorf("activate portal membership: %w", err)
			}
		case err != nil:
			return fmt.Errorf("load portal membership: %w", err)
		case membershipStatus == "suspended":
			return ErrForbidden
		case membershipStatus == "pending":
			if _, err := tx.Exec(ctx, `
				UPDATE portal_club_memberships
				SET status = 'active', approved_at = $2,
				    version = version + 1, updated_at = $2
				WHERE id = $1
			`, membershipID, now); err != nil {
				return fmt.Errorf("activate pending portal membership: %w", err)
			}
		}

		var existingAssignmentID uuid.UUID
		err = tx.QueryRow(ctx, `
			SELECT id
			FROM portal_role_assignments
			WHERE membership_id = $1
			  AND role_key = $2
			  AND team_id IS NOT DISTINCT FROM $3
			  AND season_id IS NOT DISTINCT FROM $4
			  AND competition_id IS NOT DISTINCT FROM $5
			  AND status IN ('pending', 'active', 'suspended')
			FOR UPDATE
		`, membershipID, role, teamID, seasonID, competitionID).Scan(&existingAssignmentID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_role_assignments (
					membership_id, user_id, club_id, role_key, team_id,
					season_id, competition_id, status, starts_at,
					approved_by_user_id, grant_reason
				)
				VALUES (
					$1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10
				)
			`, membershipID, userID, clubID, role, teamID, seasonID,
				competitionID, now, approvedByUserID,
				"approved portal invitation "+invitationID.String()); err != nil {
				return fmt.Errorf("activate portal role: %w", err)
			}
		case err != nil:
			return fmt.Errorf("load portal role: %w", err)
		default:
			var assignmentStatus string
			if err := tx.QueryRow(ctx, `
				SELECT status FROM portal_role_assignments WHERE id = $1
			`, existingAssignmentID).Scan(&assignmentStatus); err != nil {
				return fmt.Errorf("load existing portal role status: %w", err)
			}
			if assignmentStatus == "suspended" {
				return ErrForbidden
			}
			if assignmentStatus == "pending" {
				if _, err := tx.Exec(ctx, `
					UPDATE portal_role_assignments
					SET status = 'active', starts_at = $2,
					    version = version + 1, updated_at = $2
					WHERE id = $1
				`, existingAssignmentID, now); err != nil {
					return fmt.Errorf("activate pending portal role: %w", err)
				}
			}
		}

		tag, err := tx.Exec(ctx, `
			UPDATE portal_invitations
			SET status = 'redeemed', redeemed_at = $2, redeemed_by_user_id = $3
			WHERE id = $1 AND status = 'approved'
		`, invitationID, now, userID)
		if err != nil {
			return fmt.Errorf("redeem portal invitation: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrUnauthenticated
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_outbox_events (
				aggregate_type, aggregate_id, event_type, payload,
				idempotency_key, occurred_at
			)
			VALUES (
				'portal_invitation', $1::uuid, 'portal.invitation.redeemed',
				jsonb_build_object('club_id', $2::integer, 'user_id', $3::uuid),
				$4::text, $5::timestamptz
			)
		`, invitationID, clubID, userID,
			"portal-invitation-redeemed:"+invitationID.String(), now); err != nil {
			return fmt.Errorf("write invitation outbox event: %w", err)
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorUserID:   &userID,
			ActorKind:     "portal_user",
			ActingRoleKey: string(role),
			Action:        "portal.invitation.redeemed",
			TargetType:    "portal_invitation",
			TargetID:      invitationID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"approved_by_admin_id": approvedByAdminID,
				"approved_by_user_id":  approvedByUserID,
				"role":                 role,
			},
			OccurredAt: now,
		})
	})
	return userID, err
}

func safePortalReturnTo(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return value == "/portal" || strings.HasPrefix(value, "/portal/")
}
