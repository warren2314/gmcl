package httpserver

import (
	"encoding/csv"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/junior"
)

func juniorBuilderRequest(values url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/admin/juniors/forms/new", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	return r
}
func TestJuniorFormBuilderParsesQuestionsAndTargets(t *testing.T) {
	values := url.Values{"title": {"Availability"}, "season_id": {"7"}, "due_date": {"2026-10-31"}, "requires_review": {"1"}, "field_label_1": {"Can you attend?"}, "field_type_1": {"yes_no"}, "field_required_1": {"1"}, "field_label_2": {"Age group"}, "field_type_2": {"select"}, "field_options_2": {"U9\r\nU11\nU13"}, "club_id": {"12", "12", "14"}}
	input, err := juniorParseFormBuilder(juniorBuilderRequest(values), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Fields) != 2 || input.Fields[0].Key != "question_1" || !input.Fields[0].Required || len(input.Fields[1].Options) != 3 || len(input.ClubIDs) != 2 || !input.RequiresReview || input.SeasonID != 7 {
		t.Fatalf("builder contract: %+v", input)
	}
	for _, bad := range []struct{ key, value string }{{"field_label_11", "Unexpected"}, {"field_type_1", "file"}, {"field_key_2", "question_1"}, {"field_options_2", "U9\nU9"}, {"club_id", "-1"}, {"due_date", "not a date"}} {
		t.Run(bad.key+bad.value, func(t *testing.T) {
			copy := url.Values{}
			for key, items := range values {
				copy[key] = append([]string{}, items...)
			}
			copy.Set(bad.key, bad.value)
			if _, err := juniorParseFormBuilder(juniorBuilderRequest(copy), 0); !errors.Is(err, junior.ErrValidation) {
				t.Fatalf("invalid builder accepted: %v", err)
			}
		})
	}
}
func TestJuniorFormResponseHTMLAndFilteredCSV(t *testing.T) {
	field := junior.FormField{Key: "availability", Label: `<script>label</script>`, Type: "select", Required: true, Options: []string{`<unsafe>`, `Yes`}}
	w := httptest.NewRecorder()
	juniorRenderFormAnswer(w, field, `<unsafe>`)
	html := w.Body.String()
	if strings.Contains(html, "<script>") || strings.Contains(html, "<unsafe>") || !strings.Contains(html, "required") || !strings.Contains(html, "selected") {
		t.Fatalf("unsafe or unusable answer HTML: %s", html)
	}
	items := []junior.FormAssignment{{ID: 1, Title: "Availability", Club: "A", Status: "submitted", Fields: []junior.FormField{field}, Values: map[string]string{"availability": "draft"}, SubmittedValues: map[string]string{"availability": "=HYPERLINK(\"x\")"}, DueDate: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}, {ID: 2, Title: "Availability", Club: "B", Status: "assigned", Fields: []junior.FormField{field}}}
	filtered := juniorFilterFormAssignments(items, "submitted", "a")
	if len(filtered) != 1 || filtered[0].ID != 1 {
		t.Fatal("filter/export population differs")
	}
	w = httptest.NewRecorder()
	juniorFixtureCSV(w, "junior-form-responses.csv", juniorFormCSVRows(filtered))
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][9] != "'=HYPERLINK(\"x\")" || rows[1][10] != "draft" {
		t.Fatalf("submitted/draft separation or formula guard lost: %v", rows)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response export can be cached")
	}
}
func TestJuniorFormBuilderRetainsUniqueKeysAcrossNewVersions(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.renderJuniorFormBuilder(w, httptest.NewRequest("GET", "/admin/juniors/forms/1?edit=1", nil), junior.FormInput{ID: 1, SeasonID: 7, Fields: []junior.FormField{{Key: "question_2", Label: "Existing", Type: "text"}}}, []junior.ClubSummary{{ID: 1, Name: "A", State: "active"}}, []junior.Season{{ID: 7, Name: "2026", State: "open"}})
	if !strings.Contains(w.Body.String(), `name="field_key_1" value="question_2"`) || !strings.Contains(w.Body.String(), `name="field_key_2" value="question_1"`) {
		t.Fatal("new question reused existing immutable key")
	}
}
func TestJuniorFixtureFiltersPreservePublishedPopulation(t *testing.T) {
	fixtures := []junior.Fixture{{ID: 1, SeasonID: 3, AgeGroup: "U13", Division: "North", CompetitionName: "League", MatchDate: "2027-05-08", HomeClubID: 7, AwayClubID: 8, HomeClubName: "A", AwayClubName: "B"}, {ID: 2, SeasonID: 3, AgeGroup: "U13", Division: "South", MatchDate: "2027-05-08", HomeClubID: 7, AwayClubID: 9}, {ID: 3, SeasonID: 4, AgeGroup: "U13", Division: "North", MatchDate: "2027-05-08", HomeClubID: 7, AwayClubID: 8}}
	filtered, err := juniorFilterFixtures(fixtures, url.Values{"season_id": {"3"}, "age_group": {"U13"}, "division": {"North"}, "club_id": {"8"}, "date_from": {"2027-05-01"}, "date_to": {"2027-05-15"}, "q": {"league"}})
	if err != nil || len(filtered) != 1 || filtered[0].ID != 1 {
		t.Fatalf("fixture filter population: %+v %v", filtered, err)
	}
	if _, err = juniorFilterFixtures(fixtures, url.Values{"date_from": {"bad"}}); !errors.Is(err, junior.ErrValidation) {
		t.Fatal("invalid fixture date filter accepted")
	}
}
