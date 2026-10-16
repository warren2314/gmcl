package httpserver

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
		DecisionTotal:  1,
		CanApprove:     true,
		CanInvestigate: true,
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
		`href="/admin/cases/1176?return_to=%2Fadmin%2Fdashboard%23my-responses#club-response"`,
		`id="my-cases"`,
		`href="/admin/cases/mine/ineligible"`,
		"My ineligible cases",
		"Decisions needing my role",
		"Decision work",
		`href="/admin/cases/1177?return_to=%2Fadmin%2Fdashboard%23my-decisions"`,
		"These counts can overlap",
		"Tasks assigned to me",
		`href="/admin/cases/tasks?mine=1&amp;live=1#task-42"`,
		"Overdue",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard output missing %q", want)
		}
	}
	if strings.Contains(html, "Warren <Exec>") {
		t.Fatal("administrator name was not escaped")
	}
	if strings.Contains(html, `/admin/ineligible?scope=mine`) {
		t.Fatal("My cases still links to the report-history queue")
	}
	for _, unwanted := range []string{"2 need attention", "3 need attention"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("dashboard output unexpectedly contains %q", unwanted)
		}
	}
	caseIndex := strings.Index(html, `id="my-cases"`)
	responseIndex := strings.Index(html, `id="my-responses"`)
	decisionIndex := strings.Index(html, `id="my-decisions"`)
	taskIndex := strings.Index(html, `id="my-tasks"`)
	if caseIndex < 0 || responseIndex < 0 || decisionIndex < 0 || taskIndex < 0 || !(responseIndex < decisionIndex && decisionIndex < taskIndex && taskIndex < caseIndex) {
		t.Fatalf("next actions must precede assigned case history: cases=%d responses=%d decisions=%d tasks=%d", caseIndex, responseIndex, decisionIndex, taskIndex)
	}
	if !strings.Contains(html, `<div class="col-12" id="my-decisions">`) {
		t.Fatal("decision queue should sit below the two primary investigator panels")
	}
}

func TestWritePersonalWorkDashboardShowsCase194OnlyInTrainingSection(t *testing.T) {
	data := personalWorkDashboard{
		AdminName:      "denver",
		CanInvestigate: true,
		TestTotal:      1,
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
		`href="/admin/cases/194?return_to=%2Fadmin%2Fdashboard%23test-cases"`,
		"Training cases can send real email",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("training dashboard output missing %q", want)
		}
	}
}

func TestWritePersonalWorkDashboardHasClearEmptyStates(t *testing.T) {
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, personalWorkDashboard{AdminName: "Alex", CanInvestigate: true}, time.UTC, time.Date(2026, time.August, 13, 14, 0, 0, 0, time.UTC))
	html := output.Body.String()
	for _, want := range []string{
		"Good afternoon, Alex",
		"No new responses assigned to you.",
		"No active cases are assigned to you.",
		"No open supporting tasks assigned to you.",
		"Live work",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("empty dashboard output missing %q", want)
		}
	}
	if strings.Contains(html, "need attention</span>") {
		t.Fatal("empty dashboard sums overlapping cases into an attention total")
	}
}

func TestHomeMetricStatesDistinguishMissingDataFromZero(t *testing.T) {
	failure := errors.New("query unavailable")
	if got := homeCountLabel(7, failure); got != "Unavailable" {
		t.Fatalf("failed count = %q", got)
	}
	if got := homeCountLabel(0, nil); got != "0" {
		t.Fatalf("valid zero = %q", got)
	}
	if got := homePitchLabel(0, 0, nil); got != "No ratings" {
		t.Fatalf("empty rating sample = %q", got)
	}
	if got := homePitchLabel(4.2, 10, failure); got != "Unavailable" {
		t.Fatalf("failed rating query = %q", got)
	}
	if got := homePitchLabel(4.24, 10, nil); got != "4.2" {
		t.Fatalf("rated sample = %q", got)
	}
	if got := homeCompletionLabel(reportProgress{}); got != "No reports required" {
		t.Fatalf("no obligations = %q", got)
	}
	if got := homeCompletionLabel(reportProgress{Expected: 40, Submitted: 32, Exempt: 3, Satisfied: 35, Missing: 5}); got != "87.5% complete · 35 of 40 satisfied" {
		t.Fatalf("exempt-inclusive completion = %q", got)
	}
}

