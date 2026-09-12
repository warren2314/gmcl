package httpserver

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/sanctions"
)

// Reproduce an investigator's proposal being handed to Dave, then submitted
// for approval. General approvers must neither receive it nor see it in their
// decision queue; Dave and Warren must see it regardless of case ownership.
func TestIneligibleApprovalRoutingAfterHandover(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.New(ctx, os.Getenv("TEST_DB_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	stamp := time.Now().UnixNano()
	prefix := fmt.Sprintf("approval-routing-%d", stamp)
	nextID := int32(1_500_000_000 + stamp%400_000_000)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	admin := func(name, role string, designated, active bool) int32 {
		t.Helper()
		nextID++
		exec(`INSERT INTO admin_users(id,username,password_hash,email,role,is_active) VALUES($1,$2,'test'::bytea,$3,$4,$5)`, nextID, prefix+name, prefix+name+"@example.invalid", role, active)
		exec(`INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'sanctions_approve')`, nextID)
		if designated {
			exec(`INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'sanctions_ineligible_approve')`, nextID)
		}
		return nextID
	}
	steve := admin("steve", "admin", false, true)
	geoff := admin("geoff", "admin", false, true)
	dave := admin("dave", "admin", true, true)
	warren := admin("warren", "super_admin", true, true)
	otherSuper := admin("other-super", "super_admin", false, true)
	denver := admin("denver", "admin", true, true)
	inactive := admin("inactive", "admin", true, false)
	exec(`INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'sanctions_publish'),($1,'sanctions_ineligible_issue')`, denver)
	exec(`INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES('play_cricket','Test final issuer',$1)`, prefix+"denver@example.invalid")
	var caseID, decisionID int64
	if err := pool.QueryRow(ctx, `INSERT INTO sanction_cases(source_type,status,assigned_admin_id,proposed_by_admin_id) VALUES('ineligible_player','decision_proposed',$1,$2) RETURNING id`, dave, steve).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,status,public_reason,proposed_by_admin_id) VALUES($1,1,'proposed','Test proposed outcome',$2) RETURNING id`, caseID, steve).Scan(&decisionID); err != nil {
		t.Fatal(err)
	}
	service := sanctions.NewService(pool)
	actor := sanctions.Actor{Type: "admin", ID: &dave, Label: "Dave"}
	for i := 0; i < 2; i++ {
		if err := service.SubmitDecisionForApproval(ctx, caseID, actor); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{DB: pool}
	for _, tc := range []struct {
		name string
		id   int32
		want bool
	}{{"steve", steve, false}, {"geoff", geoff, false}, {"dave", dave, true}, {"warren", warren, true}, {"denver", denver, false}, {"inactive", inactive, false}, {"other-super", otherSuper, false}} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_notification_outbox WHERE case_id=$1 AND recipient=$2`, caseID, prefix+tc.name+"@example.invalid").Scan(&count); err != nil {
			t.Fatal(err)
		}
		wantCount := 0
		if tc.want {
			wantCount = 1
		}
		if count != wantCount {
			t.Fatalf("%s received %d alerts, want %d", tc.name, count, wantCount)
		}
		data, err := server.loadPersonalWorkDashboardWithLimit(ctx, tc.id, tc.name, 300)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range data.DecisionQueue {
			if item.CaseID == caseID {
				found = true
			}
		}
		if found != tc.want {
			t.Fatalf("%s approval queue contains case=%v, want %v", tc.name, found, tc.want)
		}
	}
	if err := service.RejectProposedCase(ctx, caseID, sanctions.Actor{Type: "admin", ID: &steve}, "Test rejection"); err == nil || !strings.Contains(err.Error(), "designated ineligible-player decision approver") {
		t.Fatalf("ordinary investigator rejection should be blocked, got %v", err)
	}
	// Simulate an alert left by the old broad-permission routing and a removed
	// reviewer. Maintenance must revoke both without attempting email delivery.
	exec(`INSERT INTO sanction_notification_outbox(case_id,decision_revision_id,message_kind,idempotency_key,recipient,subject,body) VALUES($1,$2,'decision_approval_request',$3,$4,'Old approval alert','Old body')`, caseID, decisionID, prefix+"legacy", prefix+"steve@example.invalid")
	exec(`DELETE FROM admin_user_permissions WHERE admin_user_id=$1 AND permission='sanctions_ineligible_approve'`, dave)
	t.Setenv("SANCTIONS_EMAIL_DISABLED", "true")
	response := httptest.NewRecorder()
	server.handleInternalSanctionOutbox()(response, httptest.NewRequest("POST", "/internal/sanctions/outbox", nil))
	if !strings.Contains(response.Body.String(), "email sending is disabled") {
		t.Fatalf("maintenance failed: %s", response.Body.String())
	}
	for _, name := range []string{"steve", "dave", "warren"} {
		var revoked bool
		if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM sanction_notification_outbox WHERE case_id=$1 AND recipient=$2`, caseID, prefix+name+"@example.invalid").Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		if revoked != (name != "warren") {
			t.Fatalf("%s revoked=%v", name, revoked)
		}
	}
	// A newer proposal must retire the old request even while the case still
	// has decision_proposed status and retains its old submission event.
	exec(`INSERT INTO sanction_decision_revisions(case_id,revision,status,public_reason) VALUES($1,2,'proposed','Revised proposal')`, caseID)
	response = httptest.NewRecorder()
	server.handleInternalSanctionOutbox()(response, httptest.NewRequest("POST", "/internal/sanctions/outbox", nil))
	var pending int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_notification_outbox WHERE case_id=$1 AND processed_at IS NULL AND revoked_at IS NULL`, caseID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("superseded proposal has %d pending approval alerts", pending)
	}
	// Approving and locking a reviewed decision hands over only to Denver.
	// The role that copies Play-Cricket outcome emails is not a sign-off role.
	stuart := admin("stuart", "admin", false, true)
	exec(`INSERT INTO admin_user_permissions(admin_user_id,permission) VALUES($1,'sanctions_publish')`, stuart)
	exec(`INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES('play_cricket','Test outcome copy',$1)`, prefix+"stuart@example.invalid")
	nextID++
	clubID := nextID
	exec(`INSERT INTO clubs(id,name) VALUES($1,$2)`, clubID, prefix+"club")
	exec(`INSERT INTO sanction_club_contacts(club_id,email,verified_at) VALUES($1,$2,now())`, clubID, prefix+"club@example.invalid")
	exec(`UPDATE sanction_cases SET club_id=$2 WHERE id=$1`, caseID, clubID)
	exec(`INSERT INTO sanction_case_parties(case_id,party_type,relationship,name) VALUES($1,'league','league','GMCL Official')`, caseID)
	for _, role := range []string{"executive", "discipline"} {
		exec(`INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES($1,$2,$3)`, role, prefix+role, prefix+role+"@example.invalid")
	}
	if err := service.ApproveCase(ctx, caseID, sanctions.Actor{Type: "admin", ID: &warren, Label: "Warren"}, ""); err != nil {
		t.Fatal(err)
	}
	var recipient string
	if err := pool.QueryRow(ctx, `SELECT recipient FROM sanction_notification_outbox WHERE case_id=$1 AND message_kind='final_sign_off_request'`, caseID).Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	if recipient != prefix+"denver@example.invalid" {
		t.Fatalf("final sign-off recipient=%s", recipient)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_notification_outbox WHERE case_id=$1 AND processed_at IS NULL AND revoked_at IS NULL`, caseID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("approval queued %d messages, want only Denver's sign-off request", pending)
	}
	for _, id := range []int32{steve, geoff, dave, warren, denver, stuart} {
		data, err := server.loadPersonalWorkDashboardWithLimit(ctx, id, "Test", 300)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range data.DecisionQueue {
			if item.CaseID == caseID {
				found = true
			}
		}
		if found != (id == denver) {
			t.Fatalf("final sign-off queue for admin %d contains case=%v", id, found)
		}
	}
	exec(`UPDATE sanction_cases SET status='published' WHERE id=$1`, caseID)
	response = httptest.NewRecorder()
	server.handleInternalSanctionOutbox()(response, httptest.NewRequest("POST", "/internal/sanctions/outbox", nil))
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_notification_outbox WHERE case_id=$1 AND processed_at IS NULL AND revoked_at IS NULL`, caseID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("issued case still has %d pending alerts", pending)
	}
}
