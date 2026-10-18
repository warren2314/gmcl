package httpserver

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cricket-ground-feedback/internal/junior"
)

func TestJuniorWorkflowDispatchRejectsStaleMessagesPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	t.Setenv("JUNIOR_OUTBOUND_EMAIL_ENABLED", "false")
	seed := seedJuniorFixtureProgramme(t, ctx, pool)
	service := junior.NewService(pool)
	staff := seed.Admin
	lead := seedJuniorAccess(t, pool, seed.Clubs[0], staff.AdminID, "dispatch-lead@example.invalid")
	sent := 0
	service.Delivery = func(_ context.Context, envelope junior.MailEnvelope) error {
		if envelope.To != "dispatch-lead@example.invalid" {
			t.Fatal("unexpected invented workflow recipient")
		}
		sent++
		return nil
	}
	messageID := func(kind string, recordID int64) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM junior_messages WHERE kind=$1 AND record_id=$2 AND club_id=$3 ORDER BY id DESC LIMIT 1`, kind, recordID, seed.Clubs[0]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	blocked := func(id int64, context string) {
		t.Helper()
		before := sent
		if err := service.DispatchMessage(ctx, staff, id); !errors.Is(err, junior.ErrConflict) {
			t.Fatalf("%s dispatched or returned wrong error: %v", context, err)
		}
		if sent != before {
			t.Fatalf("%s reached fake sender", context)
		}
		var attempts int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM junior_message_attempts attempt JOIN junior_message_recipients recipient ON recipient.id=attempt.recipient_id WHERE recipient.message_id=$1`, id).Scan(&attempts); err != nil || attempts != 0 {
			t.Fatalf("%s recorded a transport attempt: count=%d err=%v", context, attempts, err)
		}
	}
	input := junior.FormInput{SeasonID: seed.SeasonID, Title: "Synthetic dispatch reminder", DueDate: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC), RequiresReview: true, ClubIDs: []int32{seed.Clubs[0]}, Fields: []junior.FormField{{Key: "answer", Label: "Answer", Type: "text", Required: true}}}
	formID, err := service.PublishForm(ctx, staff, input)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := service.FormAssignments(ctx, staff, formID)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("reminder assignment: %+v %v", assignments, err)
	}
	assignmentID := assignments[0].ID
	if _, err := service.QueueFormReminders(ctx, staff, formID); err != nil {
		t.Fatal(err)
	}
	reminderID := messageID("form_reminder", assignmentID)
	if _, err := service.SaveFormResponse(ctx, lead.Principal, assignmentID, 0, map[string]string{"answer": "Submitted after reminder preparation"}, true); err != nil {
		t.Fatal(err)
	}
	blocked(reminderID, "submitted form reminder")
	comp, err := service.CreateCompetition(ctx, staff, junior.Competition{SeasonID: seed.SeasonID, Name: "Synthetic dispatch fixtures", AgeGroup: "U13", Format: "once", StartDate: "2027-05-01", EndDate: "2027-05-31", DefaultStartTime: "18:00"})
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 { return juniorCompetitionRevision(t, ctx, service, staff, comp) }
	for _, teamID := range seed.Teams[:2] {
		if err := service.SaveCompetitionEntry(ctx, staff, comp, revision(), teamID, true); err != nil {
			t.Fatal(err)
		}
	}
	constraints := junior.FixtureConstraints{PlayingDates: []string{"2027-05-01"}, ReserveDates: []string{"2027-05-08"}, DurationMinutes: 60, Grounds: []junior.GroundConstraint{{ClubID: seed.Clubs[0], Name: "Synthetic A", Capacity: 1}, {ClubID: seed.Clubs[1], Name: "Synthetic B", Capacity: 1}}}
	if err := service.SaveFixtureConstraints(ctx, staff, comp, revision(), constraints); err != nil {
		t.Fatal(err)
	}
	if err := service.GenerateFixtures(ctx, staff, comp, revision()); err != nil {
		t.Fatal(err)
	}
	first, err := service.PublishFixtures(ctx, staff, comp, revision(), true, "Reviewed all synthetic bookings")
	if err != nil {
		t.Fatal(err)
	}
	oldPublicationMessage := messageID("fixture_publication", first)
	constraints.ReserveDates = append(constraints.ReserveDates, "2027-05-15")
	if err := service.SaveFixtureConstraints(ctx, staff, comp, revision(), constraints); err != nil {
		t.Fatal(err)
	}
	current, err := service.PublishFixtures(ctx, staff, comp, revision(), true, "Reviewed amended synthetic reserve date")
	if err != nil {
		t.Fatal(err)
	}
	blocked(oldPublicationMessage, "superseded fixture publication")
	if err := service.DispatchMessage(ctx, staff, messageID("fixture_publication", current)); err != nil {
		t.Fatalf("current official publication was blocked: %v", err)
	}
	if sent != 1 {
		t.Fatalf("current official publication sends=%d", sent)
	}
	fixtures, err := service.ListFixtures(ctx, staff, comp, true)
	if err != nil || len(fixtures) != 1 {
		t.Fatalf("published fixture: %+v %v", fixtures, err)
	}
	result := []byte(fmt.Sprintf("fixture_id,outcome,evidence\n%d,possible_concession,Synthetic source needs review\n", fixtures[0].ID))
	if _, err := service.ImportFixtureResultsCSV(ctx, staff, comp, "Synthetic dispatch source", result); err != nil {
		t.Fatal(err)
	}
	candidates, err := service.ListConcessions(ctx, staff, junior.ConcessionFilter{CompetitionID: comp})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("information candidate: %+v %v", candidates, err)
	}
	candidate := candidates[0]
	if err := service.ReviewConcession(ctx, staff, candidate.ID, candidate.Revision, "request_information", 0, seed.Clubs[0], "Synthetic first information request"); err != nil {
		t.Fatal(err)
	}
	firstRequestMessage := messageID("concession_information", candidate.ID)
	candidates, err = service.ListConcessions(ctx, staff, junior.ConcessionFilter{CompetitionID: comp})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReviewConcession(ctx, staff, candidate.ID, candidates[0].Revision, "request_information", 0, seed.Clubs[0], "Synthetic revised information request"); err != nil {
		t.Fatal(err)
	}
	latestRequestMessage := messageID("concession_information", candidate.ID)
	blocked(firstRequestMessage, "superseded information request revision")
	corrected := []byte(fmt.Sprintf("fixture_id,outcome,evidence\n%d,played,Synthetic corrected source confirms play\n", fixtures[0].ID))
	if _, err := service.ImportFixtureResultsCSV(ctx, staff, comp, "Synthetic dispatch source", corrected); err != nil {
		t.Fatal(err)
	}
	blocked(latestRequestMessage, "information request after source correction")
	input.Title = "Synthetic closure reminder"
	secondForm, err := service.PublishForm(ctx, staff, input)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err = service.FormAssignments(ctx, staff, secondForm)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("closure reminder assignment: %+v %v", assignments, err)
	}
	if _, err := service.QueueFormReminders(ctx, staff, secondForm); err != nil {
		t.Fatal(err)
	}
	closureReminder := messageID("form_reminder", assignments[0].ID)
	if _, err := pool.Exec(ctx, `UPDATE junior_seasons SET state='closed' WHERE id=$1`, seed.SeasonID); err != nil {
		t.Fatal(err)
	}
	blocked(closureReminder, "closed season reminder")
	if sent != 1 {
		t.Fatalf("stale workflow reached sender; sends=%d", sent)
	}
}
