package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/portal"

	"github.com/google/uuid"
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
		`href="/portal/activity">Activity</a>`,
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

func TestWritePortalAppointmentCardsShowsScopedEffectiveAccess(t *testing.T) {
	currentID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	otherID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	start := time.Date(2026, time.April, 1, 9, 30, 0, 0, time.UTC)
	end := time.Date(2026, time.September, 30, 17, 0, 0, 0, time.UTC)
	currentAssignment := portal.Assignment{
		ID:       currentID,
		Role:     portal.RoleClubPrimaryAdmin,
		StartsAt: start,
	}
	recorder := httptest.NewRecorder()
	writePortalAppointmentCards(
		recorder,
		`csrf<&>`,
		portal.Principal{Assignment: &currentAssignment},
		[]portal.ActingContext{
			{
				Assignment: currentAssignment,
				ClubName:   `Alpha <script>`,
			},
			{
				Assignment: portal.Assignment{
					ID:       otherID,
					Role:     portal.RoleCaptainManager,
					StartsAt: start,
					EndsAt:   &end,
				},
				ClubName:        "Beta CC",
				TeamName:        "Second XI",
				SeasonName:      "2026",
				CompetitionName: "Championship",
			},
		},
		time.UTC,
	)
	body := recorder.Body.String()
	for _, expected := range []string{
		`aria-label="Active club appointments"`,
		`Alpha &lt;script&gt;`,
		"Club Primary Administrator",
		"Club-wide",
		"Current role",
		"This appointment is selected for the current session.",
		"Beta CC",
		"Captain or Manager",
		"Second XI · 2026 · Championship",
		`datetime="2026-04-01T09:30:00Z"`,
		`datetime="2026-09-30T17:00:00Z"`,
		`name="csrf_token" value="csrf&lt;&amp;&gt;"`,
		`name="assignment_id" value="22222222-2222-4222-8222-222222222222"`,
		"Switch to this role",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("appointment cards omitted %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{
		`Alpha <script>`,
		`name="assignment_id" value="11111111-1111-4111-8111-111111111111"`,
		"Continue as this role",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("appointment cards included %q: %s", forbidden, body)
		}
	}
	if strings.Count(body, `<form class="mt-auto"`) != 1 {
		t.Fatalf("switch form count = %d: %s", strings.Count(body, `<form class="mt-auto"`), body)
	}
}

func TestPortalAppointmentPeriodHandlesMissingDates(t *testing.T) {
	if got := portalAppointmentPeriod(portal.Assignment{}, nil); got != "Currently active" {
		t.Fatalf("missing period = %q", got)
	}
}

func TestPortalActivityPresentationIsAllowlisted(t *testing.T) {
	label, description := portalActivityPresentation("portal.session.created")
	if label != "Signed in" || !strings.Contains(description, "session") {
		t.Fatalf("known activity = %q, %q", label, description)
	}
	unknown := `<script>alert("internal")</script>`
	label, description = portalActivityPresentation(unknown)
	if label != "Account activity" ||
		strings.Contains(label, unknown) ||
		strings.Contains(description, unknown) {
		t.Fatalf("unknown activity leaked internal value: %q, %q", label, description)
	}
}

func TestPortalActivityOutcomeBadgeIsAllowlisted(t *testing.T) {
	for outcome, expected := range map[string]string{
		"success": "Completed",
		"denied":  "Denied",
		"failure": "Failed",
		"unknown": "Recorded",
	} {
		badge := portalActivityOutcomeBadge(outcome)
		if !strings.Contains(badge, expected) {
			t.Fatalf("outcome %q badge = %q", outcome, badge)
		}
	}
	if badge := portalActivityOutcomeBadge(`<script>`); strings.Contains(badge, "<script>") {
		t.Fatalf("unknown outcome leaked input: %q", badge)
	}
}

func TestPortalActivityNavigationMarksCurrentPage(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePortalNav(
		recorder,
		"csrf-token",
		portal.Principal{DisplayName: "Alice"},
		"/portal/activity",
	)
	body := recorder.Body.String()
	if !strings.Contains(
		body,
		`href="/portal/activity" aria-current="page">Activity</a>`,
	) {
		t.Fatalf("activity navigation was not current: %s", body)
	}
	if strings.Count(body, `aria-current="page"`) != 1 {
		t.Fatalf("current-page count = %d", strings.Count(body, `aria-current="page"`))
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
