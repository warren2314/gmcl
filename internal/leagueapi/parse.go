package leagueapi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (p *PlayerSheets) UnmarshalJSON(body []byte) error {
	var sides []PlayerSide
	if err := json.Unmarshal(body, &sides); err == nil {
		for _, side := range sides {
			p.HomeTeam = append(p.HomeTeam, side.HomeTeam...)
			p.AwayTeam = append(p.AwayTeam, side.AwayTeam...)
		}
		return nil
	}
	var side PlayerSide
	if err := json.Unmarshal(body, &side); err != nil {
		return err
	}
	p.HomeTeam, p.AwayTeam = side.HomeTeam, side.AwayTeam
	return nil
}

// stringOrNumber accepts the inconsistent scalar representation used by
// Play-Cricket. Identifier and points fields can be JSON strings in one match
// and JSON numbers in another; keeping the normalised value as text preserves
// the upstream value without losing precision.
type stringOrNumber string

func (value *stringOrNumber) UnmarshalJSON(body []byte) error {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || trimmed == "null" {
		*value = ""
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(body, &text); err != nil {
			return err
		}
		*value = stringOrNumber(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(body, &number); err != nil {
		return fmt.Errorf("expected string or number: %w", err)
	}
	*value = stringOrNumber(number.String())
	return nil
}

func (match *ScorecardMatch) UnmarshalJSON(body []byte) error {
	type scorecardMatchAlias ScorecardMatch
	wire := struct {
		HomeTeamID      stringOrNumber `json:"home_team_id"`
		HomeClubID      stringOrNumber `json:"home_club_id"`
		AwayTeamID      stringOrNumber `json:"away_team_id"`
		AwayClubID      stringOrNumber `json:"away_club_id"`
		ResultAppliedTo stringOrNumber `json:"result_applied_to"`
		*scorecardMatchAlias
	}{scorecardMatchAlias: (*scorecardMatchAlias)(match)}
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}
	match.HomeTeamID = string(wire.HomeTeamID)
	match.HomeClubID = string(wire.HomeClubID)
	match.AwayTeamID = string(wire.AwayTeamID)
	match.AwayClubID = string(wire.AwayClubID)
	match.ResultAppliedTo = string(wire.ResultAppliedTo)
	return nil
}

func (points *ScorecardPoints) UnmarshalJSON(body []byte) error {
	type scorecardPointsAlias ScorecardPoints
	wire := struct {
		TeamID                    stringOrNumber `json:"team_id"`
		GamePoints                stringOrNumber `json:"game_points"`
		PenaltyPoints             stringOrNumber `json:"penalty_points"`
		BonusPointsTogether       stringOrNumber `json:"bonus_points_together"`
		BonusPointsBatting        stringOrNumber `json:"bonus_points_batting"`
		BonusPointsBowling        stringOrNumber `json:"bonus_points_bowling"`
		BonusPointsSecondTogether stringOrNumber `json:"bonus_points_2nd_innings_together"`
		BonusPointsSecondBatting  stringOrNumber `json:"bonus_points_2nd_innings_batting"`
		BonusPointsSecondBowling  stringOrNumber `json:"bonus_points_2nd_innings_bowling"`
		*scorecardPointsAlias
	}{scorecardPointsAlias: (*scorecardPointsAlias)(points)}
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}
	points.TeamID = string(wire.TeamID)
	points.GamePoints = string(wire.GamePoints)
	points.PenaltyPoints = string(wire.PenaltyPoints)
	points.BonusPointsTogether = string(wire.BonusPointsTogether)
	points.BonusPointsBatting = string(wire.BonusPointsBatting)
	points.BonusPointsBowling = string(wire.BonusPointsBowling)
	points.BonusPointsSecondTogether = string(wire.BonusPointsSecondTogether)
	points.BonusPointsSecondBatting = string(wire.BonusPointsSecondBatting)
	points.BonusPointsSecondBowling = string(wire.BonusPointsSecondBowling)
	return nil
}

func ParseScorecardJSON(body []byte) (*ScorecardResponse, error) {
	var r ScorecardResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if len(r.MatchDetails) == 0 {
		return nil, fmt.Errorf("match detail response contained no match_details")
	}
	for i := range r.MatchDetails {
		if strings.TrimSpace(r.MatchDetails[i].MatchID) == "" && r.MatchDetails[i].ID > 0 {
			r.MatchDetails[i].MatchID = strconv.FormatInt(r.MatchDetails[i].ID, 10)
		}
	}
	return &r, nil
}

// DetailToJSON returns JSON for the payload column.
func DetailToJSON(d MatchDetail) []byte {
	b, _ := json.Marshal(d)
	return b
}

// ParseMatchDetailsJSON decodes the league API JSON body into MatchDetailsResponse.
func ParseMatchDetailsJSON(body []byte) (*MatchDetailsResponse, error) {
	var r MatchDetailsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if len(r.MatchDetails) == 0 && len(r.Matches) > 0 {
		r.MatchDetails = r.Matches
	}
	for i := range r.MatchDetails {
		if strings.TrimSpace(r.MatchDetails[i].MatchID) == "" && r.MatchDetails[i].ID > 0 {
			r.MatchDetails[i].MatchID = strconv.FormatInt(r.MatchDetails[i].ID, 10)
		}
	}
	return &r, nil
}

// ParseMatchDate parses match_date from API (typically dd/MM/yyyy) to a calendar date.
func ParseMatchDate(s, formatHint string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty match date")
	}
	switch formatHint {
	case "dd/MM/yyyy", "":
		t, err := time.Parse("02/01/2006", s)
		if err == nil {
			return t, nil
		}
	}
	// ISO
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Parse("02/01/2006", s)
}

// FormatDateForTemplate formats t for URL templates (default dd/MM/yyyy).
func FormatDateForTemplate(t time.Time, formatHint string) string {
	if formatHint == "2006-01-02" {
		return t.Format("2006-01-02")
	}
	return t.Format("02/01/2006")
}
