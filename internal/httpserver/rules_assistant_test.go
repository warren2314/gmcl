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
