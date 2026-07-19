package httpserver

import "testing"

func TestRulesAssistantEnabledDefaultsOff(t *testing.T) {
	t.Setenv("RULES_ASSISTANT_ENABLED", "")
	if rulesAssistantEnabled() {
		t.Fatal("rules assistant must default to disabled")
	}
}

func TestRulesAssistantEnabledExplicitly(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RULES_ASSISTANT_ENABLED", value)
			if !rulesAssistantEnabled() {
				t.Fatalf("expected %q to enable rules assistant", value)
			}
		})
	}
}

func TestNormaliseRulesScope(t *testing.T) {
	got, ok := normaliseRulesScope(" Junior ", "CUP")
	if !ok || got != "Junior rules; Cup match" {
		t.Fatalf("scope=%q ok=%v", got, ok)
	}
	if _, ok := normaliseRulesScope("academy", "cup"); ok {
		t.Fatal("invalid scope was accepted")
	}
}

func TestSanctionLookupRecognisesGenericCardQuestion(t *testing.T) {
	if !isSanctionLookupQuestion("Why does Woodley have cards?") {
		t.Fatal("generic card question was not recognised as a sanctions lookup")
	}
}

func TestMatchSanctionLookupClubUsesNamedLongestMatch(t *testing.T) {
	clubs := []importClub{
		{ID: 1, Name: "Bolton Cricket Club"},
		{ID: 2, Name: "Bolton Indians Cricket Club"},
		{ID: 3, Name: "Woodley Cricket Club"},
	}
	got, ok := matchSanctionLookupClub("Why does Bolton Indians have cards?", clubs)
	if !ok || got.ID != 2 {
		t.Fatalf("matched club = %#v, ok=%v; want Bolton Indians", got, ok)
	}
	if _, ok = matchSanctionLookupClub("Why does this team have cards?", clubs); ok {
		t.Fatal("question without a named club unexpectedly matched")
	}
}
