package junior

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
)

const accessDeliveryRecordTimeout = 5 * time.Second

func PortalEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("JUNIOR_PORTAL_ENABLED")), "true")
}

// Access links must use an operator-configured HTTPS origin, never the incoming
// Host/Forwarded headers. Tokens remain ephemeral and are not put in an outbox.
func accessBaseURL() (string, error) {
	value := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
	if value == "" {
		value = strings.TrimSpace(os.Getenv("APP_BASE_URL"))
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", ErrTransportUnavailable
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func strictAccessMailer(ctx context.Context, to, subject, body string) error {
	// The shared SMTP client's development fallback logs full bodies. Auth
	// mail must never reach that fallback or an address override.
	if !PortalEnabled() || !strings.EqualFold(strings.TrimSpace(os.Getenv("JUNIOR_OUTBOUND_EMAIL_ENABLED")), "true") || strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" || strings.TrimSpace(os.Getenv("EMAIL_OVERRIDE")) != "" {
		return ErrTransportUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := email.NewJuniorFromEnv()
	if err != nil {
		return ErrTransportUnavailable
	}
	return client.Send(to, subject, body)
}

func (s *Service) accessDeliveryReady() error {
	if !PortalEnabled() {
		return ErrTransportUnavailable
	}
	if _, err := accessBaseURL(); err != nil {
		return err
	}
	if s.AccessMailer == nil && (!strings.EqualFold(strings.TrimSpace(os.Getenv("JUNIOR_OUTBOUND_EMAIL_ENABLED")), "true") || strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" || strings.TrimSpace(os.Getenv("EMAIL_OVERRIDE")) != "") {
		return ErrTransportUnavailable
	}
	if s.AccessMailer == nil {
		if _, err := email.NewJuniorFromEnv(); err != nil {
			return ErrTransportUnavailable
		}
	}
	return nil
}

func (s *Service) deliverAccess(ctx context.Context, tokenID int64, to, subject, raw string, clubID int32) error {
	base, deliveryErr := accessBaseURL()
	var clubName string
	if deliveryErr == nil {
		deliveryErr = s.DB.QueryRow(ctx, `SELECT name FROM clubs WHERE id=$1`, clubID).Scan(&clubName)
	}
	if deliveryErr == nil {
		mailer := s.AccessMailer
		if mailer == nil {
			mailer = strictAccessMailer
		}
		body := fmt.Sprintf("GMCL junior portal — %s\n\n%s\n\nOpen the link below, then confirm to continue. It can be used once. Never forward it.\n\n%s/junior/access?token=%s\n\nIf you were not expecting this message, ignore it and contact your league administrator.\n", clubName, subject, base, url.QueryEscape(raw))
		deliveryErr = mailer(ctx, to, subject, body)
	}
	state := "sent"
	if deliveryErr != nil {
		state = "failed"
	}
	// Record delivery accurately, even after a request cancellation. This never
	// sends another message and never stores the raw token or SMTP error text.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessDeliveryRecordTimeout)
	defer cancel()
	tx, err := s.DB.Begin(recordCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(recordCtx)
	if _, err = tx.Exec(recordCtx, `UPDATE junior_access_tokens SET delivery_state=$2,revoked_at=CASE WHEN $2='failed' AND consumed_at IS NULL THEN now() ELSE revoked_at END WHERE id=$1`, tokenID, state); err != nil {
		return err
	}
	if err = Audit(recordCtx, tx, Principal{}, clubID, "access_token", tokenID, "delivery_"+state, map[string]any{}); err != nil {
		return err
	}
	if err = tx.Commit(recordCtx); err != nil {
		return err
	}
	if deliveryErr != nil {
		return ErrTransportUnavailable
	}
	return nil
}
