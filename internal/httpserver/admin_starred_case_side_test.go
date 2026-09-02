package httpserver

import (
	"testing"

	"cricket-ground-feedback/internal/starred"
)

func TestSelectStarredFixtureSideFallsBackToUniqueClubForTeamNameAlias(t *testing.T) {
	breach := starred.Breach{Appearance: starred.Appearance{
		MatchID: 7460280, ClubName: "Bolton Deane & Derby CC", ClubKey: "boltondeanederby", TeamName: "3rd XI", TeamLevel: 3,
	}}
	home := starredFixtureSide{Side: "home", TeamPCID: "123", ClubName: "Deane & Derby CC", TeamName: "3rd XI"}
	away := starredFixtureSide{Side: "away", TeamPCID: "456", ClubName: "Other CC", TeamName: "Other CC 3rd XI"}

	selected, err := selectStarredFixtureSide(breach, home, away)
	if err != nil {
		t.Fatalf("selectStarredFixtureSide returned error: %v", err)
	}
	if selected.Side != "home" || selected.TeamPCID != "123" {
		t.Fatalf("selected=%#v want home side", selected)
	}
}

func TestStarredBreachRuleLabelDistinguishesListsAndFinalThreeRule(t *testing.T) {
	if got := starredBreachRuleLabel(starred.Breach{ListType: "A", RuleReference: "3.5"}); got != "List A" {
		t.Fatalf("List A label=%q", got)
	}
	if got := starredBreachRuleLabel(starred.Breach{ListType: "Last 3", RuleReference: starred.LastThreeSecondXIRule}); got != "Rule 4.6.3.3.5.1" {
		t.Fatalf("final-three label=%q", got)
	}
}