func TestHomeLeagueOverviewLabelsUnitsAndPreservesCaseDrilldowns(t *testing.T) {
	output := httptest.NewRecorder()
	writeAdminHomeLeagueOverview(output, ineligibleDashboardCounts{LiveCases: 5, AwaitingDenverSignoff: 1, DeliveryExceptions: 2, NewIntakes: 3, HiddenReports: 4}, nil, true)
	html := output.Body.String()
	for _, want := range []string{"all seasons", "My work", "3 reports awaiting triage", "4 deferred reports", "completed cases", "group=live", "group=awaiting_denver", "group=delivery_exceptions", "view=reports"} {
		if !strings.Contains(html, want) {
			t.Errorf("overview missing %q", want)
		}
	}
	output = httptest.NewRecorder()
	writeAdminHomeLeagueOverview(output, ineligibleDashboardCounts{}, errors.New("timeout"), true)
	if html := output.Body.String(); !strings.Contains(html, "League counts unavailable") || strings.Contains(html, "0 reports awaiting triage") {
		t.Fatalf("failed overview resembles valid zero: %s", html)
	}
	output = httptest.NewRecorder()
	writeAdminHomeLeagueOverview(output, ineligibleDashboardCounts{NewIntakes: 3}, nil, false)
	if output.Body.Len() != 0 {
		t.Fatal("full workspace overview shown without triage capability")
	}
}

func TestPersonalHomeExpandedWorkKeepsExistingAccessAndPopulation(t *testing.T) {
	for _, mode := range []string{"responses", "decisions"} {
		got, limit := personalHomeWorkMode(mode)
		if got != mode || limit != 300 {
			t.Fatalf("expanded mode %q=%q/%d", mode, got, limit)
		}
	}
	for _, mode := range []string{"", "cases", "all", "responses;DROP TABLE"} {
		got, limit := personalHomeWorkMode(mode)
		if got != "" || limit != 6 {
			t.Fatalf("unknown work mode expanded population: %q=%q/%d", mode, got, limit)
		}
	}
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, personalWorkDashboard{
		CanApprove: true, ExpandedWork: "responses", ResponseTotal: 2, DecisionTotal: 2,
		Responses:     []personalWorkResponse{{CaseID: 1, Reference: "CASE-1"}},
		DecisionQueue: []personalWorkQueueItem{{CaseID: 2, Reference: "CASE-2", Action: "Review decision"}},
	}, time.UTC, time.Now())
	html := output.Body.String()
	for _, want := range []string{"Expanded assigned responses", "Showing 1 of 2 assigned responses", "Showing 1 of 2 decision work items", `href="/admin/dashboard?work=responses#my-responses"`, `href="/admin/dashboard?work=decisions#my-decisions"`, `href="/admin/cases/mine/ineligible"`} {
		if !strings.Contains(html, want) {
			t.Errorf("expanded Home missing %q", want)
		}
	}
	if strings.Contains(html, "/admin/cases?work=") || strings.Contains(html, "/admin/ineligible?view=") {
		t.Fatal("personal work introduces a triage-only or unimplemented overflow destination")
	}
	output = httptest.NewRecorder()
	writePersonalResponseList(output, make([]personalWorkResponse, 300), 301, time.UTC, true)
	if html := output.Body.String(); !strings.Contains(html, "Showing 300 of 301 assigned responses") || strings.Contains(html, "Expand assigned responses") {
		t.Fatal("expanded list conceals its limit or offers a circular expansion link")
	}
}

func TestPersonalHomeCaseLinksRetainOnlySupportedReturnContext(t *testing.T) {
	for _, mode := range []string{"responses", "decisions", "", "https://external.example", "responses&scope=all"} {
		got, err := url.Parse(personalHomeCaseURL(42, "#club-response", "#my-responses", mode))
		if err != nil {
			t.Fatal(err)
		}
		if got.Path != "/admin/cases/42" || got.Fragment != "club-response" {
			t.Fatalf("case target lost: %s", got)
		}
		want := "/admin/dashboard#my-responses"
		if mode == "responses" || mode == "decisions" {
			want = "/admin/dashboard?work=" + mode + "#my-responses"
		}
		if got.Query().Get("return_to") != want {
			t.Fatalf("return=%q, want%q", got.Query().Get("return_to"), want)
		}
	}
}

