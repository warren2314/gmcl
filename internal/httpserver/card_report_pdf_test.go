package httpserver

import (
	"strings"
	"testing"
	"time"
)

func TestCardDueForOffence(t *testing.T) {
	tests := []struct {
		name          string
		offenceNumber int64
		priorRed      int64
		wantCard      string
		wantPoints    int
	}{
		{name: "first offence is yellow", offenceNumber: 1, priorRed: 0, wantCard: "yellow", wantPoints: 0},
		{name: "third offence is first red", offenceNumber: 3, priorRed: 0, wantCard: "red", wantPoints: 1},
		{name: "sixth offence is second red", offenceNumber: 6, priorRed: 1, wantCard: "red", wantPoints: 2},
		{name: "seventh offence returns to yellow", offenceNumber: 7, priorRed: 2, wantCard: "yellow", wantPoints: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCard, gotPoints := cardDueForOffence(tt.offenceNumber, tt.priorRed)
			if gotCard != tt.wantCard || gotPoints != tt.wantPoints {
				t.Fatalf("card due: got %s/%d want %s/%d", gotCard, gotPoints, tt.wantCard, tt.wantPoints)
			}
		})
	}
}

func TestBuildWeeklyCardReportPDFContainsReportDetails(t *testing.T) {
	week := cardReportWeek{
		ID:         12,
		SeasonID:   2,
		SeasonName: "2026",
		Number:     8,
		StartDate:  time.Date(2026, time.June, 13, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2026, time.June, 19, 0, 0, 0, 0, time.UTC),
	}
	sanctionID := int64(99)
	issuedAt := time.Date(2026, time.June, 18, 9, 30, 0, 0, time.UTC)
	rows := []weeklyCardReportRow{
		{
			TeamID:              1,
			ClubName:            "Example CC",
			TeamName:            "1st XI",
			FixtureCount:        1,
			SubmittedCount:      0,
			MissingCount:        1,
			PriorYellowCount:    2,
			PriorRedCount:       0,
			PriorOffenceCount:   2,
			OffenceNumber:       3,
			CardDue:             "red",
			PointsDeduction:     1,
			ExistingSanctionID:  &sanctionID,
			ExistingCard:        "red",
			ExistingReason:      "non_submission",
			ExistingStatus:      "active",
			ExistingEmailStatus: "pending",
			ExistingIssuedAt:    &issuedAt,
			MissingFixtures: []weeklyCardReportFixture{
				{
					MatchDate:          time.Date(2026, time.June, 13, 0, 0, 0, 0, time.UTC),
					Opposition:         "Opposition CC 1st XI",
					PlayCricketMatchID: 12345,
				},
			},
		},
	}

	pdf := buildWeeklyCardReportPDF(week, rows, issuedAt)
	if !strings.HasPrefix(string(pdf), "%PDF-1.4") {
		t.Fatalf("expected PDF header, got %q", string(pdf[:8]))
	}
	body := string(pdf)
	for _, want := range []string{
		"GMCL Weekly Card Report",
		"Example CC",
		"Red card - 1",
		"point deduction",
		"Opposition CC 1st XI",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PDF missing %q", want)
		}
	}
}
