package email

import (
	"testing"
	"time"
)

func TestJuniorMailHasExplicitSenderAndReplyMailbox(t *testing.T) {
	t.Setenv("SMTP_HOST", "smtp.example.invalid")
	t.Setenv("SMTP_FROM", "Senior Desk <senior@example.invalid>")
	t.Setenv("SMTP_REPLY_TO", "senior-replies@example.invalid")
	t.Setenv("JUNIOR_SMTP_FROM", "")
	if _, err := NewJuniorFromEnv(); err == nil {
		t.Fatal("missing junior sender silently used global sender")
	}
	t.Setenv("JUNIOR_SMTP_FROM", "GMCL Juniors <juniors@example.invalid>")
	t.Setenv("JUNIOR_REPLY_TO", "")
	client, err := NewJuniorFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if client.fromAddr != "juniors@example.invalid" || client.replyTo != "juniors@example.invalid" || client.timeout != 30*time.Second || !client.requireTLS {
		t.Fatal("junior sender/reply/deadline/TLS configuration is incorrect")
	}
	if senior := NewFromEnv(); senior.fromAddr != "senior@example.invalid" || senior.replyTo != "senior-replies@example.invalid" || senior.timeout != 0 {
		t.Fatal("junior factory changed global delivery")
	}
	t.Setenv("JUNIOR_REPLY_TO", "Junior Replies <reply@example.invalid>")
	client, err = NewJuniorFromEnv()
	if err != nil || client.replyTo != "Junior Replies <reply@example.invalid>" {
		t.Fatalf("explicit junior reply: %v", err)
	}
	t.Setenv("JUNIOR_REPLY_TO", "reply@example.invalid\r\nBcc: unwanted@example.invalid")
	if _, err := NewJuniorFromEnv(); err == nil {
		t.Fatal("header injection accepted")
	}
	t.Setenv("JUNIOR_REPLY_TO", "")
	t.Setenv("SMTP_HOST", "")
	if _, err := NewJuniorFromEnv(); err == nil {
		t.Fatal("missing SMTP accepted dev fallback")
	}
}
