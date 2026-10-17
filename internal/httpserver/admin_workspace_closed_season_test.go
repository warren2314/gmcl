package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClosedSeasonHomeZeroWorkKeepsScopedEntrances(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, personalWorkDashboard{AdminName: "Dave", CanInvestigate: true, CanApprove: true, CanTriage: true}, time.UTC, now)
	writeAdminHomeLeagueOverview(output, ineligibleDashboardCounts{}, nil, true)
	html := output.Body.String()
	for _, want := range []string{
		"No active cases are assigned to you.", "No new responses assigned to you.",
		"No decision, issue or changed-source review work currently needs your role.",
		"No open supporting tasks assigned to you.", "0 reports awaiting triage",
		"all seasons", "group=live", "group=delivery_exceptions", "/admin/cases/mine/ineligible",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("zero-work Home missing %q", want)
		}
	}
	for _, unwanted := range []string{"Unavailable", "No historical cases", "No case history", `href="/admin/cases/0`, "0 need attention"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("zero work falsely implies %q", unwanted)
		}
	}
	output = httptest.NewRecorder()
	writeAdminHomeLeagueOverview(output, ineligibleDashboardCounts{}, errors.New("test source unavailable"), true)
	html = output.Body.String()
	if !strings.Contains(html, "League counts unavailable") || strings.Contains(html, "0 reports awaiting triage") {
		t.Fatal("failed source is presented as a valid closed-season zero")
	}
}

func TestClosedSeasonCompletedFollowupsRemainVisibleWithoutActiveCases(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	future := time.Date(2027, 4, 17, 0, 0, 0, 0, time.UTC)
	output := httptest.NewRecorder()
	writePersonalWorkDashboard(output, personalWorkDashboard{
		CanInvestigate: true, CanTriage: true, TaskTotal: 1,
		Tasks: []personalWorkTask{{ID: 42, CaseID: 260, Reference: "IP-260 · completed case", Note: "Apply the approved future-season instruction", NotBefore: &future}},
	}, time.UTC, now)
	writeAdminHomeLeagueOverview(output, ineligibleDashboardCounts{DeliveryExceptions: 1}, nil, true)
	html := output.Body.String()
	for _, want := range []string{"No active cases are assigned to you.", "IP-260 · completed case", "Available from 17 Apr 2027", "/admin/cases/tasks?mine=1&amp;live=1#task-42", "Includes completed cases", "group=delivery_exceptions"} {
		if !strings.Contains(html, want) {
			t.Errorf("completed follow-up missing %q", want)
		}
	}
	if strings.Contains(html, "No open supporting tasks assigned to you.") || strings.Contains(html, "Overdue") {
		t.Fatal("closed season hides future work or turns it into a false overdue task")
	}
}

func TestClosedSeasonEmptyCaseListPreservesHistoryAndExportContext(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	filter := ineligibleCaseFilter{View: "cases", Scope: "mine", Page: 1}
	access := ineligibleCaseAccess{AdminID: 7, CanInvestigate: true}
	output := httptest.NewRecorder()
	writeIneligibleWorkspaceTabs(output, "cases", true, url.Values{"scope": {"mine"}})
	writeIneligibleCaseResults(output, nil, 0, filter, access, time.UTC, now)
	html := output.Body.String()
	for _, want := range []string{"0 matching cases", "assigned to you", "cases in progress", "live records", "all seasons", "No cases match these filters.", "check case history", "view=history", "Export matching cases", "Page 1 of 1 · 0 total matching records"} {
		if !strings.Contains(html, want) {
			t.Errorf("empty list missing %q", want)
		}
	}
	if strings.Contains(html, `href="/admin/cases/0`) || strings.Contains(html, ">Next</a>") {
		t.Fatal("empty list offers a nonexistent case or next page")
	}
	output = httptest.NewRecorder()
	writeIneligibleCaseResults(output, []ineligibleCaseWorkRow{{ID: 260, Reference: "IP-260", Status: "closed", Season: "2026", Owner: "Dave", OpenTasks: 1, DeliveryException: true, ReplyUnreviewed: true}}, 1,
		ineligibleCaseFilter{View: "history", Scope: "all", Page: 1}, access, time.UTC, now)
	html = output.Body.String()
	for _, want := range []string{"1 matching cases", "case history · live records", "Case outcome recorded", "Delivery problem", "1 open follow-up task(s)", "/admin/cases/260?return_to="} {
		if !strings.Contains(html, want) {
			t.Errorf("retained history missing %q", want)
		}
	}
	if strings.Contains(html, "Review the club reply") || strings.Contains(html, "· live work ·") {
		t.Fatal("historical outcome is presented as active investigation work")
	}
	label := ineligibleCaseScopeLabel(ineligibleCaseFilter{View: "cases", Scope: "all", Group: "delivery_exceptions"})
	if !strings.Contains(label, "includes case history") {
		t.Fatalf("delivery scope hides history inclusion: %s", label)
	}
}

