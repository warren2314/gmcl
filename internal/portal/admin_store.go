package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type FeatureKey string

const (
	FeaturePortalAccess         FeatureKey = "portal_access"
	FeatureReadOnlyDashboard    FeatureKey = "read_only_dashboard"
	FeatureSecureMessaging      FeatureKey = "secure_messaging"
	FeatureClubSelfService      FeatureKey = "club_self_service"
	FeatureJuniorAdministration FeatureKey = "junior_administration"
	FeaturePlayerIdentity       FeatureKey = "player_identity"
	FeatureRegistration         FeatureKey = "registration"
	FeatureFixtureOptimisation  FeatureKey = "fixture_optimisation"
)

var validFeatureKeys = map[FeatureKey]struct{}{
	FeaturePortalAccess:         {},
	FeatureReadOnlyDashboard:    {},
	FeatureSecureMessaging:      {},
	FeatureClubSelfService:      {},
	FeatureJuniorAdministration: {},
	FeaturePlayerIdentity:       {},
	FeatureRegistration:         {},
	FeatureFixtureOptimisation:  {},
}

func ParseFeatureKey(value string) (FeatureKey, bool) {
	key := FeatureKey(strings.ToLower(strings.TrimSpace(value)))
	_, ok := validFeatureKeys[key]
	return key, ok
}

func (store *Store) FeatureEnabled(
	ctx context.Context,
	principal Principal,
	key FeatureKey,
) (bool, error) {
	if principal.Assignment == nil {
		return false, ErrForbidden
	}
	if _, ok := validFeatureKeys[key]; !ok {
		return false, ErrForbidden
	}
	var enabled bool
	err := store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE((
				SELECT enabled
				FROM portal_club_features
				WHERE club_id = $1 AND feature_key = $2
			), FALSE)
		`, assignment.Scope.ClubID, key).Scan(&enabled); err != nil {
			return fmt.Errorf("load portal feature flag: %w", err)
		}
		return nil
	})
	return enabled, err
}

type PilotClub struct {
	ID                int32
	Name              string
	PortalAccess      bool
	ReadOnlyDashboard bool
}

type ClubReconciliationSummary struct {
	ClubID                int32
	ClubName              string
	ActiveTeams           int64
	MappedActiveTeams     int64
	ActiveCaptainContacts int64
	ActiveMemberships     int64
	ActiveAssignments     int64
	LastFixtureSyncAt     *time.Time
}

func (summary ClubReconciliationSummary) TeamMappingsComplete() bool {
	return summary.ActiveTeams > 0 && summary.MappedActiveTeams == summary.ActiveTeams
}

func (store *Store) ListClubReconciliation(
	ctx context.Context,
) ([]ClubReconciliationSummary, error) {
	now := store.now()
	var summaries []ClubReconciliationSummary
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH team_summary AS (
				SELECT
					club_id,
					COUNT(*) FILTER (WHERE active) AS active_teams,
					COUNT(*) FILTER (
						WHERE active
						  AND NULLIF(BTRIM(play_cricket_team_id), '') IS NOT NULL
					) AS mapped_active_teams
				FROM teams
				GROUP BY club_id
			),
			captain_summary AS (
				SELECT
					t.club_id,
					COUNT(DISTINCT c.id) AS active_captain_contacts
				FROM captains c
				JOIN teams t ON t.id = c.team_id
				WHERE t.active
				  AND c.active_from <= ($1::timestamptz AT TIME ZONE 'Europe/London')::date
				  AND (
					c.active_to IS NULL
					OR c.active_to >= ($1::timestamptz AT TIME ZONE 'Europe/London')::date
				  )
				GROUP BY t.club_id
			),
			membership_summary AS (
				SELECT
					club_id,
					COUNT(*) FILTER (
						WHERE status = 'active'
						  AND starts_at <= $1::timestamptz
						  AND (ends_at IS NULL OR ends_at > $1::timestamptz)
					) AS active_memberships
				FROM portal_club_memberships
				GROUP BY club_id
			),
			assignment_summary AS (
				SELECT
					club_id,
					COUNT(*) FILTER (
						WHERE status = 'active'
						  AND starts_at <= $1::timestamptz
						  AND (ends_at IS NULL OR ends_at > $1::timestamptz)
					) AS active_assignments
				FROM portal_role_assignments
				GROUP BY club_id
			),
			fixture_team_sync AS (
				SELECT t.club_id, MAX(lf.fetched_at) AS last_fixture_sync_at
				FROM teams t
				JOIN league_fixtures lf
				  ON BTRIM(lf.home_team_pc_id) = BTRIM(t.play_cricket_team_id)
				WHERE NULLIF(BTRIM(t.play_cricket_team_id), '') IS NOT NULL
				GROUP BY t.club_id
				UNION ALL
				SELECT t.club_id, MAX(lf.fetched_at) AS last_fixture_sync_at
				FROM teams t
				JOIN league_fixtures lf
				  ON BTRIM(lf.away_team_pc_id) = BTRIM(t.play_cricket_team_id)
				WHERE NULLIF(BTRIM(t.play_cricket_team_id), '') IS NOT NULL
				GROUP BY t.club_id
			),
			fixture_summary AS (
				SELECT club_id, MAX(last_fixture_sync_at) AS last_fixture_sync_at
				FROM fixture_team_sync
				GROUP BY club_id
			)
			SELECT
				c.id,
				c.name,
				COALESCE(ts.active_teams, 0),
				COALESCE(ts.mapped_active_teams, 0),
				COALESCE(cs.active_captain_contacts, 0),
				COALESCE(ms.active_memberships, 0),
				COALESCE(asum.active_assignments, 0),
				fs.last_fixture_sync_at
			FROM clubs c
			LEFT JOIN team_summary ts ON ts.club_id = c.id
			LEFT JOIN captain_summary cs ON cs.club_id = c.id
			LEFT JOIN membership_summary ms ON ms.club_id = c.id
			LEFT JOIN assignment_summary asum ON asum.club_id = c.id
			LEFT JOIN fixture_summary fs ON fs.club_id = c.id
			ORDER BY c.name, c.id
		`, now)
		if err != nil {
			return fmt.Errorf("list portal club reconciliation: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				summary  ClubReconciliationSummary
				lastSync pgtype.Timestamptz
			)
			if err := rows.Scan(
				&summary.ClubID,
				&summary.ClubName,
				&summary.ActiveTeams,
				&summary.MappedActiveTeams,
				&summary.ActiveCaptainContacts,
				&summary.ActiveMemberships,
				&summary.ActiveAssignments,
				&lastSync,
			); err != nil {
				return fmt.Errorf("scan portal club reconciliation: %w", err)
			}
			summary.LastFixtureSyncAt = timePtr(lastSync)
			summaries = append(summaries, summary)
		}
		return rows.Err()
	})
	return summaries, err
}

