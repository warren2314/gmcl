package httpserver

import (
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

	var output strings.Builder
	writePersonalWorkDashboard(&output, data, loc, now)
	html := output.String()
	for _, want := range []string{
		"Good morning, Warren &lt;Exec&gt;",
		"Responses awaiting review",
		`href="/admin/cases/1176"`,
		`href="/admin/cases/tasks#task-42"`,
		"Overdue",
		"Decisions needing my role",
		`href="/admin/cases/1177"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard output missing %q", want)
		}
	}
	if strings.Contains(html, "Warren <Exec>") {
		t.Fatal("administrator name was not escaped")
	}
}

func TestWritePersonalWorkDashboardHasClearEmptyStates(t *testing.T) {
	var output strings.Builder
	writePersonalWorkDashboard(&output, personalWorkDashboard{AdminName: "Alex"}, time.UTC, time.Date(2026, time.August, 13, 14, 0, 0, 0, time.UTC))
	html := output.String()
	for _, want := range []string{
		"Good afternoon, Alex",
		"No new responses assigned to you.",
		"No open supporting tasks assigned to you.",
		"No active cases are assigned to you.",
		"0 need attention",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("empty dashboard output missing %q", want)
		}
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
