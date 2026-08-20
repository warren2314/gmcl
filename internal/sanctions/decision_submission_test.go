package sanctions

import "testing"

func TestCanSubmitDecisionForApprovalUsesCurrentOwner(t *testing.T) {
	ownerID := int32(17)
	otherID := int32(23)

	if !CanSubmitDecisionForApproval("decision_proposed", &ownerID, &ownerID) {
		t.Fatal("current case owner cannot submit the proposed decision")
	}
	for _, test := range []struct {
		name     string
		status   string
		assigned *int32
		actor    *int32
	}{
		{name: "wrong status", status: "investigating", assigned: &ownerID, actor: &ownerID},
		{name: "different administrator", status: "decision_proposed", assigned: &ownerID, actor: &otherID},
		{name: "unassigned", status: "decision_proposed", actor: &ownerID},
		{name: "unauthenticated", status: "decision_proposed", assigned: &ownerID},
	} {
		t.Run(test.name, func(t *testing.T) {
			if CanSubmitDecisionForApproval(test.status, test.assigned, test.actor) {
				t.Fatal("non-owner submission was allowed")
			}
		})
	}
}

func TestCanAmendProposedDecisionStopsAtIndependentApproval(t *testing.T) {
	ownerID := int32(17)
	otherID := int32(23)

	if !CanAmendProposedDecision("decision_proposed", &ownerID, &ownerID, false) {
		t.Fatal("current case owner cannot amend the proposal before independent approval")
	}
	for _, test := range []struct {
		name     string
		status   string
		assigned *int32
		actor    *int32
		sent     bool
	}{
		{name: "already sent", status: "decision_proposed", assigned: &ownerID, actor: &ownerID, sent: true},
		{name: "wrong status", status: "investigating", assigned: &ownerID, actor: &ownerID},
		{name: "different administrator", status: "decision_proposed", assigned: &ownerID, actor: &otherID},
		{name: "unassigned", status: "decision_proposed", actor: &ownerID},
		{name: "unauthenticated", status: "decision_proposed", assigned: &ownerID},
	} {
		t.Run(test.name, func(t *testing.T) {
			if CanAmendProposedDecision(test.status, test.assigned, test.actor, test.sent) {
				t.Fatal("proposal amendment was allowed outside the owner review stage")
			}
		})
	}
}

func TestOutcomeLetterSignatoryIsDenver(t *testing.T) {
	if outcomeLetterSignatoryName != "Denver Thornton" {
		t.Fatalf("outcome letter signatory = %q, want Denver Thornton", outcomeLetterSignatoryName)
	}
}
