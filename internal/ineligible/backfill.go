package ineligible

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"
)

const (
	// TrackerSheetName is deliberately exact: accepting a renamed sheet would
	// make a workbook layout change look like an empty or valid import.
	TrackerSheetName    = "Form responses 1"
	trackerBackfillYear = 2026
	maxTrackerRows      = 10000
	maxTrackerCols      = 128
	maxTrackerXML       = 64 << 20
	maxSharedXML        = 32 << 20
)

var trackerHeaders = []string{
	"Timestamp",
	"Email address",
	"Name of defaulting player as shown on scorecard",
	"Reason you believe the player is ineligible",
	"Additional Info",
	"Your Club",
	"Your Name & Role at Club/League",
	"Your Preferred tel no",
	"Offending Club's Name",
	"Team in question",
	"Fixture Date",
	"Additional Evidence",
	"File Upload",
	"Score",
	"Initial Exec Comments (Please put Dates & Names)",
	"Investigation Required (Yes/No)?",
	"Responsible Officer?",
	"Email Sent Date",
	"Offending Club Response Received? (Yes/No)",
	"Offending Club Response Date?",
	"Offending Club Response Text",
	"Ready for Final Decision ",
	"POINTS deduction",
	"Cards",
	"Outcome Comms Shared with reporting and offending clubs?",
	"Case Closed? (Yes/No)",
}

// TrackerWorkbook is the immutable, validated representation of a supplied
// 2026 tracker. It contains no inferred sanctions or case-state changes.
type TrackerWorkbook struct {
	SheetName    string
	HeaderSHA256 string
	SourceSHA256 string
	Rows         []TrackerRow
}

// TrackerRow preserves the original Google-form columns and the manual O:Z
// tracker history separately. Parsed fields are used only for reconciliation.
type TrackerRow struct {
	SourceRowNumber      int
	RowSHA256            string
	FormData             map[string]string
	ManualHistory        map[string]string
	SubmittedAt          *time.Time
	FixtureDate          *time.Time
	PlayerText           string
	OffendingClubText    string
	TeamText             string
	TrackerStateHint     string
	PointsText           string
	CardsText            string
	RequiresEffectReview bool
	HasManualHistory     bool
	Errors               []string
}

// IntakeMatchCandidate is the narrow intake projection needed by the
// conservative tracker reconciliation algorithm.
type IntakeMatchCandidate struct {
	ID                int64
	ExternalCreatedAt *time.Time
	FixtureDate       *time.Time
	PlayerText        string
	OffendingClubText string
	TeamText          string
}

// TrackerMatch records a deterministic result. CandidateIDs are retained for
// human review even when no candidate passes every matching field.
type TrackerMatch struct {
	Status       string
	IntakeID     *int64
	CandidateIDs []int64
	Exception    string
}

// BackfillSummary is returned after an immutable run and its rows commit.
type BackfillSummary struct {
	RunID                 int64
	RowsTotal             int
	MatchedExact          int
	MatchedNormalized     int
	Unmatched             int
	Ambiguous             int
	Invalid               int
	WithManualHistory     int
	RequiringEffectReview int
	SourceSHA256          string
}

type trackerWorkbookXML struct {
	WorkbookPr struct {
		Date1904 string `xml:"date1904,attr"`
	} `xml:"workbookPr"`
	Sheets []struct {
		Name string `xml:"name,attr"`
		RID  string `xml:"id,attr"`
	} `xml:"sheets>sheet"`
}

