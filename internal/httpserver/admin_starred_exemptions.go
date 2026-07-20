package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/starred"

	"github.com/go-chi/chi/v5"
)

const starredExemptionGuidanceURL = "https://www.gtrmcrcricket.co.uk/pages/exemption-requests"

type starredExemption struct {
	ID            int64
	SeasonYear    int
	ClubKey       string
	ClubName      string
	PlayerID      int64
	PlayerKey     string
	PlayerName    string
	MatchID       int64
	ExemptionType string
	Status        string
	ValidFrom     time.Time
	ValidTo       *time.Time
	WicketKeeper  bool
	Notes         string
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

func starredSundayExemptionEligible(breach starred.Breach) bool {
	if !strings.EqualFold(starredBreachDay(breach), "Sunday") || !strings.EqualFold(breach.Appearance.CompetitionType, "League") {
		return false
	}
	scope := strings.ToLower(breach.Appearance.CompetitionName + " " + breach.Appearance.CompetitionType)
	return !strings.Contains(scope, "cup") && !strings.Contains(scope, "gmcl20") && !strings.Contains(scope, "t20")
}

func sameStarredExemptionIdentity(exemption starredExemption, breach starred.Breach) bool {
	if exemption.ClubKey != breach.Appearance.ClubKey {
		return false
	}
	if exemption.PlayerID > 0 {
		return breach.Appearance.PlayerID == exemption.PlayerID
	}
	return exemption.PlayerKey != "" && exemption.PlayerKey == breach.Appearance.PlayerKey
}

func (exemption starredExemption) covers(breach starred.Breach) bool {
	if exemption.Status != "approved" || exemption.RevokedAt != nil || !starredSundayExemptionEligible(breach) || !sameStarredExemptionIdentity(exemption, breach) {
		return false
	}
	date := breach.Appearance.MatchDate
	switch exemption.ExemptionType {
	case "sunday_single_match":
		if exemption.MatchID > 0 {
			return exemption.MatchID == breach.Appearance.MatchID
		}
		return sameStarredCalendarDate(exemption.ValidFrom, date)
	case "sunday_development":
		if date.Before(exemption.ValidFrom) {
			return false
		}
		return exemption.ValidTo == nil || !date.After(*exemption.ValidTo)
	default:
		return false
	}
}

func sameStarredCalendarDate(left, right time.Time) bool {
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}

func starredExemptionForBreach(breach starred.Breach, exemptions []starredExemption) *starredExemption {
	for index := range exemptions {
		if exemptions[index].covers(breach) {
			return &exemptions[index]
		}
	}
	return nil
}

func filterStarredBreachesWithoutApprovedExemption(breaches []starred.Breach, exemptions []starredExemption) []starred.Breach {
	out := make([]starred.Breach, 0, len(breaches))
	for _, breach := range breaches {
		if starredExemptionForBreach(breach, exemptions) == nil {
			out = append(out, breach)
		}
	}
	return out
}

func (s *Server) loadStarredExemptions(ctx context.Context, year int) []starredExemption {
	rows, err := s.DB.Query(ctx, `
		SELECT id,season_year,club_key,COALESCE(club_name,club_key),COALESCE(play_cricket_player_id,0),COALESCE(player_key,''),COALESCE(player_name,player_key,''),
		       COALESCE(play_cricket_match_id,0),exemption_type,status,valid_from,valid_to,wicket_keeper,COALESCE(notes,''),created_at,revoked_at
		FROM starred_exemptions WHERE season_year=$1 ORDER BY created_at DESC,id DESC`, year)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]starredExemption, 0)
	for rows.Next() {
		var exemption starredExemption
		if rows.Scan(&exemption.ID, &exemption.SeasonYear, &exemption.ClubKey, &exemption.ClubName, &exemption.PlayerID, &exemption.PlayerKey, &exemption.PlayerName,
			&exemption.MatchID, &exemption.ExemptionType, &exemption.Status, &exemption.ValidFrom, &exemption.ValidTo, &exemption.WicketKeeper, &exemption.Notes, &exemption.CreatedAt, &exemption.RevokedAt) == nil {
			out = append(out, exemption)
		}
	}
	return out
}

