package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const pitchPreviewCookie = "pitch_csv_preview"

const (
	pitchDefaultFrom = "2026-04-18"
	pitchDefaultTo   = "2026-06-27"
)

var pitchCompetitionNames = []string{
	"GMCL Saturday Premier",
	"GMCL Saturday Premier 2",
	"GMCL Saturday Championship",
	"GMCL Saturday Division 1",
}

var pitchCompetitionDisplayNames = map[string]string{
	"GMCL Saturday Premier":      "Premier 1",
	"GMCL Saturday Premier 2":    "Premier 2",
	"GMCL Saturday Championship": "Championship",
	"GMCL Saturday Division 1":   "Division 1",
}

var playCricketPitchCompetitionNames = map[string]string{
	"robert hinchliffe premier league": "GMCL Saturday Premier",
	"gmcl premier league 2":            "GMCL Saturday Premier 2",
	"gmcl championship":                "GMCL Saturday Championship",
	"gmcl division 1":                  "GMCL Saturday Division 1",
	"gmcl saturday premier":            "GMCL Saturday Premier",
	"gmcl saturday premier 2":          "GMCL Saturday Premier 2",
	"gmcl saturday championship":       "GMCL Saturday Championship",
	"gmcl saturday division 1":         "GMCL Saturday Division 1",
}

type pitchVector struct {
	Uneven float64
	Seam   float64
	Carry  float64
	Turn   float64
}

func (v pitchVector) add(o pitchVector) pitchVector {
	return pitchVector{v.Uneven + o.Uneven, v.Seam + o.Seam, v.Carry + o.Carry, v.Turn + o.Turn}
}

func (v pitchVector) div(n float64) pitchVector {
	if n == 0 {
		return pitchVector{}
	}
	return pitchVector{v.Uneven / n, v.Seam / n, v.Carry / n, v.Turn / n}
}

func (v pitchVector) overall() float64 { return (v.Uneven + v.Seam + v.Carry + v.Turn) / 4 }

type umpirePitchParsedRow struct {
	Index      int               `json:"index"`
	SourceKind string            `json:"source_kind"`
	Timestamp  time.Time         `json:"timestamp"`
	MatchDate  time.Time         `json:"match_date"`
	Division   string            `json:"division"`
	HomeClub   string            `json:"home_club"`
	AwayClub   string            `json:"away_club"`
	Ground     string            `json:"ground"`
	Marks      pitchVector       `json:"marks"`
	Hash       string            `json:"hash"`
	Raw        map[string]string `json:"raw"`
	Errors     []string          `json:"errors,omitempty"`
	Status     string            `json:"status"`
	Candidates []pitchCandidate  `json:"candidates,omitempty"`
	SelectedID int64             `json:"selected_id,omitempty"`
}

type pitchCandidate struct {
	MatchID     int64     `json:"match_id"`
	MatchDate   time.Time `json:"match_date"`
	Competition string    `json:"competition"`
	HomeClub    string    `json:"home_club"`
	AwayClub    string    `json:"away_club"`
}

func (c pitchCandidate) label() string {
	return fmt.Sprintf("%s — %s v %s (%s)", c.MatchDate.Format("2 Jan 2006"), c.HomeClub, c.AwayClub, c.Competition)
}

type pitchImportPreview struct {
	Filename string                 `json:"filename"`
	Checksum string                 `json:"checksum"`
	Rows     []umpirePitchParsedRow `json:"rows"`
}

func parseUmpirePitchFile(data []byte) ([]umpirePitchParsedRow, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("file is empty")
	}
	firstLine := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		firstLine = data[:i]
	}
	delimiter := ','
	if bytes.Count(firstLine, []byte{'\t'}) > bytes.Count(firstLine, []byte{','}) {
		delimiter = '\t'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delimiter
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse delimited file: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("file has no data rows")
	}
	header := make(map[string]int, len(records[0]))
	for i, h := range records[0] {
		header[normalizePitchHeader(h)] = i
	}
	required := []string{
		"timestamp", "which division was your game in?", "date of game",
		"home club full formal name", "away club full formal name",
		"unevenness of bounce", "seam movement", "carry and / or bounce", "turn",
	}
	for _, h := range required {
		if _, ok := header[normalizePitchHeader(h)]; !ok {
			return nil, fmt.Errorf("required column missing: %s", h)
		}
	}

	field := func(record []string, name string) string {
		i, ok := header[normalizePitchHeader(name)]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	loc, _ := time.LoadLocation("Europe/London")
	if loc == nil {
		loc = time.UTC
	}
	rows := make([]umpirePitchParsedRow, 0, len(records)-1)
	for i, record := range records[1:] {
		if len(record) == 0 || strings.TrimSpace(strings.Join(record, "")) == "" {
			continue
		}
		row := umpirePitchParsedRow{Index: i + 1, SourceKind: "panel_form", Raw: map[string]string{}, Status: "invalid"}
		for name, idx := range header {
			if idx < len(record) {
				row.Raw[name] = strings.TrimSpace(record[idx])
			}
		}
		row.Division = field(record, "Which Division was your game in?")
		row.HomeClub = pitchClubField(field(record, "Home Club Full Formal Name"), field(record, "If Home Club Not Listed, enter club name here"))
		row.AwayClub = pitchClubField(field(record, "Away Club Full Formal Name"), field(record, "If Away Club Not Listed, enter club name here"))
		row.MatchDate, err = time.ParseInLocation("02/01/2006", field(record, "Date of Game"), loc)
		if err != nil {
			row.Errors = append(row.Errors, "invalid date of game")
		}
		rawTimestamp := field(record, "Timestamp")
		row.Timestamp, _ = parsePitchTimestamp(rawTimestamp, loc)
		if row.HomeClub == "" || row.AwayClub == "" || row.Division == "" {
			row.Errors = append(row.Errors, "home club, away club and division are required")
		}
		mark := func(name string) float64 {
			v, parseErr := strconv.Atoi(field(record, name))
			if parseErr != nil || v < 1 || v > 5 {
				row.Errors = append(row.Errors, name+" must be an integer from 1 to 5")
				return 0
			}
			return float64(v)
		}
		row.Marks = pitchVector{
			Uneven: mark("Unevenness of bounce"),
			Seam:   mark("Seam movement"),
			Carry:  mark("Carry and / or bounce"),
			Turn:   mark("Turn"),
		}
		canonical := fmt.Sprintf("%s|%s|%s|%s|%s|%.0f|%.0f|%.0f|%.0f",
			strings.TrimSpace(rawTimestamp), row.MatchDate.Format("2006-01-02"),
			normalizeCaptainCSVClubKey(row.HomeClub), normalizeCaptainCSVClubKey(row.AwayClub), strings.ToLower(row.Division),
			row.Marks.Uneven, row.Marks.Seam, row.Marks.Carry, row.Marks.Turn)
		h := sha256.Sum256([]byte(canonical))
		row.Hash = hex.EncodeToString(h[:])
		rows = append(rows, row)
	}
	return rows, nil
}

func parseUmpirePitchUpload(filename string, data []byte) ([]umpirePitchParsedRow, error) {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if extension == ".xlsx" || (len(data) >= 4 && bytes.Equal(data[:4], []byte{'P', 'K', 3, 4})) {
		return parsePlayCricketUmpireGroundXLSX(data)
	}
	return parseUmpirePitchFile(data)
}

type pitchXLSXInlineString struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (s pitchXLSXInlineString) value() string {
	var b strings.Builder
	b.WriteString(s.Text)
	for _, run := range s.Runs {
		b.WriteString(run.Text)
	}
	return b.String()
}

type pitchXLSXCell struct {
	Reference string                `xml:"r,attr"`
	Type      string                `xml:"t,attr"`
	Value     string                `xml:"v"`
	Inline    pitchXLSXInlineString `xml:"is"`
}

type pitchXLSXRow struct {
	Cells []pitchXLSXCell `xml:"c"`
}

