package junior

import (
	"fmt"
	"testing"
	"time"
)

func TestJuniorRoundRobinSchedules(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 8} {
		for _, format := range []string{"once", "homeaway"} {
			t.Run(fmt.Sprintf("%d_%s", n, format), func(t *testing.T) {
				c := Competition{ID: 1, Format: format, StartDate: "2027-05-01", EndDate: "2027-08-31", DefaultStartTime: "18:00"}
				entries := []CompetitionEntry{}
				constraints := FixtureConstraints{DurationMinutes: 120}
				for i := 1; i <= n; i++ {
					entries = append(entries, CompetitionEntry{TeamID: int64(i), ClubID: int32(i)})
					constraints.Grounds = append(constraints.Grounds, GroundConstraint{ClubID: int32(i), Name: fmt.Sprint("Ground", i), Capacity: 1})
				}
				for i := 0; i < 20; i++ {
					constraints.PlayingDates = append(constraints.PlayingDates, time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i*3).Format("2006-01-02"))
				}
				fixtures, err := GenerateFixtureSchedule(c, entries, constraints)
				if err != nil {
					t.Fatal(err)
				}
				legs := 1
				if format == "homeaway" {
					legs = 2
				}
				if len(fixtures) != n*(n-1)/2*legs {
					t.Fatalf("fixtures=%d", len(fixtures))
				}
				teamGames := map[int64]int{}
				for _, f := range fixtures {
					if f.HomeTeamID == 0 || f.AwayTeamID == 0 || f.HomeTeamID == f.AwayTeamID {
						t.Fatal("bye or self fixture persisted")
					}
					teamGames[f.HomeTeamID]++
					teamGames[f.AwayTeamID]++
				}
				for _, e := range entries {
					if teamGames[e.TeamID] != (n-1)*legs {
						t.Fatalf("team%d games=%d", e.TeamID, teamGames[e.TeamID])
					}
				}
				if err = validateFixturePairings(c, entries, fixtures); err != nil {
					t.Fatal(err)
				}
				for _, issue := range CheckFixtureSchedule(c, constraints, fixtures, nil) {
					if issue.Blocking {
						t.Fatal(issue)
					}
				}
			})
		}
	}
}
func TestJuniorFixtureGenerationRejectsImpossibleDatesAndDuplicatePairs(t *testing.T) {
	c := Competition{Format: "once", StartDate: "2027-05-01", EndDate: "2027-05-31", DefaultStartTime: "18:00"}
	entries := []CompetitionEntry{{TeamID: 1}, {TeamID: 2}, {TeamID: 3}}
	for _, dates := range [][]string{{"2027-05-01"}, {"2027-05-01", "2027-05-01", "2027-05-03"}, {"2027-04-01", "2027-05-02", "2027-05-03"}} {
		if _, err := GenerateFixtureSchedule(c, entries, FixtureConstraints{PlayingDates: dates}); err == nil {
			t.Fatal("invalid date set accepted")
		}
	}
	fixtures, _ := GenerateFixtureSchedule(c, entries, FixtureConstraints{PlayingDates: []string{"2027-05-01", "2027-05-02", "2027-05-03"}})
	fixtures = append(fixtures, fixtures[0])
	if validateFixturePairings(c, entries, fixtures) == nil {
		t.Fatal("duplicate pairing accepted")
	}
}
func TestJuniorFixtureClashAndCoverageChecks(t *testing.T) {
	c := Competition{StartDate: "2027-05-01", EndDate: "2027-05-31"}
	base := Fixture{ID: 1, HomeTeamID: 1, AwayTeamID: 2, HomeClubID: 1, AwayClubID: 2, MatchDate: "2027-05-01", StartTime: "18:00", GroundName: "Shared / ground"}
	constraints := FixtureConstraints{PlayingDates: []string{"2027-05-01"}, DurationMinutes: 120, Grounds: []GroundConstraint{{ClubID: 1, Name: base.GroundName, Capacity: 1}}}
	has := func(issues []FixtureIssue, code string) bool {
		for _, x := range issues {
			if x.Code == code {
				return true
			}
		}
		return false
	}
	other := base
	other.ID = 2
	other.HomeTeamID = 3
	other.AwayTeamID = 4
	other.DurationMinutes = 60
	other.GroundCapacity = 1
	later := base
	later.StartTime = "19:00"
	if has(CheckFixtureSchedule(c, constraints, []Fixture{later}, []Fixture{other}), "ground_clash") {
		t.Fatal("non-overlapping known booking blocked")
	}
	later.StartTime = "18:59"
	if !has(CheckFixtureSchedule(c, constraints, []Fixture{later}, []Fixture{other}), "ground_clash") {
		t.Fatal("overlapping booking missed")
	}
	unknown := base
	unknown.GroundName = ""
	issues := CheckFixtureSchedule(c, constraints, []Fixture{unknown}, nil)
	if !has(issues, "missing_ground") || !has(issues, "senior_coverage_gap") {
		t.Fatal("coverage gap silently certified")
	}
	unavailable := constraints
	unavailable.Unavailable = []UnavailableDate{{ClubID: 2, Date: "2027-05-01"}}
	if !has(CheckFixtureSchedule(c, unavailable, []Fixture{base}, nil), "club_unavailable") {
		t.Fatal("club unavailability missed")
	}
	other.GroundName = "Different"
	other.HomeTeamID = 1
	if !has(CheckFixtureSchedule(c, constraints, []Fixture{base}, []Fixture{other}), "team_double_booking") {
		t.Fatal("cross-competition team booking missed")
	}
	unknown.StartTime = "23:00"
	if !has(CheckFixtureSchedule(c, constraints, []Fixture{unknown}, nil), "overnight_fixture") {
		t.Fatal("unknown ground bypassed overnight validation")
	}
}
func TestJuniorFixtureGenerationUsesAvailablePlayingDates(t *testing.T) {
	c := Competition{StartDate: "2027-05-01", EndDate: "2027-05-31", Format: "once", DefaultStartTime: "18:00"}
	constraints := FixtureConstraints{PlayingDates: []string{"2027-05-01", "2027-05-08"}, ReserveDates: []string{"2027-05-15"}, DurationMinutes: 60, Grounds: []GroundConstraint{{ClubID: 1, Name: "A", Capacity: 1}, {ClubID: 2, Name: "B", Capacity: 1}}, Unavailable: []UnavailableDate{{ClubID: 1, Date: "2027-05-01"}}}
	base, err := GenerateFixtureSchedule(c, []CompetitionEntry{{TeamID: 1, ClubID: 1}, {TeamID: 2, ClubID: 2}}, constraints)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := ScheduleGeneratedFixtureDates(c, constraints, base, nil)
	if err != nil || len(moved) != 1 || moved[0].MatchDate != "2027-05-08" || moved[0].HomeTeamID != base[0].HomeTeamID || moved[0].AwayTeamID != base[0].AwayTeamID {
		t.Fatalf("available date/pair preservation: %+v %v", moved, err)
	}
	constraints.Unavailable = nil
	booked := base[0]
	booked.DurationMinutes = 60
	booked.GroundCapacity = 1
	moved, err = ScheduleGeneratedFixtureDates(c, constraints, base, []Fixture{booked})
	if err != nil || moved[0].MatchDate != "2027-05-08" {
		t.Fatalf("known junior booking not avoided: %+v %v", moved, err)
	}
	constraints.Unavailable = []UnavailableDate{{ClubID: 1, Date: "2027-05-01"}, {ClubID: 1, Date: "2027-05-08"}}
	if _, err = ScheduleGeneratedFixtureDates(c, constraints, base, nil); err == nil {
		t.Fatal("generator silently used a reserve date or ignored unavailable dates")
	}
}
