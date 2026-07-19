package httpserver

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSanctionImportExceptionMappingIsValidJSON(t *testing.T) {
	message := `club "name" was not matched`
	var mapping map[string]string
	if err := json.Unmarshal([]byte(sanctionImportExceptionMapping(message)), &mapping); err != nil {
		t.Fatalf("exception mapping is not valid JSON: %v", err)
	}
	if mapping["error"] != message {
		t.Fatalf("error = %q, want %q", mapping["error"], message)
	}
}

func TestParseSanctionImportCSVNormalisesHeadersWithoutChangingRows(t *testing.T) {
	headers, rows, err := parseSanctionImportCSV([]byte("Club,,Club\nAlpha,value,duplicate\n"))
	if err != nil {
		t.Fatalf("parseSanctionImportCSV: %v", err)
	}
	wantHeaders := []string{"Club", "column_2", "Club_2"}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("headers = %#v, want %#v", headers, wantHeaders)
	}
	wantRows := [][]string{{"Alpha", "value", "duplicate"}}
	if !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("rows = %#v, want %#v", rows, wantRows)
	}
}

func TestNormaliseImportNameMatchesPublishedAliases(t *testing.T) {
	tests := map[string]string{
		"Bolton Deane & Derby CC": "deane and derby",
		"Blackley CC, Lancs":      "blackley",
		"Bradshaw Cricket Club":   "bradshaw",
		"Flixton C&SC":            "flixton",
		"Micklehurst C&SC":        "micklehurst and social",
		"Springhead CCC":          "springhead",
		"Westleigh CC, Leigh":     "westleigh",
		"Woodley CC, Cheshire":    "woodley",
	}
	for input, want := range tests {
		if got := normaliseImportName(input); got != want {
			t.Errorf("normaliseImportName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParsePublishedCardCandidatePreservesRecordedConversion(t *testing.T) {
	clubID, teamID := int32(56), int32(65)
	lookup := sanctionImportLookup{
		clubs: map[string][]importClub{},
		teamsByParts: map[string][]importTeam{
			"deane and derby|1st xi": {{ID: teamID, ClubID: clubID, ClubName: "Deane & Derby CC", Name: "1st XI"}},
		},
		teamsFull: map[string][]importTeam{},
	}
	raw := map[string]string{
		"Name of Club":               "Bolton Deane & Derby CC",
		"Team Standard":              "1st XI",
		"Date of Breach":             "06/06/2026",
		"Offence":                    "8.1.5.1. Captain's post-match report incorrect or late",
		"Card Penalty":               "Red",
		"Resulting Points Deduction": "1",
		"Notes":                      "3rd yellow card offence",
	}
	candidate := parseSanctionImportCandidate("live-team-card-register.csv", 1, 2, "pending", nil, raw, lookup)
	if candidate.Error != "" {
		t.Fatalf("unexpected mapping error: %s", candidate.Error)
	}
	if candidate.ClubID == nil || *candidate.ClubID != clubID || candidate.TeamID == nil || *candidate.TeamID != teamID {
		t.Fatalf("unexpected mapping: club=%v team=%v", candidate.ClubID, candidate.TeamID)
	}
	if candidate.EffectType != "red_card" || candidate.YellowDelta != -2 || candidate.RedDelta != 1 {
		t.Fatalf("unexpected card effect: %#v", candidate)
	}
	if candidate.Points == nil || *candidate.Points != 1 || candidate.RuleReference != "8.1.5.1" {
		t.Fatalf("points/rule not preserved: %#v", candidate)
	}
}

func TestTeamBanDatesCoverEveryRecordedSeason(t *testing.T) {
	start, end := teamBanDates("Banned 27&28")
	if start == nil || end == nil {
		t.Fatal("expected dates")
	}
	if !start.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || end.Year() != 2028 || end.Month() != time.December {
		t.Fatalf("unexpected range: %v to %v", start, end)
	}
}

func TestImportOverrideResolvesTeamAndMissingDateWithoutChangingSource(t *testing.T) {
	clubID, teamID := int32(4), int32(9)
	lookup := sanctionImportLookup{
		clubsByID: map[int32]importClub{clubID: {ID: clubID, Name: "Example CC"}},
		teamsByID: map[int32]importTeam{teamID: {ID: teamID, ClubID: clubID, ClubName: "Example CC", Name: "2nd XI"}},
	}
	candidate := sanctionImportCandidate{EffectType: "yellow_card", PublicReason: "Recorded source offence", Error: "club/team not matched"}
	date := "2026-06-20"
	got := applySanctionImportOverride(candidate, "live-team-card-register.csv", sanctionImportOverride{TeamID: &teamID, OffenceDate: date}, lookup)
	if got.Error != "" || got.TeamID == nil || *got.TeamID != teamID || got.ClubID == nil || *got.ClubID != clubID {
		t.Fatalf("override did not resolve mapping: %#v", got)
	}
	if got.OffenceDate == nil || got.OffenceDate.Format("2006-01-02") != date || got.PublicReason != candidate.PublicReason {
		t.Fatalf("override changed or failed to complete source candidate: %#v", got)
	}
}

func TestParseSanctionImportCSVRejectsEmptyInput(t *testing.T) {
	if _, _, err := parseSanctionImportCSV(nil); err == nil {
		t.Fatal("expected empty CSV to be rejected")
	}
}

func TestLiveSanctionFeedsAreUniquePublishedCSVFeeds(t *testing.T) {
	if len(liveSanctionFeeds) != 3 {
		t.Fatalf("feed count = %d, want 3 unique datasets", len(liveSanctionFeeds))
	}
	seen := map[string]bool{}
	for _, feed := range liveSanctionFeeds {
		if seen[feed.URL] {
			t.Fatalf("duplicate feed URL: %s", feed.URL)
		}
		seen[feed.URL] = true
		if feed.Name == "" || feed.Filename == "" {
			t.Fatalf("incomplete feed definition: %#v", feed)
		}
	}
}
