package httpserver

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIneligibleNewRepliesRequireALiveCase(t *testing.T) {
	for _, alias := range []string{"", "cases", "c"} {
		predicate := ineligibleCaseGroupPredicate("new_replies", alias)
		if !strings.Contains(predicate, ineligibleCaseGroupPredicate("live", alias)) {
			t.Fatalf("new replies do not enforce the live-case predicate for alias %q: %s", alias, predicate)
		}
		for _, want := range []string{"party_response", "external_response_recorded", "response_reviewed", "response_event_id", "ORDER BY event.id DESC LIMIT 1"} {
			if !strings.Contains(predicate, want) {
				t.Fatalf("new replies lost their existing latest/unreviewed reply handling: missing %q", want)
			}
		}
	}
}

func TestIneligibleActiveQueuesExcludeFinishedCases(t *testing.T) {
	adminID := int32(7)
	for _, tt := range []struct {
		name   string
		filter ineligibleQueueFilters
	}{
		{"default", ineligibleQueueFilters{}},
		{"selected", ineligibleQueueFilters{State: "open", Worklist: "visible", Scope: "all"}},
		{"assigned", ineligibleQueueFilters{State: "all", Worklist: "visible", Scope: "mine"}},
		{"unreviewed replies", ineligibleQueueFilters{State: "all", Worklist: "all", ReplyStatus: "unreviewed"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			query, _ := buildIneligibleQueueQueryForAdmin(tt.filter, &adminID)
			if !strings.Contains(query, "(c.id IS NULL OR c.status NOT IN ('published','closed','rejected','withdrawn'))") {
				t.Fatalf("active queue can include a finished case: %s", query)
			}
		})
	}
}

func TestIneligibleFinishedCasesRemainSearchableInHistory(t *testing.T) {
	activeGuard := "(c.id IS NULL OR c.status NOT IN ('published','closed','rejected','withdrawn'))"
	history := ineligibleQueueFilters{State: "all", Worklist: "all", Scope: "all"}
	query, _ := buildIneligibleQueueQuery(history)
	if strings.Contains(query, activeGuard) {
		t.Fatal("report history must retain completed cases")
	}
	for _, status := range []string{"published", "closed", "rejected", "withdrawn"} {
		filter := history
		filter.CaseStatus = status
		query, args := buildIneligibleQueueQuery(filter)
		if strings.Contains(query, activeGuard) || !strings.Contains(query, "c.status=$1") {
			t.Fatalf("explicit %q status search was overridden: %s", status, query)
		}
		if !reflect.DeepEqual(args, []any{status}) {
			t.Fatalf("explicit %q status arguments = %#v", status, args)
		}
	}
}

func TestIneligibleVisibleCountExcludesFinishedLinkedCases(t *testing.T) {
	if !strings.Contains(ineligibleDashboardSource(t), "intake.linked_case_status NOT IN ('published','closed','rejected','withdrawn')") {
		t.Fatal("visible queue count must use the same finished-case exclusion as the linked queue")
	}
}

// Use session-local temporary tables, never persistent case records. CI sets
// TEST_DB_DSN; a developer without an integration database runs the unit tests.
func TestIneligibleClosedCaseDoesNotReturnAsNewReplyPostgres(t *testing.T) {
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
		CREATE TEMP TABLE sanction_cases (
			id bigint PRIMARY KEY,
			source_type text NOT NULL,
			status text NOT NULL,
			player_name text NOT NULL DEFAULT 'Example Player'
		);
		CREATE TEMP TABLE sanction_case_events (
			id bigint PRIMARY KEY,
			case_id bigint NOT NULL,
			event_type text NOT NULL,
			metadata jsonb NOT NULL DEFAULT '{}'
		);
		INSERT INTO sanction_cases(id,source_type,status) VALUES
			(1,'ineligible_player','response_pending'),
			(2,'ineligible_player','closed'),
			(3,'ineligible_player','published'),
			(4,'ineligible_player','rejected'),
			(5,'ineligible_player','withdrawn'),
			(6,'ineligible_player','investigating'),
			(7,'another_source','investigating'),
			(8,'ineligible_player','investigating');
		INSERT INTO sanction_case_events(id,case_id,event_type)
			SELECT id,id,'party_response' FROM sanction_cases;
		INSERT INTO sanction_case_events(id,case_id,event_type,metadata)
			VALUES(9,8,'response_reviewed','{"response_event_id":"8"}');
	`); err != nil {
		t.Fatal(err)
	}
	assertNewReplies := func(want []int64) {
		t.Helper()
		query := "SELECT cases.id FROM sanction_cases cases WHERE " + ineligibleCaseGroupPredicate("new_replies", "cases") + " ORDER BY cases.id"
		rows, err := conn.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
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
			t.Fatalf("new replies = %v, want %v", got, want)
		}
	}

	// A separate live case for the same name must remain visible. Finished
	// cases, reviewed replies, and other case sources must not be counted.
	assertNewReplies([]int64{1, 6})
	if _, err = conn.Exec(ctx, "UPDATE sanction_cases SET status='closed' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	assertNewReplies([]int64{6})

	// A subsequently recorded reply is evidence, not permission to resurrect
	// work that has already been closed.
	if _, err = conn.Exec(ctx, "INSERT INTO sanction_case_events(id,case_id,event_type) VALUES(10,1,'external_response_recorded')"); err != nil {
		t.Fatal(err)
	}
	assertNewReplies([]int64{6})
	var status string
	var replies int
	if err = conn.QueryRow(ctx, "SELECT status,(SELECT COUNT(*) FROM sanction_case_events WHERE case_id=1) FROM sanction_cases WHERE id=1").Scan(&status, &replies); err != nil {
		t.Fatal(err)
	}
	if status != "closed" || replies != 2 {
		t.Fatalf("closure/history changed: status=%q reply events=%d", status, replies)
	}

	// An explicit later reopening restores the case to the active queue.
	if _, err = conn.Exec(ctx, "UPDATE sanction_cases SET status='investigating' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	assertNewReplies([]int64{1, 6})
}
