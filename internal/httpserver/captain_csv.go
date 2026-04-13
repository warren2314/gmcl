package httpserver

import (
	"context"
	"fmt"
	"strings"
)

type captainCSVLayout struct {
	clubIdx      int
	teamIdx      int
	nameIdx      int
	emailIdx     int
	firstNameIdx int
	lastNameIdx  int
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
			firstNameIdx: indexByHeader["first_name"],
			lastNameIdx:  indexByHeader["last_name"],
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

	return captainCSVClubRef{}, false
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
	s = strings.NewReplacer("&", " and ", ".", " ", ",", " ", "'", "", "-", " ").Replace(s)
	s = strings.ReplaceAll(s, "cricket club", " ")
	fields := strings.Fields(s)
	filtered := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "cricket" || field == "club" || field == "cc" {
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