func parsePlayCricketUmpireGroundXLSX(data []byte) ([]umpirePitchParsedRow, error) {
	records, err := readPitchXLSXRows(data)
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("workbook has no data rows")
	}
	header := make(map[string]int, len(records[0]))
	for i, value := range records[0] {
		header[normalizePitchHeader(value)] = i
	}
	required := []string{"match date", "home team", "away team", "division / cup", "ground", "question", "response"}
	for _, name := range required {
		if _, ok := header[name]; !ok {
			return nil, fmt.Errorf("required workbook column missing: %s", name)
		}
	}
	field := func(record []string, name string) string {
		index, ok := header[name]
		if !ok || index < 0 || index >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[index])
	}
	type pitchGroup struct {
		date, home, away, homeTeam, awayTeam, division, ground, responsible string
		marks                                                               map[string]string
		errors                                                              []string
	}
	groups := map[string]*pitchGroup{}
	var groupOrder []string
	questionNames := map[string]string{
		"unevenness of bounce":  "unevenness",
		"seam movement":         "seam",
		"carry and / or bounce": "carry",
		"turn":                  "turn",
	}
	loc, _ := time.LoadLocation("Europe/London")
	if loc == nil {
		loc = time.UTC
	}
	filterFrom, _ := time.ParseInLocation("2006-01-02", pitchDefaultFrom, loc)
	filterTo, _ := time.ParseInLocation("2006-01-02", pitchDefaultTo, loc)
	for _, record := range records[1:] {
		division, ok := normalizePlayCricketPitchCompetition(field(record, "division / cup"))
		if !ok {
			continue
		}
		date := field(record, "match date")
		matchDate, dateErr := time.ParseInLocation("02/01/2006", date, loc)
		if dateErr == nil && (matchDate.Before(filterFrom) || matchDate.After(filterTo) || matchDate.Weekday() != time.Saturday) {
			continue
		}
		homeTeam := field(record, "home team")
		awayTeam := field(record, "away team")
		home := pitchClubFromTeamName(homeTeam)
		away := pitchClubFromTeamName(awayTeam)
		ground := field(record, "ground")
		responsible := field(record, "responsible club")
		key := strings.Join([]string{date, home, away, division}, "|")
		group := groups[key]
		if group == nil {
			group = &pitchGroup{
				date: date, home: home, away: away, homeTeam: homeTeam, awayTeam: awayTeam,
				division: division, ground: ground, responsible: responsible, marks: map[string]string{},
			}
			groups[key] = group
			groupOrder = append(groupOrder, key)
		} else if ground != "" && group.ground != "" && normalizeCaptainCSVClubKey(ground) != normalizeCaptainCSVClubKey(group.ground) {
			group.errors = append(group.errors, "inconsistent ground name")
		} else if group.ground == "" {
			group.ground = ground
		}
		markName, isPitchQuestion := questionNames[normalizePitchHeader(field(record, "question"))]
		if !isPitchQuestion {
			continue
		}
		if _, exists := group.marks[markName]; exists {
			group.errors = append(group.errors, "duplicate "+markName+" response")
		}
		group.marks[markName] = field(record, "response")
	}
	parsed := make([]umpirePitchParsedRow, 0, len(groupOrder))
	for _, key := range groupOrder {
		group := groups[key]
		row := umpirePitchParsedRow{
			Index:      len(parsed) + 1,
			SourceKind: "play_cricket_ground",
			Division:   group.division,
			HomeClub:   group.home,
			AwayClub:   group.away,
			Ground:     group.ground,
			Raw: map[string]string{
				"source_format":    "play_cricket_ground_xlsx",
				"match_date":       group.date,
				"home_team":        group.homeTeam,
				"away_team":        group.awayTeam,
				"home_club":        group.home,
				"away_club":        group.away,
				"division":         group.division,
				"ground":           group.ground,
				"responsible_club": group.responsible,
			},
			Status: "invalid",
			Errors: append([]string(nil), group.errors...),
		}
		row.MatchDate, err = time.ParseInLocation("02/01/2006", group.date, loc)
		if err != nil {
			row.Errors = append(row.Errors, "invalid match date")
		}
		if row.HomeClub == "" || row.AwayClub == "" {
			row.Errors = append(row.Errors, "home team and away team are required")
		}
		mark := func(key, label string) float64 {
			raw := strings.TrimSpace(group.marks[key])
			row.Raw[key] = raw
			value, parseErr := strconv.Atoi(raw)
			if parseErr != nil || value < 1 || value > 5 {
				row.Errors = append(row.Errors, label+" must be an integer from 1 to 5")
				return 0
			}
			return float64(value)
		}
		row.Marks = pitchVector{
			Uneven: mark("unevenness", "Unevenness of bounce"),
			Seam:   mark("seam", "Seam movement"),
			Carry:  mark("carry", "Carry and / or bounce"),
			Turn:   mark("turn", "Turn"),
		}
		canonical := fmt.Sprintf("play_cricket_ground|%s|%s|%s|%s|%.0f|%.0f|%.0f|%.0f",
			row.MatchDate.Format("2006-01-02"), normalizeCaptainCSVClubKey(row.HomeClub), normalizeCaptainCSVClubKey(row.AwayClub),
			strings.ToLower(row.Division), row.Marks.Uneven, row.Marks.Seam, row.Marks.Carry, row.Marks.Turn)
		hash := sha256.Sum256([]byte(canonical))
		row.Hash = hex.EncodeToString(hash[:])
		parsed = append(parsed, row)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("workbook contains no supported Premier 1, Premier 2, Championship or Division 1 ground reports")
	}
	return parsed, nil
}

// syntheticPitchMatchID provides a stable negative fixture key for workbook
// matches that are not present in the Play-Cricket fixture cache. Real
// Play-Cricket match IDs are positive.
func syntheticPitchMatchID(row umpirePitchParsedRow) int64 {
	canonical := strings.Join([]string{
		row.MatchDate.Format("2006-01-02"),
		normalizeCaptainCSVClubKey(row.HomeClub),
		normalizeCaptainCSVClubKey(row.AwayClub),
		strings.ToLower(strings.TrimSpace(row.Division)),
	}, "|")
	hash := sha256.Sum256([]byte(canonical))
	value := binary.BigEndian.Uint64(hash[:8]) & ((uint64(1) << 63) - 1)
	if value == 0 {
		value = 1
	}
	return -int64(value)
}

func normalizePlayCricketPitchCompetition(value string) (string, bool) {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
	competition, ok := playCricketPitchCompetitionNames[normalized]
	return competition, ok
}

func pitchClubFromTeamName(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	for _, suffix := range []string{
		" - 1st xi", " \u2013 1st xi", " \u2014 1st xi",
		" - first xi", " \u2013 first xi", " \u2014 first xi",
	} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimSpace(value[:len(value)-len(suffix)])
		}
	}
	return value
}

func readPitchXLSXRows(data []byte) ([][]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("workbook is empty")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open workbook: %w", err)
	}
	var worksheets []*zip.File
	var sharedStringsEntry *zip.File
	for _, file := range reader.File {
		switch {
		case strings.HasPrefix(file.Name, "xl/worksheets/") && strings.HasSuffix(file.Name, ".xml"):
			worksheets = append(worksheets, file)
		case file.Name == "xl/sharedStrings.xml":
			sharedStringsEntry = file
		}
	}
	if len(worksheets) == 0 {
		return nil, fmt.Errorf("workbook contains no worksheets")
	}
	sort.Slice(worksheets, func(i, j int) bool { return worksheets[i].Name < worksheets[j].Name })
	sharedStrings, err := readPitchXLSXSharedStrings(sharedStringsEntry)
	if err != nil {
		return nil, err
	}
	const maxWorksheetXML = 64 << 20
	if worksheets[0].UncompressedSize64 > maxWorksheetXML {
		return nil, fmt.Errorf("worksheet is too large")
	}
	stream, err := worksheets[0].Open()
	if err != nil {
		return nil, fmt.Errorf("open worksheet: %w", err)
	}
	defer stream.Close()
	decoder := xml.NewDecoder(stream)
	rows := make([][]string, 0, 8192)
	for {
		token, decodeErr := decoder.Token()
		if decodeErr == io.EOF {
			break
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("read worksheet: %w", decodeErr)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		var sourceRow pitchXLSXRow
		if err := decoder.DecodeElement(&sourceRow, &start); err != nil {
			return nil, fmt.Errorf("read worksheet row: %w", err)
		}
		maxColumn := -1
		for _, cell := range sourceRow.Cells {
			if column := pitchXLSXColumnIndex(cell.Reference); column > maxColumn {
				maxColumn = column
			}
		}
		if maxColumn < 0 {
			continue
		}
		if maxColumn > 1024 {
			return nil, fmt.Errorf("worksheet has too many columns")
		}
		record := make([]string, maxColumn+1)
		for _, cell := range sourceRow.Cells {
			column := pitchXLSXColumnIndex(cell.Reference)
			if column < 0 || column >= len(record) {
				continue
			}
			record[column] = pitchXLSXCellValue(cell, sharedStrings)
		}
		rows = append(rows, record)
		if len(rows) > 100000 {
			return nil, fmt.Errorf("worksheet has too many rows")
		}
	}
	return rows, nil
}

