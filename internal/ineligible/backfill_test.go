package ineligible

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseSuppliedTrackerWhenConfigured(t *testing.T) {
	filename := os.Getenv("INELIGIBLE_TRACKER_TEST_FILE")
	if filename == "" {
		t.Skip("INELIGIBLE_TRACKER_TEST_FILE is not configured")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	workbook, err := ParseTrackerWorkbook(data, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if workbook.SheetName != TrackerSheetName || len(workbook.Rows) != 37 {
		t.Fatalf("supplied tracker parsed sheet=%q rows=%d, want %q and 37", workbook.SheetName, len(workbook.Rows), TrackerSheetName)
	}
}

func trackerColumnName(index int) string {
	index++
	name := ""
	for index > 0 {
		index--
		name = string(rune('A'+index%26)) + name
		index /= 26
	}
	return name
}

func makeTrackerWorkbook(t *testing.T, sheetName string, headers []string, rows [][]string, numeric map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	write := func(name, content string) {
		t.Helper()
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	write("xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="`+sheetName+`" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	write("xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`)
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	allRows := append([][]string{headers}, rows...)
	for rowIndex, row := range allRows {
		number := strconv.Itoa(rowIndex + 1)
		sheet.WriteString(`<row r="` + number + `">`)
		for columnIndex, value := range row {
			reference := trackerColumnName(columnIndex) + number
			if raw, ok := numeric[reference]; ok {
				sheet.WriteString(`<c r="` + reference + `"><v>` + raw + `</v></c>`)
				continue
			}
			sheet.WriteString(`<c r="` + reference + `" t="inlineStr"><is><t>`)
			if err := xml.EscapeText(&sheet, []byte(value)); err != nil {
				t.Fatal(err)
			}
			sheet.WriteString(`</t></is></c>`)
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	write("xl/worksheets/sheet1.xml", sheet.String())
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func trackerExcelSerial(value time.Time) string {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, value.Location())
	return fmt.Sprintf("%.12f", value.Sub(base).Hours()/24)
}

func TestParseTrackerWorkbookPreservesManualHistory(t *testing.T) {
	loc := time.UTC
	submitted := time.Date(2026, 4, 21, 18, 20, 7, 0, loc)
	fixture := time.Date(2026, 4, 18, 0, 0, 0, 0, loc)
	row := make([]string, len(trackerHeaders))
	row[1] = "reporter@example.test"
	row[2] = "Connor Bliss"
	row[3] = "Not registered"
	row[5] = "Reporting CC"
	row[8] = "Stretford CC"
	row[9] = "2nd XI"
	row[14] = "22/04/26: Hussan Javaid"
	row[20] = "The club response, preserved verbatim."
	row[22] = "Six league points were discussed."
	row[23] = "One red card was discussed."
	row[25] = "Yes"
	data := makeTrackerWorkbook(t, TrackerSheetName, trackerHeaders, [][]string{row}, map[string]string{
		"A2": trackerExcelSerial(submitted),
		"K2": trackerExcelSerial(fixture),
	})

	parsed, err := ParseTrackerWorkbook(data, loc)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SheetName != TrackerSheetName || len(parsed.Rows) != 1 || parsed.SourceSHA256 == "" || parsed.HeaderSHA256 == "" {
		t.Fatalf("parsed=%+v", parsed)
	}
	got := parsed.Rows[0]
	if got.SubmittedAt == nil || !got.SubmittedAt.Equal(submitted) {
		t.Fatalf("submitted=%v want %v", got.SubmittedAt, submitted)
	}
	if got.FixtureDate == nil || !sameTrackerDate(*got.FixtureDate, fixture) {
		t.Fatalf("fixture=%v", got.FixtureDate)
	}
	if got.FormData[trackerHeaders[2]] != "Connor Bliss" || got.ManualHistory[trackerHeaders[20]] != "The club response, preserved verbatim." {
		t.Fatalf("row=%+v", got)
	}
	if !got.HasManualHistory || !got.RequiresEffectReview || got.TrackerStateHint != "closed" || len(got.Errors) != 0 {
		t.Fatalf("row flags=%+v", got)
	}
}

func TestParseTrackerWorkbookFailsClosedOnSheetOrHeaderDrift(t *testing.T) {
	row := make([]string, len(trackerHeaders))
	row[0] = "21/04/2026 18:20:07"
	row[2] = "Player"
	row[8] = "Club"
	row[9] = "2nd XI"
	row[10] = "18/04/2026"
	if _, err := ParseTrackerWorkbook(makeTrackerWorkbook(t, "Renamed", trackerHeaders, [][]string{row}, nil), time.UTC); err == nil || !strings.Contains(err.Error(), TrackerSheetName) {
		t.Fatalf("expected exact sheet error, got %v", err)
	}
	drifted := append([]string(nil), trackerHeaders...)
	drifted[22] = "League points"
	if _, err := ParseTrackerWorkbook(makeTrackerWorkbook(t, TrackerSheetName, drifted, [][]string{row}, nil), time.UTC); err == nil || !strings.Contains(err.Error(), "header 23 changed") {
		t.Fatalf("expected header drift error, got %v", err)
	}
}

func TestReconcileTrackerRowRequiresFiveFieldIdentity(t *testing.T) {
	submitted := time.Date(2026, 4, 21, 18, 20, 7, 0, time.UTC)
	fixture := time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)
	row := TrackerRow{SubmittedAt: &submitted, FixtureDate: &fixture, PlayerText: "Connor Bliss", OffendingClubText: "Stretford CC", TeamText: "2nd XI"}
	exactCandidate := IntakeMatchCandidate{ID: 41, ExternalCreatedAt: &submitted, FixtureDate: &fixture, PlayerText: row.PlayerText, OffendingClubText: row.OffendingClubText, TeamText: row.TeamText}
	match := ReconcileTrackerRow(row, []IntakeMatchCandidate{exactCandidate})
	if match.Status != "matched_exact" || match.IntakeID == nil || *match.IntakeID != 41 {
		t.Fatalf("exact=%+v", match)
	}

	normalizedCandidate := exactCandidate
	normalizedCandidate.ID = 42
	normalizedCandidate.PlayerText = "  CONNOR   bliss "
	normalizedCandidate.OffendingClubText = "stretford cc"
	normalizedCandidate.TeamText = "2ND XI"
	match = ReconcileTrackerRow(row, []IntakeMatchCandidate{normalizedCandidate})
	if match.Status != "matched_normalized" || match.IntakeID == nil || *match.IntakeID != 42 {
		t.Fatalf("normalized=%+v", match)
	}

	wrongTeam := normalizedCandidate
	wrongTeam.ID = 43
	wrongTeam.TeamText = "3rd XI"
	match = ReconcileTrackerRow(row, []IntakeMatchCandidate{wrongTeam})
	if match.Status != "unmatched" || match.IntakeID != nil {
		t.Fatalf("wrong team=%+v", match)
	}

	duplicate := exactCandidate
	duplicate.ID = 44
	match = ReconcileTrackerRow(row, []IntakeMatchCandidate{exactCandidate, duplicate})
	if match.Status != "ambiguous" || match.IntakeID != nil || len(match.CandidateIDs) != 2 {
		t.Fatalf("ambiguous=%+v", match)
	}
}

func TestBackfillReviewAndSignoffGates(t *testing.T) {
	base := BackfillReviewInput{
		Disposition:          "accept_match",
		ReviewedIntakeID:     41,
		ReviewedCaseState:    "closed",
		EffectsReviewStatus:  "pending_manual_interpretation",
		EffectInterpretation: "",
		ReviewReason:         "Matched against the five source identity fields.",
		ReviewerName:         "Hussan Javaid",
	}
	if err := ValidateBackfillReview(true, base); err != nil {
		t.Fatalf("pending review should be recordable: %v", err)
	}
	base.EffectsReviewStatus = "manually_interpreted"
	if err := ValidateBackfillReview(true, base); err == nil {
		t.Fatal("manual interpretation must include a note")
	}
	base.EffectInterpretation = "Six league points and one card require separate structured decisions; nothing imported."
	if err := ValidateBackfillReview(true, base); err != nil {
		t.Fatal(err)
	}
	if err := (BackfillSignoffReadiness{RowsTotal: 37, RowsReviewed: 37, RowsNeedingEffectReview: 1}).Validate(); err == nil {
		t.Fatal("sign-off must remain blocked while an effect interpretation is pending")
	}
	if err := (BackfillSignoffReadiness{RowsTotal: 37, RowsReviewed: 37}).Validate(); err != nil {
		t.Fatalf("ready sign-off rejected: %v", err)
	}
}
