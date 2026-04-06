package leagueapi

import (
	"testing"
)

func TestParseMatchDetailsJSON(t *testing.T) {
	body := []byte(`{"match_details":[{"match_id":"1","match_date":"26/04/2025","umpire_1_name":"A","umpire_2_name":"B","home_team_id":"10","away_team_id":"20"}]}`)
	r, err := ParseMatchDetailsJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MatchDetails) != 1 {
		t.Fatalf("got %d details", len(r.MatchDetails))
	}
	d := r.MatchDetails[0]
	if d.Umpire1Name != "A" || d.Umpire2Name != "B" {
		t.Fatalf("umpires: %+v", d)
	}
	mt, err := ParseMatchDate(d.MatchDate, "")
	if err != nil {
		t.Fatal(err)
	}
	if mt.Format("2006-01-02") != "2025-04-26" {
		t.Fatalf("date: %s", mt.Format("2006-01-02"))
	}
}
