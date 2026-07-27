package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type MessageCategory string

const (
	MessageCategoryGeneral        MessageCategory = "general"
	MessageCategoryCompliance     MessageCategory = "compliance_sanctions"
	MessageCategoryFixtures       MessageCategory = "fixtures"
	MessageCategoryRegistration   MessageCategory = "registration"
	MessageCategoryStarred        MessageCategory = "starred_players"
	MessageCategoryJunior         MessageCategory = "junior_administration"
	MessageCategoryContact        MessageCategory = "contact_correction"
	MessageCategoryPlayerIdentity MessageCategory = "player_identity"
)

var validMessageCategories = map[MessageCategory]struct{}{
	MessageCategoryGeneral:        {},
	MessageCategoryCompliance:     {},
	MessageCategoryFixtures:       {},
	MessageCategoryRegistration:   {},
	MessageCategoryStarred:        {},
	MessageCategoryJunior:         {},
	MessageCategoryContact:        {},
	MessageCategoryPlayerIdentity: {},
}

type MessageCaseSummary struct {
	ID             uuid.UUID
	ClubID         int32
	ClubName       string
	Category       MessageCategory
	Subject        string
	Status         string
	Priority       string
	DeadlineAt     *time.Time
	AcknowledgedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MessageCount   int64
	LastMessageAt  time.Time
}

type ClubVisibleMessage struct {
	ID          uuid.UUID
	CaseID      uuid.UUID
	AuthorKind  string
	AuthorLabel string
	AuthorRole  StaffRoleKey
	Body        string
	EmailStatus string
	CreatedAt   time.Time
}

type MessageCaseDetail struct {
	MessageCaseSummary
	CreatedByUserID *uuid.UUID
	CreatedByEmail  string
	Watching        bool
	Messages        []ClubVisibleMessage
}

type InternalNote struct {
	ID          uuid.UUID
	AuthorLabel string
	Body        string
	CreatedAt   time.Time
}

type AdminMessageCaseDetail struct {
	MessageCaseDetail
	AssignedAdminID       *int32
	CampaignCompetitionID *uuid.UUID
	InternalNotes         []InternalNote
}

type CreateMessageCaseRequest struct {
	Category MessageCategory
	Subject  string
	Body     string
	Priority string
}

type ModuleRequestType string

const (
	ModuleRequestCorrection   ModuleRequestType = "record_correction"
	ModuleRequestStarred      ModuleRequestType = "starred_player_review"
	ModuleRequestJunior       ModuleRequestType = "junior_administration"
	ModuleRequestIdentity     ModuleRequestType = "player_identity_reconciliation"
	ModuleRequestRegistration ModuleRequestType = "registration_handoff"
)

