package leagueapi

import (
	"strings"
	"testing"
	"time"
)

// gmclSaturdayJSON is a representative Play-Cricket API response for a GMCL Saturday.
// Teams use the fake PCTeamIDs assigned in seed.go (10011–10082).
const gmclSaturdayJSON = `{
  "match_details": [
    {
      "match_id": "90001",
      "league_id": "5501",
      "competition_id": "88101",
      "match_date": "05/04/2025",
      "ground_name": "Heaton Mersey CC Ground",
      "home_team_name": "Heaton Mersey CC - 1st XI",
      "home_team_id": "10011",
      "home_club_name": "Heaton Mersey CC",
      "home_club_id": "2001",
      "away_team_name": "Denton St Lawrence CC - 1st XI",
      "away_team_id": "10021",
      "away_club_name": "Denton St Lawrence CC",
      "away_club_id": "2002",
      "umpire_1_name": "R. Patel",
      "umpire_2_name": "S. Khan",
      "umpire_3_name": ""
    },
    {
      "match_id": "90002",
      "league_id": "5501",
      "competition_id": "88102",
      "match_date": "05/04/2025",
      "ground_name": "Woodhouses CC Ground",
      "home_team_name": "Woodhouses CC - 1st XI",
      "home_team_id": "10031",
      "home_club_name": "Woodhouses CC",
      "home_club_id": "2003",
      "away_team_name": "Prestwich CC - 1st XI",
      "away_team_id": "10041",
      "away_club_name": "Prestwich CC",
      "away_club_id": "2004",
      "umpire_1_name": "T. Briggs",
      "umpire_2_name": "D. Walsh",
      "umpire_3_name": ""
    },
    {
      "match_id": "90003",
      "league_id": "5501",
      "competition_id": "88103",
      "match_date": "05/04/2025",
      "ground_name": "Royton CC Ground",
      "home_team_name": "Royton CC - 1st XI",
      "home_team_id": "10061",
      "home_club_name": "Royton CC",
      "home_club_id": "2006",
      "away_team_name": "Norden CC - 1st XI",
      "away_team_id": "10071",
      "away_club_name": "Norden CC",
      "away_club_id": "2007",
      "umpire_1_name": "M. Thornton",
      "umpire_2_name": "J. Clarke",
      "umpire_3_name": ""
    }
  ]
}`

// gmclSundayJSON is a Sunday card for 2nd XI fixtures.
const gmclSundayJSON = `{
  "match_details": [
    {
      "match_id": "90101",
      "league_id": "5501",
      "competition_id": "88201",
      "match_date": "06/04/2025",
      "ground_name": "Heaton Mersey CC Ground",
      "home_team_name": "Heaton Mersey CC - 2nd XI",
      "home_team_id": "10012",
      "home_club_name": "Heaton Mersey CC",
      "home_club_id": "2001",
      "away_team_name": "Prestwich CC - 2nd XI",
      "away_team_id": "10042",
      "away_club_name": "Prestwich CC",
      "away_club_id": "2004",
      "umpire_1_name": "N. Webb",
      "umpire_2_name": "O. Ramshaw",
      "umpire_3_name": ""
    },
    {
      "match_id": "90102",
      "league_id": "5501",
      "competition_id": "88202",
      "match_date": "06/04/2025",
      "ground_name": "Hyde CC Ground",
      "home_team_name": "Hyde CC - 1st XI",
      "home_team_id": "10081",
      "home_club_name": "Hyde CC",
      "home_club_id": "2008",
      "away_team_name": "Swinton Moorside CC - 1st XI",
      "away_team_id": "10051",
      "away_club_name": "Swinton Moorside CC",
      "away_club_id": "2005",
      "umpire_1_name": "B. Sharma",
      "umpire_2_name": "C. Doyle",
      "umpire_3_name": ""
    }
  ]
}`

// gmclMissingUmpiresJSON has fixtures where umpires have not yet been assigned.
const gmclMissingUmpiresJSON = `{
  "match_details": [
    {
      "match_id": "90201",
      "league_id": "5501",
      "competition_id": "88301",
      "match_date": "12/04/2025",
      "ground_name": "Denton St Lawrence CC Ground",
      "home_team_name": "Denton St Lawrence CC - 2nd XI",
      "home_team_id": "10022",
      "home_club_name": "Denton St Lawrence CC",
      "home_club_id": "2002",
      "away_team_name": "Woodhouses CC - 2nd XI",
      "away_team_id": "10032",
      "away_club_name": "Woodhouses CC",
      "away_club_id": "2003",
      "umpire_1_name": "",
      "umpire_2_name": "",
      "umpire_3_name": ""
    }
  ]
}`