func readPitchXLSXSharedStrings(file *zip.File) ([]string, error) {
	if file == nil {
		return nil, nil
	}
	const maxSharedStringsXML = 32 << 20
	if file.UncompressedSize64 > maxSharedStringsXML {
		return nil, fmt.Errorf("shared strings are too large")
	}
	stream, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open shared strings: %w", err)
	}
	defer stream.Close()
	var table struct {
		Items []pitchXLSXInlineString `xml:"si"`
	}
	if err := xml.NewDecoder(stream).Decode(&table); err != nil {
		return nil, fmt.Errorf("read shared strings: %w", err)
	}
	values := make([]string, len(table.Items))
	for i, item := range table.Items {
		values[i] = item.value()
	}
	return values, nil
}

func pitchXLSXCellValue(cell pitchXLSXCell, sharedStrings []string) string {
	switch cell.Type {
	case "inlineStr":
		return cell.Inline.value()
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(cell.Value))
		if err == nil && index >= 0 && index < len(sharedStrings) {
			return sharedStrings[index]
		}
		return ""
	default:
		return strings.TrimSpace(cell.Value)
	}
}

func pitchXLSXColumnIndex(reference string) int {
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

func parsePitchTimestamp(value string, loc *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{
		"02/01/2006 15:04:05",
		"2/1/2006 15:04:05",
		"02/01/2006 15:04",
		"2/1/2006 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"02/01/2006 3:04:05 PM",
		"2/1/2006 3:04:05 PM",
	} {
		if parsed, err := time.ParseInLocation(layout, value, loc); err == nil {
			return parsed, nil
		}
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp")
}

func normalizePitchHeader(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(s)), "\ufeff")
	return strings.Join(strings.Fields(s), " ")
}

func pitchClubField(formal, fallback string) string {
	formal = strings.TrimSpace(formal)
	lower := strings.ToLower(formal)
	if formal == "" || lower == "other" || lower == "not listed" || lower == "n/a" || lower == "na" || lower == "none" {
		return strings.TrimSpace(fallback)
	}
	return formal
}

func matchPitchRow(row umpirePitchParsedRow, candidates []pitchCandidate) (string, []pitchCandidate, int64) {
	var sameDivision []pitchCandidate
	for _, c := range candidates {
		if c.MatchDate.Format("2006-01-02") == row.MatchDate.Format("2006-01-02") && (c.Competition == "" || strings.EqualFold(strings.TrimSpace(c.Competition), strings.TrimSpace(row.Division))) {
			sameDivision = append(sameDivision, c)
		}
	}
	var exact []pitchCandidate
	homeKey, awayKey := normalizeCaptainCSVClubKey(row.HomeClub), normalizeCaptainCSVClubKey(row.AwayClub)
	for _, c := range sameDivision {
		if normalizeCaptainCSVClubKey(c.HomeClub) == homeKey && normalizeCaptainCSVClubKey(c.AwayClub) == awayKey {
			exact = append(exact, c)
		}
	}
	if len(exact) == 1 {
		return "exact", exact, exact[0].MatchID
	}
	if len(exact) > 1 {
		return "ambiguous", exact, 0
	}
	type scored struct {
		candidate pitchCandidate
		distance  int
	}
	var scores []scored
	for _, c := range sameDivision {
		hd := levenshtein(homeKey, normalizeCaptainCSVClubKey(c.HomeClub))
		ad := levenshtein(awayKey, normalizeCaptainCSVClubKey(c.AwayClub))
		if hd <= 2 && ad <= 2 {
			scores = append(scores, scored{c, hd + ad})
		}
	}
	if len(scores) == 0 {
		return "unmatched", nil, 0
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].distance < scores[j].distance })
	best := scores[0].distance
	var bestCandidates []pitchCandidate
	for _, s := range scores {
		if s.distance == best {
			bestCandidates = append(bestCandidates, s.candidate)
		}
	}
	if len(bestCandidates) == 1 {
		return "suggested", bestCandidates, bestCandidates[0].MatchID
	}
	return "ambiguous", bestCandidates, 0
}

