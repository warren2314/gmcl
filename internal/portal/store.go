package portal

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrUnauthenticated = errors.New("portal authentication required")
	ErrForbidden       = errors.New("portal access denied")
	ErrNotFound        = errors.New("portal record not found")
	ErrStepUpRequired  = errors.New("recent step-up authentication required")
)

type SessionPolicy struct {
	IdleLifetime     time.Duration
	AbsoluteLifetime time.Duration
	TouchInterval    time.Duration
	StepUpLifetime   time.Duration
}

func DefaultSessionPolicy() SessionPolicy {
	return SessionPolicy{
		IdleLifetime:     30 * time.Minute,
		AbsoluteLifetime: 12 * time.Hour,
		TouchInterval:    5 * time.Minute,
		StepUpLifetime:   15 * time.Minute,
	}
}

func LoadSessionPolicyFromEnv() (SessionPolicy, error) {
	policy := DefaultSessionPolicy()
	var err error
	if policy.IdleLifetime, err = durationFromEnv(
		"CLUB_PORTAL_SESSION_IDLE_MINUTES",
		time.Minute,
		policy.IdleLifetime,
	); err != nil {
		return SessionPolicy{}, err
	}
	if policy.AbsoluteLifetime, err = durationFromEnv(
		"CLUB_PORTAL_SESSION_ABSOLUTE_HOURS",
		time.Hour,
		policy.AbsoluteLifetime,
	); err != nil {
		return SessionPolicy{}, err
	}
	if policy.StepUpLifetime, err = durationFromEnv(
		"CLUB_PORTAL_STEP_UP_MINUTES",
		time.Minute,
		policy.StepUpLifetime,
	); err != nil {
		return SessionPolicy{}, err
	}
	if err := policy.Validate(); err != nil {
		return SessionPolicy{}, fmt.Errorf("invalid portal session policy: %w", err)
	}
	return policy, nil
}

func durationFromEnv(name string, unit, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	amount, err := strconv.Atoi(value)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return time.Duration(amount) * unit, nil
}

func (policy SessionPolicy) Validate() error {
	if policy.IdleLifetime < 5*time.Minute {
		return fmt.Errorf("idle lifetime must be at least 5 minutes")
	}
	if policy.AbsoluteLifetime < policy.IdleLifetime {
		return fmt.Errorf("absolute lifetime must not be shorter than idle lifetime")
	}
	if policy.AbsoluteLifetime > 24*time.Hour {
		return fmt.Errorf("absolute lifetime must not exceed 24 hours")
	}
	if policy.TouchInterval <= 0 || policy.TouchInterval >= policy.IdleLifetime {
		return fmt.Errorf("touch interval must be positive and shorter than idle lifetime")
	}
	if policy.StepUpLifetime <= 0 || policy.StepUpLifetime > policy.IdleLifetime {
		return fmt.Errorf("step-up lifetime must be positive and no longer than idle lifetime")
	}
	return nil
}

type ClientDetails struct {
	IPAddress string
	UserAgent string
}

type Principal struct {
	SessionID         uuid.UUID
	UserID            uuid.UUID
	DisplayName       string
	SecurityVersion   int64
	AuthenticatedAt   time.Time
	StepUpAt          *time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	Assignment        *Assignment
}

func (principal Principal) RequiresStepUp(now time.Time, policy SessionPolicy) bool {
	return principal.StepUpAt == nil || now.Sub(*principal.StepUpAt) > policy.StepUpLifetime
}

func (store *Store) StepUpRequired(principal Principal) bool {
	return principal.RequiresStepUp(store.now(), store.policy)
}

type UserSession struct {
	ID                uuid.UUID
	AuthenticatedAt   time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	IPAddress         string
	UserAgent         string
	ClubName          string
	Role              RoleKey
	Current           bool
}

type ActingContext struct {
	Assignment      Assignment
	ClubName        string
	TeamName        string
	SeasonName      string
	CompetitionName string
}

