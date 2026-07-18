package httpserver

import (
	"reflect"
	"testing"
	"time"
)

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
