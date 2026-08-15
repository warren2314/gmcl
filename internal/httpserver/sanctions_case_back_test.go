package httpserver

import "testing"

func TestAdminCaseBackDestinationReturnsOwnerToMyCases(t *testing.T) {
	adminID := int32(27)
	for _, source := range []string{"ineligible_player", "manual", "discipline"} {
		label, destination := adminCaseBackDestination(source, &adminID, &adminID)
		if label != "Back to my cases" {
			t.Fatalf("source=%q label=%q want Back to my cases", source, label)
		}
		if destination != "/admin/dashboard#my-cases" {
			t.Fatalf("source=%q destination=%q want personal dashboard case section", source, destination)
		}
	}
}

func TestAdminCaseBackDestinationDoesNotExposeAnotherOwnersQueue(t *testing.T) {
	ownerID := int32(27)
	currentID := int32(31)
	for name, testCase := range map[string]struct {
		source   string
		assigned *int32
		current  *int32
	}{
		"another investigator":  {source: "ineligible_player", assigned: &ownerID, current: &currentID},
		"unassigned":            {source: "ineligible_player", assigned: nil, current: &currentID},
		"missing current admin": {source: "manual", assigned: &ownerID, current: nil},
	} {
		t.Run(name, func(t *testing.T) {
			label, destination := adminCaseBackDestination(testCase.source, testCase.assigned, testCase.current)
			if label != "Back to cases" || destination != "/admin/cases" {
				t.Fatalf("got (%q,%q), want general case list", label, destination)
			}
		})
	}
}

func TestAdminCaseListSubjectPrefersPlayerAndKeepsContext(t *testing.T) {
	primary, context := adminCaseListSubject("  Alex Player ", "Example CC", "3rd XI")
	if primary != "Alex Player" || context != "Example CC / 3rd XI" {
		t.Fatalf("got (%q,%q), want player with club/team context", primary, context)
	}
}

func TestAdminCaseListSubjectFallsBackToClubAndTeam(t *testing.T) {
	primary, context := adminCaseListSubject("", "Example CC", "3rd XI")
	if primary != "Example CC" || context != "3rd XI" {
		t.Fatalf("got (%q,%q), want club and team fallback", primary, context)
	}
}
