package junior

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestU9SharedGroundCapacity(t *testing.T) {
	makeFixture := func(id int64, age, start string) Fixture {
		return Fixture{ID: id, CompetitionID: id, GroundID: 1, GroundName: "Shared", MatchDate: "2027-05-02", StartTime: start, DurationMinutes: 60, AgeGroup: age, GroundLayouts: []GroundLayout{{"U9": 3}, {"U11": 2}}}
	}
	a, b, c := makeFixture(1, "U9", "09:00"), makeFixture(2, "U9", "09:00"), makeFixture(3, "U9", "09:30")
	for _, tc := range []struct {
		name   string
		others []Fixture
		clash  bool
	}{
		{"three overlapping games", []Fixture{b, c}, false},
		{"four overlapping games", []Fixture{b, c, makeFixture(4, "U9", "09:00")}, true},
		{"U9 and U11 need explicit mixed layout", []Fixture{makeFixture(2, "U11", "09:00")}, true},
		{"adjacent slots", []Fixture{makeFixture(2, "U11", "10:00")}, false},
		{"published draft duplication", []Fixture{a, b, c}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckGroundLayouts([]Fixture{a}, tc.others)
			if (len(got) > 0) != tc.clash {
				t.Fatalf("issues: %+v", got)
			}
		})
	}
	ext := makeFixture(-1, "External", "09:00")
	ext.BookingState = "external"
	if issues := CheckGroundLayouts([]Fixture{a}, []Fixture{ext}); len(issues) == 0 || !issues[0].Blocking {
		t.Fatalf("external booking failed to reserve ground: %+v", issues)
	}
	if issues := CheckGroundLayouts([]Fixture{ext}, nil); len(issues) > 0 {
		t.Fatalf("external booking clashes with itself: %+v", issues)
	}
}
func TestGroundCalendarContext(t *testing.T) {
	all := []Fixture{{ID: 1, CompetitionID: 1, GroundID: 1, MatchDate: "2027-05-02"}, {ID: 2, CompetitionID: 2, GroundID: 1, MatchDate: "2027-05-02"}, {ID: 3, CompetitionID: 1, GroundID: 2, MatchDate: "2027-05-02"}, {ID: 4, CompetitionID: 1, GroundID: 1, MatchDate: "2027-05-03"}}
	if got := GroundDay(all, "2027-05-02", 1); len(got) != 2 || got[1].ID != 2 {
		t.Fatalf("ground context %+v", got)
	}
	if got := DivisionDay(all, "2027-05-02", 1); len(got) != 2 || got[1].GroundID != 2 {
		t.Fatalf("division excluded other ground %+v", got)
	}
}
func TestEntryGradingValidation(t *testing.T) {
	entries := []TeamEntry{{Name: "A", AgeGroup: "U9", RequestedGrade: " Beginner ", Zone: " Oldham ", GradingNotes: strings.Repeat("x", 1800)}, {Name: "B", AgeGroup: "U9", GradingNotes: strings.Repeat("y", 1800)}, {Name: "C", AgeGroup: "U9", GradingNotes: strings.Repeat("z", 1800)}}
	raw, _ := json.Marshal(entries)
	if err := ValidateFormValues([]FormField{{Key: "teams", Type: "team_entries", Required: true}}, map[string]string{"teams": string(raw)}, true); err != nil {
		t.Fatalf("valid multiple grading notes rejected: %v", err)
	}
	parsed, err := ParseTeamEntries(string(raw), true)
	if err != nil || parsed[0].Zone != "Oldham" || parsed[0].RequestedGrade != "Beginner" {
		t.Fatalf("grading: %+v %v", parsed, err)
	}
	entries[0].GradingNotes = strings.Repeat("x", 2001)
	raw, _ = json.Marshal(entries)
	if _, err = ParseTeamEntries(string(raw), true); err == nil {
		t.Fatal("overlong grading notes accepted")
	}
	entries[0].GradingNotes = ""
	entries[0].Zone = "Oldham\x00"
	raw, _ = json.Marshal(entries)
	if _, err = ParseTeamEntries(string(raw), true); err == nil {
		t.Fatal("invalid zone accepted")
	}
}

func TestExternalBookingInput(t *testing.T) {
	base := ExternalGroundBooking{GroundID: 1, Title: "Women’s fixture", Category: "women", MatchDate: "2027-05-02", StartTime: "9:00", DurationMinutes: 60, Source: "Club confirmation"}
	if err := validateExternalBooking(&base); err != nil || base.StartTime != "09:00" {
		t.Fatalf("normalise time: %+v %v", base, err)
	}
	for _, change := range []func(*ExternalGroundBooking){
		func(b *ExternalGroundBooking) { b.StartTime = "bad" },
		func(b *ExternalGroundBooking) { b.MatchDate = "2027-02-30" },
		func(b *ExternalGroundBooking) { b.DurationMinutes = 0 },
		func(b *ExternalGroundBooking) { b.StartTime = "23:30"; b.DurationMinutes = 60 },
		func(b *ExternalGroundBooking) { b.Category = "junior" },
		func(b *ExternalGroundBooking) { b.Source = "" },
	} {
		b := base
		change(&b)
		if err := validateExternalBooking(&b); err == nil {
			t.Fatalf("invalid booking accepted: %+v", b)
		}
	}
}
