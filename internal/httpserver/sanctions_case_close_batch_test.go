package httpserver

import (
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestCloseBatchUsesTheSameStatusesAsSingleCaseClose(t *testing.T) {
	raw, err := os.ReadFile("sanctions_case_close_no_action.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, status := range closeBatchClosableStatuses {
		if !strings.Contains(source, `"`+status+`": true`) {
			t.Fatalf("bulk close allows %q but the single-case control does not", status)
		}
		if !closeBatchStatusClosable(status) {
			t.Fatalf("closeBatchStatusClosable rejects its own status %q", status)
		}
	}
	for _, status := range []string{"approved", "published", "closed", "rejected", "withdrawn", "appealed"} {
		if closeBatchStatusClosable(status) {
			t.Fatalf("bulk close must not touch %q cases", status)
		}
	}
}

func TestParseCloseBatchCaseIDsIsSortedAndDeduplicated(t *testing.T) {
	got := parseCloseBatchCaseIDs([]string{"12", "3", "12", "", "abc", "-4", "0", " 7 "})
	want := []int64{3, 7, 12}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsed %v, want %v", got, want)
		}
	}
}

func TestParseCloseBatchFiltersRejectsUnknownValues(t *testing.T) {
	filters := parseCloseBatchFilters(url.Values{"opened_before": {"not-a-date"}, "source": {"'; DROP TABLE"}})
	if filters.OpenedBefore != "" || filters.Source != "" {
		t.Fatalf("invalid filters survived parsing: %+v", filters)
	}
	filters = parseCloseBatchFilters(url.Values{"opened_before": {"2025-01-31"}, "source": {"ineligible_player"}})
	if filters.OpenedBefore != "2025-01-31" || filters.Source != "ineligible_player" {
		t.Fatalf("valid filters were dropped: %+v", filters)
	}
}

func TestCloseBatchWhereSQLBindsDatesAndExcludesTestCases(t *testing.T) {
	where, args := closeBatchWhereSQL(closeBatchFilters{OpenedBefore: "2025-01-31", Source: "ineligible_player"}, 4)
	if !strings.Contains(where, "NOT cases.is_test") || !strings.Contains(where, "case_training_designated") {
		t.Fatalf("bulk close would list test or training cases: %s", where)
	}
	if !strings.Contains(where, "cases.created_at < $2::text::date") {
		t.Fatalf("opened-before filter is not a bound parameter: %s", where)
	}
	if len(args) != 2 || args[0] != int32(4) || args[1] != "2025-01-31" {
		t.Fatalf("unexpected arguments %v", args)
	}
	if !strings.Contains(where, "cases.source_type='ineligible_player'") {
		t.Fatalf("source filter missing: %s", where)
	}
	where, args = closeBatchWhereSQL(closeBatchFilters{}, 4)
	if strings.Contains(where, "created_at") || len(args) != 1 {
		t.Fatalf("empty filters still constrain the query: %s / %v", where, args)
	}
}

func TestCloseBatchSkippedSummaryStaysReadable(t *testing.T) {
	if got := closeBatchSkippedSummary(nil); got != "Nothing was skipped." {
		t.Fatalf("empty summary = %q", got)
	}
	summary := closeBatchSkippedSummary([]string{"A", "B", "C", "D", "E", "F", "G"})
	if !strings.Contains(summary, "7 case(s) were skipped") || !strings.Contains(summary, "and 2 more") {
		t.Fatalf("long summary = %q", summary)
	}
}

func TestCloseBatchPageGuardsTheSubmission(t *testing.T) {
	output := httptest.NewRecorder()
	writeCloseBatchScript(output)
	script := output.Body.String()
	for _, want := range []string{"Tick at least one case to close.", "Close at most ", "window.confirm(", "200"} {
		if !strings.Contains(script, want) {
			t.Fatalf("bulk close script missing %q", want)
		}
	}

	output = httptest.NewRecorder()
	writeCloseBatchFilters(output, closeBatchFilters{OpenedBefore: "2025-01-31", Source: "other"})
	filters := output.Body.String()
	if !strings.Contains(filters, `value="2025-01-31"`) || !strings.Contains(filters, `<option value="other" selected>`) {
		t.Fatalf("filter form does not keep the current choices: %s", filters)
	}
}

func TestCloseBatchRoutesRequireInvestigatePermission(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		`r.With(s.requireAdminPermission("sanctions_investigate")).Get("/cases/close-batch", s.handleAdminCaseCloseBatch())`,
		`r.With(s.requireAdminPermission("sanctions_investigate")).Post("/cases/close-batch", s.handleAdminCaseCloseBatchApply())`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("bulk close route missing or differently protected: %q", want)
		}
	}
}

func TestCloseBatchRecordsHistoryAndCannotTakeOthersCases(t *testing.T) {
	raw, err := os.ReadFile("sanctions_case_close_batch.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"assignedAdminID != nil && !sameAdminAssignment(assignedAdminID, actor.ID)",
		"'investigator_assigned'",
		"closeSanctionCaseNoActionSteps(ctx, tx, caseID, status, *actor.ID, actor.Label, actor.RequestID, reason, len(ids))",
		"pg_try_advisory_xact_lock",
		"FOR UPDATE",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("bulk close is missing the safeguard %q", want)
		}
	}
}