func TestPersonalHomeDoesNotOfferCaseDetailsWithoutExistingReadCapability(t *testing.T) {
	data := personalWorkDashboard{
		CanTriage: true, CanPublish: true, AssignedTotal: 1, ResponseTotal: 1, DecisionTotal: 1,
		AssignedCases: []personalWorkCase{{ID: 41, Reference: "CASE-41", Status: "investigating"}},
		Responses:     []personalWorkResponse{{CaseID: 42, Reference: "CASE-42"}},
		DecisionQueue: []personalWorkQueueItem{{CaseID: 43, Reference: "CASE-43", Action: "Issue approved outcome"}},
		TestTotal:     1, TestCases: []personalWorkQueueItem{{CaseID: 44, Reference: "CASE-44"}},
	}
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, data, time.UTC, time.Now())
	html := output.Body.String()
	for _, id := range []string{"41", "42", "43", "44"} {
		if !strings.Contains(html, "CASE-"+id) {
			t.Errorf("stage context for %s missing", id)
		}
		if strings.Contains(html, `href="/admin/cases/`+id) {
			t.Errorf("case %s offers unavailable detail route", id)
		}
	}
	if !strings.Contains(html, `href="/admin/cases/mine/ineligible"`) || !strings.Contains(html, "does not grant that access") {
		t.Fatal("permitted index link or capability explanation missing")
	}
	data.CanTriage = false
	output = httptest.NewRecorder()
	writePersonalWorkDashboard(output, data, time.UTC, time.Now())
	if strings.Contains(output.Body.String(), `href="/admin/cases/mine/ineligible"`) {
		t.Fatal("index CTA shown without any existing index capability")
	}
}

func TestPersonalTasksShowFutureAvailabilityWithoutFalseOverdue(t *testing.T) {
	now := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
	pastDue, futureStart := now.Add(-time.Hour), now.AddDate(1, 0, 0)
	output := httptest.NewRecorder()
	writePersonalTaskList(output, []personalWorkTask{{ID: 2, Reference: "CASE-COMPLETED", Note: "Future points task", DueAt: &pastDue, NotBefore: &futureStart}}, 1, time.UTC, now)
	html := output.Body.String()
	if !strings.Contains(html, "Available from 15 Oct 2027") || strings.Contains(html, "Overdue") {
		t.Fatalf("future task appears overdue: %s", html)
	}
	output = httptest.NewRecorder()
	writePersonalWorkDashboard(output, personalWorkDashboard{TaskTotal: 1}, time.UTC, now)
	if html := output.Body.String(); !strings.Contains(html, "does not have investigation access") || strings.Contains(html, `id="my-tasks"`) {
		t.Fatalf("task access mismatch is hidden or links to unavailable route: %s", html)
	}
}

