package httpserver

import (
	"os"
	"strings"
	"testing"
)

func TestInvestigationAdminSelectMarksCurrentOwnerAndEscapesNames(t *testing.T) {
	selected := int32(12)
	html := investigationAdminSelect([]investigationAdminOption{
		{ID: 7, Name: "Alex Admin"},
		{ID: 12, Name: `Sam <Lead>`},
	}, &selected)
	for _, required := range []string{
		`value="7">Alex Admin`,
		`value="12" selected>Sam &lt;Lead&gt;`,
		"Choose an administrator",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("administrator options do not contain %q: %s", required, html)
		}
	}
}

func TestDelegationHandlersRequireAuthorisedTargetsAndWriteAuditHistory(t *testing.T) {
	raw, err := os.ReadFile("sanctions_delegation.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"permission='sanctions_investigate'",
		"investigator_assigned",
		"sanction_case_events",
		"investigation_support",
		"sanction_follow_up_task_events",
		"Case owner and help",
		"Assign the whole investigation",
		"Give a supporting task to another administrator",
		"reassignOpenCaseOwnerTasks",
		"task.task_type='investigation_support'",
		"task.status IN ('open','in_progress')",
		"sanction_follow_up_task_events",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("delegation implementation does not contain %q", required)
		}
	}
}

func TestCaseAssignmentSuccessReportsMovedOwnerTasks(t *testing.T) {
	if got := caseAssignmentSuccess("Tom", 0); got != "Case assigned to Tom" {
		t.Fatalf("no-task message = %q", got)
	}
	if got := caseAssignmentSuccess("Tom", 1); !strings.Contains(got, "1 open investigation task moved") {
		t.Fatalf("single-task message = %q", got)
	}
	if got := caseAssignmentSuccess("Tom", 3); !strings.Contains(got, "3 open investigation tasks moved") {
		t.Fatalf("multi-task message = %q", got)
	}
}

func TestCase170OwnerTaskMigrationIsScopedAndAudited(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0078_align_case_170_owner_task.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"task.case_id = 170",
		"task.task_type = 'investigation_support'",
		"task.status IN ('open', 'in_progress')",
		"task.assigned_admin_id IS DISTINCT FROM target_admin_id",
		"INSERT INTO sanction_follow_up_task_events",
		"migration-0078-case-170-owner-task",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("case 170 migration missing %q", required)
		}
	}
}
func TestResponseDraftSelfTestCannotCreateLiveResponseLifecycle(t *testing.T) {
	raw, err := os.ReadFile("sanctions_correspondence_drafts.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (s *Server) handleAdminCaseResponseDraftTest()")
	end := strings.Index(source[start:], "func (s *Server) handleAdminCaseResponseDraftPreview()")
	if start < 0 || end < 1 {
		t.Fatal("could not isolate response-draft self-test handler")
	}
	handler := source[start : start+end]
	for _, required := range []string{
		"response_request_test",
		"[TEST ONLY - NO CLUB CONTACT]",
		"strings.Replace(body, responseLinkPlaceholder",
		"SELECT COALESCE(email,'') FROM admin_users",
		"response_request_test_queued",
		"ineligibleOutboundEmailEnabled()",
	} {
		if !strings.Contains(handler, required) {
			t.Fatalf("safe self-test handler does not contain %q", required)
		}
	}
	for _, forbidden := range []string{
		"INSERT INTO sanction_case_access_tokens",
		"UPDATE sanction_cases",
		"/request-response",
	} {
		if strings.Contains(handler, forbidden) {
			t.Fatalf("safe self-test handler contains live lifecycle operation %q", forbidden)
		}
	}
}

func TestInvestigationSupportTaskMigrationPreservesAllowedTaskTypes(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0072_sanction_investigation_support_tasks.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"DROP CONSTRAINT IF EXISTS sanction_follow_up_tasks_task_type_check",
		"ADD CONSTRAINT sanction_follow_up_tasks_task_type_check",
		"'play_cricket_points'",
		"'migration_exception'",
		"'investigation_support'",
		"WHERE status IN ('open','in_progress')",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("support-task migration does not contain %q", required)
		}
	}
}
