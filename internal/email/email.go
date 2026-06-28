package email

import (
	"crypto/tls"
	"fmt"
	"html"
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
	configSet  string
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
		configSet:  strings.TrimSpace(os.Getenv("SES_CONFIGURATION_SET")),
	}
}

func (c *Client) Send(to, subject, body string) error {
	if override := os.Getenv("EMAIL_OVERRIDE"); override != "" {
		log.Printf("[email override] original_to=%s redirecting_to=%s subject=%s", to, override, subject)
		to = override
	}
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
		"Subject: " + subject + "\r\n"
	if c.configSet != "" {
		msg += "X-SES-CONFIGURATION-SET: " + c.configSet + "\r\n"
	}
	msg +=
		"MIME-Version: 1.0\r\n" +
			"Content-Type: text/html; charset=UTF-8\r\n" +
			"\r\n" +
			toHTML(body) + "\r\n"

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

// toHTML converts a plain-text email body to HTML. Lines that are a bare
// https:// URL are replaced with a styled button so the link is never
// split across lines by an SMTP relay or email client.
func toHTML(body string) string {
	lines := strings.Split(html.EscapeString(body), "\n")
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body style="font-family:Arial,sans-serif;font-size:15px;line-height:1.6;color:#333;max-width:600px;margin:0 auto;padding:20px">`)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "BUTTON_URL:") {
			linkURL := strings.TrimSpace(strings.TrimPrefix(trimmed, "BUTTON_URL:"))
			fmt.Fprintf(&b,
				`<p><a href="%s" style="display:inline-block;padding:12px 24px;background:#cc0000;color:#ffffff;text-decoration:none;border-radius:4px;font-weight:bold">Open secure form</a></p>`,
				linkURL)
		} else if strings.HasPrefix(trimmed, "BACKUP_URL:") {
			backupURL := strings.TrimSpace(strings.TrimPrefix(trimmed, "BACKUP_URL:"))
			fmt.Fprintf(&b,
				`<p style="word-break:break-all;font-size:13px;color:#555"><strong>Backup link:</strong> <a href="%s" style="color:#cc0000">%s</a></p>`,
				backupURL, backupURL)
		} else if strings.HasPrefix(trimmed, "ACCESS_URL:") {
			accessURL := strings.TrimSpace(strings.TrimPrefix(trimmed, "ACCESS_URL:"))
			fmt.Fprintf(&b,
				`<p style="font-size:13px;color:#555"><strong>Manual access page:</strong> <a href="%s" style="color:#cc0000">%s</a></p>`,
				accessURL, accessURL)
		} else if strings.HasPrefix(trimmed, "ACCESS_CODE:") {
			code := strings.TrimSpace(strings.TrimPrefix(trimmed, "ACCESS_CODE:"))
			fmt.Fprintf(&b,
				`<p style="font-size:13px;color:#555;margin-bottom:6px"><strong>Access code:</strong></p><pre style="white-space:pre-wrap;word-break:break-all;background:#f6f6f6;border:1px solid #ddd;border-radius:4px;padding:12px;font-size:14px;color:#111">%s</pre>`,
				code)
		} else if strings.HasPrefix(trimmed, "https://") {
			fmt.Fprintf(&b,
				`<p><a href="%s" style="display:inline-block;padding:12px 24px;background:#cc0000;color:#ffffff;text-decoration:none;border-radius:4px;font-weight:bold">Open link</a></p>`+
					`<p style="word-break:break-all;font-size:12px;color:#666">%s</p>`,
				trimmed, trimmed)
		} else if strings.HasPrefix(trimmed, "CODE:") {
			code := strings.TrimSpace(strings.TrimPrefix(trimmed, "CODE:"))
			fmt.Fprintf(&b,
				`<p style="text-align:center"><span style="display:inline-block;padding:16px 32px;background:#f4f4f4;border:2px solid #ccc;border-radius:6px;font-size:32px;font-weight:bold;letter-spacing:8px;color:#111;font-family:monospace">%s</span></p>`,
				code)
		} else if strings.HasPrefix(trimmed, "NOTE:") {
			msg := strings.TrimSpace(strings.TrimPrefix(trimmed, "NOTE:"))
			fmt.Fprintf(&b,
				`<p style="background:#fff3cd;border-left:4px solid #cc0000;padding:12px 16px;border-radius:4px;font-size:14px;color:#333">%s</p>`,
				msg)
		} else if trimmed == "" {
			b.WriteString(`<br>`)
		} else {
			b.WriteString(`<p>` + line + `</p>`)
		}
	}
	b.WriteString(`</body></html>`)
	return b.String()
}
