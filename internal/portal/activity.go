package portal

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	defaultUserActivityLimit = 50
	maxUserActivityLimit     = 100
)

// UserActivity is the deliberately minimal account-facing audit projection.
// It excludes metadata, target identifiers, network/device data and hashes.
type UserActivity struct {
	Action     string
	Outcome    string
	ActingRole RoleKey
	OccurredAt time.Time
}

// ListUserActivity returns only events for the authenticated user that are
// visible in the selected club context under portal audit RLS. Global account
// events are visible in every valid context; another club's events are not.
func (store *Store) ListUserActivity(
	ctx context.Context,
	principal Principal,
	limit int,
) ([]UserActivity, error) {
	if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil {
		return nil, ErrUnauthenticated
	}
	limit = normalizedUserActivityLimit(limit)
	activities := make([]UserActivity, 0, limit)
	err := store.WithTenantTx(ctx, principal, func(tx pgx.Tx, _ Assignment) error {
		rows, err := tx.Query(ctx, `
			SELECT
				action,
				outcome,
				COALESCE(acting_role_key, ''),
				occurred_at
			FROM portal_audit_events
			WHERE actor_user_id = $1
			ORDER BY occurred_at DESC, id DESC
			LIMIT $2
		`, principal.UserID, limit)
		if err != nil {
			return fmt.Errorf("list portal user activity: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var activity UserActivity
			if err := rows.Scan(
				&activity.Action,
				&activity.Outcome,
				&activity.ActingRole,
				&activity.OccurredAt,
			); err != nil {
				return fmt.Errorf("scan portal user activity: %w", err)
			}
			activities = append(activities, activity)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate portal user activity: %w", err)
		}
		return nil
	})
	return activities, err
}

func normalizedUserActivityLimit(limit int) int {
	if limit <= 0 {
		return defaultUserActivityLimit
	}
	if limit > maxUserActivityLimit {
		return maxUserActivityLimit
	}
	return limit
}
