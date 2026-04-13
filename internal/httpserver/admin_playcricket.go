package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/leagueapi"
	"cricket-ground-feedback/internal/middleware"
)

type playCricketMappingSuggestion struct {
	PlayCricketTeamID string
	FixtureClub       string
	FixtureTeam       string
	LocalTeamID       int32
	LocalClub         string
	LocalTeam         string
	ExistingPCID      string
	Reason            string
	Status            string
	Appearances       int32
	UmpireRows        int32
	LastMatchDate     time.Time
}

type playCricketMappingPageData struct {
	FixtureCount       int
	DistinctFixtureIDs int
	MappedTeams        int
	LastFetchedAt      *time.Time
	Suggestions        []playCricketMappingSuggestion
	ReadyCount         int
	AlreadyMappedCount int
	ConflictCount      int
	UnmatchedCount     int
}

type playCricketResolver struct {
	csvResolver *captainCSVResolver
	localByID   map[int32]playCricketLocalTeamRef
	localByPCID map[string]playCricketLocalTeamRef
}

type playCricketLocalTeamRef struct {
	ID        int32
	ClubName  string
	TeamName  string
	CurrentID string
}

func (s *Server) handleAdminPlayCricketGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		data, err := s.buildPlayCricketMappingPageData(ctx)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.renderAdminPlayCricketPage(w, r, data, "", "")
	}
}

func (s *Server) handleAdminPlayCricketSync() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		matchDate := strings.TrimSpace(r.FormValue("match_date"))
		rawBody := strings.TrimSpace(r.FormValue("raw_body"))
		var seasonID *int32
		if seasonRaw := strings.TrimSpace(r.FormValue("season_id")); seasonRaw != "" {
			parsed, err := strconv.ParseInt(seasonRaw, 10, 32)
			if err != nil {
				data, _ := s.buildPlayCricketMappingPageData(ctx)
				s.renderAdminPlayCricketPage(w, r, data, "", "Season ID must be a number.")
				return
			}
			v := int32(parsed)
			seasonID = &v
		}

		var details []leagueapi.MatchDetail
		if rawBody != "" {
			parsed, err := leagueapi.ParseMatchDetailsJSON([]byte(rawBody))
			if err != nil {
				data, _ := s.buildPlayCricketMappingPageData(ctx)
				s.renderAdminPlayCricketPage(w, r, data, "", "Raw JSON could not be parsed: "+err.Error())
				return
			}
			details = parsed.MatchDetails
		} else {
			if matchDate == "" {
				data, _ := s.buildPlayCricketMappingPageData(ctx)
				s.renderAdminPlayCricketPage(w, r, data, "", "Match date is required when raw JSON is blank.")
				return
			}
			md, err := time.Parse("2006-01-02", matchDate)
			if err != nil {
				data, _ := s.buildPlayCricketMappingPageData(ctx)
				s.renderAdminPlayCricketPage(w, r, data, "", "Match date must use YYYY-MM-DD.")
				return
			}
			client := leagueapi.NewClient(leagueapi.NewConfigFromEnv())
			fetched, err := client.FetchMatchesForDate(ctx, md)
			if err != nil {
				data, _ := s.buildPlayCricketMappingPageData(ctx)
				s.renderAdminPlayCricketPage(w, r, data, "", "Fixture sync failed: "+err.Error())
				return
			}
			details = fetched
		}

		if err := leagueapi.UpsertFixtureBatch(ctx, s.DB, seasonID, details); err != nil {
			data, _ := s.buildPlayCricketMappingPageData(ctx)
			s.renderAdminPlayCricketPage(w, r, data, "", "Fixture sync failed: "+err.Error())
			return
		}

		data, err := s.buildPlayCricketMappingPageData(ctx)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.audit(ctx, r, "admin", nil, "play_cricket_sync", "league_fixture", nil, map[string]any{
			"count":      len(details),
			"match_date": matchDate,
			"season_id":  seasonID,
		})
		s.renderAdminPlayCricketPage(w, r, data, fmt.Sprintf("Synced %d fixture rows.", len(details)), "")
	}
}

