package httpserver

import "testing"

func TestAdminCaseBackDestinationReturnsOwnerToMyCases(t *testing.T) {
	adminID := int32(27)
	label, destination := adminCaseBackDestination("ineligible_player", &adminID, &adminID)
	if label != "Back to my cases" {
		t.Fatalf("label=%q want Back to my cases", label)
	}
	if destination != "/admin/ineligible?scope=mine&state=all" {
		t.Fatalf("destination=%q want personal ineligible-player queue", destination)
	}
}

func TestAdminCaseBackDestinationDoesNotExposeAnotherOwnersQueue(t *testing.T) {
	ownerID := int32(27)
	currentID := int32(31)
	for name, source, assigned, current := range map[string]struct {
		source   string
		assigned *int32
		current  *int32
	}{
		"another investigator": {source: "ineligible_player", assigned: &ownerID, current: &currentID},
		"unassigned":           {source: "ineligible_player", assigned: nil, current: &currentID},
		"ordinary case":        {source: "manual", assigned: &currentID, current: &currentID},
	} {
		t.Run(name, func(t *testing.T) {
			label, destination := adminCaseBackDestination(source, assigned, current)
			if label != "Back to cases" || destination != "/admin/cases" {
				t.Fatalf("got (%q,%q), want general case list", label, destination)
			}
		})
	}
}
