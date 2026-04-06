package leagueapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client performs HTTP calls to the league / Play-Cricket API.
type Client struct {
	cfg Config
	hc  *http.Client
}

// NewClient returns a client for league API calls.
func NewClient(cfg Config) *Client {
	return &Client{
		cfg: cfg,
		hc: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// BuildMatchesURL expands BaseURL + MatchesURLTemplate with league id and date.
func (c *Client) BuildMatchesURL(matchDate time.Time) (string, error) {
	if c.cfg.BaseURL == "" {
		return "", fmt.Errorf("PLAY_CRICKET_API_BASE_URL is not set")
	}
	tpl := c.cfg.MatchesURLTemplate
	if !strings.HasPrefix(tpl, "/") {
		tpl = "/" + tpl
	}
	dateStr := FormatDateForTemplate(matchDate, c.cfg.DateFormat)
	tpl = strings.ReplaceAll(tpl, "{leagueId}", url.QueryEscape(c.cfg.LeagueID))
	tpl = strings.ReplaceAll(tpl, "{date}", url.QueryEscape(dateStr))

	u, err := url.Parse(c.cfg.BaseURL + tpl)
	if err != nil {
		return "", err
	}
	if c.cfg.APIKey != "" && c.cfg.AuthQueryParam != "" {
		q := u.Query()
		q.Set(c.cfg.AuthQueryParam, c.cfg.APIKey)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// FetchMatchesForDate GETs match list for a calendar date and returns parsed details.
func (c *Client) FetchMatchesForDate(ctx context.Context, matchDate time.Time) ([]MatchDetail, error) {
	if !c.cfg.Enabled() {
		return nil, fmt.Errorf("league API client not configured (set PLAY_CRICKET_API_BASE_URL and PLAY_CRICKET_API_KEY)")
	}
	urlStr, err := c.BuildMatchesURL(matchDate)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.AuthQueryParam == "" {
		switch {
		case c.cfg.AuthHeader != "":
			req.Header.Set("Authorization", c.cfg.AuthHeader)
		case c.cfg.APIKey != "":
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("league API HTTP %d: %s", resp.StatusCode, truncate(body, 500))
	}
	parsed, err := ParseMatchDetailsJSON(body)
	if err != nil {
		return nil, fmt.Errorf("decode league API JSON: %w", err)
	}
	return parsed.MatchDetails, nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