func TestPersonalHomeCaseAndTaskPopulationsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DB_DSN"))
	if dsn == "" {
		t.Skip("TEST_DB_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `
		CREATE TEMP TABLE clubs(id integer PRIMARY KEY,name text);
		CREATE TEMP TABLE teams(id integer PRIMARY KEY,name text);
		CREATE TEMP TABLE sanction_cases(id bigint PRIMARY KEY,reference text,source_type text DEFAULT 'ineligible_player',status text DEFAULT 'investigating',player_name text DEFAULT 'Example',club_id integer DEFAULT 1,team_id integer DEFAULT 1,assigned_admin_id integer DEFAULT 7,is_test boolean DEFAULT false,updated_at timestamptz DEFAULT '2026-10-15 12:00+00');
		CREATE TEMP TABLE sanction_case_events(id bigint PRIMARY KEY,case_id bigint,event_type text);
		CREATE TEMP TABLE sanction_follow_up_tasks(id bigint PRIMARY KEY,case_id bigint,current_note text DEFAULT '',status text DEFAULT 'open',due_at timestamptz,not_before timestamptz,assigned_admin_id integer DEFAULT 7);
		INSERT INTO clubs VALUES(1,'Example Club'); INSERT INTO teams VALUES(1,'1st XI');
		INSERT INTO sanction_cases(id,reference) SELECT n,'CASE-'||n FROM generate_series(1,7) n;
		UPDATE sanction_cases SET source_type='discipline' WHERE id=3;
		UPDATE sanction_cases SET assigned_admin_id=8 WHERE id=4;
		UPDATE sanction_cases SET is_test=true WHERE id=5;
		INSERT INTO sanction_case_events VALUES(1,6,'case_training_designated');
		UPDATE sanction_cases SET status='closed' WHERE id=7;
		INSERT INTO sanction_follow_up_tasks(id,case_id) VALUES(40,7),(41,1),(42,1),(43,1),(44,5),(45,6);
		UPDATE sanction_follow_up_tasks SET not_before='2027-04-01 00:00+00' WHERE id=41;
		UPDATE sanction_follow_up_tasks SET status='completed' WHERE id=42;
		UPDATE sanction_follow_up_tasks SET assigned_admin_id=8 WHERE id=43;
	`); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, personalAssignedIneligibleCasesQuery, int32(7), 1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var item personalWorkCase
		var total int64
		if err := rows.Scan(&item.ID, &item.Reference, &item.Status, &item.Player, &item.Club, &item.Team, &item.UpdatedAt, &total); err != nil {
			t.Fatal(err)
		}
		if total != 2 {
			t.Fatalf("assigned total=%d, want2 (excluding other source, owner, test, training, completed)", total)
		}
		ids = append(ids, item.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []int64{2}) {
		t.Fatalf("limited assigned cases=%v", ids)
	}
	rows, err = conn.Query(ctx, personalOpenTasksQuery, int32(7), 20)
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for rows.Next() {
		var item personalWorkTask
		var total int64
		if err := rows.Scan(&item.ID, &item.CaseID, &item.Reference, &item.Note, &item.Status, &item.DueAt, &item.NotBefore, &total); err != nil {
			t.Fatal(err)
		}
		if total != 2 {
			t.Fatalf("open task total=%d, want2", total)
		}
		if item.ID == 41 && item.NotBefore == nil {
			t.Fatal("future availability omitted")
		}
		ids = append(ids, item.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []int64{40, 41}) {
		t.Fatalf("tasks must retain completed-case and future work: %v", ids)
	}
}

func TestCaseStatusLabelUsesActionableLanguage(t *testing.T) {
	for status, want := range map[string]string{
		"response_pending":  "Awaiting response",
		"decision_proposed": "Decision proposed",
		"approved":          "Approved",
	} {
		if got := caseStatusLabel(status); got != want {
			t.Fatalf("caseStatusLabel(%q)=%q want %q", status, got, want)
		}
	}
}

func TestPersonalDecisionWorkSeparatesChangedSourceFromIssuePostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	if _, err := conn.Exec(ctx, `
		INSERT INTO sanction_cases(id,reference,status) VALUES(20,'STALE-20','approved');
		INSERT INTO sanction_cases(id,reference,status,source_type) VALUES(21,'MANUAL-21','approved','manual');
		INSERT INTO sanction_intakes(id,state,latest_revision) VALUES(600,'linked',1),(700,'linked',2);
		INSERT INTO sanction_intake_revisions VALUES(601,600,1),(701,700,1),(702,700,2);
		INSERT INTO sanction_intake_case_links VALUES(600,6,'primary'),(700,20,'primary'),(700,21,'primary');
		INSERT INTO sanction_case_intake_merge_resolutions VALUES(6000,6,600,'primary',601),(7000,20,700,'primary',701);
	`); err != nil {
		t.Fatal(err)
	}
	assertIDs := func(predicate string, want []int64) {
		t.Helper()
		rows, err := conn.Query(ctx, `SELECT cases.id FROM sanction_cases cases WHERE `+predicate+` ORDER BY cases.id`)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got = append(got, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("decision work cases=%v, want%v", got, want)
		}
	}
	// Only case20 needs source review. Its changed source must not also block
	// the unrelated approved case6 or the ordinary manual-source case21.
	assertIDs(personalApprovedSourceReviewSQL(), []int64{20})
	assertIDs(personalIssueReadySQL(), []int64{6, 21})
	if action := personalDecisionWorkAction("approved", true); action != "Review changed source evidence" {
		t.Fatalf("stale issue label=%q", action)
	}
	if action := personalDecisionWorkAction("approved", false); action != "Issue approved outcome" {
		t.Fatalf("current issue label=%q", action)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO sanction_case_intake_merge_resolutions VALUES(7001,20,700,'primary',702)`); err != nil {
		t.Fatal(err)
	}
	assertIDs(personalApprovedSourceReviewSQL(), nil)
	assertIDs(personalIssueReadySQL(), []int64{6, 20, 21})
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, personalWorkDashboard{CanInvestigate: true, DecisionTotal: 1, DecisionQueue: []personalWorkQueueItem{{CaseID: 20, Reference: "STALE-20", Action: "Review changed source evidence"}}}, time.UTC, time.Now())
	html := output.Body.String()
	if !strings.Contains(html, "Decision work") || !strings.Contains(html, "#source-change-review") || strings.Contains(html, "Issue approved outcome") {
		t.Fatalf("investigator review CTA is missing or misleading: %s", html)
	}
}
