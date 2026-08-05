package httpserver

import "testing"

func TestConfiguredPrivateGoogleFormURL(t *testing.T) {
	t.Setenv("INELIGIBLE_PRIVATE_GOOGLE_FORM_URL", "https://docs.google.com/forms/d/e/example/viewform")
	got, err := configuredPrivateGoogleFormURL()
	if err != nil || got != "https://docs.google.com/forms/d/e/example/viewform" {
		t.Fatalf("configured URL: got %q err %v", got, err)
	}

	for _, invalid := range []string{"", "http://docs.google.com/form", "https://user:pass@example.com/form", "/relative"} {
		t.Run(invalid, func(t *testing.T) {
			t.Setenv("INELIGIBLE_PRIVATE_GOOGLE_FORM_URL", invalid)
			if _, err := configuredPrivateGoogleFormURL(); err == nil {
				t.Fatalf("invalid private form URL %q was accepted", invalid)
			}
		})
	}
}
