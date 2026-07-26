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

func TestPortalResponsesAreNotCacheable(t *testing.T) {
	handler := portalNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/portal", nil),
	)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q", got)
	}
	if got := recorder.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("pragma = %q", got)
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

func TestWritePortalNavProvidesKeyboardAndCurrentPageSemantics(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePortalNav(
		recorder,
		"csrf-token",
		portal.Principal{DisplayName: `Alice <Admin>`},
		"/portal/reports",
	)
	body := recorder.Body.String()
	for _, expected := range []string{
		`href="#main-content">Skip to main content</a>`,
		`aria-label="Club portal"`,
		`href="/portal/reports" aria-current="page">Reports</a>`,
		`Alice &lt;Admin&gt;`,
		`name="csrf_token" value="csrf-token"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("portal navigation omitted %q: %s", expected, body)
		}
	}
	if strings.Count(body, `aria-current="page"`) != 1 {
		t.Fatalf("portal navigation current-page count = %d: %s",
			strings.Count(body, `aria-current="page"`),
			body,
		)
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

func TestOptionalPositiveInt32Query(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/portal?season_id=2026", nil)
	value, err := optionalPositiveInt32Query(request, "season_id")
	if err != nil || value == nil || *value != 2026 {
		t.Fatalf("season query = %v, %v", value, err)
	}
	for _, target := range []string{
		"/portal?season_id=-1",
		"/portal?season_id=not-a-number",
		"/portal?season_id=999999999999",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		if _, err := optionalPositiveInt32Query(request, "season_id"); err == nil {
			t.Fatalf("invalid scope query accepted: %s", target)
		}
	}
}

func TestPortalReadScopeQueryEscapesSelectedScope(t *testing.T) {
	teamID := int32(12)
	query := portalReadScopeQuery(portal.ReadScopeSelection{
		SelectedSeasonID: 2026,
		SelectedTeamID:   &teamID,
	})
	if query != "?season_id=2026&team_id=12" {
		t.Fatalf("scope query = %q", query)
	}
}
