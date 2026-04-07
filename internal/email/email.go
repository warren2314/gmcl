package email

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
)

// Client sends transactional email via SMTP (Postfix on the host droplet).
type Client struct {
	host string
	port string
	from string
}

func NewFromEnv() *Client {
	return &Client{
		host: os.Getenv("SMTP_HOST"),
		port: getEnv("SMTP_PORT", "25"),
		from: getEnv("SMTP_FROM", "webmaster@gmcl.co.uk"),
	}
}

func (c *Client) Send(to, subject, body string) error {
	if c.host == "" {
		// Dev fallback — log to stdout when SMTP_HOST is not set.
		log.Printf("[email dev] to=%s subject=%s body=%s", to, subject, body)
		return nil
	}

	addr := fmt.Sprintf("%s:%s", c.host, c.port)
	msg := []byte("From: " + c.from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body + "\r\n")

	// Postfix on localhost requires no auth.
	if err := smtp.SendMail(addr, nil, c.from, []string{to}, msg); err != nil {
		return fmt.Errorf("2fa_email_failed: %w", err)
	}
	log.Printf("[email] sent to=%s subject=%q", to, subject)
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