func TestClosedSeasonCaseScopeLabelsMatchQueryOverrides(t *testing.T) {
	for _, test := range []struct {
		name   string
		filter ineligibleCaseFilter
		want   string
	}{
		{"active", ineligibleCaseFilter{View: "cases", Scope: "all"}, "all permitted cases · cases in progress · live records · all seasons"},
		{"history", ineligibleCaseFilter{View: "history", Scope: "mine"}, "assigned to you · case history · live records · all seasons"},
		{"closed status overrides cases", ineligibleCaseFilter{View: "cases", Scope: "all", CaseStatus: "closed"}, "all permitted cases · case history · live records · all seasons"},
		{"active status overrides history", ineligibleCaseFilter{View: "history", Scope: "all", CaseStatus: "investigating"}, "all permitted cases · cases in progress · live records · all seasons"},
		{"closed group", ineligibleCaseFilter{View: "cases", Scope: "all", Group: "closed"}, "all permitted cases · case history · live records · all seasons"},
		{"delivery includes both populations", ineligibleCaseFilter{View: "history", Scope: "all", Group: "delivery_exceptions"}, "all permitted cases · delivery follow-up (includes case history) · live records · all seasons"},
		{"delivery active status", ineligibleCaseFilter{View: "history", Scope: "all", Group: "delivery_exceptions", CaseStatus: "investigating"}, "all permitted cases · delivery follow-up · cases in progress · live records · all seasons"},
		{"delivery historical status", ineligibleCaseFilter{View: "cases", Scope: "all", Group: "delivery_exceptions", CaseStatus: "closed"}, "all permitted cases · delivery follow-up · case history · live records · all seasons"},
		{"training", ineligibleCaseFilter{View: "history", Scope: "all", Training: true}, "all permitted cases · case history · training and test cases · all seasons"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ineligibleCaseScopeLabel(test.filter); got != test.want {
				t.Errorf("scope label=%q, want %q", got, test.want)
			}
		})
	}
}

func TestClosedSeasonCaseQueriesKeepHistoryAndTasksPostgres(t *testing.T) {
	ctx, conn := ineligibleWorkspaceTestDB(t)
	if _, err := conn.Exec(ctx, `
		UPDATE sanction_cases SET status='closed';
		ALTER TABLE sanction_follow_up_tasks ADD COLUMN current_note text DEFAULT 'Completed-case task',
		ADD COLUMN due_at timestamptz, ADD COLUMN not_before timestamptz, ADD COLUMN assigned_admin_id integer DEFAULT 7;
	`); err != nil {
		t.Fatal(err)
	}
	access := ineligibleCaseAccess{AdminID: 7, CanInvestigate: true, CanReviewSource: true}
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{View: "cases", Scope: "all", Page: 1}, access, nil, 0)
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{View: "cases", Scope: "action", Page: 1}, access, nil, 0)
	history := assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{View: "history", Scope: "all", Page: 1}, access,
		[]int64{19, 18, 17, 16, 15, 14, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}, 16)
	if len(history) == 0 {
		t.Fatal("closed season erased history")
	}
	assertIneligibleWorkspaceCases(t, ctx, conn, ineligibleCaseFilter{View: "cases", Scope: "all", Group: "delivery_exceptions", Page: 1}, access, []int64{7, 2}, 2)
	rows, err := conn.Query(ctx, personalOpenTasksQuery, int32(7), 20)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var taskCount int
	for rows.Next() {
		var item personalWorkTask
		var total int64
		if err := rows.Scan(&item.ID, &item.CaseID, &item.Reference, &item.Note, &item.Status, &item.DueAt, &item.NotBefore, &total); err != nil {
			t.Fatal(err)
		}
		if total != 2 || item.CaseID != 1 {
			t.Fatalf("completed-case task population changed: case=%d total=%d", item.CaseID, total)
		}
		taskCount++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if taskCount != 2 {
		t.Fatalf("open tasks after closure=%d, want2", taskCount)
	}
}

