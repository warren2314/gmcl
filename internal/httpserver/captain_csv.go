package httpserver

import (
	"context"
	"fmt"
	"strings"
)

type captainCSVLayout struct {
	clubIdx              int
	teamIdx              int
	nameIdx              int
	emailIdx             int
	phoneIdx             int
	firstNameIdx         int
	lastNameIdx          int
	requireExistingTeams bool
}

type captainCSVResolver struct {
	clubsByExactKey     map[string]captainCSVClubRef
	clubsByCanonicalKey map[string][]captainCSVClubRef
	teamsByClubID       map[int32]captainCSVTeamResolver
}

type captainCSVClubRef struct {
	ID   int32
	Name string
}

type captainCSVTeamRef struct {
	ID   int32
	Name string
}

type captainCSVTeamResolver struct {
	byExactKey     map[string]captainCSVTeamRef
	byCanonicalKey map[string][]captainCSVTeamRef
}

func parseCaptainCSVLayout(header []string) (captainCSVLayout, error) {
	indexByHeader := make(map[string]int, len(header))
	for i, h := range header {
		key := normalizeCaptainCSVHeader(h)
		if key == "" {
			return captainCSVLayout{}, fmt.Errorf("unexpected header")
		}
		if _, exists := indexByHeader[key]; exists {
			return captainCSVLayout{}, fmt.Errorf("unexpected header")
		}
		indexByHeader[key] = i
	}

	if len(indexByHeader) == 4 &&
		hasCaptainCSVHeaders(indexByHeader, "club", "team", "captain_name", "captain_email") {
		return captainCSVLayout{
			clubIdx:      indexByHeader["club"],
			teamIdx:      indexByHeader["team"],
			nameIdx:      indexByHeader["captain_name"],
			emailIdx:     indexByHeader["captain_email"],
			phoneIdx:     -1,
			firstNameIdx: -1,
			lastNameIdx:  -1,
		}, nil
	}

	if len(indexByHeader) == 6 &&
		hasCaptainCSVHeaders(indexByHeader, "first_name", "last_name", "email", "mobiletel", "club", "team") {
		return captainCSVLayout{
			clubIdx:      indexByHeader["club"],
			teamIdx:      indexByHeader["team"],
			nameIdx:      -1,
			emailIdx:     indexByHeader["email"],
			phoneIdx:     indexByHeader["mobiletel"],
			firstNameIdx: indexByHeader["first_name"],
			lastNameIdx:  indexByHeader["last_name"],
		}, nil
	}

	const googleFormEmailHeader = "please_confirm_your_email_address_for_us_to_send_reminders_and_links_for_the_captains_report"
	if hasCaptainCSVHeaders(indexByHeader,
		"timestamp",
		"please_confirm_your_name",
		"please_state_your_club",
		"please_state_your_team_grade",
		googleFormEmailHeader,
	) {
		return captainCSVLayout{
			clubIdx:              indexByHeader["please_state_your_club"],
			teamIdx:              indexByHeader["please_state_your_team_grade"],
			nameIdx:              indexByHeader["please_confirm_your_name"],
			emailIdx:             indexByHeader[googleFormEmailHeader],
			phoneIdx:             -1,
			firstNameIdx:         -1,
			lastNameIdx:          -1,
			requireExistingTeams: true,
		}, nil
	}

	return captainCSVLayout{}, fmt.Errorf("unexpected header")
}

func hasCaptainCSVHeaders(indexByHeader map[string]int, keys ...string) bool {
	for _, key := range keys {
		if _, ok := indexByHeader[key]; !ok {
			return false
		}
	}
	return true
}

func normalizeCaptainCSVHeader(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.NewReplacer(" ", "_", "-", "_").Replace(s)
	return s
}

func (l captainCSVLayout) buildRow(record []string) captainCSVRow {
	field := func(idx int) string {
		if idx < 0 || idx >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[idx])
	}

	name := field(l.nameIdx)
	if l.nameIdx < 0 {
		name = strings.TrimSpace(strings.TrimSpace(field(l.firstNameIdx) + " " + field(l.lastNameIdx)))
	}

	return captainCSVRow{
		Club:  field(l.clubIdx),
		Team:  field(l.teamIdx),
		Name:  name,
		Email: strings.ToLower(field(l.emailIdx)),
		Phone: field(l.phoneIdx),
	}
}

