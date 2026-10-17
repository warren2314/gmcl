package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
)

// Exercise the real authenticated command handlers and their domain services
// in an isolated, fully migrated schema. Senders, jobs and external APIs never
// run. Approval and issue deliberately create immutable queue records, whose
// payloads are inspected here; queueing is not a claim of successful delivery.
func TestAdminWorkspaceStagingActionFlowPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("ADMIN_SESSION_SECRET", "synthetic-staging-action-secret-never-used-outside-tests")
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("SANCTIONS_EMAIL_DISABLED", "true")
	t.Setenv("INELIGIBLE_OUTBOUND_EMAIL_ENABLED", "false")
	t.Setenv("PUBLIC_BASE_URL", "https://staging.example.invalid")
	t.Setenv("PLAY_CRICKET_API_BASE_URL", "")
	t.Setenv("PLAY_CRICKET_API_KEY", "")
	seedAdminWorkspaceHandlerCases(t, ctx, pool)
	seedAdminWorkspaceStagingActions(t, ctx, pool)
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: pool, LondonLoc: london}
	router := chi.NewRouter()
	router.Mount("/admin", server.adminRouter())

	t.Run("record and review response with authority and CSRF", func(t *testing.T) {
		denied := adminWorkspaceHandlerRequest(t, router, 90008, http.MethodPost, "/admin/cases/911001/response-reviewed", nil)
		if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "csrf validation failed") {
			t.Fatalf("missing CSRF: %d %s", denied.Code, denied.Body.String())
		}
		assertStagingPostDenied(t, router, 90014, "/admin/cases/911001/response-reviewed", http.StatusForbidden, "forbidden", nil)
		response := stagingMultipartPost(t, router, 90008, "/admin/cases/911001/manual-response", url.Values{
			"channel": {"email"}, "respondent": {"club-response@example.invalid"}, "response": {"Synthetic private club reply: registration records were checked internally."},
		})
		assertStagingPostSucceeded(t, response)
		page := adminWorkspaceHandlerRequest(t, router, 90008, http.MethodGet, "/admin/cases/911001", nil)
		assertAdminWorkspaceHandlerHTML(t, page, []string{"Review the club reply", "Synthetic private club reply"}, nil)
		for i := 0; i < 2; i++ {
			assertStagingPostSucceeded(t, stagingPost(t, router, 90008, "/admin/cases/911001/response-reviewed", url.Values{"note": {"Synthetic owner read the complete reply"}}))
		}
		var reviews int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_case_events reviewed WHERE reviewed.case_id=911001 AND reviewed.event_type='response_reviewed'
			AND reviewed.metadata->>'response_event_id'=(SELECT id::text FROM sanction_case_events WHERE case_id=911001 AND event_type='external_response_recorded' ORDER BY id DESC LIMIT 1)`).Scan(&reviews); err != nil || reviews != 1 {
			t.Fatalf("latest response review must be attributable and idempotent: count=%d error=%v", reviews, err)
		}
	})
	if t.Failed() {
		return
	}

	t.Run("proposal owner notices designated approval separate issue", func(t *testing.T) {
		// An existing proposal grant does not let somebody else take the
		// assigned owner's decision. This command returns an error redirect.
		wrongOwner := stagingPost(t, router, 90007, "/admin/cases/911001/propose", stagingProposalForm())
		if wrongOwner.Code != http.StatusSeeOther || !strings.Contains(wrongOwner.Header().Get("Location"), "error=") {
			t.Fatalf("unassigned proposer was accepted: %d %s", wrongOwner.Code, wrongOwner.Header().Get("Location"))
		}
		assertStagingCaseStatus(t, ctx, pool, 911001, "investigating")
		approvedID, saved := stagingProposeReviewAndApprove(t, ctx, pool, router, 911001)
		assertStagingCaseStatus(t, ctx, pool, 911001, "approved")
		verifyStagingLockedNotices(t, ctx, pool, 911001, approvedID, saved)
		var proposer, approver int32
		var designatedSelfApproval bool
		if err := pool.QueryRow(ctx, `SELECT proposed_by_admin_id,approved_by_admin_id,emergency_override FROM sanction_decision_revisions WHERE id=$1`, approvedID).Scan(&proposer, &approver, &designatedSelfApproval); err != nil || proposer != 90008 || approver != 90008 || !designatedSelfApproval {
			t.Fatalf("designated same-proposer approval record: proposer=%d approver=%d policy_exception=%v error=%v", proposer, approver, designatedSelfApproval, err)
		}
		assertStagingPostDenied(t, router, 90008, "/admin/cases/911001/publish", http.StatusBadRequest, "only Denver", nil)
		assertStagingCaseStatus(t, ctx, pool, 911001, "approved")
		issuerPage := adminWorkspaceHandlerRequest(t, router, 90010, http.MethodGet, "/admin/cases/911001", nil)
		assertAdminWorkspaceHandlerHTML(t, issuerPage, []string{"Final sign-off and issue", "Synthetic reviewed notice version"}, nil)
		captureAdminWorkspaceHandlerHTML(t, "staging-approved-ready-to-issue", issuerPage.Body.String())
		assertStagingPostSucceeded(t, stagingPost(t, router, 90010, "/admin/cases/911001/publish", nil))
		assertStagingCaseStatus(t, ctx, pool, 911001, "published")
		var issuedMessages, mismatched, prematureAttempts int
		if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE outbox.subject<>approved.subject OR outbox.body<>approved.body
			OR queued.pdf_sha256<>approved.pdf_sha256 OR queued.pdf_bytes<>approved.pdf_bytes OR outbox.attachment_manifest<>approved.attachment_manifest)
			FROM sanction_notification_outbox outbox
			JOIN sanction_correspondence_revisions queued ON queued.id=outbox.correspondence_revision_id
			JOIN sanction_correspondence_revisions approved ON approved.id=queued.supersedes_id
			WHERE outbox.case_id=911001 AND outbox.message_kind LIKE 'outcome_%' AND queued.status='queued' AND approved.status='approved'`).Scan(&issuedMessages, &mismatched); err != nil || issuedMessages != 7 || mismatched != 0 {
			t.Fatalf("issued immutable snapshots: messages=%d mismatches=%d error=%v", issuedMessages, mismatched, err)
		}
		assertStagingPostDenied(t, router, 90010, "/admin/cases/911001/publish", http.StatusBadRequest, "publication", nil)
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_notification_attempts WHERE outbox_id IN(SELECT id FROM sanction_notification_outbox WHERE case_id=911001)`).Scan(&prematureAttempts); err != nil || prematureAttempts != 0 {
			t.Fatalf("a sender ran during command walkthrough: attempts=%d error=%v", prematureAttempts, err)
		}
		var activeEffects, yellowDelta, pointsTask int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM sanction_effect_revisions WHERE decision_revision_id=$1 AND status='active'),
			(SELECT COALESCE(sum(yellow_delta),0) FROM sanction_card_ledger_entries WHERE case_id=911001),
			(SELECT count(*) FROM sanction_follow_up_tasks WHERE case_id=911001 AND task_type='play_cricket_points' AND assigned_admin_id=90010 AND status='open')`, approvedID).Scan(&activeEffects, &yellowDelta, &pointsTask); err != nil || activeEffects != 3 || yellowDelta != 1 || pointsTask != 1 {
			t.Fatalf("approved effects/ledger/tasks: effects=%d yellow_delta=%d assigned_points_tasks=%d error=%v", activeEffects, yellowDelta, pointsTask, err)
		}
		var pointsTaskID int64
		if err := pool.QueryRow(ctx, `SELECT id FROM sanction_follow_up_tasks WHERE case_id=911001 AND task_type='play_cricket_points'`).Scan(&pointsTaskID); err != nil {
			t.Fatal(err)
		}
		assertStagingPostDenied(t, router, 90008, fmt.Sprintf("/admin/cases/tasks/%d", pointsTaskID), http.StatusForbidden, "assigned Play-Cricket administrator", url.Values{"status": {"complete"}, "reason": {"Synthetic unauthorized task update"}})
		assertStagingPostSucceeded(t, stagingPost(t, router, 90010, fmt.Sprintf("/admin/cases/tasks/%d", pointsTaskID), url.Values{"status": {"complete"}, "reason": {"Synthetic staging verification only"}, "note": {"Synthetic points task checked; no external API was called"}}))
		var taskEvents int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_follow_up_task_events WHERE task_id=$1 AND actor_admin_id=90010 AND after_data->>'status'='complete'`, pointsTaskID).Scan(&taskEvents); err != nil || taskEvents != 1 {
			t.Fatalf("completed follow-up audit: count=%d error=%v", taskEvents, err)
		}
		completed := adminWorkspaceHandlerRequest(t, router, 90008, http.MethodGet, "/admin/cases/911001", nil)
		assertAdminWorkspaceHandlerHTML(t, completed, []string{"Case outcome recorded", "Synthetic reviewed notice version"}, []string{`action="/admin/cases/911001/publish"`})
		captureAdminWorkspaceHandlerHTML(t, "staging-published-with-follow-up", completed.Body.String())
	})

	t.Run("changed source blocks issue and audited reopening preserves history", func(t *testing.T) {
		// Team PC ID is deliberately absent, so the link handler's optional
		// scorecard collector returns before constructing an external client.
		assertStagingPostSucceeded(t, stagingPost(t, router, 90007, "/admin/ineligible/991001/link-case", url.Values{"case_id": {"911002"}, "team_id": {"90002"}, "reporting_club_id": {"90002"}, "reason": {"Synthetic original source reviewed and mapped"}}))
		approvedID, saved := stagingProposeReviewAndApprove(t, ctx, pool, router, 911002)
		verifyStagingLockedNotices(t, ctx, pool, 911002, approvedID, saved)
		mustAdminWorkspaceFixtureExec(t, ctx, pool, `INSERT INTO sanction_intake_revisions(id,intake_id,revision,raw_data,raw_sha256)
			VALUES(991012,991001,2,'{"player":"Staging Player Two","reason":"Synthetic corrected immutable source allegation"}',repeat('e',64));
			UPDATE sanction_intakes SET latest_revision=2,state='exception',exception_message='Synthetic source revision requires review' WHERE id=991001`)
		assertStagingPostDenied(t, router, 90010, "/admin/cases/911002/publish", http.StatusBadRequest, "linked intake changed", nil)
		assertStagingCaseStatus(t, ctx, pool, 911002, "approved")
		page := adminWorkspaceHandlerRequest(t, router, 90008, http.MethodGet, "/admin/cases/911002", nil)
		assertAdminWorkspaceHandlerHTML(t, page, []string{"Review changed source evidence", "Synthetic corrected immutable source allegation"}, []string{`action="/admin/cases/911002/publish"`})
		captureAdminWorkspaceHandlerHTML(t, "staging-changed-source-blocks-issue", page.Body.String())
		assertStagingPostSucceeded(t, stagingPost(t, router, 90008, "/admin/cases/911002/reopen-source-change", url.Values{"reason": {"Synthetic staging source correction reviewed before delivery"}}))
		assertStagingCaseStatus(t, ctx, pool, 911002, "investigating")
		var corrected, cancelledEffects, reversedYellow, cancelledTasks, supersededNotices, outcomeMessages int
		if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM sanction_decision_revisions WHERE case_id=911002 AND status='corrected' AND supersedes_id=$1),
			(SELECT count(*) FROM sanction_effect_revisions WHERE decision_revision_id=(SELECT id FROM sanction_decision_revisions WHERE case_id=911002 ORDER BY revision DESC LIMIT 1) AND status='cancelled'),
			(SELECT COALESCE(sum(yellow_delta),0) FROM sanction_card_ledger_entries WHERE case_id=911002),
			(SELECT count(*) FROM sanction_follow_up_tasks WHERE case_id=911002 AND status='cancelled'),
			(SELECT count(*) FROM sanction_correspondence_revisions WHERE case_id=911002 AND status='superseded'),
			(SELECT count(*) FROM sanction_notification_outbox WHERE case_id=911002 AND message_kind LIKE 'outcome_%')`, approvedID).
			Scan(&corrected, &cancelledEffects, &reversedYellow, &cancelledTasks, &supersededNotices, &outcomeMessages); err != nil || corrected != 1 || cancelledEffects != 3 || reversedYellow != 0 || cancelledTasks != 2 || supersededNotices != 3 || outcomeMessages != 0 {
			t.Fatalf("reopen compensation: corrected=%d cancelled_effects=%d net_yellows=%d cancelled_tasks=%d superseded_notices=%d outcome_messages=%d error=%v", corrected, cancelledEffects, reversedYellow, cancelledTasks, supersededNotices, outcomeMessages, err)
		}
		verifyStagingLockedNotices(t, ctx, pool, 911002, approvedID, saved)
		assertStagingPostSucceeded(t, stagingPost(t, router, 90007, "/admin/ineligible/991001/link-case", url.Values{"case_id": {"911002"}, "team_id": {"90002"}, "reporting_club_id": {"90002"}, "reason": {"Synthetic corrected revision reviewed and merged"}}))
		var latestMerged, revisionID int64
		if err := pool.QueryRow(ctx, `SELECT id,revision_id FROM sanction_case_intake_merge_resolutions WHERE case_id=911002 AND intake_id=991001 ORDER BY id DESC LIMIT 1`).Scan(&latestMerged, &revisionID); err != nil || revisionID != 991012 {
			t.Fatalf("corrected source was not merged: resolution=%d revision=%d error=%v", latestMerged, revisionID, err)
		}
		assertStagingPostSucceeded(t, stagingPost(t, router, 90008, "/admin/cases/911002/propose", stagingProposalForm()))
		fresh := adminWorkspaceHandlerRequest(t, router, 90008, http.MethodGet, "/admin/cases/911002", nil)
		assertAdminWorkspaceHandlerHTML(t, fresh, []string{"Owner notice review", "approved_decision_reopened_source_changed"}, []string{`action="/admin/cases/911002/approve"`, "draft unavailable"})
		captureAdminWorkspaceHandlerHTML(t, "staging-reopened-fresh-owner-review", fresh.Body.String())
	})
	var realRecipients, attempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_notification_outbox WHERE case_id IN(911001,911002) AND recipient NOT LIKE '%@example.invalid'`).Scan(&realRecipients); err != nil || realRecipients != 0 {
		t.Fatalf("non-synthetic queued recipients=%d error=%v", realRecipients, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_notification_attempts WHERE outbox_id IN(SELECT id FROM sanction_notification_outbox WHERE case_id IN(911001,911002))`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("external delivery attempts=%d error=%v", attempts, err)
	}
}

