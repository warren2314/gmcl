package httpserver

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/starred"
)

type starredPlayerReviewRow struct {
	Division   string
	ClubName   string
	ClubKey    string
	PlayerID   int64
	PlayerName string
	PlayerKey  string
	ListType   string
	Counts     map[int]int
	Total      int
	FirstPct   float64
	RuleGames  int
	RulePct    float64
	Signal     string
}

func starredReviewThresholds(r *http.Request) (green, orange float64) {
	green, orange = 50, 25
	if value, err := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("green")), 64); err == nil && value >= 0 && value <= 100 {
		green = value
	}
	if value, err := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("orange")), 64); err == nil && value >= 0 && value <= 100 {
		orange = value
	}
	if orange > green {
		orange = green
	}
	return
}

func starredRetentionSignal(listType string, firstPct, green, orange float64) string {
	if listType == "" {
		return "neutral"
	}
	if firstPct >= green {
		return "green"
	}
	if firstPct >= orange {
		return "orange"
	}
	return "red"
}

func buildStarredPlayerReviewRows(periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping, cutoff time.Time, clubDivisions map[string]string, selectedDivision, selectedClub string, green, orange float64) []starredPlayerReviewRow {
	activePeriods := make([]starred.Period, 0)
	for _, period := range periods {
		if cutoff.Before(period.ValidFrom) || (period.ValidTo != nil && !cutoff.Before(*period.ValidTo)) {
			continue
		}
		if (selectedDivision == "" || clubDivisions[period.ClubKey] == selectedDivision) && (selectedClub == "" || period.ClubKey == selectedClub) {
			activePeriods = append(activePeriods, period)
		}
	}
	knownIDs := make(map[string]int64)
	for _, appearance := range appearances {
		if appearance.PlayerID > 0 {
			knownIDs[appearance.ClubKey+"|"+appearance.PlayerKey] = appearance.PlayerID
		}
	}
	for _, mapping := range mappings {
		if mapping.PlayerID > 0 {
			knownIDs[mapping.ClubKey+"|"+mapping.StarredPlayerKey] = mapping.PlayerID
		}
	}
	identityKey := func(clubKey, playerKey string, playerID int64) string {
		if playerID == 0 {
			playerID = knownIDs[clubKey+"|"+playerKey]
		}
		if playerID > 0 {
			return clubKey + "|id:" + strconv.FormatInt(playerID, 10)
		}
		return clubKey + "|name:" + playerKey
	}
	rows := make(map[string]*starredPlayerReviewRow)
	seen := make(map[string]struct{})
	for _, appearance := range appearances {
		if appearance.TeamLevel < 1 || (selectedDivision != "" && clubDivisions[appearance.ClubKey] != selectedDivision) || (selectedClub != "" && appearance.ClubKey != selectedClub) {
			continue
		}
		key := identityKey(appearance.ClubKey, appearance.PlayerKey, appearance.PlayerID)
		row := rows[key]
		if row == nil {
			row = &starredPlayerReviewRow{Division: clubDivisions[appearance.ClubKey], ClubName: appearance.ClubName, ClubKey: appearance.ClubKey, PlayerID: appearance.PlayerID, PlayerName: appearance.PlayerName, PlayerKey: appearance.PlayerKey, Counts: make(map[int]int)}
			rows[key] = row
		}
		if row.PlayerID == 0 && appearance.PlayerID > 0 {
			row.PlayerID = appearance.PlayerID
		}
		appearanceKey := key + "|" + strconv.FormatInt(appearance.MatchID, 10) + "|" + strconv.Itoa(appearance.TeamLevel)
		if _, duplicate := seen[appearanceKey]; duplicate {
			continue
		}
		seen[appearanceKey] = struct{}{}
		row.Counts[appearance.TeamLevel]++
		row.Total++
	}
	for _, period := range activePeriods {
		mappedID := knownIDs[period.ClubKey+"|"+period.PlayerKey]
		key := identityKey(period.ClubKey, period.PlayerKey, mappedID)
		row := rows[key]
		if row == nil {
			row = &starredPlayerReviewRow{Division: clubDivisions[period.ClubKey], ClubName: period.ClubName, ClubKey: period.ClubKey, PlayerID: mappedID, PlayerName: period.PlayerName, PlayerKey: period.PlayerKey, Counts: make(map[int]int)}
			rows[key] = row
		}
		row.ListType = period.ListType
	}
	out := make([]starredPlayerReviewRow, 0, len(rows))
	for _, row := range rows {
		if row.Total > 0 {
			row.FirstPct = float64(row.Counts[1]) * 100 / float64(row.Total)
			row.RuleGames = row.Counts[1]
			if row.ListType == "B" {
				row.RuleGames += row.Counts[2]
			}
			row.RulePct = float64(row.RuleGames) * 100 / float64(row.Total)
		}
		row.Signal = starredRetentionSignal(row.ListType, row.RulePct, green, orange)
		out = append(out, *row)
	}
	signalRank := map[string]int{"red": 0, "orange": 1, "green": 2, "neutral": 3}
	sort.Slice(out, func(i, j int) bool {
		if starredDivisionRank(out[i].Division) != starredDivisionRank(out[j].Division) {
			return starredDivisionRank(out[i].Division) < starredDivisionRank(out[j].Division)
		}
		if out[i].ClubName != out[j].ClubName {
			return out[i].ClubName < out[j].ClubName
		}
		if signalRank[out[i].Signal] != signalRank[out[j].Signal] {
			return signalRank[out[i].Signal] < signalRank[out[j].Signal]
		}
		return out[i].PlayerName < out[j].PlayerName
	})
	return out
}

