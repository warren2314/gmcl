package httpserver

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"strings"
	"testing"
	"time"
)

func TestLinkDiagFixtureStatus(t *testing.T) {
	tests := []struct {
		name      string
		row       linkDiagFixtureEvidence
		wantLabel string
	}{
		{
			name:      "missing",
			row:       linkDiagFixtureEvidence{},
			wantLabel: "Missing",
		},
		{
			name:      "submitted",
			row:       linkDiagFixtureEvidence{HasSubmission: true},
			wantLabel: "Submitted",
		},
		{
			name:      "legacy submission",
			row:       linkDiagFixtureEvidence{HasSubmission: true, LegacyCovered: true},
			wantLabel: "Submitted (legacy)",
		},
		{
			name:      "admin resolved",
			row:       linkDiagFixtureEvidence{ExemptionReason: "umpire report only"},
			wantLabel: "Admin resolved",
		},
		{
			name:      "bye",
			row:       linkDiagFixtureEvidence{IsBye: true},
			wantLabel: "Bye",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := linkDiagFixtureStatus(tt.row)
			if got != tt.wantLabel {
				t.Fatalf("linkDiagFixtureStatus() label = %q, want %q", got, tt.wantLabel)
			}
		})
	}
}

func TestLinkDiagFixtureCounts(t *testing.T) {
	rows := []linkDiagFixtureEvidence{
		{},
		{HasSubmission: true},
		{ExemptionReason: "resolved"},
		{IsBye: true},
	}

	missing, submitted, resolved, byes := linkDiagFixtureCounts(rows)
	if missing != 1 || submitted != 1 || resolved != 1 || byes != 1 {
		t.Fatalf("counts = missing %d submitted %d resolved %d byes %d, want 1 each",
			missing, submitted, resolved, byes)
	}
}

func TestLinkDiagExportButtons(t *testing.T) {
	got := linkDiagExportButtons("Radcliffe CC")
	for _, want := range []string{
		`/admin/link-diagnostics/export.csv?q=Radcliffe+CC`,
		`/admin/link-diagnostics/export.pdf?q=Radcliffe+CC`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("export buttons missing %q in %s", want, got)
		}
	}
}

func TestLinkDiagExportFilename(t *testing.T) {
	got := linkDiagExportFilename("pdf", "Radcliffe CC", time.Date(2026, 7, 2, 10, 11, 12, 0, time.UTC))
	want := "gmcl-link-diagnostics-radcliffe-cc-20260702-101112.pdf"
	if got != want {
		t.Fatalf("linkDiagExportFilename() = %q, want %q", got, want)
	}
}

func TestWriteLinkDiagnosticsCSVIncludesEvidenceSections(t *testing.T) {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	writeLinkDiagnosticsCSV(cw, sampleLinkDiagData(), time.Date(2026, 7, 2, 10, 11, 12, 0, time.UTC))
	cw.Flush()
	if err := cw.Error(); err != nil {
		t.Fatalf("CSV writer error: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		"Evidence Summary",
		"Card / Sanction Records",
		"Fixture Report Evidence",
		"Radcliffe CC",
		"Yellow Card",
		"Missing",
		"Match date",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("CSV missing %q:\n%s", want, body)
		}
	}
}

func TestBuildLinkDiagnosticsPDFContainsEvidencePackDetails(t *testing.T) {
	pdf := buildLinkDiagnosticsPDF(sampleLinkDiagData(), time.Date(2026, 7, 2, 10, 11, 12, 0, time.UTC))
	if !strings.HasPrefix(string(pdf), "%PDF-1.4") {
		t.Fatalf("expected PDF header, got %q", string(pdf[:8]))
	}
	body := string(pdf)
	for _, want := range []string{
		"GMCL Link Diagnostics Evidence Pack",
		"Evidence Summary",
		"Card / Sanction Records",
		"Fixture Report Evidence",
		"Radcliffe CC",
		"Yellow Card",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PDF missing %q", want)
		}
	}
}

func sampleLinkDiagData() linkDiagPageData {
	matchDate := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	return linkDiagPageData{
		Query: "Radcliffe CC",
		Captains: []linkDiagCaptain{
			{
				ID:             1,
				TeamID:         10,
				FullName:       "Example Captain",
				Email:          "captain@example.test",
				EffectiveEmail: "captain@example.test",
				ClubName:       "Radcliffe CC",
				TeamName:       "1st XI",
				ActiveFrom:     "2026-04-01",
				IsActive:       true,
			},
		},
		Sanctions: []linkDiagSanction{
			{
				ID:          99,
				TeamID:      10,
				ClubName:    "Radcliffe CC",
				TeamName:    "1st XI",
				SeasonName:  "2026",
				WeekNumber:  9,
				MatchDate:   matchDate,
				Colour:      "yellow",
				Reason:      "non_submission",
				Status:      "active",
				EmailStatus: "sent",
				IssuedAt:    time.Date(2026, 6, 24, 23, 59, 0, 0, time.UTC),
				IssuedBy:    "system",
				EmailSentAt: sql.NullTime{Time: time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC), Valid: true},
			},
		},
		Fixtures: []linkDiagFixtureEvidence{
			{
				TeamID:             10,
				WeekNumber:         9,
				MatchDate:          matchDate,
				ClubName:           "Radcliffe CC",
				TeamName:           "1st XI",
				PlayCricketMatchID: 123456,
				Opponent:           "Opposition CC 1st XI",
				Ground:             "Radcliffe Ground",
				ReminderCount:      2,
				ReminderTypes:      "game_day, wednesday",
				SanctionStatus:     "YELLOW / active / email sent",
			},
		},
		Reminders: []linkDiagReminderSend{
			{
				SentAt:       time.Date(2026, 6, 20, 21, 0, 0, 0, time.UTC),
				MatchDate:    matchDate,
				ReminderType: "game_day",
				Recipient:    "captain@example.test",
				ClubName:     "Radcliffe CC",
				TeamName:     "1st XI",
				TokenID:      sql.NullInt64{Int64: 44, Valid: true},
			},
		},
		Submits: []linkDiagSubmission{},
		Events: []linkDiagEmailEvent{
			{
				CreatedAt: time.Date(2026, 6, 20, 21, 2, 0, 0, time.UTC),
				EventType: "click",
				Recipient: "captain@example.test",
				Subject:   "GMCL Captain's Report",
			},
		},
		AuditRows: []linkDiagAudit{
			{
				CreatedAt: time.Date(2026, 6, 20, 21, 3, 0, 0, time.UTC),
				Action:    "magic_link_clicked",
				Metadata:  `{"team_id":10}`,
			},
		},
	}
}
