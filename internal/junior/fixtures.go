package junior

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Competition struct {
	ID, SeasonID, Revision, PublicationID, PublicationRevision                          int64
	Name, AgeGroup, Division, Format, StartDate, EndDate, DefaultStartTime, SeasonState string
}
type CompetitionEntry struct {
	TeamID             int64
	ClubID             int32
	TeamName, ClubName string
}
type GroundConstraint struct {
	ClubID   int32  `json:"club_id"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
}
type UnavailableDate struct {
	ClubID int32  `json:"club_id"`
	Date   string `json:"date"`
}
type FixtureConstraints struct {
	PlayingDates    []string           `json:"playing_dates"`
	ReserveDates    []string           `json:"reserve_dates"`
	Grounds         []GroundConstraint `json:"grounds"`
	Unavailable     []UnavailableDate  `json:"unavailable"`
	DurationMinutes int                `json:"duration_minutes"`
}
type Fixture struct {
	DurationMinutes, GroundCapacity                                                          int
	ID, CompetitionID, PublicationID, HomeTeamID, AwayTeamID                                 int64
	HomeClubID, AwayClubID                                                                   int32
	MatchDate, StartTime, GroundName, HomeTeamName, AwayTeamName, HomeClubName, AwayClubName string
	SeasonID                                                                                 int64
	CompetitionName, AgeGroup, Division                                                      string
}
type FixtureIssue struct {
	Code, Message string
	Blocking      bool
}

func fixtureValidation(message string) error     { return fmt.Errorf("%w: %s", ErrValidation, message) }
func validFixtureTime(value string) bool         { _, err := time.Parse("15:04", value); return err == nil }
func fixtureDay(value string) (time.Time, error) { return time.Parse("2006-01-02", value) }

// GenerateFixtureSchedule is deterministic; zero team IDs represent byes and
// never become fixtures or concession candidates.
func GenerateFixtureSchedule(c Competition, entries []CompetitionEntry, f FixtureConstraints) ([]Fixture, error) {
	if c.Format == "custom" {
		return nil, fixtureValidation("custom competitions use manual draft fixtures")
	}
	if len(entries) < 2 {
		return nil, fixtureValidation("at least two participating teams required")
	}
	if len(entries) > 64 {
		return nil, fixtureValidation("automatic generation supports up to 64 teams per competition; use smaller divisions or a custom schedule")
	}
	teams := append([]CompetitionEntry{}, entries...)
	sort.Slice(teams, func(i, j int) bool { return teams[i].TeamID < teams[j].TeamID })
	seen := map[int64]bool{}
	for _, t := range teams {
		if t.TeamID <= 0 || seen[t.TeamID] {
			return nil, fixtureValidation("duplicate or invalid team")
		}
		seen[t.TeamID] = true
	}
	if len(teams)%2 == 1 {
		teams = append(teams, CompetitionEntry{})
	}
	rounds := len(teams) - 1
	legs := 1
	if c.Format == "homeaway" {
		legs = 2
	} else if c.Format != "once" {
		return nil, fixtureValidation("invalid fixture format")
	}
	dates := append([]string{}, f.PlayingDates...)
	sort.Strings(dates)
	if len(dates) < rounds*legs {
		return nil, fixtureValidation(fmt.Sprintf("%d playing dates required", rounds*legs))
	}
	for i, date := range dates {
		if _, err := fixtureDay(date); err != nil || date < c.StartDate || date > c.EndDate || (i > 0 && date == dates[i-1]) {
			return nil, fixtureValidation("playing dates must be unique and within the competition window")
		}
	}
	if !validFixtureTime(c.DefaultStartTime) {
		return nil, fixtureValidation("invalid default start time")
	}
	grounds := map[int32]string{}
	for _, g := range f.Grounds {
		if grounds[g.ClubID] == "" {
			grounds[g.ClubID] = strings.TrimSpace(g.Name)
		}
	}
	out := []Fixture{}
	rotation := append([]CompetitionEntry{}, teams...)
	for round := 0; round < rounds; round++ {
		for pair := 0; pair < len(rotation)/2; pair++ {
			home, away := rotation[pair], rotation[len(rotation)-1-pair]
			if home.TeamID == 0 || away.TeamID == 0 {
				continue
			}
			if (round+pair)%2 == 1 {
				home, away = away, home
			}
			for leg := 0; leg < legs; leg++ {
				h, a := home, away
				if leg == 1 {
					h, a = a, h
				}
				out = append(out, Fixture{CompetitionID: c.ID, HomeTeamID: h.TeamID, AwayTeamID: a.TeamID, HomeClubID: h.ClubID, AwayClubID: a.ClubID, HomeTeamName: h.TeamName, AwayTeamName: a.TeamName, HomeClubName: h.ClubName, AwayClubName: a.ClubName, MatchDate: dates[round+leg*rounds], StartTime: c.DefaultStartTime, GroundName: grounds[h.ClubID]})
			}
		}
		last := rotation[len(rotation)-1]
		copy(rotation[2:], rotation[1:len(rotation)-1])
		rotation[1] = last
	}
	return out, nil
}

// ScheduleGeneratedFixtureDates preserves the generated pairings and home
// grounds, trying their round date first and then the other playing dates.
// A bounded search keeps impossible calendars from tying up an admin request.
// Reserve dates are deliberately left for explicit manual rescheduling.
func ScheduleGeneratedFixtureDates(c Competition, f FixtureConstraints, generated, otherPublished []Fixture) ([]Fixture, error) {
	if len(generated) == 0 {
		return nil, fixtureValidation("no generated fixtures to schedule")
	}
	blocking := func(issues []FixtureIssue) bool {
		for _, issue := range issues {
			if issue.Blocking {
				return true
			}
		}
		return false
	}
	out := append([]Fixture{}, generated...)
	if !blocking(CheckFixtureSchedule(c, f, out, otherPublished)) {
		return out, nil
	}
	dates := append([]string{}, f.PlayingDates...)
	sort.Strings(dates)
	const maxAttempts = 20000
	attempts := 0
	assigned := make([]Fixture, 0, len(out))
	booked := append([]Fixture{}, otherPublished...)
	var search func(int) bool
	search = func(index int) bool {
		if index == len(out) {
			return true
		}
		choices := []string{generated[index].MatchDate}
		for _, date := range dates {
			if date != generated[index].MatchDate {
				choices = append(choices, date)
			}
		}
		for _, date := range choices {
			attempts++
			if attempts > maxAttempts {
				return false
			}
			candidate := generated[index]
			candidate.MatchDate = date
			if blocking(CheckFixtureSchedule(c, f, []Fixture{candidate}, booked)) {
				continue
			}
			assigned = append(assigned, candidate)
			booked = append(booked, candidate)
			if search(index + 1) {
				return true
			}
			assigned = assigned[:len(assigned)-1]
			booked = booked[:len(booked)-1]
		}
		return false
	}
	if !search(0) {
		return nil, fixtureValidation("no clash-free date allocation found within the configured playing dates and search limit; add playing dates, change constraints or use manual draft fixtures")
	}
	return assigned, nil
}

func CheckFixtureSchedule(c Competition, f FixtureConstraints, drafts, otherPublished []Fixture) []FixtureIssue {
	issues := []FixtureIssue{{Code: "senior_coverage_gap", Message: "Senior/other external ground bookings are not ingested. Check separately before acknowledging publication."}}
	if len(drafts) == 0 {
		issues = append(issues, FixtureIssue{"empty_schedule", "No draft fixtures to publish", true})
	}
	permitted := map[string]bool{}
	for _, d := range append(append([]string{}, f.PlayingDates...), f.ReserveDates...) {
		permitted[d] = true
	}
	teamDates := map[string]bool{}
	groundCapacity := map[string]int{}
	for _, g := range f.Grounds {
		key := strings.ToLower(strings.TrimSpace(g.Name))
		if groundCapacity[key] == 0 || g.Capacity < groundCapacity[key] {
			groundCapacity[key] = g.Capacity
		}
	}
	type slot struct{ start, end int }
	groundSlots := map[string][]slot{}
	ownGrounds := map[string]bool{}
	all := append(append([]Fixture{}, otherPublished...), drafts...)
	for i, x := range all {
		own := i >= len(otherPublished)
		if x.GroundCapacity > 0 {
			g := strings.ToLower(strings.TrimSpace(x.GroundName))
			if groundCapacity[g] == 0 || x.GroundCapacity < groundCapacity[g] {
				groundCapacity[g] = x.GroundCapacity
			}
		}
		if own && (x.MatchDate < c.StartDate || x.MatchDate > c.EndDate || !permitted[x.MatchDate]) {
			issues = append(issues, FixtureIssue{"outside_dates", fmt.Sprintf("Fixture%d is outside permitted playing/reserve dates", x.ID), true})
		}
		for _, team := range []int64{x.HomeTeamID, x.AwayTeamID} {
			key := fmt.Sprintf("%d/%s", team, x.MatchDate)
			if teamDates[key] && own {
				issues = append(issues, FixtureIssue{"team_double_booking", fmt.Sprintf("Team%d plays twice on %s", team, x.MatchDate), true})
			}
			teamDates[key] = true
		}
		if own {
			for _, u := range f.Unavailable {
				if u.Date == x.MatchDate && (u.ClubID == x.HomeClubID || u.ClubID == x.AwayClubID) {
					issues = append(issues, FixtureIssue{"club_unavailable", fmt.Sprintf("Club%d unavailable on %s", u.ClubID, u.Date), true})
				}
			}
		}
		ground := strings.ToLower(strings.TrimSpace(x.GroundName))
		t, err := time.Parse("15:04", x.StartTime)
		if err != nil {
			if own {
				issues = append(issues, FixtureIssue{"invalid_time", "Invalid fixture time", true})
			}
			continue
		}
		duration := f.DurationMinutes
		if x.DurationMinutes > 0 {
			duration = x.DurationMinutes
		}
		if duration <= 0 {
			duration = 120
		}
		start := t.Hour()*60 + t.Minute()
		if own && start+duration > 1440 {
			issues = append(issues, FixtureIssue{"overnight_fixture", "Fixture extends beyond its calendar date", true})
		}
		if ground == "" {
			if own {
				issues = append(issues, FixtureIssue{"missing_ground", fmt.Sprintf("Fixture%d has no verified ground", x.ID), false})
			}
			continue
		}
		key := ground + "/" + x.MatchDate
		if own {
			ownGrounds[key] = true
			if groundCapacity[ground] == 0 {
				issues = append(issues, FixtureIssue{"unknown_ground_capacity", "No verified capacity for " + x.GroundName, false})
			}
		}
		groundSlots[key] = append(groundSlots[key], slot{start, start + duration})
	}
	keys := []string{}
	for key := range groundSlots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !ownGrounds[key] {
			continue
		}
		slots := groundSlots[key]
		ground := key[:strings.LastIndex(key, "/")]
		capacity := groundCapacity[ground]
		if capacity <= 0 {
			capacity = 1
		}
		type edge struct{ minute, delta int }
		edges := []edge{}
		for _, s := range slots {
			edges = append(edges, edge{s.start, 1}, edge{s.end, -1})
		}
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].minute == edges[j].minute {
				return edges[i].delta < edges[j].delta
			}
			return edges[i].minute < edges[j].minute
		})
		count := 0
		for _, e := range edges {
			count += e.delta
			if count > capacity {
				issues = append(issues, FixtureIssue{"ground_clash", "Ground capacity exceeded: " + key, true})
				break
			}
		}
	}
	return issues
}
func FixtureCSVRows(fixtures []Fixture) [][]string {
	rows := [][]string{{"fixture_id", "competition_id", "publication_id", "date", "start_time", "home_club", "home_team", "away_club", "away_team", "ground", "season_id", "competition", "age_group", "division"}}
	for _, f := range fixtures {
		rows = append(rows, []string{strconv.FormatInt(f.ID, 10), strconv.FormatInt(f.CompetitionID, 10), strconv.FormatInt(f.PublicationID, 10), f.MatchDate, f.StartTime, f.HomeClubName, f.HomeTeamName, f.AwayClubName, f.AwayTeamName, f.GroundName, strconv.FormatInt(f.SeasonID, 10), f.CompetitionName, f.AgeGroup, f.Division})
	}
	return rows
}
