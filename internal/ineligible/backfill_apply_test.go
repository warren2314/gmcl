package ineligible

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBackfillApplyPreviewReadinessIsFailClosed(t *testing.T) {
	ready := BackfillApplyPreview{RunID: 7, SignoffID: 9, AcceptedRows: 3}
	if !ready.Ready() {
		t.Fatal("signed, accepted and issue-free preview should be ready")
	}
	for name, preview := range map[string]BackfillApplyPreview{
		"no signoff":       {RunID: 7, AcceptedRows: 3},
		"no accepted rows": {RunID: 7, SignoffID: 9},
		"issue":            {RunID: 7, SignoffID: 9, AcceptedRows: 3, Issues: []string{"missing case"}},
		"already applied":  {RunID: 7, SignoffID: 9, AcceptedRows: 3, AlreadyApplied: true},
	} {
		t.Run(name, func(t *testing.T) {
			if preview.Ready() {
				t.Fatalf("preview unexpectedly ready: %+v", preview)
			}
		})
	}
}

func TestBackfillApplySourceHasNoEffectLedgerOrMessageWrites(t *testing.T) {
	source, err := os.ReadFile("backfill_apply.go")
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(string(source))
	for _, table := range []string{
		"SANCTION_DECISION_REVISIONS", "SANCTION_EFFECT_REVISIONS",
		"SANCTION_CARD_LEDGER_ENTRIES", "SANCTIONS", "SANCTION_FOLLOW_UP_TASKS",
		"SANCTION_CORRESPONDENCE_REVISIONS", "SANCTION_NOTIFICATION_OUTBOX",
		"SANCTION_RESPONSE_REQUESTS", "SANCTION_CASE_ACCESS_TOKENS",
	} {
		for _, verb := range []string{"INSERT INTO", "UPDATE", "DELETE FROM"} {
			forbidden := verb + " " + table
			if strings.Contains(upper, forbidden) {
				t.Fatalf("application service contains forbidden write: %s", forbidden)
			}
		}
	}
}

func TestValidateBackfillCaseSelectionRequiresOnePrivateIneligibleCase(t *testing.T) {
	states := map[int64]string{}
	if selected, issues := validateBackfillCaseSelection(2, 41, "closed", nil, states); selected != nil || len(issues) != 1 || !strings.Contains(issues[0], "exactly one") {
		t.Fatalf("missing selection=%+v issues=%v", selected, issues)
	}
	two := []BackfillApplyCase{{ID: 10}, {ID: 11}}
	if selected, issues := validateBackfillCaseSelection(2, 41, "closed", two, states); selected != nil || len(issues) != 1 {
		t.Fatalf("ambiguous selection=%+v issues=%v", selected, issues)
	}

	publishedAt := time.Now()
	unsafe := []BackfillApplyCase{{ID: 12, Reference: "GMCL-2026-12", SourceType: "manual", Status: "submitted", PublicStatus: "active", PublishedAt: &publishedAt}}
	selected, issues := validateBackfillCaseSelection(2, 41, "closed", unsafe, states)
	if selected == nil || len(issues) != 2 || !strings.Contains(strings.Join(issues, " "), "non-ineligible") || !strings.Contains(strings.Join(issues, " "), "public history") {
		t.Fatalf("unsafe selection=%+v issues=%v", selected, issues)
	}

	valid := []BackfillApplyCase{{ID: 13, Reference: "GMCL-2026-13", SourceType: "ineligible_player", Status: "investigating", PublicStatus: "unpublished"}}
	selected, issues = validateBackfillCaseSelection(3, 42, "open", valid, states)
	if selected == nil || len(issues) != 0 || states[13] != "open" {
		t.Fatalf("valid selection=%+v issues=%v states=%v", selected, issues, states)
	}
	_, issues = validateBackfillCaseSelection(4, 43, "closed", valid, states)
	if len(issues) != 1 || !strings.Contains(issues[0], "conflicting") {
		t.Fatalf("conflicting states were not blocked: %v", issues)
	}
}

