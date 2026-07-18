package httpserver

import "testing"

func TestNormaliseRulesScope(t *testing.T) {
	got, ok := normaliseRulesScope(" Junior ", "CUP")
	if !ok || got != "Junior rules; Cup match" {
		t.Fatalf("scope=%q ok=%v", got, ok)
	}
	if _, ok := normaliseRulesScope("academy", "cup"); ok {
		t.Fatal("invalid scope was accepted")
	}
}