type Store struct {
	pool           *db.Pool
	policy         SessionPolicy
	now            func() time.Time
	useRuntimeRole bool
}

func NewStore(pool *db.Pool, policy SessionPolicy) (*Store, error) {
	if pool == nil || pool.Pool == nil {
		return nil, fmt.Errorf("portal store requires a database pool")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Store{
		pool:   pool,
		policy: policy,
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// InitializeSecurity proves that portal transactions cannot run with a
// PostgreSQL superuser/BYPASSRLS identity. Existing deployments use a
// superuser connection, so migration 0048 supplies a restricted SET ROLE
// target. A non-bypass application role can use RLS directly.
func (store *Store) InitializeSecurity(ctx context.Context) error {
	var (
		isSuperuser   bool
		bypassesRLS   bool
		runtimeExists bool
		runtimeMember bool
	)
	if err := store.pool.QueryRow(ctx, `
		SELECT
			current_role_row.rolsuper,
			current_role_row.rolbypassrls,
			EXISTS (
				SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime'
			),
			CASE
				WHEN EXISTS (
					SELECT 1 FROM pg_roles WHERE rolname = 'gmcl_portal_runtime'
				)
				THEN pg_has_role(current_user, 'gmcl_portal_runtime', 'MEMBER')
				ELSE FALSE
			END
		FROM pg_roles current_role_row
		WHERE current_role_row.rolname = current_user
	`).Scan(&isSuperuser, &bypassesRLS, &runtimeExists, &runtimeMember); err != nil {
		return fmt.Errorf("inspect portal database role: %w", err)
	}
	if isSuperuser || bypassesRLS {
		if !runtimeExists || !runtimeMember {
			return fmt.Errorf("portal database connection bypasses RLS and cannot assume gmcl_portal_runtime")
		}
		store.useRuntimeRole = true
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin portal role verification: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.applyRuntimeRole(ctx, tx); err != nil {
		return err
	}
	var effectiveBypass bool
	if err := tx.QueryRow(ctx, `
		SELECT rolsuper OR rolbypassrls
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&effectiveBypass); err != nil {
		return fmt.Errorf("verify effective portal database role: %w", err)
	}
	if effectiveBypass {
		return fmt.Errorf("effective portal database role still bypasses RLS")
	}
	return nil
}

func (store *Store) applyRuntimeRole(ctx context.Context, tx pgx.Tx) error {
	if !store.useRuntimeRole {
		return nil
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE gmcl_portal_runtime`); err != nil {
		return fmt.Errorf("assume restricted portal database role: %w", err)
	}
	return nil
}

func setSystemContext(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('app.portal_system', 'true', true)`); err != nil {
		return fmt.Errorf("set portal system context: %w", err)
	}
	return nil
}

func setTenantContext(ctx context.Context, tx pgx.Tx, userID uuid.UUID, clubID int32) error {
	if userID == uuid.Nil || clubID <= 0 {
		return ErrForbidden
	}
	if _, err := tx.Exec(ctx, `
		SELECT
			set_config('app.portal_system', 'false', true),
			set_config('app.portal_user_id', $1, true),
			set_config('app.portal_club_id', $2, true)
	`, userID.String(), fmt.Sprint(clubID)); err != nil {
		return fmt.Errorf("set portal tenant context: %w", err)
	}
	return nil
}

func (store *Store) withSystemTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return store.withSystemTxOptions(ctx, pgx.TxOptions{}, fn)
}

func (store *Store) withSystemReadOnlyTx(
	ctx context.Context,
	fn func(pgx.Tx) error,
) error {
	return store.withSystemTxOptions(
		ctx,
		pgx.TxOptions{AccessMode: pgx.ReadOnly},
		fn,
	)
}

func (store *Store) withSystemTxOptions(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(pgx.Tx) error,
) error {
	tx, err := store.pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin portal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.applyRuntimeRole(ctx, tx); err != nil {
		return err
	}
	if err := setSystemContext(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit portal transaction: %w", err)
	}
	return nil
}

// WithTenantTx establishes the PostgreSQL RLS context and executes a
// tenant-scoped repository operation. Handlers must not query private portal
// tables directly.
func (store *Store) WithTenantTx(
	ctx context.Context,
	principal Principal,
	fn func(pgx.Tx, Assignment) error,
) error {
	if principal.Assignment == nil {
		return ErrForbidden
	}
	assignment := *principal.Assignment
	if !Authorize(assignment, PermissionPortalView, assignment.Scope, store.now()) {
		return ErrForbidden
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin tenant transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.applyRuntimeRole(ctx, tx); err != nil {
		return err
	}
	if err := setTenantContext(ctx, tx, principal.UserID, assignment.Scope.ClubID); err != nil {
		return err
	}
	if err := fn(tx, assignment); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tenant transaction: %w", err)
	}
	return nil
}

func (store *Store) CreateSession(
	ctx context.Context,
	userID uuid.UUID,
	details ClientDetails,
	stepUp bool,
) (Principal, string, error) {
	rawToken, tokenHash, err := NewOpaqueToken()
	if err != nil {
		return Principal{}, "", err
	}
	now := store.now()
	idleExpires := now.Add(store.policy.IdleLifetime)
	absoluteExpires := now.Add(store.policy.AbsoluteLifetime)
	var principal Principal

	err = store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT display_name, status, security_version
			FROM portal_users
			WHERE id = $1
		`, userID).Scan(&principal.DisplayName, &status, &principal.SecurityVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnauthenticated
			}
			return fmt.Errorf("load portal user: %w", err)
		}
		if status != "active" {
			return ErrUnauthenticated
		}

		principal.SessionID = uuid.New()
		principal.UserID = userID
		principal.AuthenticatedAt = now
		principal.IdleExpiresAt = idleExpires
		principal.AbsoluteExpiresAt = absoluteExpires
		if stepUp {
			stepUpAt := now
			principal.StepUpAt = &stepUpAt
		}

		ipAddress := nullableIPAddress(details.IPAddress)
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_sessions (
				id, user_id, token_hash, security_version, authenticated_at,
				step_up_at, last_seen_at, idle_expires_at, absolute_expires_at,
				ip_address, user_agent
			)
			VALUES ($1, $2, $3, $4, $5, $6, $5, $7, $8, $9, $10)
		`, principal.SessionID, userID, tokenHash[:], principal.SecurityVersion,
			now, principal.StepUpAt, idleExpires, absoluteExpires, ipAddress,
			sanitizeUserAgent(details.UserAgent)); err != nil {
			return fmt.Errorf("create portal session: %w", err)
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ActorUserID:   &userID,
			ActorKind:     "portal_user",
			Action:        "portal.session.created",
			TargetType:    "portal_session",
			TargetID:      principal.SessionID.String(),
			Outcome:       "success",
			CorrelationID: principal.SessionID.String(),
			Metadata:      map[string]any{"step_up": stepUp},
			IPAddress:     details.IPAddress,
			UserAgent:     details.UserAgent,
			OccurredAt:    now,
		})
	})
	if err != nil {
		return Principal{}, "", err
	}
	return principal, rawToken, nil
}

func (store *Store) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	tokenHash, err := HashOpaqueToken(rawToken)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	now := store.now()
	var principal Principal

	err = store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			membershipID      pgtype.UUID
			assignmentID      pgtype.UUID
			roleKey           pgtype.Text
			clubID            pgtype.Int4
			teamID            pgtype.Int4
			seasonID          pgtype.Int4
			competitionID     pgtype.UUID
			assignmentStatus  pgtype.Text
			assignmentStarts  pgtype.Timestamptz
			assignmentEnds    pgtype.Timestamptz
			assignmentVersion pgtype.Int8
			contextValid      bool
			lastSeen          time.Time
		)

		err := tx.QueryRow(ctx, `
			SELECT
				s.id,
				s.user_id,
				u.display_name,
				u.security_version,
				s.authenticated_at,
				s.step_up_at,
				s.idle_expires_at,
				s.absolute_expires_at,
				s.last_seen_at,
				s.selected_membership_id,
				s.selected_role_assignment_id,
				a.role_key,
				a.club_id,
				a.team_id,
				a.season_id,
				a.competition_id,
				a.status,
				a.starts_at,
				a.ends_at,
				a.version,
				(
					s.selected_membership_id IS NULL
					OR (
						m.id IS NOT NULL
						AND m.user_id = s.user_id
						AND m.status = 'active'
						AND m.starts_at <= $2
						AND (m.ends_at IS NULL OR m.ends_at > $2)
					)
				)
				AND (
					s.selected_role_assignment_id IS NULL
					OR (
						a.id IS NOT NULL
						AND a.user_id = s.user_id
						AND a.membership_id = s.selected_membership_id
						AND a.status = 'active'
						AND a.starts_at <= $2
						AND (a.ends_at IS NULL OR a.ends_at > $2)
					)
				) AS context_valid
			FROM portal_sessions s
			JOIN portal_users u ON u.id = s.user_id
			LEFT JOIN portal_club_memberships m ON m.id = s.selected_membership_id
			LEFT JOIN portal_role_assignments a ON a.id = s.selected_role_assignment_id
			WHERE s.token_hash = $1
			  AND s.revoked_at IS NULL
			  AND s.idle_expires_at > $2
			  AND s.absolute_expires_at > $2
			  AND u.status = 'active'
			  AND u.security_version = s.security_version
		`, tokenHash[:], now).Scan(
			&principal.SessionID,
			&principal.UserID,
			&principal.DisplayName,
			&principal.SecurityVersion,
			&principal.AuthenticatedAt,
			&principal.StepUpAt,
			&principal.IdleExpiresAt,
			&principal.AbsoluteExpiresAt,
			&lastSeen,
			&membershipID,
			&assignmentID,
			&roleKey,
			&clubID,
			&teamID,
			&seasonID,
			&competitionID,
			&assignmentStatus,
			&assignmentStarts,
			&assignmentEnds,
			&assignmentVersion,
			&contextValid,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnauthenticated
			}
			return fmt.Errorf("authenticate portal session: %w", err)
		}
		if !contextValid {
			_, _ = tx.Exec(ctx, `
				UPDATE portal_sessions
				SET revoked_at = $2, revoke_reason = 'acting appointment inactive'
				WHERE id = $1 AND revoked_at IS NULL
			`, principal.SessionID, now)
			return ErrUnauthenticated
		}

		if assignmentID.Valid {
			assignment := Assignment{
				ID:           uuid.UUID(assignmentID.Bytes),
				MembershipID: uuid.UUID(membershipID.Bytes),
				UserID:       principal.UserID,
				Role:         RoleKey(roleKey.String),
				Scope: Scope{
					ClubID:        clubID.Int32,
					TeamID:        int32Ptr(teamID),
					SeasonID:      int32Ptr(seasonID),
					CompetitionID: uuidPtr(competitionID),
				},
				Status:   assignmentStatus.String,
				StartsAt: assignmentStarts.Time,
				EndsAt:   timePtr(assignmentEnds),
				Version:  assignmentVersion.Int64,
			}
			principal.Assignment = &assignment
		}

		if now.Sub(lastSeen) >= store.policy.TouchInterval {
			newIdleExpiry := now.Add(store.policy.IdleLifetime)
			if newIdleExpiry.After(principal.AbsoluteExpiresAt) {
				newIdleExpiry = principal.AbsoluteExpiresAt
			}
			if _, err := tx.Exec(ctx, `
				UPDATE portal_sessions
				SET last_seen_at = $2, idle_expires_at = $3
				WHERE id = $1 AND revoked_at IS NULL
			`, principal.SessionID, now, newIdleExpiry); err != nil {
				return fmt.Errorf("touch portal session: %w", err)
			}
			principal.IdleExpiresAt = newIdleExpiry
		}
		return nil
	})
	if err != nil {
		return Principal{}, err
	}
	return principal, nil
}

func (store *Store) ListActingContexts(ctx context.Context, principal Principal) ([]ActingContext, error) {
	var contexts []ActingContext
	now := store.now()
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				a.id,
				a.membership_id,
				a.role_key,
				a.club_id,
				a.team_id,
				a.season_id,
				a.competition_id,
				a.status,
				a.starts_at,
				a.ends_at,
				a.version,
				c.name,
				COALESCE(t.name, ''),
				COALESCE(s.name, ''),
				COALESCE(pc.name, '')
			FROM portal_role_assignments a
			JOIN portal_club_memberships m
			  ON m.id = a.membership_id
			 AND m.user_id = a.user_id
			 AND m.club_id = a.club_id
			JOIN clubs c ON c.id = a.club_id
			LEFT JOIN teams t ON t.id = a.team_id AND t.club_id = a.club_id
			LEFT JOIN seasons s ON s.id = a.season_id
			LEFT JOIN portal_competitions pc ON pc.id = a.competition_id
			JOIN portal_club_features pf
			  ON pf.club_id = a.club_id
			 AND pf.feature_key = 'portal_access'
			 AND pf.enabled = TRUE
			WHERE a.user_id = $1
			  AND a.status = 'active'
			  AND a.starts_at <= $2
			  AND (a.ends_at IS NULL OR a.ends_at > $2)
			  AND m.status = 'active'
			  AND m.starts_at <= $2
			  AND (m.ends_at IS NULL OR m.ends_at > $2)
			ORDER BY c.name, a.role_key, t.name NULLS FIRST
		`, principal.UserID, now)
		if err != nil {
			return fmt.Errorf("list portal acting contexts: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				context       ActingContext
				teamID        pgtype.Int4
				seasonID      pgtype.Int4
				competitionID pgtype.UUID
				endsAt        pgtype.Timestamptz
			)
			context.Assignment.UserID = principal.UserID
			if err := rows.Scan(
				&context.Assignment.ID,
				&context.Assignment.MembershipID,
				&context.Assignment.Role,
				&context.Assignment.Scope.ClubID,
				&teamID,
				&seasonID,
				&competitionID,
				&context.Assignment.Status,
				&context.Assignment.StartsAt,
				&endsAt,
				&context.Assignment.Version,
				&context.ClubName,
				&context.TeamName,
				&context.SeasonName,
				&context.CompetitionName,
			); err != nil {
				return fmt.Errorf("scan portal acting context: %w", err)
			}
			context.Assignment.Scope.TeamID = int32Ptr(teamID)
			context.Assignment.Scope.SeasonID = int32Ptr(seasonID)
			context.Assignment.Scope.CompetitionID = uuidPtr(competitionID)
			context.Assignment.EndsAt = timePtr(endsAt)
			contexts = append(contexts, context)
		}
		return rows.Err()
	})
	return contexts, err
}

