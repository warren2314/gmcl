package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	NotificationTemplateAccountActivated = "portal_account_activated"
	NotificationTemplateAccessRevoked    = "portal_access_revoked"

	NotificationMaxAttempts  = 5
	notificationLeaseTimeout = 10 * time.Minute
)

type OutboxMaterializationResult struct {
	Selected int
	Created  int
	Deferred int
}

type PendingNotification struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	ClubID       *int32
	TemplateKey  string
	Recipient    string
	Payload      map[string]any
	AttemptCount int
}

type NotificationDeliveryHealth struct {
	UnpublishedEvents int64
	OutboxDeadLetter  int64
	Pending           int64
	Retrying          int64
	Sending           int64
	Sent              int64
	DeadLetter        int64
	OldestReadyAt     *time.Time
	LatestError       string
}

type portalOutboxEvent struct {
	ID        uuid.UUID
	EventType string
	RawUserID string
	RawClubID string
	UserID    uuid.UUID
	ClubID    int32
	Role      string
}

// MaterializeSecurityNotifications turns committed portal security events into
// one durable email notification per event. The unique idempotency key makes
// retries safe if a worker exits after the insert but before publishing the
// source event.
func (store *Store) MaterializeSecurityNotifications(
	ctx context.Context,
	limit int,
) (OutboxMaterializationResult, error) {
	if limit <= 0 || limit > 100 {
		return OutboxMaterializationResult{}, ErrForbidden
	}
	var result OutboxMaterializationResult
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				id,
				event_type,
				COALESCE(payload->>'user_id', ''),
				COALESCE(payload->>'club_id', ''),
				COALESCE(payload->>'role', '')
			FROM portal_outbox_events
			WHERE published_at IS NULL
			  AND attempt_count < $2
			ORDER BY occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		`, limit, NotificationMaxAttempts)
		if err != nil {
			return fmt.Errorf("select portal security outbox events: %w", err)
		}
		var events []portalOutboxEvent
		for rows.Next() {
			var event portalOutboxEvent
			if err := rows.Scan(
				&event.ID,
				&event.EventType,
				&event.RawUserID,
				&event.RawClubID,
				&event.Role,
			); err != nil {
				rows.Close()
				return fmt.Errorf("scan portal security outbox event: %w", err)
			}
			events = append(events, event)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate portal security outbox events: %w", err)
		}
		rows.Close()

		result.Selected = len(events)
		for index := range events {
			event := &events[index]
			event.UserID, err = uuid.Parse(event.RawUserID)
			if err != nil {
				if err := markOutboxMaterializationFailure(
					ctx,
					tx,
					event.ID,
					"event has no valid user identifier",
				); err != nil {
					return err
				}
				result.Deferred++
				continue
			}
			rawClubID, err := strconv.ParseInt(strings.TrimSpace(event.RawClubID), 10, 32)
			if err != nil || rawClubID <= 0 {
				if err := markOutboxMaterializationFailure(
					ctx,
					tx,
					event.ID,
					"event has no valid club identifier",
				); err != nil {
					return err
				}
				result.Deferred++
				continue
			}
			event.ClubID = int32(rawClubID)
			templateKey, supported := securityNotificationTemplate(event.EventType)
			if !supported {
				if err := markOutboxMaterializationFailure(
					ctx,
					tx,
					event.ID,
					"unsupported portal security event type",
				); err != nil {
					return err
				}
				result.Deferred++
				continue
			}

			var (
				recipient string
				clubName  string
			)
			err = tx.QueryRow(ctx, `
				SELECT identity.verified_email, club.name
				FROM clubs AS club
				JOIN LATERAL (
					SELECT verified_email
					FROM portal_identities
					WHERE user_id = $1
					  AND email_verified
					  AND NULLIF(BTRIM(verified_email), '') IS NOT NULL
					ORDER BY last_authenticated_at DESC NULLS LAST, created_at DESC, id
					LIMIT 1
				) AS identity ON TRUE
				WHERE club.id = $2
			`, event.UserID, event.ClubID).Scan(&recipient, &clubName)
			if errors.Is(err, pgx.ErrNoRows) {
				if err := markOutboxMaterializationFailure(
					ctx,
					tx,
					event.ID,
					"no verified delivery identity is available",
				); err != nil {
					return err
				}
				result.Deferred++
				continue
			}
			if err != nil {
				return fmt.Errorf("resolve portal security notification recipient: %w", err)
			}

			payload, err := json.Marshal(map[string]any{
				"event_id":   event.ID,
				"event_type": event.EventType,
				"club_id":    event.ClubID,
				"club_name":  clubName,
				"role":       strings.TrimSpace(event.Role),
			})
			if err != nil {
				return fmt.Errorf("encode portal security notification payload: %w", err)
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO portal_notifications (
					club_id,
					user_id,
					channel,
					template_key,
					recipient,
					payload,
					status,
					idempotency_key
				)
				VALUES ($1, $2, 'email', $3, $4, $5, 'pending', $6)
				ON CONFLICT (idempotency_key) DO NOTHING
			`, event.ClubID, event.UserID, templateKey, recipient, string(payload),
				"portal-security-event:"+event.ID.String())
			if err != nil {
				return fmt.Errorf("create portal security notification: %w", err)
			}
			if tag.RowsAffected() == 1 {
				result.Created++
			}
			if _, err := tx.Exec(ctx, `
				UPDATE portal_outbox_events
				SET published_at = $2,
				    last_error = NULL
				WHERE id = $1
				  AND published_at IS NULL
			`, event.ID, store.now()); err != nil {
				return fmt.Errorf("publish portal security outbox event: %w", err)
			}
		}
		return nil
	})
	return result, err
}

