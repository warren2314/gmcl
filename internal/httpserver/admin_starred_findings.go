package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/starred"

	"github.com/go-chi/chi/v5"
)

type starredFindingState struct {
	ID     int64
	Status string
}

type starredBreachGroup struct {
	Day      string
	Division string
	Breaches []starred.Breach
}

func parseStarredBreachDateRange(r *http.Request) (from, to *time.Time, err error) {
	parse := func(field, label string) (*time.Time, error) {
		value := strings.TrimSpace(r.URL.Query().Get(field))
		if value == "" {
			return nil, nil
		}
		parsed, parseErr := time.Parse("2006-01-02", value)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid breach %s date", label)
		}
		return &parsed, nil
	}
	if from, err = parse("breach_from", "from"); err != nil {
		return nil, nil, err
	}
	if to, err = parse("breach_to", "to"); err != nil {
		return nil, nil, err
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, fmt.Errorf("breach from date must be on or before the to date")
	}
	return from, to, nil
}

func filterStarredBreachesByDate(breaches []starred.Breach, from, to *time.Time) []starred.Breach {
	if from == nil && to == nil {
		return breaches
	}
	filtered := make([]starred.Breach, 0, len(breaches))
	for _, breach := range breaches {
		date := time.Date(breach.Appearance.MatchDate.Year(), breach.Appearance.MatchDate.Month(), breach.Appearance.MatchDate.Day(), 0, 0, 0, 0, time.UTC)
		if from != nil && date.Before(*from) {
			continue
		}
		if to != nil && date.After(*to) {
			continue
		}
		filtered = append(filtered, breach)
	}
	return filtered
}

func starredFindingStatus(state starredFindingState) string {
	if state.ID == 0 {
		return "Outstanding"
	}
	switch state.Status {
	case "accepted":
		return "Accepted / closed"
	case "draft":
		return "Draft letter"
	case "approved":
		return "Approved / sending"
	case "sent":
		return "Letter sent"
	case "send_failed":
		return "Send failed"
	default:
		return state.Status
	}
}

