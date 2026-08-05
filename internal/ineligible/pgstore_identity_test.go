package ineligible

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestResolveGoogleSourceAnchorFailsClosed(t *testing.T) {
	tests := []struct {
		name          string
		anchors       []googleSourceRowAnchor
		externalMatch int64
		incomingKey   string
		wantTargets   []int64
		wantConflict  string
	}{
		{name: "new row", incomingKey: "new"},
		{name: "same anchored identity", anchors: []googleSourceRowAnchor{{IntakeID: 11, ExternalKey: "same"}}, externalMatch: 11, incomingKey: "same", wantTargets: []int64{11}},
		{name: "identity fields edited", anchors: []googleSourceRowAnchor{{IntakeID: 11, ExternalKey: "old"}}, incomingKey: "new", wantTargets: []int64{11}, wantConflict: "identity_changed"},
		{name: "rows reordered", anchors: []googleSourceRowAnchor{{IntakeID: 11, ExternalKey: "old"}}, externalMatch: 22, incomingKey: "other", wantTargets: []int64{11}, wantConflict: "source_row_reordered"},
		{name: "known identity moved", externalMatch: 22, incomingKey: "known", wantTargets: []int64{22}, wantConflict: "identity_moved_rows"},
		{name: "same row already ambiguous", anchors: []googleSourceRowAnchor{{IntakeID: 11, ExternalKey: "a"}, {IntakeID: 22, ExternalKey: "b"}}, externalMatch: 22, incomingKey: "b", wantTargets: []int64{11, 22}, wantConflict: "ambiguous_source_row"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveGoogleSourceAnchor(test.anchors, test.externalMatch, test.incomingKey)
			if got.ConflictKind != test.wantConflict || !reflect.DeepEqual(got.TargetIntakeIDs, test.wantTargets) {
				t.Fatalf("resolution=%+v, want targets=%v conflict=%q", got, test.wantTargets, test.wantConflict)
			}
		})
	}
}

func TestGoogleIdentityConflictManualResolutionIsRetryIdempotent(t *testing.T) {
	row := IntakeRow{RawSHA256: strings.Repeat("a", 64), State: "new"}
	resolved := googleIntakeCurrent{
		LatestSHA: row.RawSHA256, LatestIdentityConflict: true,
		State: "linked", ExceptionMessage: "",
	}
	if !googleIdentityConflictAlreadyResolved(resolved, row) {
		t.Fatal("manually resolved unchanged conflict should remain resolved on retry")
	}
	resolved.State = "exception"
	if googleIdentityConflictAlreadyResolved(resolved, row) {
		t.Fatal("unresolved exception was treated as accepted")
	}
	resolved.State = "linked"
	row.State = "exception"
	if googleIdentityConflictAlreadyResolved(resolved, row) {
		t.Fatal("a newly malformed source row was hidden by an earlier resolution")
	}
}

func TestPGStoreIdentityConflictRetainsRevisionAndInvalidatesLinkedCases(t *testing.T) {
	raw, err := os.ReadFile("pgstore.go")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Join(strings.Fields(string(raw)), " ")
	for _, required := range []string{
		"revision.source_row_number=$3",
		"resolveGoogleSourceAnchor(anchors, externalMatchID, row.ExternalKey)",
		"INSERT INTO sanction_intake_revisions(",
		"'_gmcl_identity_anchor_exception'",
		"'identity_change',true",
		"invalidateLinkedCaseResponseWindows(ctx, tx, current.ID, message, nextRevision)",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("PGStore identity anchor flow is missing %q", required)
		}
	}
}
