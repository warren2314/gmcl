package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/portal"
)

func TestPortalSessionCookieSecurity(t *testing.T) {
	recorder := httptest.NewRecorder()
	expiry := time.Now().Add(time.Hour)
	setPortalSessionCookie(recorder, "opaque-token", expiry)

	response := recorder.Result()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != portalSessionCookie || cookie.Value != "opaque-token" {
		t.Fatalf("unexpected cookie: %#v", cookie)
	}
	if cookie.Path != "/portal" || !cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("insecure portal cookie: %#v", cookie)
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("same-site mode = %v", cookie.SameSite)
	}
}

func TestClearPortalSessionCookie(t *testing.T) {
	recorder := httptest.NewRecorder()
	clearPortalSessionCookie(recorder)
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("portal cookie was not cleared: %#v", cookies)
	}
}

func TestPortalClientDetailsDropsInvalidIP(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/portal", nil)
	request.RemoteAddr = "not-an-ip"
	request.Header.Set("User-Agent", "GMCL portal test")
	details := portalClientDetails(request)
	if details.IPAddress != "" {
		t.Fatalf("invalid IP retained: %q", details.IPAddress)
	}
	if details.UserAgent != "GMCL portal test" {
		t.Fatalf("user agent = %q", details.UserAgent)
	}
}

func TestHumanPortalRoleDoesNotElevateUnknownRole(t *testing.T) {
	if got := humanPortalRole(portal.RoleClubSecretary); got != "Club Secretary" {
		t.Fatalf("role label = %q", got)
	}
	if got := humanPortalRole(portal.RoleKey("idp-admin")); got != "Unknown role" {
		t.Fatalf("unknown role label = %q", got)
	}
}

func TestRenderPortalSignInFailedIsGeneric(t *testing.T) {
	recorder := httptest.NewRecorder()
	renderPortalSignInFailed(recorder)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, secretTerm := range []string{"subject", "issuer", "token_hash", "SQL"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(secretTerm)) {
			t.Fatalf("generic failure page disclosed %q", secretTerm)
		}
	}
}

func TestPortalStatusBadgeHandlesUnknownStatus(t *testing.T) {
	if badge := portalStatusBadge(""); !strings.Contains(badge, "Unknown") {
		t.Fatalf("empty status badge = %q", badge)
	}
	if badge := portalStatusBadge(`<script>alert(1)</script>`); strings.Contains(badge, "<script>") {
		t.Fatalf("status badge did not escape input: %q", badge)
	}
}
