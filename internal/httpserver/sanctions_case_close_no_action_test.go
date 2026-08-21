package httpserver

import (
	"strings"
	"testing"
)

func TestAdminCloseCaseNoActionHTML(t *testing.T) {
	owner, other := int32(7), int32(8)
	html := adminCloseCaseNoActionHTML(42, `token"value`, "investigating", false, &owner, &owner)
	for _, want := range []string{"/admin/cases/42/close-no-action", "Close case with no action", "goes straight to", "token&quot;value", "no sanction, approval request or outcome letter"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in %s", want, html)
		}
	}
	for _, test := range []struct {
		status      string
		hasProposed bool
		assigned    *int32
		actor       *int32
	}{
		{status: "approved", assigned: &owner, actor: &owner},
		{status: "investigating", assigned: &other, actor: &owner},
		{status: "investigating", assigned: nil, actor: &owner},
	} {
		if got := adminCloseCaseNoActionHTML(42, "csrf", test.status, test.hasProposed, test.assigned, test.actor); got != "" {
			t.Fatalf("unexpected close control for %+v: %s", test, got)
		}
	}
	for _, test := range []struct {
		status      string
		hasProposed bool
	}{
		{status: "decision_proposed", hasProposed: true},
		{status: "investigating", hasProposed: true},
	} {
		if got := adminCloseCaseNoActionHTML(42, "csrf", test.status, test.hasProposed, &owner, &owner); got == "" {
			t.Fatalf("missing close control for %+v", test)
		}
	}
}

func TestCloseCaseNoActionErrorMessageIdentifiesFailedStage(t *testing.T) {
	message := closeCaseNoActionErrorMessage("cancel_response_request", "request-123")
	for _, want := range []string{"case was not changed", "response window", "request-123"} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in %q", want, message)
		}
	}
}

func TestCloseCaseNoActionErrorMessageDoesNotRequireRequestID(t *testing.T) {
	message := closeCaseNoActionErrorMessage("unexpected", " ")
	if !strings.Contains(message, "case was not changed") {
		t.Fatalf("unexpected message %q", message)
	}
	if strings.Contains(message, "support reference") {
		t.Fatalf("unexpected empty support reference in %q", message)
	}
}
