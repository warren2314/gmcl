package httpserver

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestAdminCaseDecisionHTMLShowsProposedPunishment(t *testing.T) {
	points := 2
	starts := time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC)
	html := adminCaseDecisionHTML(adminCaseDecision{
		Status:        "proposed",
		Revision:      1,
		PublicReason:  "Failure to submit captain's report",
		RuleReference: "Penalty rule 3",
	}, []adminCaseEffect{{
		EffectType:         "red_card",
		Status:             "pending",
		Points:             &points,
		StartsAt:           &starts,
		CountsForTotting:   true,
		Explanation:        "Yellow card 3 converts to red card 2 with a 2-point deduction.",
		YellowBalanceAfter: "0",
		TeamRedCountAfter:  "2",
	}})
	for _, want := range []string{
		"Proposed punishment",
		"Red card",
		"2 points",
		"Yellow balance after",
		"Team red count after",
		"Counts towards card totting",
		"Penalty rule 3",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("decision HTML does not contain %q: %s", want, html)
		}
	}
}

func TestAdminCaseAssignmentHidesDuplicateSelfAssignment(t *testing.T) {
	adminID := int32(42)
	html := adminCaseAssignmentHTML(152, "token", &adminID, "warren2314", &adminID)
	if !strings.Contains(html, "assigned to you") {
		t.Fatalf("self-assignment status missing: %s", html)
	}
	if strings.Contains(html, "assign-self") || strings.Contains(html, "<button") {
		t.Fatalf("self-assignment action should be hidden: %s", html)
	}
	if !sameAdminAssignment(&adminID, &adminID) {
		t.Fatal("duplicate assignment must be recognised as unchanged")
	}
}

func TestAdminCaseAssignmentAllowsExplicitReassignment(t *testing.T) {
	assignedID := int32(7)
	currentID := int32(42)
	html := adminCaseAssignmentHTML(152, "token", &assignedID, "joe", &currentID)
	for _, want := range []string{"Current investigator:", "joe", "Reassign investigation to me", "/admin/cases/152/assign-self"} {
		if !strings.Contains(html, want) {
			t.Fatalf("reassignment HTML does not contain %q: %s", want, html)
		}
	}
	if sameAdminAssignment(&assignedID, &currentID) {
		t.Fatal("different investigators must not be treated as the same assignment")
	}
}
