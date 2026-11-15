package junior

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func fixtureBookingLabel(f Fixture) string {
	if f.BookingState == "external" {
		return f.CompetitionName + " (whole-ground booking)"
	}
	home, away := strings.TrimSpace(f.HomeClubName+" "+f.HomeTeamName), strings.TrimSpace(f.AwayClubName+" "+f.AwayTeamName)
	if home == "" {
		home = fmt.Sprintf("Team %d", f.HomeTeamID)
	}
	if away == "" {
		away = fmt.Sprintf("Team %d", f.AwayTeamID)
	}
	state := f.BookingState
	if state == "" {
		state = "draft"
	}
	return fmt.Sprintf("%s v %s (%s, %s, %s, fixture %d)", home, away, f.AgeGroup, state, strings.TrimSpace(f.CompetitionName+" · "+f.Division), f.ID)
}
func CheckGroundLayouts(own, other []Fixture) []FixtureIssue {
	issues := []FixtureIssue{}
	type booking struct {
		f          Fixture
		own        bool
		start, end int
		key        string
	}
	groups := map[string][]booking{}
	savedOwnDates := map[string]bool{}
	for _, f := range own {
		if f.GroundID > 0 {
			savedOwnDates[f.MatchDate] = true
		}
	}
	all := append(append([]Fixture{}, other...), own...)
	for i, f := range all {
		ours := i >= len(other)
		if f.GroundID == 0 {
			if f.RequiresSavedGround && (ours || savedOwnDates[f.MatchDate]) {
				issues = append(issues, FixtureIssue{"unmapped_ground", fmt.Sprintf("Venue needs linking: %s on %s at %s, currently recorded as %q. Choose a saved ground on the draft or link its published booking under Grounds and clashes.", fixtureBookingLabel(f), f.MatchDate, f.StartTime, f.GroundName), true})
			}
			continue
		}
		if ours && f.GroundArchived {
			issues = append(issues, FixtureIssue{"archived_ground", "Ground is archived: " + f.GroundName + " " + f.GroundPostcode + ". Choose an active venue before publication.", true})
		}
		startTime, err := time.Parse("15:04", f.StartTime)
		if err != nil {
			continue
		}
		start := startTime.Hour()*60 + startTime.Minute()
		duration := f.DurationMinutes
		if duration <= 0 {
			duration = 120
		}
		key := fmt.Sprintf("%d/%d", f.CompetitionID, f.ID)
		if f.ID == 0 {
			key = fmt.Sprintf("new/%d", i)
		}
		group := fmt.Sprintf("%d/%s", f.GroundID, f.MatchDate)
		groups[group] = append(groups[group], booking{f, ours, start, start + duration, key})
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		bookings := groups[key]
		points := []int{}
		for _, b := range bookings {
			points = append(points, b.start, b.end)
		}
		sort.Ints(points)
		reported := map[string]bool{}
		for i, start := range points {
			if i+1 >= len(points) || points[i+1] == start {
				continue
			}
			end := points[i+1]
			active := map[string]Fixture{}
			hasOwn := false
			for _, b := range bookings {
				if b.start <= start && b.end > start {
					active[b.key] = b.f
					hasOwn = hasOwn || b.own
				}
			}
			if !hasOwn || len(active) == 0 {
				continue
			}
			ids := []string{}
			for id := range active {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			signature := strings.Join(ids, ",")
			if reported[signature] {
				continue
			}
			counts := GroundLayout{}
			labels := []string{}
			var venue Fixture
			for _, id := range ids {
				f := active[id]
				venue = f
				counts[f.AgeGroup]++
				if len(labels) < 12 {
					labels = append(labels, fixtureBookingLabel(f))
				}
			}
			fits := false
			for _, layout := range venue.GroundLayouts {
				ok := true
				for age, n := range counts {
					if n > layout[age] {
						ok = false
					}
				}
				if ok {
					fits = true
					break
				}
			}
			if counts["External"] > 0 {
				fits = len(active) == 1
			}
			if fits {
				continue
			}
			reported[signature] = true
			advice := "Change the time or venue, or verify a genuinely supported mixed layout."
			if counts["External"] > 0 {
				advice = "The external booking reserves the whole ground. Change the fixture time or venue, or correct the external booking."
			}
			message := fmt.Sprintf("GROUND CLASH — %s (%s), %s %02d:%02d–%02d:%02d. Cannot fit together: %s. Allowed layouts: %s. %s", venue.GroundName, venue.GroundPostcode, venue.MatchDate, start/60, start%60, end/60, end%60, strings.Join(labels, "; "), GroundLayoutsText(venue.GroundLayouts), advice)
			issues = append(issues, FixtureIssue{"ground_clash", message, true})
		}
	}
	return issues
}