func (s *Server) starredReviewData(ctx context.Context, year int, cutoff time.Time, periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping, r *http.Request) ([]starredSaturdayDivision, map[string]string, []starredPlayerReviewRow, float64, float64) {
	clubNames := make(map[string]string)
	for _, period := range periods {
		if !cutoff.Before(period.ValidFrom) && (period.ValidTo == nil || cutoff.Before(*period.ValidTo)) {
			clubNames[period.ClubKey] = period.ClubName
		}
	}
	divisions := saturdayStarredClubDivisions(clubNames, appearances, s.loadStarredDivisionOverrides(ctx, year))
	clubDivisions := make(map[string]string)
	for _, division := range divisions {
		for _, clubKey := range division.Clubs {
			clubDivisions[clubKey] = division.Label
		}
	}
	green, orange := starredReviewThresholds(r)
	rows := buildStarredPlayerReviewRows(periods, appearances, mappings, cutoff, clubDivisions, strings.TrimSpace(r.URL.Query().Get("division")), strings.TrimSpace(r.URL.Query().Get("club")), green, orange)
	return divisions, clubNames, rows, green, orange
}

func signalBadge(signal string) (string, string) {
	switch signal {
	case "green":
		return "bg-success", "Retain"
	case "orange":
		return "bg-warning text-dark", "Monitor"
	case "red":
		return "bg-danger", "Removal review"
	default:
		return "bg-secondary", "Not starred"
	}
}

