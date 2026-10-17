package httpserver

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in visual fixtures use the production components with explicit synthetic
// context. They do not start the app, connect to a database, or enable senders.
// They are not a full-handler, authentication, data or deployment walkthrough.
func TestGenerateCaseWorkspaceComponentFixtures(t *testing.T) {
	directory := os.Getenv("GMCL_CASE_UI_FIXTURE_DIR")
	if directory == "" {
		t.Skip("GMCL_CASE_UI_FIXTURE_DIR is not configured")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	delivered := now.Add(-6 * 24 * time.Hour)
	reminder := delivered.Add(5 * 24 * time.Hour)
	deadline := now.Add(24 * time.Hour)
	for _, fixture := range []struct {
		name string
		view adminCaseActionContext
	}{
		{"owner-notice-review", adminCaseActionContext{Source: "ineligible_player", Status: "decision_proposed", OwnerName: "Synthetic case owner", IsOwner: true, CanPropose: true, CanInvestigate: true, HasProposed: true, OwnerReviewRequired: true}},
		{"decision-approval", adminCaseActionContext{Source: "ineligible_player", Status: "decision_proposed", OwnerName: "Synthetic case owner", IsOwner: true, CanApprove: true, HasProposed: true, OwnerReviewRequired: true, SentForApproval: true}},
		{"final-issuer", adminCaseActionContext{Source: "ineligible_player", Status: "approved", OwnerName: "Synthetic case owner", CanIssue: true}},
		{"changed-source", adminCaseActionContext{Source: "ineligible_player", Status: "approved", OwnerName: "Synthetic case owner", SourceReviewRequired: true, CanReviewSource: true, CanIssue: true}},
		{"awaiting-response", adminCaseActionContext{Source: "ineligible_player", Status: "response_pending", OwnerName: "Synthetic case owner", CanInvestigate: true, HasResponseRequest: true, ResponseWindow: adminCaseResponseWindowView{ID: 1, Status: "pending", DeliveredAt: &delivered, ReminderDueAt: &reminder, DueAt: &deadline}}},
		{"delivery-problem", adminCaseActionContext{Source: "ineligible_player", Status: "investigating", OwnerName: "Synthetic case owner", CanInvestigate: true, DeliveryException: true}},
		{"completed-delivery", adminCaseActionContext{Source: "ineligible_player", Status: "closed", OwnerName: "Synthetic case owner", CanInvestigate: true, DeliveryException: true}},
		{"unavailable", adminCaseActionContext{Source: "ineligible_player", Status: "approved", OwnerName: "Synthetic case owner", CanIssue: true, Unavailable: true}},
	} {
		w := httptest.NewRecorder()
		pageHead(w, "Synthetic case component fixture")
		writeAdminNav(w, "visual-fixture-no-submit", "/admin/cases/9420", "admin")
		fmt.Fprint(w, `<main class="container py-4" style="max-width:1280px"><div class="alert alert-info"><strong>Synthetic component fixture.</strong> Production UI components with invented test context, not a complete case handler or production record. Forms cannot submit.</div><h1 class="h2">Case UI fixture</h1>`)
		fmt.Fprint(w, adminCaseNextActionHTML(selectAdminCaseNextAction(fixture.view, now), fixture.view, loc))
		fmt.Fprint(w, `<section id="case-actions"><h2 class="h4">Current work</h2>`)
		if fixture.view.Status == "response_pending" {
			fmt.Fprint(w, adminCaseResponseWindowHTMLAt(9420, "visual-fixture-no-submit", fixture.view.ResponseWindow, fixture.view.Status, loc, now, adminCaseWorkspacePermissions{CanInvestigate: fixture.view.CanInvestigate}))
		}
		if fixture.view.Status == "decision_proposed" {
			fmt.Fprint(w, `<div id="case-decision">`)
			fmt.Fprint(w, adminCaseDecisionHTML(adminCaseDecision{Status: "proposed", Revision: 1, PublicReason: "Synthetic finding for component layout review.", RuleReference: "Synthetic rule reference"}, nil))
			fmt.Fprint(w, `</div>`)
			if fixture.view.OwnerReviewRequired && !fixture.view.SentForApproval {
				fmt.Fprint(w, `<section id="outcome-notice-review"><p class="text-muted">Audience notice previews are loaded by the complete case handler and are not simulated here.</p>`)
				fmt.Fprint(w, adminCaseOwnerReviewSubmitHTML(9420, "visual-fixture-no-submit"))
				fmt.Fprint(w, `</section>`)
			}
		}
		(&Server{}).writeAdminCaseWorkspaceDecisionActions(w, httptest.NewRequest("GET", "/admin/cases/9420", nil), 9420, "visual-fixture-no-submit", fixture.view, fixture.view.CanApprove)
		fmt.Fprint(w, `</section><details class="card mb-4" id="case-history"><summary class="card-header">History placeholder</summary><div class="card-body">The full handler supplies real case events.</div></details><details class="card mb-4" id="case-delivery"><summary class="card-header">Delivery placeholder</summary><div class="card-body">The full handler supplies recipient, attempt and retry records.</div></details></main><script>document.addEventListener('submit',function(event){event.preventDefault();});</script>`)
		pageFooter(w)
		path := filepath.Join(directory, "case-detail-"+fixture.name+".html")
		if err := os.WriteFile(path, w.Body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
		t.Log(path)
	}
}
