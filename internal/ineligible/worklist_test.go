package ineligible

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
	"time"
)

func TestWorklistCandidateSHA256IsDeterministic(t *testing.T) {
	candidates := []WorklistCandidate{
		{IntakeID: 42, RevisionID: 5, ManifestRowID: 91, Selectable: true},
		{IntakeID: 7, RevisionID: 2, ManifestRowID: 11, Selectable: false},
	}
	original := append([]WorklistCandidate(nil), candidates...)

	got := worklistCandidateSHA256(candidates)
	want := testWorklistSHA256("7:2:11:false\n42:5:91:true\n")
	if got != want {
		t.Fatalf("candidate fingerprint = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(candidates, original) {
		t.Fatalf("candidate fingerprint mutated input: got %#v, want %#v", candidates, original)
	}

	reordered := []WorklistCandidate{candidates[1], candidates[0]}
	if reorderedHash := worklistCandidateSHA256(reordered); reorderedHash != got {
		t.Fatalf("reordered candidate fingerprint = %q, want %q", reorderedHash, got)
	}

	changed := append([]WorklistCandidate(nil), candidates...)
	changed[0].RevisionID++
	if changedHash := worklistCandidateSHA256(changed); changedHash == got {
		t.Fatal("candidate fingerprint did not change when revision provenance changed")
	}

	changed = append([]WorklistCandidate(nil), candidates...)
	changed[0].ManifestRowID++
	if changedHash := worklistCandidateSHA256(changed); changedHash == got {
		t.Fatal("candidate fingerprint did not change when manifest provenance changed")
	}

	changed = append([]WorklistCandidate(nil), candidates...)
	changed[0].Selectable = false
	if changedHash := worklistCandidateSHA256(changed); changedHash == got {
		t.Fatal("candidate fingerprint did not change when selectability changed")
	}
}

func TestWorklistSelectionSHA256IsDeterministic(t *testing.T) {
	ids := []int64{42, 7, 19}
	original := append([]int64(nil), ids...)

	got := worklistSelectionSHA256(ids)
	want := testWorklistSHA256("7\n19\n42\n")
	if got != want {
		t.Fatalf("selection fingerprint = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(ids, original) {
		t.Fatalf("selection fingerprint mutated input: got %v, want %v", ids, original)
	}

	if reordered := worklistSelectionSHA256([]int64{19, 42, 7}); reordered != got {
		t.Fatalf("reordered selection fingerprint = %q, want %q", reordered, got)
	}
	if changed := worklistSelectionSHA256([]int64{7, 19, 43}); changed == got {
		t.Fatal("selection fingerprint did not change when selected membership changed")
	}
	if empty := worklistSelectionSHA256(nil); empty != testWorklistSHA256("") {
		t.Fatalf("empty selection fingerprint = %q, want SHA-256 of empty input", empty)
	}
}

func TestWorklistRunReady(t *testing.T) {
	completed := time.Date(2026, time.August, 11, 20, 0, 0, 0, time.UTC)
	readyRun := func() WorklistRun {
		return WorklistRun{
			Status:        "succeeded",
			RowsSeen:      2,
			ManifestCount: 2,
			CompletedAt:   &completed,
			Candidates:    []WorklistCandidate{{IntakeID: 1, Selectable: true}},
		}
	}

	tests := []struct {
		name   string
		mutate func(*WorklistRun)
		want   bool
	}{
		{name: "succeeded", want: true},
		{name: "partial with resolved validation exception", mutate: func(run *WorklistRun) {
			run.Status = "partial"
			run.RowsErrored = 1
		}, want: true},
		{name: "unresolved manifest row", mutate: func(run *WorklistRun) {
			run.Status = "partial"
			run.RowsErrored = 1
			run.UnresolvedRows = []WorklistUnresolvedRow{{SourceRowNumber: 3, Error: "identity unresolved"}}
		}},
		{name: "not completed", mutate: func(run *WorklistRun) { run.CompletedAt = nil }},
		{name: "running", mutate: func(run *WorklistRun) { run.Status = "running" }},
		{name: "failed", mutate: func(run *WorklistRun) { run.Status = "failed" }},
		{name: "manifest short", mutate: func(run *WorklistRun) { run.ManifestCount-- }},
		{name: "manifest exceeds rows seen", mutate: func(run *WorklistRun) { run.ManifestCount++ }},
		{name: "no candidates", mutate: func(run *WorklistRun) { run.Candidates = nil }},
		{name: "maximum candidates", mutate: func(run *WorklistRun) {
			run.Candidates = make([]WorklistCandidate, MaxWorklistCandidates)
			run.Candidates[0].Selectable = true
		}, want: true},
		{name: "no selectable candidates", mutate: func(run *WorklistRun) { run.Candidates[0].Selectable = false }},
		{name: "too many candidates", mutate: func(run *WorklistRun) {
			run.Candidates = make([]WorklistCandidate, MaxWorklistCandidates+1)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := readyRun()
			if test.mutate != nil {
				test.mutate(&run)
			}
			if got := run.Ready(); got != test.want {
				t.Fatalf("Ready() = %v, want %v", got, test.want)
			}
		})
	}
}

func testWorklistSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