func (s *Server) renderStarredPlayerReview(w http.ResponseWriter, ctx context.Context, year int, cutoff time.Time, periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping, r *http.Request) {
	divisions, clubNames, rows, green, orange := s.starredReviewData(ctx, year, cutoff, periods, appearances, mappings, r)
	selectedDivision := strings.TrimSpace(r.URL.Query().Get("division"))
	selectedClub := strings.TrimSpace(r.URL.Query().Get("club"))
	fmt.Fprintf(w, `Player list review%s</span><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d">Close</a></div>`,
		starredHelpIcon("Player list review", "Every open-age player in the selected division or club, with their appearances split by team. Rule games are the games at the level the player's list permits (List A: 1st XI only; List B: 1st + 2nd XI), and Rule % is that share of all their games. Green suggests keeping the player starred, orange means monitor, red suggests a removal review — adjust the thresholds to suit, nothing is changed automatically."), year)
	fmt.Fprintf(w, `<div class="card-body border-bottom"><form method="get" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><input type="hidden" name="view" value="player-review"><div class="col-md-3"><label class="form-label fw-semibold">Saturday division</label><select class="form-select" name="division" required onchange="this.form.elements.club.value='';this.form.submit()"><option value="">Choose division…</option>`, year)
	for _, division := range divisions {
		selected := ""
		if division.Label == selectedDivision {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(division.Label), selected, escapeHTML(division.Label))
	}
	fmt.Fprint(w, `</select></div><div class="col-md-3"><label class="form-label fw-semibold">Club</label><select class="form-select" name="club"><option value="">All clubs in division</option>`)
	for _, division := range divisions {
		if division.Label != selectedDivision {
			continue
		}
		for _, clubKey := range division.Clubs {
			selected := ""
			if clubKey == selectedClub {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(clubKey), selected, escapeHTML(clubNames[clubKey]))
		}
	}
	fmt.Fprintf(w, `</select></div><div class="col-md-2"><label class="form-label">Green at/above rule %%</label><input class="form-control" type="number" min="0" max="100" step="0.1" name="green" value="%.1f"></div><div class="col-md-2"><label class="form-label">Orange at/above rule %%</label><input class="form-control" type="number" min="0" max="100" step="0.1" name="orange" value="%.1f"></div><div class="col-auto"><button class="btn btn-primary">Run review</button></div></form><div class="form-text">Counts all classified open-age XI appearances: List A uses the 1st XI percentage; List B uses the combined 1st XI and 2nd XI percentage. Green means retain, orange means monitor, and red means removal review. No list is changed automatically.</div></div>`, green, orange)
	if selectedDivision == "" {
		fmt.Fprint(w, `<div class="card-body text-center text-muted py-4">Choose a Saturday division to review all of its clubs and players.</div></div>`)
		return
	}
	greenCount, orangeCount, redCount := 0, 0, 0
	for _, row := range rows {
		switch row.Signal {
		case "green":
			greenCount++
		case "orange":
			orangeCount++
		case "red":
			redCount++
		}
	}
	exportQuery := url.Values{"season": {strconv.Itoa(year)}, "view": {"player-review"}, "division": {selectedDivision}, "green": {strconv.FormatFloat(green, 'f', 1, 64)}, "orange": {strconv.FormatFloat(orange, 'f', 1, 64)}, "export": {"csv"}}
	if selectedClub != "" {
		exportQuery.Set("club", selectedClub)
	}
	fmt.Fprintf(w, `<div class="card-body border-bottom d-flex flex-wrap justify-content-between align-items-center gap-2"><div><span class="badge bg-success me-1">Retain %d</span><span class="badge bg-warning text-dark me-1">Monitor %d</span><span class="badge bg-danger">Removal review %d</span></div><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?%s">Export CSV</a></div>`, greenCount, orangeCount, redCount, escapeHTML(exportQuery.Encode()))
	fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Club</th><th>Player</th><th>Current list</th><th>Status</th>`)
	for level := 1; level <= 6; level++ {
		fmt.Fprintf(w, `<th class="text-center text-nowrap">%s</th>`, starredTeamLevelLabel(level))
	}
	fmt.Fprint(w, `<th class="text-center">Total</th><th class="text-center">Rule games</th><th class="text-center">Rule %</th><th>Action</th></tr></thead><tbody>`)
	for _, row := range rows {
		badgeClass, badgeLabel := signalBadge(row.Signal)
		listLabel := "—"
		if row.ListType != "" {
			listLabel = "List " + row.ListType
		}
		appearanceSearch := starredAppearanceSearch(row.PlayerName, row.PlayerID)
		fmt.Fprintf(w, `<tr><td>%s</td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td><td>%s</td><td><span class="badge %s">%s</span></td>`, escapeHTML(row.ClubName), year, url.QueryEscape(appearanceSearch), escapeHTML(row.PlayerName), escapeHTML(listLabel), badgeClass, badgeLabel)
		for level := 1; level <= 6; level++ {
			percentage := float64(0)
			if row.Total > 0 {
				percentage = float64(row.Counts[level]) * 100 / float64(row.Total)
			}
			fmt.Fprintf(w, `<td class="text-center text-nowrap">%d <span class="text-muted small">(%.0f%%)</span></td>`, row.Counts[level], percentage)
		}
		action := ""
		if row.ListType != "" {
			q := url.Values{"season": {strconv.Itoa(year)}, "club_key": {row.ClubKey}, "player_key": {row.PlayerKey}, "green": {strconv.FormatFloat(green, 'f', 1, 64)}, "orange": {strconv.FormatFloat(orange, 'f', 1, 64)}}
			action = fmt.Sprintf(`<a class="btn btn-sm btn-outline-danger text-nowrap" href="/admin/starred-player-replacements/new?%s">Request replacement</a>`, escapeHTML(q.Encode()))
		}
		fmt.Fprintf(w, `<td class="text-center fw-semibold">%d</td><td class="text-center fw-semibold">%d</td><td class="text-center fw-semibold">%.1f%%</td><td>%s</td></tr>`, row.Total, row.RuleGames, row.RulePct, action)
	}
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="14" class="text-center text-muted py-3">No classified open-age XI appearances were found for this selection.</td></tr>`)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func (s *Server) writeStarredPlayerReviewCSV(w http.ResponseWriter, ctx context.Context, year int, cutoff time.Time, periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping, r *http.Request) {
	_, _, rows, green, orange := s.starredReviewData(ctx, year, cutoff, periods, appearances, mappings, r)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="starred-player-review-%d.csv"`, year))
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Division", "Club", "Player", "Play-Cricket Player ID", "Current List", "Review Signal", "1st XI Games", "1st XI %", "2nd XI Games", "2nd XI %", "3rd XI Games", "3rd XI %", "4th XI Games", "4th XI %", "5th XI Games", "5th XI %", "6th XI Games", "6th XI %", "Total Games", "Rule Games", "Rule %", "Green Threshold", "Orange Threshold"})
	for _, row := range rows {
		_, signal := signalBadge(row.Signal)
		record := []string{row.Division, row.ClubName, row.PlayerName, strconv.FormatInt(row.PlayerID, 10), row.ListType, signal}
		for level := 1; level <= 6; level++ {
			percentage := float64(0)
			if row.Total > 0 {
				percentage = float64(row.Counts[level]) * 100 / float64(row.Total)
			}
			record = append(record, strconv.Itoa(row.Counts[level]), strconv.FormatFloat(percentage, 'f', 1, 64))
		}
		record = append(record, strconv.Itoa(row.Total), strconv.Itoa(row.RuleGames), strconv.FormatFloat(row.RulePct, 'f', 1, 64), strconv.FormatFloat(green, 'f', 1, 64), strconv.FormatFloat(orange, 'f', 1, 64))
		_ = writer.Write(record)
	}
	writer.Flush()
}
