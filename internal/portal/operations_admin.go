package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AdminMessageCaseSummary struct {
	MessageCaseSummary
	CreatedByEmail    string
	AssignedAdminID   *int32
	AssignedAdminName string
}

type AdminCaseUpdate struct {
	Status          string
	Priority        string
	AssignedAdminID *int32
	DeadlineAt      *time.Time
}

type AdminOperationalRequest struct {
	ModuleRequest
	ClubID   int32
	ClubName string
}

type AdminClubContact struct {
	ClubContact
	ClubID   int32
	ClubName string
}

type AdminMessageDeliveryContext struct {
	MessageID         uuid.UUID
	CaseID            uuid.UUID
	Subject           string
	Body              string
	AuthorKind        string
	CreatedByEmail    string
	CurrentEmailState string
}

func (store *Store) LoadAdminMessageDeliveryContext(
	ctx context.Context,
	messageID uuid.UUID,
) (AdminMessageDeliveryContext, error) {
	if messageID == uuid.Nil {
		return AdminMessageDeliveryContext{}, ErrNotFound
	}
	var item AdminMessageDeliveryContext
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT
				message.id, message.case_id, c.subject, message.body,
				message.author_kind,
				COALESCE((
					SELECT identity.verified_email
					FROM portal_identities identity
					WHERE identity.user_id = c.created_by_user_id
					  AND identity.email_verified
					ORDER BY identity.last_authenticated_at DESC NULLS LAST
					LIMIT 1
				), ''),
				message.email_status
			FROM portal_club_visible_messages message
			JOIN portal_message_cases c ON c.id = message.case_id
			WHERE message.id = $1
		`, messageID).Scan(
			&item.MessageID, &item.CaseID, &item.Subject, &item.Body,
			&item.AuthorKind, &item.CreatedByEmail, &item.CurrentEmailState,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load portal message delivery context: %w", err)
		}
		return nil
	})
	return item, err
}

func (store *Store) ListAdminOperationalRequests(
	ctx context.Context,
) ([]AdminOperationalRequest, error) {
	var requests []AdminOperationalRequest
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				r.id, r.case_id, r.request_type, r.title,
				COALESCE(r.external_reference, ''), r.payload, r.status,
				COALESCE(r.rule_release, ''), r.human_review_required,
				COALESCE(r.review_note, ''), r.created_at, r.updated_at,
				r.club_id, club.name
			FROM portal_module_requests r
			JOIN clubs club ON club.id = r.club_id
			ORDER BY
				CASE r.status
					WHEN 'submitted' THEN 0
					WHEN 'under_review' THEN 1
					WHEN 'awaiting_club' THEN 2
					ELSE 3
				END,
				r.updated_at DESC
		`)
		if err != nil {
			return fmt.Errorf("list admin portal module requests: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item AdminOperationalRequest
			var payload []byte
			if err := rows.Scan(
				&item.ID, &item.CaseID, &item.Type, &item.Title,
				&item.ExternalReference, &payload, &item.Status,
				&item.RuleRelease, &item.HumanReviewRequired,
				&item.ReviewNote, &item.CreatedAt, &item.UpdatedAt,
				&item.ClubID, &item.ClubName,
			); err != nil {
				return fmt.Errorf("scan admin portal module request: %w", err)
			}
			if err := json.Unmarshal(payload, &item.Payload); err != nil {
				return fmt.Errorf("decode admin portal module request: %w", err)
			}
			requests = append(requests, item)
		}
		return rows.Err()
	})
	return requests, err
}