// One connection keeps every table session-local. The direct Home handler is
// tested below only for its reporting read/render path; authentication and
// role-specific personal work are covered separately by handler tests.
func closedSeasonReportingServer(t *testing.T) (context.Context, *Server, time.Time) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DB_DSN"))
	if dsn == "" {
		t.Skip("TEST_DB_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: &db.Pool{Pool: pool}, LondonLoc: loc}
	today, err := time.Parse("2006-01-02", server.londonDate())
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		CREATE TEMP TABLE seasons(id integer PRIMARY KEY,name text,start_date date,end_date date,is_archived boolean DEFAULT false,compliance_start_week integer DEFAULT 1);
		CREATE TEMP TABLE weeks(id integer PRIMARY KEY,season_id integer,week_number integer,start_date date,end_date date);
		CREATE TEMP TABLE admin_users(id integer PRIMARY KEY,role text,username text,email text);
		CREATE TEMP TABLE admin_user_permissions(admin_user_id integer,permission text);
		CREATE TEMP TABLE clubs(id integer PRIMARY KEY,name text);
		CREATE TEMP TABLE teams(id integer PRIMARY KEY,club_id integer,name text,active boolean,play_cricket_team_id text);
		CREATE TEMP TABLE captains(id integer PRIMARY KEY,full_name text);
		CREATE TEMP TABLE league_fixtures(play_cricket_match_id bigint,home_team_pc_id text,away_team_pc_id text,match_date date,is_bye boolean);
		CREATE TEMP TABLE submissions(id bigint,season_id integer,week_id integer,team_id integer,captain_id integer,play_cricket_match_id bigint,match_date date,submitted_at timestamptz,pitch_rating integer);
		CREATE TEMP TABLE report_exemptions(week_id integer,team_id integer,match_date date,play_cricket_match_id bigint);
		CREATE TEMP TABLE sanctions(season_id integer,status text);
		INSERT INTO clubs VALUES(1,'Example Club'); INSERT INTO captains VALUES(1,'Example Captain');
		INSERT INTO teams VALUES(1,1,'1st XI',true,'111');
	`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO seasons VALUES
		(1,'Completed season',$1::date-200,$1::date-30,false,1),
		(2,'Next configured season',$1::date+180,$1::date+330,false,1);`, today.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO weeks VALUES(1,1,22,$1::date-36,$1::date-30),(2,2,1,$1::date+180,$1::date+186);`, today.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	match := today.AddDate(0, 0, -32)
	if match.Weekday() == time.Friday {
		match = match.AddDate(0, 0, -1)
	}
	_, err = pool.Exec(ctx, `INSERT INTO league_fixtures VALUES(3001,'111','999',$1::date,false);`, match.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO submissions VALUES(1,1,1,1,1,3001,$1::date,now(),4);`, match.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	return ctx, server, today
}

func TestClosedSeasonReportingPolicyPreservesExistingModesPostgres(t *testing.T) {
	ctx, server, today := closedSeasonReportingServer(t)
	assertWeek := func(mode competitionWeekResolution, want int32, status competitionWeekStatus) {
		t.Helper()
		week, err := server.resolveCompetitionWeek(ctx, mode)
		if err != nil || week.ID != want || week.Status != status {
			t.Fatalf("policy=%d week=%d/%s want=%d/%s error=%v", mode, week.ID, week.Status, want, status, err)
		}
	}
	assertWeek(competitionWeekForReporting, 1, competitionWeekCompleted)
	assertWeek(competitionWeekForDisplay, 2, competitionWeekUpcoming)
	assertWeek(competitionWeekForPlanning, 2, competitionWeekUpcoming)
	assertWeek(competitionWeekForSubmission, 1, competitionWeekCompleted)
	assertWeek(competitionWeekForSupportLink, 1, competitionWeekCompleted)
	if _, err := server.DB.Exec(ctx, `UPDATE seasons SET is_archived=true WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	assertWeek(competitionWeekForReporting, 2, competitionWeekUpcoming)
	if _, err := server.DB.Exec(ctx, `UPDATE seasons SET is_archived=false WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	assertWeek(competitionWeekForReporting, 1, competitionWeekCompleted)
	if _, err := server.resolveCompetitionWeek(ctx, competitionWeekActiveOnly); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("closed season inferred active week: %v", err)
	}
	if _, err := server.DB.Exec(ctx, `INSERT INTO seasons VALUES(3,'Current season',$1::date-1,$1::date+60,false,1);`, today.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if _, err := server.DB.Exec(ctx, `INSERT INTO weeks VALUES(3,3,7,$1::date-1,$1::date+1);`, today.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []competitionWeekResolution{competitionWeekForReporting, competitionWeekForDisplay, competitionWeekForPlanning, competitionWeekForSubmission, competitionWeekForSupportLink, competitionWeekActiveOnly} {
		assertWeek(mode, 3, competitionWeekActive)
	}
	if _, err := server.DB.Exec(ctx, `DELETE FROM weeks WHERE id IN(1,3); DELETE FROM seasons WHERE id IN(1,3);`); err != nil {
		t.Fatal(err)
	}
	assertWeek(competitionWeekForReporting, 2, competitionWeekUpcoming)
	assertWeek(competitionWeekForPlanning, 2, competitionWeekUpcoming)
	if _, err := server.DB.Exec(ctx, `DELETE FROM weeks`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.resolveCompetitionWeek(ctx, competitionWeekForReporting); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("empty schedule invented reporting week: %v", err)
	}
}

func TestClosedSeasonActualHomeKeepsCompletedReportingAndSeparatesUnavailablePostgres(t *testing.T) {
	ctx, server, _ := closedSeasonReportingServer(t)
	output := httptest.NewRecorder()
	server.handleAdminDashboard()(output, httptest.NewRequest("GET", "/admin/dashboard", nil))
	html := output.Body.String()
	for _, want := range []string{"Completed season", "Week 22", "100.0% complete · 1 of 1 satisfied", "1 received + 0 exempt + 0 missing = 1 required"} {
		if !strings.Contains(html, want) {
			t.Errorf("completed reporting Home missing %q", want)
		}
	}
	if strings.Contains(html, "Next configured season") || strings.Contains(html, "No reports required") {
		t.Fatal("configured future season captured completed Home reporting")
	}
	if _, err := server.DB.Exec(ctx, `ALTER TABLE submissions RENAME COLUMN pitch_rating TO unavailable_rating`); err != nil {
		t.Fatal(err)
	}
	output = httptest.NewRecorder()
	server.handleAdminDashboard()(output, httptest.NewRequest("GET", "/admin/dashboard", nil))
	html = output.Body.String()
	for _, want := range []string{"100.0% complete", "Unavailable", "Rating sample unavailable", "Pitch distribution unavailable"} {
		if !strings.Contains(html, want) {
			t.Errorf("independent unavailable sample missing %q", want)
		}
	}
	if strings.Contains(html, "0 rating observations") || strings.Contains(html, "No ratings</strong>") {
		t.Fatal("failed pitch sample was shown as a valid zero sample")
	}
}

func TestClosedSeasonAuthenticatedHomeRetainsCompletedFollowupPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-closed-season-handler-secret")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("SANCTIONS_EMAIL_DISABLED", "true")
	seedAdminWorkspaceHandlerCases(t, ctx, pool)
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: pool, LondonLoc: loc}
	today := server.londonDate()
	// Only the explicitly configured relative dates below determine this
	// synthetic closed-season scenario, even after migration-seeded years pass.
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `UPDATE seasons SET is_archived=true WHERE id<>90001`)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `UPDATE sanction_cases SET status='closed',closed_at=now()`)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `UPDATE seasons SET start_date=$1::date-200,end_date=$1::date-30 WHERE id=90001`, today)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `INSERT INTO weeks(id,season_id,week_number,start_date,end_date) VALUES(90001,90001,22,$1::date-36,$1::date-30)`, today)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `INSERT INTO seasons(id,name,start_date,end_date) VALUES(90002,'Synthetic future season',$1::date+180,$1::date+330)`, today)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `INSERT INTO weeks(id,season_id,week_number,start_date,end_date) VALUES(90002,90002,1,$1::date+180,$1::date+186)`, today)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `INSERT INTO weeks(id,season_id,week_number,start_date,end_date) VALUES(90003,90002,22,$1::date+190,$1::date+196)`, today)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `INSERT INTO seasons(id,name,start_date,end_date,is_archived) VALUES(90003,'Synthetic archived season',$1::date-400,$1::date-210,true)`, today)
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `UPDATE sanction_follow_up_tasks SET not_before=$1::date+180 WHERE id=970001`, today)

	personal, err := server.loadPersonalWorkDashboard(ctx, 90007, "Synthetic owner")
	if err != nil {
		t.Fatal(err)
	}
	if personal.AssignedTotal != 0 || personal.ResponseTotal != 0 || personal.DecisionTotal != 0 || personal.TestTotal != 0 || personal.TaskTotal != 1 {
		t.Fatalf("closed-season personal counts: cases=%d responses=%d decisions=%d training=%d tasks=%d", personal.AssignedTotal, personal.ResponseTotal, personal.DecisionTotal, personal.TestTotal, personal.TaskTotal)
	}
	league, err := server.loadIneligibleDashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if league.LiveCases != 0 || league.RecentReplies != 0 || league.AwaitingDecision != 0 || league.AwaitingDenverSignoff != 0 || league.NewIntakes != 0 || league.ClosedCases != 8 || league.DeliveryExceptions != 1 {
		t.Fatalf("closed-season league counts: %+v", league)
	}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())
	home := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/dashboard", nil)
	assertAdminWorkspaceHandlerHTML(t, home, []string{
		"Synthetic closed season &mdash; Week 22", "No active cases are assigned to you.",
		"No new responses assigned to you.", "No decision, issue or changed-source review work currently needs your role.",
		"SYNTHETIC-COMPLETED", "Synthetic follow-up evidence check", "Available from",
		"0 reports awaiting triage", "0 received + 0 exempt + 0 missing = 0 required", "No reports required",
		`href="/admin/cases/mine/ineligible"`, `href="/admin/compliance?week_id=90001"`,
		`href="#season-statistics"`, `href="/admin/rankings?season_id=90001"`, `href="/admin/sanctions?season_id=90001"`,
		`href="/admin/weeks?season_id=90001"`,
		`function reveal(hash)`, `node.tagName==='DETAILS'`, `node.open=true`,
		`>Executive report</strong>`,
		`/admin/cases/tasks?mine=1&amp;live=1#task-970001`,
	}, []string{"My work is temporarily unavailable", "League counts unavailable", "Synthetic future season", "No open supporting tasks assigned to you.", `href="/admin/cases/0`})
	captureAdminWorkspaceHandlerHTML(t, "closed-season-home", home.Body.String())
	rankings := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/rankings?season_id=90001", nil)
	assertAdminWorkspaceHandlerHTML(t, rankings, []string{`<option value="90001" selected>Synthetic closed season</option>`}, nil)
	ledger := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/sanctions?season_id=90001", nil)
	assertAdminWorkspaceHandlerHTML(t, ledger, []string{"Synthetic closed season", `name="season_id" value="90001"`}, nil)
	weeks := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/weeks?season_id=90001", nil)
	assertAdminWorkspaceHandlerHTML(t, weeks, []string{"Synthetic closed season", "/admin/weeks/22?season_id=90001"}, []string{"Synthetic future season"})
	week := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/weeks/22?season_id=90001&club=Synthetic", nil)
	assertAdminWorkspaceHandlerHTML(t, week, []string{"Synthetic closed season", `href="/admin/weeks?season_id=90001"`, `href="/admin/weeks/22?season_id=90001"`, `name="season_id" value="90001"`}, []string{"Synthetic future season"})
	defaultWeeks := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/weeks", nil)
	assertAdminWorkspaceHandlerHTML(t, defaultWeeks, []string{"Synthetic future season"}, []string{"Synthetic closed season"})
	defaultWeek := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/weeks/22", nil)
	assertAdminWorkspaceHandlerHTML(t, defaultWeek, []string{"Synthetic future season"}, []string{"Synthetic closed season"})
	for _, destination := range []string{"/admin/weeks", "/admin/weeks/22"} {
		for _, invalid := range []string{"", "bad", "0", "-1", "2147483648"} {
			response := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, destination+"?season_id="+invalid, nil)
			if response.Code != http.StatusBadRequest {
				t.Errorf("invalid season %q on %s returned %d", invalid, destination, response.Code)
			}
		}
		for _, unavailable := range []string{"90003", "99999"} {
			response := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, destination+"?season_id="+unavailable, nil)
			if response.Code != http.StatusNotFound {
				t.Errorf("archived/unknown season %q on %s returned %d", unavailable, destination, response.Code)
			}
		}
	}
	assigned := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/cases/mine/ineligible", nil)
	assertAdminWorkspaceHandlerHTML(t, assigned, []string{"No active ineligible-player cases are assigned to you.", "Test and completed cases are excluded"}, []string{"SYNTHETIC-COMPLETED", `href="/admin/cases/0`})
	history := adminWorkspaceHandlerRequest(t, router, 90007, http.MethodGet, "/admin/ineligible?view=history", nil)
	assertAdminWorkspaceHandlerHTML(t, history, []string{"8 matching cases", "case history · live records · all seasons", "SYNTHETIC-COMPLETED", "Delivery problem", "1 open follow-up task(s)"}, []string{"Casework and its count are unavailable"})
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_notification_outbox WHERE processed_at IS NULL`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("closed-season GETs queued messages: pending=%d error=%v", queued, err)
	}
}