func (s *Server) loadPitchCandidates(ctx context.Context, from, to time.Time) (map[string][]pitchCandidate, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT play_cricket_match_id, match_date,
		       COALESCE(payload->>'competition_name',''),
		       COALESCE(home_club_name,''), COALESCE(away_club_name,'')
		FROM league_fixtures
		WHERE match_date BETWEEN $1 AND $2
		ORDER BY match_date, home_club_name, away_club_name
	`, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]pitchCandidate{}
	for rows.Next() {
		var c pitchCandidate
		if err := rows.Scan(&c.MatchID, &c.MatchDate, &c.Competition, &c.HomeClub, &c.AwayClub); err != nil {
			return nil, err
		}
		key := c.MatchDate.Format("2006-01-02")
		result[key] = append(result[key], c)
	}
	return result, rows.Err()
}

func (s *Server) handleAdminPitchMarksGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrf := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrf = c.Value
		}
		var imports, reports int64
		_ = s.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM umpire_pitch_imports`).Scan(&imports)
		_ = s.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM umpire_pitch_reports`).Scan(&reports)
		from, _ := time.Parse("2006-01-02", pitchDefaultFrom)
		to, _ := time.Parse("2006-01-02", pitchDefaultTo)
		weights := pitchWeights{Home: 10, Away: 40, Umpire: 50}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		comparisonRows, comparisonErr := s.loadPitchComparisonRows(ctx, from, to, weights)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Pitch Mark Comparison")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<div class="container-fluid px-4">
<h4 class="mb-1 fw-bold">Pitch Mark Comparison</h4>
<p class="text-muted">Compare current home captain, away captain and combined captain marks by ground, then add umpire marks when they are available.</p>
<div class="row g-3 mb-4"><div class="col-md-6"><div class="card shadow-sm h-100"><div class="card-body">
<h5>Import umpire reports</h5><p class="small text-muted">Accepts Play-Cricket Ground Response workbooks (.xlsx), plus CSV, TSV or tab-separated TXT exports. Every report is previewed and matched to a Play-Cricket fixture before import.</p>
<form method="POST" action="/admin/pitch-marks/import/preview" enctype="multipart/form-data">
<input type="hidden" name="csrf_token" value="%s"><input class="form-control mb-3" type="file" name="pitch_file" accept=".xlsx,.csv,.tsv,.txt" required>
<button class="btn btn-primary" type="submit">Upload and preview</button></form>
<p class="small text-muted mt-3 mb-0">%d imports &middot; %d reports stored</p></div></div></div>
<div class="col-md-6"><div class="card shadow-sm h-100"><div class="card-body"><h5>Download comparison CSV</h5>
<form method="GET" action="/admin/pitch-marks/export.csv">
<div class="row g-2"><div class="col-6"><label class="form-label">From</label><input class="form-control" type="date" name="from" value="%s" required></div>
<div class="col-6"><label class="form-label">To</label><input class="form-control" type="date" name="to" value="%s" required></div>
<div class="col-4"><label class="form-label">Home %%</label><input class="form-control" type="number" name="home_weight" value="10" min="0" max="100" step="0.01" required></div>
<div class="col-4"><label class="form-label">Away %%</label><input class="form-control" type="number" name="away_weight" value="40" min="0" max="100" step="0.01" required></div>
<div class="col-4"><label class="form-label">Umpire %%</label><input class="form-control" type="number" name="umpire_weight" value="50" min="0" max="100" step="0.01" required></div></div>
<p class="form-text">Weights must total 100%%. Missing sources are rebalanced automatically.</p>
<button class="btn btn-success" type="submit">Download CSV</button></form></div></div></div></div>
<div class="alert alert-info"><strong>Included competitions:</strong> %s. Fixture dates must be Saturdays.</div>`,
			escapeHTML(csrf), imports, reports, pitchDefaultFrom, pitchDefaultTo, escapeHTML(strings.Join(pitchCompetitionNames, ", ")))
		if comparisonErr != nil {
			fmt.Fprint(w, `<div class="alert alert-danger">The live captain comparison could not be loaded. The CSV export will work once the data connection is restored.</div>`)
		} else {
			writePitchComparisonTable(w, comparisonRows, from, to)
		}
		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminPitchMarksPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		f, fh, err := r.FormFile("pitch_file")
		if err != nil {
			http.Error(w, "pitch file is required", http.StatusBadRequest)
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16<<20))
		if err != nil {
			http.Error(w, "could not read upload", http.StatusBadRequest)
			return
		}
		parsed, err := parseUmpirePitchUpload(fh.Filename, data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		var from, to time.Time
		for _, row := range parsed {
			if !row.MatchDate.IsZero() && (from.IsZero() || row.MatchDate.Before(from)) {
				from = row.MatchDate
			}
			if !row.MatchDate.IsZero() && (to.IsZero() || row.MatchDate.After(to)) {
				to = row.MatchDate
			}
		}
		if from.IsZero() || to.IsZero() {
			http.Error(w, "file contains no valid match dates", http.StatusBadRequest)
			return
		}
		fixtures, err := s.loadPitchCandidates(ctx, from, to)
		if err != nil {
			http.Error(w, "could not load fixtures", http.StatusInternalServerError)
			return
		}
		seenHashes := map[string]bool{}
		for i := range parsed {
			row := &parsed[i]
			if row.SourceKind == "play_cricket_ground" {
				row.SelectedID = syntheticPitchMatchID(*row)
				row.Candidates = []pitchCandidate{{
					MatchID:     row.SelectedID,
					MatchDate:   row.MatchDate,
					Competition: row.Division,
					HomeClub:    row.HomeClub,
					AwayClub:    row.AwayClub,
				}}
			}
			if len(row.Errors) > 0 {
				continue
			}
			if seenHashes[row.Hash] {
				row.Status = "duplicate"
				continue
			}
			seenHashes[row.Hash] = true
			var duplicate bool
			_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM umpire_pitch_reports WHERE source_row_hash=$1)`, row.Hash).Scan(&duplicate)
			if duplicate {
				row.Status = "duplicate"
				continue
			}
			if row.SourceKind == "play_cricket_ground" {
				row.Status = "exact"
				continue
			}
			row.Status, row.Candidates, row.SelectedID = matchPitchRow(*row, fixtures[row.MatchDate.Format("2006-01-02")])
		}
		sum := sha256.Sum256(data)
		preview := pitchImportPreview{Filename: fh.Filename, Checksum: base64.RawURLEncoding.EncodeToString(sum[:]), Rows: parsed}
		previewJSON, _ := json.Marshal(preview)
		token, err := s.storePitchPreview(ctx, r, preview.Checksum, previewJSON)
		if err != nil {
			http.Error(w, "could not store preview", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: pitchPreviewCookie, Value: token, Path: "/admin", Expires: time.Now().Add(30 * time.Minute), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		csrf := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrf = c.Value
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Pitch Import Preview")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<div class="container-fluid px-4"><h4>Pitch import preview</h4><p class="text-muted">%d fixture reports. Suggested and ambiguous matches can be confirmed below.</p>
<form method="POST" action="/admin/pitch-marks/import/apply"><input type="hidden" name="csrf_token" value="%s">
<div class="table-responsive"><table class="table table-sm table-bordered align-middle"><thead><tr><th>#</th><th>Date</th><th>Division</th><th>Fixture from file</th><th>Marks</th><th>Status / fixture</th><th>Import</th></tr></thead><tbody>`, len(parsed), escapeHTML(csrf))
		for _, row := range parsed {
			badge := map[string]string{"exact": "bg-success", "suggested": "bg-warning text-dark", "ambiguous": "bg-warning text-dark", "duplicate": "bg-secondary", "unmatched": "bg-danger", "invalid": "bg-danger"}[row.Status]
			selectHTML := ""
			canImport := row.Status == "exact" || row.Status == "suggested" || row.Status == "ambiguous"
			if canImport {
				selectHTML = fmt.Sprintf(`<select class="form-select form-select-sm" name="fixture_%d">`, row.Index)
				if row.Status == "ambiguous" {
					selectHTML += `<option value="">Select fixture…</option>`
				}
				for _, c := range row.Candidates {
					sel := ""
					if c.MatchID == row.SelectedID {
						sel = " selected"
					}
					selectHTML += fmt.Sprintf(`<option value="%d"%s>%s</option>`, c.MatchID, sel, escapeHTML(c.label()))
				}
				selectHTML += `</select>`
			}
			reason := strings.Join(row.Errors, "; ")
			check := "—"
			if canImport {
				check = fmt.Sprintf(`<input class="form-check-input" type="checkbox" name="include" value="%d" checked>`, row.Index)
			}
			ground := strings.TrimSpace(row.Ground)
			if ground == "" {
				ground = row.HomeClub
			}
			fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%s</td><td>%s v %s<div class="small text-muted">Ground: %s</div></td><td>%.0f / %.0f / %.0f / %.0f</td><td><span class="badge %s">%s</span><div class="small text-danger">%s</div>%s</td><td class="text-center">%s</td></tr>`,
				row.Index, row.MatchDate.Format("02/01/2006"), escapeHTML(row.Division), escapeHTML(row.HomeClub), escapeHTML(row.AwayClub),
				escapeHTML(ground), row.Marks.Uneven, row.Marks.Seam, row.Marks.Carry, row.Marks.Turn, badge, escapeHTML(row.Status), escapeHTML(reason), selectHTML, check)
		}
		fmt.Fprint(w, `</tbody></table></div><button class="btn btn-success" type="submit">Import selected rows</button> <a class="btn btn-outline-secondary" href="/admin/pitch-marks">Cancel</a></form></div>`)
		pageFooter(w)
		s.audit(ctx, r, "admin", nil, "umpire_pitch_import_preview", "csv_upload", nil, map[string]any{"filename": fh.Filename, "rows": len(parsed), "checksum": preview.Checksum})
	}
}

func (s *Server) storePitchPreview(ctx context.Context, r *http.Request, checksum string, previewJSON []byte) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(token))
	adminID := adminIDForRequest(r)
	_, err := s.DB.Exec(ctx, `INSERT INTO csv_preview_tokens(id,token_hash,admin_user_id,checksum,preview_json,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), h[:], adminID, checksum, previewJSON, time.Now().Add(30*time.Minute))
	return token, err
}

