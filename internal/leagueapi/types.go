package leagueapi

// MatchDetailsResponse matches Play-Cricket responses. Some endpoints return
// "match_details" while others return "matches"; both are accepted.
type MatchDetailsResponse struct {
	MatchDetails []MatchDetail `json:"match_details"`
	Matches      []MatchDetail `json:"matches"`
}

// MatchDetail is a subset of fields returned per match; extra fields are ignored.
type MatchDetail struct {
	MatchID         string `json:"match_id"`
	ID              int64  `json:"id"`
	LeagueID        string `json:"league_id"`
	CompetitionID   string `json:"competition_id"`
	CompetitionName string `json:"competition_name"`
	CompetitionType string `json:"competition_type"`
	LastUpdated     string `json:"last_updated"`
	MatchDate       string `json:"match_date"`
	GroundName      string `json:"ground_name"`
	HomeTeamName    string `json:"home_team_name"`
	HomeTeamID      string `json:"home_team_id"`
	HomeClubName    string `json:"home_club_name"`
	HomeClubID      string `json:"home_club_id"`
	AwayTeamName    string `json:"away_team_name"`
	AwayTeamID      string `json:"away_team_id"`
	AwayClubName    string `json:"away_club_name"`
	AwayClubID      string `json:"away_club_id"`
	Umpire1Name     string `json:"umpire_1_name"`
	Umpire2Name     string `json:"umpire_2_name"`
	Umpire3Name     string `json:"umpire_3_name"`
}

// ScorecardResponse is returned by /api/v2/match_detail.json.
type ScorecardResponse struct {
	MatchDetails []ScorecardMatch `json:"match_details"`
}

type ScorecardMatch struct {
	ID              int64        `json:"id"`
	MatchID         string       `json:"match_id"`
	LastUpdated     string       `json:"last_updated"`
	CompetitionName string       `json:"competition_name"`
	CompetitionType string       `json:"competition_type"`
	MatchDate       string       `json:"match_date"`
	HomeTeamName    string       `json:"home_team_name"`
	HomeTeamID      string       `json:"home_team_id"`
	HomeClubName    string       `json:"home_club_name"`
	HomeClubID      string       `json:"home_club_id"`
	AwayTeamName    string       `json:"away_team_name"`
	AwayTeamID      string       `json:"away_team_id"`
	AwayClubName    string       `json:"away_club_name"`
	AwayClubID      string       `json:"away_club_id"`
	Players         PlayerSheets `json:"players"`
}

type ScorecardPlayer struct {
	Position     int    `json:"position"`
	PlayerName   string `json:"player_name"`
	PlayerID     int64  `json:"player_id"`
	Captain      bool   `json:"captain"`
	WicketKeeper bool   `json:"wicket_keeper"`
}

type PlayerSide struct {
	HomeTeam []ScorecardPlayer `json:"home_team"`
	AwayTeam []ScorecardPlayer `json:"away_team"`
}

// PlayerSheets accepts both the documented array-of-side-objects and the
// object form observed in some Play-Cricket responses.
type PlayerSheets struct {
	HomeTeam []ScorecardPlayer
	AwayTeam []ScorecardPlayer
}
