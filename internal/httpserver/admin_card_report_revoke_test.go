package httpserver

import (
	"os"
	"strings"
	"testing"
)

func TestWeeklyCardReportOffersAuditedRemovalForIssuedYellow(t *testing.T) {
	id := int64(124)
	html := weeklyCardReportRevokeButton(weeklyCardReportRow{
		ExistingSanctionID: &id,
		ExistingCard:       "yellow",
		ExistingReason:     "non_submission",
		ExistingStatus:     "active",
	})
	for _, want := range []string{
		`formaction="/admin/sanctions/124/revoke-yellow"`,
		`name="reason"`,
		"Remove mistaken yellow",
		"retain it in the audit history",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("weekly report removal control missing %q: %s", want, html)
		}
	}
}

func TestYellowRemovalClosesLinkedCaseInsteadOfOnlyHidingLegacyRow(t *testing.T) {
	handler, err := os.ReadFile("admin_sanctions.go")
	if err != nil {
		t.Fatal(err)
	}
	service, err := os.ReadFile("../sanctions/service.go")
	if err != nil {
		t.Fatal(err)
	}
	for label, source := range map[string]string{"handler": string(handler), "service": string(service)} {
		for _, want := range map[string][]string{
			"handler": {"SELECT case_id FROM sanctions", "OverturnCase(ctx, *linkedCaseID", "linked case closed"},
			"service": {"'reversal'", "public_status='overturned'", "THEN 'skipped'"},
		}[label] {
			if !strings.Contains(source, want) {
				t.Fatalf("%s does not preserve the linked-case removal step %q", label, want)
			}
		}
	}
}

func TestWeeklyCardReportDoesNotOfferYellowRemovalForOtherRows(t *testing.T) {
	id := int64(124)
	for _, row := range []weeklyCardReportRow{
		{ExistingCard: "yellow", ExistingReason: "non_submission", ExistingStatus: "active"},
		{ExistingSanctionID: &id, ExistingCard: "red", ExistingReason: "non_submission", ExistingStatus: "active"},
		{ExistingSanctionID: &id, ExistingCard: "yellow", ExistingReason: "manual", ExistingStatus: "active"},
		{ExistingSanctionID: &id, ExistingCard: "yellow", ExistingReason: "non_submission", ExistingStatus: "overturned"},
	} {
		if html := weeklyCardReportRevokeButton(row); html != "" {
			t.Fatalf("unexpected removal control for %+v: %s", row, html)
		}
	}
}
