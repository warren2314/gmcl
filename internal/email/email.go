package email

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mailersend/mailersend-go"
)

// Client sends transactional email via the MailerSend API.
type Client struct {
	apiKey   string
	fromName string
	fromEmail string
}

func NewFromEnv() *Client {
	return &Client{
		apiKey:    os.Getenv("MAILERSEND_API_KEY"),
		fromName:  getEnv("MAILERSEND_FROM_NAME", "GMCL Admin"),
		fromEmail: getEnv("MAILERSEND_FROM_EMAIL", "webmaster@gmcl.co.uk"),
	}
}

func (c *Client) Send(to, subject, body string) error {
	if c.apiKey == "" {
		// Dev fallback — log to stdout instead of sending.
		log.Printf("[email dev] to=%s subject=%s body=%s", to, subject, body)
		return nil
	}

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
		return fmt.Errorf("mailersend: %w", err)
	}

	log.Printf("[email] sent to=%s subject=%q message-id=%s", to, subject, res.Header.Get("X-Message-Id"))
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