func seedAdminWorkspaceStagingActions(t *testing.T, ctx context.Context, pool *db.Pool) {
	t.Helper()
	mustAdminWorkspaceFixtureExec(t, ctx, pool, `
		UPDATE sanction_recipient_directory SET active=false WHERE email NOT LIKE '%@example.invalid';
		INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES
			('executive','Synthetic executive','executive@example.invalid'),('discipline','Synthetic discipline','discipline@example.invalid'),('finance','Synthetic finance','finance@example.invalid');
		INSERT INTO sanction_club_contacts(club_id,email,verified_at) VALUES(90001,'offending@example.invalid',now()),(90002,'reporting@example.invalid',now());
		-- Wider fixture grants prove the domain still enforces the designated
		-- reviewer / separate final-issuer rules behind ordinary route grants.
		INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES(90008,'sanctions_publish'),(90009,'sanctions_publish'),(90010,'sanctions_approve');
		INSERT INTO teams(id,club_id,name) VALUES(90002,90001,'Synthetic Second XI');
		INSERT INTO weeks(id,season_id,week_number,start_date,end_date) VALUES(90001,90001,1,'2026-07-01','2026-07-07');
		INSERT INTO sanction_cases(id,reference,source_type,status,season_id,week_id,club_id,reporting_club_id,team_id,assigned_admin_id,public_summary,private_summary,player_name,match_date,reporter_name,reporter_email) VALUES
			(911001,'STAGING-ACTION-FLOW','ineligible_player','investigating',90001,90001,90001,90002,90001,90008,'An eligibility allegation requires investigation.','Synthetic private staged rationale','Staging Player One','2026-07-04','Synthetic Confidential Reporter','reporter@example.invalid'),
			(911002,'STAGING-SOURCE-REVIEW','ineligible_player','investigating',90001,90001,90001,90002,90002,90008,'An eligibility allegation requires investigation.','Synthetic private staged rationale','Staging Player Two','2026-07-05','Synthetic Confidential Reporter','reporter@example.invalid');
		INSERT INTO sanction_intakes(id,origin,external_key,state,latest_revision,reporting_club_text,player_text)
			VALUES(991001,'native_form','synthetic-staging-source','new',1,'Synthetic Reporting Club','Staging Player Two');
		INSERT INTO sanction_intake_revisions(id,intake_id,revision,raw_data,raw_sha256)
			VALUES(991011,991001,1,'{"player":"Staging Player Two","reason":"Synthetic original immutable source allegation"}',repeat('d',64));
	`)
}