type trackerRelationshipsXML struct {
	Relationships []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

type trackerRichText struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (r trackerRichText) value() string {
	var value strings.Builder
	value.WriteString(r.Text)
	for _, run := range r.Runs {
		value.WriteString(run.Text)
	}
	return value.String()
}

type trackerSheetRow struct {
	Number int                `xml:"r,attr"`
	Cells  []trackerSheetCell `xml:"c"`
}

type trackerSheetCell struct {
	Reference string          `xml:"r,attr"`
	Type      string          `xml:"t,attr"`
	Value     string          `xml:"v"`
	Inline    trackerRichText `xml:"is"`
}

type trackerGridRow struct {
	Number int
	Cells  map[int]trackerSheetCell
}

// ParseTrackerWorkbook reads XLSX ZIP/XML with strict sheet/header validation.
// It never writes to or mutates the supplied workbook.
func ParseTrackerWorkbook(data []byte, loc *time.Location) (TrackerWorkbook, error) {
	if len(data) == 0 {
		return TrackerWorkbook{}, fmt.Errorf("tracker workbook is empty")
	}
	if loc == nil {
		loc = time.UTC
	}
	sourceHash := sha256.Sum256(data)
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return TrackerWorkbook{}, fmt.Errorf("open tracker workbook: %w", err)
	}
	if len(reader.File) > 2048 {
		return TrackerWorkbook{}, fmt.Errorf("tracker workbook contains too many ZIP entries")
	}
	entries := make(map[string]*zip.File, len(reader.File))
	var totalUncompressed uint64
	for _, file := range reader.File {
		const maxExpandedWorkbook = uint64(256 << 20)
		if file.UncompressedSize64 > maxExpandedWorkbook || totalUncompressed > maxExpandedWorkbook-file.UncompressedSize64 {
			return TrackerWorkbook{}, fmt.Errorf("tracker workbook expands beyond the permitted size")
		}
		totalUncompressed += file.UncompressedSize64
		clean := path.Clean(strings.ReplaceAll(file.Name, "\\", "/"))
		if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return TrackerWorkbook{}, fmt.Errorf("tracker workbook contains an unsafe ZIP path")
		}
		entries[clean] = file
	}
	workbookFile := entries["xl/workbook.xml"]
	relsFile := entries["xl/_rels/workbook.xml.rels"]
	if workbookFile == nil || relsFile == nil {
		return TrackerWorkbook{}, fmt.Errorf("tracker workbook metadata is missing")
	}
	var workbookMeta trackerWorkbookXML
	if err = decodeTrackerXMLEntry(workbookFile, 4<<20, &workbookMeta); err != nil {
		return TrackerWorkbook{}, fmt.Errorf("read tracker workbook metadata: %w", err)
	}
	var relationships trackerRelationshipsXML
	if err = decodeTrackerXMLEntry(relsFile, 4<<20, &relationships); err != nil {
		return TrackerWorkbook{}, fmt.Errorf("read tracker workbook relationships: %w", err)
	}
	var relationshipID string
	for _, sheet := range workbookMeta.Sheets {
		if sheet.Name == TrackerSheetName {
			if relationshipID != "" {
				return TrackerWorkbook{}, fmt.Errorf("tracker workbook contains duplicate %q sheets", TrackerSheetName)
			}
			relationshipID = sheet.RID
		}
	}
	if relationshipID == "" {
		return TrackerWorkbook{}, fmt.Errorf("tracker workbook must contain the exact sheet %q", TrackerSheetName)
	}
	var worksheetTarget string
	for _, relationship := range relationships.Relationships {
		if relationship.ID == relationshipID {
			worksheetTarget = relationship.Target
			break
		}
	}
	worksheetPath, err := safeTrackerWorksheetPath(worksheetTarget)
	if err != nil {
		return TrackerWorkbook{}, err
	}
	worksheetFile := entries[worksheetPath]
	if worksheetFile == nil {
		return TrackerWorkbook{}, fmt.Errorf("tracker worksheet %q is missing", worksheetPath)
	}
	sharedStrings, err := readTrackerSharedStrings(entries["xl/sharedStrings.xml"])
	if err != nil {
		return TrackerWorkbook{}, err
	}
	grid, err := readTrackerGrid(worksheetFile)
	if err != nil {
		return TrackerWorkbook{}, err
	}
	if len(grid) == 0 {
		return TrackerWorkbook{}, fmt.Errorf("tracker worksheet is empty")
	}
	if err = validateTrackerHeaders(grid[0], sharedStrings); err != nil {
		return TrackerWorkbook{}, err
	}
	headerHash := sha256.Sum256([]byte(strings.Join(trackerHeaders, "\x1f")))
	date1904 := strings.EqualFold(strings.TrimSpace(workbookMeta.WorkbookPr.Date1904), "1") ||
		strings.EqualFold(strings.TrimSpace(workbookMeta.WorkbookPr.Date1904), "true")
	parsed := TrackerWorkbook{
		SheetName:    TrackerSheetName,
		HeaderSHA256: hex.EncodeToString(headerHash[:]),
		SourceSHA256: hex.EncodeToString(sourceHash[:]),
		Rows:         make([]TrackerRow, 0, len(grid)-1),
	}
	for _, source := range grid[1:] {
		values := make([]string, len(trackerHeaders))
		allBlank := true
		for column := range trackerHeaders {
			cell, ok := source.Cells[column]
			if !ok {
				continue
			}
			values[column] = trackerDisplayCell(cell, sharedStrings, column, date1904, loc)
			if strings.TrimSpace(values[column]) != "" {
				allBlank = false
			}
		}
		if allBlank {
			continue
		}
		parsed.Rows = append(parsed.Rows, parseTrackerRow(source.Number, values, source.Cells, sharedStrings, date1904, loc))
	}
	if len(parsed.Rows) == 0 {
		return TrackerWorkbook{}, fmt.Errorf("tracker worksheet contains no response rows")
	}
	return parsed, nil
}

