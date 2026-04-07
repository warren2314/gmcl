package email

import (
	"context"
	"fmt"
	"log"
	"net/smtp"
	"os"
	"time"

	"github.com/mailersend/mailersend-go"
)

// Client sends transactional email via MailerSend API or SMTP (Postfix fallback).
type Client struct {
	// MailerSend
	apiKey    string
	fromName  string
	fromEmail string

	// SMTP (Postfix on host)
	smtpHost string
	smtpPort string
	smtpFrom string
}

func NewFromEnv() *Client {
	return &Client{
		apiKey:    os.Getenv("MAILERSEND_API_KEY"),
		fromName:  getEnv("MAILERSEND_FROM_NAME", "GMCL Admin"),
		fromEmail: getEnv("MAILERSEND_FROM_EMAIL", "webmaster@gmcl.co.uk"),

		smtpHost: os.Getenv("SMTP_HOST"),
		smtpPort: getEnv("SMTP_PORT", "25"),
		smtpFrom: getEnv("SMTP_FROM", "webmaster@gmcl.co.uk"),
	}
}

func (c *Client) Send(to, subject, body string) error {
	// Prefer MailerSend if API key is set.
	if c.apiKey != "" {
		return c.sendMailerSend(to, subject, body)
	}

	// Fall back to SMTP (Postfix on host) if configured.
	if c.smtpHost != "" {
		return c.sendSMTP(to, subject, body)
	}

	// Dev fallback — log to stdout.
	log.Printf("[email dev] to=%s subject=%s body=%s", to, subject, body)
	return nil
}

func (c *Client) sendMailerSend(to, subject, body string) error {
	ms := mailersend.NewMailersend(c.apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	message := ms.Email.NewMessage()
	message.SetFrom(mailersend.From{Name: c.fromName, Email: c.fromEmail})
	message.SetRecipients([]mailersend.Recipient{{Email: to}})
	message.SetSubject(subject)
	message.SetText(body)
	message.SetHTML(fmt.Sprintf("<pre style='font-family:monospace'>%s</pre>", body))

	res, err := ms.Email.Send(ctx, message)
	if err != nil {
		return fmt.Errorf("2fa_email_failed: mailersend: %w", err)
	}
	log.Printf("[email] sent via MailerSend to=%s message-id=%s", to, res.Header.Get("X-Message-Id"))
	return nil
}

func (c *Client) sendSMTP(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%s", c.smtpHost, c.smtpPort)
	msg := []byte("From: " + c.smtpFrom + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body + "\r\n")

	// Postfix on localhost requires no auth.
	if err := smtp.SendMail(addr, nil, c.smtpFrom, []string{to}, msg); err != nil {
		return fmt.Errorf("2fa_email_failed: smtp: %w", err)
	}
	log.Printf("[email] sent via SMTP to=%s subject=%q", to, subject)
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
