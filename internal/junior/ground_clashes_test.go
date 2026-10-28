package junior

import (
	"strings"
	"testing"
)

func TestSavedGroundLayouts(t *testing.T) {
	fixture := func(id int64, age string) Fixture {
		return Fixture{ID: id, CompetitionID: id, GroundID: 1, GroundName: "Fictional shared ground", GroundPostcode: "ZZ1 1AA", MatchDate: "2027-05-01", StartTime: "18:00", DurationMinutes: 60, AgeGroup: age, GroundLayouts: []GroundLayout{{"U11": 2}, {"U15": 1}}}
	}
	a, b := fixture(1, "U11"), fixture(2, "U11")
	tests := []struct {
		name       string
		own, other []Fixture
		clash      bool
	}{
		{"two U11 fit", []Fixture{a}, []Fixture{b}, false},
		{"two U15 clash", []Fixture{fixture(1, "U15")}, []Fixture{fixture(2, "U15")}, true},
		{"mixed ages need explicit layout", []Fixture{a}, []Fixture{fixture(2, "U15")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckGroundLayouts(tc.own, tc.other)
			if (len(got) > 0) != tc.clash {
				t.Fatalf("issues=%+v", got)
			}
			if tc.clash && (!got[0].Blocking || !strings.Contains(got[0].Message, "ZZ1 1AA")) {
				t.Fatalf("unclear warning: %+v", got)
			}
		})
	}
	b.AgeGroup = "U15"
	a.GroundLayouts = []GroundLayout{{"U11": 1, "U15": 1}}
	b.GroundLayouts = a.GroundLayouts
	if got := CheckGroundLayouts([]Fixture{a}, []Fixture{b}); len(got) > 0 {
		t.Fatalf("explicit mixed layout: %+v", got)
	}
	a = fixture(1, "U15")
	b = fixture(2, "U15")
	b.StartTime = "19:00"
	if got := CheckGroundLayouts([]Fixture{a}, []Fixture{b}); len(got) > 0 {
		t.Fatalf("back-to-back: %+v", got)
	}
	b.StartTime = "18:00"
	b.GroundID = 2
	if got := CheckGroundLayouts([]Fixture{a}, []Fixture{b}); len(got) > 0 {
		t.Fatalf("distinct venue ID, same postcode: %+v", got)
	}
	b.GroundID = 1
	b.GroundName = "Renamed venue"
	if got := CheckGroundLayouts([]Fixture{a}, []Fixture{b}); len(got) == 0 {
		t.Fatal("renaming hid shared venue clash")
	}
	// Current official and tentative copies reserve the union of their intervals,
	// without counting a single match twice during their overlap.
	copy := a
	copy.BookingState = "published"
	copy.StartTime = "18:30"
	if got := CheckGroundLayouts([]Fixture{a}, []Fixture{copy}); len(got) > 0 {
		t.Fatalf("same match counted twice: %+v", got)
	}
}

func TestGroundInputValidation(t *testing.T) {
	g := Ground{Name: "Demo", Address: "1 Fictional Lane", Postcode: " zz11aa ", Source: "Fictional demonstration only", RulesConfirmed: true, Layouts: []GroundLayout{{"U11": 2}, {"U15": 1}}}
	if err := validateGround(&g); err != nil || g.Postcode != "ZZ1 1AA" {
		t.Fatalf("normalisation: %+v %v", g, err)
	}
	g.RulesConfirmed = false
	if err := validateGround(&g); err == nil {
		t.Fatal("unconfirmed layouts accepted")
	}
	g.RulesConfirmed = true
	g.Layouts = []GroundLayout{{"U9": 2}}
	if err := validateGround(&g); err == nil {
		t.Fatal("U9 rules should be deferred")
	}
}
