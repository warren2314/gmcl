package email

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Client sends transactional email via SMTP.
type Client struct {
	host       string
	port       string
	fromHeader string
	fromAddr   string
	username   string
	password   string
	heloDomain string
}

func NewFromEnv() *Client {
	fromHeader := getEnv("SMTP_FROM", "webmaster@gmcl.co.uk")
	fromAddr := fromHeader
	if parsed, err := mail.ParseAddress(fromHeader); err == nil {
		fromAddr = parsed.Address
	}

	heloDomain := "gmcl.co.uk"
	if parts := strings.SplitN(fromAddr, "@", 2); len(parts) == 2 {
		heloDomain = parts[1]
	}

	return &Client{
		host:       os.Getenv("SMTP_HOST"),
		port:       getEnv("SMTP_PORT", "25"),
		fromHeader: fromHeader,
		fromAddr:   fromAddr,
		username:   os.Getenv("SMTP_USERNAME"),
		password:   os.Getenv("SMTP_PASSWORD"),
		heloDomain: heloDomain,
	}
}

func (c *Client) Send(to, subject, body string) error {
	if c.host == "" {
		log.Printf("[email dev] to=%s subject=%s body=%s", to, subject, body)
		return nil
	}

	addr := fmt.Sprintf("%s:%s", c.host, c.port)
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

	if err := client.Hello(c.heloDomain); err != nil {
		return fmt.Errorf("2fa_email_failed: EHLO: %w", err)
	}

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName: c.host,
			MinVersion: tls.VersionTLS12,
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("2fa_email_failed: STARTTLS: %w", err)
		}
	} else if c.username != "" || c.password != "" {
		return fmt.Errorf("2fa_email_failed: server does not support STARTTLS")
	}

	if c.username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return fmt.Errorf("2fa_email_failed: server does not support AUTH")
		}
		auth := smtp.PlainAuth("", c.username, c.password, c.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("2fa_email_failed: AUTH: %w", err)
		}
	}

	if err := client.Mail(c.fromAddr); err != nil {
		return fmt.Errorf("2fa_email_failed: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("2fa_email_failed: RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("2fa_email_failed: DATA: %w", err)
	}

	msg := "From: " + c.fromHeader + "\r\n" +
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

	if err := client.Quit(); err != nil {
		return fmt.Errorf("2fa_email_failed: quit: %w", err)
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