// SelectActingContext rotates the bearer token so a context or privilege
// change cannot leave the previous token usable.
func (store *Store) SelectActingContext(
	ctx context.Context,
	principal Principal,
	assignmentID uuid.UUID,
	details ClientDetails,
) (Principal, string, error) {
	rawToken, tokenHash, err := NewOpaqueToken()
	if err != nil {
		return Principal{}, "", err
	}
	now := store.now()
	var selected Principal

	err = store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			assignment    Assignment
			teamID        pgtype.Int4
			seasonID      pgtype.Int4
			competitionID pgtype.UUID
			endsAt        pgtype.Timestamptz
		)
		assignment.UserID = principal.UserID
		if err := tx.QueryRow(ctx, `
			SELECT
				a.id, a.membership_id, a.role_key, a.club_id, a.team_id,
				a.season_id, a.competition_id, a.status, a.starts_at,
				a.ends_at, a.version
			FROM portal_role_assignments a
			JOIN portal_club_memberships m
			  ON m.id = a.membership_id
			 AND m.user_id = a.user_id
			 AND m.club_id = a.club_id
			WHERE a.id = $1
			  AND a.user_id = $2
			  AND a.status = 'active'
			  AND a.starts_at <= $3
			  AND (a.ends_at IS NULL OR a.ends_at > $3)
			  AND m.status = 'active'
			  AND m.starts_at <= $3
			  AND (m.ends_at IS NULL OR m.ends_at > $3)
		`, assignmentID, principal.UserID, now).Scan(
			&assignment.ID,
			&assignment.MembershipID,
			&assignment.Role,
			&assignment.Scope.ClubID,
			&teamID,
			&seasonID,
			&competitionID,
			&assignment.Status,
			&assignment.StartsAt,
			&endsAt,
			&assignment.Version,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("select portal assignment: %w", err)
		}
		assignment.Scope.TeamID = int32Ptr(teamID)
		assignment.Scope.SeasonID = int32Ptr(seasonID)
		assignment.Scope.CompetitionID = uuidPtr(competitionID)
		assignment.EndsAt = timePtr(endsAt)
		if !Authorize(assignment, PermissionPortalView, assignment.Scope, now) {
			return ErrForbidden
		}

		tag, err := tx.Exec(ctx, `
			UPDATE portal_sessions
			SET revoked_at = $3, revoke_reason = 'acting context changed'
			WHERE id = $1
			  AND user_id = $2
			  AND revoked_at IS NULL
			  AND idle_expires_at > $3
			  AND absolute_expires_at > $3
		`, principal.SessionID, principal.UserID, now)
		if err != nil {
			return fmt.Errorf("rotate previous portal session: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrUnauthenticated
		}

		idleExpires := now.Add(store.policy.IdleLifetime)
		if idleExpires.After(principal.AbsoluteExpiresAt) {
			idleExpires = principal.AbsoluteExpiresAt
		}
		selected = principal
		selected.SessionID = uuid.New()
		selected.Assignment = &assignment
		selected.IdleExpiresAt = idleExpires
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_sessions (
				id, user_id, token_hash, selected_membership_id,
				selected_role_assignment_id, security_version, authenticated_at,
				step_up_at, last_seen_at, idle_expires_at, absolute_expires_at,
				ip_address, user_agent
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		`, selected.SessionID, selected.UserID, tokenHash[:],
			assignment.MembershipID, assignment.ID, selected.SecurityVersion,
			selected.AuthenticatedAt, selected.StepUpAt, now, idleExpires,
			selected.AbsoluteExpiresAt, nullableIPAddress(details.IPAddress),
			sanitizeUserAgent(details.UserAgent)); err != nil {
			return fmt.Errorf("create rotated portal session: %w", err)
		}

		clubID := assignment.Scope.ClubID
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorUserID:   &selected.UserID,
			ActorKind:     "portal_user",
			ActingRoleKey: string(assignment.Role),
			Action:        "portal.context.selected",
			TargetType:    "portal_role_assignment",
			TargetID:      assignment.ID.String(),
			Outcome:       "success",
			CorrelationID: selected.SessionID.String(),
			IPAddress:     details.IPAddress,
			UserAgent:     details.UserAgent,
			OccurredAt:    now,
		})
	})
	if err != nil {
		return Principal{}, "", err
	}
	return selected, rawToken, nil
}

func (store *Store) RevokeSession(
	ctx context.Context,
	principal Principal,
	reason string,
) error {
	return store.RevokeUserSession(ctx, principal, principal.SessionID, reason)
}

func (store *Store) ListUserSessions(
	ctx context.Context,
	principal Principal,
) ([]UserSession, error) {
	if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil {
		return nil, ErrUnauthenticated
	}
	now := store.now()
	var sessions []UserSession
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				s.id,
				s.authenticated_at,
				s.last_seen_at,
				s.idle_expires_at,
				s.absolute_expires_at,
				COALESCE(s.ip_address::text, ''),
				COALESCE(s.user_agent, ''),
				COALESCE(c.name, ''),
				COALESCE(a.role_key, '')
			FROM portal_sessions s
			LEFT JOIN portal_club_memberships m
			  ON m.id = s.selected_membership_id
			 AND m.user_id = s.user_id
			LEFT JOIN clubs c ON c.id = m.club_id
			LEFT JOIN portal_role_assignments a
			  ON a.id = s.selected_role_assignment_id
			 AND a.user_id = s.user_id
			WHERE s.user_id = $1
			  AND s.revoked_at IS NULL
			  AND s.idle_expires_at > $2
			  AND s.absolute_expires_at > $2
			ORDER BY s.last_seen_at DESC, s.created_at DESC
		`, principal.UserID, now)
		if err != nil {
			return fmt.Errorf("list portal sessions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var role string
			var session UserSession
			if err := rows.Scan(
				&session.ID,
				&session.AuthenticatedAt,
				&session.LastSeenAt,
				&session.IdleExpiresAt,
				&session.AbsoluteExpiresAt,
				&session.IPAddress,
				&session.UserAgent,
				&session.ClubName,
				&role,
			); err != nil {
				return fmt.Errorf("scan portal session: %w", err)
			}
			session.Role = RoleKey(role)
			session.Current = session.ID == principal.SessionID
			sessions = append(sessions, session)
		}
		return rows.Err()
	})
	return sessions, err
}