type ModuleRequest struct {
	ID                  uuid.UUID
	CaseID              *uuid.UUID
	Type                ModuleRequestType
	Title               string
	ExternalReference   string
	Payload             map[string]string
	Status              string
	RuleRelease         string
	HumanReviewRequired bool
	ReviewNote          string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type CreateModuleRequest struct {
	Type              ModuleRequestType
	Title             string
	ExternalReference string
	Payload           map[string]string
	Message           string
}

type ClubContact struct {
	ID                uuid.UUID
	RoleKey           string
	DisplayName       string
	Email             string
	Phone             string
	Status            string
	EvidenceReference string
	EffectiveFrom     time.Time
	EffectiveUntil    *time.Time
	CreatedAt         time.Time
}

type CreateClubContactRequest struct {
	RoleKey           string
	DisplayName       string
	Email             string
	Phone             string
	EvidenceReference string
}

type FixtureConstraint struct {
	ID             uuid.UUID
	TeamID         *int32
	TeamName       string
	SeasonID       *int32
	SeasonName     string
	ConstraintType string
	Description    string
	StartsOn       *time.Time
	EndsOn         *time.Time
	HardConstraint bool
	Status         string
	ReviewNote     string
	CreatedAt      time.Time
}

type CreateFixtureConstraintRequest struct {
	TeamID         *int32
	SeasonID       *int32
	ConstraintType string
	Description    string
	StartsOn       *time.Time
	EndsOn         *time.Time
	HardConstraint bool
}

func (store *Store) withAuthorizedPortalWrite(
	ctx context.Context,
	principal Principal,
	permission Permission,
	feature FeatureKey,
	fn func(pgx.Tx, Assignment) error,
) error {
	if principal.Assignment == nil {
		return ErrForbidden
	}
	assignment := *principal.Assignment
	if !Authorize(assignment, permission, assignment.Scope, store.now()) {
		return ErrForbidden
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var authorized bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM portal_role_assignments role
				JOIN portal_club_memberships membership
				  ON membership.id = role.membership_id
				 AND membership.user_id = role.user_id
				 AND membership.club_id = role.club_id
				WHERE role.id = $1
				  AND role.user_id = $2
				  AND role.club_id = $3
				  AND role.status = 'active'
				  AND role.starts_at <= $4
				  AND (role.ends_at IS NULL OR role.ends_at > $4)
				  AND membership.status = 'active'
				  AND membership.starts_at <= $4
				  AND (membership.ends_at IS NULL OR membership.ends_at > $4)
				  AND EXISTS (
				      SELECT 1 FROM portal_club_features access
				      WHERE access.club_id = role.club_id
				        AND access.feature_key = 'portal_access'
				        AND access.enabled
				  )
				  AND EXISTS (
				      SELECT 1 FROM portal_club_features module
				      WHERE module.club_id = role.club_id
				        AND module.feature_key = $5
				        AND module.enabled
				  )
			)
		`, assignment.ID, principal.UserID, assignment.Scope.ClubID, now, feature).
			Scan(&authorized); err != nil {
			return fmt.Errorf("revalidate portal operation: %w", err)
		}
		if !authorized {
			return ErrForbidden
		}
		return fn(tx, assignment)
	})
}

func (store *Store) ListMessageCases(
	ctx context.Context,
	principal Principal,
) ([]MessageCaseSummary, error) {
	if principal.Assignment == nil ||
		!Authorize(*principal.Assignment, PermissionMessagesView,
			principal.Assignment.Scope, store.now()) {
		return nil, ErrForbidden
	}
	enabled, err := store.FeatureEnabled(ctx, principal, FeatureSecureMessaging)
	if err != nil || !enabled {
		if err != nil {
			return nil, err
		}
		return nil, ErrForbidden
	}
	var cases []MessageCaseSummary
	err = store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		rows, err := tx.Query(ctx, `
			SELECT
				c.id,
				c.club_id,
				club.name,
				c.category,
				c.subject,
				c.status,
				c.priority,
				c.deadline_at,
				c.acknowledged_at,
				c.created_at,
				c.updated_at,
				COUNT(message.id),
				COALESCE(MAX(message.created_at), c.created_at)
			FROM portal_message_cases c
			JOIN clubs club ON club.id = c.club_id
			LEFT JOIN portal_club_visible_messages message ON message.case_id = c.id
			WHERE c.club_id = $1
			GROUP BY c.id, club.name
			ORDER BY
				CASE c.priority WHEN 'urgent' THEN 0 ELSE 1 END,
				CASE WHEN c.deadline_at IS NOT NULL AND c.deadline_at < $2 THEN 0 ELSE 1 END,
				c.updated_at DESC
		`, assignment.Scope.ClubID, store.now())
		if err != nil {
			return fmt.Errorf("list portal message cases: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item MessageCaseSummary
			if err := rows.Scan(
				&item.ID,
				&item.ClubID,
				&item.ClubName,
				&item.Category,
				&item.Subject,
				&item.Status,
				&item.Priority,
				&item.DeadlineAt,
				&item.AcknowledgedAt,
				&item.CreatedAt,
				&item.UpdatedAt,
				&item.MessageCount,
				&item.LastMessageAt,
			); err != nil {
				return fmt.Errorf("scan portal message case: %w", err)
			}
			cases = append(cases, item)
		}
		return rows.Err()
	})
	return cases, err
}

func (store *Store) LoadMessageCase(
	ctx context.Context,
	principal Principal,
	caseID uuid.UUID,
) (MessageCaseDetail, error) {
	if caseID == uuid.Nil || principal.Assignment == nil ||
		!Authorize(*principal.Assignment, PermissionMessagesView,
			principal.Assignment.Scope, store.now()) {
		return MessageCaseDetail{}, ErrForbidden
	}
	enabled, err := store.FeatureEnabled(ctx, principal, FeatureSecureMessaging)
	if err != nil || !enabled {
		if err != nil {
			return MessageCaseDetail{}, err
		}
		return MessageCaseDetail{}, ErrForbidden
	}
	var detail MessageCaseDetail
	err = store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		if err := tx.QueryRow(ctx, `
			SELECT
				c.id,
				c.club_id,
				club.name,
				c.category,
				c.subject,
				c.status,
				c.priority,
				c.deadline_at,
				c.acknowledged_at,
				c.created_at,
				c.updated_at,
				c.created_by_user_id,
				COALESCE((
					SELECT identity.verified_email
					FROM portal_identities identity
					WHERE identity.user_id = c.created_by_user_id
					  AND identity.email_verified
					ORDER BY identity.last_authenticated_at DESC NULLS LAST
					LIMIT 1
				), ''),
				EXISTS (
					SELECT 1 FROM portal_case_watchers watcher
					WHERE watcher.case_id = c.id
					  AND watcher.user_id = $3
				)
			FROM portal_message_cases c
			JOIN clubs club ON club.id = c.club_id
			WHERE c.id = $1 AND c.club_id = $2
		`, caseID, assignment.Scope.ClubID, principal.UserID).Scan(
			&detail.ID,
			&detail.ClubID,
			&detail.ClubName,
			&detail.Category,
			&detail.Subject,
			&detail.Status,
			&detail.Priority,
			&detail.DeadlineAt,
			&detail.AcknowledgedAt,
			&detail.CreatedAt,
			&detail.UpdatedAt,
			&detail.CreatedByUserID,
			&detail.CreatedByEmail,
			&detail.Watching,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load portal message case: %w", err)
		}
		rows, err := tx.Query(ctx, `
			SELECT
				message.id,
				message.case_id,
				message.author_kind,
				CASE message.author_kind
					WHEN 'club_user' THEN COALESCE(portal_user.display_name, 'Club user')
					WHEN 'gmcl_admin' THEN COALESCE(admin_user.username, 'GMCL')
					ELSE 'System'
				END,
				COALESCE(message.author_staff_role_key, ''),
				message.body,
				message.email_status,
				message.created_at
			FROM portal_club_visible_messages message
			LEFT JOIN portal_users portal_user ON portal_user.id = message.author_user_id
			LEFT JOIN admin_users admin_user ON admin_user.id = message.author_admin_user_id
			WHERE message.case_id = $1 AND message.club_id = $2
			ORDER BY message.created_at, message.id
		`, caseID, assignment.Scope.ClubID)
		if err != nil {
			return fmt.Errorf("load portal visible messages: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var message ClubVisibleMessage
			if err := rows.Scan(
				&message.ID,
				&message.CaseID,
				&message.AuthorKind,
				&message.AuthorLabel,
				&message.AuthorRole,
				&message.Body,
				&message.EmailStatus,
				&message.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan portal visible message: %w", err)
			}
			detail.Messages = append(detail.Messages, message)
		}
		detail.MessageCount = int64(len(detail.Messages))
		if len(detail.Messages) > 0 {
			detail.LastMessageAt = detail.Messages[len(detail.Messages)-1].CreatedAt
		} else {
			detail.LastMessageAt = detail.CreatedAt
		}
		return rows.Err()
	})
	return detail, err
}

func (store *Store) CreateMessageCase(
	ctx context.Context,
	principal Principal,
	request CreateMessageCaseRequest,
	correlationID string,
) (uuid.UUID, uuid.UUID, error) {
	request.Subject = strings.TrimSpace(request.Subject)
	request.Body = strings.TrimSpace(request.Body)
	request.Priority = strings.ToLower(strings.TrimSpace(request.Priority))
	if _, ok := validMessageCategories[request.Category]; !ok ||
		len(request.Subject) < 3 || len(request.Subject) > 200 ||
		request.Body == "" || len(request.Body) > 10000 {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid portal message")
	}
	if request.Priority == "" {
		request.Priority = "normal"
	}
	if request.Priority != "normal" && request.Priority != "urgent" {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid portal message priority")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	caseID := uuid.New()
	messageID := uuid.New()
	now := store.now()
	err := store.withAuthorizedPortalWrite(
		ctx,
		principal,
		PermissionMessagesReply,
		FeatureSecureMessaging,
		func(tx pgx.Tx, assignment Assignment) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_message_cases (
					id, club_id, category, subject, status, priority,
					created_by_user_id, created_at, updated_at
				)
				VALUES ($1, $2, $3, $4, 'awaiting_gmcl', $5, $6, $7, $7)
			`, caseID, assignment.Scope.ClubID, request.Category, request.Subject,
				request.Priority, principal.UserID, now); err != nil {
				return fmt.Errorf("create portal message case: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_club_visible_messages (
					id, case_id, club_id, author_kind, author_user_id,
					body, email_status, created_at
				)
				VALUES ($1, $2, $3, 'club_user', $4, $5, 'pending', $6)
			`, messageID, caseID, assignment.Scope.ClubID,
				principal.UserID, request.Body, now); err != nil {
				return fmt.Errorf("create portal visible message: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_case_watchers (case_id, club_id, user_id, created_at)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING
			`, caseID, assignment.Scope.ClubID, principal.UserID, now); err != nil {
				return fmt.Errorf("watch created portal case: %w", err)
			}
			clubID := assignment.Scope.ClubID
			return store.appendAuditTx(ctx, tx, AuditEvent{
				ClubID:        &clubID,
				ActorUserID:   &principal.UserID,
				ActorKind:     "portal_user",
				ActingRoleKey: string(assignment.Role),
				Action:        "portal.case.created",
				TargetType:    "portal_message_case",
				TargetID:      caseID.String(),
				Outcome:       "success",
				CorrelationID: correlationID,
				Metadata: map[string]any{
					"category": request.Category,
					"priority": request.Priority,
				},
				OccurredAt: now,
			})
		},
	)
	return caseID, messageID, err
}

