package rulesassistant

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncatePreservesUTF8Boundaries(t *testing.T) {
	value := strings.Repeat("a", 299) + "› registration rule"
	got := truncate(value, 300)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate returned invalid UTF-8: %x", []byte(got))
	}
	if got != strings.Repeat("a", 299) {
		t.Fatalf("truncate split a multi-byte character: length=%d suffix=%q", len(got), got[max(0, len(got)-5):])
	}
}

func TestTruncateHandlesLimitsAndASCII(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc" {
		t.Fatalf("ASCII truncate=%q", got)
	}
	if got := truncate("abc", 0); got != "" {
		t.Fatalf("zero-limit truncate=%q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Fatalf("short input truncate=%q", got)
	}
}
