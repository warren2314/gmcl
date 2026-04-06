package leagueapi

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds Play-Cricket / league API settings (from environment).
// Base URL and key are required for live HTTP fetch; sync can still upsert from JSON via internal endpoint without them.
type Config struct {
	BaseURL  string
	APIKey   string
	LeagueID string
	// MatchesURLTemplate is appended to BaseURL. Placeholders: {leagueId}, {date}
	// Example: /api/v1/matches?league_id={leagueId}&match_date={date}
	MatchesURLTemplate string
	// DateFormat is used for {date}: "dd/MM/yyyy" (API sample) or "2006-01-02"
	DateFormat string
	// AuthQueryParam if set adds &param=key to the request URL (common for Play-Cricket style APIs).
	AuthQueryParam string
	// AuthHeader if non-empty sets Authorization: <value> (e.g. "Bearer xxx" or raw token).
	AuthHeader string
	Timeout    time.Duration
}

// NewConfigFromEnv reads league API env vars. Missing BaseURL or APIKey means HTTP fetch is disabled.
func NewConfigFromEnv() Config {
	timeout := 30 * time.Second
	if s := os.Getenv("PLAY_CRICKET_HTTP_TIMEOUT_SEC"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	df := strings.TrimSpace(os.Getenv("PLAY_CRICKET_DATE_FORMAT"))
	if df == "" {
		df = "dd/MM/yyyy"
	}
	tpl := strings.TrimSpace(os.Getenv("PLAY_CRICKET_MATCHES_URL_TEMPLATE"))
	if tpl == "" {
		tpl = "/?league_id={leagueId}&match_date={date}"
	}
	return Config{
		BaseURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("PLAY_CRICKET_API_BASE_URL")), "/"),
		APIKey:             strings.TrimSpace(os.Getenv("PLAY_CRICKET_API_KEY")),
		LeagueID:           strings.TrimSpace(os.Getenv("PLAY_CRICKET_LEAGUE_ID")),
		MatchesURLTemplate: tpl,
		DateFormat:         df,
		AuthQueryParam:     strings.TrimSpace(os.Getenv("PLAY_CRICKET_AUTH_QUERY_PARAM")),
		AuthHeader:         strings.TrimSpace(os.Getenv("PLAY_CRICKET_AUTH_HEADER")),
		Timeout:            timeout,
	}
}

// Enabled reports whether outbound HTTP to the league API is configured.
func (c Config) Enabled() bool {
	return c.BaseURL != "" && c.APIKey != ""
}
