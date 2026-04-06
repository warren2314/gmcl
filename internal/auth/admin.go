package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/email"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a plaintext password for admin users.
func HashPassword(plain string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
}

// CheckPassword verifies a plaintext password against a stored hash.
func CheckPassword(hash []byte, plain string) error {
	return bcrypt.CompareHashAndPassword(hash, []byte(plain))
}

// StartAdminLogin verifies username/password and, if valid, creates a 2FA code and emails it.
func StartAdminLogin(ctx context.Context, pool *db.Pool, mailer *email.Client, username, password, ip string) (int32, error) {
	var id int32
	var pwHash []byte
	var emailAddr string
	var isActive bool
	var lockedUntil sql.NullTime

	err := pool.QueryRow(ctx, `
		SELECT id, password_hash, email, is_active, locked_until
		FROM admin_users
		WHERE username = $1
	`, username).Scan(&id, &pwHash, &emailAddr, &isActive, &lockedUntil)
	if err != nil {
		if err == pgx.ErrNoRows {
			// mimic timing and behaviour for non-existent users
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO7O7O7O7O7O7O7O7O7O7O7O7O7O7O"), []byte(password))
			return 0, fmt.Errorf("invalid credentials")
		}
		return 0, err
	}
	if !isActive {
		return 0, fmt.Errorf("account disabled")
	}
	if lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		return 0, fmt.Errorf("account locked")
	}
	if err := CheckPassword(pwHash, password); err != nil {
		_, _ = pool.Exec(ctx, `
			UPDATE admin_users
			SET failed_login_attempts = failed_login_attempts + 1
			WHERE id = $1
		`, id)
		return 0, fmt.Errorf("invalid credentials")
	}

	// reset failure count and lock
	_, _ = pool.Exec(ctx, `
		UPDATE admin_users
		SET failed_login_attempts = 0,
		    locked_until = NULL
		WHERE id = $1
	`, id)

	code := generateNumericCode(6)
	codeHash := sha256.Sum256([]byte(code))
	expires := time.Now().Add(10 * time.Minute)

	_, err = pool.Exec(ctx, `
		INSERT INTO admin_2fa_codes (admin_user_id, code_hash, expires_at, ip_address)
		VALUES ($1, $2, $3, $4)
	`, id, codeHash[:], expires, ip)
	if err != nil {
		return 0, err
	}

	if err := mailer.Send(emailAddr, "Your admin login code", "Your one-time code is: "+code); err != nil {
		return 0, err
	}

	return id, nil
}

// VerifyAdmin2FA verifies a submitted code for an admin user and marks it as used.
func VerifyAdmin2FA(ctx context.Context, pool *db.Pool, adminID int32, code string) error {
	codeHash := sha256.Sum256([]byte(code))

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var storedHash []byte
	var expiresAt time.Time
	var usedAt sql.NullTime

	err = tx.QueryRow(ctx, `
		SELECT code_hash, expires_at, used_at
		FROM admin_2fa_codes
		WHERE admin_user_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, adminID).Scan(&storedHash, &expiresAt, &usedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("no code")
		}
		return err
	}

	if usedAt.Valid {
		return fmt.Errorf("code already used")
	}
	if time.Now().After(expiresAt) {
		return fmt.Errorf("code expired")
	}
	if !hmacEqual(storedHash, codeHash[:]) {
		return fmt.Errorf("invalid code")
	}

	_, err = tx.Exec(ctx, `
		UPDATE admin_2fa_codes
		SET used_at = now()
		WHERE admin_user_id = $1 AND used_at IS NULL
	`, adminID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func generateNumericCode(length int) string {
	if length <= 0 {
		length = 6
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(i)
		}
	}
	for i := range buf {
		buf[i] = '0' + (buf[i] % 10)
	}
	return string(buf)
}

func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var res byte
	for i := range a {
		res |= a[i] ^ b[i]
	}
	return res == 0
}