func securityNotificationTemplate(eventType string) (string, bool) {
	switch strings.TrimSpace(eventType) {
	case "portal.invitation.redeemed":
		return NotificationTemplateAccountActivated, true
	case "portal.role_assignment.revoked":
		return NotificationTemplateAccessRevoked, true
	default:
		return "", false
	}
}

func markOutboxMaterializationFailure(
	ctx context.Context,
	tx pgx.Tx,
	eventID uuid.UUID,
	message string,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE portal_outbox_events
		SET attempt_count = attempt_count + 1,
		    last_error = $2
		WHERE id = $1
		  AND published_at IS NULL
	`, eventID, truncateNotificationError(message)); err != nil {
		return fmt.Errorf("record portal outbox materialization failure: %w", err)
	}
	return nil
}

// ClaimSecurityNotification obtains a short database lease for one delivery.
// AttemptCount is the lease generation and must be supplied when completing
// the delivery so a stale worker cannot overwrite a later retry result.
func (store *Store) ClaimSecurityNotification(
	ctx context.Context,
) (*PendingNotification, error) {
	now := store.now()
	var notification PendingNotification
	var (
		clubID  pgtype.Int4
		payload []byte
		found   bool
	)
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE portal_notifications
			SET status = 'failed',
			    last_error = 'delivery lease expired after final attempt',
			    updated_at = $1::timestamptz
			WHERE status = 'sending'
			  AND attempt_count >= $2
			  AND updated_at <= $1::timestamptz - make_interval(secs => $3)
		`, now, NotificationMaxAttempts,
			int64(notificationLeaseTimeout/time.Second)); err != nil {
			return fmt.Errorf("expire final portal notification leases: %w", err)
		}
		scanErr := tx.QueryRow(ctx, `
			WITH candidate AS (
				SELECT id
				FROM portal_notifications
				WHERE channel = 'email'
				  AND attempt_count < $2
				  AND (
				      (
				        status IN ('pending', 'failed')
				        AND available_at <= $1::timestamptz
				      )
				      OR (
				        status = 'sending'
				        AND updated_at <= $1::timestamptz - make_interval(secs => $3)
				      )
				  )
				ORDER BY available_at, created_at, id
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE portal_notifications AS notification
			SET status = 'sending',
			    attempt_count = notification.attempt_count + 1,
			    last_error = NULL,
			    updated_at = $1::timestamptz
			FROM candidate
			WHERE notification.id = candidate.id
			RETURNING
				notification.id,
				notification.user_id,
				notification.club_id,
				notification.template_key,
				notification.recipient,
				notification.payload,
				notification.attempt_count
		`, now, NotificationMaxAttempts, int64(notificationLeaseTimeout/time.Second)).Scan(
			&notification.ID,
			&notification.UserID,
			&clubID,
			&notification.TemplateKey,
			&notification.Recipient,
			&payload,
			&notification.AttemptCount,
		)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		found = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if clubID.Valid {
		value := clubID.Int32
		notification.ClubID = &value
	}
	if err := json.Unmarshal(payload, &notification.Payload); err != nil {
		_ = store.MarkSecurityNotificationFailed(
			ctx,
			notification.ID,
			notification.AttemptCount,
			"notification payload could not be decoded",
		)
		return nil, fmt.Errorf("decode portal security notification payload: %w", err)
	}
	return &notification, nil
}

