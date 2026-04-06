package email

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
)

// Client is a minimal SMTP email client.
type Client struct {
	host     string
	port     string
	username string
	password string
	from     string
}

func NewFromEnv() *Client {
	return &Client{
		host:     os.Getenv("SMTP_HOST"),
		port:     os.Getenv("SMTP_PORT"),
		username: os.Getenv("SMTP_USERNAME"),
		password: os.Getenv("SMTP_PASSWORD"),
		from:     os.Getenv("SMTP_FROM"),
	}
}

func (c *Client) Send(to, subject, body string) error {
	if c.host == "" || c.port == "" || c.from == "" {
		// For local dev, fall back to logging.
		log.Printf("email send (dev fallback) to=%s subject=%s body=%s", to, subject, body)
		return nil
	}

	addr := fmt.Sprintf("%s:%s", c.host, c.port)
	auth := smtp.PlainAuth("", c.username, c.password, c.host)
	msg := []byte("To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body + "\r\n")

	return smtp.SendMail(addr, auth, c.from, []string{to}, msg)
}

