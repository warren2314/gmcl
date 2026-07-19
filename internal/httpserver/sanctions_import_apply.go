package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type sanctionImportCandidate struct {
	RowID         int64
	RowNumber     int
	MappingStatus string
	CaseID        *int64
	EffectType    string
	EffectStatus  string
	Subject       string
	PublicReason  string
	RuleReference string
	PlayerName    string
	ClubID        *int32
	TeamID        *int32
	OffenceDate   *time.Time
	StartsAt      *time.Time
	EndsAt        *time.Time
	Points        *int
	YellowDelta   int
	RedDelta      int
	Error         string
	Raw           map[string]string
}

type sanctionImportLookup struct {
	clubs        map[string][]importClub
	teamsByParts map[string][]importTeam
	teamsFull    map[string][]importTeam
	clubsByID    map[int32]importClub
	teamsByID    map[int32]importTeam
}

type sanctionImportOverride struct {
	ClubID      *int32 `json:"club_id"`
	TeamID      *int32 `json:"team_id"`
	OffenceDate string `json:"offence_date"`
}

type importClub struct {
	ID   int32
	Name string
}

type importTeam struct {
	ID       int32
	ClubID   int32
	ClubName string
	Name     string
}

var nonNameCharacters = regexp.MustCompile(`[^a-z0-9]+`)
var rulePrefixPattern = regexp.MustCompile(`^\s*(\d+(?:\.\d+)+\.?)`)
var banYearPattern = regexp.MustCompile(`\b(20\d{2}|\d{2})\b`)

func sanctionImportExceptionMapping(message string) string {
	encoded, _ := json.Marshal(map[string]string{"error": message})
	return string(encoded)
}

func normaliseImportName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "&", " and ")
	words := strings.Fields(nonNameCharacters.ReplaceAllString(value, " "))
	kept := words[:0]
	for _, word := range words {
		if word != "cc" && word != "cricket" && word != "club" && word != "lancs" {
			kept = append(kept, word)
		}
	}
	result := strings.Join(kept, " ")
	aliases := map[string]string{
		"bolton deane and derby": "deane and derby",
		"delph and dobcross":     "delph dobcross",
		"flixton c and sc":       "flixton",
		"micklehurst c and sc":   "micklehurst and social",
		"springhead ccc":         "springhead",
		"westleigh leigh":        "westleigh",
		"woodley cheshire":       "woodley",
	}
	if alias, ok := aliases[result]; ok {
		return alias
	}
	return result
}

func parseImportDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"02/01/2006", "2/1/2006", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil && parsed.Year() >= 2000 {
			return &parsed
		}
	}
	return nil
}