func TestParseMatchDetailsJSON_Saturday(t *testing.T) {
	r, err := ParseMatchDetailsJSON([]byte(gmclSaturdayJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MatchDetails) != 3 {
		t.Fatalf("want 3 matches, got %d", len(r.MatchDetails))
	}

	first := r.MatchDetails[0]
	if first.MatchID != "90001" {
		t.Errorf("match_id: got %q", first.MatchID)
	}
	if first.HomeTeamID != "10011" || first.AwayTeamID != "10021" {
		t.Errorf("team IDs: home=%q away=%q", first.HomeTeamID, first.AwayTeamID)
	}
	if first.Umpire1Name != "R. Patel" || first.Umpire2Name != "S. Khan" {
		t.Errorf("umpires: %q / %q", first.Umpire1Name, first.Umpire2Name)
	}
	if first.HomeClubName != "Heaton Mersey CC" {
		t.Errorf("home club: %q", first.HomeClubName)
	}
	if first.AwayClubName != "Denton St Lawrence CC" {
		t.Errorf("away club: %q", first.AwayClubName)
	}

	// Verify all three dates parse correctly as Saturday 2025-04-05.
	for i, d := range r.MatchDetails {
		mt, err := ParseMatchDate(d.MatchDate, "")
		if err != nil {
			t.Fatalf("match %d date parse: %v", i, err)
		}
		if got := mt.Format("2006-01-02"); got != "2025-04-05" {
			t.Errorf("match %d: want 2025-04-05, got %s", i, got)
		}
	}
}

func TestParseMatchDetailsJSON_Sunday(t *testing.T) {
	r, err := ParseMatchDetailsJSON([]byte(gmclSundayJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MatchDetails) != 2 {
		t.Fatalf("want 2 matches, got %d", len(r.MatchDetails))
	}

	second := r.MatchDetails[1]
	if second.MatchID != "90102" {
		t.Errorf("match_id: got %q", second.MatchID)
	}
	if second.HomeTeamID != "10081" || second.AwayTeamID != "10051" {
		t.Errorf("team IDs: home=%q away=%q", second.HomeTeamID, second.AwayTeamID)
	}
	if second.Umpire1Name != "B. Sharma" || second.Umpire2Name != "C. Doyle" {
		t.Errorf("umpires: %q / %q", second.Umpire1Name, second.Umpire2Name)
	}

	mt, err := ParseMatchDate(r.MatchDetails[0].MatchDate, "")
	if err != nil {
		t.Fatal(err)
	}
	if mt.Weekday() != time.Sunday {
		t.Errorf("want Sunday, got %s", mt.Weekday())
	}
}

func TestParseMatchDetailsJSON_MissingUmpires(t *testing.T) {
	r, err := ParseMatchDetailsJSON([]byte(gmclMissingUmpiresJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MatchDetails) != 1 {
		t.Fatalf("want 1 match, got %d", len(r.MatchDetails))
	}
	d := r.MatchDetails[0]
	if d.Umpire1Name != "" || d.Umpire2Name != "" {
		t.Errorf("expected empty umpires, got %q / %q", d.Umpire1Name, d.Umpire2Name)
	}
}

func TestParseMatchDetailsJSON_Empty(t *testing.T) {
	r, err := ParseMatchDetailsJSON([]byte(`{"match_details":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MatchDetails) != 0 {
		t.Fatalf("want 0, got %d", len(r.MatchDetails))
	}
}

func TestParseMatchDetailsJSON_NullMatchDetails(t *testing.T) {
	r, err := ParseMatchDetailsJSON([]byte(`{"match_details":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.MatchDetails != nil && len(r.MatchDetails) != 0 {
		t.Fatalf("want nil/empty, got %v", r.MatchDetails)
	}
}

func TestParseMatchDetailsJSON_InvalidJSON(t *testing.T) {
	_, err := ParseMatchDetailsJSON([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseMatchDetailsJSON_ExtraFields(t *testing.T) {
	// Extra fields in the API response must be silently ignored.
	body := `{"match_details":[{"match_id":"1","match_date":"05/04/2025","unknown_future_field":"ignored"}]}`
	r, err := ParseMatchDetailsJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MatchDetails) != 1 || r.MatchDetails[0].MatchID != "1" {
		t.Fatalf("unexpected result: %+v", r.MatchDetails)
	}
}

// ---------------------------------------------------------------------------
// ParseMatchDate
// ---------------------------------------------------------------------------

func TestParseMatchDate_DDMMYYYY(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"05/04/2025", "2025-04-05"},
		{"14/06/2025", "2025-06-14"},
		{"01/01/2026", "2026-01-01"},
		{"31/12/2025", "2025-12-31"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got, err := ParseMatchDate(c.input, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.Format("2006-01-02") != c.want {
				t.Errorf("want %s, got %s", c.want, got.Format("2006-01-02"))
			}
		})
	}
}

func TestParseMatchDate_ISO(t *testing.T) {
	got, err := ParseMatchDate("2025-04-05", "2006-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if got.Format("2006-01-02") != "2025-04-05" {
		t.Errorf("got %s", got.Format("2006-01-02"))
	}
}

func TestParseMatchDate_ISOFallback(t *testing.T) {
	// Even with no format hint, ISO-style input should be handled.
	got, err := ParseMatchDate("2025-06-14", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Format("2006-01-02") != "2025-06-14" {
		t.Errorf("got %s", got.Format("2006-01-02"))
	}
}

func TestParseMatchDate_Empty(t *testing.T) {
	_, err := ParseMatchDate("", "")
	if err == nil {
		t.Fatal("expected error for empty date")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseMatchDate_Whitespace(t *testing.T) {
	got, err := ParseMatchDate("  05/04/2025  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Format("2006-01-02") != "2025-04-05" {
		t.Errorf("got %s", got.Format("2006-01-02"))
	}
}

func TestParseMatchDate_Invalid(t *testing.T) {
	_, err := ParseMatchDate("not-a-date", "")
	if err == nil {
		t.Fatal("expected error for invalid date")
	}
}

// ---------------------------------------------------------------------------
// FormatDateForTemplate
// ---------------------------------------------------------------------------

func TestFormatDateForTemplate_Default(t *testing.T) {
	d := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	got := FormatDateForTemplate(d, "")
	if got != "05/04/2025" {
		t.Errorf("want 05/04/2025, got %s", got)
	}
}

func TestFormatDateForTemplate_ISO(t *testing.T) {
	d := time.Date(2025, 4, 5, 0, 0, 0, 0, time.UTC)
	got := FormatDateForTemplate(d, "2006-01-02")
	if got != "2025-04-05" {
		t.Errorf("want 2025-04-05, got %s", got)
	}
}

func TestFormatDateForTemplate_ExplicitDDMMYYYY(t *testing.T) {
	d := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	got := FormatDateForTemplate(d, "dd/MM/yyyy")
	if got != "31/12/2025" {
		t.Errorf("want 31/12/2025, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// DetailToJSON
// ---------------------------------------------------------------------------

func TestDetailToJSON_RoundTrip(t *testing.T) {
	d := MatchDetail{
		MatchID:      "90001",
		MatchDate:    "05/04/2025",
		HomeTeamID:   "10011",
		HomeTeamName: "Heaton Mersey CC - 1st XI",
		AwayTeamID:   "10021",
		AwayTeamName: "Denton St Lawrence CC - 1st XI",
		Umpire1Name:  "R. Patel",
		Umpire2Name:  "S. Khan",
	}
	b := DetailToJSON(d)
	if len(b) == 0 {
		t.Fatal("expected non-empty JSON")
	}
	// Re-parse to verify round-trip.
	inner := append(b, []byte(`]}`)...)
	wrapped := append([]byte(`{"match_details":[`), inner...)
	r, err := ParseMatchDetailsJSON(wrapped)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if len(r.MatchDetails) != 1 {
		t.Fatalf("want 1, got %d", len(r.MatchDetails))
	}
	if r.MatchDetails[0].MatchID != "90001" {
		t.Errorf("match_id mismatch: %q", r.MatchDetails[0].MatchID)
	}
	if r.MatchDetails[0].Umpire1Name != "R. Patel" {
		t.Errorf("umpire1 mismatch: %q", r.MatchDetails[0].Umpire1Name)
	}
}
