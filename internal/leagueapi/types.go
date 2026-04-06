package leagueapi

// MatchDetailsResponse matches the common Play-Cricket league "match_details" envelope.
type MatchDetailsResponse struct {
	MatchDetails []MatchDetail `json:"match_details"`
}

// MatchDetail is a subset of fields returned per match; extra fields are ignored.
type MatchDetail struct {
	MatchID       string `json:"match_id"`
	LeagueID      string `json:"league_id"`
	CompetitionID string `json:"competition_id"`
	MatchDate     string `json:"match_date"`
	GroundName    string `json:"ground_name"`
	HomeTeamName  string `json:"home_team_name"`
	HomeTeamID    string `json:"home_team_id"`
	HomeClubName  string `json:"home_club_name"`
	HomeClubID    string `json:"home_club_id"`
	AwayTeamName  string `json:"away_team_name"`
	AwayTeamID    string `json:"away_team_id"`
	AwayClubName  string `json:"away_club_name"`
	AwayClubID    string `json:"away_club_id"`
	Umpire1Name   string `json:"umpire_1_name"`
	Umpire2Name   string `json:"umpire_2_name"`
	Umpire3Name   string `json:"umpire_3_name"`
}
