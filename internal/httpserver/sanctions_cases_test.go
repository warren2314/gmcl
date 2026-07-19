package httpserver

import (
	"net/url"
	"reflect"
	"testing"
)

func TestSanctionsSearchPatternsTreatAndAsAmpersand(t *testing.T) {
	for input, want := range map[string][]string{
		"Deane and Derby": {"%Deane and Derby%", "%deane & derby%"},
		"Deane & Derby":   {"%Deane & Derby%", "%deane and derby%"},
	} {
		if got := sanctionsSearchPatterns(input); !reflect.DeepEqual(got, want) {
			t.Errorf("sanctionsSearchPatterns(%q) = %#v, want %#v", input, got, want)
		}
	}
}

func TestSanctionsCategoryURLPreservesSearchAndClearsType(t *testing.T) {
	values := url.Values{"q": {"Deane and Derby"}, "season": {"2026"}, "type": {"fine"}}
	got := sanctionsCategoryURL(values, "yellow")
	want := "/sanctions?q=Deane+and+Derby&season=2026&view=yellow"
	if got != want {
		t.Fatalf("category URL = %q, want %q", got, want)
	}
}