func (store *Store) MarkSecurityNotificationSent(
	ctx context.Context,
	notificationID uuid.UUID,
	attempt int,
) error {
	if notificationID == uuid.Nil || attempt <= 0 {
		return ErrForbidden
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE portal_notifications
			SET status = 'sent',
			    sent_at = $3,
			    last_error = NULL,
			    updated_at = $3
			WHERE id = $1
			  AND status = 'sending'
			  AND attempt_count = $2
		`, notificationID, attempt, now)
		if err != nil {
			return fmt.Errorf("complete portal security notification: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
}

func (store *Store) MarkSecurityNotificationFailed(
	ctx context.Context,
	notificationID uuid.UUID,
	attempt int,
	deliveryError string,
) error {
	if notificationID == uuid.Nil || attempt <= 0 {
		return ErrForbidden
	}
	now := store.now()
	retryAt := now.Add(notificationRetryDelay(attempt))
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE portal_notifications
			SET status = 'failed',
			    available_at = $3,
			    last_error = $4,
			    updated_at = $5
			WHERE id = $1
			  AND status = 'sending'
			  AND attempt_count = $2
		`, notificationID, attempt, retryAt,
			truncateNotificationError(deliveryError), now)
		if err != nil {
			return fmt.Errorf("fail portal security notification: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
}

func notificationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<(attempt-1)) * time.Minute
}

func truncateNotificationError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "delivery failed"
	}
	const maxRunes = 1000
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}

func (store *Store) LoadNotificationDeliveryHealth(
	ctx context.Context,
) (NotificationDeliveryHealth, error) {
	var health NotificationDeliveryHealth
	var oldestReady pgtype.Timestamptz
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM portal_outbox_events WHERE published_at IS NULL),
				(SELECT COUNT(*)
				 FROM portal_outbox_events
				 WHERE published_at IS NULL
				   AND attempt_count >= $1),
				COUNT(*) FILTER (WHERE status = 'pending'),
				COUNT(*) FILTER (
					WHERE status = 'failed' AND attempt_count < $1
				),
				COUNT(*) FILTER (WHERE status = 'sending'),
				COUNT(*) FILTER (WHERE status = 'sent'),
				COUNT(*) FILTER (
					WHERE status = 'failed' AND attempt_count >= $1
				),
				MIN(available_at) FILTER (
					WHERE status IN ('pending', 'failed')
					  AND attempt_count < $1
				),
				COALESCE((
					SELECT error_message
					FROM (
						SELECT last_error AS error_message, updated_at AS error_at, id
						FROM portal_notifications
						WHERE last_error IS NOT NULL
						UNION ALL
						SELECT last_error AS error_message, occurred_at AS error_at, id
						FROM portal_outbox_events
						WHERE last_error IS NOT NULL
					) AS errors
					ORDER BY error_at DESC, id DESC
					LIMIT 1
				), '')
			FROM portal_notifications
		`, NotificationMaxAttempts).Scan(
			&health.UnpublishedEvents,
			&health.OutboxDeadLetter,
			&health.Pending,
			&health.Retrying,
			&health.Sending,
			&health.Sent,
			&health.DeadLetter,
			&oldestReady,
			&health.LatestError,
		)
	})
	if err != nil {
		return NotificationDeliveryHealth{}, fmt.Errorf(
			"load portal notification delivery health: %w",
			err,
		)
	}
	health.OldestReadyAt = timePtr(oldestReady)
	return health, nil
}
