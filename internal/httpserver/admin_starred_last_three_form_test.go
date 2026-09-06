package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseStarredFindingFormAcceptsLastThreeRule(t *testing.T) {
	form := url.Values{
		"season":     {"2026"},
		"match_id":   {"7460280"},
		"player_id":  {"123"},
		"club_key":   {"boltondeaneandderby"},
		"player_key": {"testplayer"},
		"list_type":  {"Last 3"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/starred-players/findings/create-case", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	_, _, _, _, _, listType, err := parseStarredFindingForm(request)
	if err != nil {
		t.Fatalf("parseStarredFindingForm returned error: %v", err)
	}
	if listType != "Last 3" {
		t.Fatalf("listType=%q want %q", listType, "Last 3")
	}
}

func TestParseStarredFindingFormRejectsUnknownRuleType(t *testing.T) {
	form := url.Values{
		"season":     {"2026"},
		"match_id":   {"7460280"},
		"player_id":  {"123"},
		"club_key":   {"boltondeaneandderby"},
		"player_key": {"testplayer"},
		"list_type":  {"unknown"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/starred-players/findings/create-case", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if _, _, _, _, _, _, err := parseStarredFindingForm(request); err == nil {
		t.Fatal("unknown rule type was accepted")
	}
}
