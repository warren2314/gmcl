// Package junior owns club junior participation, official actions and access.
// Its teams, seasons and fixtures never enter the senior reporting tables.
package junior

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"cricket-ground-feedback/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrForbidden            = errors.New("junior access forbidden")
	ErrNotFound             = errors.New("junior record not found")
	ErrConflict             = errors.New("junior record changed; reload before continuing")
	ErrValidation           = errors.New("invalid junior information")
	ErrInvalid              = ErrValidation
	ErrTransportUnavailable = errors.New("junior email transport is not enabled or configured")
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// A principal is constructed by staff authentication or a database-backed
// junior session. Public service methods revalidate it; posted IDs confer no access.
type Principal struct {
	AdminID   int32
	SessionID int64
	AccountID int64
	ClubID    int32
	ReadOnly  bool
}

type AccessMailer func(ctx context.Context, to, subject, body string) error

type MailEnvelope struct {
	To, Subject, Body, MessageID string
}

type Service struct {
	DB           *db.Pool
	AccessMailer AccessMailer
	Delivery     func(context.Context, MailEnvelope) error
}

func NewService(pool *db.Pool) *Service { return &Service{DB: pool} }

// Audit records only business metadata, never raw access/session tokens.
func Audit(ctx context.Context, q DBTX, p Principal, clubID int32, entityType string, entityID int64, action string, metadata any) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode junior event: %w", err)
	}
	_, err = q.Exec(ctx, `INSERT INTO junior_events(club_id,actor_admin_id,actor_account_id,actor_session_id,entity_type,entity_id,action,metadata)
		VALUES(NULLIF($1,0),NULLIF($2,0),NULLIF($3,0),NULLIF($4,0),$5,$6,$7,$8::jsonb)`, clubID, p.AdminID, p.AccountID, p.SessionID, entityType, entityID, action, string(encoded))
	return err
}

func CanonicalEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%w: enter one valid email address", ErrValidation)
	}
	return strings.ToLower(address.Address), nil
}

func CSVCell(value string) string {
	trimmed := strings.TrimLeft(value, " ")
	if trimmed != "" && strings.ContainsRune("=+-@\t\r\n＝＋－＠", []rune(trimmed)[0]) {
		return "'" + value
	}
	return value
}
