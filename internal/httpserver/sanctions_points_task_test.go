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
	source, err := os.ReadFile("admin_ineligible.go")
	if err != nil {
		t.Fatal(err)
	}
	dashboardSource := string(source)
	if !strings.Contains(dashboardSource, "League points awaiting Denver") {
		t.Fatal("dashboard does not explain that league-points work awaits Denver")
	}
}
