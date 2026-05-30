package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMagicLinkEmailBlockUsesConfiguredPublicAndAlternateURLs(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://gmcl.co.uk/")
	t.Setenv("PUBLIC_ALT_BASE_URL", "https://www.gmcl.co.uk/")

	req := httptest.NewRequest("GET", "http://internal/magic-link/request", nil)
	block := magicLinkEmailBlock(req, "abc+123")

	if !strings.Contains(block, "https://gmcl.co.uk/magic-link/confirm?token=abc%2B123") {
		t.Fatalf("primary link missing or not escaped: %s", block)
	}
	if !strings.Contains(block, "BACKUP_URL:https://www.gmcl.co.uk/magic-link/confirm?token=abc%2B123") {
		t.Fatalf("backup link missing or not escaped: %s", block)
	}
}

func TestMagicLinkEmailBlockDerivesWWWBackupForApexHost(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("PUBLIC_ALT_BASE_URL", "")

	req := httptest.NewRequest("GET", "http://gmcl.co.uk/magic-link/request", nil)
	block := magicLinkEmailBlock(req, "token")

	if !strings.Contains(block, "https://gmcl.co.uk/magic-link/confirm?token=token") {
		t.Fatalf("primary link should default to https apex: %s", block)
	}
	if !strings.Contains(block, "BACKUP_URL:https://www.gmcl.co.uk/magic-link/confirm?token=token") {
		t.Fatalf("www backup link missing: %s", block)
	}
}

func TestMagicLinkEmailBlockDoesNotAddBackupForUnrelatedHost(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://admin.example.test")
	t.Setenv("PUBLIC_ALT_BASE_URL", "")

	req := httptest.NewRequest("GET", "http://internal/magic-link/request", nil)
	block := magicLinkEmailBlock(req, "token")

	if strings.Contains(block, "BACKUP_URL:") {
		t.Fatalf("unexpected backup link for unrelated host: %s", block)
	}
}

func TestMagicLinkTokenFromURL(t *testing.T) {
	got := magicLinkTokenFromURL("https://gmcl.co.uk/magic-link/confirm?token=abc123")
	if got != "abc123" {
		t.Fatalf("token: got %q", got)
	}
}

func TestMagicLinkTokenFromNestedTrackingURL(t *testing.T) {
	got := magicLinkTokenFromURL("https://tracker.example/click?u=https%3A%2F%2Fgmcl.co.uk%2Fmagic-link%2Fconfirm%3Ftoken%3Dabc123")
	if got != "abc123" {
		t.Fatalf("token: got %q", got)
	}
}

func TestRedactMagicTokenInText(t *testing.T) {
	got := redactMagicTokenInText("clicked https://gmcl.co.uk/magic-link/confirm?token=abc123&x=1")
	want := "clicked https://gmcl.co.uk/magic-link/confirm?token=[redacted]&x=1"
	if got != want {
		t.Fatalf("redacted: got %q", got)
	}
}