func stagingProposalForm() url.Values {
	return url.Values{
		"public_reason":  {"The reviewed match evidence confirms an eligibility rule breach."},
		"private_reason": {"Synthetic private staged rationale"}, "rule_reference": {"3.5"},
		"outcome_subject": {"Synthetic eligibility outcome"}, "outcome_findings": {"The reviewed match evidence confirms an eligibility rule breach."},
		"appeal_instructions": {"Appeals follow the recorded league procedure."},
		"effect_type":         {"yellow_card", "fine", "points_adjustment"}, "fine_pounds": {"", "25.00", ""}, "points": {"", "", "-6"},
	}
}

func stagingPost(t *testing.T, router http.Handler, adminID int32, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", "synthetic-csrf-token")
	return adminWorkspaceHandlerRequest(t, router, adminID, http.MethodPost, path, form)
}

func stagingMultipartPost(t *testing.T, router http.Handler, adminID int32, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	form.Set("csrf_token", "synthetic-csrf-token")
	for name, values := range form {
		for _, value := range values {
			if err := writer.WriteField(name, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://synthetic.invalid"+path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: "synthetic-csrf-token"})
	addAdminWorkspaceHandlerSession(t, request, adminID)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertStagingPostSucceeded(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusSeeOther || strings.Contains(response.Header().Get("Location"), "error=") {
		t.Fatalf("staging command failed: status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func assertStagingPostDenied(t *testing.T, router http.Handler, adminID int32, path string, status int, message string, form url.Values) {
	t.Helper()
	response := stagingPost(t, router, adminID, path, form)
	if response.Code != status || !strings.Contains(response.Body.String(), message) {
		t.Fatalf("expected command rejection %d %q, got %d: %s", status, message, response.Code, response.Body.String())
	}
}

func assertStagingCaseStatus(t *testing.T, ctx context.Context, pool *db.Pool, id int64, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT status FROM sanction_cases WHERE id=$1`, id).Scan(&got); err != nil || got != want {
		t.Fatalf("case %d status=%q want=%q error=%v", id, got, want, err)
	}
}

func stagingProposeReviewAndApprove(t *testing.T, ctx context.Context, pool *db.Pool, router http.Handler, caseID int64) (int64, map[string]url.Values) {
	t.Helper()
	base := fmt.Sprintf("/admin/cases/%d", caseID)
	assertStagingPostSucceeded(t, stagingPost(t, router, 90008, base+"/propose", stagingProposalForm()))
	assertStagingCaseStatus(t, ctx, pool, caseID, "decision_proposed")
	assertStagingPostDenied(t, router, 90008, base+"/approve", http.StatusBadRequest, "case owner must review", nil)
	assertStagingPostDenied(t, router, 90008, base+"/send-for-approval", http.StatusBadRequest, "reload and review", nil)
	page := adminWorkspaceHandlerRequest(t, router, 90008, http.MethodGet, base, nil)
	assertAdminWorkspaceHandlerHTML(t, page, []string{"Owner notice review"}, []string{"draft unavailable"})
	saved := map[string]url.Values{}
	for action, form := range stagingOutcomeForms(t, page.Body.String()) {
		form.Set("subject", form.Get("subject")+" — synthetic reviewed")
		form.Set("body", form.Get("body")+"\n\nSynthetic reviewed notice version A.")
		assertStagingPostSucceeded(t, stagingPost(t, router, 90008, action, form))
		audience := action[strings.LastIndex(action, "/")+1:]
		saved[audience] = form
	}
	page = adminWorkspaceHandlerRequest(t, router, 90008, http.MethodGet, base, nil)
	assertAdminWorkspaceHandlerHTML(t, page, []string{"Synthetic reviewed notice version A."}, []string{"draft unavailable"})
	versions := url.Values{}
	for _, match := range regexp.MustCompile(`name="(reviewed_[^"]+)" value="([^"]+)"`).FindAllStringSubmatch(page.Body.String(), -1) {
		versions.Set(match[1], html.UnescapeString(match[2]))
	}
	if len(versions) != 3 {
		t.Fatalf("actual owner notice form has %d current audience versions, want 3", len(versions))
	}
	stale := url.Values{}
	for key, value := range versions {
		stale[key] = append([]string{}, value...)
	}
	stale.Set("reviewed_official", strings.Split(versions.Get("reviewed_official"), ":")[0]+":0")
	assertStagingPostDenied(t, router, 90008, base+"/send-for-approval", http.StatusBadRequest, "notice wording changed", stale)
	for i := 0; i < 2; i++ {
		assertStagingPostSucceeded(t, stagingPost(t, router, 90008, base+"/send-for-approval", versions))
	}
	var requests int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sanction_notification_outbox WHERE case_id=$1 AND message_kind='decision_approval_request' AND recipient='reviewer@example.invalid'`, caseID).Scan(&requests); err != nil || requests != 1 {
		t.Fatalf("duplicate submission duplicated designated approval alerts: count=%d error=%v", requests, err)
	}
	assertStagingPostDenied(t, router, 90009, base+"/approve", http.StatusBadRequest, "designated ineligible-player", nil)
	assertStagingPostDenied(t, router, 90010, base+"/approve", http.StatusBadRequest, "final sign-off account cannot also approve", nil)
	assertStagingPostSucceeded(t, stagingPost(t, router, 90008, base+"/approve", nil))
	var approvedID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM sanction_decision_revisions WHERE case_id=$1 AND status='approved' ORDER BY revision DESC LIMIT 1`, caseID).Scan(&approvedID); err != nil {
		t.Fatal(err)
	}
	var issuerAlerts, otherAlerts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE recipient='issuer@example.invalid'),count(*) FILTER(WHERE recipient<>'issuer@example.invalid')
		FROM sanction_notification_outbox WHERE case_id=$1 AND message_kind='final_sign_off_request'`, caseID).Scan(&issuerAlerts, &otherAlerts); err != nil || issuerAlerts != 1 || otherAlerts != 0 {
		t.Fatalf("final issue handover: issuer=%d others=%d error=%v", issuerAlerts, otherAlerts, err)
	}
	return approvedID, saved
}

func stagingOutcomeForms(t *testing.T, body string) map[string]url.Values {
	t.Helper()
	forms := map[string]url.Values{}
	for _, match := range regexp.MustCompile(`(?s)<form data-outcome-draft[^>]*action="([^"]+)"[^>]*>(.*?)</form>`).FindAllStringSubmatch(body, -1) {
		values := url.Values{}
		for _, input := range regexp.MustCompile(`name="([^"]+)"[^>]*value="([^"]*)"`).FindAllStringSubmatch(match[2], -1) {
			values.Set(input[1], html.UnescapeString(input[2]))
		}
		text := regexp.MustCompile(`(?s)<textarea[^>]*name="body"[^>]*>(.*?)</textarea>`).FindStringSubmatch(match[2])
		if len(text) != 2 || values.Get("decision_id") == "" || values.Get("draft_id") == "" || values.Get("subject") == "" {
			t.Fatal("actual outcome draft form is missing its current version or complete wording")
		}
		values.Set("body", html.UnescapeString(text[1]))
		forms[html.UnescapeString(match[1])] = values
	}
	if len(forms) != 3 {
		t.Fatalf("actual page contains %d audience notice forms, want 3", len(forms))
	}
	return forms
}

func verifyStagingLockedNotices(t *testing.T, ctx context.Context, pool *db.Pool, caseID, decisionID int64, saved map[string]url.Values) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT audience,subject,body,pdf_bytes,pdf_sha256,recipients::text FROM sanction_correspondence_revisions
		WHERE case_id=$1 AND decision_revision_id=$2 AND status='approved' ORDER BY audience`, caseID, decisionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var audience, subject, body, checksum, recipients string
		var pdf []byte
		if err := rows.Scan(&audience, &subject, &body, &pdf, &checksum, &recipients); err != nil {
			t.Fatal(err)
		}
		count++
		if subject != saved[audience].Get("subject") || body != saved[audience].Get("body") || !bytes.HasPrefix(pdf, []byte("%PDF")) || fmt.Sprintf("%x", sha256.Sum256(pdf)) != checksum {
			t.Errorf("approved %s notice differs from the exact owner-reviewed wording or verified PDF snapshot", audience)
		}
		if !strings.Contains(recipients, "@example.invalid") {
			t.Errorf("approved %s notice has no invented recipient: %s", audience, recipients)
		}
	}
	if err := rows.Err(); err != nil || count != 3 {
		t.Fatalf("locked audience notices=%s error=%v", strconv.Itoa(count), err)
	}
}