func upsertPitchWorkbookFixture(ctx context.Context, tx pgx.Tx, preview pitchImportPreview, row umpirePitchParsedRow) (bool, error) {
	if row.SourceKind != "play_cricket_ground" || row.MatchDate.IsZero() || row.HomeClub == "" || row.AwayClub == "" || row.Division == "" {
		return false, nil
	}
	ground := strings.TrimSpace(row.Ground)
	if ground == "" {
		ground = row.HomeClub
	}
	homeTeam := strings.TrimSpace(row.Raw["home_team"])
	if homeTeam == "" {
		homeTeam = row.HomeClub + " - 1st XI"
	}
	awayTeam := strings.TrimSpace(row.Raw["away_team"])
	if awayTeam == "" {
		awayTeam = row.AwayClub + " - 1st XI"
	}
	payload, err := json.Marshal(map[string]string{
		"competition_name":     row.Division,
		"pitch_fixture_source": "play_cricket_ground_xlsx",
		"source_filename":      preview.Filename,
		"source_checksum":      preview.Checksum,
	})
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO league_fixtures(
			play_cricket_match_id,season_id,match_date,league_id,competition_id,
			home_club_name,away_club_name,home_team_name,away_team_name,ground_name,
			payload,fetched_at,updated_at
		)
		VALUES(
			$1,(SELECT id FROM seasons WHERE $2::date BETWEEN start_date AND end_date ORDER BY start_date DESC LIMIT 1),
			$2,'GMCL',$3,$4,$5,$6,$7,$8,$9,now(),now()
		)
		ON CONFLICT(play_cricket_match_id) DO UPDATE SET
			season_id=EXCLUDED.season_id,
			match_date=EXCLUDED.match_date,
			league_id=EXCLUDED.league_id,
			competition_id=EXCLUDED.competition_id,
			home_club_name=EXCLUDED.home_club_name,
			away_club_name=EXCLUDED.away_club_name,
			home_team_name=EXCLUDED.home_team_name,
			away_team_name=EXCLUDED.away_team_name,
			ground_name=EXCLUDED.ground_name,
			payload=EXCLUDED.payload,
			fetched_at=now(),
			updated_at=now()
	`, syntheticPitchMatchID(row), row.MatchDate, row.Division, row.HomeClub, row.AwayClub, homeTeam, awayTeam, ground, payload)
	return err == nil, err
}

func (s *Server) handleAdminPitchMarksApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		cookie, err := r.Cookie(pitchPreviewCookie)
		if err != nil {
			http.Error(w, "preview expired", http.StatusBadRequest)
			return
		}
		h := sha256.Sum256([]byte(cookie.Value))
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			http.Error(w, "could not start import", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)
		var storedHash, previewJSON []byte
		var checksum string
		var expires time.Time
		var used *time.Time
		err = tx.QueryRow(ctx, `SELECT token_hash,preview_json,checksum,expires_at,used_at FROM csv_preview_tokens WHERE token_hash=$1 AND admin_user_id=$2 FOR UPDATE`, h[:], adminIDForRequest(r)).Scan(&storedHash, &previewJSON, &checksum, &expires, &used)
		if err != nil || subtle.ConstantTimeCompare(storedHash, h[:]) != 1 || used != nil || time.Now().After(expires) {
			http.Error(w, "preview invalid, expired or already used", http.StatusBadRequest)
			return
		}
		var preview pitchImportPreview
		if json.Unmarshal(previewJSON, &preview) != nil {
			http.Error(w, "preview invalid", http.StatusBadRequest)
			return
		}
		included := map[int]bool{}
		for _, v := range r.Form["include"] {
			if n, e := strconv.Atoi(v); e == nil {
				included[n] = true
			}
		}
		var importID int64
		err = tx.QueryRow(ctx, `INSERT INTO umpire_pitch_imports(source_filename,source_checksum,imported_by_admin_id,row_count) VALUES($1,$2,$3,$4) RETURNING id`, preview.Filename, checksum, adminIDForRequest(r), len(preview.Rows)).Scan(&importID)
		if err != nil {
			http.Error(w, "could not create import", http.StatusInternalServerError)
			return
		}
		fixturesUpdated := 0
		for _, row := range preview.Rows {
			updated, fixtureErr := upsertPitchWorkbookFixture(ctx, tx, preview, row)
			if fixtureErr != nil {
				http.Error(w, "could not update ground fixtures", http.StatusInternalServerError)
				return
			}
			if updated {
				fixturesUpdated++
			}
		}
		imported := 0
		for _, row := range preview.Rows {
			if !included[row.Index] || (row.Status != "exact" && row.Status != "suggested" && row.Status != "ambiguous") {
				continue
			}
			selected, _ := strconv.ParseInt(r.FormValue(fmt.Sprintf("fixture_%d", row.Index)), 10, 64)
			validCandidate := false
			for _, c := range row.Candidates {
				if c.MatchID == selected {
					validCandidate = true
					break
				}
			}
			if !validCandidate {
				continue
			}
			rawJSON, _ := json.Marshal(row.Raw)
			var timestampArg any
			if !row.Timestamp.IsZero() {
				timestampArg = row.Timestamp
			}
			sourceKind := strings.TrimSpace(row.SourceKind)
			if sourceKind == "" {
				sourceKind = "panel_form"
			}
			args := []any{
				importID, selected, timestampArg, row.MatchDate, row.Division, row.HomeClub, row.AwayClub,
				int(row.Marks.Uneven), int(row.Marks.Seam), int(row.Marks.Carry), int(row.Marks.Turn),
				row.Hash, rawJSON, sourceKind,
			}
			execReport := func(query string) (int64, error) {
				cmd, execErr := tx.Exec(ctx, query, args...)
				if execErr != nil {
					return 0, execErr
				}
				return cmd.RowsAffected(), nil
			}
			var affected int64
			var execErr error
			if sourceKind == "play_cricket_ground" {
				affected, execErr = execReport(`
					INSERT INTO umpire_pitch_reports(import_id,play_cricket_match_id,source_timestamp,match_date,division_label,home_club_label,away_club_label,unevenness_mark,seam_mark,carry_mark,turn_mark,source_row_hash,source_row,source_kind)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
					ON CONFLICT (play_cricket_match_id,source_kind) WHERE source_kind='play_cricket_ground'
					DO UPDATE SET
						import_id=EXCLUDED.import_id,
						source_timestamp=EXCLUDED.source_timestamp,
						match_date=EXCLUDED.match_date,
						division_label=EXCLUDED.division_label,
						home_club_label=EXCLUDED.home_club_label,
						away_club_label=EXCLUDED.away_club_label,
						unevenness_mark=EXCLUDED.unevenness_mark,
						seam_mark=EXCLUDED.seam_mark,
						carry_mark=EXCLUDED.carry_mark,
						turn_mark=EXCLUDED.turn_mark,
						source_row_hash=EXCLUDED.source_row_hash,
						source_row=EXCLUDED.source_row,
						created_at=now()
				`)
			} else {
				affected, execErr = execReport(`
					INSERT INTO umpire_pitch_reports(import_id,play_cricket_match_id,source_timestamp,match_date,division_label,home_club_label,away_club_label,unevenness_mark,seam_mark,carry_mark,turn_mark,source_row_hash,source_row,source_kind)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
					ON CONFLICT(source_row_hash) DO NOTHING
				`)
			}
			if execErr != nil {
				http.Error(w, "could not import selected rows", http.StatusInternalServerError)
				return
			}
			imported += int(affected)
		}
		_, err = tx.Exec(ctx, `UPDATE umpire_pitch_imports SET imported_count=$2,skipped_count=$3 WHERE id=$1`, importID, imported, len(preview.Rows)-imported)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE csv_preview_tokens SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL`, h[:])
		}
		if err != nil || tx.Commit(ctx) != nil {
			http.Error(w, "could not finish import", http.StatusInternalServerError)
			return
		}
		s.audit(ctx, r, "admin", nil, "umpire_pitch_import_apply", "umpire_pitch_import", &importID, map[string]any{"imported": imported, "skipped": len(preview.Rows) - imported, "fixtures_updated": fixturesUpdated, "checksum": checksum})
		http.Redirect(w, r, "/admin/pitch-marks?imported="+strconv.Itoa(imported), http.StatusSeeOther)
	}
}

type pitchWeights struct{ Home, Away, Umpire float64 }

func parsePitchWeights(r *http.Request) (pitchWeights, error) {
	parse := func(name string, fallback float64) (float64, error) {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			return fallback, nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 0 || v > 100 {
			return 0, fmt.Errorf("%s must be between 0 and 100", name)
		}
		return v, nil
	}
	h, err := parse("home_weight", 10)
	if err != nil {
		return pitchWeights{}, err
	}
	a, err := parse("away_weight", 40)
	if err != nil {
		return pitchWeights{}, err
	}
	u, err := parse("umpire_weight", 50)
	if err != nil {
		return pitchWeights{}, err
	}
	if math.Abs(h+a+u-100) > 0.001 {
		return pitchWeights{}, fmt.Errorf("weights must total 100")
	}
	return pitchWeights{h, a, u}, nil
}

