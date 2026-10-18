package junior

import (
	"strings"
	"testing"
)

func TestJuniorResultCSVRequiresExplicitFixtureAndOutcomeEvidence(t *testing.T) {
	valid := "\ufefffixture_id,outcome,evidence\n1,conceded_home,Official result explicitly names home as conceding\n2,unknown,\n3,abandoned,Rain\n"
	rows, err := ParseFixtureResultsCSV([]byte(valid))
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%v error=%v", rows, err)
	}
	if !concessionOutcome(rows[0].Outcome) || concessionOutcome(rows[1].Outcome) || concessionOutcome(rows[2].Outcome) {
		t.Fatal("non-concession became candidate")
	}
	for _, body := range []string{"fixture_id,outcome,evidence\n1,conceded_home,\n", "fixture_id,outcome,evidence\n1,win,Home won\n", "fixture_id,outcome,evidence\n1,played,\n1,played,\n", "fixture_id,outcome,evidence\n0,played,\n", "fixture_id,outcome,evidence\n1,played,\x00\n", "wrong,outcome,evidence\n1,played,\n"} {
		if _, err := ParseFixtureResultsCSV([]byte(body)); err == nil {
			t.Fatalf("invalid CSV accepted: %q", body)
		}
	}
	if _, err := ParseFixtureResultsCSV([]byte(strings.Repeat("x", (4<<20)+1))); err == nil {
		t.Fatal("oversized CSV accepted")
	}
}
func TestJuniorResultEvidenceFingerprintDoesNotDependOnOtherRows(t *testing.T) {
	row := FixtureResultInput{FixtureID: 1, Outcome: "possible_concession", Evidence: "Needs review"}
	first := resultFingerprint("Source A", row)
	if first != resultFingerprint("Source A", row) {
		t.Fatal("unchanged evidence was not idempotent")
	}
	row.Evidence = "Corrected evidence"
	if first == resultFingerprint("Source A", row) {
		t.Fatal("corrected evidence fingerprint unchanged")
	}
	if resultFingerprint("Source A", row) == resultFingerprint("Source B", row) {
		t.Fatal("source authority was lost")
	}
}