func decodeTrackerXMLEntry(file *zip.File, maxSize uint64, target any) error {
	if file == nil {
		return fmt.Errorf("required workbook entry is missing")
	}
	if file.UncompressedSize64 > maxSize {
		return fmt.Errorf("workbook XML entry is too large")
	}
	stream, err := file.Open()
	if err != nil {
		return err
	}
	defer stream.Close()
	return xml.NewDecoder(io.LimitReader(stream, int64(maxSize)+1)).Decode(target)
}

func safeTrackerWorksheetPath(target string) (string, error) {
	target = strings.ReplaceAll(strings.TrimSpace(target), "\\", "/")
	if target == "" {
		return "", fmt.Errorf("tracker worksheet relationship is missing")
	}
	if strings.HasPrefix(target, "/") {
		target = strings.TrimPrefix(target, "/")
	} else {
		target = path.Join("xl", target)
	}
	clean := path.Clean(target)
	if clean == "." || strings.HasPrefix(clean, "../") || !strings.HasPrefix(clean, "xl/worksheets/") {
		return "", fmt.Errorf("tracker worksheet relationship is unsafe")
	}
	return clean, nil
}

func readTrackerSharedStrings(file *zip.File) ([]string, error) {
	if file == nil {
		return nil, nil
	}
	if file.UncompressedSize64 > maxSharedXML {
		return nil, fmt.Errorf("tracker shared strings are too large")
	}
	stream, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open tracker shared strings: %w", err)
	}
	defer stream.Close()
	var table struct {
		Items []trackerRichText `xml:"si"`
	}
	if err = xml.NewDecoder(io.LimitReader(stream, maxSharedXML+1)).Decode(&table); err != nil {
		return nil, fmt.Errorf("read tracker shared strings: %w", err)
	}
	values := make([]string, len(table.Items))
	for index, item := range table.Items {
		values[index] = item.value()
	}
	return values, nil
}

