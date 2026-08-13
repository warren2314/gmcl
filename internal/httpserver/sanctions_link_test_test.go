package httpserver

import (
	"strings"
	"testing"
)

func TestPrivateLinkTestDeliveryErrorHTML(t *testing.T) {
	if got := testDeliveryErrorHTML(" "); got != "" {
		t.Fatalf("empty delivery error rendered as %q", got)
	}
	got := testDeliveryErrorHTML(`<script>alert("x")</script>`)
	if strings.Contains(got, "<script>") || !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("delivery error was not safely escaped: %s", got)
	}
}

func TestPrivateLinkTestUsesUnmistakablePhrase(t *testing.T) {
	if privateLinkTestResponse != "GMCL LINK TEST COMPLETE" {
		t.Fatalf("unexpected test phrase %q", privateLinkTestResponse)
	}
}
