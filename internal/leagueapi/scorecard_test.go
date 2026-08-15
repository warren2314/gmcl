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

func TestParseScorecardResultAndPoints(t *testing.T) {
	body := []byte(`{"match_details":[{"match_id":"123","home_team_id":"10","away_team_id":"20","result":"W","result_description":"Beta CC - 2nd XI - Win 20pts","result_applied_to":"20","points":[{"team_id":"10","game_points":"0","bonus_points_batting":"3"},{"team_id":"20","game_points":"20","bonus_points_bowling":"1"}],"players":[]} ]}`)
	r, err := ParseScorecardJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	m := r.MatchDetails[0]
	if m.ResultDescription != "Beta CC - 2nd XI - Win 20pts" || m.ResultAppliedTo != "20" {
		t.Fatalf("result fields not parsed: %#v", m)
	}
	if len(m.Points) != 2 || m.Points[1].GamePoints != "20" || m.Points[1].BonusPointsBowling != "1" {
		t.Fatalf("points breakdown not parsed: %#v", m.Points)
	}
}