func readTrackerGrid(file *zip.File) ([]trackerGridRow, error) {
	if file.UncompressedSize64 > maxTrackerXML {
		return nil, fmt.Errorf("tracker worksheet is too large")
	}
	stream, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open tracker worksheet: %w", err)
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, maxTrackerXML+1))
	rows := make([]trackerGridRow, 0, 128)
	for {
		token, decodeErr := decoder.Token()
		if decodeErr == io.EOF {
			break
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("read tracker worksheet: %w", decodeErr)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		var row trackerSheetRow
		if err = decoder.DecodeElement(&row, &start); err != nil {
			return nil, fmt.Errorf("read tracker row: %w", err)
		}
		if row.Number <= 0 {
			row.Number = len(rows) + 1
		}
		capacity := len(row.Cells)
		if capacity > maxTrackerCols {
			capacity = maxTrackerCols
		}
		gridRow := trackerGridRow{Number: row.Number, Cells: make(map[int]trackerSheetCell, capacity)}
		for _, cell := range row.Cells {
			column := trackerColumnIndex(cell.Reference)
			if column < 0 {
				return nil, fmt.Errorf("tracker row %d has an invalid column reference", row.Number)
			}
			if column >= maxTrackerCols {
				// The supplied tracker formats all 16,384 Excel columns. Ignore
				// style-only empty cells outside the validated A:Z schema, but
				// fail closed if any such cell contains actual data.
				if strings.TrimSpace(cell.Value) != "" || strings.TrimSpace(cell.Inline.value()) != "" {
					return nil, fmt.Errorf("tracker row %d contains data beyond the supported schema", row.Number)
				}
				continue
			}
			gridRow.Cells[column] = cell
		}
		rows = append(rows, gridRow)
		if len(rows) > maxTrackerRows {
			return nil, fmt.Errorf("tracker worksheet has too many rows")
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Number < rows[j].Number })
	return rows, nil
}

func trackerColumnIndex(reference string) int {
	column := 0
	letters := 0
	for _, char := range reference {
		switch {
		case char >= 'A' && char <= 'Z':
			column = column*26 + int(char-'A'+1)
			letters++
		case char >= 'a' && char <= 'z':
			column = column*26 + int(char-'a'+1)
			letters++
		default:
			if letters == 0 {
				return -1
			}
			return column - 1
		}
	}
	if letters == 0 {
		return -1
	}
	return column - 1
}

func trackerCellText(cell trackerSheetCell, shared []string) string {
	switch cell.Type {
	case "inlineStr":
		return cell.Inline.value()
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(cell.Value))
		if err == nil && index >= 0 && index < len(shared) {
			return shared[index]
		}
		return ""
	default:
		return cell.Value
	}
}

func validateTrackerHeaders(header trackerGridRow, shared []string) error {
	for index, expected := range trackerHeaders {
		observed := trackerCellText(header.Cells[index], shared)
		if observed != expected {
			return fmt.Errorf("tracker header %d changed: expected %q, got %q", index+1, expected, observed)
		}
	}
	for index, cell := range header.Cells {
		if index >= len(trackerHeaders) && strings.TrimSpace(trackerCellText(cell, shared)) != "" {
			return fmt.Errorf("tracker contains an unexpected non-empty header in column %d", index+1)
		}
	}
	return nil
}

func trackerDisplayCell(cell trackerSheetCell, shared []string, column int, date1904 bool, loc *time.Location) string {
	value := trackerCellText(cell, shared)
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if cell.Type == "" || cell.Type == "n" {
		if number, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			switch column {
			case 0:
				return excelTrackerTime(number, date1904, loc).Format("02/01/2006 15:04:05")
			case 10, 17, 19:
				return excelTrackerTime(number, date1904, loc).Format("02/01/2006")
			}
		}
	}
	return value
}

func excelTrackerTime(serial float64, date1904 bool, loc *time.Location) time.Time {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, loc)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, loc)
	}
	days := math.Floor(serial)
	seconds := math.Round((serial - days) * 24 * 60 * 60)
	return base.AddDate(0, 0, int(days)).Add(time.Duration(seconds) * time.Second)
}

