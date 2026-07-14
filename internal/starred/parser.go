package starred

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	listRowRE  = regexp.MustCompile(`(?i)^list\s+([ab])(\d+)$`)
	prefixRE   = regexp.MustCompile(`(?i)^\s*[ab]\d+\s*-\s*`)
	dateRE     = regexp.MustCompile(`\((\d{2}/\d{2}/\d{4})\)\s*$`)
	replaceRE  = regexp.MustCompile(`(?i)^\s*(.*?)\s+replaced\s+by\s+(.*?)\s*$`)
	knownTagRE = regexp.MustCompile(`(?i)\((pro|o/s|overseas|under\s*1[78]|u1[78])\)|\b(under\s*1[78]|u1[78])\b`)
)

func ParsePublishedCSV(r io.Reader, seasonYear int) (Snapshot, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read starred-player CSV: %w", err)
	}
	if len(records) < 2 || len(records[0]) < 2 {
		return Snapshot{}, fmt.Errorf("starred-player CSV is empty")
	}

	headers := records[0]
	s := Snapshot{SeasonYear: seasonYear}
	clubByColumn := make(map[int]int)
	for col := 1; col < len(headers); col++ {
		name := strings.TrimSpace(headers[col])
		if name == "" || strings.EqualFold(name, "xx") {
			continue
		}
		info := ClubInfo{ClubName: name, ClubKey: NormalizeClub(name)}
		s.Clubs = append(s.Clubs, info)
		clubByColumn[col] = len(s.Clubs) - 1
	}

	for _, rec := range records[1:] {
		if len(rec) == 0 {
			continue
		}
		label := strings.TrimSpace(rec[0])
		if m := listRowRE.FindStringSubmatch(label); m != nil {
			listType := strings.ToUpper(m[1])
			slot, _ := strconv.Atoi(m[2])
			for col := 1; col < len(headers); col++ {
				clubIndex, ok := clubByColumn[col]
				if !ok {
					continue
				}
				if col >= len(rec) {
					continue
				}
				club := &s.Clubs[clubIndex]
				raw := strings.TrimSpace(rec[col])
				if raw == "" {
					continue
				}
				name, tags := parsePlayerCell(raw)
				if name == "" {
					continue
				}
				s.Entries = append(s.Entries, Entry{
					SeasonYear: seasonYear, ClubName: club.ClubName, ClubKey: club.ClubKey,
					ListType: listType, Slot: slot, PlayerName: name, PlayerKey: NormalizeName(name),
					RawValue: raw, Tags: tags,
				})
			}
			continue
		}

		switch {
		case strings.EqualFold(label, "List B Required?"):
			for col := 1; col < len(headers); col++ {
				clubIndex, ok := clubByColumn[col]
				if !ok {
					continue
				}
				if col < len(rec) {
					club := &s.Clubs[clubIndex]
					club.ListBRule = strings.TrimSpace(rec[col])
				}
			}
		case strings.EqualFold(label, "Number of Starred Players Submitted"):
			for col := 1; col < len(headers); col++ {
				clubIndex, ok := clubByColumn[col]
				if !ok {
					continue
				}
				if col < len(rec) {
					club := &s.Clubs[clubIndex]
					club.SubmittedCount, _ = strconv.Atoi(strings.TrimSpace(rec[col]))
				}
			}
		case strings.HasPrefix(strings.ToLower(label), "amendment"):
			seq, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(label), "amendment")))
			for col := 1; col < len(headers); col++ {
				clubIndex, ok := clubByColumn[col]
				if !ok {
					continue
				}
				if col >= len(rec) || strings.TrimSpace(rec[col]) == "" {
					continue
				}
				s.Amendments = append(s.Amendments, parseAmendment(seasonYear, s.Clubs[clubIndex], seq, rec[col]))
			}
		default:
			for col := 1; col < len(headers); col++ {
				clubIndex, ok := clubByColumn[col]
				if !ok {
					continue
				}
				if col < len(rec) && strings.EqualFold(strings.TrimSpace(rec[col]), "No form submitted") {
					club := &s.Clubs[clubIndex]
					club.NoForm = true
				}
			}
		}
	}
	return s, nil
}

func parsePlayerCell(raw string) (string, []string) {
	name := strings.TrimSpace(prefixRE.ReplaceAllString(raw, ""))
	var tags []string
	for _, m := range knownTagRE.FindAllString(name, -1) {
		tags = append(tags, strings.ToLower(strings.Trim(strings.TrimSpace(m), "()")))
	}
	name = strings.TrimSpace(knownTagRE.ReplaceAllString(name, ""))
	name = whitespaceRE.ReplaceAllString(name, " ")
	return name, tags
}

func parseAmendment(year int, club ClubInfo, seq int, raw string) Amendment {
	a := Amendment{SeasonYear: year, ClubName: club.ClubName, ClubKey: club.ClubKey, Sequence: seq, RawValue: strings.TrimSpace(raw), Status: "parsed"}
	withoutDate := a.RawValue
	if m := dateRE.FindStringSubmatch(withoutDate); m != nil {
		if d, err := time.Parse("02/01/2006", m[1]); err == nil {
			a.Date = &d
		}
		withoutDate = strings.TrimSpace(dateRE.ReplaceAllString(withoutDate, ""))
	} else {
		a.Status, a.Issue = "review", "missing or invalid effective date"
	}
	if m := replaceRE.FindStringSubmatch(withoutDate); m != nil {
		a.Outgoing = cleanAmendmentName(m[1])
		a.Incoming = cleanAmendmentName(m[2])
		a.OutgoingKey = NormalizeName(a.Outgoing)
		a.IncomingKey = NormalizeName(a.Incoming)
		if a.IncomingKey == "" {
			a.Status, a.Issue = "review", "missing incoming player"
		}
	} else {
		a.Status, a.Issue = "review", "could not parse replacement"
	}
	return a
}

func cleanAmendmentName(s string) string {
	s = regexp.MustCompile(`(?i)\b(to\s+(go|come)\s+(on|off)?\s*[ab]\s+list|to\s+(go|come)\s+(on|off|to)?\s*list\s*[ab]|missing\s+player\s+b\s+list)\b`).ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	name, _ := parsePlayerCell(s)
	return name
}
