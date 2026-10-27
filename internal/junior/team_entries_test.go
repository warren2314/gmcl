package junior

import "testing"

func TestTeamEntryIdentity(t *testing.T) {
	for _, raw := range []string{`[{"name":"A","age_group":"u13"}]`, `[{"name":"  U13   A  ","age_group":"U13"}]`} {
		entries, err := ParseTeamEntries(raw, true)
		if err != nil || entries[0].Name != "U13 A" {
			t.Fatalf("normalisation: %#v %v", entries, err)
		}
	}
	for _, raw := range []string{`[]`, `null`, `not json`, `[{"name":"U11 A","age_group":"U13"}]`, `[{"name":"A","age_group":"U13"},{"name":"u13  a","age_group":"U13"}]`, `[{"name":"","age_group":"U13"}]`} {
		if _, err := ParseTeamEntries(raw, true); err == nil {
			t.Fatalf("accepted invalid team rows: %s", raw)
		}
	}
}
