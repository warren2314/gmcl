package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWritePersonalWorkDashboardShowsDirectUserActions(t *testing.T) {
	loc := time.FixedZone("Europe/London", 3600)
	now := time.Date(2026, time.August, 13, 9, 0, 0, 0, loc)
	due := now.Add(-24 * time.Hour)
	data := personalWorkDashboard{
		AdminName:     "Warren <Exec>",
		AssignedTotal: 1,
		AssignedCases: []personalWorkCase{{
			ID:        1176,
			Reference: "GMCL-2026-001176",
			Status:    "investigating",
			Player:    "Ronnie Harris",
			Club:      "Mottram CC",
			UpdatedAt: now,
		}},
		ResponseTotal: 1,
		Responses: []personalWorkResponse{{
			CaseID:     1176,
			Reference:  "GMCL-2026-001176",
			Player:     "Ronnie Harris",
			Club:       "Mottram CC",
			ReceivedAt: now,
		}},
		TaskTotal: 1,
		Tasks: []personalWorkTask{{
			ID:        42,
			CaseID:    1176,
			Reference: "GMCL-2026-001176",
			Note:      "Check registration",
			DueAt:     &due,
		}},
		DecisionTotal: 1,
		CanApprove:    true,
		DecisionQueue: []personalWorkQueueItem{{
			CaseID:    1177,
			Reference: "GMCL-2026-001177",
			Action:    "Review decision",
			Club:      "Springhead CCC",
			Player:    "Irfan Aktar",
		}},
	}

	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, data, loc, now)
	html := output.Body.String()
	for _, want := range []string{
		"Good morning, Warren &lt;Exec&gt;",
		"Responses awaiting review",
		`href="/admin/cases/1176#club-response"`,
		`id="my-cases"`,
		"Decisions needing my role",
		"Approval / issue queue",
		`href="/admin/cases/1177"`,
		"2 need attention",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard output missing %q", want)
		}
	}
	if strings.Contains(html, "Warren <Exec>") {
		t.Fatal("administrator name was not escaped")
	}
	for _, unwanted := range []string{"My tasks", "Tasks assigned to me", "supporting tasks", `id="my-tasks"`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("dashboard output unexpectedly contains %q", unwanted)
		}
	}
	caseIndex := strings.Index(html, `id="my-cases"`)
	responseIndex := strings.Index(html, `id="my-responses"`)
	decisionIndex := strings.Index(html, `id="my-decisions"`)
	if caseIndex < 0 || responseIndex < 0 || decisionIndex < 0 || !(caseIndex < responseIndex && responseIndex < decisionIndex) {
		t.Fatalf("dashboard panels are not ordered cases, responses, decisions: cases=%d responses=%d decisions=%d", caseIndex, responseIndex, decisionIndex)
	}
	if !strings.Contains(html, `<div class="col-12" id="my-decisions">`) {
		t.Fatal("decision queue should sit below the two primary investigator panels")
	}
}

func TestWritePersonalWorkDashboardShowsCase194OnlyInTrainingSection(t *testing.T) {
	data := personalWorkDashboard{
		AdminName: "denver",
		TestTotal: 1,
		TestCases: []personalWorkQueueItem{{
			CaseID: 194, Reference: "GMCL-2026-001193", Action: "Review decision",
			Player: "Warren Phillips", Club: "Example CC",
		}},
	}
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, data, time.UTC, time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC))
	html := output.Body.String()
	for _, want := range []string{
		`id="test-cases"`,
		"Test cases - training only",
		"Case 194 - GMCL-2026-001193",
		`href="/admin/cases/194"`,
		"0 need attention",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("training dashboard output missing %q", want)
		}
	}
}

func TestWritePersonalWorkDashboardHasClearEmptyStates(t *testing.T) {
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, personalWorkDashboard{AdminName: "Alex"}, time.UTC, time.Date(2026, time.August, 13, 14, 0, 0, 0, time.UTC))
	html := output.Body.String()
	for _, want := range []string{
		"Good afternoon, Alex",
		"No new responses assigned to you.",
		"No active cases are assigned to you.",
		"0 need attention",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("empty dashboard output missing %q", want)
		}
	}
	if strings.Contains(html, "Tasks assigned to me") || strings.Contains(html, "My tasks") {
		t.Fatal("empty dashboard still renders the removed task section")
	}
}

func TestCaseStatusLabelUsesActionableLanguage(t *testing.T) {
	for status, want := range map[string]string{
		"response_pending":  "Awaiting response",
		"decision_proposed": "Awaiting approval",
		"approved":          "Ready to issue",
	} {
		if got := caseStatusLabel(status); got != want {
			t.Fatalf("caseStatusLabel(%q)=%q want %q", status, got, want)
		}
	}
}
