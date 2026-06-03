package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
)

var (
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenExpired = errors.New("token expired")
)

type MagicToken struct {
	ID            int64
	CaptainID     int32
	SeasonID      int32
	WeekID        int32
	MatchDate     *time.Time
	DelegateName  string
	DelegateEmail string
}

// GenerateAndStoreMagicToken creates a random token, stores its hash, and returns the plaintext token.
func GenerateAndStoreMagicToken(ctx context.Context, pool *db.Pool, captainID, seasonID, weekID int32, ttl time.Duration, ip, ua string) (string, error) {
	expiresAt := time.Now().Add(ttl)
	return GenerateAndStoreMagicTokenWithRevocation(ctx, pool, captainID, seasonID, weekID, expiresAt, ip, ua)
}

// GenerateAndStoreMagicTokenWithRevocation invalidates any existing unused tokens for the same
// captain/week (and match_date if provided), then inserts a new token.
func GenerateAndStoreMagicTokenWithRevocation(ctx context.Context, pool *db.Pool, captainID, seasonID, weekID int32, expiresAt time.Time, ip, ua string) (string, error) {
	return GenerateAndStoreMagicTokenWithDelegate(ctx, pool, captainID, seasonID, weekID, nil, expiresAt, ip, ua, "", "")
}

// GenerateAndStoreMagicTokenForDate inserts a new token scoped to a specific match_date
// without revoking any existing tokens. Used by reminder emails so that captains can
// still use links from earlier emails even after a new reminder is sent.
func GenerateAndStoreMagicTokenForDate(ctx context.Context, pool *db.Pool, captainID, seasonID, weekID int32, matchDate time.Time, expiresAt time.Time, ip, ua string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))

	_, err := pool.Exec(ctx, `
		INSERT INTO magic_link_tokens
		    (captain_id, season_id, week_id, match_date, token_hash, expires_at, request_ip, request_user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::inet, $8)
	`, captainID, seasonID, weekID, matchDate, hash[:], expiresAt, ip, ua)
	if err != nil {
		return "", err
	}
	return token, nil
}

// GenerateAndStoreMagicTokenWithDelegate creates or replaces the token for a captain/week
// (scoped to matchDate when non-nil) and can optionally attach delegate identity.
func GenerateAndStoreMagicTokenWithDelegate(ctx context.Context, pool *db.Pool, captainID, seasonID, weekID int32, matchDate *time.Time, expiresAt time.Time, ip, ua, delegateName, delegateEmail string) (string, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	now := time.Now()
	delegateEmail = strings.TrimSpace(delegateEmail)

	// Revoke existing unused tokens for the same captain/week/date and actor scope.
	// Captain links must not invalidate stand-in links, and a stand-in invite should
	// only supersede earlier invites for the same stand-in email.
	if matchDate != nil {
		if delegateEmail != "" {
			_, err = tx.Exec(ctx, `
				UPDATE magic_link_tokens SET used_at = $1
				WHERE captain_id = $2 AND season_id = $3 AND week_id = $4
				  AND match_date = $5
				  AND LOWER(delegate_email) = LOWER($6)
				  AND used_at IS NULL AND expires_at > $1
			`, now, captainID, seasonID, weekID, matchDate, delegateEmail)
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE magic_link_tokens SET used_at = $1
				WHERE captain_id = $2 AND season_id = $3 AND week_id = $4
				  AND match_date = $5
				  AND NULLIF(delegate_email, '') IS NULL
				  AND used_at IS NULL AND expires_at > $1
			`, now, captainID, seasonID, weekID, matchDate)
		}
	} else {
		if delegateEmail != "" {
			_, err = tx.Exec(ctx, `
				UPDATE magic_link_tokens SET used_at = $1
				WHERE captain_id = $2 AND season_id = $3 AND week_id = $4
				  AND match_date IS NULL
				  AND LOWER(delegate_email) = LOWER($5)
				  AND used_at IS NULL AND expires_at > $1
			`, now, captainID, seasonID, weekID, delegateEmail)
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE magic_link_tokens SET used_at = $1
				WHERE captain_id = $2 AND season_id = $3 AND week_id = $4
				  AND match_date IS NULL
				  AND NULLIF(delegate_email, '') IS NULL
				  AND used_at IS NULL AND expires_at > $1
			`, now, captainID, seasonID, weekID)
		}
	}
	if err != nil {
		return "", err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))

	_, err = tx.Exec(ctx, `
		INSERT INTO magic_link_tokens
		    (captain_id, season_id, week_id, match_date, token_hash, expires_at, request_ip, request_user_agent, delegate_name, delegate_email)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::inet, $8, NULLIF($9, ''), NULLIF($10, ''))
	`, captainID, seasonID, weekID, matchDate, hash[:], expiresAt, ip, ua, delegateName, delegateEmail)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return token, nil
}

// ValidateMagicToken verifies a token is valid and returns the linked identity
// without consuming it, allowing the same link to be used multiple times until
// it expires at Wednesday 23:59 or is superseded by a newer token.
func ValidateMagicToken(ctx context.Context, pool *db.Pool, token string) (*MagicToken, error) {
	hash := sha256.Sum256([]byte(token))

	var t MagicToken
	var expiresAt time.Time
	var usedAt sql.NullTime

	err := pool.QueryRow(ctx, `
		SELECT id, captain_id, season_id, week_id, match_date, COALESCE(delegate_name, ''), COALESCE(delegate_email, ''), expires_at, used_at
		FROM magic_link_tokens
		WHERE token_hash = $1
	`, hash[:]).Scan(&t.ID, &t.CaptainID, &t.SeasonID, &t.WeekID, &t.MatchDate, &t.DelegateName, &t.DelegateEmail, &expiresAt, &usedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}

	now := time.Now()
	if !usedAt.Time.IsZero() {
		return nil, ErrTokenInvalid
	}
	if now.After(expiresAt) {
		return nil, ErrTokenExpired
	}

	return &t, nil
}

// ConsumeMagicToken verifies a token, marks it as used, and returns the linked identity.
// Kept for any flows that require true one-time use.
func ConsumeMagicToken(ctx context.Context, pool *db.Pool, token string) (*MagicToken, error) {
	hash := sha256.Sum256([]byte(token))

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var t MagicToken
	var expiresAt time.Time
	var usedAt sql.NullTime

	err = tx.QueryRow(ctx, `
		SELECT id, captain_id, season_id, week_id, COALESCE(delegate_name, ''), COALESCE(delegate_email, ''), expires_at, used_at
		FROM magic_link_tokens
		WHERE token_hash = $1
	`, hash[:]).Scan(&t.ID, &t.CaptainID, &t.SeasonID, &t.WeekID, &t.DelegateName, &t.DelegateEmail, &expiresAt, &usedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}

	now := time.Now()
	if !usedAt.Time.IsZero() {
		return nil, ErrTokenInvalid
	}
	if now.After(expiresAt) {
		return nil, ErrTokenExpired
	}

	_, err = tx.Exec(ctx, `
		UPDATE magic_link_tokens
		SET used_at = $1
		WHERE token_hash = $2 AND used_at IS NULL
	`, now, hash[:])
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &t, nil
}