func (store *Store) ListAdminClubContacts(
	ctx context.Context,
) ([]AdminClubContact, error) {
	var contacts []AdminClubContact
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				contact.id, contact.role_key, contact.display_name,
				contact.email, COALESCE(contact.phone, ''), contact.status,
				contact.evidence_reference, contact.effective_from,
				contact.effective_until, contact.created_at,
				contact.club_id, club.name
			FROM portal_club_contacts contact
			JOIN clubs club ON club.id = contact.club_id
			ORDER BY
				CASE contact.status WHEN 'pending' THEN 0 ELSE 1 END,
				contact.created_at DESC
		`)
		if err != nil {
			return fmt.Errorf("list admin portal club contacts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item AdminClubContact
			if err := rows.Scan(
				&item.ID, &item.RoleKey, &item.DisplayName, &item.Email,
				&item.Phone, &item.Status, &item.EvidenceReference,
				&item.EffectiveFrom, &item.EffectiveUntil, &item.CreatedAt,
				&item.ClubID, &item.ClubName,
			); err != nil {
				return fmt.Errorf("scan admin portal club contact: %w", err)
			}
			contacts = append(contacts, item)
		}
		return rows.Err()
	})
	return contacts, err
}

func (store *Store) ListAdminMessageCases(
	ctx context.Context,
) ([]AdminMessageCaseSummary, error) {
	var cases []AdminMessageCaseSummary
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
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
				COALESCE(MAX(message.created_at), c.created_at),
				COALESCE((
					SELECT identity.verified_email
					FROM portal_identities identity
					WHERE identity.user_id = c.created_by_user_id
					  AND identity.email_verified
					ORDER BY identity.last_authenticated_at DESC NULLS LAST
					LIMIT 1
				), ''),
				c.assigned_admin_user_id,
				COALESCE(assigned.username, '')
			FROM portal_message_cases c
			JOIN clubs club ON club.id = c.club_id
			LEFT JOIN portal_club_visible_messages message ON message.case_id = c.id
			LEFT JOIN admin_users assigned ON assigned.id = c.assigned_admin_user_id
			GROUP BY c.id, club.name, assigned.username
			ORDER BY
				CASE c.priority WHEN 'urgent' THEN 0 ELSE 1 END,
				CASE c.status
					WHEN 'new' THEN 0
					WHEN 'awaiting_gmcl' THEN 1
					WHEN 'reopened' THEN 2
					WHEN 'in_progress' THEN 3
					WHEN 'awaiting_club' THEN 4
					WHEN 'resolved' THEN 5
					ELSE 6
				END,
				c.updated_at DESC
		`)
		if err != nil {
			return fmt.Errorf("list admin portal cases: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item AdminMessageCaseSummary
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
				&item.CreatedByEmail,
				&item.AssignedAdminID,
				&item.AssignedAdminName,
			); err != nil {
				return fmt.Errorf("scan admin portal case: %w", err)
			}
			cases = append(cases, item)
		}
		return rows.Err()
	})
	return cases, err
}

