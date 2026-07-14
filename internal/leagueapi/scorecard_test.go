package leagueapi

import "testing"

func TestParseScorecardPlayersArray(t *testing.T) {
	body := []byte(`{"match_details":[{"id":123,"match_date":"18/04/2026","home_club_name":"Alpha CC","home_team_name":"1st XI","away_club_name":"Beta CC","away_team_name":"2nd XI","players":[{"home_team":[{"player_id":1,"player_name":"Jane Smith"}]},{"away_team":[{"player_id":2,"player_name":"Sam Jones"}]}]}]}`)
	r, err := ParseScorecardJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	m := r.MatchDetails[0]
	if m.MatchID != "123" || len(m.Players.HomeTeam) != 1 || len(m.Players.AwayTeam) != 1 {
		t.Fatalf("unexpected scorecard: %#v", m)
	}
}

func TestParseScorecardPlayersObject(t *testing.T) {
	body := []byte(`{"match_details":[{"match_id":"123","players":{"home_team":[{"player_id":1,"player_name":"Jane Smith"}],"away_team":[]}}]}`)
	r, err := ParseScorecardJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MatchDetails[0].Players.HomeTeam) != 1 {
		t.Fatal("object-form players not parsed")
	}
}
