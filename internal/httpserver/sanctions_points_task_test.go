package httpserver

import (
	"os"
	"strings"
	"testing"
)

func TestPlayCricketPointsTaskRequiresAssignedFinalSignOffAdmin(t *testing.T) {
	source, err := os.ReadFile("sanctions_operations.go")
	if err != nil {
		t.Fatal(err)
	}
	operationsSource := string(source)
	if !strings.Contains(operationsSource, `taskType == "play_cricket_points" && (assignedAdminID == nil || *assignedAdminID != *actor.ID)`) {
		t.Fatal("Play-Cricket points tasks must require the assigned final sign-off administrator")
	}
}

func TestIneligibleDashboardCountsAllOpenPlayCricketPointsTasks(t *testing.T) {
	dashboardSource := ineligibleDashboardSource(t)
	if !strings.Contains(dashboardSource, "League points awaiting Denver") {
		t.Fatal("dashboard does not explain that league-points work awaits Denver")
	}
	want := "JOIN live_cases c ON c.id=t.case_id WHERE t.task_type='play_cricket_points' AND t.status IN ('open','in_progress')"
	if !strings.Contains(dashboardSource, want) {
		t.Fatal("dashboard must count open Play-Cricket points tasks from every live sanctions source")
	}
	if strings.Contains(dashboardSource, "c.source_type='ineligible_player' AND t.task_type='play_cricket_points'") {
		t.Fatal("dashboard still excludes Denver's points tasks from other sanctions sources")
	}
}
