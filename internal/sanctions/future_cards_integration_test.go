package sanctions

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
)

// This exercises real proposal/approval/reversal transactions against the
// disposable migrated TEST_DB_DSN database used by CI. Audit rows are immutable,
// so fixtures have unique names and are discarded with that test database.
func TestFutureSeasonAwardLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service := NewService(pool)
	fixtureStamp := time.Now().UnixNano()
	unique := fmt.Sprintf("future-card-%d", fixtureStamp)
	// Other packages seed explicit low IDs without advancing the shared SERIAL
	// sequences. Reserve this run's own high IDs instead of racing those seeds.
	nextFixtureID := int32(1_500_000_000 + fixtureStamp%400_000_000)
	fixtureID := func() int32 {
		nextFixtureID++
		return nextFixtureID
	}
	queryID := func(query string, args ...any) int32 {
		t.Helper()
		var id int32
		if err := pool.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	season2026 := queryID(`INSERT INTO seasons(id,name,start_date,end_date) VALUES($1,$2,'2026-01-01','2026-12-31') RETURNING id`, fixtureID(), unique+"-2026")
	season2027 := queryID(`INSERT INTO seasons(id,name,start_date,end_date) VALUES($1,$2,'2027-01-01','2027-12-31') RETURNING id`, fixtureID(), unique+"-2027")
	ownerID := queryID(`INSERT INTO admin_users(id,username,password_hash,email) VALUES($1,$2,'test'::bytea,$3) RETURNING id`, fixtureID(), unique+"-owner", unique+"-owner@example.invalid")
	approverID := queryID(`INSERT INTO admin_users(id,username,password_hash,email) VALUES($1,$2,'test'::bytea,$3) RETURNING id`, fixtureID(), unique+"-approver", unique+"-approver@example.invalid")
	clubID := queryID(`INSERT INTO clubs(id,name) VALUES($1,$2) RETURNING id`, fixtureID(), unique)
	team1 := queryID(`INSERT INTO teams(id,club_id,name) VALUES($1,$2,'1st XI') RETURNING id`, fixtureID(), clubID)
	team2 := queryID(`INSERT INTO teams(id,club_id,name) VALUES($1,$2,'2nd XI') RETURNING id`, fixtureID(), clubID)
	exec(`INSERT INTO sanction_club_contacts(club_id,email,verified_at) VALUES($1,$2,now())`, clubID, unique+"-club@example.invalid")
	for _, role := range []string{"executive", "discipline", "play_cricket"} {
		exec(`INSERT INTO sanction_recipient_directory(recipient_role,name,email) VALUES($1,$2,$3)`, role, unique, unique+"-"+role+"@example.invalid")
	}
	owner := Actor{Type: "admin", ID: &ownerID, Label: "Test owner"}
	approver := Actor{Type: "admin", ID: &approverID, Label: "Test approver"}
	newCase := func(season int32, date string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO sanction_cases(source_type,status,season_id,club_id,team_id,match_date,public_summary,assigned_admin_id)
			VALUES('manual','investigating',$1,$2,$3,$4::date,'Test board decision',$5) RETURNING id`, season, clubID, team1, date, ownerID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Five current-season reds must not inflate a future award's six points.
	historyCase := newCase(season2026, "2026-06-01")
	var historicalDecision int64
	if err := pool.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,status,public_reason) VALUES($1,1,'approved','Historical cards') RETURNING id`, historyCase).Scan(&historicalDecision); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO sanction_card_ledger_entries(case_id,decision_revision_id,team_id,club_id,season_id,match_date,red_delta,points_deduction,entry_type,explanation)
		VALUES($1,$2,$3,$4,$5,'2026-06-01',5,15,'import','Test opening history')`, historyCase, historicalDecision, team1, clubID, season2026)
	caseID := newCase(season2026, "2026-09-01")
	var subject1, subject2 int64
	if err := pool.QueryRow(ctx, `INSERT INTO sanction_case_subjects(case_id,subject_type,team_id,is_primary) VALUES($1,'team',$2,true) RETURNING id`, caseID, team1).Scan(&subject1); err != nil {
		t.Fatal(err)
	}
	otherClub := queryID(`INSERT INTO clubs(id,name) VALUES($1,$2) RETURNING id`, fixtureID(), unique+"-other")
	otherTeam := queryID(`INSERT INTO teams(id,club_id,name) VALUES($1,$2,'Other XI') RETURNING id`, fixtureID(), otherClub)
	for _, invalid := range []struct {
		team   int32
		reason string
		actor  Actor
	}{
		{team2, "", owner}, {team2, "Meeting decision", approver}, {team1, "Meeting decision", owner}, {otherTeam, "Meeting decision", owner},
	} {
		if err := service.AddCaseTeam(ctx, caseID, invalid.team, invalid.reason, invalid.actor); err == nil {
			t.Fatal("invalid additional team accepted")
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.AddCaseTeam(ctx, caseID, team2, "Meeting agreed a separate second-XI deduction", owner); err != nil {
			t.Fatal(err)
		}
	}
	var addedEvents, parties int
	var savedPrimary int32
	if err := pool.QueryRow(ctx, `SELECT team_id,(SELECT COUNT(*) FROM sanction_case_events WHERE case_id=$1 AND event_type='case_team_added'),(SELECT COUNT(*) FROM sanction_case_parties WHERE case_id=$1 AND team_id=$2 AND relationship='offending_club') FROM sanction_cases WHERE id=$1`, caseID, team2).Scan(&savedPrimary, &addedEvents, &parties); err != nil {
		t.Fatal(err)
	}
	if savedPrimary != team1 || addedEvents != 1 || parties != 1 {
		t.Fatalf("team addition changed primary or duplicated audit/party: %d/%d/%d", savedPrimary, addedEvents, parties)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM sanction_case_subjects WHERE case_id=$1 AND subject_type='team' AND team_id=$2`, caseID, team2).Scan(&subject2); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	first, second := -41, -6
	req := DecisionBundleRequest{CaseID: caseID, PublicReason: "Meeting outcome", PrivateReason: "Test decision", RuleReference: "Board award", Actor: owner, Effects: []DecisionEffectRequest{
		{EffectType: "points_adjustment", CaseSubjectID: &subject1, Points: &first},
		{EffectType: "points_adjustment", CaseSubjectID: &subject2, Points: &second},
		{EffectType: "scheduled_red", CaseSubjectID: &subject1, TargetSeasonID: &season2027, StartsAt: &start, RedCardCount: 3},
		{EffectType: "suspended_red", CaseSubjectID: &subject2, TargetSeasonID: &season2027, StartsAt: &start, RedCardCount: 2, Trigger: "Further eligibility breach"},
	}}
	decisionID, err := service.ProposeDecisionBundle(ctx, req)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if err := service.AddCaseTeam(ctx, caseID, team2, "Cannot change saved decision", owner); err == nil {
		t.Fatal("saved decision allowed team mutation")
	}
	var points, count int
	var target int32
	if err := pool.QueryRow(ctx, `SELECT points,red_card_count,target_season_id FROM sanction_effect_revisions WHERE decision_revision_id=$1 AND effect_type='scheduled_red'`, decisionID).Scan(&points, &count, &target); err != nil {
		t.Fatal(err)
	}
	if points != 6 || count != 3 || target != season2027 {
		t.Fatalf("proposal points/count/target=%d/%d/%d", points, count, target)
	}
	if err := service.AmendProposedDecision(ctx, caseID, owner, "Verify scheduled award survives amendment"); err != nil {
		t.Fatal(err)
	}
	var preserved bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sanction_effect_revisions e JOIN sanction_decision_revisions d ON d.id=e.decision_revision_id WHERE d.case_id=$1 AND d.status='corrected' AND e.effect_type='scheduled_red' AND e.target_season_id=$2 AND e.red_card_count=3 AND e.starts_at=$3)`, caseID, season2027, start).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("amendment fields preserved=%v err=%v", preserved, err)
	}
	if _, err = service.ProposeDecisionBundle(ctx, req); err != nil {
		t.Fatal(err)
	}
	const customNotice = "Penalty notices issued via manual message from Denver."
	exec(`UPDATE sanction_cases SET reporter_email=$2 WHERE id=$1`, caseID, unique+"-private-reporter@example.invalid")
	var editedDraft OutcomeDraft
	for _, audience := range []string{"offending_club", "reporting_club", "official"} {
		draft, draftErr := service.OutcomeDraft(ctx, caseID, audience)
		if draftErr != nil {
			t.Fatal(draftErr)
		}
		version := OutcomeDraftVersion{DecisionID: draft.DecisionID, DraftID: draft.ID, Audience: audience}
		if _, draftErr = service.SaveOutcomeDraft(ctx, caseID, audience, draft.Subject, draft.Body, approver, version); draftErr == nil {
			t.Fatal("non-owner edited notice")
		}
		if _, draftErr = service.SaveOutcomeDraft(ctx, caseID, audience, draft.Subject, "Incomplete notice", owner, version); draftErr == nil {
			t.Fatal("incomplete notice accepted")
		}
		if _, draftErr = service.SaveOutcomeDraft(ctx, caseID, audience, draft.Subject, draft.Body+"\n"+unique+"-private-reporter@example.invalid", owner, version); draftErr == nil {
			t.Fatal("private reporter address accepted")
		}
		editedDraft, draftErr = service.SaveOutcomeDraft(ctx, caseID, audience, "Reviewed: "+draft.Subject, draft.Body+"\n\n"+customNotice, owner, version)
		if draftErr != nil {
			t.Fatalf("save %s: %v", audience, draftErr)
		}
		if _, draftErr = service.SaveOutcomeDraft(ctx, caseID, audience, draft.Subject, draft.Body, owner, version); draftErr == nil {
			t.Fatal("stale notice overwrote current wording")
		}
		if draftErr = service.SubmitDecisionForApproval(ctx, caseID, owner, version); draftErr == nil {
			t.Fatal("stale owner review submitted")
		}
		loaded, loadErr := service.OutcomeDraft(ctx, caseID, audience)
		if loadErr != nil || loaded.Body != editedDraft.Body || loaded.Subject != editedDraft.Subject {
			t.Fatalf("edited notice lost on reload: %v", loadErr)
		}
		pdf, _, previewErr := service.PreviewOutcomeLetter(ctx, caseID, audience)
		if previewErr != nil || !strings.Contains(string(pdf), customNotice) {
			t.Fatalf("edited PDF preview failed: %v", previewErr)
		}
	}
	if err = service.SubmitDecisionForApproval(ctx, caseID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveOutcomeDraft(ctx, caseID, editedDraft.Audience, editedDraft.Subject, editedDraft.Body, owner); err == nil {
		t.Fatal("submitted notice remained editable")
	}
	var alreadyConfigured bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users admin JOIN sanction_recipient_directory recipient ON LOWER(BTRIM(recipient.email))=LOWER(BTRIM(admin.email)) WHERE admin.is_active AND recipient.active AND recipient.recipient_role='play_cricket')`).Scan(&alreadyConfigured); err != nil {
		t.Fatal(err)
	}
	if !alreadyConfigured {
		if err := service.ApproveCase(ctx, caseID, approver, ""); err == nil || !strings.Contains(err.Error(), "configure the Play-Cricket administrator") {
			t.Fatalf("missing Play-Cricket admin should block approval: %v", err)
		}
		var reservedRows int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_card_ledger_entries WHERE case_id=$1`, caseID).Scan(&reservedRows); err != nil || reservedRows != 0 {
			t.Fatalf("failed approval retained ledger rows=%d err=%v", reservedRows, err)
		}
	}
	queryID(`INSERT INTO admin_users(id,username,password_hash,email) VALUES($1,$2,'test'::bytea,$3) RETURNING id`, fixtureID(), unique+"-play-cricket", unique+"-play_cricket@example.invalid")
	if err = service.ApproveCase(ctx, caseID, approver, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err = service.SaveOutcomeDraft(ctx, caseID, editedDraft.Audience, editedDraft.Subject, editedDraft.Body, owner); err == nil {
		t.Fatal("approved notice remained editable")
	}
	lockedPDF, _, pdfErr := service.PreviewOutcomeLetter(ctx, caseID, "offending_club")
	if pdfErr != nil || !strings.Contains(string(lockedPDF), customNotice) {
		t.Fatalf("approved PDF lost edited wording: %v", pdfErr)
	}
	if err = service.ApproveCase(ctx, caseID, approver, ""); err == nil {
		t.Fatal("second approval unexpectedly accepted")
	}
	var originalPoints, futurePoints, reds, ledgerRows int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(points_deduction) FILTER(WHERE season_id=$2),0),COALESCE(SUM(points_deduction) FILTER(WHERE season_id=$3),0),COALESCE(SUM(red_delta) FILTER(WHERE season_id=$3),0),COUNT(*) FROM sanction_card_ledger_entries WHERE case_id=$1`, caseID, season2026, season2027).Scan(&originalPoints, &futurePoints, &reds, &ledgerRows); err != nil {
		t.Fatal(err)
	}
	if originalPoints != 0 || futurePoints != 6 || reds != 3 || ledgerRows != 1 {
		t.Fatalf("ledger original=%d future=%d reds=%d rows=%d", originalPoints, futurePoints, reds, ledgerRows)
	}
	var taskCount, scheduledTasks, suspendedTasks int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE task_type='play_cricket_points'),COUNT(*) FILTER(WHERE task_type='play_cricket_points' AND not_before=$2 AND due_at=$2),COUNT(*) FILTER(WHERE task_type='suspended_review' AND not_before=$2) FROM sanction_follow_up_tasks WHERE case_id=$1`, caseID, start).Scan(&taskCount, &scheduledTasks, &suspendedTasks); err != nil {
		t.Fatal(err)
	}
	if taskCount != 3 || scheduledTasks != 1 || suspendedTasks != 1 {
		t.Fatalf("tasks points=%d scheduled=%d suspended=%d", taskCount, scheduledTasks, suspendedTasks)
	}
	var futureTaskAssigned bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM sanction_follow_up_tasks task JOIN admin_users admin ON admin.id=task.assigned_admin_id
		JOIN sanction_recipient_directory recipient ON LOWER(BTRIM(recipient.email))=LOWER(BTRIM(admin.email))
		WHERE task.case_id=$1 AND task.task_type='play_cricket_points' AND task.not_before=$2
		  AND admin.is_active AND recipient.active AND recipient.recipient_role='play_cricket')`, caseID, start).Scan(&futureTaskAssigned); err != nil || !futureTaskAssigned {
		t.Fatalf("scheduled task assigned to active Play-Cricket admin=%v err=%v", futureTaskAssigned, err)
	}
	var adjustmentsCorrect bool
	if err := pool.QueryRow(ctx, `SELECT COUNT(*)=2 AND BOOL_AND((e.subject_id=$2 AND e.points=-41) OR (e.subject_id=$3 AND e.points=-6)) FROM sanction_effect_revisions e JOIN sanction_decision_revisions d ON d.id=e.decision_revision_id WHERE d.case_id=$1 AND d.status='approved' AND e.effect_type='points_adjustment'`, caseID, team1, team2).Scan(&adjustmentsCorrect); err != nil || !adjustmentsCorrect {
		t.Fatalf("adjustments correctly mapped=%v err=%v", adjustmentsCorrect, err)
	}
	var conditionalSafe bool
	if err := pool.QueryRow(ctx, `SELECT e.status='suspended' AND e.red_card_count=2 AND e.target_season_id=$2 AND COALESCE(e.points,0)=0 AND NOT e.counts_for_totting FROM sanction_effect_revisions e JOIN sanction_decision_revisions d ON d.id=e.decision_revision_id WHERE d.case_id=$1 AND d.status='approved' AND e.effect_type='suspended_red'`, caseID, season2027).Scan(&conditionalSafe); err != nil || !conditionalSafe {
		t.Fatalf("conditional award remains unapplied=%v err=%v", conditionalSafe, err)
	}
	var outcome string
	if err := pool.QueryRow(ctx, `SELECT body FROM sanction_correspondence_revisions WHERE case_id=$1 AND status='approved' AND audience='offending_club' ORDER BY id DESC LIMIT 1`, caseID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 red cards", "target season 2027", "effective 1 January 2027", "Further eligibility breach", customNotice} {
		if !strings.Contains(outcome, want) {
			t.Fatalf("outcome missing %q: %s", want, outcome)
		}
	}
	nextCase := newCase(season2027, "2027-06-01")
	preview, err := service.PreviewCaseDirectRed(ctx, nextCase)
	if err != nil || preview.Before.TeamRedCount != 3 || preview.DirectRed.PointsDeduction != 4 {
		t.Fatalf("next-season escalation preview=%+v err=%v", preview, err)
	}
	if err := service.OverturnCase(ctx, caseID, approver, "Test reversal of future award"); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(red_delta),0),COALESCE(SUM(points_deduction),0) FROM sanction_card_ledger_entries WHERE case_id=$1 AND season_id=$2`, caseID, season2027).Scan(&reds, &futurePoints); err != nil {
		t.Fatal(err)
	}
	if reds != 0 || futurePoints != 0 {
		t.Fatalf("reversal reds=%d points=%d", reds, futurePoints)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sanction_follow_up_tasks WHERE case_id=$1 AND effect_key IS NOT NULL AND status IN ('open','in_progress')`, caseID).Scan(&taskCount); err != nil || taskCount != 0 {
		t.Fatalf("linked tasks left open=%d err=%v", taskCount, err)
	}
}
