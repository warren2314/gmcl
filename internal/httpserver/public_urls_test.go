package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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

func TestNotFoundRedirectsLigaturePathPreservingToken(t *testing.T) {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if canonical := canonicalizePath(req.URL.Path); canonical != req.URL.Path {
			target := canonical
			if req.URL.RawQuery != "" {
				target += "?" + req.URL.RawQuery
			}
			http.Redirect(w, req, target, http.StatusFound)
			return
		}
		http.NotFound(w, req)
	})
	r.Get("/magic-link/confirm", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/magic-link/conﬁrm?token=abc%2B123", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/magic-link/confirm?token=abc%2B123" {
		t.Fatalf("redirect location: got %q", loc)
	}
}

func TestCanonicalizePathNormalisesLigatures(t *testing.T) {
	// "conﬁrm" uses the U+FB01 fi ligature that some clients substitute.
	if got := canonicalizePath("/magic-link/conﬁrm"); got != "/magic-link/confirm" {
		t.Fatalf("ligature path not normalised: got %q", got)
	}
	// Canonical ASCII paths must be returned unchanged.
	if got := canonicalizePath("/magic-link/confirm"); got != "/magic-link/confirm" {
		t.Fatalf("canonical path altered: got %q", got)
	}
}

func TestRedactMagicTokenInText(t *testing.T) {
	got := redactMagicTokenInText("clicked https://gmcl.co.uk/magic-link/confirm?token=abc123&x=1")
	want := "clicked https://gmcl.co.uk/magic-link/confirm?token=[redacted]&x=1"
	if got != want {
		t.Fatalf("redacted: got %q", got)
	}
}