func (s *Server) handleAdminPlayCricketMappingApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		data, err := s.buildPlayCricketMappingPageData(ctx)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}

		applied := 0
		for _, suggestion := range data.Suggestions {
			if suggestion.Status != "ready" {
				continue
			}
			if _, err := s.DB.Exec(ctx, `
				UPDATE teams
				SET play_cricket_team_id = $1
				WHERE id = $2
				  AND (play_cricket_team_id IS NULL OR TRIM(play_cricket_team_id) = '')
			`, suggestion.PlayCricketTeamID, suggestion.LocalTeamID); err != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}
			applied++
		}

		data, err = s.buildPlayCricketMappingPageData(ctx)
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		s.audit(ctx, r, "admin", nil, "play_cricket_mapping_apply", "team", nil, map[string]any{
			"applied": applied,
		})
		s.renderAdminPlayCricketPage(w, r, data, fmt.Sprintf("Applied %d team mappings.", applied), "")
	}
}

func (s *Server) renderAdminPlayCricketPage(w http.ResponseWriter, r *http.Request, data playCricketMappingPageData, successMsg, errorMsg string) {
	csrfToken := ""
	if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
		csrfToken = c.Value
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHead(w, "Play-Cricket")
	writeAdminNav(w, csrfToken, r.URL.Path)

	fmt.Fprint(w, `<div class="container-fluid">
<div class="d-flex align-items-center justify-content-between mb-4">
  <div>
    <h3 class="mb-1">Play-Cricket Sync</h3>
    <p class="text-muted mb-0">Sync fixtures, bulk-map team IDs, and keep umpire prefill up to date for Saturday and Sunday reports.</p>
  </div>
</div>
`)
	if successMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(successMsg))
	}
	if errorMsg != "" {
		fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(errorMsg))
	}

	lastFetched := "Never"
	if data.LastFetchedAt != nil {
		lastFetched = data.LastFetchedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04 MST")
	}

	fmt.Fprintf(w, `<div class="row g-4 mb-4">
  <div class="col-lg-5">
    <div class="card card-gmcl shadow-sm h-100">
      <div class="card-body">
        <h5 class="card-title">Sync Fixtures</h5>
        <form method="POST" action="/admin/play-cricket/sync">
          <input type="hidden" name="csrf_token" value="%s">
          <div class="mb-3">
            <label class="form-label">Match date</label>
            <input type="date" class="form-control" name="match_date" value="%s">
          </div>
          <div class="mb-3">
            <label class="form-label">Season ID (optional)</label>
            <input type="number" class="form-control" name="season_id" min="1">
          </div>
          <div class="mb-3">
            <label class="form-label">Raw JSON (optional)</label>
            <textarea class="form-control" name="raw_body" rows="8" placeholder='Paste Play-Cricket JSON here if you want to sync a full API response directly.'></textarea>
            <div class="form-text">Leave raw JSON blank to fetch by date using the configured API credentials. The parser accepts both <code>match_details</code> and <code>matches</code>.</div>
          </div>
          <button type="submit" class="btn btn-primary">Sync now</button>
        </form>
      </div>
    </div>
  </div>
  <div class="col-lg-7">
    <div class="card card-gmcl shadow-sm h-100">
      <div class="card-body">
        <h5 class="card-title">Status</h5>
        <div class="row row-cols-2 row-cols-xl-4 g-3 mb-3">
          <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Fixture rows</div><div class="fs-4 fw-semibold">%d</div></div></div>
          <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Distinct team IDs</div><div class="fs-4 fw-semibold">%d</div></div></div>
          <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Mapped local teams</div><div class="fs-4 fw-semibold">%d</div></div></div>
          <div class="col"><div class="border rounded p-3 bg-light"><div class="text-muted small">Ready to apply</div><div class="fs-4 fw-semibold">%d</div></div></div>
        </div>
        <p class="mb-2"><strong>Last sync:</strong> %s</p>
        <p class="mb-2"><strong>Recommended automation:</strong> run Saturday at 08:00 and 10:30, then Sunday at 08:00 and 10:30.</p>
        <p class="text-muted mb-0">Where Play-Cricket has no umpire assignments, the captain form remains editable and they can type names in manually.</p>
      </div>
    </div>
  </div>
</div>
`, escapeHTML(csrfToken), time.Now().In(s.LondonLoc).Format("2006-01-02"), data.FixtureCount, data.DistinctFixtureIDs, data.MappedTeams, data.ReadyCount, escapeHTML(lastFetched))

	fmt.Fprintf(w, `<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-body d-flex align-items-center justify-content-between gap-3 flex-wrap">
    <div>
      <h5 class="card-title mb-1">Bulk Team Mapping</h5>
      <p class="text-muted mb-0">Safe auto-matches only. Existing non-empty local mappings are left alone unless they already match.</p>
    </div>
    <form method="POST" action="/admin/play-cricket/team-mapping/apply">
      <input type="hidden" name="csrf_token" value="%s">
      <button type="submit" class="btn btn-primary"%s>Apply %d ready mappings</button>
    </form>
  </div>
</div>
`, escapeHTML(csrfToken), disabledAttr(data.ReadyCount == 0), data.ReadyCount)

	fmt.Fprintf(w, `<div class="row g-3 mb-4">
  <div class="col-md-3"><div class="border rounded p-3 bg-success-subtle"><div class="text-muted small">Ready</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col-md-3"><div class="border rounded p-3 bg-secondary-subtle"><div class="text-muted small">Already mapped</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col-md-3"><div class="border rounded p-3 bg-warning-subtle"><div class="text-muted small">Conflicts</div><div class="fs-4 fw-semibold">%d</div></div></div>
  <div class="col-md-3"><div class="border rounded p-3 bg-danger-subtle"><div class="text-muted small">Unmatched</div><div class="fs-4 fw-semibold">%d</div></div></div>
</div>
`, data.ReadyCount, data.AlreadyMappedCount, data.ConflictCount, data.UnmatchedCount)

	fmt.Fprint(w, `<div class="card card-gmcl shadow-sm">
  <div class="table-responsive">
    <table class="table table-hover table-striped table-gmcl mb-0">
      <thead>
        <tr>
          <th>Status</th>
          <th>Play-Cricket</th>
          <th>Fixture Team</th>
          <th>Local Team</th>
          <th>Fixtures</th>
          <th>Umpire Rows</th>
          <th>Last Match</th>
          <th>Notes</th>
        </tr>
      </thead>
      <tbody>
`)
	if len(data.Suggestions) == 0 {
		fmt.Fprint(w, `<tr><td colspan="8" class="text-center text-muted py-4">No cached fixtures yet. Run a sync first.</td></tr>`)
	} else {
		for _, suggestion := range data.Suggestions {
			localLabel := "No local match"
			if suggestion.LocalTeamID > 0 {
				localLabel = escapeHTML(suggestion.LocalClub + " - " + suggestion.LocalTeam)
			}
			note := suggestion.Reason
			if suggestion.ExistingPCID != "" && suggestion.ExistingPCID != suggestion.PlayCricketTeamID {
				note = strings.TrimSpace(note + " Existing local ID: " + suggestion.ExistingPCID + ".")
			}
			fmt.Fprintf(w, `<tr>
  <td>%s</td>
  <td><code>%s</code></td>
  <td>%s - %s</td>
  <td>%s</td>
  <td>%d</td>
  <td>%d</td>
  <td>%s</td>
  <td>%s</td>
</tr>
`,
				playCricketStatusBadge(suggestion.Status),
				escapeHTML(suggestion.PlayCricketTeamID),
				escapeHTML(suggestion.FixtureClub),
				escapeHTML(suggestion.FixtureTeam),
				localLabel,
				suggestion.Appearances,
				suggestion.UmpireRows,
				escapeHTML(suggestion.LastMatchDate.Format("2006-01-02")),
				escapeHTML(note),
			)
		}
	}
	fmt.Fprint(w, `      </tbody>
    </table>
  </div>
</div>
</div>
`)
	pageFooter(w)
}