func (store *Store) LoadAdminMessageCase(
	ctx context.Context,
	caseID uuid.UUID,
) (AdminMessageCaseDetail, error) {
	if caseID == uuid.Nil {
		return AdminMessageCaseDetail{}, ErrNotFound
	}
	var detail AdminMessageCaseDetail
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
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
				c.assigned_admin_user_id
			FROM portal_message_cases c
			JOIN clubs club ON club.id = c.club_id
			WHERE c.id = $1
		`, caseID).Scan(
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
			&detail.AssignedAdminID,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load admin portal case: %w", err)
		}

		messageRows, err := tx.Query(ctx, `
			SELECT
				message.id,
				message.case_id,
				message.author_kind,
				CASE message.author_kind
					WHEN 'club_user' THEN COALESCE(portal_user.display_name, 'Club user')
					WHEN 'gmcl_admin' THEN COALESCE(admin_user.username, 'GMCL')
					ELSE 'System'
				END,
				message.body,
				message.email_status,
				message.created_at
			FROM portal_club_visible_messages message
			LEFT JOIN portal_users portal_user ON portal_user.id = message.author_user_id
			LEFT JOIN admin_users admin_user ON admin_user.id = message.author_admin_user_id
			WHERE message.case_id = $1
			ORDER BY message.created_at, message.id
		`, caseID)
		if err != nil {
			return fmt.Errorf("load admin visible messages: %w", err)
		}
		defer messageRows.Close()
		for messageRows.Next() {
			var message ClubVisibleMessage
			if err := messageRows.Scan(
				&message.ID,
				&message.CaseID,
				&message.AuthorKind,
				&message.AuthorLabel,
				&message.Body,
				&message.EmailStatus,
				&message.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan admin visible message: %w", err)
			}
			detail.Messages = append(detail.Messages, message)
		}
		if err := messageRows.Err(); err != nil {
			return err
		}
		detail.MessageCount = int64(len(detail.Messages))
		if len(detail.Messages) > 0 {
			detail.LastMessageAt = detail.Messages[len(detail.Messages)-1].CreatedAt
		} else {
			detail.LastMessageAt = detail.CreatedAt
		}

		noteRows, err := tx.Query(ctx, `
			SELECT
				note.id,
				COALESCE(admin_user.username, 'GMCL'),
				note.body,
				note.created_at
			FROM portal_internal_notes note
			LEFT JOIN admin_users admin_user ON admin_user.id = note.author_admin_user_id
			WHERE note.case_id = $1
			ORDER BY note.created_at, note.id
		`, caseID)
		if err != nil {
			return fmt.Errorf("load portal internal notes: %w", err)
		}
		defer noteRows.Close()
		for noteRows.Next() {
			var note InternalNote
			if err := noteRows.Scan(
				&note.ID,
				&note.AuthorLabel,
				&note.Body,
				&note.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan portal internal note: %w", err)
			}
			detail.InternalNotes = append(detail.InternalNotes, note)
		}
		return noteRows.Err()
	})
	return detail, err
}

func (store *Store) AddAdminCaseMessage(
	ctx context.Context,
	caseID uuid.UUID,
	adminID int32,
	body string,
	correlationID string,
) (uuid.UUID, error) {
	body = strings.TrimSpace(body)
	if caseID == uuid.Nil || adminID <= 0 || body == "" || len(body) > 10000 {
		return uuid.Nil, fmt.Errorf("invalid GMCL case reply")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	messageID := uuid.New()
	now := store.now()
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			SELECT club_id FROM portal_message_cases
			WHERE id = $1 AND status <> 'closed'
			FOR UPDATE
		`, caseID).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load GMCL reply case: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_club_visible_messages (
				id, case_id, club_id, author_kind, author_admin_user_id,
				body, email_status, created_at
			)
			VALUES ($1, $2, $3, 'gmcl_admin', $4, $5, 'pending', $6)
		`, messageID, caseID, clubID, adminID, body, now); err != nil {
			return fmt.Errorf("create GMCL case reply: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_message_cases
			SET status = 'awaiting_club',
			    assigned_admin_user_id = COALESCE(assigned_admin_user_id, $2),
			    version = version + 1,
			    updated_at = $3
			WHERE id = $1
		`, caseID, adminID, now); err != nil {
			return fmt.Errorf("update GMCL replied case: %w", err)
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.case.gmcl_replied",
			TargetType:    "portal_message_case",
			TargetID:      caseID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			OccurredAt:    now,
		})
	})
	return messageID, err
}

