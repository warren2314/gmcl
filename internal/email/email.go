package email

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"html"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client sends transactional email via SMTP.
type Client struct {
	host       string
	port       string
	fromHeader string
	fromAddr   string
	replyTo    string
	username   string
	password   string
	heloDomain string
	configSet  string
}

// Attachment is an immutable email attachment snapshot. Callers are expected
// to load bytes from their audited storage only after validating the manifest.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

const defaultCaptainReportReplyTo = "reports@gtrmcrcricket.co.uk"
const defaultStarredPlayerReplyTo = "joep@gtrmcrcricket.co.uk"

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
		replyTo:    strings.TrimSpace(os.Getenv("SMTP_REPLY_TO")),
		username:   os.Getenv("SMTP_USERNAME"),
		password:   os.Getenv("SMTP_PASSWORD"),
		heloDomain: heloDomain,
		configSet:  strings.TrimSpace(os.Getenv("SES_CONFIGURATION_SET")),
	}
}

// NewCaptainReportFromEnv returns the SMTP client used by captain-report
// messages. Captain replies must go to the reports mailbox even when the
// general transactional reply-to address is configured for another workflow.
func NewCaptainReportFromEnv() *Client {
	client := NewFromEnv()
	replyTo := strings.TrimSpace(os.Getenv("CAPTAIN_REPORT_REPLY_TO"))
	if replyTo == "" {
		replyTo = defaultCaptainReportReplyTo
	}
	client.replyTo = replyTo
	return client
}

// NewStarredPlayerFromEnv returns the SMTP client used by all starred-player
// messages. Replies must go to Joe's monitored mailbox rather than falling
// back to the general webmaster From address.
func NewStarredPlayerFromEnv() *Client {
	client := NewFromEnv()
	replyTo := strings.TrimSpace(os.Getenv("STARRED_PLAYER_REPLY_TO"))
	if replyTo == "" {
		replyTo = defaultStarredPlayerReplyTo
	}
	client.replyTo = replyTo
	return client
}

func (c *Client) Send(to, subject, body string) error {
	return c.SendWithAttachments(to, subject, body, nil)
}

// SendWithAttachments sends a transactional message with optional MIME
// attachments. The envelope still has exactly one recipient; multi-recipient
// workflows create one immutable outbox row per recipient.
func (c *Client) SendWithAttachments(to, subject, body string, attachments []Attachment) error {
	return c.sendWithMessageID(to, subject, body, attachments, "")
}

// SendSnapshot sends an immutable outbox message with a stable RFC Message-ID
// so retries and provider bounce/complaint events can be correlated.
func (c *Client) SendSnapshot(to, subject, body, messageID string, attachments []Attachment) error {
	return c.sendWithMessageID(to, subject, body, attachments, messageID)
}