func TestBackfillApplyOnlyRestoresAppropriateCaseStatuses(t *testing.T) {
	for _, test := range []struct {
		reviewed string
		current  string
		allowed  bool
	}{
		{"open", "submitted", true},
		{"open", "triage", true},
		{"open", "investigating", true},
		{"open", "closed", false},
		{"closed", "submitted", true},
		{"closed", "triage", true},
		{"closed", "investigating", true},
		{"closed", "closed", true},
		{"open", "response_pending", false},
		{"closed", "decision_proposed", false},
		{"closed", "approved", false},
		{"closed", "published", false},
		{"closed", "appealed", false},
		{"closed", "rejected", false},
		{"closed", "withdrawn", false},
		{"unknown", "submitted", false},
	} {
		name := test.reviewed + "_from_" + test.current
		t.Run(name, func(t *testing.T) {
			if got := backfillCurrentStatusAllowed(test.reviewed, test.current); got != test.allowed {
				t.Fatalf("allowed=%v, want %v", got, test.allowed)
			}
		})
	}
}

func TestBackfillApplyBlocksEveryProtectedCaseActivity(t *testing.T) {
	activities := []struct {
		name      string
		caseValue BackfillApplyCase
		want      string
	}{
		{"decision revisions", BackfillApplyCase{HasDecisionRevisions: true}, "decision revisions"},
		{"decision effects", BackfillApplyCase{HasEffectRevisions: true}, "decision effects"},
		{"correspondence", BackfillApplyCase{HasCorrespondence: true}, "correspondence"},
		{"outbox", BackfillApplyCase{HasOutboxMessages: true}, "outbox messages"},
		{"response request", BackfillApplyCase{HasPendingResponseRequest: true}, "pending response request"},
		{"response token", BackfillApplyCase{HasPendingResponseToken: true}, "active response token"},
	}
	for index, test := range activities {
		t.Run(test.name, func(t *testing.T) {
			linkedCase := test.caseValue
			linkedCase.ID = int64(index + 100)
			linkedCase.Reference = "GMCL-2026-ACTIVE"
			linkedCase.SourceType = "ineligible_player"
			linkedCase.Status = "investigating"
			linkedCase.PublicStatus = "unpublished"
			selected, issues := validateBackfillCaseSelection(2, 41, "open", []BackfillApplyCase{linkedCase}, map[int64]string{})
			if selected == nil || len(issues) != 1 || !strings.Contains(issues[0], test.want) {
				t.Fatalf("selected=%+v issues=%v", selected, issues)
			}
		})
	}

	approvedAt := time.Now()
	approved := BackfillApplyCase{
		ID: 999, Reference: "GMCL-2026-APPROVED", SourceType: "ineligible_player",
		Status: "closed", PublicStatus: "unpublished", ApprovedAt: &approvedAt,
	}
	selected, issues := validateBackfillCaseSelection(9, 49, "closed", []BackfillApplyCase{approved}, map[int64]string{})
	if selected == nil || len(issues) != 1 || !strings.Contains(issues[0], "approval history") {
		t.Fatalf("approved selection=%+v issues=%v", selected, issues)
	}
}

func TestBackfillApplyPreflightQueriesEveryProtectedActivity(t *testing.T) {
	source, err := os.ReadFile("backfill_apply.go")
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(string(source))
	for _, fragment := range []string{
		"FROM SANCTION_DECISION_REVISIONS",
		"FROM SANCTION_EFFECT_REVISIONS",
		"FROM SANCTION_CORRESPONDENCE_REVISIONS",
		"FROM SANCTION_NOTIFICATION_OUTBOX",
		"FROM SANCTION_RESPONSE_REQUESTS",
		"FROM SANCTION_CASE_ACCESS_TOKENS",
		"REQUEST.STATUS IN ('QUEUED','PENDING')",
		"TOKEN.PURPOSE='RESPOND'",
		"TOKEN.REVOKED_AT IS NULL",
		"TOKEN.EXPIRES_AT>NOW()",
	} {
		if !strings.Contains(upper, fragment) {
			t.Fatalf("preflight query is missing %q", fragment)
		}
	}
}

func TestBackfillSignoffReviewSnapshotIsStableJSON(t *testing.T) {
	snapshot := []BackfillReviewSnapshotEntry{{RowID: 2, ReviewID: 10}, {RowID: 3, ReviewID: 11}}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `[{"row_id":2,"review_id":10},{"row_id":3,"review_id":11}]` {
		t.Fatalf("snapshot=%s", raw)
	}
}
