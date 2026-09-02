package starred

import (
	"testing"
	"time"
)

func TestLastThreeSecondXIBreachesAppliesRule(t *testing.T) {
	start := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	appearance := func(matchID int64, week, level int, playerID int64, playerName string) Appearance {
		return Appearance{
			MatchID: matchID, SeasonYear: 2026, MatchDate: start.AddDate(0, 0, week*7),
			CompetitionType: "League", CompetitionName: "GMCL Sunday Regional",
			ClubName: "Alpha CC", ClubKey: "alpha", TeamName: map[int]string{1: "1st XI", 2: "2nd XI"}[level],
			TeamLevel: level, PlayingDay: map[int]string{1: "Saturday", 2: "Sunday"}[level],
			PlayerID: playerID, PlayerName: playerName, PlayerKey: NormalizeName(playerName),
		}
	}

	var appearances []Appearance
	for i := 0; i < 6; i++ {
		appearances = append(appearances, appearance(int64(100+i), i, 1, 10, "Rule Breach"))
	}
	appearances = append(appearances,
		appearance(200, 6, 2, 10, "Rule Breach"),
		appearance(201, 7, 2, 20, "Other Player"),
		appearance(202, 8, 2, 20, "Other Player"),
		appearance(203, 9, 2, 20, "Other Player"),
		appearance(204, 10, 2, 10, "Rule Breach"),
	)

	breaches := lastThreeSecondXIBreaches(appearances, start.AddDate(0, 0, 100))
	if len(breaches) != 1 {
		t.Fatalf("breaches=%d want 1: %#v", len(breaches), breaches)
	}
	breach := breaches[0]
	if breach.Appearance.MatchID != 204 || breach.RuleReference != LastThreeSecondXIRule || breach.FirstXILeague != 6 || breach.SecondXILeague != 2 {
		t.Fatalf("unexpected breach: %#v", breach)
	}
}

func TestLastThreeSecondXIBreachesRequiresSixFirstAndFewerThanThreeSecondXI(t *testing.T) {
	cutoff := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	apps := []Appearance{
		{MatchID: 1, MatchDate: cutoff.AddDate(0, 0, -14), CompetitionType: "League", ClubKey: "alpha", TeamLevel: 2, PlayingDay: "Sunday", PlayerID: 10},
		{MatchID: 2, MatchDate: cutoff.AddDate(0, 0, -7), CompetitionType: "League", ClubKey: "alpha", TeamLevel: 2, PlayingDay: "Sunday", PlayerID: 20},
		{MatchID: 3, MatchDate: cutoff, CompetitionType: "League", ClubKey: "alpha", TeamLevel: 2, PlayingDay: "Sunday", PlayerID: 30},
	}
	for i := 0; i < 5; i++ {
		apps = append(apps, Appearance{MatchID: int64(100 + i), MatchDate: cutoff.AddDate(0, 0, -30-i), CompetitionType: "League", ClubKey: "alpha", TeamLevel: 1, PlayingDay: "Saturday", PlayerID: 10})
	}
	if breaches := lastThreeSecondXIBreaches(apps, cutoff); len(breaches) != 0 {
		t.Fatalf("five First XI appearances produced breaches: %#v", breaches)
	}
}
