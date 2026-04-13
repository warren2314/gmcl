package leagueapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Config.Enabled
// ---------------------------------------------------------------------------

func TestConfigEnabled_BothSet(t *testing.T) {
	c := Config{BaseURL: "https://api.example.com", APIKey: "secret"}
	if !c.Enabled() {
		t.Error("want enabled when both BaseURL and APIKey are set")
	}
}

func TestConfigEnabled_MissingKey(t *testing.T) {
	c := Config{BaseURL: "https://api.example.com"}
	if c.Enabled() {
		t.Error("want disabled when APIKey is empty")
	}
}

func TestConfigEnabled_MissingBaseURL(t *testing.T) {
	c := Config{APIKey: "secret"}
	if c.Enabled() {
		t.Error("want disabled when BaseURL is empty")
	}
}

func TestConfigEnabled_BothEmpty(t *testing.T) {
	c := Config{}
	if c.Enabled() {
		t.Error("want disabled when both empty")
	}
}

// ---------------------------------------------------------------------------
// BuildMatchesURL
// ---------------------------------------------------------------------------

func TestBuildMatchesURL_Basic(t *testing.T) {
	c := NewClient(Config{
		BaseURL:            "https://play-cricket.example.com",
		APIKey:             "testkey",
		SiteID:             "5501",
		MatchesURLTemplate: "/api/v1/matches?site_id={siteId}&match_date={date}",
		DateFormat:         "dd/MM/yyyy",
	})
	d := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	u, err := c.BuildMatchesURL(d, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := u; len(got) == 0 {
		t.Fatal("empty URL")
	}
	// Check that league ID and date appear in the URL.
	for _, want := range []string{"5501", "05%2F04%2F2025"} {
		if !contains(u, want) {
			t.Errorf("URL %q does not contain %q", u, want)
		}
	}
}

func TestBuildMatchesURL_AuthQueryParam(t *testing.T) {
	c := NewClient(Config{
		BaseURL:            "https://play-cricket.example.com",
		APIKey:             "myapikey",
		SiteID:             "5501",
		MatchesURLTemplate: "/matches",
		AuthQueryParam:     "api_key",
	})
	d := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	u, err := c.BuildMatchesURL(d, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(u, "api_key=myapikey") {
		t.Errorf("URL %q does not contain api_key param", u)
	}
}

func TestBuildMatchesURL_NoLeadingSlashTemplate(t *testing.T) {
	c := NewClient(Config{
		BaseURL:            "https://play-cricket.example.com",
		APIKey:             "k",
		MatchesURLTemplate: "api/v1/matches",
	})
	d := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	u, err := c.BuildMatchesURL(d, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(u, "play-cricket.example.com/api/v1/matches") {
		t.Errorf("URL %q missing expected path", u)
	}
}

func TestBuildMatchesURL_ISODateFormat(t *testing.T) {
	c := NewClient(Config{
		BaseURL:            "https://api.example.com",
		APIKey:             "k",
		MatchesURLTemplate: "/matches?date={date}",
		DateFormat:         "2006-01-02",
	})
	d := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	u, err := c.BuildMatchesURL(d, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(u, "2025-04-05") {
		t.Errorf("URL %q does not contain ISO date", u)
	}
}

func TestBuildMatchesURL_NoBaseURL(t *testing.T) {
	c := NewClient(Config{APIKey: "k"})
	_, err := c.BuildMatchesURL(time.Now(), 0)
	if err == nil {
		t.Fatal("expected error when BaseURL is empty")
	}
}

// ---------------------------------------------------------------------------
// FetchMatchesForDate – mock HTTP server
// ---------------------------------------------------------------------------

func TestFetchMatchesForDate_NotConfigured(t *testing.T) {
	c := NewClient(Config{}) // no BaseURL or APIKey
	_, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected error when client not configured")
	}
}

func TestFetchMatchesForDate_Success_Saturday(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, gmclSaturdayJSON)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	d := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	details, err := c.FetchMatchesForDate(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 3 {
		t.Fatalf("want 3, got %d", len(details))
	}
	// Spot-check first fixture.
	if details[0].MatchID != "90001" {
		t.Errorf("match_id: %q", details[0].MatchID)
	}
	if details[0].HomeTeamID != "10011" {
		t.Errorf("home_team_id: %q", details[0].HomeTeamID)
	}
	if details[0].Umpire1Name != "R. Patel" {
		t.Errorf("umpire1: %q", details[0].Umpire1Name)
	}
}

func TestFetchMatchesForDate_Success_Sunday(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, gmclSundayJSON)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	d := time.Date(2025, 4, 6, 0, 0, 0, 0, time.UTC)
	details, err := c.FetchMatchesForDate(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 2 {
		t.Fatalf("want 2, got %d", len(details))
	}
	if details[1].HomeTeamID != "10081" {
		t.Errorf("home_team_id: %q", details[1].HomeTeamID)
	}
}

func TestFetchMatchesForDate_HTTP400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected error on HTTP 400")
	}
	if !contains(err.Error(), "400") {
		t.Errorf("error should mention 400: %v", err)
	}
}

func TestFetchMatchesForDate_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestFetchMatchesForDate_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `not valid json`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

func TestFetchMatchesForDate_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"match_details":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	details, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 0 {
		t.Fatalf("want 0, got %d", len(details))
	}
}

func TestFetchMatchesForDate_MissingUmpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, gmclMissingUmpiresJSON)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	details, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 {
		t.Fatalf("want 1, got %d", len(details))
	}
	if details[0].Umpire1Name != "" || details[0].Umpire2Name != "" {
		t.Errorf("expected empty umpires, got %q / %q", details[0].Umpire1Name, details[0].Umpire2Name)
	}
}

func TestFetchMatchesForDate_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context cancels.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := newTestClient(srv.URL)
	_, err := c.FetchMatchesForDate(ctx, time.Now())
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestFetchMatchesForDate_AuthHeaderSet(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"match_details":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:            srv.URL,
		APIKey:             "bearer-token-xyz",
		MatchesURLTemplate: "/matches",
		Timeout:            5 * time.Second,
	})
	_, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(gotAuth, "bearer-token-xyz") {
		t.Errorf("Authorization header not set correctly: %q", gotAuth)
	}
}

func TestFetchMatchesForDate_AuthQueryParamSet(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"match_details":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:            srv.URL,
		APIKey:             "my-key",
		AuthQueryParam:     "api_key",
		MatchesURLTemplate: "/matches",
		Timeout:            5 * time.Second,
	})
	_, err := c.FetchMatchesForDate(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(gotURL, "api_key=my-key") {
		t.Errorf("api_key param not in query: %q", gotURL)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestClient returns a Client pointed at the given mock server URL.
func newTestClient(serverURL string) *Client {
	return NewClient(Config{
		BaseURL:            serverURL,
		APIKey:             "testkey",
		SiteID:             "5501",
		MatchesURLTemplate: "/matches",
		Timeout:            5 * time.Second,
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
