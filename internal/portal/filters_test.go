package portal

import (
	"testing"
	"time"
)

func TestChooseSeasonRespectsAssignmentAndRequest(t *testing.T) {
	options := []SeasonOption{
		{ID: 2026, StartDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)},
		{ID: 2025, StartDate: time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2025, 9, 30, 0, 0, 0, 0, time.UTC)},
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	assigned := int32(2025)
	requested2026 := int32(2026)
	if got := chooseSeason(options, &assigned, &requested2026, now); got != 0 {
		t.Fatalf("cross-season appointment broadening returned %d", got)
	}
	requested2025 := int32(2025)
	if got := chooseSeason(options, &assigned, &requested2025, now); got != 2025 {
		t.Fatalf("assigned season returned %d", got)
	}
	if got := chooseSeason(options, nil, nil, now); got != 2026 {
		t.Fatalf("current season returned %d", got)
	}
	if got := chooseSeason(options, nil, &requested2025, now); got != 2025 {
		t.Fatalf("historical season returned %d", got)
	}
}

func TestFilterContainmentHelpers(t *testing.T) {
	if !containsSeason([]SeasonOption{{ID: 7}}, 7) || containsSeason([]SeasonOption{{ID: 7}}, 8) {
		t.Fatal("season containment failed")
	}
	if !containsTeam([]TeamOption{{ID: 11}}, 11) || containsTeam([]TeamOption{{ID: 11}}, 12) {
		t.Fatal("team containment failed")
	}
}