func (store *Store) ReplyMessageCase(
	ctx context.Context,
	principal Principal,
	caseID uuid.UUID,
	body string,
	correlationID string,
) (uuid.UUID, error) {
	body = strings.TrimSpace(body)
	if caseID == uuid.Nil || body == "" || len(body) > 10000 {
		return uuid.Nil, fmt.Errorf("invalid portal reply")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	messageID := uuid.New()
	now := store.now()
	err := store.withAuthorizedPortalWrite(
		ctx,
		principal,
		PermissionMessagesReply,
		FeatureSecureMessaging,
		func(tx pgx.Tx, assignment Assignment) error {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM portal_message_cases
					WHERE id = $1 AND club_id = $2 AND status <> 'closed'
				)
			`, caseID, assignment.Scope.ClubID).Scan(&exists); err != nil {
				return fmt.Errorf("check portal reply case: %w", err)
			}
			if !exists {
				return ErrNotFound
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_club_visible_messages (
					id, case_id, club_id, author_kind, author_user_id,
					body, email_status, created_at
				)
				VALUES ($1, $2, $3, 'club_user', $4, $5, 'pending', $6)
			`, messageID, caseID, assignment.Scope.ClubID,
				principal.UserID, body, now); err != nil {
				return fmt.Errorf("reply to portal case: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE portal_message_cases
				SET status = 'awaiting_gmcl',
				    version = version + 1,
				    updated_at = $3
				WHERE id = $1 AND club_id = $2
			`, caseID, assignment.Scope.ClubID, now); err != nil {
				return fmt.Errorf("update replied portal case: %w", err)
			}
			clubID := assignment.Scope.ClubID
			return store.appendAuditTx(ctx, tx, AuditEvent{
				ClubID:        &clubID,
				ActorUserID:   &principal.UserID,
				ActorKind:     "portal_user",
				ActingRoleKey: string(assignment.Role),
				Action:        "portal.case.replied",
				TargetType:    "portal_message_case",
				TargetID:      caseID.String(),
				Outcome:       "success",
				CorrelationID: correlationID,
				OccurredAt:    now,
			})
		},
	)
	return messageID, err
}

func (store *Store) AcknowledgeMessageCase(
	ctx context.Context,
	principal Principal,
	caseID uuid.UUID,
	correlationID string,
) error {
	if caseID == uuid.Nil {
		return ErrNotFound
	}
	now := store.now()
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	return store.withAuthorizedPortalWrite(
		ctx,
		principal,
		PermissionMessagesView,
		FeatureSecureMessaging,
		func(tx pgx.Tx, assignment Assignment) error {
			tag, err := tx.Exec(ctx, `
				UPDATE portal_message_cases
				SET acknowledged_at = COALESCE(acknowledged_at, $3),
				    acknowledged_by_user_id = COALESCE(acknowledged_by_user_id, $4),
				    version = CASE WHEN acknowledged_at IS NULL THEN version + 1 ELSE version END,
				    updated_at = CASE WHEN acknowledged_at IS NULL THEN $3 ELSE updated_at END
				WHERE id = $1 AND club_id = $2
			`, caseID, assignment.Scope.ClubID, now, principal.UserID)
			if err != nil {
				return fmt.Errorf("acknowledge portal case: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return ErrNotFound
			}
			clubID := assignment.Scope.ClubID
			return store.appendAuditTx(ctx, tx, AuditEvent{
				ClubID:        &clubID,
				ActorUserID:   &principal.UserID,
				ActorKind:     "portal_user",
				ActingRoleKey: string(assignment.Role),
				Action:        "portal.case.acknowledged",
				TargetType:    "portal_message_case",
				TargetID:      caseID.String(),
				Outcome:       "success",
				CorrelationID: correlationID,
				OccurredAt:    now,
			})
		},
	)
}

func (store *Store) SetCaseWatching(
	ctx context.Context,
	principal Principal,
	caseID uuid.UUID,
	watching bool,
) error {
	if caseID == uuid.Nil {
		return ErrNotFound
	}
	return store.withAuthorizedPortalWrite(
		ctx,
		principal,
		PermissionMessagesView,
		FeatureSecureMessaging,
		func(tx pgx.Tx, assignment Assignment) error {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM portal_message_cases
					WHERE id = $1 AND club_id = $2
				)
			`, caseID, assignment.Scope.ClubID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			if watching {
				_, err := tx.Exec(ctx, `
					INSERT INTO portal_case_watchers (case_id, club_id, user_id)
					VALUES ($1, $2, $3)
					ON CONFLICT DO NOTHING
				`, caseID, assignment.Scope.ClubID, principal.UserID)
				return err
			}
			_, err := tx.Exec(ctx, `
				DELETE FROM portal_case_watchers
				WHERE case_id = $1 AND club_id = $2 AND user_id = $3
			`, caseID, assignment.Scope.ClubID, principal.UserID)
			return err
		},
	)
}

func (store *Store) MarkMessageEmailDelivery(
	ctx context.Context,
	messageID uuid.UUID,
	sent bool,
	deliveryError string,
) error {
	if messageID == uuid.Nil {
		return ErrNotFound
	}
	status := "failed"
	var sentAt *time.Time
	now := store.now()
	if sent {
		status = "sent"
		sentAt = &now
		deliveryError = ""
	}
	deliveryError = truncateNotificationError(deliveryError)
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE portal_club_visible_messages
			SET email_status = $2,
			    email_sent_at = $3,
			    email_last_error = $4
			WHERE id = $1
		`, messageID, status, sentAt, nullableString(deliveryError))
		if err != nil {
			return fmt.Errorf("mark portal case email delivery: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
}

func moduleRequestConfiguration(
	requestType ModuleRequestType,
) (FeatureKey, Permission, MessageCategory, bool) {
	switch requestType {
	case ModuleRequestCorrection:
		return FeatureClubSelfService, PermissionClubProfileManage,
			MessageCategoryContact, true
	case ModuleRequestStarred:
		return FeatureClubSelfService, PermissionStarredPlayersManage,
			MessageCategoryStarred, true
	case ModuleRequestJunior:
		return FeatureJuniorAdministration, PermissionJuniorAdminManage,
			MessageCategoryJunior, true
	case ModuleRequestIdentity:
		return FeaturePlayerIdentity, PermissionPlayerIdentityManage,
			MessageCategoryPlayerIdentity, true
	case ModuleRequestRegistration:
		return FeatureRegistration, PermissionRegistrationManage,
			MessageCategoryRegistration, true
	default:
		return "", "", "", false
	}
}

func (store *Store) CreateOperationalRequest(
	ctx context.Context,
	principal Principal,
	request CreateModuleRequest,
	correlationID string,
) (uuid.UUID, uuid.UUID, error) {
	feature, permission, category, ok := moduleRequestConfiguration(request.Type)
	request.Title = strings.TrimSpace(request.Title)
	request.ExternalReference = strings.TrimSpace(request.ExternalReference)
	request.Message = strings.TrimSpace(request.Message)
	if !ok || len(request.Title) < 3 || len(request.Title) > 200 ||
		len(request.ExternalReference) > 500 ||
		request.Message == "" || len(request.Message) > 10000 {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid portal operational request")
	}
	messagingEnabled, err := store.FeatureEnabled(
		ctx,
		principal,
		FeatureSecureMessaging,
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if !messagingEnabled {
		return uuid.Nil, uuid.Nil, ErrForbidden
	}
	payload := make(map[string]string, len(request.Payload))
	for key, value := range request.Payload {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || len(key) > 64 || len(value) > 1000 {
			return uuid.Nil, uuid.Nil, fmt.Errorf("invalid portal operational request field")
		}
		payload[key] = value
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("encode portal request: %w", err)
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	caseID := uuid.New()
	requestID := uuid.New()
	messageID := uuid.New()
	now := store.now()
	err = store.withAuthorizedPortalWrite(
		ctx,
		principal,
		permission,
		feature,
		func(tx pgx.Tx, assignment Assignment) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_message_cases (
					id, club_id, category, subject, status, priority,
					created_by_user_id, created_at, updated_at
				)
				VALUES ($1, $2, $3, $4, 'awaiting_gmcl', 'normal', $5, $6, $6)
			`, caseID, assignment.Scope.ClubID, category, request.Title,
				principal.UserID, now); err != nil {
				return fmt.Errorf("create operational request case: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_club_visible_messages (
					id, case_id, club_id, author_kind, author_user_id,
					body, email_status, created_at
				)
				VALUES ($1, $2, $3, 'club_user', $4, $5, 'pending', $6)
			`, messageID, caseID, assignment.Scope.ClubID,
				principal.UserID, request.Message, now); err != nil {
				return fmt.Errorf("create operational request message: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_module_requests (
					id, club_id, case_id, request_type, title,
					external_reference, payload, status, submitted_by_user_id,
					human_review_required, created_at, updated_at
				)
				VALUES (
					$1, $2, $3, $4, $5, $6, $7, 'submitted', $8, TRUE, $9, $9
				)
			`, requestID, assignment.Scope.ClubID, caseID, request.Type,
				request.Title, nullableString(request.ExternalReference), payloadJSON,
				principal.UserID, now); err != nil {
				return fmt.Errorf("create portal module request: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_case_watchers (case_id, club_id, user_id, created_at)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING
			`, caseID, assignment.Scope.ClubID, principal.UserID, now); err != nil {
				return err
			}
			clubID := assignment.Scope.ClubID
			return store.appendAuditTx(ctx, tx, AuditEvent{
				ClubID:        &clubID,
				ActorUserID:   &principal.UserID,
				ActorKind:     "portal_user",
				ActingRoleKey: string(assignment.Role),
				Action:        "portal.module_request.submitted",
				TargetType:    "portal_module_request",
				TargetID:      requestID.String(),
				Outcome:       "success",
				CorrelationID: correlationID,
				Metadata: map[string]any{
					"request_type": request.Type,
					"case_id":      caseID.String(),
				},
				OccurredAt: now,
			})
		},
	)
	return requestID, caseID, err
}

func (store *Store) ListOperationalRequests(
	ctx context.Context,
	principal Principal,
	requestType ModuleRequestType,
) ([]ModuleRequest, error) {
	feature, permission, _, ok := moduleRequestConfiguration(requestType)
	if !ok || principal.Assignment == nil ||
		!Authorize(*principal.Assignment, permission,
			principal.Assignment.Scope, store.now()) {
		// View-only roles receive the matching view permission instead.
		viewPermission := permission
		switch requestType {
		case ModuleRequestStarred:
			viewPermission = PermissionStarredPlayersView
		case ModuleRequestIdentity:
			viewPermission = PermissionPlayerIdentityView
		case ModuleRequestRegistration:
			viewPermission = PermissionRegistrationView
		case ModuleRequestJunior:
			viewPermission = PermissionJuniorAdminView
		case ModuleRequestCorrection:
			viewPermission = PermissionClubProfileView
		}
		if principal.Assignment == nil ||
			!Authorize(*principal.Assignment, viewPermission,
				principal.Assignment.Scope, store.now()) {
			return nil, ErrForbidden
		}
	}
	enabled, err := store.FeatureEnabled(ctx, principal, feature)
	if err != nil || !enabled {
		if err != nil {
			return nil, err
		}
		return nil, ErrForbidden
	}
	var requests []ModuleRequest
	err = store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		rows, err := tx.Query(ctx, `
			SELECT
				id,
				case_id,
				request_type,
				title,
				COALESCE(external_reference, ''),
				payload,
				status,
				COALESCE(rule_release, ''),
				human_review_required,
				COALESCE(review_note, ''),
				created_at,
				updated_at
			FROM portal_module_requests
			WHERE club_id = $1 AND request_type = $2
			ORDER BY updated_at DESC, id
		`, assignment.Scope.ClubID, requestType)
		if err != nil {
			return fmt.Errorf("list portal module requests: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item ModuleRequest
			var payloadJSON []byte
			if err := rows.Scan(
				&item.ID,
				&item.CaseID,
				&item.Type,
				&item.Title,
				&item.ExternalReference,
				&payloadJSON,
				&item.Status,
				&item.RuleRelease,
				&item.HumanReviewRequired,
				&item.ReviewNote,
				&item.CreatedAt,
				&item.UpdatedAt,
			); err != nil {
				return fmt.Errorf("scan portal module request: %w", err)
			}
			if err := json.Unmarshal(payloadJSON, &item.Payload); err != nil {
				return fmt.Errorf("decode portal module request: %w", err)
			}
			requests = append(requests, item)
		}
		return rows.Err()
	})
	return requests, err
}

func (store *Store) ListClubContacts(
	ctx context.Context,
	principal Principal,
) ([]ClubContact, error) {
	if principal.Assignment == nil ||
		!Authorize(*principal.Assignment, PermissionClubProfileView,
			principal.Assignment.Scope, store.now()) {
		return nil, ErrForbidden
	}
	enabled, err := store.FeatureEnabled(ctx, principal, FeatureClubSelfService)
	if err != nil || !enabled {
		if err != nil {
			return nil, err
		}
		return nil, ErrForbidden
	}
	var contacts []ClubContact
	err = store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		rows, err := tx.Query(ctx, `
			SELECT
				id,
				role_key,
				display_name,
				email,
				COALESCE(phone, ''),
				status,
				evidence_reference,
				effective_from,
				effective_until,
				created_at
			FROM portal_club_contacts
			WHERE club_id = $1
			ORDER BY role_key, effective_from DESC, id
		`, assignment.Scope.ClubID)
		if err != nil {
			return fmt.Errorf("list portal club contacts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var contact ClubContact
			if err := rows.Scan(
				&contact.ID,
				&contact.RoleKey,
				&contact.DisplayName,
				&contact.Email,
				&contact.Phone,
				&contact.Status,
				&contact.EvidenceReference,
				&contact.EffectiveFrom,
				&contact.EffectiveUntil,
				&contact.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan portal club contact: %w", err)
			}
			contacts = append(contacts, contact)
		}
		return rows.Err()
	})
	return contacts, err
}

func (store *Store) CreateClubContact(
	ctx context.Context,
	principal Principal,
	request CreateClubContactRequest,
	correlationID string,
) (uuid.UUID, error) {
	request.RoleKey = strings.ToLower(strings.TrimSpace(request.RoleKey))
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Email = strings.ToLower(strings.TrimSpace(request.Email))
	request.Phone = strings.TrimSpace(request.Phone)
	request.EvidenceReference = strings.TrimSpace(request.EvidenceReference)
	validRoles := map[string]struct{}{
		"primary_contact":      {},
		"secretary":            {},
		"play_cricket_admin":   {},
		"junior_contact":       {},
		"fixtures_contact":     {},
		"registration_contact": {},
	}
	if _, ok := validRoles[request.RoleKey]; !ok ||
		request.DisplayName == "" || len(request.DisplayName) > 200 ||
		request.EvidenceReference == "" || len(request.EvidenceReference) > 500 {
		return uuid.Nil, fmt.Errorf("invalid club contact")
	}
	address, err := mail.ParseAddress(request.Email)
	if err != nil || !strings.EqualFold(address.Address, request.Email) {
		return uuid.Nil, fmt.Errorf("invalid club contact email")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	contactID := uuid.New()
	now := store.now()
	err = store.withAuthorizedPortalWrite(
		ctx,
		principal,
		PermissionClubProfileManage,
		FeatureClubSelfService,
		func(tx pgx.Tx, assignment Assignment) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_club_contacts (
					id, club_id, role_key, display_name, email, phone,
					status, evidence_reference, submitted_by_user_id,
					effective_from, created_at, updated_at
				)
				VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, $9, $9)
			`, contactID, assignment.Scope.ClubID, request.RoleKey,
				request.DisplayName, request.Email, nullableString(request.Phone),
				request.EvidenceReference, principal.UserID, now); err != nil {
				return fmt.Errorf("create portal club contact: %w", err)
			}
			clubID := assignment.Scope.ClubID
			return store.appendAuditTx(ctx, tx, AuditEvent{
				ClubID:        &clubID,
				ActorUserID:   &principal.UserID,
				ActorKind:     "portal_user",
				ActingRoleKey: string(assignment.Role),
				Action:        "portal.club_contact.submitted",
				TargetType:    "portal_club_contact",
				TargetID:      contactID.String(),
				Outcome:       "success",
				CorrelationID: correlationID,
				Metadata: map[string]any{
					"role_key": request.RoleKey,
				},
				OccurredAt: now,
			})
		},
	)
	return contactID, err
}

