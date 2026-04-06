package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
)

var (
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenExpired = errors.New("token expired")
)

type MagicToken struct {
	CaptainID     int32
	SeasonID      int32
	WeekID        int32
	DelegateName  string
	DelegateEmail string
}

// GenerateAndStoreMagicToken creates a random token, stores its hash, and returns the plaintext token.
func GenerateAndStoreMagicToken(ctx context.Context, pool *db.Pool, captainID, seasonID, weekID int32, ttl time.Duration, ip, ua string) (string, error) {
	expiresAt := time.Now().Add(ttl)
	return GenerateAndStoreMagicTokenWithRevocation(ctx, pool, captainID, seasonID, weekID, expiresAt, ip, ua)
}

// GenerateAndStoreMagicTokenWithRevocation invalidates any existing unused tokens for the same
// captain/week, then inserts a new token with the given expiresAt. Call this when you want
// "latest link wins" and an explicit expiry (e.g. next Wednesday 23:59:59).
func GenerateAndStoreMagicTokenWithRevocation(ctx context.Context, pool *db.Pool, captainID, seasonID, weekID int32, expiresAt time.Time, ip, ua string) (string, error) {
	return GenerateAndStoreMagicTokenWithDelegate(ctx, pool, captainID, seasonID, weekID, expiresAt, ip, ua, "", "")
}

// GenerateAndStoreMagicTokenWithDelegate creates or replaces the token for a captain/week and
// can optionally attach delegate identity (for stand-in captain access).
func GenerateAndStoreMagicTokenWithDelegate(ctx context.Context, pool *db.Pool, captainID, seasonID, weekID int32, expiresAt time.Time, ip, ua, delegateName, delegateEmail string) (string, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	now := time.Now()
	_, err = tx.Exec(ctx, `
		UPDATE magic_link_tokens
		SET used_at = $1
		WHERE captain_id = $2 AND season_id = $3 AND week_id = $4
		  AND used_at IS NULL AND expires_at > $1
	`, now, captainID, seasonID, weekID)
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
		INSERT INTO magic_link_tokens (captain_id, season_id, week_id, token_hash, expires_at, request_ip, request_user_agent, delegate_name, delegate_email)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''))
	`, captainID, seasonID, weekID, hash[:], expiresAt, ip, ua, delegateName, delegateEmail)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return token, nil
}

// ConsumeMagicToken verifies a token, marks it as used, and returns the linked identity.
func ConsumeMagicToken(ctx context.Context, pool *db.Pool, token string) (*MagicToken, error) {
	hash := sha256.Sum256([]byte(token))

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // safe even if committed

	var t MagicToken
	var expiresAt time.Time
	var usedAt sql.NullTime

	err = tx.QueryRow(ctx, `
		SELECT captain_id, season_id, week_id, COALESCE(delegate_name, ''), COALESCE(delegate_email, ''), expires_at, used_at
		FROM magic_link_tokens
		WHERE token_hash = $1
	`, hash[:]).Scan(&t.CaptainID, &t.SeasonID, &t.WeekID, &t.DelegateName, &t.DelegateEmail, &expiresAt, &usedAt)
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
