package httpserver

import (
	"errors"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/junior"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestJuniorFormClosedSeasonAndConcurrentResponsesPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	t.Setenv("SUPER_ADMIN_EMAILS", "")
	adminID, clubID, _ := seedJuniorAccessAdminClub(t, ctx, pool)
	staff := junior.Principal{AdminID: adminID}
	service := junior.NewService(pool)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	season := junior.SeasonInput{Name: "Synthetic form lifecycle", StartDate: today.AddDate(0, -1, 0), EndDate: today.AddDate(0, 12, 0), State: "open"}
	var err error
	season.ID, err = service.SaveSeason(ctx, staff, season)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetParticipation(ctx, staff, clubID, season.ID, "active", "synthetic fixture"); err != nil {
		t.Fatal(err)
	}
	grant := seedJuniorAccess(t, pool, clubID, adminID, "form-concurrency@example.invalid")
	input := junior.FormInput{SeasonID: season.ID, Title: "Synthetic simultaneous response", DueDate: today.AddDate(0, 1, 0), RequiresReview: true, ClubIDs: []int32{clubID}, Fields: []junior.FormField{{Key: "answer", Label: "Answer", Type: "text", Required: true}}}
	formID, err := service.PublishForm(ctx, staff, input)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := service.FormAssignments(ctx, staff, formID)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("assignments=%d err=%v", len(assignments), err)
	}
	assignmentID := assignments[0].ID
	cfg := pool.Config().Copy()
	cfg.MaxConns = 4
	concurrentPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrentPool.Close)
	concurrent := junior.NewService(&db.Pool{Pool: concurrentPool})
	gate := make(chan struct{})
	results := make(chan error, 2)
	for _, answer := range []string{"First competing draft", "Second competing draft"} {
		go func(value string) {
			<-gate
			_, err := concurrent.SaveFormResponse(ctx, grant.Principal, assignmentID, 0, map[string]string{"answer": value}, false)
			results <- err
		}(answer)
	}
	close(gate)
	saved, conflicted := 0, 0
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				saved++
			} else if errors.Is(err, junior.ErrConflict) {
				conflicted++
			} else {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent drafts did not complete")
		}
	}
	if saved != 1 || conflicted != 1 {
		t.Fatalf("optimistic draft locking: saved=%d conflicted=%d", saved, conflicted)
	}
	assignment, err := service.FormAssignment(ctx, grant.Principal, assignmentID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.DraftRevision != 1 {
		t.Fatalf("draft revision=%d", assignment.DraftRevision)
	}
	submissionID, err := service.SaveFormResponse(ctx, grant.Principal, assignmentID, 1, assignment.Values, true)
	if err != nil {
		t.Fatal(err)
	}
	season.State = "closed"
	if _, err := service.SaveSeason(ctx, staff, season); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveFormResponse(ctx, grant.Principal, assignmentID, 2, map[string]string{"answer": "Should not change closed season"}, false); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("closed draft mutation: %v", err)
	}
	if err := service.ReviewForm(ctx, staff, assignmentID, submissionID, "complete", "Review after closure"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("closed staff review: %v", err)
	}
	if _, err := service.PublishForm(ctx, staff, input); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("closed form publication: %v", err)
	}
	if _, err := service.QueueFormReminders(ctx, staff, formID); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("closed form reminder queued: %v", err)
	}
	replay, err := service.SaveFormResponse(ctx, grant.Principal, assignmentID, 1, assignment.Values, true)
	if err != nil || replay != submissionID {
		t.Fatalf("completed identical retry changed history: %d %v", replay, err)
	}
	var submissionCount, reviewCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM junior_form_submissions WHERE assignment_id=$1),(SELECT count(*) FROM junior_form_reviews WHERE assignment_id=$1)`, assignmentID).Scan(&submissionCount, &reviewCount); err != nil || submissionCount != 1 || reviewCount != 0 {
		t.Fatalf("closed mutation history: submissions=%d reviews=%d err=%v", submissionCount, reviewCount, err)
	}
	season.State = "open"
	if _, err := service.SaveSeason(ctx, staff, season); err != nil {
		t.Fatal(err)
	}
	if err := service.ReviewForm(ctx, staff, assignmentID, submissionID, "complete", "Verified synthetic answer"); err != nil {
		t.Fatal(err)
	}
	season.State = "closed"
	if _, err := service.SaveSeason(ctx, staff, season); err != nil {
		t.Fatal(err)
	}
	if err := service.ReviewForm(ctx, staff, assignmentID, submissionID, "complete", "Verified synthetic answer"); err != nil {
		t.Fatalf("idempotent closed review retry: %v", err)
	}
	if err := service.ReviewForm(ctx, staff, assignmentID, submissionID, "return", "Should not reopen a closed season"); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("closed changes requested: %v", err)
	}
	if err := junior.ValidateFormValues(input.Fields, map[string]string{"answer": strings.Repeat(" ", 5000) + "x"}, true); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("oversized whitespace response accepted: %v", err)
	}
	if err := junior.ValidateFormValues(input.Fields, map[string]string{"answer": "invalid\x00value"}, true); !errors.Is(err, junior.ErrValidation) {
		t.Fatalf("invalid PostgreSQL JSON character accepted: %v", err)
	}
}