func (store *Store) ListPilotClubs(ctx context.Context) ([]PilotClub, error) {
	var clubs []PilotClub
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				c.id,
				c.name,
				COALESCE(bool_or(f.enabled) FILTER (
					WHERE f.feature_key = 'portal_access'
				), FALSE) AS portal_access,
				COALESCE(bool_or(f.enabled) FILTER (
					WHERE f.feature_key = 'read_only_dashboard'
				), FALSE) AS read_only_dashboard
			FROM clubs c
			LEFT JOIN portal_club_features f ON f.club_id = c.id
			GROUP BY c.id, c.name
			ORDER BY c.name
		`)
		if err != nil {
			return fmt.Errorf("list portal pilot clubs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var club PilotClub
			if err := rows.Scan(
				&club.ID,
				&club.Name,
				&club.PortalAccess,
				&club.ReadOnlyDashboard,
			); err != nil {
				return fmt.Errorf("scan portal pilot club: %w", err)
			}
			clubs = append(clubs, club)
		}
		return rows.Err()
	})
	return clubs, err
}

func (store *Store) SetClubFeature(
	ctx context.Context,
	clubID int32,
	key FeatureKey,
	enabled bool,
	legacyAdminID int32,
	notes string,
	correlationID string,
) error {
	if clubID <= 0 || legacyAdminID <= 0 {
		return ErrForbidden
	}
	if _, ok := validFeatureKeys[key]; !ok {
		return ErrForbidden
	}
	now := store.now()
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubExists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM clubs WHERE id = $1)
		`, clubID).Scan(&clubExists); err != nil {
			return fmt.Errorf("check portal pilot club: %w", err)
		}
		if !clubExists {
			return ErrNotFound
		}
		if key != FeaturePortalAccess && enabled {
			var portalEnabled bool
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE((
					SELECT enabled
					FROM portal_club_features
					WHERE club_id = $1 AND feature_key = 'portal_access'
					FOR UPDATE
				), FALSE)
			`, clubID).Scan(&portalEnabled); err != nil {
				return fmt.Errorf("check portal access feature: %w", err)
			}
			if !portalEnabled {
				return fmt.Errorf("portal access must be enabled before module features")
			}
		}
		if key == FeaturePortalAccess && !enabled {
			if _, err := tx.Exec(ctx, `
				UPDATE portal_club_features
				SET enabled = FALSE, enabled_at = NULL,
				    enabled_by_user_id = NULL,
				    enabled_by_admin_user_id = $2,
				    notes = $3, updated_at = $4
				WHERE club_id = $1
			`, clubID, legacyAdminID, nullableString(notes), now); err != nil {
				return fmt.Errorf("disable portal club features: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_club_features (
				club_id, feature_key, enabled, enabled_at,
				enabled_by_admin_user_id, notes, updated_at
			)
			VALUES (
				$1,
				$2,
				$3::boolean,
				CASE
					WHEN $3::boolean THEN $4::timestamptz
					ELSE NULL::timestamptz
				END,
				$5,
				$6,
				$4::timestamptz
			)
			ON CONFLICT (club_id, feature_key)
			DO UPDATE SET
				enabled = EXCLUDED.enabled,
				enabled_at = EXCLUDED.enabled_at,
				enabled_by_user_id = NULL,
				enabled_by_admin_user_id = EXCLUDED.enabled_by_admin_user_id,
				notes = EXCLUDED.notes,
				updated_at = EXCLUDED.updated_at
		`, clubID, key, enabled, now, legacyAdminID, nullableString(notes)); err != nil {
			return fmt.Errorf("set portal club feature: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.feature.changed",
			TargetType:    "portal_club_feature",
			TargetID:      fmt.Sprintf("%d:%s", clubID, key),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"feature": key,
				"enabled": enabled,
				"notes":   strings.TrimSpace(notes),
			},
			OccurredAt: now,
		})
	})
}