func writeStarredBreachesCSV(w http.ResponseWriter, year int, breaches []starred.Breach, states map[string]starredFindingState, from, to *time.Time) {
	rangeLabel := "all-dates"
	if from != nil && to != nil {
		rangeLabel = from.Format("2006-01-02") + "-to-" + to.Format("2006-01-02")
	} else if from != nil {
		rangeLabel = "from-" + from.Format("2006-01-02")
	} else if to != nil {
		rangeLabel = "through-" + to.Format("2006-01-02")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="starred-player-breaches-%d-%s.csv"`, year, rangeLabel))
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Match date", "Day", "Division", "Competition", "Format", "Club", "Player", "Published starred name", "Play-Cricket Player ID", "List", "Team", "Evidence", "Review status", "Match ID", "Scorecard"})
	for _, breach := range breaches {
		evidence := "Review"
		if breach.NeedsExemptionReview {
			evidence = "Junior tag - verify exemption"
		}
		_ = writer.Write([]string{
			breach.Appearance.MatchDate.Format("2006-01-02"), starredBreachDay(breach), starredDivisionLabel(breach.Appearance.CompetitionName, breach.Appearance.CompetitionType),
			breach.Appearance.CompetitionName, breach.Appearance.CompetitionType, breach.Appearance.ClubName, breach.Appearance.PlayerName, breach.StarredName,
			strconv.FormatInt(breach.Appearance.PlayerID, 10), "List " + breach.ListType, breach.Appearance.TeamName, evidence,
			starredFindingStatus(states[starredFindingKey(breach)]), strconv.FormatInt(breach.Appearance.MatchID, 10),
			fmt.Sprintf("https://gmcl.co.uk/admin/starred-players?season=%d&view=scorecard&match_id=%d#card-detail", year, breach.Appearance.MatchID),
		})
	}
	writer.Flush()
}

func starredBreachDay(b starred.Breach) string {
	day := strings.TrimSpace(b.Appearance.PlayingDay)
	if day == "" {
		day = b.Appearance.MatchDate.Weekday().String()
	}
	if day == "" {
		return "Other day"
	}
	return strings.ToUpper(day[:1]) + strings.ToLower(day[1:])
}

func starredDivisionLabel(name, competitionType string) string {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	markers := []string{"premier", "championship", "division"}
	for _, marker := range markers {
		if index := strings.Index(lower, marker); index >= 0 {
			label := strings.TrimSpace(name[index:])
			if marker == "premier" {
				label = strings.Replace(label, "Premier League", "Premier", 1)
				label = strings.Replace(label, "Premier league", "Premier", 1)
				if strings.EqualFold(label, "Premier") {
					label = "Premier 1"
				}
			}
			return label
		}
	}
	if name != "" {
		return name
	}
	if strings.TrimSpace(competitionType) != "" {
		return strings.TrimSpace(competitionType)
	}
	return "Other competition"
}

func starredDivisionRank(label string) int {
	lower := strings.ToLower(label)
	numberAfter := func(marker string) int {
		index := strings.Index(lower, marker)
		if index < 0 {
			return 0
		}
		for _, field := range strings.Fields(lower[index+len(marker):]) {
			field = strings.Trim(field, "-–—()")
			if field == "league" {
				continue
			}
			if number, err := strconv.Atoi(field); err == nil {
				return number
			}
			break
		}
		return 0
	}
	switch {
	case strings.Contains(lower, "premier"):
		if number := numberAfter("premier"); number > 0 {
			return number * 10
		}
		return 25
	case strings.Contains(lower, "championship"):
		return 30
	case strings.Contains(lower, "division"):
		if number := numberAfter("division"); number > 0 {
			return 40 + number
		}
		return 70
	case strings.Contains(lower, "cup"):
		return 900
	default:
		return 500
	}
}

func groupStarredBreaches(breaches []starred.Breach) []starredBreachGroup {
	groupsByKey := make(map[string]*starredBreachGroup)
	for _, breach := range breaches {
		day := starredBreachDay(breach)
		division := starredDivisionLabel(breach.Appearance.CompetitionName, breach.Appearance.CompetitionType)
		key := strings.ToLower(day + "\x00" + division)
		group := groupsByKey[key]
		if group == nil {
			group = &starredBreachGroup{Day: day, Division: division}
			groupsByKey[key] = group
		}
		group.Breaches = append(group.Breaches, breach)
	}
	groups := make([]starredBreachGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, *group)
	}
	dayRank := func(day string) int {
		switch strings.ToLower(day) {
		case "saturday":
			return 10
		case "sunday":
			return 20
		default:
			return 30
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if dayRank(groups[i].Day) != dayRank(groups[j].Day) {
			return dayRank(groups[i].Day) < dayRank(groups[j].Day)
		}
		if starredDivisionRank(groups[i].Division) != starredDivisionRank(groups[j].Division) {
			return starredDivisionRank(groups[i].Division) < starredDivisionRank(groups[j].Division)
		}
		return groups[i].Division < groups[j].Division
	})
	return groups
}

func starredFindingActionsHTML(b starred.Breach, state starredFindingState, csrf string, year int) string {
	if state.ID > 0 {
		label, class := "Review", "bg-secondary"
		switch state.Status {
		case "accepted":
			return `<span class="badge bg-success">Accepted / closed</span><div class="small text-muted mt-1">No letter required</div>`
		case "draft":
			label, class = "Draft — review letter", "bg-warning text-dark"
		case "approved":
			label, class = "Approved — sending", "bg-info text-dark"
		case "sent":
			label, class = "Letter sent", "bg-success"
		case "send_failed":
			label, class = "Send failed — retry", "bg-danger"
		}
		return fmt.Sprintf(`<a class="badge %s text-decoration-none" href="/admin/starred-players/findings/%d">%s</a>`, class, state.ID, escapeHTML(label))
	}
	hidden := fmt.Sprintf(`<input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="match_id" value="%d"><input type="hidden" name="player_id" value="%d"><input type="hidden" name="club_key" value="%s"><input type="hidden" name="player_key" value="%s"><input type="hidden" name="list_type" value="%s">`, escapeHTML(csrf), year, b.Appearance.MatchID, b.Appearance.PlayerID, escapeHTML(b.Appearance.ClubKey), escapeHTML(b.Appearance.PlayerKey), escapeHTML(b.ListType))
	return fmt.Sprintf(`<div class="d-flex flex-column gap-1" style="min-width:190px"><form method="post" action="/admin/starred-players/findings/accept" onsubmit="return confirm('Accept and close this finding with no offence letter?')">%s<button class="btn btn-sm btn-outline-success w-100">Accept / close — no offence</button></form><form method="post" action="/admin/starred-players/findings/escalate">%s<button class="btn btn-sm btn-outline-danger w-100">Not accepted — draft letter</button></form></div>`, hidden, hidden)
}

func starredFindingKey(b starred.Breach) string {
	identity := b.Appearance.PlayerKey
	if b.Appearance.PlayerID > 0 {
		identity = "id:" + strconv.FormatInt(b.Appearance.PlayerID, 10)
	}
	raw := fmt.Sprintf("%d|%s|%s|%s", b.Appearance.MatchID, b.Appearance.ClubKey, identity, b.ListType)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Server) loadStarredFindingStates(ctx context.Context, seasonYear int) map[string]starredFindingState {
	states := make(map[string]starredFindingState)
	rows, err := s.DB.Query(ctx, `SELECT finding_key,id,status FROM starred_finding_reviews WHERE season_year=$1`, seasonYear)
	if err != nil {
		return states
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var state starredFindingState
		if rows.Scan(&key, &state.ID, &state.Status) == nil {
			states[key] = state
		}
	}
	return states
}

func (s *Server) verifiedStarredBreach(ctx context.Context, year int, matchID, playerID int64, clubKey, playerKey, listType string) (starred.Breach, error) {
	periods, appearances, mappings, _, err := starred.LoadEvaluationInputs(ctx, s.DB, year)
	if err != nil {
		return starred.Breach{}, err
	}
	appearances = remapStarredAppearanceClubs(appearances, s.loadStarredAppearanceClubOverrides(ctx, year), activeStarredClubNames(periods, starred.ReviewCutoff(year, time.Now())))
	evaluation := starred.Evaluate(periods, appearances, mappings, starred.ReviewCutoff(year, time.Now()))
	for _, breach := range evaluation.Breaches {
		if breach.Appearance.MatchID != matchID || breach.Appearance.ClubKey != clubKey || breach.ListType != listType {
			continue
		}
		if playerID > 0 && breach.Appearance.PlayerID == playerID {
			return breach, nil
		}
		if playerID == 0 && breach.Appearance.PlayerKey == playerKey {
			return breach, nil
		}
	}
	return starred.Breach{}, fmt.Errorf("finding is no longer present in the 31 July compliance evaluation")
}

func parseStarredFindingForm(r *http.Request) (year int, matchID, playerID int64, clubKey, playerKey, listType string, err error) {
	if err = r.ParseForm(); err != nil {
		return
	}
	year, err = strconv.Atoi(strings.TrimSpace(r.FormValue("season")))
	if err != nil || year < 2000 || year > 2100 {
		err = fmt.Errorf("invalid season")
		return
	}
	matchID, err = strconv.ParseInt(strings.TrimSpace(r.FormValue("match_id")), 10, 64)
	if err != nil || matchID <= 0 {
		err = fmt.Errorf("invalid match")
		return
	}
	playerID, _ = strconv.ParseInt(strings.TrimSpace(r.FormValue("player_id")), 10, 64)
	clubKey = strings.TrimSpace(r.FormValue("club_key"))
	playerKey = strings.TrimSpace(r.FormValue("player_key"))
	listType = strings.ToUpper(strings.TrimSpace(r.FormValue("list_type")))
	if clubKey == "" || playerKey == "" || (listType != "A" && listType != "B") {
		err = fmt.Errorf("invalid finding")
	}
	return
}

func (s *Server) handleAdminStarredFindingAccept() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, matchID, playerID, clubKey, playerKey, listType, err := parseStarredFindingForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		breach, err := s.verifiedStarredBreach(ctx, year, matchID, playerID, clubKey, playerKey, listType)
		if err != nil {
			redirectStarredAnchor(w, r, year, "", err.Error(), "potential-breaches")
			return
		}
		key := starredFindingKey(breach)
		adminID := s.resolveAdminID(r)
		note := strings.TrimSpace(r.FormValue("decision_note"))
		var findingID int64
		err = s.DB.QueryRow(ctx, `
			INSERT INTO starred_finding_reviews(finding_key,season_year,play_cricket_match_id,match_date,club_name,club_key,team_name,play_cricket_player_id,player_name,player_key,list_type,status,decision_note,reviewed_by,reviewed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,0),$9,$10,$11,'accepted',NULLIF($12,''),$13,now())
			ON CONFLICT(finding_key) DO UPDATE SET status='accepted',decision_note=EXCLUDED.decision_note,reviewed_by=EXCLUDED.reviewed_by,reviewed_at=now(),updated_at=now()
			WHERE starred_finding_reviews.status <> 'sent'
			RETURNING id`, key, year, matchID, breach.Appearance.MatchDate, breach.Appearance.ClubName, breach.Appearance.ClubKey, breach.Appearance.TeamName, breach.Appearance.PlayerID, breach.Appearance.PlayerName, breach.Appearance.PlayerKey, breach.ListType, note, adminID).Scan(&findingID)
		if err != nil {
			redirectStarredAnchor(w, r, year, "", "Could not close finding: "+err.Error(), "potential-breaches")
			return
		}
		s.audit(ctx, r, "admin", adminID, "starred_finding_accepted", "starred_finding_review", &findingID, map[string]any{"match_id": matchID, "player": breach.Appearance.PlayerName})
		redirectStarredAnchor(w, r, year, "Finding accepted and closed with no letter.", "", "potential-breaches")
	}
}

type starredCaptain struct {
	ID    int32
	Name  string
	Email string
}

func (s *Server) captainForStarredBreach(ctx context.Context, breach starred.Breach) starredCaptain {
	var homeID, awayID, homeClub, awayClub string
	if err := s.DB.QueryRow(ctx, `SELECT COALESCE(home_team_pc_id,''),COALESCE(away_team_pc_id,''),COALESCE(home_club_name,''),COALESCE(away_club_name,'') FROM league_fixtures WHERE play_cricket_match_id=$1`, breach.Appearance.MatchID).Scan(&homeID, &awayID, &homeClub, &awayClub); err != nil {
		return starredCaptain{}
	}
	teamPCID := homeID
	if starred.NormalizeClub(awayClub) == breach.Appearance.ClubKey {
		teamPCID = awayID
	} else if starred.NormalizeClub(homeClub) != breach.Appearance.ClubKey {
		return starredCaptain{}
	}
	var captain starredCaptain
	_ = s.DB.QueryRow(ctx, `
		SELECT c.id,c.full_name,c.email
		FROM teams t JOIN captains c ON c.team_id=t.id
		WHERE TRIM(t.play_cricket_team_id)=TRIM($1)
		  AND c.active_from <= $2::date AND (c.active_to IS NULL OR c.active_to >= $2::date)
		ORDER BY c.active_from DESC,c.id DESC LIMIT 1`, teamPCID, breach.Appearance.MatchDate).Scan(&captain.ID, &captain.Name, &captain.Email)
	return captain
}

func (s *Server) starredScorecardEvidence(ctx context.Context, breach starred.Breach) string {
	appearanceClubKey := breach.Appearance.ClubKey
	if mappedClubKey := s.loadStarredAppearanceClubOverrides(ctx, breach.Appearance.SeasonYear)[breach.Appearance.ClubKey]; mappedClubKey != "" {
		appearanceClubKey = mappedClubKey
	}
	rows, err := s.DB.Query(ctx, `SELECT club_name,team_name,player_name,COALESCE(play_cricket_player_id,0),captain,wicket_keeper FROM starred_appearances WHERE play_cricket_match_id=$1 AND club_key=$2 AND team_name=$3 AND team_level > 0 ORDER BY player_name`, breach.Appearance.MatchID, appearanceClubKey, breach.Appearance.TeamName)
	if err != nil {
		return "Scorecard team sheet unavailable."
	}
	defer rows.Close()
	var b strings.Builder
	currentTeam := ""
	for rows.Next() {
		var club, team, player string
		var playerID int64
		var captain, keeper bool
		if rows.Scan(&club, &team, &player, &playerID, &captain, &keeper) != nil {
			continue
		}
		heading := club + " — " + team
		if heading != currentTeam {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(heading + ":\n")
			currentTeam = heading
		}
		roles := ""
		if captain {
			roles += " captain"
		}
		if keeper {
			roles += " wicketkeeper"
		}
		fmt.Fprintf(&b, "- %s (Play-Cricket ID %d%s)\n", player, playerID, roles)
	}
	return strings.TrimSpace(b.String())
}

func starredFindingDraft(breach starred.Breach, captain starredCaptain, evidence string) (string, string) {
	restriction := "List A players may only play at 1st XI level unless an exemption applies."
	if breach.ListType == "B" {
		restriction = "List B players may not play below 2nd XI level unless an exemption applies."
	}
	subject := fmt.Sprintf("GMCL Rule 3.5 review — %s — match %d", breach.Appearance.ClubName, breach.Appearance.MatchID)
	name := captain.Name
	if strings.TrimSpace(name) == "" {
		name = "Captain"
	}
	body := fmt.Sprintf(`Dear %s,

During the GMCL Rule 3.5 starred-player compliance review, the following potential offence was identified:

Club: %s
Player: %s
Published status: List %s
Match date: %s
Team: %s
Competition: %s
Play-Cricket match ID: %d

Potential offence: %s

Scorecard evidence:
%s

Please review this information and reply with any relevant exemption or correction. No final determination should be inferred beyond the contents of this approved notice.

Regards,
Greater Manchester Cricket League`, name, breach.Appearance.ClubName, breach.Appearance.PlayerName, breach.ListType, breach.Appearance.MatchDate.Format("02 January 2006"), breach.Appearance.TeamName, breach.Appearance.CompetitionName, breach.Appearance.MatchID, restriction, evidence)
	return subject, body
}

func (s *Server) handleAdminStarredFindingEscalate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year, matchID, playerID, clubKey, playerKey, listType, err := parseStarredFindingForm(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		breach, err := s.verifiedStarredBreach(ctx, year, matchID, playerID, clubKey, playerKey, listType)
		if err != nil {
			redirectStarred(w, r, year, "", err.Error())
			return
		}
		captain := s.captainForStarredBreach(ctx, breach)
		subject, body := starredFindingDraft(breach, captain, s.starredScorecardEvidence(ctx, breach))
		adminID := s.resolveAdminID(r)
		var findingID int64
		err = s.DB.QueryRow(ctx, `
			INSERT INTO starred_finding_reviews(finding_key,season_year,play_cricket_match_id,match_date,club_name,club_key,team_name,play_cricket_player_id,player_name,player_key,list_type,status,captain_id,captain_name,captain_email,email_subject,email_body,reviewed_by,reviewed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,0),$9,$10,$11,'draft',NULLIF($12,0),NULLIF($13,''),NULLIF($14,''),$15,$16,$17,now())
			ON CONFLICT(finding_key) DO UPDATE SET status='draft',captain_id=EXCLUDED.captain_id,captain_name=EXCLUDED.captain_name,captain_email=EXCLUDED.captain_email,email_subject=EXCLUDED.email_subject,email_body=EXCLUDED.email_body,reviewed_by=EXCLUDED.reviewed_by,reviewed_at=now(),send_error=NULL,updated_at=now()
			WHERE starred_finding_reviews.status <> 'sent'
			RETURNING id`, starredFindingKey(breach), year, matchID, breach.Appearance.MatchDate, breach.Appearance.ClubName, breach.Appearance.ClubKey, breach.Appearance.TeamName, breach.Appearance.PlayerID, breach.Appearance.PlayerName, breach.Appearance.PlayerKey, breach.ListType, captain.ID, captain.Name, captain.Email, subject, body, adminID).Scan(&findingID)
		if err != nil {
			redirectStarred(w, r, year, "", "Could not create letter draft: "+err.Error())
			return
		}
		s.audit(ctx, r, "admin", adminID, "starred_finding_escalated", "starred_finding_review", &findingID, map[string]any{"match_id": matchID, "captain_email": captain.Email})
		http.Redirect(w, r, fmt.Sprintf("/admin/starred-players/findings/%d", findingID), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminStarredFindingDraft() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid finding", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var year int
		var matchID int64
		var matchDate time.Time
		var club, team, player, listType, status, captainName, captainEmail, subject, body, sendError string
		err = s.DB.QueryRow(ctx, `SELECT season_year,play_cricket_match_id,match_date,club_name,team_name,player_name,list_type,status,COALESCE(captain_name,''),COALESCE(captain_email,''),COALESCE(email_subject,''),COALESCE(email_body,''),COALESCE(send_error,'') FROM starred_finding_reviews WHERE id=$1`, id).Scan(&year, &matchID, &matchDate, &club, &team, &player, &listType, &status, &captainName, &captainEmail, &subject, &body, &sendError)
		if err != nil {
			http.Error(w, "finding not found", http.StatusNotFound)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Starred-player letter")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<div class="container" style="max-width:900px"><div class="d-flex justify-content-between align-items-start mb-4"><div><h4 class="mb-1">Rule 3.5 letter draft</h4><p class="text-muted mb-0">%s — %s — %s — Match %d</p></div><a class="btn btn-outline-secondary btn-sm" href="/admin/starred-players?season=%d#potential-breaches">Back to findings</a></div>`, escapeHTML(club), escapeHTML(player), matchDate.Format("02 Jan 2006"), matchID, year)
		fmt.Fprintf(w, `<div class="card shadow-sm mb-3"><div class="card-header fw-semibold">Evidence</div><div class="card-body"><div class="row g-3"><div class="col-md-3"><div class="small text-muted">List</div><strong>List %s</strong></div><div class="col-md-3"><div class="small text-muted">Team</div><strong>%s</strong></div><div class="col-md-3"><div class="small text-muted">Captain</div><strong>%s</strong></div><div class="col-md-3"><div class="small text-muted">Recipient</div><strong>%s</strong></div></div><a class="btn btn-sm btn-outline-primary mt-3" href="/admin/starred-players?season=%d&amp;view=scorecard&amp;match_id=%d#card-detail">Open scorecard evidence</a></div></div>`, escapeHTML(listType), escapeHTML(team), escapeHTML(captainName), escapeHTML(captainEmail), year, matchID)
		if captainEmail == "" {
			fmt.Fprint(w, `<div class="alert alert-danger">No active captain email could be matched to this team and match date. Update the team/captain record before approving this letter.</div>`)
		}
		if sendError != "" {
			fmt.Fprintf(w, `<div class="alert alert-danger">Previous send failed: %s</div>`, escapeHTML(sendError))
		}
		if status == "sent" {
			fmt.Fprint(w, `<div class="alert alert-success">This approved letter has been sent to the captain. The record is now locked.</div>`)
		}
		fmt.Fprintf(w, `<form method="post" action="/admin/starred-players/findings/%d/approve"><input type="hidden" name="csrf_token" value="%s"><div class="card shadow-sm"><div class="card-header fw-semibold">Editable letter</div><div class="card-body"><div class="mb-3"><label class="form-label fw-semibold">Subject</label><input class="form-control" name="email_subject" value="%s" required></div><div><label class="form-label fw-semibold">Body</label><textarea class="form-control font-monospace" name="email_body" rows="24" required>%s</textarea></div></div><div class="card-footer d-flex justify-content-between align-items-center"><span class="small text-muted">Status: %s. Sending requires this separate approval.</span><button class="btn btn-success" %s onclick="return confirm('Approve this exact letter and send it to %s?')">Approve &amp; Send to Captain</button></div></div></form></div>`, id, escapeHTML(csrf), escapeHTML(subject), escapeHTML(body), escapeHTML(status), disabledIf(captainEmail == "" || (status != "draft" && status != "send_failed")), escapeHTML(captainEmail))
		pageFooter(w)
	}
}