func (c *Client) sendWithMessageID(to, subject, body string, attachments []Attachment, messageID string) error {
	if override := os.Getenv("EMAIL_OVERRIDE"); override != "" {
		log.Printf("[email override] original_to=%s redirecting_to=%s subject=%s", to, override, subject)
		to = override
	}
	recipient, err := parseSingleRecipient(to)
	if err != nil {
		return fmt.Errorf("email_failed: invalid recipient: %w", err)
	}
	message, err := c.buildMessageWithID(recipient.String(), subject, body, attachments, messageID)
	if err != nil {
		return err
	}
	if c.host == "" {
		log.Printf("[email dev] to=%s subject=%s body=%s", to, subject, body)
		return nil
	}

	addr := net.JoinHostPort(c.host, c.port)
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
	if err := client.Rcpt(recipient.Address); err != nil {
		return fmt.Errorf("2fa_email_failed: RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("2fa_email_failed: DATA: %w", err)
	}

	if _, err := w.Write(message); err != nil {
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

func (c *Client) buildMessage(to, subject, body string, attachments []Attachment) ([]byte, error) {
	return c.buildMessageWithID(to, subject, body, attachments, "")
}

func (c *Client) buildMessageWithID(to, subject, body string, attachments []Attachment, messageID string) ([]byte, error) {
	headers, err := c.messageHeadersWithID(to, subject, messageID)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(headers)
	if c.configSet != "" {
		if hasHeaderControl(c.configSet) {
			return nil, fmt.Errorf("email_failed: invalid SES configuration-set header")
		}
		out.WriteString("X-SES-CONFIGURATION-SET: " + c.configSet + "\r\n")
	}
	out.WriteString("MIME-Version: 1.0\r\n")
	if len(attachments) == 0 {
		out.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		out.WriteString(toHTML(body))
		out.WriteString("\r\n")
		return out.Bytes(), nil
	}

	var mimeBody bytes.Buffer
	writer := multipart.NewWriter(&mimeBody)
	boundary := writer.Boundary()
	out.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")

	htmlHeader := make(textproto.MIMEHeader)
	htmlHeader.Set("Content-Type", "text/html; charset=UTF-8")
	htmlHeader.Set("Content-Transfer-Encoding", "8bit")
	htmlPart, err := writer.CreatePart(htmlHeader)
	if err != nil {
		return nil, err
	}
	if _, err = htmlPart.Write([]byte(toHTML(body))); err != nil {
		return nil, err
	}
	for _, attachment := range attachments {
		filename := filepath.Base(strings.TrimSpace(attachment.Filename))
		if filename == "" || filename == "." {
			filename = "attachment"
		}
		if hasHeaderControl(filename) {
			return nil, fmt.Errorf("email_failed: attachment filename contains control characters")
		}
		contentType := strings.TrimSpace(attachment.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		parsedContentType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || hasHeaderControl(parsedContentType) || !validMediaType(parsedContentType) {
			return nil, fmt.Errorf("email_failed: invalid attachment content type %q", contentType)
		}
		contentType = parsedContentType
		header := make(textproto.MIMEHeader)
		header.Set("Content-Type", contentType+"; name="+mime.QEncoding.Encode("UTF-8", filename))
		header.Set("Content-Disposition", "attachment; filename="+mime.QEncoding.Encode("UTF-8", filename))
		header.Set("Content-Transfer-Encoding", "base64")
		part, createErr := writer.CreatePart(header)
		if createErr != nil {
			return nil, createErr
		}
		encoder := base64.NewEncoder(base64.StdEncoding, newBase64LineWriter(part))
		if _, err = encoder.Write(attachment.Data); err != nil {
			return nil, err
		}
		if err = encoder.Close(); err != nil {
			return nil, err
		}
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	out.Write(mimeBody.Bytes())
	return out.Bytes(), nil
}

type base64LineWriter struct {
	w       interface{ Write([]byte) (int, error) }
	lineLen int
}

func newBase64LineWriter(w interface{ Write([]byte) (int, error) }) *base64LineWriter {
	return &base64LineWriter{w: w}
}

func (w *base64LineWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		remaining := 76 - w.lineLen
		if remaining == 0 {
			if _, err := w.w.Write([]byte("\r\n")); err != nil {
				return written, err
			}
			w.lineLen = 0
			remaining = 76
		}
		n := remaining
		if n > len(p) {
			n = len(p)
		}
		if _, err := w.w.Write(p[:n]); err != nil {
			return written, err
		}
		written += n
		w.lineLen += n
		p = p[n:]
	}
	return written, nil
}

func (c *Client) messageHeaders(to, subject string) (string, error) {
	return c.messageHeadersWithID(to, subject, "")
}

func (c *Client) messageHeadersWithID(to, subject, messageID string) (string, error) {
	if hasHeaderControl(c.fromHeader) || hasHeaderControl(subject) {
		return "", fmt.Errorf("email_failed: header contains control characters")
	}
	if _, err := mail.ParseAddress(c.fromHeader); err != nil {
		return "", fmt.Errorf("email_failed: invalid From address: %w", err)
	}
	recipient, err := parseSingleRecipient(to)
	if err != nil {
		return "", fmt.Errorf("email_failed: invalid recipient: %w", err)
	}
	encodedSubject := subject
	if containsNonASCII(subject) {
		encodedSubject = mime.QEncoding.Encode("UTF-8", subject)
	}
	recipientHeader := recipient.Address
	if strings.TrimSpace(recipient.Name) != "" {
		recipientHeader = recipient.String()
	}
	headers := "From: " + c.fromHeader + "\r\n" +
		"To: " + recipientHeader + "\r\n" +
		"Subject: " + encodedSubject + "\r\n"
	if messageID != "" {
		if !validMessageID(messageID) {
			return "", fmt.Errorf("email_failed: invalid Message-ID")
		}
		headers += "Message-ID: " + messageID + "\r\n"
	}
	if c.replyTo != "" {
		if hasHeaderControl(c.replyTo) {
			return "", fmt.Errorf("email_failed: invalid SMTP_REPLY_TO")
		}
		replyTo, err := mail.ParseAddress(c.replyTo)
		if err != nil {
			return "", fmt.Errorf("2fa_email_failed: invalid SMTP_REPLY_TO: %w", err)
		}
		headers += "Reply-To: " + replyTo.String() + "\r\n"
	}
	return headers, nil
}

func parseSingleRecipient(value string) (*mail.Address, error) {
	if hasHeaderControl(value) {
		return nil, fmt.Errorf("address contains control characters")
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(parsed.Address) == "" || strings.ContainsAny(parsed.Address, "\r\n,;") {
		return nil, fmt.Errorf("exactly one recipient is required")
	}
	return parsed, nil
}

func hasHeaderControl(value string) bool {
	for _, r := range value {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

func containsNonASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return true
		}
	}
	return false
}

func validMessageID(value string) bool {
	if len(value) < 5 || len(value) > 254 || hasHeaderControl(value) || !strings.HasPrefix(value, "<") || !strings.HasSuffix(value, ">") {
		return false
	}
	inner := value[1 : len(value)-1]
	return strings.Count(inner, "@") == 1 && !strings.ContainsAny(inner, " <>(),;:\\[]")
}

func validMediaType(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && validMIMEToken(parts[0]) && validMIMEToken(parts[1])
}

func validMIMEToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("!#$&^_.+-", r) {
			continue
		}
		return false
	}
	return true
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
