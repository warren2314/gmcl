package junior

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"github.com/jackc/pgx/v5"
)

func strictMessageDelivery(ctx context.Context, envelope MailEnvelope) error {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("JUNIOR_OUTBOUND_EMAIL_ENABLED")), "true") || strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" || strings.TrimSpace(os.Getenv("EMAIL_OVERRIDE")) != "" {
		return ErrTransportUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := email.NewJuniorFromEnv()
	if err != nil {
		return ErrTransportUnavailable
	}
	return client.SendSnapshot(envelope.To, envelope.Subject, envelope.Body, envelope.MessageID, nil)
}

func OutboundEmailConfigured() bool {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("JUNIOR_OUTBOUND_EMAIL_ENABLED")), "true") || strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" || strings.TrimSpace(os.Getenv("EMAIL_OVERRIDE")) != "" {
		return false
	}
	_, err := email.NewJuniorFromEnv()
	return err == nil
}

// DispatchMessage is an explicit staff command. No junior sender or poller is
// started implicitly. SMTP acceptance is recorded as accepted, never delivered.
// A stable Message-ID supports provider correlation, not an exactly-once claim.
func (s *Service) DispatchMessage(ctx context.Context, p Principal, messageID int64) error {
	if p.AdminID <= 0 {
		return ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, 0, true); err != nil {
		return err
	}
	if s.Delivery == nil && (!strings.EqualFold(strings.TrimSpace(os.Getenv("JUNIOR_OUTBOUND_EMAIL_ENABLED")), "true") || strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" || strings.TrimSpace(os.Getenv("EMAIL_OVERRIDE")) != "") {
		return ErrTransportUnavailable
	}
	if s.Delivery == nil {
		if _, err = email.NewJuniorFromEnv(); err != nil {
			return ErrTransportUnavailable
		}
	}
	var subject, body string
	var workflow queuedWorkflowMessage
	if err = tx.QueryRow(ctx, `SELECT subject,body,COALESCE(club_id,0),season_id,kind,record_type,record_id,idempotency_key FROM junior_messages WHERE id=$1`, messageID).Scan(&subject, &body, &workflow.ClubID, &workflow.SeasonID, &workflow.Kind, &workflow.RecordType, &workflow.RecordID, &workflow.Key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err = validateQueuedWorkflowMessage(ctx, tx, workflow); err != nil {
		return err
	}
	clubID := workflow.ClubID
	rows, err := tx.Query(ctx, `SELECT id,email FROM junior_message_recipients WHERE message_id=$1 AND (status IN ('queued','failed') OR (status='sending' AND lease_until<now())) ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`, messageID)
	if err != nil {
		return err
	}
	type pending struct {
		id    int64
		email string
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.email); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM junior_message_recipients WHERE message_id=$1`, messageID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("%w: resolve the missing recipients before sending", ErrValidation)
		}
		return nil
	}
	for _, item := range items {
		if _, err = tx.Exec(ctx, `UPDATE junior_message_recipients SET status='sending',attempts=attempts+1,lease_until=now()+interval '10 minutes' WHERE id=$1`, item.id); err != nil {
			return err
		}
	}
	if err = Audit(ctx, tx, p, clubID, "message", messageID, "dispatch_started", map[string]any{"recipients": len(items)}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	deliver := s.Delivery
	if deliver == nil {
		deliver = strictMessageDelivery
	}
	anyFailed := false
	for _, item := range items {
		sendErr := deliver(ctx, MailEnvelope{To: item.email, Subject: subject, Body: body, MessageID: fmt.Sprintf("<junior-%d-%d@gmcl.co.uk>", messageID, item.id)})
		status, failure := "accepted", ""
		if sendErr != nil {
			status, failure = "failed", "SMTP transport did not accept this message"
			anyFailed = true
		}
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		recordTx, err := s.DB.Begin(recordCtx)
		if err != nil {
			cancel()
			return err
		}
		_, err = recordTx.Exec(recordCtx, `UPDATE junior_message_recipients SET status=$2,lease_until=NULL,accepted_at=CASE WHEN $2='accepted' THEN now() ELSE accepted_at END WHERE id=$1 AND status='sending'`, item.id, status)
		if err == nil {
			_, err = recordTx.Exec(recordCtx, `INSERT INTO junior_message_attempts(recipient_id,actor_admin_id,status,failure) VALUES($1,$2,$3,$4)`, item.id, p.AdminID, status, failure)
		}
		if err == nil {
			err = Audit(recordCtx, recordTx, p, clubID, "message", messageID, "transport_"+status, map[string]any{"recipient_id": item.id})
		}
		if err == nil {
			err = recordTx.Commit(recordCtx)
		} else {
			_ = recordTx.Rollback(recordCtx)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	if anyFailed {
		return ErrTransportUnavailable
	}
	return nil
}

func (s *Service) Messages(ctx context.Context, p Principal, clubID int32) ([]MessageSummary, error) {
	return s.MessagesFiltered(ctx, p, MessageFilter{ClubID: clubID})
}

func (s *Service) MessagesFiltered(ctx context.Context, p Principal, filter MessageFilter) ([]MessageSummary, error) {
	clubID := filter.ClubID
	if clubID < 0 || filter.SeasonID < 0 {
		return nil, ErrValidation
	}
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT m.id,m.season_id,COALESCE(m.club_id,0),m.kind,m.record_type,m.record_id,m.subject,m.body,m.created_at,
        (SELECT count(DISTINCT r.id) FROM junior_message_recipients r WHERE r.message_id=m.id AND ($1::integer=0 OR EXISTS(SELECT 1 FROM junior_message_audiences a WHERE a.recipient_id=r.id AND a.club_id=$1))),
        (SELECT count(DISTINCT r.id) FROM junior_message_recipients r WHERE r.message_id=m.id AND r.status='accepted' AND ($1::integer=0 OR EXISTS(SELECT 1 FROM junior_message_audiences a WHERE a.recipient_id=r.id AND a.club_id=$1))),
        (SELECT count(DISTINCT r.id) FROM junior_message_recipients r WHERE r.message_id=m.id AND r.status='failed' AND ($1::integer=0 OR EXISTS(SELECT 1 FROM junior_message_audiences a WHERE a.recipient_id=r.id AND a.club_id=$1)))
        FROM junior_messages m WHERE ($1::integer=0 OR EXISTS(SELECT 1 FROM junior_message_audiences a WHERE a.message_id=m.id AND a.club_id=$1) OR ($2::boolean AND m.club_id=$1))
        AND ($3::bigint=0 OR m.season_id=$3)
        AND (NOT $4::boolean OR NOT EXISTS(SELECT 1 FROM junior_message_recipients r WHERE r.message_id=m.id) OR EXISTS(SELECT 1 FROM junior_message_recipients r WHERE r.message_id=m.id AND r.status='failed' AND ($1::integer=0 OR EXISTS(SELECT 1 FROM junior_message_audiences a WHERE a.recipient_id=r.id AND a.club_id=$1))))
        ORDER BY m.created_at DESC,m.id DESC LIMIT 100`, clubID, p.AdminID > 0 && !p.ReadOnly, filter.SeasonID, filter.ProblemsOnly)
	if err != nil {
		return nil, err
	}
	result := []MessageSummary{}
	for rows.Next() {
		var item MessageSummary
		if err = rows.Scan(&item.ID, &item.SeasonID, &item.ClubID, &item.Kind, &item.RecordType, &item.RecordID, &item.Subject, &item.Body, &item.CreatedAt, &item.RecipientCount, &item.AcceptedCount, &item.FailedCount); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case item.RecipientCount == 0:
			item.Status = "needs_recipients"
		case item.FailedCount > 0:
			item.Status = "failed"
		case item.AcceptedCount == item.RecipientCount:
			item.Status = "accepted"
		default:
			item.Status = "queued"
		}
		result = append(result, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range result {
		rows, err = s.DB.Query(ctx, `SELECT a.club_id,COALESCE(a.contact_id,0),r.email,a.name FROM junior_message_audiences a JOIN junior_message_recipients r ON r.id=a.recipient_id WHERE a.message_id=$1 AND ($2::integer=0 OR a.club_id=$2) ORDER BY a.club_id,r.email,a.id`, result[i].ID, clubID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var recipient Recipient
			if err = rows.Scan(&recipient.ClubID, &recipient.ContactID, &recipient.Email, &recipient.Name); err != nil {
				rows.Close()
				return nil, err
			}
			result[i].Recipients = append(result[i].Recipients, recipient)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Service) ListMessages(ctx context.Context, p Principal, clubID int32) ([]MessageSummary, error) {
	return s.Messages(ctx, p, clubID)
}