func (store *Store) AddInternalCaseNote(
	ctx context.Context,
	caseID uuid.UUID,
	adminID int32,
	body string,
	correlationID string,
) (uuid.UUID, error) {
	body = strings.TrimSpace(body)
	if caseID == uuid.Nil || adminID <= 0 || body == "" || len(body) > 10000 {
		return uuid.Nil, fmt.Errorf("invalid GMCL internal note")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	noteID := uuid.New()
	now := store.now()
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			SELECT club_id FROM portal_message_cases WHERE id = $1
		`, caseID).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load internal note case: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_internal_notes (
				id, case_id, club_id, author_admin_user_id, body, created_at
			)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, noteID, caseID, clubID, adminID, body, now); err != nil {
			return fmt.Errorf("create GMCL internal note: %w", err)
		}
		// Deliberately do not update portal_message_cases.updated_at. A club
		// response must not reveal that an internal note exists.
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.case.internal_note_added",
			TargetType:    "portal_internal_note",
			TargetID:      noteID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			OccurredAt:    now,
		})
	})
	return noteID, err
}

func (store *Store) UpdateAdminCase(
	ctx context.Context,
	caseID uuid.UUID,
	adminID int32,
	update AdminCaseUpdate,
	correlationID string,
) error {
	update.Status = strings.ToLower(strings.TrimSpace(update.Status))
	update.Priority = strings.ToLower(strings.TrimSpace(update.Priority))
	validStatuses := map[string]struct{}{
		"new":           {},
		"awaiting_gmcl": {},
		"awaiting_club": {},
		"in_progress":   {},
		"resolved":      {},
		"closed":        {},
		"reopened":      {},
	}
	if caseID == uuid.Nil || adminID <= 0 {
		return ErrForbidden
	}
	if _, ok := validStatuses[update.Status]; !ok {
		return fmt.Errorf("invalid portal case status")
	}
	if update.Priority != "normal" && update.Priority != "urgent" {
		return fmt.Errorf("invalid portal case priority")
	}
	if update.AssignedAdminID != nil && *update.AssignedAdminID <= 0 {
		return fmt.Errorf("invalid portal case assignee")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			SELECT club_id FROM portal_message_cases WHERE id = $1 FOR UPDATE
		`, caseID).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE portal_message_cases
			SET status = $2,
			    priority = $3,
			    assigned_admin_user_id = $4,
			    deadline_at = $5,
			    closed_at = CASE WHEN $2 = 'closed' THEN $6 ELSE NULL END,
			    version = version + 1,
			    updated_at = $6
			WHERE id = $1
		`, caseID, update.Status, update.Priority, update.AssignedAdminID,
			update.DeadlineAt, now)
		if err != nil {
			return fmt.Errorf("update portal case: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.case.updated",
			TargetType:    "portal_message_case",
			TargetID:      caseID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"status":   update.Status,
				"priority": update.Priority,
			},
			OccurredAt: now,
		})
	})
}

func (store *Store) ReviewOperationalRequest(
	ctx context.Context,
	requestID uuid.UUID,
	adminID int32,
	status string,
	reviewNote string,
	ruleRelease string,
	correlationID string,
) error {
	status = strings.ToLower(strings.TrimSpace(status))
	reviewNote = strings.TrimSpace(reviewNote)
	ruleRelease = strings.TrimSpace(ruleRelease)
	validStatuses := map[string]struct{}{
		"under_review":  {},
		"awaiting_club": {},
		"approved":      {},
		"rejected":      {},
		"completed":     {},
	}
	if requestID == uuid.Nil || adminID <= 0 {
		return ErrForbidden
	}
	if _, ok := validStatuses[status]; !ok || len(reviewNote) > 2000 ||
		len(ruleRelease) > 200 {
		return fmt.Errorf("invalid operational request review")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		var requestType ModuleRequestType
		tag, err := tx.Exec(ctx, `
			UPDATE portal_module_requests
			SET status = $2,
			    reviewed_by_admin_user_id = $3,
			    review_note = $4,
			    rule_release = $5,
			    reviewed_at = $6,
			    version = version + 1,
			    updated_at = $6
			WHERE id = $1
		`, requestID, status, adminID, nullableString(reviewNote),
			nullableString(ruleRelease), now)
		if err != nil {
			return fmt.Errorf("review portal module request: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `
			SELECT club_id, request_type FROM portal_module_requests WHERE id = $1
		`, requestID).Scan(&clubID, &requestType); err != nil {
			return err
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.module_request.reviewed",
			TargetType:    "portal_module_request",
			TargetID:      requestID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"request_type": requestType,
				"status":       status,
			},
			OccurredAt: now,
		})
	})
}

func (store *Store) VerifyClubContact(
	ctx context.Context,
	contactID uuid.UUID,
	adminID int32,
	approved bool,
	correlationID string,
) error {
	if contactID == uuid.Nil || adminID <= 0 {
		return ErrForbidden
	}
	status := "rejected"
	var verifiedAt *time.Time
	now := store.now()
	if approved {
		status = "verified"
		verifiedAt = &now
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = uuid.NewString()
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var clubID int32
		if err := tx.QueryRow(ctx, `
			UPDATE portal_club_contacts
			SET status = $2,
			    verified_by_admin_user_id = $3,
			    verified_at = $4,
			    version = version + 1,
			    updated_at = $5
			WHERE id = $1 AND status = 'pending'
			RETURNING club_id
		`, contactID, status, adminID, verifiedAt, now).Scan(&clubID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("review portal club contact: %w", err)
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorKind:     "legacy_admin",
			LegacyAdminID: &adminID,
			Action:        "portal.club_contact.reviewed",
			TargetType:    "portal_club_contact",
			TargetID:      contactID.String(),
			Outcome:       "success",
			CorrelationID: correlationID,
			Metadata: map[string]any{
				"status": status,
			},
			OccurredAt: now,
		})
	})
}
