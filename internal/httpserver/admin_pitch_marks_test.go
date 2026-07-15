package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const pitchTestHeader = "Timestamp\tHome Club Full Formal Name\tIf Home Club Not Listed, enter club name here\tWhich Division was your game in?\tDate of Game\tAway Club Full Formal Name\tIf Away Club Not Listed, enter club name here\tUnevenness of bounce\tSeam movement\tCarry and / or bounce\tTurn\n"

func TestParseUmpirePitchFileTSV(t *testing.T) {
	data := pitchTestHeader + "19/04/2026 12:02:00\tClifton CC\t\tGMCL Saturday Division 1\t18/04/2026\tDarcy Lever CC\t\t4\t3\t5\t2\n"
	rows, err := parseUmpirePitchFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.HomeClub != "Clifton CC" || row.AwayClub != "Darcy Lever CC" {
		t.Fatalf("clubs=%q/%q", row.HomeClub, row.AwayClub)
	}
	if row.MatchDate.Format("2006-01-02") != "2026-04-18" {
		t.Fatalf("date=%s", row.MatchDate)
	}
	if row.Marks != (pitchVector{4, 3, 5, 2}) || len(row.Errors) != 0 || row.Hash == "" {
		t.Fatalf("row=%+v", row)
	}
}

func TestParseUmpirePitchFileUsesFallbackClubAndDetectsComma(t *testing.T) {
	parts := strings.Split(strings.TrimSuffix(pitchTestHeader, "\n"), "\t")
	for i := range parts {
		parts[i] = `"` + strings.ReplaceAll(parts[i], `"`, `""`) + `"`
	}
	header := strings.Join(parts, ",") + "\n"
	data := header + "19/04/2026 12:02:00,Other,New Home CC,GMCL Saturday Division 1,18/04/2026,Not Listed,New Away CC,4,4,4,4\n"
	rows, err := parseUmpirePitchFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].HomeClub != "New Home CC" || rows[0].AwayClub != "New Away CC" {
		t.Fatalf("fallback clubs=%q/%q", rows[0].HomeClub, rows[0].AwayClub)
	}
}

func TestParseUmpirePitchFileValidation(t *testing.T) {
	data := pitchTestHeader + "bad\tClifton CC\t\tGMCL Saturday Division 1\t18/04/2026\tDarcy Lever CC\t\t0\t6\tx\t4\n"
	rows, err := parseUmpirePitchFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[0].Errors) != 3 {
		t.Fatalf("errors=%v", rows[0].Errors)
	}
	if !rows[0].Timestamp.IsZero() {
		t.Fatalf("unrecognized timestamp should be stored as null: %v", rows[0].Timestamp)
	}
}

func TestParsePitchTimestampFormats(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		loc = time.UTC
	}
	for _, value := range []string{
		"18/04/2026 15:04:58",
		"18/4/2026 15:04:58",
		"18/04/2026 15:04",
		"2026-04-18 15:04:58",
		"18/04/2026 3:04:58 PM",
		"2026-04-18T14:04:58Z",
	} {
		if _, err := parsePitchTimestamp(value, loc); err != nil {
			t.Errorf("format %q: %v", value, err)
		}
	}
}

func TestUnknownTimestampDoesNotInvalidatePitchRow(t *testing.T) {
	data := pitchTestHeader + "Excel timestamp\tClifton CC\t\tGMCL Saturday Division 1\t18/04/2026\tDarcy Lever CC\t\t4\t4\t4\t4\n"
	rows, err := parseUmpirePitchFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows[0].Errors) != 0 || !rows[0].Timestamp.IsZero() || rows[0].Hash == "" {
		t.Fatalf("row=%+v", rows[0])
	}
}