func captainPitchMark(level int) float64 {
	switch level {
	case 1:
		return 5
	case 2:
		return 4
	case 3:
		return 3
	case 4:
		return 2
	case 5, 6:
		return 1
	default:
		return 0
	}
}

func captainPitchVector(data []byte) (pitchVector, bool) {
	var form map[string]any
	if json.Unmarshal(data, &form) != nil {
		return pitchVector{}, false
	}
	outcome, _ := form["match_outcome"].(string)
	if outcome = strings.TrimSpace(outcome); outcome != "" && outcome != "played" && outcome != "play_started_abandoned" {
		return pitchVector{}, false
	}
	read := func(key string) int {
		switch v := form[key].(type) {
		case float64:
			return int(v)
		case string:
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		default:
			return 0
		}
	}
	v := pitchVector{captainPitchMark(read("unevenness_of_bounce")), captainPitchMark(read("seam_movement")), captainPitchMark(read("carry_bounce")), captainPitchMark(read("turn"))}
	return v, v.Uneven > 0 && v.Seam > 0 && v.Carry > 0 && v.Turn > 0
}

func weightedPitchVector(sources map[string]pitchVector, weights pitchWeights) (pitchVector, map[string]float64, []string, bool) {
	configured := map[string]float64{"home": weights.Home, "away": weights.Away, "umpire": weights.Umpire}
	effective := map[string]float64{"home": 0, "away": 0, "umpire": 0}
	missing := []string{}
	total := 0.0
	for _, name := range []string{"home", "away", "umpire"} {
		if _, ok := sources[name]; !ok {
			missing = append(missing, name)
			continue
		}
		total += configured[name]
	}
	if total == 0 {
		return pitchVector{}, effective, missing, false
	}
	result := pitchVector{}
	for name, v := range sources {
		w := configured[name] / total
		effective[name] = w * 100
		result = result.add(pitchVector{v.Uneven * w, v.Seam * w, v.Carry * w, v.Turn * w})
	}
	return result, effective, missing, true
}

type pitchFixture struct {
	ID, HomeTeamPC, AwayTeamPC string
	MatchID                    int64
	Date                       time.Time
	Competition, HomeClub      string
	AwayClub, Ground           string
}

func pitchFixtureGroundLabel(f pitchFixture) string {
	if ground := strings.TrimSpace(f.Ground); ground != "" {
		return ground
	}
	return strings.TrimSpace(f.HomeClub)
}

func pitchFixtureGroundKey(f pitchFixture) string {
	return normalizeCaptainCSVClubKey(pitchFixtureGroundLabel(f))
}

type pitchSourceValue struct {
	Vector  pitchVector
	Reports int
}
type pitchClubAggregate struct {
	Name             string
	Divisions        map[string]bool
	FixtureCount     int
	Sources          map[string][]pitchSourceValue
	ExcludedCaptains int
}

type pitchComparisonRow struct {
	Club                 string
	Divisions            []string
	EligibleFixtures     int
	Home                 pitchVector
	HomeOK               bool
	HomeFixtures         int
	Away                 pitchVector
	AwayOK               bool
	AwayFixtures         int
	Combined             pitchVector
	CombinedOK           bool
	Umpire               pitchVector
	UmpireOK             bool
	UmpireFixtures       int
	UmpireReports        int
	Weighted             pitchVector
	WeightedOK           bool
	Effective            map[string]float64
	Missing              []string
	ExcludedCaptainMarks int
}