func (s *Server) handleAdminStarredFindingApprove() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid finding", http.StatusBadRequest)
			return
		}
		subject := strings.TrimSpace(r.FormValue("email_subject"))
		body := strings.TrimSpace(r.FormValue("email_body"))
		if subject == "" || body == "" {
			http.Error(w, "subject and body are required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		var year int
		var recipient, status string
		if err := s.DB.QueryRow(ctx, `SELECT season_year,COALESCE(captain_email,''),status FROM starred_finding_reviews WHERE id=$1`, id).Scan(&year, &recipient, &status); err != nil {
			http.Error(w, "finding not found", http.StatusNotFound)
			return
		}
		if status == "sent" {
			http.Redirect(w, r, fmt.Sprintf("/admin/starred-players/findings/%d", id), http.StatusSeeOther)
			return
		}
		if status != "draft" && status != "send_failed" {
			http.Error(w, "this finding is not awaiting letter approval", http.StatusConflict)
			return
		}
		if _, err := mail.ParseAddress(recipient); err != nil {
			http.Error(w, "captain email is missing or invalid", http.StatusBadRequest)
			return
		}
		adminID := s.resolveAdminID(r)
		result, err := s.DB.Exec(ctx, `UPDATE starred_finding_reviews SET email_subject=$1,email_body=$2,status='approved',approved_by=$3,approved_at=now(),send_error=NULL,updated_at=now() WHERE id=$4 AND status IN ('draft','send_failed')`, subject, body, adminID, id)
		if err != nil {
			http.Error(w, "could not approve letter", http.StatusInternalServerError)
			return
		}
		if result.RowsAffected() != 1 {
			http.Error(w, "this letter has already been approved or sent", http.StatusConflict)
			return
		}
		if err := email.NewFromEnv().Send(recipient, subject, body); err != nil {
			_, _ = s.DB.Exec(ctx, `UPDATE starred_finding_reviews SET status='send_failed',send_error=$1,updated_at=now() WHERE id=$2`, err.Error(), id)
			s.audit(ctx, r, "admin", adminID, "starred_finding_send_failed", "starred_finding_review", &id, map[string]any{"recipient": recipient, "error": err.Error()})
			http.Redirect(w, r, fmt.Sprintf("/admin/starred-players/findings/%d", id), http.StatusSeeOther)
			return
		}
		_, _ = s.DB.Exec(ctx, `UPDATE starred_finding_reviews SET status='sent',sent_at=now(),send_error=NULL,updated_at=now() WHERE id=$1`, id)
		s.audit(ctx, r, "admin", adminID, "starred_finding_letter_sent", "starred_finding_review", &id, map[string]any{"recipient": recipient})
		redirectStarredAnchor(w, r, year, "Approved letter sent to "+recipient+".", "", "potential-breaches")
	}
}

func disabledIf(disabled bool) string {
	if disabled {
		return "disabled"
	}
	return ""
}