func parseTrackerRow(rowNumber int, values []string, cells map[int]trackerSheetCell, shared []string, date1904 bool, loc *time.Location) TrackerRow {
	row := TrackerRow{
		SourceRowNumber:   rowNumber,
		FormData:          make(map[string]string, 14),
		ManualHistory:     make(map[string]string, 12),
		PlayerText:        strings.TrimSpace(values[2]),
		OffendingClubText: strings.TrimSpace(values[8]),
		TeamText:          strings.TrimSpace(values[9]),
		PointsText:        strings.TrimSpace(values[22]),
		CardsText:         strings.TrimSpace(values[23]),
		TrackerStateHint:  trackerStateHint(values[25]),
	}
	for index, header := range trackerHeaders {
		if index < 14 {
			row.FormData[header] = values[index]
		} else {
			row.ManualHistory[header] = values[index]
			if strings.TrimSpace(values[index]) != "" {
				row.HasManualHistory = true
			}
		}
	}
	row.RequiresEffectReview = row.PointsText != "" || row.CardsText != ""
	if timestamp, err := parseTrackerTimeCell(cells[0], shared, date1904, loc, true); err != nil {
		row.Errors = append(row.Errors, "Timestamp: "+err.Error())
	} else if timestamp.Year() != trackerBackfillYear {
		row.Errors = append(row.Errors, fmt.Sprintf("Timestamp: year must be %d", trackerBackfillYear))
	} else {
		row.SubmittedAt = &timestamp
	}
	if fixture, err := parseTrackerTimeCell(cells[10], shared, date1904, loc, false); err != nil {
		row.Errors = append(row.Errors, "Fixture Date: "+err.Error())
	} else if fixture.Year() != trackerBackfillYear {
		row.Errors = append(row.Errors, fmt.Sprintf("Fixture Date: year must be %d", trackerBackfillYear))
	} else {
		fixture = time.Date(fixture.Year(), fixture.Month(), fixture.Day(), 0, 0, 0, 0, loc)
		row.FixtureDate = &fixture
	}
	for label, value := range map[string]string{
		"player":         row.PlayerText,
		"offending club": row.OffendingClubText,
		"team":           row.TeamText,
	} {
		if value == "" {
			row.Errors = append(row.Errors, label+" is blank")
		}
	}
	canonical := make([]string, len(trackerHeaders))
	copy(canonical, values)
	hash := sha256.Sum256([]byte(strings.Join(canonical, "\x1f")))
	row.RowSHA256 = hex.EncodeToString(hash[:])
	return row
}