func (s *Server) buildPlayCricketMappingPageData(ctx context.Context) (playCricketMappingPageData, error) {
	var data playCricketMappingPageData

	if err := s.DB.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(DISTINCT play_cricket_match_id),
			MAX(fetched_at)
		FROM league_fixtures
	`).Scan(&data.FixtureCount, &data.DistinctFixtureIDs, &data.LastFetchedAt); err != nil {
		return data, err
	}
	if err := s.DB.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM teams
		WHERE play_cricket_team_id IS NOT NULL AND TRIM(play_cricket_team_id) <> ''
	`).Scan(&data.MappedTeams); err != nil {
		return data, err
	}

	resolver, err := newPlayCricketResolver(ctx, s)
	if err != nil {
		return data, err
	}

	rows, err := s.DB.Query(ctx, `
		WITH fixture_teams AS (
			SELECT
				TRIM(home_team_pc_id) AS pc_id,
				TRIM(home_club_name) AS club_name,
				TRIM(home_team_name) AS team_name,
				match_date,
				CASE
					WHEN TRIM(COALESCE(umpire_1_name, '')) <> '' OR TRIM(COALESCE(umpire_2_name, '')) <> '' THEN 1
					ELSE 0
				END AS has_umpires
			FROM league_fixtures
			WHERE TRIM(COALESCE(home_team_pc_id, '')) <> ''
			UNION ALL
			SELECT
				TRIM(away_team_pc_id) AS pc_id,
				TRIM(away_club_name) AS club_name,
				TRIM(away_team_name) AS team_name,
				match_date,
				CASE
					WHEN TRIM(COALESCE(umpire_1_name, '')) <> '' OR TRIM(COALESCE(umpire_2_name, '')) <> '' THEN 1
					ELSE 0
				END AS has_umpires
			FROM league_fixtures
			WHERE TRIM(COALESCE(away_team_pc_id, '')) <> ''
		)
		SELECT
			pc_id,
			club_name,
			team_name,
			COUNT(*)::int AS appearances,
			COALESCE(SUM(has_umpires), 0)::int AS umpire_rows,
			MAX(match_date) AS last_match_date
		FROM fixture_teams
		GROUP BY pc_id, club_name, team_name
		ORDER BY club_name, team_name, pc_id
	`)
	if err != nil {
		return data, err
	}
	defer rows.Close()

	for rows.Next() {
		var suggestion playCricketMappingSuggestion
		if err := rows.Scan(
			&suggestion.PlayCricketTeamID,
			&suggestion.FixtureClub,
			&suggestion.FixtureTeam,
			&suggestion.Appearances,
			&suggestion.UmpireRows,
			&suggestion.LastMatchDate,
		); err != nil {
			return data, err
		}

		local, ok := resolver.resolve(suggestion.FixtureClub, suggestion.FixtureTeam)
		if !ok {
			suggestion.Status = "unmatched"
			suggestion.Reason = "No unique local club/team match was found."
			data.UnmatchedCount++
			data.Suggestions = append(data.Suggestions, suggestion)
			continue
		}

		suggestion.LocalTeamID = local.ID
		suggestion.LocalClub = local.ClubName
		suggestion.LocalTeam = local.TeamName
		suggestion.ExistingPCID = local.CurrentID

		if local.CurrentID == suggestion.PlayCricketTeamID {
			suggestion.Status = "already_mapped"
			suggestion.Reason = "Local team already has this Play-Cricket ID."
			data.AlreadyMappedCount++
		} else if local.CurrentID != "" {
			suggestion.Status = "conflict"
			suggestion.Reason = "Local team already has a different Play-Cricket ID."
			data.ConflictCount++
		} else if existing, taken := resolver.localByPCID[suggestion.PlayCricketTeamID]; taken && existing.ID != local.ID {
			suggestion.Status = "conflict"
			suggestion.Reason = "This Play-Cricket ID is already linked to another local team."
			data.ConflictCount++
		} else {
			suggestion.Status = "ready"
			suggestion.Reason = "Exact or normalized match."
			data.ReadyCount++
		}

		data.Suggestions = append(data.Suggestions, suggestion)
	}
	if err := rows.Err(); err != nil {
		return data, err
	}

	sort.SliceStable(data.Suggestions, func(i, j int) bool {
		left := playCricketStatusOrder(data.Suggestions[i].Status)
		right := playCricketStatusOrder(data.Suggestions[j].Status)
		if left != right {
			return left < right
		}
		if data.Suggestions[i].FixtureClub != data.Suggestions[j].FixtureClub {
			return data.Suggestions[i].FixtureClub < data.Suggestions[j].FixtureClub
		}
		if data.Suggestions[i].FixtureTeam != data.Suggestions[j].FixtureTeam {
			return data.Suggestions[i].FixtureTeam < data.Suggestions[j].FixtureTeam
		}
		return data.Suggestions[i].PlayCricketTeamID < data.Suggestions[j].PlayCricketTeamID
	})

	return data, nil
}