func parseImportPoints(value string) *int {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func importRuleReference(reason string) string {
	match := rulePrefixPattern.FindStringSubmatch(reason)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSuffix(match[1], ".")
}

func teamBanDates(value string) (*time.Time, *time.Time) {
	matches := banYearPattern.FindAllString(value, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	years := make([]int, 0, len(matches))
	for _, match := range matches {
		year, _ := strconv.Atoi(match)
		if year < 100 {
			year += 2000
		}
		years = append(years, year)
	}
	sort.Ints(years)
	start := time.Date(years[0], 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(years[len(years)-1], 12, 31, 23, 59, 59, 0, time.UTC)
	return &start, &end
}

func (s *Server) loadSanctionImportLookup(ctx context.Context) (sanctionImportLookup, error) {
	lookup := sanctionImportLookup{clubs: map[string][]importClub{}, teamsByParts: map[string][]importTeam{}, teamsFull: map[string][]importTeam{}, clubsByID: map[int32]importClub{}, teamsByID: map[int32]importTeam{}}
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.name,t.id,t.name FROM clubs c LEFT JOIN teams t ON t.club_id=c.id AND t.active ORDER BY c.id,t.id`)
	if err != nil {
		return lookup, err
	}
	defer rows.Close()
	seenClubs := map[int32]bool{}
	for rows.Next() {
		var clubID int32
		var clubName string
		var teamID *int32
		var teamName *string
		if err = rows.Scan(&clubID, &clubName, &teamID, &teamName); err != nil {
			return lookup, err
		}
		if !seenClubs[clubID] {
			key := normaliseImportName(clubName)
			club := importClub{ID: clubID, Name: clubName}
			lookup.clubs[key] = append(lookup.clubs[key], club)
			lookup.clubsByID[clubID] = club
			seenClubs[clubID] = true
		}
		if teamID != nil && teamName != nil {
			team := importTeam{ID: *teamID, ClubID: clubID, ClubName: clubName, Name: *teamName}
			partsKey := normaliseImportName(clubName) + "|" + normaliseImportName(*teamName)
			lookup.teamsByParts[partsKey] = append(lookup.teamsByParts[partsKey], team)
			lookup.teamsFull[normaliseImportName(clubName+" "+*teamName)] = append(lookup.teamsFull[normaliseImportName(clubName+" "+*teamName)], team)
			lookup.teamsByID[*teamID] = team
		}
	}
	return lookup, rows.Err()
}

func parseSanctionImportCandidate(filename string, rowID int64, rowNumber int, mappingStatus string, caseID *int64, raw map[string]string, lookup sanctionImportLookup) sanctionImportCandidate {
	candidate := sanctionImportCandidate{RowID: rowID, RowNumber: rowNumber, MappingStatus: mappingStatus, CaseID: caseID, Raw: raw, EffectStatus: "active"}
	switch filename {
	case "live-individual-bans.csv":
		clubName := strings.TrimSpace(raw["Club"])
		candidate.PlayerName = strings.TrimSpace(raw["Person's name"])
		candidate.PublicReason = strings.TrimSpace(raw["Summary Description of Offence"])
		candidate.RuleReference = strings.TrimSpace(raw["Offence Level"])
		candidate.OffenceDate = parseImportDate(raw["Date of Offence"])
		candidate.StartsAt = parseImportDate(raw["Starting Date of Ban"])
		candidate.EndsAt = parseImportDate(raw["Date Player can play again"])
		candidate.EffectType = "player_ban"
		candidate.Subject = strings.TrimSpace(clubName + " — " + candidate.PlayerName)
		clubs := lookup.clubs[normaliseImportName(clubName)]
		if len(clubs) == 1 {
			candidate.ClubID = &clubs[0].ID
		} else if len(clubs) == 0 {
			candidate.Error = "club not matched"
		} else {
			candidate.Error = "club match is ambiguous"
		}
		servedEnd := candidate.EndsAt
		suspendedEnd := parseImportDate(raw["Date Suspended Ban ends"])
		if servedEnd != nil && suspendedEnd != nil && servedEnd.Before(time.Now()) && suspendedEnd.After(time.Now()) {
			candidate.EffectStatus = "suspended"
			candidate.StartsAt = servedEnd
			candidate.EndsAt = suspendedEnd
		}
		if candidate.PlayerName == "" || candidate.PublicReason == "" {
			candidate.Error = "player or public reason is blank"
		}
	case "live-team-card-register.csv":
		clubName := strings.TrimSpace(raw["Name of Club"])
		teamName := strings.TrimSpace(raw["Team Standard"])
		card := strings.ToLower(strings.TrimSpace(raw["Card Penalty"]))
		candidate.PublicReason = strings.TrimSpace(raw["Offence"])
		notes := strings.TrimSpace(raw["Notes"])
		if notes != "" && !strings.EqualFold(notes, "none") {
			candidate.PublicReason += " — " + notes
		}
		candidate.RuleReference = importRuleReference(raw["Offence"])
		candidate.OffenceDate = parseImportDate(raw["Date of Breach"])
		candidate.StartsAt = candidate.OffenceDate
		candidate.Points = parseImportPoints(raw["Resulting Points Deduction"])
		candidate.Subject = strings.TrimSpace(clubName + " — " + teamName)
		teams := lookup.teamsByParts[normaliseImportName(clubName)+"|"+normaliseImportName(teamName)]
		if len(teams) == 1 {
			candidate.ClubID = &teams[0].ClubID
			candidate.TeamID = &teams[0].ID
		} else if len(teams) == 0 {
			candidate.Error = "club/team not matched"
		} else {
			candidate.Error = "club/team match is ambiguous"
		}
		if strings.Contains(card, "yellow") {
			candidate.EffectType = "yellow_card"
			candidate.YellowDelta = 1
		} else if strings.Contains(card, "red") {
			if strings.Contains(strings.ToLower(notes), "suspend") {
				candidate.EffectType = "suspended_red"
				candidate.EffectStatus = "suspended"
			} else {
				candidate.EffectType = "red_card"
				candidate.RedDelta = 1
				lowerReason := strings.ToLower(candidate.PublicReason)
				if strings.Contains(lowerReason, "yellow") || strings.Contains(lowerReason, "captain's post-match") {
					candidate.YellowDelta = -2
				}
			}
		} else {
			candidate.Error = "card colour is blank or unsupported"
		}
		if clubName == "" {
			candidate.Error = "blank source row"
		}
	case "live-team-cup-bans.csv":
		fullTeam := strings.TrimSpace(raw["GMCL TEAM NAME"])
		candidate.PublicReason = strings.TrimSpace(raw["column_8"])
		if candidate.PublicReason == "" {
			candidate.PublicReason = strings.TrimSpace(raw["All Bans in place"])
		}
		candidate.StartsAt, candidate.EndsAt = teamBanDates(raw["All Bans in place"])
		candidate.OffenceDate = candidate.StartsAt
		candidate.EffectType = "team_ban"
		candidate.Subject = fullTeam
		teams := lookup.teamsFull[normaliseImportName(fullTeam)]
		if len(teams) == 1 {
			candidate.ClubID = &teams[0].ClubID
			candidate.TeamID = &teams[0].ID
		} else if len(teams) == 0 {
			candidate.Error = "team not matched"
		} else {
			candidate.Error = "team match is ambiguous"
		}
		if fullTeam == "" {
			candidate.Error = "blank source row"
		}
	default:
		candidate.Error = "this source does not support automatic application"
	}
	if candidate.OffenceDate == nil && candidate.Error == "" {
		candidate.Error = "offence/effective date is invalid"
	}
	return candidate
}

func applySanctionImportOverride(candidate sanctionImportCandidate, filename string, override sanctionImportOverride, lookup sanctionImportLookup) sanctionImportCandidate {
	if override.ClubID != nil {
		if club, ok := lookup.clubsByID[*override.ClubID]; ok {
			candidate.ClubID = &club.ID
		}
	}
	if override.TeamID != nil {
		if team, ok := lookup.teamsByID[*override.TeamID]; ok {
			candidate.TeamID = &team.ID
			candidate.ClubID = &team.ClubID
		}
	}
	if parsed := parseImportDate(override.OffenceDate); parsed != nil {
		candidate.OffenceDate = parsed
		if candidate.StartsAt == nil {
			candidate.StartsAt = parsed
		}
	}
	candidate.Error = ""
	switch filename {
	case "live-individual-bans.csv":
		if candidate.ClubID == nil {
			candidate.Error = "club not matched"
		} else if candidate.PlayerName == "" || candidate.PublicReason == "" {
			candidate.Error = "player or public reason is blank"
		}
	case "live-team-card-register.csv":
		if candidate.EffectType == "" {
			candidate.Error = "card colour is blank or unsupported"
		} else if candidate.TeamID == nil {
			candidate.Error = "club/team not matched"
		}
	case "live-team-cup-bans.csv":
		if candidate.Subject == "" {
			candidate.Error = "blank source row"
		} else if candidate.TeamID == nil {
			candidate.Error = "team not matched"
		}
	default:
		candidate.Error = "this source does not support automatic application"
	}
	if candidate.OffenceDate == nil && candidate.Error == "" {
		candidate.Error = "offence/effective date is invalid"
	}
	return candidate
}

func (s *Server) sanctionImportCandidates(ctx context.Context, batchID int64) (string, string, []sanctionImportCandidate, error) {
	var sourceName, filename string
	if err := s.DB.QueryRow(ctx, `SELECT source_name,original_filename FROM sanction_import_batches WHERE id=$1`, batchID).Scan(&sourceName, &filename); err != nil {
		return "", "", nil, err
	}
	lookup, err := s.loadSanctionImportLookup(ctx)
	if err != nil {
		return "", "", nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT r.id,r.row_number,r.raw_data,m.status,m.case_id,m.mapping FROM sanction_import_rows r JOIN sanction_import_mappings m ON m.import_row_id=r.id WHERE r.batch_id=$1 ORDER BY r.row_number`, batchID)
	if err != nil {
		return "", "", nil, err
	}
	defer rows.Close()
	candidates := []sanctionImportCandidate{}
	for rows.Next() {
		var rowID int64
		var rowNumber int
		var rawBytes []byte
		var mappingStatus string
		var caseID *int64
		var mappingBytes []byte
		if err = rows.Scan(&rowID, &rowNumber, &rawBytes, &mappingStatus, &caseID, &mappingBytes); err != nil {
			return "", "", nil, err
		}
		raw := map[string]string{}
		if err = json.Unmarshal(rawBytes, &raw); err != nil {
			return "", "", nil, err
		}
		candidate := parseSanctionImportCandidate(filename, rowID, rowNumber, mappingStatus, caseID, raw, lookup)
		var override sanctionImportOverride
		if len(mappingBytes) > 0 && json.Unmarshal(mappingBytes, &override) == nil && (override.ClubID != nil || override.TeamID != nil || override.OffenceDate != "") {
			candidate = applySanctionImportOverride(candidate, filename, override, lookup)
		}
		candidates = append(candidates, candidate)
	}
	return sourceName, filename, candidates, rows.Err()
}

func (s *Server) handleAdminSanctionImportReview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		batchID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		source, _, candidates, err := s.sanctionImportCandidates(r.Context(), batchID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ready, exceptions, applied := 0, 0, 0
		for _, candidate := range candidates {
			if candidate.MappingStatus == "applied" {
				applied++
			} else if candidate.Error != "" || candidate.MappingStatus == "exception" {
				exceptions++
			} else {
				ready++
			}
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Review sanctions import")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4"><a class="btn btn-sm btn-outline-secondary mb-3" href="/admin/cases/imports">Back to imports</a><div class="d-flex flex-wrap justify-content-between gap-3"><div><h1 class="h2">%s</h1><p class="text-muted">Only deterministic matches can be applied. Source defects and unmatched teams remain visible for correction.</p></div>`, escapeHTML(source))
		if ready > 0 {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/imports/%d/apply" class="w-100 w-sm-auto"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-danger w-100">Apply %d matched rows</button><div class="form-text text-sm-end">Creates published historical records; no notification emails are sent.</div></form>`, batchID, csrf, ready)
		}
		fmt.Fprintf(w, `</div><div class="row g-3 my-2"><div class="col-md-4"><div class="card card-body"><strong>%d ready</strong></div></div><div class="col-md-4"><div class="card card-body"><strong>%d exceptions</strong></div></div><div class="col-md-4"><div class="card card-body"><strong>%d applied</strong></div></div></div>`, ready, exceptions, applied)
		if r.URL.Query().Get("applied") != "" {
			fmt.Fprint(w, `<div class="alert alert-success">Matched rows were applied to the sanctions register. Exceptions were retained for review.</div>`)
		}
		if exceptions > 0 {
			clubs, teams := s.sanctionImportResolutionOptions(r.Context())
			fmt.Fprint(w, `<section class="card mb-4"><div class="card-header"><strong>Resolve import exceptions</strong></div><div class="card-body"><p class="text-muted">The source row remains unchanged. Your selected mapping and reason are stored as a separate immutable event, then the row is re-checked before it can be applied.</p><div class="row g-3">`)
			for _, candidate := range candidates {
				if candidate.MappingStatus == "applied" || (candidate.Error == "" && candidate.MappingStatus != "exception") {
					continue
				}
				fmt.Fprintf(w, `<div class="col-12"><form method="POST" action="/admin/cases/imports/%d/rows/%d/resolve" class="border rounded p-3"><div class="d-flex flex-wrap justify-content-between gap-2 mb-2"><strong>Row %d · %s</strong><span class="badge text-bg-warning">%s</span></div><div class="row g-2">`, batchID, candidate.RowID, candidate.RowNumber, escapeHTML(candidate.Subject), escapeHTML(candidate.Error))
				if candidate.EffectType == "player_ban" {
					fmt.Fprint(w, `<div class="col-md-4"><label class="form-label">Correct club</label><select class="form-select" name="club_id"><option value="">Keep automatic match</option>`)
					for _, club := range clubs {
						selected := ""
						if candidate.ClubID != nil && *candidate.ClubID == club.ID {
							selected = " selected"
						}
						fmt.Fprintf(w, `<option value="%d"%s>%s</option>`, club.ID, selected, escapeHTML(club.Name))
					}
					fmt.Fprint(w, `</select></div>`)
				} else {
					fmt.Fprint(w, `<div class="col-md-4"><label class="form-label">Correct club and team</label><select class="form-select" name="team_id"><option value="">Keep automatic match</option>`)
					for _, team := range teams {
						selected := ""
						if candidate.TeamID != nil && *candidate.TeamID == team.ID {
							selected = " selected"
						}
						fmt.Fprintf(w, `<option value="%d"%s>%s — %s</option>`, team.ID, selected, escapeHTML(team.ClubName), escapeHTML(team.Name))
					}
					fmt.Fprint(w, `</select></div>`)
				}
				dateValue := ""
				if candidate.OffenceDate != nil {
					dateValue = candidate.OffenceDate.Format("2006-01-02")
				}
				fmt.Fprintf(w, `<div class="col-md-3"><label class="form-label">Effective/offence date</label><input class="form-control" type="date" name="offence_date" value="%s"></div><div class="col-md-4"><label class="form-label">Reason for mapping</label><input class="form-control" name="reason" required placeholder="Why is this correction accurate?"></div><div class="col-md-1 d-flex align-items-end"><button class="btn btn-primary w-100">Save</button></div></div><input type="hidden" name="csrf_token" value="%s"></form></div>`, dateValue, csrf)
			}
			fmt.Fprint(w, `</div></div></section>`)
		}
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm responsive-cards align-middle"><thead><tr><th>Row</th><th>Subject</th><th>Effect</th><th>Date</th><th>Reason</th><th>Result</th></tr></thead><tbody>`)
		for _, candidate := range candidates {
			result := `<span class="badge text-bg-success">Ready</span>`
			if candidate.MappingStatus == "applied" {
				result = `<span class="badge text-bg-primary">Applied</span>`
			} else if candidate.Error != "" {
				result = `<span class="badge text-bg-warning">` + escapeHTML(candidate.Error) + `</span>`
			} else if candidate.MappingStatus == "exception" {
				result = `<span class="badge text-bg-warning">Exception</span>`
			}
			date := "—"
			if candidate.OffenceDate != nil {
				date = candidate.OffenceDate.Format("02 Jan 2006")
			}
			fmt.Fprintf(w, `<tr><td data-label="Row">%d</td><td data-label="Subject">%s</td><td data-label="Effect">%s</td><td data-label="Date">%s</td><td data-label="Reason">%s</td><td data-label="Result">%s</td></tr>`, candidate.RowNumber, escapeHTML(candidate.Subject), escapeHTML(effectLabel(candidate.EffectType)), date, escapeHTML(candidate.PublicReason), result)
		}
		fmt.Fprint(w, `</tbody></table></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) sanctionImportResolutionOptions(ctx context.Context) ([]importClub, []importTeam) {
	clubs := []importClub{}
	teams := []importTeam{}
	rows, err := s.DB.Query(ctx, `SELECT c.id,c.name,t.id,t.name FROM clubs c LEFT JOIN teams t ON t.club_id=c.id AND t.active ORDER BY c.name,t.name`)
	if err != nil {
		return clubs, teams
	}
	defer rows.Close()
	seen := map[int32]bool{}
	for rows.Next() {
		var clubID int32
		var clubName string
		var teamID *int32
		var teamName *string
		if rows.Scan(&clubID, &clubName, &teamID, &teamName) != nil {
			continue
		}
		if !seen[clubID] {
			clubs = append(clubs, importClub{ID: clubID, Name: clubName})
			seen[clubID] = true
		}
		if teamID != nil && teamName != nil {
			teams = append(teams, importTeam{ID: *teamID, ClubID: clubID, ClubName: clubName, Name: *teamName})
		}
	}
	return clubs, teams
}

func (s *Server) handleAdminSanctionImportResolve() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		batchID, batchErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		rowID, rowErr := strconv.ParseInt(chi.URLParam(r, "rowID"), 10, 64)
		if batchErr != nil || rowErr != nil || batchID < 1 || rowID < 1 || r.ParseForm() != nil {
			http.Error(w, "invalid import row", 400)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" {
			http.Error(w, "mapping reason is required", 400)
			return
		}
		override := sanctionImportOverride{OffenceDate: strings.TrimSpace(r.FormValue("offence_date"))}
		if value, err := strconv.ParseInt(r.FormValue("club_id"), 10, 32); err == nil && value > 0 {
			clubID := int32(value)
			override.ClubID = &clubID
		}
		if value, err := strconv.ParseInt(r.FormValue("team_id"), 10, 32); err == nil && value > 0 {
			teamID := int32(value)
			override.TeamID = &teamID
		}
		if override.OffenceDate != "" && parseImportDate(override.OffenceDate) == nil {
			http.Error(w, "effective date is invalid", 400)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", 401)
			return
		}
		after, _ := json.Marshal(override)
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "mapping failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var before []byte
		if tx.QueryRow(r.Context(), `SELECT jsonb_build_object('status',m.status,'mapping',m.mapping) FROM sanction_import_mappings m JOIN sanction_import_rows ir ON ir.id=m.import_row_id WHERE m.import_row_id=$1 AND ir.batch_id=$2 AND m.status<>'applied' FOR UPDATE OF m`, rowID, batchID).Scan(&before) != nil {
			http.NotFound(w, r)
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE sanction_import_mappings SET mapping=$2::jsonb,status='pending',reviewed_by_admin_id=$3,reviewed_at=now() WHERE import_row_id=$1`, rowID, string(after), *actor.ID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_import_exceptions SET resolved_by_admin_id=$2,resolution=$3,resolved_at=now() WHERE import_row_id=$1 AND resolved_at IS NULL`, rowID, *actor.ID, reason)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_import_mapping_events(import_row_id,actor_admin_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,$2,$3,$4,$5::jsonb,jsonb_build_object('status','pending','mapping',$6::jsonb),$7)`, rowID, *actor.ID, actor.Label, reason, string(before), string(after), actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "mapping failed", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/imports/%d", batchID), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminSanctionImportApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		batchID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		source, _, candidates, err := s.sanctionImportCandidates(r.Context(), batchID)
		if err != nil {
			http.Error(w, "import batch not found", 404)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", 401)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not apply import", 500)
			return
		}
		defer tx.Rollback(r.Context())
		for _, candidate := range candidates {
			if candidate.MappingStatus != "pending" {
				continue
			}
			if candidate.Error != "" {
				_, err = tx.Exec(r.Context(), `UPDATE sanction_import_mappings SET status='exception',mapping=$2::jsonb,reviewed_by_admin_id=$3,reviewed_at=now() WHERE import_row_id=$1 AND status='pending'`, candidate.RowID, sanctionImportExceptionMapping(candidate.Error), *actor.ID)
				if err == nil {
					_, err = tx.Exec(r.Context(), `INSERT INTO sanction_import_exceptions(import_row_id,exception_type,details) VALUES($1,'mapping',$2)`, candidate.RowID, candidate.Error)
				}
				if err != nil {
					slog.Error("record sanction import exception", "batch_id", batchID, "row_id", candidate.RowID, "row_number", candidate.RowNumber, "error", err)
					http.Error(w, "could not record import exception", 500)
					return
				}
				continue
			}
			if err = applySanctionImportCandidate(r.Context(), tx, batchID, source, candidate, *actor.ID, actor.Label, actor.RequestID); err != nil {
				http.Error(w, "could not apply row "+strconv.Itoa(candidate.RowNumber)+": "+err.Error(), 500)
				return
			}
		}
		var exceptionCount int
		if err = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM sanction_import_mappings m JOIN sanction_import_rows r ON r.id=m.import_row_id WHERE r.batch_id=$1 AND m.status='exception'`, batchID).Scan(&exceptionCount); err != nil {
			http.Error(w, "could not reconcile import", 500)
			return
		}
		status := "applied"
		if exceptionCount > 0 {
			status = "reconciling"
		}
		if _, err = tx.Exec(r.Context(), `UPDATE sanction_import_batches SET status=$2 WHERE id=$1`, batchID, status); err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not finish import", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/imports/%d?applied=1", batchID), http.StatusSeeOther)
	}
}

func applySanctionImportCandidate(ctx context.Context, tx pgx.Tx, batchID int64, source string, candidate sanctionImportCandidate, adminID int32, actorLabel, requestID string) error {
	var seasonID, weekID *int32
	if candidate.OffenceDate != nil {
		var sid, wid int32
		if tx.QueryRow(ctx, `SELECT season_id,id FROM weeks WHERE $1::date BETWEEN start_date AND end_date ORDER BY id DESC LIMIT 1`, *candidate.OffenceDate).Scan(&sid, &wid) == nil {
			seasonID, weekID = &sid, &wid
		} else if tx.QueryRow(ctx, `SELECT id FROM seasons WHERE $1::date BETWEEN start_date AND end_date ORDER BY id DESC LIMIT 1`, *candidate.OffenceDate).Scan(&sid) == nil {
			seasonID = &sid
		}
	}
	var policyID *int64
	if candidate.EffectType == "yellow_card" || candidate.EffectType == "red_card" || candidate.EffectType == "suspended_red" {
		var pid int64
		if candidate.OffenceDate != nil && tx.QueryRow(ctx, `SELECT id FROM sanction_policy_versions WHERE effective_from<=$1::date AND (effective_to IS NULL OR effective_to>=$1::date) ORDER BY effective_from DESC LIMIT 1`, *candidate.OffenceDate).Scan(&pid) == nil {
			policyID = &pid
		}
	}
	effectStatus := candidate.EffectStatus
	publicStatus := "active"
	if effectStatus == "suspended" {
		publicStatus = "suspended"
	}
	if candidate.EndsAt != nil && candidate.EndsAt.Before(time.Now()) {
		effectStatus = "expired"
		publicStatus = "expired"
	}
	privateSummary := fmt.Sprintf("Imported from %s, batch %d row %d. Source values preserved as recorded.", source, batchID, candidate.RowNumber)
	var caseID int64
	var reference string
	err := tx.QueryRow(ctx, `INSERT INTO sanction_cases(source_type,status,public_status,season_id,week_id,club_id,team_id,player_name,match_date,public_summary,private_summary,approved_by_admin_id,approved_at,published_at) VALUES('historical_import','published',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now(),now()) RETURNING id,reference`, publicStatus, seasonID, weekID, candidate.ClubID, candidate.TeamID, nullIfEmptyHTTP(candidate.PlayerName), candidate.OffenceDate, candidate.PublicReason, privateSummary, adminID).Scan(&caseID, &reference)
	if err != nil {
		return err
	}
	var decisionID int64
	err = tx.QueryRow(ctx, `INSERT INTO sanction_decision_revisions(case_id,revision,status,public_reason,private_reason,rule_reference,policy_version_id,approved_by_admin_id,effective_at) VALUES($1,1,'approved',$2,$3,$4,$5,$6,COALESCE($7::date,now())) RETURNING id`, caseID, candidate.PublicReason, privateSummary, nullIfEmptyHTTP(candidate.RuleReference), policyID, adminID, candidate.OffenceDate).Scan(&decisionID)
	if err != nil {
		return err
	}
	subjectType := "case"
	var subjectID any
	if candidate.EffectType == "player_ban" {
		subjectType = "player"
	} else if candidate.TeamID != nil {
		subjectType = "team"
		subjectID = *candidate.TeamID
	} else if candidate.ClubID != nil {
		subjectType = "club"
		subjectID = *candidate.ClubID
	}
	publicDetails, _ := json.Marshal(map[string]any{"historical_import": true, "source_batch_id": batchID, "source_row_number": candidate.RowNumber})
	privateDetails, _ := json.Marshal(map[string]any{"raw_source": candidate.Raw})
	countsForTotting := candidate.EffectType == "yellow_card" || candidate.EffectType == "red_card"
	_, err = tx.Exec(ctx, `INSERT INTO sanction_effect_revisions(decision_revision_id,effect_type,status,subject_type,subject_id,player_name,points,starts_at,ends_at,public_details,private_details,counts_for_totting) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12)`, decisionID, candidate.EffectType, effectStatus, subjectType, subjectID, nullIfEmptyHTTP(candidate.PlayerName), candidate.Points, candidate.StartsAt, candidate.EndsAt, string(publicDetails), string(privateDetails), countsForTotting)
	if err != nil {
		return err
	}
	if countsForTotting && candidate.TeamID != nil && candidate.ClubID != nil && seasonID != nil && (candidate.YellowDelta != 0 || candidate.RedDelta != 0 || (candidate.Points != nil && *candidate.Points != 0)) {
		points := 0
		if candidate.Points != nil {
			points = *candidate.Points
		}
		_, err = tx.Exec(ctx, `INSERT INTO sanction_card_ledger_entries(case_id,decision_revision_id,team_id,club_id,season_id,match_date,yellow_delta,red_delta,points_deduction,entry_type,explanation) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'import',$10)`, caseID, decisionID, *candidate.TeamID, *candidate.ClubID, *seasonID, candidate.OffenceDate, candidate.YellowDelta, candidate.RedDelta, points, "Imported historical outcome: "+candidate.PublicReason)
		if err != nil {
			return err
		}
	}
	after, _ := json.Marshal(map[string]any{"reference": reference, "decision_revision_id": decisionID, "effect_type": candidate.EffectType, "source_batch_id": batchID, "source_row_number": candidate.RowNumber})
	_, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id) VALUES($1,'historical_import_applied','import',$2,$3,$4,$5::jsonb,$6)`, caseID, adminID, actorLabel, "Approved public source imported without recalculation", string(after), requestID)
	if err != nil {
		return err
	}
	mapping, _ := json.Marshal(map[string]any{"club_id": candidate.ClubID, "team_id": candidate.TeamID, "effect_type": candidate.EffectType, "reference": reference})
	_, err = tx.Exec(ctx, `UPDATE sanction_import_mappings SET case_id=$2,mapping=$3::jsonb,status='applied',reviewed_by_admin_id=$4,reviewed_at=now() WHERE import_row_id=$1 AND status='pending'`, candidate.RowID, caseID, string(mapping), adminID)
	return err
}