func starredExemptionTypeLabel(value string) string {
	if value == "sunday_development" {
		return "Development (season-long)"
	}
	return "Single Sunday match"
}

func starredExemptionStatusBadge(status string) string {
	switch status {
	case "approved":
		return `<span class="badge bg-success">Approved</span>`
	case "refused":
		return `<span class="badge bg-danger">Refused</span>`
	case "revoked":
		return `<span class="badge bg-secondary">Revoked</span>`
	default:
		return `<span class="badge bg-warning text-dark">Pending</span>`
	}
}

func starredExemptionRequestURL(breach starred.Breach, year int) string {
	if !starredSundayExemptionEligible(breach) {
		return ""
	}
	q := url.Values{
		"season":                {strconv.Itoa(year)},
		"exemption_club_key":    {breach.Appearance.ClubKey},
		"exemption_club_name":   {breach.Appearance.ClubName},
		"exemption_player_id":   {strconv.FormatInt(breach.Appearance.PlayerID, 10)},
		"exemption_player_name": {breach.Appearance.PlayerName},
		"exemption_match_id":    {strconv.FormatInt(breach.Appearance.MatchID, 10)},
		"exemption_date":        {breach.Appearance.MatchDate.Format("2006-01-02")},
	}
	return "/admin/starred-players?" + q.Encode() + "#sunday-exemptions"
}