func (store *Store) ListFixtureConstraints(
	ctx context.Context,
	principal Principal,
) ([]FixtureConstraint, error) {
	if principal.Assignment == nil ||
		!Authorize(*principal.Assignment, PermissionFixturesView,
			principal.Assignment.Scope, store.now()) {
		return nil, ErrForbidden
	}
	enabled, err := store.FeatureEnabled(ctx, principal, FeatureFixtureOptimisation)
	if err != nil || !enabled {
		if err != nil {
			return nil, err
		}
		return nil, ErrForbidden
	}
	var constraints []FixtureConstraint
	err = store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		rows, err := tx.Query(ctx, `
			SELECT
				constraint_row.id,
				constraint_row.team_id,
				COALESCE(team.name, ''),
				constraint_row.season_id,
				COALESCE(season.name, ''),
				constraint_row.constraint_type,
				constraint_row.description,
				constraint_row.starts_on,
				constraint_row.ends_on,
				constraint_row.hard_constraint,
				constraint_row.status,
				COALESCE(constraint_row.review_note, ''),
				constraint_row.created_at
			FROM portal_fixture_constraints constraint_row
			LEFT JOIN teams team ON team.id = constraint_row.team_id
			LEFT JOIN seasons season ON season.id = constraint_row.season_id
			WHERE constraint_row.club_id = $1
			ORDER BY constraint_row.created_at DESC, constraint_row.id
		`, assignment.Scope.ClubID)
		if err != nil {
			return fmt.Errorf("list fixture constraints: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item FixtureConstraint
			if err := rows.Scan(
				&item.ID,
				&item.TeamID,
				&item.TeamName,
				&item.SeasonID,
				&item.SeasonName,
				&item.ConstraintType,
				&item.Description,
				&item.StartsOn,
				&item.EndsOn,
				&item.HardConstraint,
				&item.Status,
				&item.ReviewNote,
				&item.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan fixture constraint: %w", err)
			}
			constraints = append(constraints, item)
		}
		return rows.Err()
	})
	return constraints, err
}

