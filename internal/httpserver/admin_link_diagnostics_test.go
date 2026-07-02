package httpserver

import "testing"

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