func parseTrackerTimeCell(cell trackerSheetCell, shared []string, date1904 bool, loc *time.Location, timestamp bool) (time.Time, error) {
	value := strings.TrimSpace(trackerCellText(cell, shared))
	if value == "" {
		return time.Time{}, fmt.Errorf("is blank")
	}
	if cell.Type == "" || cell.Type == "n" {
		if serial, err := strconv.ParseFloat(value, 64); err == nil {
			if serial <= 0 || serial > 100000 {
				return time.Time{}, fmt.Errorf("contains an invalid Excel date")
			}
			return excelTrackerTime(serial, date1904, loc), nil
		}
	}
	formats := []string{"02/01/2006", "2/1/2006", "2006-01-02", time.RFC3339}
	if timestamp {
		formats = []string{"02/01/2006 15:04:05", "2/1/2006 15:04:05", "02/01/2006 15:04", "2/1/2006 15:04", "2006-01-02 15:04:05", time.RFC3339}
	}
	for _, format := range formats {
		var parsed time.Time
		var err error
		if format == time.RFC3339 {
			parsed, err = time.Parse(format, value)
		} else {
			parsed, err = time.ParseInLocation(format, value, loc)
		}
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("has an unsupported date value %q", value)
}

func trackerStateHint(value string) string {
	switch normalizeTrackerText(value) {
	case "yes", "y", "closed":
		return "closed"
	case "no", "n", "open":
		return "open"
	default:
		return "unknown"
	}
}

// ReconcileTrackerRow requires timestamp, player, offending club, team and
// fixture date to agree. Normalization is deliberately limited to Unicode,
// smart punctuation, case and whitespace; punctuation is otherwise retained.
func ReconcileTrackerRow(row TrackerRow, candidates []IntakeMatchCandidate) TrackerMatch {
	if len(row.Errors) > 0 || row.SubmittedAt == nil || row.FixtureDate == nil {
		return TrackerMatch{Status: "invalid", Exception: strings.Join(row.Errors, "; ")}
	}
	allCandidateIDs := make([]int64, 0, len(candidates))
	exact := make([]int64, 0, 1)
	normalized := make([]int64, 0, 1)
	for _, candidate := range candidates {
		allCandidateIDs = append(allCandidateIDs, candidate.ID)
		if candidate.ExternalCreatedAt == nil || candidate.FixtureDate == nil ||
			!sameTrackerSecond(*row.SubmittedAt, *candidate.ExternalCreatedAt) ||
			!sameTrackerDate(*row.FixtureDate, *candidate.FixtureDate) {
			continue
		}
		if exactTrackerText(row.PlayerText, candidate.PlayerText) &&
			exactTrackerText(row.OffendingClubText, candidate.OffendingClubText) &&
			exactTrackerText(row.TeamText, candidate.TeamText) {
			exact = append(exact, candidate.ID)
			continue
		}
		if normalizeTrackerText(row.PlayerText) == normalizeTrackerText(candidate.PlayerText) &&
			normalizeTrackerText(row.OffendingClubText) == normalizeTrackerText(candidate.OffendingClubText) &&
			normalizeTrackerText(row.TeamText) == normalizeTrackerText(candidate.TeamText) {
			normalized = append(normalized, candidate.ID)
		}
	}
	sort.Slice(allCandidateIDs, func(i, j int) bool { return allCandidateIDs[i] < allCandidateIDs[j] })
	sort.Slice(exact, func(i, j int) bool { return exact[i] < exact[j] })
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	if len(exact) == 1 {
		id := exact[0]
		return TrackerMatch{Status: "matched_exact", IntakeID: &id, CandidateIDs: exact}
	}
	if len(exact) > 1 {
		return TrackerMatch{Status: "ambiguous", CandidateIDs: exact, Exception: "more than one intake has the exact five-field identity"}
	}
	if len(normalized) == 1 {
		id := normalized[0]
		return TrackerMatch{Status: "matched_normalized", IntakeID: &id, CandidateIDs: normalized}
	}
	if len(normalized) > 1 {
		return TrackerMatch{Status: "ambiguous", CandidateIDs: normalized, Exception: "more than one intake has the normalized five-field identity"}
	}
	return TrackerMatch{Status: "unmatched", CandidateIDs: allCandidateIDs, Exception: "no staged Google-form intake matched timestamp, player, offending club, team and fixture date"}
}

func exactTrackerText(left, right string) bool {
	return strings.TrimSpace(left) == strings.TrimSpace(right)
}

func normalizeTrackerText(value string) string {
	value = norm.NFKC.String(value)
	value = strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", "\u201c", "\"", "\u201d", "\"",
		"\u2013", "-", "\u2014", "-", "\u00a0", " ",
	).Replace(value)
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func sameTrackerSecond(left, right time.Time) bool {
	return left.UTC().Truncate(time.Second).Equal(right.UTC().Truncate(time.Second))
}

func sameTrackerDate(left, right time.Time) bool {
	ly, lm, ld := left.Date()
	ry, rm, rd := right.Date()
	return ly == ry && lm == rm && ld == rd
}

// BackfillStorageDir isolates immutable source workbooks from ordinary
// sanctions imports while allowing deployment-specific persistent storage.
func BackfillStorageDir() string {
	if configured := strings.TrimSpace(os.Getenv("INELIGIBLE_BACKFILL_DIR")); configured != "" {
		return configured
	}
	return filepath.Join("data", "ineligible-backfills")
}

// StageTrackerWorkbook stores a content-addressed immutable source and appends
// a reconciliation run. Uploading identical bytes returns the existing run so
// a retry cannot displace the signed-off rollout prerequisite. It intentionally
// writes no case, effect, ledger, correspondence, outbox or mutable intake
// projection.
func StageTrackerWorkbook(ctx context.Context, pool *db.Pool, filename string, data []byte, adminID *int64, loc *time.Location) (BackfillSummary, error) {
	if pool == nil {
		return BackfillSummary{}, fmt.Errorf("database pool is nil")
	}
	workbook, err := ParseTrackerWorkbook(data, loc)
	if err != nil {
		return BackfillSummary{}, err
	}
	storageKey := workbook.SourceSHA256 + ".xlsx"
	if err = storeTrackerSource(BackfillStorageDir(), storageKey, data, workbook.SourceSHA256); err != nil {
		return BackfillSummary{}, err
	}
	// Acquire the advisory lock before opening the serializable transaction. If
	// the lock itself were the transaction's first statement, a waiter could
	// retain a pre-commit snapshot and miss the run that the lock holder added.
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		return BackfillSummary{}, err
	}
	if _, err = lockConn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('gmcl_ineligible_tracker_backfill'))`); err != nil {
		conn := lockConn.Hijack()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = conn.Close(closeCtx)
		closeCancel()
		return BackfillSummary{}, fmt.Errorf("lock tracker backfill: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var unlocked bool
		unlockErr := lockConn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtext('gmcl_ineligible_tracker_backfill'))`).Scan(&unlocked)
		cancel()
		if unlockErr != nil || !unlocked {
			conn := lockConn.Hijack()
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			_ = conn.Close(closeCtx)
			return
		}
		lockConn.Release()
	}()
	tx, err := lockConn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return BackfillSummary{}, err
	}
	defer tx.Rollback(ctx)
	existing := BackfillSummary{}
	err = tx.QueryRow(ctx, `
		SELECT run.id,run.rows_total,run.rows_matched_exact,run.rows_matched_normalized,
		       run.rows_unmatched,run.rows_ambiguous,run.rows_invalid,
		       run.rows_with_manual_history,run.rows_requiring_effect_review,
		       run.source_sha256
		FROM sanction_ineligible_backfill_runs run
		WHERE run.source_sha256=$1
		ORDER BY EXISTS(
			SELECT 1 FROM sanction_ineligible_backfill_applications application
			WHERE application.run_id=run.id
		) DESC,run.created_at,run.id
		LIMIT 1
	`, workbook.SourceSHA256).Scan(&existing.RunID, &existing.RowsTotal, &existing.MatchedExact,
		&existing.MatchedNormalized, &existing.Unmatched, &existing.Ambiguous,
		&existing.Invalid, &existing.WithManualHistory, &existing.RequiringEffectReview,
		&existing.SourceSHA256)
	if err == nil {
		if err = tx.Commit(ctx); err != nil {
			return BackfillSummary{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BackfillSummary{}, fmt.Errorf("inspect existing tracker reconciliation: %w", err)
	}
	type stagedRow struct {
		row   TrackerRow
		match TrackerMatch
	}
	staged := make([]stagedRow, 0, len(workbook.Rows))
	summary := BackfillSummary{RowsTotal: len(workbook.Rows), SourceSHA256: workbook.SourceSHA256}
	for _, row := range workbook.Rows {
		candidates, candidateErr := loadTrackerCandidates(ctx, tx, row)
		if candidateErr != nil {
			return BackfillSummary{}, candidateErr
		}
		match := ReconcileTrackerRow(row, candidates)
		switch match.Status {
		case "matched_exact":
			summary.MatchedExact++
		case "matched_normalized":
			summary.MatchedNormalized++
		case "unmatched":
			summary.Unmatched++
		case "ambiguous":
			summary.Ambiguous++
		case "invalid":
			summary.Invalid++
		default:
			return BackfillSummary{}, fmt.Errorf("unknown tracker match status %q", match.Status)
		}
		if row.HasManualHistory {
			summary.WithManualHistory++
		}
		if row.RequiresEffectReview {
			summary.RequiringEffectReview++
		}
		staged = append(staged, stagedRow{row: row, match: match})
	}
	cleanFilename := filepath.Base(strings.TrimSpace(filename))
	if cleanFilename == "." || cleanFilename == "" {
		cleanFilename = "ineligible-player-tracker.xlsx"
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO sanction_ineligible_backfill_runs(
			source_filename,storage_key,byte_size,source_sha256,source_sheet,header_sha256,
			rows_total,rows_matched_exact,rows_matched_normalized,rows_unmatched,
			rows_ambiguous,rows_invalid,rows_with_manual_history,
			rows_requiring_effect_review,uploaded_by_admin_id
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		RETURNING id
	`, cleanFilename, storageKey, len(data), workbook.SourceSHA256, workbook.SheetName, workbook.HeaderSHA256,
		summary.RowsTotal, summary.MatchedExact, summary.MatchedNormalized, summary.Unmatched,
		summary.Ambiguous, summary.Invalid, summary.WithManualHistory,
		summary.RequiringEffectReview, adminID).Scan(&summary.RunID)
	if err != nil {
		return BackfillSummary{}, fmt.Errorf("record tracker reconciliation run: %w", err)
	}
	for _, item := range staged {
		formJSON, marshalErr := json.Marshal(item.row.FormData)
		if marshalErr != nil {
			return BackfillSummary{}, marshalErr
		}
		manualJSON, marshalErr := json.Marshal(item.row.ManualHistory)
		if marshalErr != nil {
			return BackfillSummary{}, marshalErr
		}
		candidateJSON, marshalErr := json.Marshal(item.match.CandidateIDs)
		if marshalErr != nil {
			return BackfillSummary{}, marshalErr
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO sanction_ineligible_backfill_rows(
				run_id,source_row_number,row_sha256,form_data,manual_history,
				submitted_at,fixture_date,player_text,offending_club_text,team_text,
				match_status,matched_intake_id,candidate_intake_ids,exception_message,
				tracker_state_hint,points_text,cards_text,requires_effect_review,has_manual_history
			) VALUES($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,
			         NULLIF($14,''),$15,NULLIF($16,''),NULLIF($17,''),$18,$19)
		`, summary.RunID, item.row.SourceRowNumber, item.row.RowSHA256, string(formJSON), string(manualJSON),
			item.row.SubmittedAt, item.row.FixtureDate, nullTrackerText(item.row.PlayerText),
			nullTrackerText(item.row.OffendingClubText), nullTrackerText(item.row.TeamText),
			item.match.Status, item.match.IntakeID, string(candidateJSON), item.match.Exception,
			item.row.TrackerStateHint, item.row.PointsText, item.row.CardsText,
			item.row.RequiresEffectReview, item.row.HasManualHistory); err != nil {
			return BackfillSummary{}, fmt.Errorf("record tracker row %d: %w", item.row.SourceRowNumber, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return BackfillSummary{}, err
	}
	return summary, nil
}

func loadTrackerCandidates(ctx context.Context, tx pgx.Tx, row TrackerRow) ([]IntakeMatchCandidate, error) {
	if len(row.Errors) > 0 || row.SubmittedAt == nil || row.FixtureDate == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id,external_created_at,fixture_date,COALESCE(player_text,''),
		       COALESCE(offending_club_text,''),COALESCE(team_text,'')
		FROM sanction_intakes
		WHERE origin='google_form'
		  AND fixture_date=$2::date
		  AND external_created_at >= $1::timestamptz - interval '1 second'
		  AND external_created_at <  $1::timestamptz + interval '1 second'
		ORDER BY id
	`, *row.SubmittedAt, *row.FixtureDate)
	if err != nil {
		return nil, fmt.Errorf("find staged intake for tracker row %d: %w", row.SourceRowNumber, err)
	}
	defer rows.Close()
	candidates := make([]IntakeMatchCandidate, 0, 2)
	for rows.Next() {
		var candidate IntakeMatchCandidate
		if err = rows.Scan(&candidate.ID, &candidate.ExternalCreatedAt, &candidate.FixtureDate,
			&candidate.PlayerText, &candidate.OffendingClubText, &candidate.TeamText); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func storeTrackerSource(directory, storageKey string, data []byte, expectedSHA string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create tracker backfill storage: %w", err)
	}
	filename := filepath.Join(directory, storageKey)
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(filename)
			return fmt.Errorf("store tracker source: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close tracker source: %w", closeErr)
		}
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("store tracker source: %w", err)
	}
	existing, readErr := os.ReadFile(filename)
	if readErr != nil {
		return fmt.Errorf("verify existing tracker source: %w", readErr)
	}
	hash := sha256.Sum256(existing)
	if hex.EncodeToString(hash[:]) != expectedSHA || !bytes.Equal(existing, data) {
		return fmt.Errorf("stored tracker source checksum collision for %s", storageKey)
	}
	return nil
}

func nullTrackerText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
