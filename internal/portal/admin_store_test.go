package portal

import (
	"strings"
	"testing"
)

func TestParseFeatureKey(t *testing.T) {
	for _, expected := range []FeatureKey{
		FeaturePortalAccess,
		FeatureReadOnlyDashboard,
		FeatureSecureMessaging,
		FeatureClubSelfService,
		FeatureJuniorAdministration,
		FeaturePlayerIdentity,
		FeatureRegistration,
		FeatureFixtureOptimisation,
	} {
		key, ok := ParseFeatureKey(" " + strings.ToUpper(string(expected)) + " ")
		if !ok || key != expected {
			t.Fatalf("feature key = %q, %v; want %q", key, ok, expected)
		}
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