type InvitationSummary struct {
	ID         uuid.UUID
	ClubName   string
	Email      string
	Role       RoleKey
	Status     string
	ApprovedAt time.Time
	ExpiresAt  time.Time
	RedeemedAt *time.Time
}

type ActiveAssignmentSummary struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	DisplayName string
	Email       string
	ClubName    string
	Role        RoleKey
	StartsAt    time.Time
}

func (store *Store) ListActiveAssignments(
	ctx context.Context,
	limit int,
) ([]ActiveAssignmentSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	now := store.now()
	var assignments []ActiveAssignmentSummary
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				a.id,
				a.user_id,
				u.display_name,
				COALESCE(identity_email.email, ''),
				c.name,
				a.role_key,
				a.starts_at
			FROM portal_role_assignments a
			JOIN portal_users u ON u.id = a.user_id
			JOIN clubs c ON c.id = a.club_id
			LEFT JOIN LATERAL (
				SELECT i.email
				FROM portal_identities i
				WHERE i.user_id = a.user_id
				  AND i.email_verified = TRUE
				  AND i.disabled_at IS NULL
				ORDER BY i.last_authenticated_at DESC NULLS LAST, i.created_at
				LIMIT 1
			) identity_email ON TRUE
			WHERE a.status = 'active'
			  AND a.starts_at <= $1
			  AND (a.ends_at IS NULL OR a.ends_at > $1)
			ORDER BY c.name, u.display_name, a.role_key
			LIMIT $2
		`, now, limit)
		if err != nil {
			return fmt.Errorf("list active portal assignments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var assignment ActiveAssignmentSummary
			if err := rows.Scan(
				&assignment.ID,
				&assignment.UserID,
				&assignment.DisplayName,
				&assignment.Email,
				&assignment.ClubName,
				&assignment.Role,
				&assignment.StartsAt,
			); err != nil {
				return fmt.Errorf("scan active portal assignment: %w", err)
			}
			assignments = append(assignments, assignment)
		}
		return rows.Err()
	})
	return assignments, err
}

func (store *Store) RevokeRoleAssignment(
	ctx context.Context,
	assignmentID uuid.UUID,
	legacyAdminID int32,
	reason string,
	correlationID string,
) error {
	reason = strings.TrimSpace(reason)
	if assignmentID == uuid.Nil || legacyAdminID <= 0 || reason == "" {
		return ErrForbidden
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			clubID int32
			userID uuid.UUID
			role   RoleKey
		)
		if err := tx.QueryRow(ctx, `
			UPDATE portal_role_assignments
			SET status = 'revoked',
			    ends_at = GREATEST($2, starts_at + INTERVAL '1 microsecond'),
			    revoked_at = $2,
			    revocation_reason = $3,
			    version = version + 1,
			    updated_at = $2
			WHERE id = $1
			  AND status = 'active'
			  AND starts_at <= $2
			  AND (ends_at IS NULL OR ends_at > $2)
			RETURNING club_id, user_id, role_key
		`, assignmentID, now, reason).Scan(&clubID, &userID, &role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("revoke portal role assignment: %w", err)
		}
		sessionTag, err := tx.Exec(ctx, `
			UPDATE portal_sessions
			SET revoked_at = $2,
			    revoke_reason = 'selected role assignment revoked'
			WHERE selected_role_assignment_id = $1
			  AND revoked_at IS NULL
		`, assignmentID, now)
		if err != nil {
			return fmt.Errorf("revoke sessions for portal role assignment: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_outbox_events (
				aggregate_type, aggregate_id, event_type, payload,
				idempotency_key, occurred_at
			)
			VALUES (
				'portal_role_assignment',
				$1::uuid,
				'portal.role_assignment.revoked',
				jsonb_build_object(
					'club_id', $2::integer,
					'user_id', $3::uuid,
					'role', $4::text,
					'reason', $5::text
				),
				$6::text,
				$7::timestamptz
			)
		`, assignmentID, clubID, userID, role, reason,
			"portal-role-revoked:"+correlationID, now); err != nil {
			return fmt.Errorf("write role revocation outbox event: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.role_assignment.revoked",
			TargetType:    "portal_role_assignment",
			TargetID:      assignmentID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"affected_user_id": userID,
				"role":             role,
				"reason":           reason,
				"sessions_revoked": sessionTag.RowsAffected(),
			},
			OccurredAt: now,
		})
	})
}

