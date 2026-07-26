package portal

import "testing"

func TestParseFeatureKey(t *testing.T) {
	if key, ok := ParseFeatureKey(" READ_ONLY_DASHBOARD "); !ok || key != FeatureReadOnlyDashboard {
		t.Fatalf("feature key = %q, %v", key, ok)
	}
	if _, ok := ParseFeatureKey("publish_everything"); ok {
		t.Fatal("unknown feature key accepted")
	}
}

func TestClubReconciliationRequiresAtLeastOneMappedTeam(t *testing.T) {
	tests := []struct {
		name    string
		summary ClubReconciliationSummary
		want    bool
	}{
		{"no teams", ClubReconciliationSummary{}, false},
		{"partial", ClubReconciliationSummary{ActiveTeams: 2, MappedActiveTeams: 1}, false},
		{"complete", ClubReconciliationSummary{ActiveTeams: 2, MappedActiveTeams: 2}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.summary.TeamMappingsComplete(); got != test.want {
				t.Fatalf("TeamMappingsComplete() = %v, want %v", got, test.want)
			}
		})
	}
}