func (s *Server) loadPitchComparisonRows(ctx context.Context, from, to time.Time, weights pitchWeights) ([]pitchComparisonRow, error) {
	fixtures, clubs, err := s.loadPitchExportFixtures(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if err := s.addCaptainPitchSources(ctx, from, to, fixtures, clubs); err != nil {
		return nil, err
	}
	if err := s.addUmpirePitchSources(ctx, from, to, fixtures, clubs); err != nil {
		return nil, err
	}
	return buildPitchComparisonRows(clubs, weights), nil
}

func buildPitchComparisonRows(clubs map[string]*pitchClubAggregate, weights pitchWeights) []pitchComparisonRow {
	rows := make([]pitchComparisonRow, 0, len(clubs))
	for _, club := range clubs {
		home, homeOK, homeFixtures, _ := averagePitchSource(club.Sources["home"])
		away, awayOK, awayFixtures, _ := averagePitchSource(club.Sources["away"])
		umpire, umpireOK, umpireFixtures, umpireReports := averagePitchSource(club.Sources["umpire"])

		captainSources := map[string]pitchVector{}
		if homeOK {
			captainSources["home"] = home
		}
		if awayOK {
			captainSources["away"] = away
		}
		combined, _, _, combinedOK := weightedPitchVector(captainSources, pitchWeights{Home: weights.Home, Away: weights.Away})

		allSources := map[string]pitchVector{}
		if homeOK {
			allSources["home"] = home
		}
		if awayOK {
			allSources["away"] = away
		}
		if umpireOK {
			allSources["umpire"] = umpire
		}
		weighted, effective, missing, weightedOK := weightedPitchVector(allSources, weights)

		divisions := make([]string, 0, len(club.Divisions))
		for division := range club.Divisions {
			divisions = append(divisions, division)
		}
		sort.Strings(divisions)
		rows = append(rows, pitchComparisonRow{
			Club:                 club.Name,
			Divisions:            divisions,
			EligibleFixtures:     club.FixtureCount,
			Home:                 home,
			HomeOK:               homeOK,
			HomeFixtures:         homeFixtures,
			Away:                 away,
			AwayOK:               awayOK,
			AwayFixtures:         awayFixtures,
			Combined:             combined,
			CombinedOK:           combinedOK,
			Umpire:               umpire,
			UmpireOK:             umpireOK,
			UmpireFixtures:       umpireFixtures,
			UmpireReports:        umpireReports,
			Weighted:             weighted,
			WeightedOK:           weightedOK,
			Effective:            effective,
			Missing:              missing,
			ExcludedCaptainMarks: club.ExcludedCaptains,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		leftDivision := primaryPitchDivision(rows[i].Divisions)
		rightDivision := primaryPitchDivision(rows[j].Divisions)
		leftRank := pitchDivisionRank(leftDivision)
		rightRank := pitchDivisionRank(rightDivision)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return strings.ToLower(rows[i].Club) < strings.ToLower(rows[j].Club)
	})
	return rows
}

func pitchDivisionRank(division string) int {
	for index, name := range pitchCompetitionNames {
		if division == name {
			return index
		}
	}
	return len(pitchCompetitionNames)
}

func primaryPitchDivision(divisions []string) string {
	primary := ""
	primaryRank := len(pitchCompetitionNames) + 1
	for _, division := range divisions {
		if rank := pitchDivisionRank(division); rank < primaryRank {
			primary = division
			primaryRank = rank
		}
	}
	return primary
}

func pitchDivisionDisplayName(division string) string {
	if display := pitchCompetitionDisplayNames[division]; display != "" {
		return display
	}
	if division != "" {
		return division
	}
	return "Other"
}

func writePitchComparisonTable(w io.Writer, rows []pitchComparisonRow, from, to time.Time) {
	eligibleFixtures, homeFixtures, awayFixtures, umpireFixtures := 0, 0, 0, 0
	for _, row := range rows {
		eligibleFixtures += row.EligibleFixtures
		homeFixtures += row.HomeFixtures
		awayFixtures += row.AwayFixtures
		umpireFixtures += row.UmpireFixtures
	}
	fmt.Fprintf(w, `<div class="card shadow-sm mb-4">
<div class="card-header d-flex flex-wrap align-items-center gap-2"><div><strong>Live ground comparison</strong><div class="small text-muted">%s to %s</div></div><span class="badge text-bg-light ms-auto">%d grounds</span></div>
<div class="card-body border-bottom"><p class="mb-3">Captain figures are rebuilt from the current linked submissions whenever this page is opened. If a report is amended, the newest submission for that match and side is used automatically.</p>
<div class="row g-2 text-center">
<div class="col-6 col-md-3"><div class="border rounded p-2"><div class="h5 mb-0">%d</div><div class="small text-muted">eligible fixtures</div></div></div>
<div class="col-6 col-md-3"><div class="border rounded p-2"><div class="h5 mb-0">%d</div><div class="small text-muted">home captain reports</div></div></div>
<div class="col-6 col-md-3"><div class="border rounded p-2"><div class="h5 mb-0">%d</div><div class="small text-muted">away captain reports</div></div></div>
<div class="col-6 col-md-3"><div class="border rounded p-2"><div class="h5 mb-0">%d</div><div class="small text-muted">fixtures with umpire marks</div></div></div>
</div></div>`,
		from.Format("2 January 2006"), to.Format("2 January 2006"), len(rows),
		eligibleFixtures, homeFixtures, awayFixtures, umpireFixtures)
	if len(rows) == 0 {
		fmt.Fprint(w, `<div class="card-body"><div class="alert alert-warning mb-0">No eligible Saturday league fixtures were found for this period.</div></div></div>`)
		return
	}
	fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0">
<thead><tr><th>Ground</th><th class="text-center">Fixtures</th><th>Home captain</th><th>Away captain</th><th>Combined captains</th><th>Umpires</th><th>Coverage</th></tr></thead><tbody>`)
	currentDivision := ""
	firstDivision := true
	for _, row := range rows {
		division := primaryPitchDivision(row.Divisions)
		if firstDivision || division != currentDivision {
			fmt.Fprintf(w, `<tr class="table-secondary"><th colspan="7" class="py-2 fs-6">%s</th></tr>`, escapeHTML(pitchDivisionDisplayName(division)))
			currentDivision = division
			firstDivision = false
		}
		coverage := fmt.Sprintf(`Home %d/%d &middot; Away %d/%d`, row.HomeFixtures, row.EligibleFixtures, row.AwayFixtures, row.EligibleFixtures)
		if len(row.Missing) > 0 {
			coverage += `<div class="small text-warning">Awaiting: ` + escapeHTML(strings.Join(row.Missing, ", ")) + `</div>`
		}
		if row.ExcludedCaptainMarks > 0 {
			coverage += fmt.Sprintf(`<div class="small text-muted">%d unlinked or incomplete captain report(s)</div>`, row.ExcludedCaptainMarks)
		}
		umpireHTML := pitchVectorSummaryHTML(row.Umpire, row.UmpireOK, row.UmpireFixtures)
		if !row.UmpireOK {
			umpireHTML = `<span class="badge text-bg-light">Pending detailed import</span>`
		}
		fmt.Fprintf(w, `<tr><td><strong>%s</strong><div class="small text-muted">%s</div></td><td class="text-center">%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="small">%s</td></tr>`,
			escapeHTML(row.Club), escapeHTML(strings.Join(row.Divisions, "; ")), row.EligibleFixtures,
			pitchVectorSummaryHTML(row.Home, row.HomeOK, row.HomeFixtures),
			pitchVectorSummaryHTML(row.Away, row.AwayOK, row.AwayFixtures),
			pitchVectorSummaryHTML(row.Combined, row.CombinedOK, row.HomeFixtures+row.AwayFixtures),
			umpireHTML, coverage)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func pitchVectorSummaryHTML(v pitchVector, ok bool, fixtures int) string {
	if !ok {
		return `<span class="text-muted">No mark</span>`
	}
	return fmt.Sprintf(`<strong>%.2f overall</strong><div class="small text-muted">Uneven %.2f &middot; Seam %.2f &middot; Carry %.2f &middot; Turn %.2f</div><div class="small text-muted">%d report fixture(s)</div>`,
		v.overall(), v.Uneven, v.Seam, v.Carry, v.Turn, fixtures)
}

func (s *Server) handleAdminPitchMarksExportCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		weights, err := parsePitchWeights(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fromRaw, toRaw := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		if fromRaw == "" {
			fromRaw = pitchDefaultFrom
		}
		if toRaw == "" {
			toRaw = pitchDefaultTo
		}
		from, err1 := time.Parse("2006-01-02", fromRaw)
		to, err2 := time.Parse("2006-01-02", toRaw)
		if err1 != nil || err2 != nil || to.Before(from) {
			http.Error(w, "invalid date range", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		comparisonRows, err := s.loadPitchComparisonRows(ctx, from, to, weights)
		if err != nil {
			http.Error(w, "could not load pitch comparison", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gmcl-pitch-comparison-%s-to-%s.csv"`, from.Format("20060102"), to.Format("20060102")))
		w.Header().Set("Cache-Control", "no-store")
		cw := csv.NewWriter(w)
		header := []string{"Club", "Divisions", "Eligible fixtures",
			"Home captain fixtures", "Home unevenness", "Home seam", "Home carry/bounce", "Home turn", "Home overall",
			"Away captain fixtures", "Away unevenness", "Away seam", "Away carry/bounce", "Away turn", "Away overall",
			"Combined captain unevenness", "Combined captain seam", "Combined captain carry/bounce", "Combined captain turn", "Combined captain overall",
			"Umpire fixtures", "Umpire reports", "Umpire unevenness", "Umpire seam", "Umpire carry/bounce", "Umpire turn", "Umpire overall",
			"Weighted unevenness", "Weighted seam", "Weighted carry/bounce", "Weighted turn", "Weighted overall",
			"Configured home %", "Configured away %", "Configured umpire %", "Effective home %", "Effective away %", "Effective umpire %",
			"Missing sources", "Excluded captain reports"}
		_ = cw.Write(header)
		for _, row := range comparisonRows {
			record := []string{safeCSVCell(row.Club), safeCSVCell(strings.Join(row.Divisions, "; ")), strconv.Itoa(row.EligibleFixtures), strconv.Itoa(row.HomeFixtures)}
			record = append(record, pitchVectorCells(row.Home, row.HomeOK)...)
			record = append(record, strconv.Itoa(row.AwayFixtures))
			record = append(record, pitchVectorCells(row.Away, row.AwayOK)...)
			record = append(record, pitchVectorCells(row.Combined, row.CombinedOK)...)
			record = append(record, strconv.Itoa(row.UmpireFixtures), strconv.Itoa(row.UmpireReports))
			record = append(record, pitchVectorCells(row.Umpire, row.UmpireOK)...)
			record = append(record, pitchVectorCells(row.Weighted, row.WeightedOK)...)
			record = append(record, format2(weights.Home), format2(weights.Away), format2(weights.Umpire), format2(row.Effective["home"]), format2(row.Effective["away"]), format2(row.Effective["umpire"]), strings.Join(row.Missing, "; "), strconv.Itoa(row.ExcludedCaptainMarks))
			_ = cw.Write(record)
		}
		cw.Flush()
		if cw.Error() != nil {
			return
		}
		s.audit(ctx, r, "admin", nil, "pitch_mark_comparison_export", "csv_export", nil, map[string]any{"from": fromRaw, "to": toRaw, "home_weight": weights.Home, "away_weight": weights.Away, "umpire_weight": weights.Umpire, "clubs": len(comparisonRows)})
	}
}