func (s *Server) renderStarredExemptions(w http.ResponseWriter, year int, periods []starred.Period, r *http.Request, csrf string, exemptions []starredExemption) {
	clubNames := make(map[string]string)
	for _, period := range periods {
		if period.ClubKey != "" && period.ClubName != "" {
			clubNames[period.ClubKey] = period.ClubName
		}
	}
	clubKeys := make([]string, 0, len(clubNames))
	for key := range clubNames {
		clubKeys = append(clubKeys, key)
	}
	sort.Slice(clubKeys, func(i, j int) bool { return clubNames[clubKeys[i]] < clubNames[clubKeys[j]] })

	pending, approved := 0, 0
	for _, exemption := range exemptions {
		switch exemption.Status {
		case "pending":
			pending++
		case "approved":
			approved++
		}
	}
	fmt.Fprintf(w, `<div id="sunday-exemptions" class="card shadow-sm mb-4"><div class="card-header">%s</div>`,
		starredSectionTitle("", "Sunday exemption requests", "Record and decide Sunday player-eligibility exemption requests. Only approved exemptions remove covered Sunday league findings; Saturday, Cup and GMCL20 findings are never suppressed.",
			"Sunday exemption rules", "Clubs may make up to four one-match applications and two development applications per season. Requests should arrive by 8pm on the preceding Thursday. Wicketkeepers may receive special consideration. Matchday use without prior approval remains subject to notification and playing restrictions."))
	fmt.Fprintf(w, `<div class="card-body border-bottom"><div class="row g-3"><div class="col-lg-8"><h6>Rules captured by this workflow</h6><ul class="small mb-2"><li>Sunday league only; never Saturday league, Cup or GMCL20.</li><li>Maximum four single-match applications and two season-long development applications per club.</li><li>Requests are due by 8pm on the Thursday before the Sunday game.</li><li>For an unexpected matchday shortage, the selected List B player may not bowl, may not bat above seven, and may not bat before an under-18 player.</li><li>Wicketkeeper applications can be marked for special consideration.</li></ul><a href="%s" target="_blank" rel="noopener">Open the published exemption guidance</a></div><div class="col-lg-4"><div class="row g-2"><div class="col-6"><div class="border rounded p-3 text-center"><div class="fs-4 fw-semibold">%d</div><div class="small text-muted">Pending</div></div></div><div class="col-6"><div class="border rounded p-3 text-center"><div class="fs-4 fw-semibold">%d</div><div class="small text-muted">Approved</div></div></div></div></div></div></div>`, starredExemptionGuidanceURL, pending, approved)

	prefillClub := strings.TrimSpace(r.URL.Query().Get("exemption_club_key"))
	prefillDate := strings.TrimSpace(r.URL.Query().Get("exemption_date"))
	if prefillDate == "" {
		prefillDate = time.Now().Format("2006-01-02")
	}
	fmt.Fprintf(w, `<div class="card-body border-bottom"><h6 class="mb-3">Record an exemption request</h6><form method="post" action="/admin/starred-players/exemptions" class="row g-3"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><div class="col-md-4"><label class="form-label">Club</label><select class="form-select" name="club_key" required><option value="">Choose club…</option>`, escapeHTML(csrf), year)
	for _, key := range clubKeys {
		selected := ""
		if key == prefillClub {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(key), selected, escapeHTML(clubNames[key]))
	}
	fmt.Fprintf(w, `</select></div><div class="col-md-4"><label class="form-label">Registered player</label><input class="form-control" name="player_name" value="%s" required></div><div class="col-md-4"><label class="form-label">Play-Cricket player ID</label><input class="form-control" type="number" min="1" name="player_id" value="%s" placeholder="Recommended"></div><div class="col-md-4"><label class="form-label">Request type</label><select class="form-select" name="exemption_type"><option value="sunday_single_match">Single Sunday match (max 4)</option><option value="sunday_development">Development, date range (max 2)</option></select></div><div class="col-md-4"><label class="form-label">Sunday match date / valid from</label><input class="form-control" type="date" name="valid_from" value="%s" required></div><div class="col-md-4"><label class="form-label">Valid to (development only)</label><input class="form-control" type="date" name="valid_to"></div><div class="col-md-4"><label class="form-label">Play-Cricket match ID</label><input class="form-control" type="number" min="1" name="match_id" value="%s" placeholder="For a single-match request"></div><div class="col-md-4"><label class="form-label">Initial status</label><select class="form-select" name="status"><option value="pending">Pending review</option><option value="approved">Approved</option><option value="refused">Refused</option></select></div><div class="col-md-4 d-flex align-items-end"><div class="form-check mb-2"><input class="form-check-input" type="checkbox" value="1" name="wicket_keeper" id="exemption-wk"><label class="form-check-label" for="exemption-wk">Wicketkeeper — special consideration</label></div></div><div class="col-12"><label class="form-label">Reason and unavailable players</label><textarea class="form-control" name="notes" rows="3" required placeholder="Record the shortage, unavailable eligible players, correspondence and any conditions imposed."></textarea></div><div class="col-12"><button class="btn btn-primary">Save exemption request</button></div></form></div>`, escapeHTML(r.URL.Query().Get("exemption_player_name")), escapeHTML(r.URL.Query().Get("exemption_player_id")), escapeHTML(prefillDate), escapeHTML(r.URL.Query().Get("exemption_match_id")))

	fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Club / player</th><th>Type</th><th>Scope</th><th>Notes</th><th>Status</th><th>Decision</th></tr></thead><tbody>`)
	if len(exemptions) == 0 {
		fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No exemption requests have been recorded for this season.</td></tr>`)
	}
	for _, exemption := range exemptions {
		scope := exemption.ValidFrom.Format("02 Jan 2006")
		if exemption.ExemptionType == "sunday_development" && exemption.ValidTo != nil {
			scope += " – " + exemption.ValidTo.Format("02 Jan 2006")
		} else if exemption.MatchID > 0 {
			scope += fmt.Sprintf(" · match %d", exemption.MatchID)
		}
		flags := ""
		if exemption.WicketKeeper {
			flags = `<div class="small text-info">Wicketkeeper consideration</div>`
		}
		actions := `<span class="text-muted">—</span>`
		if exemption.Status != "revoked" {
			actions = fmt.Sprintf(`<div class="d-flex flex-wrap gap-1"><form method="post" action="/admin/starred-players/exemptions/%d/status"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="status" value="approved"><button class="btn btn-sm btn-outline-success">Approve</button></form><form method="post" action="/admin/starred-players/exemptions/%d/status"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="status" value="refused"><button class="btn btn-sm btn-outline-danger">Refuse</button></form><form method="post" action="/admin/starred-players/exemptions/%d/status" onsubmit="return confirm('Revoke this exemption? Covered findings will return to the outstanding queue.')"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="status" value="revoked"><button class="btn btn-sm btn-outline-secondary">Revoke</button></form></div>`, exemption.ID, escapeHTML(csrf), year, exemption.ID, escapeHTML(csrf), year, exemption.ID, escapeHTML(csrf), year)
		}
		fmt.Fprintf(w, `<tr><td><strong>%s</strong><div>%s</div><div class="small text-muted">ID %d</div></td><td>%s%s</td><td>%s</td><td class="small" style="max-width:320px">%s</td><td>%s</td><td>%s</td></tr>`, escapeHTML(exemption.ClubName), escapeHTML(exemption.PlayerName), exemption.PlayerID, escapeHTML(starredExemptionTypeLabel(exemption.ExemptionType)), flags, escapeHTML(scope), escapeHTML(exemption.Notes), starredExemptionStatusBadge(exemption.Status), actions)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func (s *Server) handleAdminStarredExemptionCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		year := starredSeasonYear(r)
		exemptionType := strings.TrimSpace(r.FormValue("exemption_type"))
		limit := 4
		if exemptionType == "sunday_development" {
			limit = 2
		} else if exemptionType != "sunday_single_match" {
			redirectStarredAnchor(w, r, year, "", "Invalid exemption type.", "sunday-exemptions")
			return
		}
		clubKey := strings.TrimSpace(r.FormValue("club_key"))
		playerName := strings.TrimSpace(r.FormValue("player_name"))
		playerKey := starred.NormalizeName(playerName)
		playerID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("player_id")), 10, 64)
		matchID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("match_id")), 10, 64)
		validFrom, err := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue("valid_from")))
		if err != nil || clubKey == "" || playerName == "" || (playerID <= 0 && playerKey == "") {
			redirectStarredAnchor(w, r, year, "", "Club, player and a valid start date are required.", "sunday-exemptions")
			return
		}
		var validTo *time.Time
		if value := strings.TrimSpace(r.FormValue("valid_to")); value != "" {
			parsed, parseErr := time.Parse("2006-01-02", value)
			if parseErr != nil || parsed.Before(validFrom) {
				redirectStarredAnchor(w, r, year, "", "The valid-to date must be on or after the valid-from date.", "sunday-exemptions")
				return
			}
			validTo = &parsed
		}
		if exemptionType == "sunday_single_match" {
			validTo = &validFrom
		} else if validTo == nil {
			end := time.Date(year, time.August, 31, 0, 0, 0, 0, time.UTC)
			validTo = &end
		}
		if validFrom.Year() != year || (validTo != nil && validTo.Year() != year) {
			redirectStarredAnchor(w, r, year, "", "Exemption dates must be within the selected season.", "sunday-exemptions")
			return
		}
		status := strings.TrimSpace(r.FormValue("status"))
		if status != "pending" && status != "approved" && status != "refused" {
			status = "pending"
		}
		notes := strings.TrimSpace(r.FormValue("notes"))
		if notes == "" {
			redirectStarredAnchor(w, r, year, "", "Record the reason and unavailable players before saving.", "sunday-exemptions")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var used int
		if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM starred_exemptions WHERE season_year=$1 AND club_key=$2 AND exemption_type=$3 AND status <> 'revoked' AND revoked_at IS NULL`, year, clubKey, exemptionType).Scan(&used); err != nil {
			redirectStarredAnchor(w, r, year, "", "Could not check exemption allocation: "+err.Error(), "sunday-exemptions")
			return
		}
		if used >= limit {
			redirectStarredAnchor(w, r, year, "", fmt.Sprintf("This club has already used its %d %s applications for %d.", limit, strings.ToLower(starredExemptionTypeLabel(exemptionType)), year), "sunday-exemptions")
			return
		}
		clubName := clubKey
		_ = s.DB.QueryRow(ctx, `SELECT club_name FROM starred_periods WHERE season_year=$1 AND club_key=$2 ORDER BY valid_from DESC LIMIT 1`, year, clubKey).Scan(&clubName)
		adminID := s.resolveAdminID(r)
		var exemptionID int64
		err = s.DB.QueryRow(ctx, `
			INSERT INTO starred_exemptions(season_year,club_key,club_name,play_cricket_player_id,player_key,player_name,play_cricket_match_id,exemption_type,status,valid_from,valid_to,wicket_keeper,notes,created_by,decided_by,decided_at)
			VALUES($1,$2,$3,NULLIF($4,0),$5,$6,NULLIF($7,0),$8,$9,$10,$11,$12,$13,$14,CASE WHEN $9 IN ('approved','refused') THEN $14 ELSE NULL END,CASE WHEN $9 IN ('approved','refused') THEN now() ELSE NULL END)
			RETURNING id`, year, clubKey, clubName, playerID, playerKey, playerName, matchID, exemptionType, status, validFrom, validTo, r.FormValue("wicket_keeper") == "1", notes, adminID).Scan(&exemptionID)
		if err != nil {
			redirectStarredAnchor(w, r, year, "", "Could not save exemption request: "+err.Error(), "sunday-exemptions")
			return
		}
		s.audit(ctx, r, "admin", adminID, "starred_exemption_created", "starred_exemption", &exemptionID, map[string]any{"club": clubName, "player": playerName, "status": status, "type": exemptionType, "match_id": matchID})
		redirectStarredAnchor(w, r, year, "Sunday exemption request recorded for "+playerName+".", "", "sunday-exemptions")
	}
}

func (s *Server) handleAdminStarredExemptionStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		year := starredSeasonYear(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		status := strings.TrimSpace(r.FormValue("status"))
		if err != nil || id <= 0 || (status != "approved" && status != "refused" && status != "revoked") {
			http.Error(w, "invalid exemption decision", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		adminID := s.resolveAdminID(r)
		var resultErr error
		var rowsAffected int64
		if status == "revoked" {
			result, execErr := s.DB.Exec(ctx, `UPDATE starred_exemptions SET status='revoked',revoked_by=$1,revoked_at=now(),updated_at=now() WHERE id=$2 AND season_year=$3`, adminID, id, year)
			resultErr = execErr
			if execErr == nil {
				rowsAffected = result.RowsAffected()
			}
		} else {
			result, execErr := s.DB.Exec(ctx, `UPDATE starred_exemptions SET status=$1,decided_by=$2,decided_at=now(),revoked_by=NULL,revoked_at=NULL,updated_at=now() WHERE id=$3 AND season_year=$4`, status, adminID, id, year)
			resultErr = execErr
			if execErr == nil {
				rowsAffected = result.RowsAffected()
			}
		}
		if resultErr != nil {
			redirectStarredAnchor(w, r, year, "", "Could not update exemption: "+resultErr.Error(), "sunday-exemptions")
			return
		}
		if rowsAffected != 1 {
			redirectStarredAnchor(w, r, year, "", "Exemption request not found.", "sunday-exemptions")
			return
		}
		s.audit(ctx, r, "admin", adminID, "starred_exemption_"+status, "starred_exemption", &id, map[string]any{"season": year, "status": status})
		redirectStarredAnchor(w, r, year, "Exemption marked "+status+".", "", "sunday-exemptions")
	}
}