func (store *Store) RevokeUserSession(
	ctx context.Context,
	principal Principal,
	sessionID uuid.UUID,
	reason string,
) error {
	if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil {
		return ErrUnauthenticated
	}
	if sessionID == uuid.Nil {
		return ErrNotFound
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "revoked by account holder"
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE portal_sessions
			SET revoked_at = $3, revoke_reason = $4
			WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		`, sessionID, principal.UserID, now, reason)
		if err != nil {
			return fmt.Errorf("revoke portal session: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		event := AuditEvent{
			ActorUserID:   &principal.UserID,
			ActorKind:     "portal_user",
			Action:        "portal.session.revoked",
			TargetType:    "portal_session",
			TargetID:      sessionID.String(),
			Outcome:       "success",
			CorrelationID: uuid.NewString(),
			Metadata: map[string]any{
				"current_session": sessionID == principal.SessionID,
				"reason":          reason,
			},
			OccurredAt: now,
		}
		if principal.Assignment != nil {
			clubID := principal.Assignment.Scope.ClubID
			event.ClubID = &clubID
			event.ActingRoleKey = string(principal.Assignment.Role)
		}
		return store.appendAuditTx(ctx, tx, event)
	})
}

func (store *Store) RevokeAllUserSessions(
	ctx context.Context,
	principal Principal,
	reason string,
) error {
	if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil {
		return ErrUnauthenticated
	}
	if principal.RequiresStepUp(store.now(), store.policy) {
		return ErrStepUpRequired
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "all sessions revoked by account holder"
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE portal_users
			SET security_version = security_version + 1, updated_at = $2
			WHERE id = $1
		`, principal.UserID, now)
		if err != nil {
			return fmt.Errorf("increment portal security version: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_sessions
			SET revoked_at = $2, revoke_reason = $3
			WHERE user_id = $1 AND revoked_at IS NULL
		`, principal.UserID, now, reason); err != nil {
			return fmt.Errorf("revoke all portal sessions: %w", err)
		}
		event := AuditEvent{
			ActorUserID:   &principal.UserID,
			ActorKind:     "portal_user",
			Action:        "portal.session.revoked_all",
			TargetType:    "portal_user",
			TargetID:      principal.UserID.String(),
			Outcome:       "success",
			CorrelationID: uuid.NewString(),
			Metadata:      map[string]any{"reason": reason},
			OccurredAt:    now,
		}
		if principal.Assignment != nil {
			clubID := principal.Assignment.Scope.ClubID
			event.ClubID = &clubID
			event.ActingRoleKey = string(principal.Assignment.Role)
		}
		return store.appendAuditTx(ctx, tx, event)
	})
}

type AuditEvent struct {
	ClubID        *int32
	ActorUserID   *uuid.UUID
	ActorKind     string
	LegacyAdminID *int32
	ActingRoleKey string
	Action        string
	TargetType    string
	TargetID      string
	Outcome       string
	CorrelationID string
	Metadata      map[string]any
	IPAddress     string
	UserAgent     string
	OccurredAt    time.Time
}

func (store *Store) appendAuditTx(ctx context.Context, tx pgx.Tx, event AuditEvent) error {
	if event.ActorKind == "" || event.Action == "" || event.TargetType == "" ||
		event.CorrelationID == "" {
		return fmt.Errorf("incomplete portal audit event")
	}
	if event.Outcome == "" {
		event.Outcome = "success"
	}
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = store.now()
	}
	event.OccurredAt = normalizeAuditTime(event.OccurredAt)
	event.IPAddress = normalizeAuditIPAddress(event.IPAddress)
	event.UserAgent = normalizeAuditUserAgent(event.UserAgent)
	eventID := uuid.New()
	lockClubID := int32(0)
	if event.ClubID != nil {
		lockClubID = *event.ClubID
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1804289383, $1)`, lockClubID); err != nil {
		return fmt.Errorf("lock portal audit chain: %w", err)
	}

	var (
		lastPosition int64
		lastHash     []byte
	)
	err := tx.QueryRow(ctx, `
		SELECT chain_position, event_hash
		FROM portal_audit_events
		WHERE club_id IS NOT DISTINCT FROM $1
		ORDER BY chain_position DESC
		LIMIT 1
	`, event.ClubID).Scan(&lastPosition, &lastHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load portal audit chain head: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		lastPosition = 0
		lastHash = nil
	}
	position := lastPosition + 1

	canonical, err := canonicalAuditBytes(
		eventID,
		event,
		position,
		lastHash,
		currentAuditHashVersion,
	)
	if err != nil {
		return fmt.Errorf("encode portal audit event: %w", err)
	}
	eventHash := sha256.Sum256(canonical)

	if _, err := tx.Exec(ctx, `
		INSERT INTO portal_audit_events (
			id, club_id, actor_user_id, actor_kind, legacy_admin_user_id,
			acting_role_key, action, target_type, target_id, outcome,
			correlation_id, metadata, chain_position, previous_hash, event_hash,
			hash_version, ip_address, user_agent, occurred_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19
		)
	`, eventID, event.ClubID, event.ActorUserID, event.ActorKind,
		event.LegacyAdminID, nullableString(event.ActingRoleKey), event.Action,
		event.TargetType, event.TargetID, event.Outcome, event.CorrelationID,
		event.Metadata, position, nullableBytes(lastHash), eventHash[:],
		currentAuditHashVersion, nullableIPAddress(event.IPAddress),
		sanitizeUserAgent(event.UserAgent), event.OccurredAt); err != nil {
		return fmt.Errorf("append portal audit event: %w", err)
	}
	return nil
}

func int32Ptr(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	result := value.Int32
	return &result
}

func uuidPtr(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	result := uuid.UUID(value.Bytes)
	return &result
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func uuidString(value *uuid.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullableUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}

func nullableIPAddress(value string) any {
	value = normalizeAuditIPAddress(value)
	if value == "" {
		return nil
	}
	return value
}

func sanitizeUserAgent(value string) any {
	value = normalizeAuditUserAgent(value)
	if value == "" {
		return nil
	}
	return value
}

func normalizeAuditIPAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed := net.ParseIP(value)
	if parsed == nil {
		return ""
	}
	return parsed.String()
}

func normalizeAuditUserAgent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	const maxUserAgentBytes = 512
	if len(value) > maxUserAgentBytes {
		value = value[:maxUserAgentBytes]
	}
	return value
}