func (s *Server) loadPitchExportFixtures(ctx context.Context, from, to time.Time) (map[int64]pitchFixture, map[string]*pitchClubAggregate, error) {
	rows, err := s.DB.Query(ctx, `
		WITH scoped AS (
			SELECT play_cricket_match_id,match_date,COALESCE(payload->>'competition_name','') AS competition,
			       COALESCE(home_club_name,'') AS home_club,COALESCE(away_club_name,'') AS away_club,
			       COALESCE(home_team_pc_id,'') AS home_team_pc,COALESCE(away_team_pc_id,'') AS away_team_pc,
			       COALESCE(ground_name,'') AS ground,COALESCE(payload->>'pitch_fixture_source','') AS pitch_source
			FROM league_fixtures
			WHERE match_date BETWEEN $1 AND $2 AND EXTRACT(ISODOW FROM match_date)=6
			  AND COALESCE(payload->>'competition_name','') IN ('GMCL Saturday Premier','GMCL Saturday Premier 2','GMCL Saturday Championship','GMCL Saturday Division 1')
		)
		SELECT play_cricket_match_id,match_date,competition,home_club,away_club,home_team_pc,away_team_pc,ground
		FROM scoped fixture
		WHERE fixture.pitch_source='play_cricket_ground_xlsx'
		   OR NOT EXISTS (SELECT 1 FROM scoped imported WHERE imported.pitch_source='play_cricket_ground_xlsx')
	`, from, to)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	fixtures := map[int64]pitchFixture{}
	clubs := map[string]*pitchClubAggregate{}
	for rows.Next() {
		var f pitchFixture
		if err := rows.Scan(&f.MatchID, &f.Date, &f.Competition, &f.HomeClub, &f.AwayClub, &f.HomeTeamPC, &f.AwayTeamPC, &f.Ground); err != nil {
			return nil, nil, err
		}
		fixtures[f.MatchID] = f
		key := pitchFixtureGroundKey(f)
		if clubs[key] == nil {
			clubs[key] = &pitchClubAggregate{Name: pitchFixtureGroundLabel(f), Divisions: map[string]bool{}, Sources: map[string][]pitchSourceValue{}}
		}
		clubs[key].FixtureCount++
		clubs[key].Divisions[f.Competition] = true
	}
	return fixtures, clubs, rows.Err()
}

func (s *Server) addCaptainPitchSources(ctx context.Context, from, to time.Time, fixtures map[int64]pitchFixture, clubs map[string]*pitchClubAggregate) error {
	rows, err := s.DB.Query(ctx, `
		SELECT COALESCE(sub.play_cricket_match_id,0),sub.match_date,cl.name,t.name,COALESCE(t.level,0),
		       COALESCE(t.play_cricket_team_id,''),COALESCE(sub.form_data,'{}'::jsonb),sub.submitted_at
		FROM submissions sub
		JOIN teams t ON t.id=sub.team_id
		JOIN clubs cl ON cl.id=t.club_id
		WHERE sub.match_date BETWEEN $1 AND $2 AND EXTRACT(ISODOW FROM sub.match_date)=6
	`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	var submissions []captainPitchSubmission
	for rows.Next() {
		var submission captainPitchSubmission
		if err := rows.Scan(
			&submission.MatchID, &submission.MatchDate, &submission.Club, &submission.Team, &submission.TeamLevel,
			&submission.TeamPC, &submission.Data, &submission.Submitted,
		); err != nil {
			return err
		}
		submissions = append(submissions, submission)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	applyCaptainPitchSubmissions(fixtures, clubs, submissions)
	return nil
}

type captainPitchSubmission struct {
	MatchID   int64
	MatchDate time.Time
	Club      string
	Team      string
	TeamLevel int32
	TeamPC    string
	Data      []byte
	Submitted time.Time
}

func resolveCaptainPitchSubmission(fixtures map[int64]pitchFixture, submission captainPitchSubmission) (pitchFixture, string, bool) {
	resolveSide := func(f pitchFixture) string {
		teamPC := strings.TrimSpace(submission.TeamPC)
		if teamPC != "" && teamPC == strings.TrimSpace(f.HomeTeamPC) {
			return "home"
		}
		if teamPC != "" && teamPC == strings.TrimSpace(f.AwayTeamPC) {
			return "away"
		}
		clubKey := normalizeCaptainCSVClubKey(submission.Club)
		if clubKey != "" && clubKey == normalizeCaptainCSVClubKey(f.HomeClub) {
			return "home"
		}
		if clubKey != "" && clubKey == normalizeCaptainCSVClubKey(f.AwayClub) {
			return "away"
		}
		return ""
	}
	if f, ok := fixtures[submission.MatchID]; ok {
		if side := resolveSide(f); side != "" {
			return f, side, true
		}
		return f, "", true
	}
	teamKey := normalizeCaptainCSVTeamKey(submission.Team)
	if submission.TeamLevel != 1 && teamKey != "1xi" && teamKey != "1" && teamKey != "firstteam" {
		return pitchFixture{}, "", false
	}
	type match struct {
		fixture pitchFixture
		side    string
	}
	var matches []match
	for _, f := range fixtures {
		if submission.MatchDate.IsZero() || !samePitchDate(f.Date, submission.MatchDate) {
			continue
		}
		if side := resolveSide(f); side != "" {
			matches = append(matches, match{fixture: f, side: side})
		}
	}
	if len(matches) != 1 {
		return pitchFixture{}, "", false
	}
	return matches[0].fixture, matches[0].side, true
}

func samePitchDate(a, b time.Time) bool {
	return a.Format("2006-01-02") == b.Format("2006-01-02")
}

func applyCaptainPitchSubmissions(fixtures map[int64]pitchFixture, clubs map[string]*pitchClubAggregate, submissions []captainPitchSubmission) {
	type latest struct {
		data      []byte
		submitted time.Time
	}
	latestBySide := map[string]latest{}
	for _, submission := range submissions {
		f, side, ok := resolveCaptainPitchSubmission(fixtures, submission)
		if !ok {
			continue
		}
		club := clubs[pitchFixtureGroundKey(f)]
		if club == nil {
			continue
		}
		if side == "" {
			club.ExcludedCaptains++
			continue
		}
		key := fmt.Sprintf("%d|%s", f.MatchID, side)
		if previous, exists := latestBySide[key]; !exists || submission.Submitted.After(previous.submitted) {
			latestBySide[key] = latest{submission.Data, submission.Submitted}
		}
	}
	for key, value := range latestBySide {
		parts := strings.Split(key, "|")
		matchID, _ := strconv.ParseInt(parts[0], 10, 64)
		side := parts[1]
		f := fixtures[matchID]
		club := clubs[pitchFixtureGroundKey(f)]
		vector, valid := captainPitchVector(value.data)
		if !valid {
			club.ExcludedCaptains++
			continue
		}
		club.Sources[side] = append(club.Sources[side], pitchSourceValue{Vector: vector, Reports: 1})
	}
}

func (s *Server) addUmpirePitchSources(ctx context.Context, from, to time.Time, fixtures map[int64]pitchFixture, clubs map[string]*pitchClubAggregate) error {
	rows, err := s.DB.Query(ctx, `
		SELECT report.play_cricket_match_id,report.unevenness_mark,report.seam_mark,report.carry_mark,report.turn_mark
		FROM umpire_pitch_reports report
		WHERE report.match_date BETWEEN $1 AND $2
		  AND (
			report.source_kind='play_cricket_ground'
			OR NOT EXISTS (
				SELECT 1
				FROM umpire_pitch_reports ground
				WHERE ground.play_cricket_match_id=report.play_cricket_match_id
				  AND ground.source_kind='play_cricket_ground'
			)
		  )
	`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	type acc struct {
		vector pitchVector
		count  int
	}
	byFixture := map[int64]acc{}
	for rows.Next() {
		var id int64
		var u, se, c, t float64
		if err := rows.Scan(&id, &u, &se, &c, &t); err != nil {
			return err
		}
		if _, ok := fixtures[id]; !ok {
			continue
		}
		a := byFixture[id]
		a.vector = a.vector.add(pitchVector{u, se, c, t})
		a.count++
		byFixture[id] = a
	}
	for id, a := range byFixture {
		f := fixtures[id]
		club := clubs[pitchFixtureGroundKey(f)]
		club.Sources["umpire"] = append(club.Sources["umpire"], pitchSourceValue{Vector: a.vector.div(float64(a.count)), Reports: a.count})
	}
	return rows.Err()
}

func averagePitchSource(values []pitchSourceValue) (pitchVector, bool, int, int) {
	if len(values) == 0 {
		return pitchVector{}, false, 0, 0
	}
	total := pitchVector{}
	reports := 0
	for _, v := range values {
		total = total.add(v.Vector)
		reports += v.Reports
	}
	return total.div(float64(len(values))), true, len(values), reports
}

func pitchVectorCells(v pitchVector, ok bool) []string {
	if !ok {
		return []string{"", "", "", "", ""}
	}
	return []string{format2(v.Uneven), format2(v.Seam), format2(v.Carry), format2(v.Turn), format2(v.overall())}
}

func format2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func safeCSVCell(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}