func (store *Store) ListRecentInvitations(
	ctx context.Context,
	limit int,
) ([]InvitationSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var invitations []InvitationSummary
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				i.id, c.name, i.email, i.role_key, i.status,
				i.approved_at, i.expires_at, i.redeemed_at
			FROM portal_invitations i
			JOIN clubs c ON c.id = i.club_id
			ORDER BY i.created_at DESC
			LIMIT $1
		`, limit)
		if err != nil {
			return fmt.Errorf("list portal invitations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var invitation InvitationSummary
			if err := rows.Scan(
				&invitation.ID,
				&invitation.ClubName,
				&invitation.Email,
				&invitation.Role,
				&invitation.Status,
				&invitation.ApprovedAt,
				&invitation.ExpiresAt,
				&invitation.RedeemedAt,
			); err != nil {
				return fmt.Errorf("scan portal invitation: %w", err)
			}
			invitations = append(invitations, invitation)
		}
		return rows.Err()
	})
	return invitations, err
}

func (store *Store) RevokeInvitation(
	ctx context.Context,
	invitationID uuid.UUID,
	legacyAdminID int32,
	reason string,
	correlationID string,
) error {
	if invitationID == uuid.Nil || legacyAdminID <= 0 {
		return ErrForbidden
	}
	now := store.now()
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			UPDATE portal_invitations
			SET status = 'revoked', revoked_at = $2
			WHERE id = $1
			  AND status = 'approved'
			  AND redeemed_at IS NULL
			RETURNING club_id
		`, invitationID, now).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("revoke portal invitation: %w", err)
		}
		adminID := legacyAdminID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.invitation.revoked",
			TargetType:    "portal_invitation",
			TargetID:      invitationID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata:      map[string]any{"reason": strings.TrimSpace(reason)},
			OccurredAt:    now,
		})
	})
}
