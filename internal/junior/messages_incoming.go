package junior

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// InboundProvider is a future provider adapter boundary. There is deliberately
// no configured implementation until mailbox/provider authorisation is supplied.
// A manual record is never represented as a downloaded or synchronised email.
type InboundProvider interface {
	Verify(context.Context) error
	Fetch(context.Context, string) ([]ProviderIncoming, string, error)
}
type ProviderIncoming struct{ ProviderID, From, Subject, Body string }

func (s *Service) RecordIncoming(ctx context.Context, p Principal, clubID int32, from, subject, body string) (int64, error) {
	if p.AdminID <= 0 {
		return 0, ErrForbidden
	}
	canonical, err := CanonicalEmail(from)
	if err != nil {
		return 0, err
	}
	if clubID < 0 || strings.TrimSpace(subject) == "" || len(subject) > 250 || strings.ContainsAny(subject, "\x00\r\n") || strings.TrimSpace(body) == "" || len(body) > 50000 || strings.ContainsRune(body, 0) {
		return 0, ErrValidation
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return 0, err
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO junior_incoming_messages(club_id,from_email,subject,body,recorded_by_admin_id) VALUES(NULLIF($1,0),$2,$3,$4,$5) RETURNING id`, clubID, canonical, subject, body, p.AdminID).Scan(&id); err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, p, clubID, "incoming_message", id, "manually_recorded", map[string]any{"unmatched": clubID == 0}); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) LinkIncomingClub(ctx context.Context, p Principal, incomingID int64, clubID int32) error {
	if p.AdminID <= 0 || clubID <= 0 {
		return ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	var id int64
	if err = tx.QueryRow(ctx, `SELECT id FROM junior_incoming_messages WHERE id=$1 FOR UPDATE`, incomingID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO junior_incoming_club_links(incoming_id,club_id,actor_admin_id) VALUES($1,$2,$3)`, id, clubID, p.AdminID); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, clubID, "incoming_message", id, "club_linked", map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) IncomingMessages(ctx context.Context, p Principal) ([]IncomingMessage, error) {
	return s.IncomingMessagesForClub(ctx, p, 0)
}

func (s *Service) IncomingMessagesForClub(ctx context.Context, p Principal, clubID int32) ([]IncomingMessage, error) {
	if p.AdminID <= 0 || p.ReadOnly {
		return nil, ErrForbidden
	}
	if clubID < 0 {
		return nil, ErrValidation
	}
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `WITH incoming AS (SELECT m.id,COALESCE((SELECT l.club_id FROM junior_incoming_club_links l WHERE l.incoming_id=m.id ORDER BY l.id DESC LIMIT 1),m.club_id,0) AS club_id,m.from_email,m.subject,m.body,m.created_at FROM junior_incoming_messages m) SELECT id,club_id,from_email,subject,body,created_at FROM incoming WHERE ($1::integer=0 OR club_id=$1) ORDER BY created_at DESC,id DESC LIMIT 100`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []IncomingMessage{}
	for rows.Next() {
		var item IncomingMessage
		if err := rows.Scan(&item.ID, &item.ClubID, &item.From, &item.Subject, &item.Body, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.State = "linked"
		if item.ClubID == 0 {
			item.State = "unmatched"
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