func newCaptainCSVResolver(ctx context.Context, s *Server) (*captainCSVResolver, error) {
	resolver := &captainCSVResolver{
		clubsByExactKey:     make(map[string]captainCSVClubRef),
		clubsByCanonicalKey: make(map[string][]captainCSVClubRef),
		teamsByClubID:       make(map[int32]captainCSVTeamResolver),
	}

	clubRows, err := s.DB.Query(ctx, `SELECT id, name FROM clubs`)
	if err != nil {
		return nil, err
	}
	defer clubRows.Close()

	for clubRows.Next() {
		var club captainCSVClubRef
		if err := clubRows.Scan(&club.ID, &club.Name); err != nil {
			return nil, err
		}
		resolver.clubsByExactKey[normalizeCaptainCSVExactKey(club.Name)] = club
		key := normalizeCaptainCSVClubKey(club.Name)
		if key != "" {
			resolver.clubsByCanonicalKey[key] = append(resolver.clubsByCanonicalKey[key], club)
		}
	}
	if err := clubRows.Err(); err != nil {
		return nil, err
	}

	teamRows, err := s.DB.Query(ctx, `SELECT id, club_id, name FROM teams`)
	if err != nil {
		return nil, err
	}
	defer teamRows.Close()

	for teamRows.Next() {
		var (
			team   captainCSVTeamRef
			clubID int32
		)
		if err := teamRows.Scan(&team.ID, &clubID, &team.Name); err != nil {
			return nil, err
		}

		tr := resolver.teamsByClubID[clubID]
		if tr.byExactKey == nil {
			tr.byExactKey = make(map[string]captainCSVTeamRef)
		}
		if tr.byCanonicalKey == nil {
			tr.byCanonicalKey = make(map[string][]captainCSVTeamRef)
		}
		tr.byExactKey[normalizeCaptainCSVExactKey(team.Name)] = team

		key := normalizeCaptainCSVTeamKey(team.Name)
		if key != "" {
			tr.byCanonicalKey[key] = append(tr.byCanonicalKey[key], team)
		}
		resolver.teamsByClubID[clubID] = tr
	}
	if err := teamRows.Err(); err != nil {
		return nil, err
	}

	return resolver, nil
}

func (r *captainCSVResolver) resolveClubAndTeam(clubName, teamName string) (resolvedClub, resolvedTeam string, clubFound, teamFound bool) {
	club, ok := r.resolveClub(clubName)
	if !ok {
		return "", "", false, false
	}

	resolvedClub = club.Name
	clubFound = true

	tr, ok := r.teamsByClubID[club.ID]
	if !ok {
		return resolvedClub, "", true, false
	}

	team, ok := resolveCaptainCSVTeam(tr, teamName)
	if !ok {
		return resolvedClub, "", true, false
	}

	return resolvedClub, team.Name, true, true
}

func (r *captainCSVResolver) resolveClub(clubName string) (captainCSVClubRef, bool) {
	if club, ok := r.clubsByExactKey[normalizeCaptainCSVExactKey(clubName)]; ok {
		return club, true
	}

	matches := r.clubsByCanonicalKey[normalizeCaptainCSVClubKey(clubName)]
	if len(matches) == 1 {
		return matches[0], true
	}

	// Fuzzy fallback: find the DB club whose canonical key is closest to the CSV name.
	// Only accept if distance <= 2 and there's a single best match.
	csvKey := normalizeCaptainCSVClubKey(clubName)
	if csvKey == "" {
		return captainCSVClubRef{}, false
	}
	bestDist := 3
	var bestMatch captainCSVClubRef
	bestCount := 0
	for dbKey, clubs := range r.clubsByCanonicalKey {
		d := levenshtein(csvKey, dbKey)
		if d < bestDist {
			bestDist = d
			bestMatch = clubs[0]
			bestCount = 1
		} else if d == bestDist {
			bestCount++
		}
	}
	if bestDist <= 2 && bestCount == 1 {
		return bestMatch, true
	}

	return captainCSVClubRef{}, false
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	row := make([]int, lb+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= la; i++ {
		prev := i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			val := min3(row[j]+1, prev+1, row[j-1]+cost)
			row[j-1] = prev
			prev = val
		}
		row[lb] = prev
	}
	return row[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func resolveCaptainCSVTeam(tr captainCSVTeamResolver, teamName string) (captainCSVTeamRef, bool) {
	if team, ok := tr.byExactKey[normalizeCaptainCSVExactKey(teamName)]; ok {
		return team, true
	}

	matches := tr.byCanonicalKey[normalizeCaptainCSVTeamKey(teamName)]
	if len(matches) == 1 {
		return matches[0], true
	}

	return captainCSVTeamRef{}, false
}

func normalizeCaptainCSVExactKey(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func normalizeCaptainCSVClubKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Strip trailing ", Location" qualifiers (e.g. "Swinton Moorside CC, Salford")
	if idx := strings.Index(s, ","); idx >= 0 {
		s = s[:idx]
	}
	// Strip apostrophes: straight (U+0027), curly/smart (U+2019), and Windows-1252 byte 0x92
	s = strings.NewReplacer("&", " and ", ".", " ", "'", "", "\u2019", "", "\x92", "", "-", " ").Replace(s)
	s = strings.ReplaceAll(s, "cricket club", " ")
	fields := strings.Fields(s)
	filtered := make([]string, 0, len(fields))
	for _, field := range fields {
		switch field {
		case "cricket", "club", "cc", "ccc",
			// "C&SC" expands to "c and sc"; strip all three so it matches "Cricket & Social Club"
			"c", "sc", "and", "social",
			"lancs", "lancashire",
			"yorks", "yorkshire",
			"cheshire", "derbys", "derbyshire",
			"staffs", "staffordshire",
			"bolton":
			continue
		}
		filtered = append(filtered, field)
	}
	return strings.Join(filtered, " ")
}

func normalizeCaptainCSVTeamKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(".", " ", "-", " ").Replace(s)
	fields := strings.Fields(s)
	for i, field := range fields {
		switch field {
		case "first", "1st":
			fields[i] = "1"
		case "second", "2nd":
			fields[i] = "2"
		case "third", "3rd":
			fields[i] = "3"
		case "fourth", "4th":
			fields[i] = "4"
		case "fifth", "5th":
			fields[i] = "5"
		}
	}
	return strings.Join(fields, "")
}
