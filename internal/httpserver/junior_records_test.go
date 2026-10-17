package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"cricket-ground-feedback/internal/junior"
)

func TestJuniorContactSeasonSelectorPreservesOnlyPreviewView(t *testing.T) {
	seasons := []junior.Season{{ID: 1, Name: "Previous", State: "closed"}, {ID: 2, Name: "Next", State: "open"}}
	request := httptest.NewRequest("GET", "/admin/juniors/clubs/10/preview?season_id=1&view=contacts&action=delete&club_id=99&assignment_id=44", nil)
	response := httptest.NewRecorder()
	writeJuniorRecordSeasonSelector(response, request, seasons, 1)
	body := response.Body.String()
	for _, want := range []string{`action="/admin/juniors/clubs/10/preview"`, `name="view" value="contacts"`, `<option value="1" selected>`} {
		if !strings.Contains(body, want) {
			t.Errorf("selector missing %s", want)
		}
	}
	for _, absent := range []string{`name="action"`, `name="club_id"`, `name="assignment_id"`} {
		if strings.Contains(body, absent) {
			t.Errorf("selector carried unrelated state: %s", absent)
		}
	}
	response = httptest.NewRecorder()
	writeJuniorRecordSeasonSelector(response, httptest.NewRequest("GET", "/junior/contacts?view=contacts", nil), seasons, 2)
	if strings.Contains(response.Body.String(), `name="view"`) {
		t.Fatal("ordinary club contact page acquired preview query")
	}
}
