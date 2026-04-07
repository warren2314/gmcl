package email

import (
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"time"
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
		log.Printf("[email dev] to=%s subject=%s body=%s", to, subject, body)
		return nil
	}

	addr := fmt.Sprintf("%s:%s", c.host, c.port)

	// Dial with a timeout so we don't hang forever if Postfix is unreachable.
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("2fa_email_failed: dial %s: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, c.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("2fa_email_failed: smtp client: %w", err)
	}
	defer client.Close()

	if err := client.Mail(c.from); err != nil {
		return fmt.Errorf("2fa_email_failed: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("2fa_email_failed: RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("2fa_email_failed: DATA: %w", err)
	}

	msg := "From: " + c.from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body + "\r\n"

	if _, err := fmt.Fprint(w, msg); err != nil {
		return fmt.Errorf("2fa_email_failed: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("2fa_email_failed: close data: %w", err)
	}

	client.Quit()
	log.Printf("[email] sent to=%s subject=%q", to, subject)
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