func (store *Store) CreateFixtureConstraint(
	ctx context.Context,
	principal Principal,
	request CreateFixtureConstraintRequest,
	correlationID string,
) (uuid.UUID, error) {
	request.ConstraintType = strings.ToLower(strings.TrimSpace(request.ConstraintType))
	request.Description = strings.TrimSpace(request.Description)
	validTypes := map[string]struct{}{
		"venue_unavailable": {},
		"team_unavailable":  {},
		"shared_ground":     {},
		"travel_preference": {},
		"paired_team":       {},
		"date_preference":   {},
		"other":             {},
	}
	if _, ok := validTypes[request.ConstraintType]; !ok ||
		len(request.Description) < 3 || len(request.Description) > 1000 ||
		(request.StartsOn != nil && request.EndsOn != nil &&
			request.EndsOn.Before(*request.StartsOn)) {
		return uuid.Nil, fmt.Errorf("invalid fixture constraint")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	constraintID := uuid.New()
	now := store.now()
	err := store.withAuthorizedPortalWrite(
		ctx,
		principal,
		PermissionFixturesManage,
		FeatureFixtureOptimisation,
		func(tx pgx.Tx, assignment Assignment) error {
			if request.TeamID != nil {
				var teamAllowed bool
				if err := tx.QueryRow(ctx, `
					SELECT EXISTS (
						SELECT 1 FROM teams
						WHERE id = $1 AND club_id = $2
					)
				`, *request.TeamID, assignment.Scope.ClubID).Scan(&teamAllowed); err != nil {
					return err
				}
				if !teamAllowed ||
					(assignment.Scope.TeamID != nil &&
						*assignment.Scope.TeamID != *request.TeamID) {
					return ErrForbidden
				}
			}
			if assignment.Scope.SeasonID != nil &&
				(request.SeasonID == nil ||
					*assignment.Scope.SeasonID != *request.SeasonID) {
				return ErrForbidden
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_fixture_constraints (
					id, club_id, team_id, season_id, constraint_type,
					description, starts_on, ends_on, hard_constraint,
					status, submitted_by_user_id, created_at, updated_at
				)
				VALUES (
					$1, $2, $3, $4, $5, $6, $7, $8, $9,
					'submitted', $10, $11, $11
				)
			`, constraintID, assignment.Scope.ClubID, request.TeamID,
				request.SeasonID, request.ConstraintType, request.Description,
				request.StartsOn, request.EndsOn, request.HardConstraint,
				principal.UserID, now); err != nil {
				return fmt.Errorf("create fixture constraint: %w", err)
			}
			clubID := assignment.Scope.ClubID
			return store.appendAuditTx(ctx, tx, AuditEvent{
				ClubID:        &clubID,
				ActorUserID:   &principal.UserID,
				ActorKind:     "portal_user",
				ActingRoleKey: string(assignment.Role),
				Action:        "portal.fixture_constraint.submitted",
				TargetType:    "portal_fixture_constraint",
				TargetID:      constraintID.String(),
				Outcome:       "success",
				CorrelationID: correlationID,
				Metadata: map[string]any{
					"constraint_type": request.ConstraintType,
					"hard_constraint": request.HardConstraint,
				},
				OccurredAt: now,
			})
		},
	)
	return constraintID, err
}

func ParseMessageCategory(value string) (MessageCategory, bool) {
	category := MessageCategory(strings.ToLower(strings.TrimSpace(value)))
	_, ok := validMessageCategories[category]
	return category, ok
}

func ParseModuleRequestType(value string) (ModuleRequestType, bool) {
	requestType := ModuleRequestType(strings.ToLower(strings.TrimSpace(value)))
	_, _, _, ok := moduleRequestConfiguration(requestType)
	return requestType, ok
}
