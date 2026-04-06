package leagueapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DetailToJSON returns JSON for the payload column.
func DetailToJSON(d MatchDetail) []byte {
	b, _ := json.Marshal(d)
	return b
}

// ParseMatchDetailsJSON decodes the league API JSON body into MatchDetailsResponse.
func ParseMatchDetailsJSON(body []byte) (*MatchDetailsResponse, error) {
	var r MatchDetailsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ParseMatchDate parses match_date from API (typically dd/MM/yyyy) to a calendar date.
func ParseMatchDate(s, formatHint string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty match date")
	}
	switch formatHint {
	case "dd/MM/yyyy", "":
		t, err := time.Parse("02/01/2006", s)
		if err == nil {
			return t, nil
		}
	}
	// ISO
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Parse("02/01/2006", s)
}

// FormatDateForTemplate formats t for URL templates (default dd/MM/yyyy).
func FormatDateForTemplate(t time.Time, formatHint string) string {
	if formatHint == "2006-01-02" {
		return t.Format("2006-01-02")
	}
	return t.Format("02/01/2006")
}
