package httpserver

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIneligibleWorkspaceCSVCellsRemainText(t *testing.T) {
	for _, value := range []string{"=1+1", "+SUM(1,2)", "-2", "@name", "\t=1", "\r=1", "\n=1", "   =1", "＝1+1", "＋1", "－2", "＠name"} {
		if got := ineligibleCSVCell(value); got != "'"+value {
			t.Errorf("formula-like CSV cell %q became %q, want text prefix", value, got)
		}
	}
	for _, value := range []string{"", "Example Club", "O'Brien", `"quoted",name`, "Line one\nLine two"} {
		if got := ineligibleCSVCell(value); got != value {
			t.Errorf("ordinary CSV cell %q unexpectedly changed to %q", value, got)
		}
		var output strings.Builder
		writer := csv.NewWriter(&output)
		if err := writer.Write([]string{ineligibleCSVCell(value), "next cell"}); err != nil {
			t.Fatal(err)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			t.Fatal(err)
		}
		cells, err := csv.NewReader(strings.NewReader(output.String())).Read()
		if err != nil || !reflect.DeepEqual(cells, []string{value, "next cell"}) {
			t.Fatalf("CSV quoting changed cell boundaries: cells=%v error=%v", cells, err)
		}
	}
}

// These tests run only against an explicitly supplied test database and create
// session-local tables. They need neither migrated application tables nor seed
// data, and leave no persistent cases, messages or workflow changes behind.
func ineligibleWorkspaceTestDB(t *testing.T) (context.Context, *pgx.Conn) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DB_DSN"))
	if dsn == "" {
		t.Skip("TEST_DB_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	_, err = conn.Exec(ctx, `
		CREATE TEMP TABLE clubs(id integer PRIMARY KEY,name text);
		CREATE TEMP TABLE teams(id integer PRIMARY KEY,name text);
		CREATE TEMP TABLE seasons(id integer PRIMARY KEY,name text);
		CREATE TEMP TABLE admin_users(id integer PRIMARY KEY,username text);
		CREATE TEMP TABLE sanction_cases(
			id bigint PRIMARY KEY,reference text,source_type text DEFAULT 'ineligible_player',
			status text,player_name text DEFAULT 'Example Player',club_id integer DEFAULT 1,
			team_id integer DEFAULT 1,season_id integer DEFAULT 1,match_date date,
			assigned_admin_id integer DEFAULT 7,updated_at timestamptz DEFAULT '2026-10-01 12:00:00+00',
			is_test boolean DEFAULT false,public_status text DEFAULT 'unpublished',published_at timestamptz
		);
		CREATE TEMP TABLE sanction_case_events(
			id bigint PRIMARY KEY,case_id bigint,event_type text,metadata jsonb DEFAULT '{}',
			created_at timestamptz DEFAULT '2026-10-01 12:00:00+00'
		);
		CREATE TEMP TABLE sanction_decision_revisions(
			id bigint PRIMARY KEY,case_id bigint,status text,revision integer,supersedes_id bigint
		);
		CREATE TEMP TABLE sanction_response_requests(
			id bigint PRIMARY KEY,case_id bigint,status text,delivered_at timestamptz,
			reminder_due_at timestamptz,due_at timestamptz
		);
		CREATE TEMP TABLE sanction_follow_up_tasks(id bigint PRIMARY KEY,case_id bigint,status text);
		CREATE TEMP TABLE sanction_notification_outbox(id bigint PRIMARY KEY,case_id bigint,revoked_at timestamptz);
		CREATE TEMP TABLE sanction_notification_attempts(
			id bigint PRIMARY KEY,outbox_id bigint,attempt_number integer,status text
		);
		CREATE TEMP TABLE sanction_intake_case_links(intake_id bigint,case_id bigint,relationship text);
		CREATE TEMP TABLE sanction_intakes(id bigint PRIMARY KEY,state text DEFAULT 'new',latest_revision integer DEFAULT 1);
		CREATE TEMP TABLE sanction_intake_revisions(id bigint,intake_id bigint,revision integer);
		CREATE TEMP TABLE sanction_case_intake_merge_resolutions(id bigint,case_id bigint,intake_id bigint,relationship text,revision_id bigint);
		INSERT INTO clubs VALUES(1,'Example Club'),(2,'Manual Club');
		INSERT INTO teams VALUES(1,'First XI'),(2,'Independent Team');
		INSERT INTO seasons VALUES(1,'2026');
		INSERT INTO admin_users VALUES(7,'Case Owner'),(8,'Other Owner');
		INSERT INTO sanction_cases(id,reference,status,match_date) VALUES
			(1,'CASE-1','investigating','2026-07-01'),
			(2,'CASE-2','response_pending','2026-07-02'),
			(3,'CASE-3','response_pending','2026-07-03'),
			(4,'CASE-4','decision_proposed','2026-07-04'),
			(5,'CASE-5','decision_proposed','2026-07-05'),
			(6,'CASE-6','approved','2026-07-06'),
			(7,'CASE-7','published','2026-07-07'),
			(8,'CASE-8','closed','2026-07-08'),
			(9,'CASE-9','rejected','2026-07-09'),
			(10,'CASE-10','withdrawn','2026-07-10'),
			(11,'CASE-11','investigating','2026-07-11'),
			(12,'CASE-12','investigating','2026-07-12'),
			(13,'CASE-13','investigating','2026-07-13'),
			(14,'MANUAL-14','investigating','2026-07-14'),
			(15,'CASE-15','investigating','2026-07-15'),
			(16,'CASE-16','response_pending','2026-07-16'),
			(17,'CASE-17','investigating','2026-07-17'),
			(18,'CASE-18','response_pending','2026-07-18'),
			(19,'CASE-19','investigating','2026-07-19');
		UPDATE sanction_cases SET assigned_admin_id=8 WHERE id IN (5,6,19);
		UPDATE sanction_cases SET is_test=true WHERE id=11;
		UPDATE sanction_cases SET source_type='captain_report' WHERE id=13;
		UPDATE sanction_cases SET assigned_admin_id=NULL,club_id=2,team_id=2 WHERE id=14;
		INSERT INTO sanction_case_events(id,case_id,event_type) VALUES
			(101,1,'party_response'),(107,7,'external_response_recorded'),
			(108,8,'external_response_recorded'),(112,12,'case_training_designated'),
			(115,15,'party_response'),(118,18,'external_response_recorded'),
			(119,19,'party_response');
		INSERT INTO sanction_case_events(id,case_id,event_type,metadata) VALUES
			(215,15,'response_reviewed','{"response_event_id":"115"}'),
			(219,19,'response_reviewed','{"response_event_id":"119"}'),
			(304,4,'decision_owner_review_required','{"decision_revision_id":"41"}'),
			(305,5,'decision_owner_review_required','{"decision_revision_id":"51"}'),
			(405,5,'decision_sent_for_approval','{"decision_revision_id":"51"}');
		INSERT INTO sanction_decision_revisions VALUES
			(41,4,'proposed',1,NULL),(51,5,'proposed',1,NULL),(61,6,'approved',1,NULL);
		INSERT INTO sanction_response_requests VALUES
			(2,2,'pending',now()-interval '1 day',now()+interval '1 day',now()+interval '2 days'),
			(3,3,'pending',now()-interval '4 days',now()-interval '2 days',now()-interval '1 day'),
			(16,16,'queued',NULL,NULL,NULL),
			(17,17,'expired',now()-interval '4 days',now()-interval '2 days',now()-interval '1 day'),
			(18,18,'responded',now()-interval '1 day',now()+interval '1 day',now()+interval '2 days');
		INSERT INTO sanction_follow_up_tasks VALUES
			(1,1,'open'),(2,1,'in_progress'),(3,1,'complete'),(4,1,'cancelled');
		INSERT INTO sanction_notification_outbox VALUES(2,2,NULL),(7,7,NULL),(15,15,now());
		INSERT INTO sanction_notification_attempts VALUES
			(2,2,1,'failed'),(7,7,1,'bounced'),(15,15,1,'failed');
		-- Two reports concern one case, and one report concerns two split cases.
		-- Case 14 deliberately has no intake link at all.
		INSERT INTO sanction_intake_case_links VALUES
			(1001,1,'primary'),(1002,1,'supporting'),(1001,2,'split');
	`)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, conn
}

func assertIneligibleWorkspaceCases(t *testing.T, ctx context.Context, conn *pgx.Conn, filter ineligibleCaseFilter, access ineligibleCaseAccess, wantIDs []int64, wantTotal int64) []ineligibleCaseWorkRow {
	t.Helper()
	query, args := buildIneligibleCaseQuery(filter, access, true)
	var total int64
	if err := conn.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != wantTotal {
		t.Fatalf("filter %+v total=%d, want %d", filter, total, wantTotal)
	}
	query, args = buildIneligibleCaseQuery(filter, access, false)
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotIDs []int64
	var items []ineligibleCaseWorkRow
	for rows.Next() {
		var row ineligibleCaseWorkRow
		if err := rows.Scan(&row.ID, &row.Reference, &row.Status, &row.Player, &row.Club, &row.Team, &row.Season, &row.Owner,
			&row.UpdatedAt, &row.MatchDate, &row.IsOwner, &row.HasProposed, &row.OwnerReviewRequired, &row.SentForApproval,
			&row.ReplyUnreviewed, &row.ReplyAt, &row.RequestStatus, &row.DeliveredAt, &row.ReminderDueAt, &row.DueAt,
			&row.DeliveryException, &row.OpenTasks, &row.SourceReviewRequired); err != nil {
			t.Fatal(err)
		}
		gotIDs = append(gotIDs, row.ID)
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("filter %+v case IDs=%v, want %v", filter, gotIDs, wantIDs)
	}
	return items
}

func assertIneligibleGlobalGroup(t *testing.T, ctx context.Context, conn *pgx.Conn, group string, want []int64) {
	t.Helper()
	// Both aliases are actual callers: the legacy all-cases route uses c,
	// while league summary queries use cases. Nested source checks must remain
	// correlated to the particular outer case under either alias.
	for _, alias := range []string{"cases", "c"} {
		assertIneligibleCaseGroupAlias(t, ctx, conn, group, alias, want)
	}
}

func assertIneligibleCaseGroupAlias(t *testing.T, ctx context.Context, conn *pgx.Conn, group, alias string, want []int64) {
	t.Helper()
	query := `SELECT ` + alias + `.id FROM sanction_cases ` + alias + ` WHERE NOT ` + alias + `.is_test
		AND NOT EXISTS(SELECT 1 FROM sanction_case_events event WHERE event.case_id=` + alias + `.id AND event.event_type='case_training_designated')
		AND (` + ineligibleCaseGroupPredicate(group, alias) + `) ORDER BY ` + alias + `.id DESC`
	rows, err := conn.Query(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("global %s (alias %s) case IDs=%v, want workspace population %v", group, alias, got, want)
	}
}

func TestIneligibleWorkspaceCountsReconcileWithCasesPostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	access := ineligibleCaseAccess{AdminID: 7, CanInvestigate: true, CanPropose: true, CanApprove: true, CanIssue: true}
	tests := []struct {
		name   string
		filter ineligibleCaseFilter
		ids    []int64
	}{
		{"live cases include manual and split cases once", ineligibleCaseFilter{}, []int64{19, 18, 17, 16, 15, 14, 6, 5, 4, 3, 2, 1}},
		{"owned live cases", ineligibleCaseFilter{Scope: "mine"}, []int64{18, 17, 16, 15, 4, 3, 2, 1}},
		{"history retains all finished statuses", ineligibleCaseFilter{View: "history"}, []int64{10, 9, 8, 7}},
		{"new replies exclude closed and reviewed", ineligibleCaseFilter{Group: "new_replies"}, []int64{18, 1}},
		{"owner review", ineligibleCaseFilter{Group: "owner_review"}, []int64{4}},
		{"peer approval", ineligibleCaseFilter{Group: "awaiting_decision"}, []int64{5}},
		{"final issue", ineligibleCaseFilter{Group: "awaiting_denver"}, []int64{6}},
		{"delivered future response", ineligibleCaseFilter{Group: "responses_due"}, []int64{2}},
		{"pending and expired overdue responses", ineligibleCaseFilter{Group: "responses_overdue"}, []int64{17, 3}},
		{"delivery failures include finished case", ineligibleCaseFilter{Group: "delivery_exceptions"}, []int64{7, 2}},
		{"training is a separate explicit population", ineligibleCaseFilter{Training: true}, []int64{12, 11}},
		{"search finds a case without an intake", ineligibleCaseFilter{Query: "MANUAL-14"}, []int64{14}},
		{"search follows club relationship", ineligibleCaseFilter{Query: "Manual Club"}, []int64{14}},
		{"inclusive fixture range", ineligibleCaseFilter{FixtureFrom: "2026-07-02", FixtureTo: "2026-07-04"}, []int64{4, 3, 2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertIneligibleWorkspaceCases(t, ctx, conn, tc.filter, access, tc.ids, int64(len(tc.ids)))
			if tc.filter.Group != "" {
				assertIneligibleGlobalGroup(t, ctx, conn, tc.filter.Group, tc.ids)
			}
		})
	}
	items := assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Query: "CASE-1", FixtureTo: "2026-07-01"}, access, []int64{1}, 1)
	if items[0].OpenTasks != 2 || items[0].Club != "Example Club" || items[0].Owner != "Case Owner" || items[0].Season != "2026" {
		t.Fatalf("case projection lost linked context or open-task count: %+v", items[0])
	}
}

func TestIneligibleWorkspaceActionsRespectCapabilitiesPostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	filter := ineligibleCaseFilter{Scope: "action"}
	for _, tc := range []struct {
		name   string
		access ineligibleCaseAccess
		ids    []int64
	}{
		{"no workflow capability", ineligibleCaseAccess{AdminID: 7}, nil},
		{"owner investigation", ineligibleCaseAccess{AdminID: 7, CanInvestigate: true}, []int64{18, 17, 15, 3, 2, 1}},
		{"owner proposal review without investigation rights", ineligibleCaseAccess{AdminID: 7, CanPropose: true}, []int64{15, 4}},
		{"peer approver", ineligibleCaseAccess{AdminID: 7, CanApprove: true}, []int64{5}},
		{"final issuer", ineligibleCaseAccess{AdminID: 7, CanIssue: true}, []int64{6}},
		{"all capabilities", ineligibleCaseAccess{AdminID: 7, CanInvestigate: true, CanPropose: true, CanApprove: true, CanIssue: true}, []int64{18, 17, 15, 6, 5, 4, 3, 2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertIneligibleWorkspaceCases(t, ctx, conn, filter, tc.access, tc.ids, int64(len(tc.ids)))
		})
	}
	if _, err := conn.Exec(ctx, `INSERT INTO sanction_cases(id,reference,status,assigned_admin_id) VALUES
		(20,'APPEAL-20','appealed',8),(21,'DELIVERY-21','investigating',7);
		INSERT INTO sanction_notification_outbox VALUES(21,21,NULL);
		INSERT INTO sanction_notification_attempts VALUES(21,21,1,'failed')`); err != nil {
		t.Fatal(err)
	}
	assertIneligibleWorkspaceCases(t, ctx, conn, filter, ineligibleCaseAccess{AdminID: 7, CanPropose: true}, []int64{15, 4}, 2)
	assertIneligibleWorkspaceCases(t, ctx, conn, filter, ineligibleCaseAccess{AdminID: 7, CanInvestigate: true}, []int64{21, 18, 17, 15, 3, 2, 1}, 7)
	assertIneligibleWorkspaceCases(t, ctx, conn, filter, ineligibleCaseAccess{AdminID: 7, CanAppeal: true}, []int64{20}, 1)
	assertIneligibleWorkspaceCases(t, ctx, conn, filter, ineligibleCaseAccess{AdminID: 7, CanApprove: true, CanIssue: true}, []int64{6, 5}, 2)
}

func TestIneligibleWorkspaceLifecycleAndDeliveryPostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	access := ineligibleCaseAccess{AdminID: 7, CanInvestigate: true}
	replies := ineligibleCaseFilter{Group: "new_replies"}
	assertIneligibleWorkspaceCases(t, ctx, conn, replies, access, []int64{18, 1}, 2)
	exec := func(query string) {
		t.Helper()
		if _, err := conn.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE sanction_cases SET status='closed' WHERE id=1;
		INSERT INTO sanction_case_events(id,case_id,event_type) VALUES(501,1,'external_response_recorded')`)
	assertIneligibleWorkspaceCases(t, ctx, conn, replies, access, []int64{18}, 1)
	exec(`INSERT INTO sanction_case_events(id,case_id,event_type,metadata)
		VALUES(518,18,'response_reviewed','{"response_event_id":"118"}')`)
	assertIneligibleWorkspaceCases(t, ctx, conn, replies, access, nil, 0)
	exec(`UPDATE sanction_cases SET status='investigating' WHERE id=1`)
	assertIneligibleWorkspaceCases(t, ctx, conn, replies, access, []int64{1}, 1)
	var replyCount int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_case_events WHERE case_id=1 AND event_type IN ('party_response','external_response_recorded')`).Scan(&replyCount); err != nil || replyCount != 2 {
		t.Fatalf("reply history count=%d, error=%v; want two retained replies", replyCount, err)
	}
	// A successful later attempt clears the exception; revoked mail does not
	// continue creating work even if its last recorded attempt failed.
	exec(`INSERT INTO sanction_notification_attempts VALUES(502,2,2,'sent');
		UPDATE sanction_notification_outbox SET revoked_at=now() WHERE id=7`)
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Group: "delivery_exceptions"}, access, nil, 0)
	// Replacing an expired delivery window with a newly queued notice does not
	// make that undelivered notice overdue.
	exec(`INSERT INTO sanction_response_requests VALUES(503,3,'queued',NULL,NULL,NULL)`)
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Group: "responses_overdue"}, access, []int64{17}, 1)
	// Approval markers belong to a particular proposal revision. Sending an
	// older proposal cannot skip the owner's review of its replacement.
	exec(`INSERT INTO sanction_decision_revisions VALUES(42,4,'proposed',2,41);
		INSERT INTO sanction_case_events(id,case_id,event_type,metadata) VALUES
			(604,4,'decision_owner_review_required','{"decision_revision_id":"42"}'),
			(605,4,'decision_sent_for_approval','{"decision_revision_id":"41"}')`)
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Group: "owner_review"}, access, []int64{4}, 1)
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Group: "awaiting_decision"}, access, []int64{5}, 1)
	assertIneligibleGlobalGroup(t, ctx, conn, "owner_review", []int64{4})
	assertIneligibleGlobalGroup(t, ctx, conn, "awaiting_decision", []int64{5})
	exec(`INSERT INTO sanction_case_events(id,case_id,event_type,metadata)
		VALUES(606,4,'decision_sent_for_approval','{"decision_revision_id":"42"}')`)
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Group: "owner_review"}, access, nil, 0)
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Group: "awaiting_decision"}, access, []int64{5, 4}, 2)
	assertIneligibleGlobalGroup(t, ctx, conn, "owner_review", nil)
	assertIneligibleGlobalGroup(t, ctx, conn, "awaiting_decision", []int64{5, 4})
}

func TestIneligibleWorkspaceChangedSourcePrecedesFinalIssuePostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	if _, err := conn.Exec(ctx, `
		INSERT INTO sanction_cases(id,reference,status) VALUES(30,'UNRELATED-FRESH-30','approved');
		INSERT INTO sanction_intakes(id,state,latest_revision) VALUES(901,'linked',1);
		INSERT INTO sanction_intake_revisions VALUES(9001,901,1),(9002,901,2);
		INSERT INTO sanction_intake_case_links VALUES(901,6,'primary');
		INSERT INTO sanction_case_intake_merge_resolutions VALUES(9001,6,901,'primary',9001);
	`); err != nil {
		t.Fatal(err)
	}
	filter := ineligibleCaseFilter{Scope: "action", CaseStatus: "approved"}
	issueGroup := ineligibleCaseFilter{Group: "awaiting_denver"}
	issuer := ineligibleCaseAccess{AdminID: 7, CanIssue: true}
	items := assertIneligibleWorkspaceCases(t, ctx, conn, filter, issuer, []int64{30, 6}, 2)
	assertIneligibleWorkspaceCases(t, ctx, conn, issueGroup, issuer, []int64{30, 6}, 2)
	assertIneligibleGlobalGroup(t, ctx, conn, "awaiting_denver", []int64{30, 6})
	if items[0].SourceReviewRequired {
		t.Fatal("a source revision already reconciled to approval requires review")
	}
	if _, err := conn.Exec(ctx, `UPDATE sanction_intakes SET latest_revision=2,state='reviewing' WHERE id=901`); err != nil {
		t.Fatal(err)
	}
	assertIneligibleWorkspaceCases(t, ctx, conn, filter, issuer, []int64{30}, 1)
	assertIneligibleWorkspaceCases(t, ctx, conn, issueGroup, issuer, []int64{30}, 1)
	assertIneligibleGlobalGroup(t, ctx, conn, "awaiting_denver", []int64{30})
	reviewer := ineligibleCaseAccess{AdminID: 7, CanReviewSource: true}
	items = assertIneligibleWorkspaceCases(t, ctx, conn, filter, reviewer, []int64{6}, 1)
	if !items[0].SourceReviewRequired {
		t.Fatal("the newly changed source revision is not visible in the case projection")
	}
	action := selectAdminCaseNextAction(items[0].actionContext(reviewer), time.Now())
	if action.Title != "Review changed source evidence" || action.Href != "#source-change-review" {
		t.Fatalf("changed-source case still suggests final issue: %+v", action)
	}
	if action := selectAdminCaseNextAction(items[0].actionContext(issuer), time.Now()); action.Title != "Awaiting changed-source review" {
		t.Fatalf("issue-only access bypasses the source review stage: %+v", action)
	}
	if _, err := conn.Exec(ctx, `UPDATE sanction_intakes SET state='linked' WHERE id=901;
		INSERT INTO sanction_case_intake_merge_resolutions VALUES(9002,6,901,'primary',9002)`); err != nil {
		t.Fatal(err)
	}
	assertIneligibleWorkspaceCases(t, ctx, conn, issueGroup, issuer, []int64{30, 6}, 2)
	assertIneligibleGlobalGroup(t, ctx, conn, "awaiting_denver", []int64{30, 6})
	assertIneligibleWorkspaceCases(t, ctx, conn, filter, issuer, []int64{30, 6}, 2)
}

func TestIneligibleWorkspacePaginationUsesUniqueCaseTotalsPostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	if _, err := conn.Exec(ctx, `INSERT INTO sanction_cases(id,reference,status)
		SELECT id,'PAGE-'||id,'investigating' FROM generate_series(100,159) id`); err != nil {
		t.Fatal(err)
	}
	access := ineligibleCaseAccess{AdminID: 7}
	for page := 1; page <= 3; page++ {
		var ids []int64
		for id := 159 - (page-1)*ineligibleCasePageSize; id >= 100 && len(ids) < ineligibleCasePageSize; id-- {
			ids = append(ids, int64(id))
		}
		t.Run(fmt.Sprintf("page %d", page), func(t *testing.T) {
			assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Query: "PAGE-", Page: page}, access, ids, 60)
		})
	}
	// Export must remove pagination only, retaining the population and order.
	assertExport := func(filter ineligibleCaseFilter, want []int64) {
		t.Helper()
		query, args := buildIneligibleCaseExportQuery(filter, access)
		rows, err := conn.Query(ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				t.Fatal(err)
			}
			id, ok := values[0].(int64)
			if !ok {
				t.Fatalf("export case ID has unexpected type %T", values[0])
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("export filter %+v case IDs=%v, want %v", filter, ids, want)
		}
	}
	var all []int64
	for id := int64(159); id >= 100; id-- {
		all = append(all, id)
	}
	assertExport(ineligibleCaseFilter{Query: "PAGE-", Page: 2}, all)
	if _, err := conn.Exec(ctx, `UPDATE sanction_cases SET assigned_admin_id=8 WHERE id=159`); err != nil {
		t.Fatal(err)
	}
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{Query: "PAGE-", Scope: "mine"}, access, all[1:51], 59)
	assertExport(ineligibleCaseFilter{Query: "PAGE-", Scope: "mine", Page: 3}, all[1:])
	assertExport(ineligibleCaseFilter{Query: "PAGE-", View: "history"}, nil)
	assertExport(ineligibleCaseFilter{Query: "PAGE-", Training: true}, nil)
}

func TestIneligibleWorkspaceReportPaginationAndCountPostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	if _, err := conn.Exec(ctx, `
		ALTER TABLE sanction_intakes
			ADD COLUMN origin text DEFAULT 'google_form',ADD COLUMN external_key text,
			ADD COLUMN is_training boolean DEFAULT false,
			ADD COLUMN reporting_club_text text DEFAULT 'Example Club',ADD COLUMN offending_club_text text DEFAULT 'Other Club',
			ADD COLUMN team_text text DEFAULT 'First XI',ADD COLUMN player_text text DEFAULT 'Report Population',
			ADD COLUMN fixture_date date DEFAULT '2026-07-01',ADD COLUMN external_created_at timestamptz,
			ADD COLUMN created_at timestamptz DEFAULT '2026-10-01 12:00:00+00';
		CREATE TEMP TABLE sanction_intake_effective_case_links(id bigint,intake_id bigint,case_id bigint,relationship text);
		CREATE TEMP TABLE sanction_intake_worklist_current(intake_id bigint,visibility text,batch_id bigint);
		CREATE TEMP TABLE sanction_intake_attachments(intake_id bigint);
		INSERT INTO sanction_intakes(id,external_key) SELECT id,'REPORT-'||id FROM generate_series(100,2164) id;
		UPDATE sanction_intakes SET is_training=true WHERE id BETWEEN 100 AND 104;
		INSERT INTO sanction_intake_worklist_current SELECT id,'deferred',1 FROM generate_series(105,109) id;
		UPDATE sanction_intakes SET state='linked' WHERE id BETWEEN 110 AND 115;
		INSERT INTO sanction_intake_effective_case_links SELECT id,id,8,'primary' FROM generate_series(110,114) id;
		INSERT INTO sanction_intake_effective_case_links VALUES(115,115,10,'primary');
		INSERT INTO sanction_intake_case_links VALUES(115,10,'primary');
		UPDATE sanction_intakes SET state='ignored' WHERE id=116;
		UPDATE sanction_intakes SET player_text='Unique Needle' WHERE id=2160;
	`); err != nil {
		t.Fatal(err)
	}
	assertReports := func(filter ineligibleQueueFilters, page int, wantIDs []int64, wantTotal int64) {
		t.Helper()
		query, args := buildIneligibleReportQuery(filter, nil, page, true)
		var total int64
		if err := conn.QueryRow(ctx, query, args...).Scan(&total); err != nil {
			t.Fatal(err)
		}
		if total != wantTotal {
			t.Fatalf("report filter %+v total=%d, want %d", filter, total, wantTotal)
		}
		query, args = buildIneligibleReportQuery(filter, nil, page, false)
		rows, err := conn.Query(ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var row ineligibleQueueRow
			if err := rows.Scan(&row.ID, &row.Origin, &row.ExternalKey, &row.State, &row.IsTraining, &row.ReportingClubText,
				&row.OffendingClubText, &row.TeamText, &row.PlayerText, &row.FixtureDate, &row.ExternalCreatedAt,
				&row.CaseID, &row.CaseReference, &row.CaseStatus, &row.Assignee, &row.WorklistVisibility, &row.WorklistBatchID,
				&row.AttachmentCount, &row.ReplyCount, &row.LatestReplyAt, &row.ReplyNeedsReview); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, row.ID)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids, wantIDs) {
			t.Fatalf("report filter %+v page%d IDs=%v, want %v", filter, page, ids, wantIDs)
		}
	}
	live := ineligibleQueueFilters{LiveOnly: true}
	var all []int64
	for id := int64(2164); id >= 117; id-- {
		all = append(all, id)
	}
	// A total larger than 2000 detects accidental retention of the legacy cap.
	assertReports(live, 1, all[:50], 2048)
	assertReports(live, 2, all[50:100], 2048)
	assertReports(live, 41, all[2000:], 2048)
	assertReports(live, 42, nil, 2048)
	assertReports(ineligibleQueueFilters{TrainingOnly: true}, 1, []int64{104, 103, 102, 101, 100}, 5)
	assertReports(ineligibleQueueFilters{LiveOnly: true, Worklist: "deferred"}, 1, []int64{109, 108, 107, 106, 105}, 5)
	assertReports(ineligibleQueueFilters{LiveOnly: true, State: "all", Worklist: "all"}, 1, all[:50], 2059)
	assertReports(ineligibleQueueFilters{LiveOnly: true, State: "all", Worklist: "all", CaseStatus: "closed"}, 1, []int64{114, 113, 112, 111, 110}, 5)
	assertReports(ineligibleQueueFilters{LiveOnly: true, State: "all", Worklist: "all", CaseStatus: "withdrawn"}, 1, []int64{115}, 1)
	assertReports(ineligibleQueueFilters{LiveOnly: true, Search: "needle"}, 1, []int64{2160}, 1)
	assertReports(ineligibleQueueFilters{LiveOnly: true, Search: "absent player"}, 1, nil, 0)
	var oldest []int64
	for id := int64(117); id < 167; id++ {
		oldest = append(oldest, id)
	}
	assertReports(ineligibleQueueFilters{LiveOnly: true, Sort: "fixture_oldest"}, 1, oldest, 2048)
}
