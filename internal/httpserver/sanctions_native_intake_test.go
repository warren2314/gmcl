package httpserver

import (
	"strings"
	"testing"
)

func TestNativeIntakeEvidenceLinksExposeOnlyRetainedNativeFiles(t *testing.T) {
	sha := strings.Repeat("a", 64)
	raw := []byte(`{"File Upload":[{"original_name":"registration<check>.pdf","media_type":"application/pdf","byte_size":4321,"sha256":"` + sha + `","storage_key":"private-copy"}]}`)
	html := nativeIntakeEvidenceLinks(raw, 19, 2)
	for _, want := range []string{
		"Retained source files",
		"registration&lt;check&gt;.pdf",
		"SHA-256 " + sha,
		"/admin/ineligible/19/native-evidence/2/" + sha,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("evidence HTML does not contain %q: %s", want, html)
		}
	}
	if strings.Contains(html, "private-copy") {
		t.Fatal("private storage keys must not be exposed in the admin link")
	}
}

func TestNativeIntakeEvidenceLinksIgnoreGoogleDriveCellText(t *testing.T) {
	raw := []byte(`{"File Upload":"https://drive.google.test/file/123"}`)
	if html := nativeIntakeEvidenceLinks(raw, 19, 1); html != "" {
		t.Fatalf("non-native file cell unexpectedly rendered: %s", html)
	}
}

func TestNativeIneligibleLengthsValid(t *testing.T) {
	valid := []string{
		"Reporter", "reporter@example.com", "Secretary", "07700 900123",
		"Reporting CC", "Offending CC", "Offending CC 1st XI", "Player",
		"Reason", "Info", "Evidence", "Score",
	}
	if !nativeIneligibleLengthsValid(valid...) {
		t.Fatal("valid native intake lengths were rejected")
	}
	valid[8] = strings.Repeat("x", 10001)
	if nativeIneligibleLengthsValid(valid...) {
		t.Fatal("oversized allegation was accepted")
	}
}
