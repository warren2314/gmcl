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