func TestMatchPitchRowExactSuggestedAmbiguousAndUnmatched(t *testing.T) {
	date := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	base := umpirePitchParsedRow{MatchDate: date, Division: "GMCL Saturday Championship", HomeClub: "Elton CC", AwayClub: "Swindon Moorside CC"}
	candidates := []pitchCandidate{{MatchID: 1, MatchDate: date, Competition: base.Division, HomeClub: "Elton CC", AwayClub: "Swinton Moorside CC"}}
	status, got, selected := matchPitchRow(base, candidates)
	if status != "suggested" || len(got) != 1 || selected != 1 {
		t.Fatalf("suggested=%s/%v/%d", status, got, selected)
	}
	base.AwayClub = "Swinton Moorside CC"
	status, _, selected = matchPitchRow(base, candidates)
	if status != "exact" || selected != 1 {
		t.Fatalf("exact=%s/%d", status, selected)
	}
	candidates = append(candidates, pitchCandidate{MatchID: 2, MatchDate: date, Competition: base.Division, HomeClub: "Elton CC", AwayClub: "Swinton Moorside CC"})
	status, got, selected = matchPitchRow(base, candidates)
	if status != "ambiguous" || len(got) != 2 || selected != 0 {
		t.Fatalf("ambiguous=%s/%v/%d", status, got, selected)
	}
	base.HomeClub = "Completely Different CC"
	status, _, _ = matchPitchRow(base, candidates)
	if status != "unmatched" {
		t.Fatalf("unmatched=%s", status)
	}
}

func TestCaptainPitchVectorConversionAndOutcome(t *testing.T) {
	v, ok := captainPitchVector([]byte(`{"match_outcome":"played","unevenness_of_bounce":1,"seam_movement":"2","carry_bounce":4,"turn":6}`))
	if !ok || v != (pitchVector{5, 4, 2, 1}) {
		t.Fatalf("vector=%+v ok=%v", v, ok)
	}
	if _, ok := captainPitchVector([]byte(`{"match_outcome":"cancelled","unevenness_of_bounce":1,"seam_movement":1,"carry_bounce":1,"turn":1}`)); ok {
		t.Fatal("cancelled match should not produce marks")
	}
}

func TestWeightedPitchVectorRebalancesMissingSources(t *testing.T) {
	sources := map[string]pitchVector{
		"away":   {4, 3, 2, 1},
		"umpire": {2, 3, 4, 5},
	}
	got, effective, missing, ok := weightedPitchVector(sources, pitchWeights{Home: 10, Away: 40, Umpire: 50})
	if !ok || len(missing) != 1 || missing[0] != "home" {
		t.Fatalf("ok=%v missing=%v", ok, missing)
	}
	if !closeFloat(effective["away"], 44.444444) || !closeFloat(effective["umpire"], 55.555556) {
		t.Fatalf("effective=%v", effective)
	}
	if !closeFloat(got.Uneven, 2.888889) || !closeFloat(got.Turn, 3.222222) {
		t.Fatalf("weighted=%+v", got)
	}
}

func TestCombinedCaptainDefaultRatio(t *testing.T) {
	got, effective, _, ok := weightedPitchVector(map[string]pitchVector{
		"home": {5, 5, 5, 5}, "away": {3, 3, 3, 3},
	}, pitchWeights{Home: 10, Away: 40})
	if !ok || !closeFloat(got.overall(), 3.4) || effective["home"] != 20 || effective["away"] != 80 {
		t.Fatalf("got=%+v effective=%v", got, effective)
	}
}

func TestParsePitchWeights(t *testing.T) {
	r := httptest.NewRequest("GET", "/?home_weight=20&away_weight=30&umpire_weight=50", nil)
	w, err := parsePitchWeights(r)
	if err != nil || w != (pitchWeights{20, 30, 50}) {
		t.Fatalf("weights=%+v err=%v", w, err)
	}
	r = httptest.NewRequest("GET", "/?home_weight=20&away_weight=30&umpire_weight=40", nil)
	if _, err := parsePitchWeights(r); err == nil {
		t.Fatal("expected total validation error")
	}
}

func TestAverageUmpireReportsAtFixtureLevel(t *testing.T) {
	fixtureOne := pitchSourceValue{Vector: pitchVector{4, 4, 4, 4}, Reports: 2}
	fixtureTwo := pitchSourceValue{Vector: pitchVector{2, 2, 2, 2}, Reports: 1}
	got, ok, fixtures, reports := averagePitchSource([]pitchSourceValue{fixtureOne, fixtureTwo})
	if !ok || fixtures != 2 || reports != 3 || got.overall() != 3 {
		t.Fatalf("got=%+v ok=%v fixtures=%d reports=%d", got, ok, fixtures, reports)
	}
}

func TestSafeCSVCellPreventsFormulaExecution(t *testing.T) {
	if got := safeCSVCell("=HYPERLINK(\"bad\")"); !strings.HasPrefix(got, "'") {
		t.Fatalf("got=%q", got)
	}
}

func closeFloat(a, b float64) bool { return mathAbs(a-b) < 0.00001 }

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