func newPlayCricketResolver(ctx context.Context, s *Server) (*playCricketResolver, error) {
	csvResolver, err := newCaptainCSVResolver(ctx, s)
	if err != nil {
		return nil, err
	}
	resolver := &playCricketResolver{
		csvResolver: csvResolver,
		localByID:   make(map[int32]playCricketLocalTeamRef),
		localByPCID: make(map[string]playCricketLocalTeamRef),
	}

	rows, err := s.DB.Query(ctx, `
		SELECT t.id, cl.name, t.name, COALESCE(t.play_cricket_team_id, '')
		FROM teams t
		JOIN clubs cl ON cl.id = t.club_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var team playCricketLocalTeamRef
		if err := rows.Scan(&team.ID, &team.ClubName, &team.TeamName, &team.CurrentID); err != nil {
			return nil, err
		}
		team.CurrentID = strings.TrimSpace(team.CurrentID)
		resolver.localByID[team.ID] = team
		if team.CurrentID != "" {
			resolver.localByPCID[team.CurrentID] = team
		}
	}
	return resolver, rows.Err()
}

func (r *playCricketResolver) resolve(clubName, teamName string) (playCricketLocalTeamRef, bool) {
	club, ok := r.csvResolver.resolveClub(clubName)
	if !ok {
		return playCricketLocalTeamRef{}, false
	}

	tr, ok := r.csvResolver.teamsByClubID[club.ID]
	if !ok {
		return playCricketLocalTeamRef{}, false
	}

	candidates := playCricketTeamVariants(teamName, clubName)
	foundID := int32(0)
	for _, candidate := range candidates {
		team, ok := resolveCaptainCSVTeam(tr, candidate)
		if !ok {
			continue
		}
		if foundID != 0 && foundID != team.ID {
			return playCricketLocalTeamRef{}, false
		}
		foundID = team.ID
	}
	if foundID == 0 {
		return playCricketLocalTeamRef{}, false
	}
	team, ok := r.localByID[foundID]
	return team, ok
}

func playCricketTeamVariants(teamName, clubName string) []string {
	variants := []string{strings.TrimSpace(teamName)}

	if idx := strings.LastIndex(teamName, " - "); idx >= 0 && idx+3 < len(teamName) {
		variants = append(variants, strings.TrimSpace(teamName[idx+3:]))
	}

	teamExact := normalizeCaptainCSVExactKey(teamName)
	clubExact := normalizeCaptainCSVExactKey(clubName)
	if clubExact != "" {
		for _, prefix := range []string{clubExact + " ", clubExact + " - ", clubExact + "-"} {
			if strings.HasPrefix(teamExact, prefix) {
				variants = append(variants, strings.TrimSpace(teamExact[len(prefix):]))
			}
		}
	}

	seen := make(map[string]struct{}, len(variants))
	out := make([]string, 0, len(variants))
	for _, variant := range variants {
		variant = strings.TrimSpace(variant)
		if variant == "" {
			continue
		}
		key := normalizeCaptainCSVExactKey(variant)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, variant)
	}
	return out
}

func playCricketStatusOrder(status string) int {
	switch status {
	case "ready":
		return 0
	case "already_mapped":
		return 1
	case "conflict":
		return 2
	default:
		return 3
	}
}

func playCricketStatusBadge(status string) string {
	switch status {
	case "ready":
		return `<span class="badge text-bg-success">Ready</span>`
	case "already_mapped":
		return `<span class="badge text-bg-secondary">Already mapped</span>`
	case "conflict":
		return `<span class="badge text-bg-warning">Conflict</span>`
	default:
		return `<span class="badge text-bg-danger">Unmatched</span>`
	}
}

func disabledAttr(disabled bool) string {
	if disabled {
		return " disabled"
	}
	return ""
}

func (s *Server) writePlayCricketMappingJSONPreview(w http.ResponseWriter, data playCricketMappingPageData) {
	_ = json.NewEncoder(w).Encode(data)
}
