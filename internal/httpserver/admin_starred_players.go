package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/leagueapi"
	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/starred"
)

func starredSeasonYear(r *http.Request) int {
	if y, err := strconv.Atoi(strings.TrimSpace(r.FormValue("season"))); err == nil && y >= 2000 && y <= 2100 {
		return y
	}
	if y, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("season"))); err == nil && y >= 2000 && y <= 2100 {
		return y
	}
	return time.Now().Year()
}

func (s *Server) starredSeasonStart(ctx context.Context, year int) time.Time {
	var start time.Time
	if err := s.DB.QueryRow(ctx, `SELECT start_date FROM seasons WHERE EXTRACT(YEAR FROM start_date)::int=$1 ORDER BY start_date LIMIT 1`, year).Scan(&start); err == nil {
		return start
	}
	return time.Date(year, time.April, 1, 0, 0, 0, 0, time.UTC)
}

func (s *Server) handleAdminStarredPlayersGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year := starredSeasonYear(r)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		cutoff := starred.ReviewCutoff(year, time.Now())
		periods, apps, mappings, issues, loadErr := starred.LoadEvaluationInputs(ctx, s.DB, year)
		clubStatuses, _ := starred.LoadClubStatuses(ctx, s.DB, year, cutoff)
		reviewApps := make([]starred.Appearance, 0, len(apps))
		for _, appearance := range apps {
			if appearance.TeamLevel > 0 && !appearance.MatchDate.After(cutoff) && !starred.IsWomensAppearance(appearance) {
				reviewApps = append(reviewApps, appearance)
			}
		}
		eval := starred.Evaluation{}
		var suggestions []starred.MappingSuggestion
		if loadErr == nil {
			eval = starred.Evaluate(periods, reviewApps, mappings, cutoff)
			suggestions = starred.SuggestMappings(periods, reviewApps, mappings, cutoff)
		}
		findingStates := s.loadStarredFindingStates(ctx, year)
		currentA, currentB := 0, 0
		now := cutoff
		for _, p := range periods {
			if !now.Before(p.ValidFrom) && (p.ValidTo == nil || now.Before(*p.ValidTo)) {
				if p.ListType == "A" {
					currentA++
				} else {
					currentB++
				}
			}
		}
		matchCount := 0
		_ = s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM starred_match_imports sm WHERE sm.season_year=$1 AND sm.match_date <= $2::date AND EXISTS (SELECT 1 FROM starred_appearances sa WHERE sa.play_cricket_match_id=sm.play_cricket_match_id AND sa.team_level > 0) AND NOT EXISTS (SELECT 1 FROM starred_appearances sa WHERE sa.play_cricket_match_id=sm.play_cricket_match_id AND CONCAT_WS(' ',sa.competition_name,sa.club_name,sa.team_name) ~* '(wom(en|an)|ladies|female|girls)')`, year, cutoff).Scan(&matchCount)
		pendingCount, _ := starred.PendingMatchCount(ctx, s.DB, year)
		clubIssueCount := 0
		for _, status := range clubStatuses {
			if !status.Compliant {
				clubIssueCount++
			}
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Starred Players")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<div class="container-fluid">
<div class="d-flex flex-wrap justify-content-between align-items-center mb-4 gap-2"><div><h3 class="mb-1">Starred Player Compliance</h3><p class="text-muted mb-0">Rule 3.5 review from the published lists and Play-Cricket team sheets through 30 June.</p></div>
<div class="d-flex flex-wrap gap-2"><a class="btn btn-primary" href="/admin/starred-players?season=%d&amp;view=club-list#card-detail">Starred list by club</a><form method="get" class="d-flex gap-2"><input class="form-control" style="width:110px" type="number" name="season" value="%d"><button class="btn btn-outline-primary">Load</button></form></div></div>`, year, year)
		if msg := r.URL.Query().Get("message"); msg != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(msg))
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(errMsg))
		}
		if loadErr != nil {
			fmt.Fprintf(w, `<div class="alert alert-warning">No imported starred list is available for %d yet. Sync the published list below.</div>`, year)
		}
		fmt.Fprintf(w, `<div class="row g-3 mb-4">
<div class="col-md-2"><a class="text-decoration-none text-reset" href="/admin/starred-players?season=%d&amp;view=list-a#card-detail"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">List A at cutoff</div><div class="display-6">%d</div><div class="small text-primary">View players &rarr;</div></div></div></a></div>
<div class="col-md-2"><a class="text-decoration-none text-reset" href="/admin/starred-players?season=%d&amp;view=list-b#card-detail"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">List B at cutoff</div><div class="display-6">%d</div><div class="small text-primary">View players &rarr;</div></div></div></a></div>
<div class="col-md-2"><a class="text-decoration-none text-reset" href="/admin/starred-players?season=%d&amp;view=scorecards#card-detail"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Scorecards</div><div class="display-6">%d</div><div class="small text-muted">%d pending</div><div class="small text-primary">View scorecards &rarr;</div></div></div></a></div>
<div class="col-md-2"><a class="text-decoration-none text-reset" href="/admin/starred-players?season=%d&amp;view=appearances#card-detail"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Appearances</div><div class="display-6">%d</div><div class="small text-primary">View appearances &rarr;</div></div></div></a></div>
<div class="col-md-2"><a class="text-decoration-none text-reset" href="#potential-breaches"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Potential breaches</div><div class="display-6 text-danger">%d</div><div class="small text-primary">View evidence &rarr;</div></div></div></a></div>
<div class="col-md-2"><a class="text-decoration-none text-reset" href="#june-30-test"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Unstarred ≥50%%</div><div class="display-6 text-warning">%d</div><div class="small text-muted">%d club list issues</div><div class="small text-primary">View calculation &rarr;</div></div></div></a></div>
</div>`, year, currentA, year, currentB, year, matchCount, pendingCount, year, len(reviewApps), len(eval.Breaches), countUnstarredCandidates(eval.Candidates), clubIssueCount)
		s.renderStarredCardDetail(w, ctx, year, cutoff, strings.TrimSpace(r.URL.Query().Get("view")), periods, reviewApps, mappings, matchCount, len(reviewApps), r)
		fmt.Fprintf(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Synchronisation</div><div class="card-body d-flex flex-wrap gap-3">
<form method="post" action="/admin/starred-players/sync-list"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><button class="btn btn-primary">Sync published list</button><div class="form-text">Imports base lists and applies dated amendments.</div></form>
<form id="starred-scorecard-sync-form" method="post" action="/admin/starred-players/sync-appearances"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><button id="starred-scorecard-sync-button" class="btn btn-primary">Import all pending scorecards</button><div class="form-text">Runs automatically in batches of 50. Keep this page open until complete.</div><div id="starred-scorecard-sync-progress" class="small mt-2" aria-live="polite"></div></form>
</div></div>`, escapeHTML(csrf), year, escapeHTML(csrf), year)
		fmt.Fprint(w, `<script>
(function () {
  const form = document.getElementById('starred-scorecard-sync-form');
  const button = document.getElementById('starred-scorecard-sync-button');
  const progress = document.getElementById('starred-scorecard-sync-progress');
  if (!form || !window.fetch) return;
  form.addEventListener('submit', async function (event) {
    event.preventDefault();
    button.disabled = true;
    let matches = 0, appearances = 0, batches = 0, failures = new Set();
    try {
      while (true) {
        batches++;
        progress.className = 'small mt-2 text-primary';
        progress.textContent = 'Importing batch ' + batches + '… ' + matches + ' scorecards imported so far.';
        const body = new FormData(form);
        body.set('ajax', '1');
        const response = await fetch(form.action, {method: 'POST', body: body, headers: {'Accept': 'application/json'}});
        const result = await response.json();
        if (!response.ok) throw new Error(result.error || 'Scorecard import failed');
        matches += result.matches;
        appearances += result.appearances;
        if (result.failures) result.failures.forEach(function (failure) { failures.add(failure); });
        progress.textContent = matches + ' scorecards and ' + appearances + ' appearances imported; ' + result.pending + ' pending.';
        if (result.pending === 0) {
          progress.className = 'small mt-2 text-success';
          progress.textContent = 'Complete: ' + matches + ' scorecards and ' + appearances + ' appearances imported. Refreshing results…';
          window.setTimeout(function () { window.location.reload(); }, 1200);
          return;
        }
        if (result.matches === 0) {
          throw new Error('No further scorecards could be imported. ' + result.pending + ' remain pending. ' + Array.from(failures).slice(-5).join('; '));
        }
      }
    } catch (error) {
      progress.className = 'small mt-2 text-danger';
      progress.textContent = error.message;
      button.disabled = false;
    }
  });
}());
</script>`)

		breachGroups := groupStarredBreaches(eval.Breaches)
		fmt.Fprint(w, `<div id="potential-breaches" class="card shadow-sm mb-4"><div class="card-header"><div class="fw-semibold">Potential List A / List B breaches by division</div><div class="small text-muted">Accept and close a finding where no offence should be pursued, or create an editable letter for separate approval before it is sent.</div></div>`)
		if len(breachGroups) > 0 {
			fmt.Fprint(w, `<div class="card-body border-bottom d-flex flex-wrap gap-2">`)
			for index, group := range breachGroups {
				fmt.Fprintf(w, `<a class="btn btn-sm btn-outline-secondary" href="#starred-division-%d">%s — %s <span class="badge bg-secondary">%d</span></a>`, index, escapeHTML(group.Day), escapeHTML(group.Division), len(group.Breaches))
			}
			fmt.Fprint(w, `</div>`)
		}
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Date</th><th>Club</th><th>Player</th><th>List</th><th>Team</th><th>Format</th><th>Evidence</th><th>Review</th></tr></thead><tbody>`)
		if len(eval.Breaches) == 0 {
			fmt.Fprint(w, `<tr><td colspan="8" class="text-center text-muted py-3">No potential breaches found in imported scorecards.</td></tr>`)
		}
		for groupIndex, group := range breachGroups {
			findingWord := "findings"
			if len(group.Breaches) == 1 {
				findingWord = "finding"
			}
			fmt.Fprintf(w, `<tr id="starred-division-%d" class="table-primary"><th colspan="8" class="py-2">%s — %s <span class="badge bg-primary ms-1">%d %s</span></th></tr>`, groupIndex, escapeHTML(group.Day), escapeHTML(group.Division), len(group.Breaches), findingWord)
			for _, b := range group.Breaches {
				evidence := "Review"
				if b.NeedsExemptionReview {
					evidence = "Junior tag — verify exemption"
				}
				state := findingStates[starredFindingKey(b)]
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td><span class="badge bg-danger">%s</span></td><td>%s</td><td>%s</td><td>%s · <a href="/admin/starred-players?season=%d&amp;view=scorecard&amp;match_id=%d#card-detail">view match %d</a></td><td>%s</td></tr>`, b.Appearance.MatchDate.Format("02 Jan 2006"), escapeHTML(b.Appearance.ClubName), escapeHTML(b.Appearance.PlayerName), escapeHTML(b.ListType), escapeHTML(b.Appearance.TeamName), escapeHTML(b.Appearance.CompetitionType), escapeHTML(evidence), year, b.Appearance.MatchID, b.Appearance.MatchID, starredFindingActionsHTML(b, state, csrf, year))
			}
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)
		if clubIssueCount > 0 {
			fmt.Fprint(w, `<div class="card border-warning shadow-sm mb-4"><div class="card-header fw-semibold">Club list completeness</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Club</th><th>Active players</th><th>Expected</th><th>Reason</th></tr></thead><tbody>`)
			for _, x := range clubStatuses {
				if x.Compliant {
					continue
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%s</td></tr>`, escapeHTML(x.ClubName), x.CurrentCount, x.ExpectedCount, escapeHTML(x.Reason))
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		}

		fmt.Fprint(w, `<div id="june-30-test" class="card shadow-sm mb-4"><div class="card-header fw-semibold">30 June league-appearance test</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Club</th><th>Player</th><th>1st XI</th><th>All league</th><th>Percentage</th><th>Status</th></tr></thead><tbody>`)
		shown := 0
		for _, c := range eval.Candidates {
			if c.AlreadyStarred {
				continue
			}
			shown++
			fmt.Fprintf(w, `<tr><td>%s</td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td><td>%d</td><td>%d</td><td>%.1f%%</td><td><span class="badge bg-warning text-dark">List B review</span></td></tr>`, escapeHTML(c.ClubName), year, url.QueryEscape(c.PlayerName), escapeHTML(c.PlayerName), c.FirstXILeague, c.AllLeague, c.Percentage*100)
		}
		if shown == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No unstarred candidates currently meet the threshold.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)

		if len(suggestions) > 0 {
			fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Suggested identity matches</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Club</th><th>Published name</th><th>Play-Cricket candidate</th><th></th></tr></thead><tbody>`)
			for i, x := range suggestions {
				if i >= 100 {
					break
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s <code>%d</code></td><td><form method="post" action="/admin/starred-players/mapping"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="club_key" value="%s"><input type="hidden" name="player_key" value="%s"><input type="hidden" name="candidate_id" value="%d"><input type="hidden" name="candidate_name" value="%s"><button class="btn btn-sm btn-outline-primary">Confirm</button></form></td></tr>`, escapeHTML(x.ClubName), escapeHTML(x.StarredName), escapeHTML(x.CandidateName), x.CandidateID, escapeHTML(csrf), year, escapeHTML(x.ClubKey), escapeHTML(x.StarredPlayerKey), x.CandidateID, escapeHTML(x.CandidateName))
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		}
		if len(issues) > 0 {
			fmt.Fprint(w, `<div class="card border-warning shadow-sm mb-4"><div class="card-header fw-semibold">Amendments requiring review</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Club</th><th>Amendment</th><th>Published text</th><th>Reason</th></tr></thead><tbody>`)
			for _, i := range issues {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%s</td><td>%s</td></tr>`, escapeHTML(i.ClubName), i.Sequence, escapeHTML(i.RawValue), escapeHTML(i.Reason))
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		}
		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}

func (s *Server) renderStarredCardDetail(w http.ResponseWriter, ctx context.Context, year int, cutoff time.Time, view string, periods []starred.Period, reviewApps []starred.Appearance, mappings []starred.IdentityMapping, scorecardTotal, appearanceTotal int, r *http.Request) {
	if view == "" {
		return
	}
	page := 1
	if parsed, err := strconv.Atoi(r.URL.Query().Get("detail_page")); err == nil && parsed > 0 {
		page = parsed
	}
	const pageSize = 200
	offset := (page - 1) * pageSize

	fmt.Fprint(w, `<div id="card-detail" class="card shadow-sm mb-4"><div class="card-header d-flex justify-content-between align-items-center"><span class="fw-semibold">`)
	switch view {
	case "club-list":
		s.renderStarredClubList(w, ctx, year, cutoff, periods, reviewApps, mappings, r)
	case "list-a", "list-b":
		listType := "A"
		if view == "list-b" {
			listType = "B"
		}
		fmt.Fprintf(w, `List %s players at 30 June</span><a class="btn btn-sm btn-outline-secondary" href="/admin/starred-players?season=%d">Close</a></div>`, listType, year)
		active := make([]starred.Period, 0)
		for _, period := range periods {
			if period.ListType == listType && !cutoff.Before(period.ValidFrom) && (period.ValidTo == nil || cutoff.Before(*period.ValidTo)) {
				active = append(active, period)
			}
		}
		sort.Slice(active, func(i, j int) bool {
			if active[i].ClubName != active[j].ClubName {
				return active[i].ClubName < active[j].ClubName
			}
			return active[i].PlayerName < active[j].PlayerName
		})
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Club</th><th>Player</th><th>Effective from</th><th>Tags</th><th>Source</th></tr></thead><tbody>`)
		for _, period := range active {
			fmt.Fprintf(w, `<tr><td><a href="/admin/starred-players?season=%d&amp;view=club-list&amp;club=%s#card-detail">%s</a></td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>`, year, url.QueryEscape(period.ClubKey), escapeHTML(period.ClubName), year, url.QueryEscape(period.PlayerName), escapeHTML(period.PlayerName), period.ValidFrom.Format("02 Jan 2006"), escapeHTML(strings.Join(period.Tags, ", ")), escapeHTML(period.SourceKind))
		}
		if len(active) == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-center text-muted py-3">No active players found.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)
	case "scorecards":
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		search := "%" + query + "%"
		if query != "" {
			_ = s.DB.QueryRow(ctx, `
				SELECT COUNT(*)::int FROM starred_match_imports sm
				LEFT JOIN league_fixtures lf ON lf.play_cricket_match_id=sm.play_cricket_match_id
				WHERE sm.season_year=$1 AND sm.match_date <= $2::date
				  AND EXISTS (SELECT 1 FROM starred_appearances sa WHERE sa.play_cricket_match_id=sm.play_cricket_match_id AND sa.team_level > 0)
				  AND NOT EXISTS (SELECT 1 FROM starred_appearances sa WHERE sa.play_cricket_match_id=sm.play_cricket_match_id AND CONCAT_WS(' ',sa.competition_name,sa.club_name,sa.team_name) ~* '(wom(en|an)|ladies|female|girls)')
				  AND CONCAT_WS(' ',sm.play_cricket_match_id::text,lf.home_club_name,lf.home_team_name,lf.away_club_name,lf.away_team_name,sm.competition_name) ILIKE $3`, year, cutoff, search).Scan(&scorecardTotal)
		}
		fmt.Fprintf(w, `Open-age scorecards through 30 June</span><a class="btn btn-sm btn-outline-secondary" href="/admin/starred-players?season=%d">Close</a></div>`, year)
		fmt.Fprintf(w, `<div class="card-body border-bottom"><form method="get" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><input type="hidden" name="view" value="scorecards"><div class="col-md-8"><label class="form-label small" for="scorecard-search">Search club, team, competition or match ID</label><input id="scorecard-search" class="form-control" name="q" value="%s" placeholder="e.g. Droylsden, 2nd XI or 7458963"></div><div class="col-auto"><button class="btn btn-primary">Search</button></div><div class="col-auto"><a class="btn btn-outline-secondary" href="/admin/starred-players?season=%d&amp;view=scorecards#card-detail">Clear</a></div></form><div class="form-text">Only classified men's open-age XI fixtures are shown; women's and junior scorecards are excluded from this review.</div></div>`, year, escapeHTML(query), year)
		rows, err := s.DB.Query(ctx, `
			SELECT sm.play_cricket_match_id,sm.match_date,
			       COALESCE(lf.home_club_name,''),COALESCE(lf.home_team_name,''),
			       COALESCE(lf.away_club_name,''),COALESCE(lf.away_team_name,''),
			       COALESCE(sm.competition_type,''),COALESCE(sm.competition_name,'')
			FROM starred_match_imports sm
			LEFT JOIN league_fixtures lf ON lf.play_cricket_match_id=sm.play_cricket_match_id
			WHERE sm.season_year=$1 AND sm.match_date <= $2::date
			  AND EXISTS (SELECT 1 FROM starred_appearances sa WHERE sa.play_cricket_match_id=sm.play_cricket_match_id AND sa.team_level > 0)
			  AND NOT EXISTS (SELECT 1 FROM starred_appearances sa WHERE sa.play_cricket_match_id=sm.play_cricket_match_id AND CONCAT_WS(' ',sa.competition_name,sa.club_name,sa.team_name) ~* '(wom(en|an)|ladies|female|girls)')
			  AND ($3 = '%%' OR CONCAT_WS(' ',sm.play_cricket_match_id::text,lf.home_club_name,lf.home_team_name,lf.away_club_name,lf.away_team_name,sm.competition_name) ILIKE $3)
			ORDER BY sm.match_date DESC,sm.play_cricket_match_id DESC
			LIMIT $4 OFFSET $5`, year, cutoff, search, pageSize, offset)
		if err != nil {
			fmt.Fprintf(w, `<div class="alert alert-danger m-3">%s</div></div>`, escapeHTML(err.Error()))
			return
		}
		defer rows.Close()
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Date</th><th>Home</th><th>Away</th><th>Competition</th><th>Match</th><th></th></tr></thead><tbody>`)
		shown := 0
		for rows.Next() {
			var id int64
			var date time.Time
			var homeClub, homeTeam, awayClub, awayTeam, competitionType, competitionName string
			if err := rows.Scan(&id, &date, &homeClub, &homeTeam, &awayClub, &awayTeam, &competitionType, &competitionName); err != nil {
				continue
			}
			shown++
			fmt.Fprintf(w, `<tr><td>%s</td><td><strong>%s</strong><div class="small text-muted">%s</div></td><td><strong>%s</strong><div class="small text-muted">%s</div></td><td>%s<div class="small text-muted">%s</div></td><td><a href="/admin/starred-players?season=%d&amp;view=scorecard&amp;match_id=%d#card-detail"><code>%d</code></a></td><td><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=scorecard&amp;match_id=%d#card-detail">View match</a></td></tr>`, date.Format("02 Jan 2006"), escapeHTML(homeClub), escapeHTML(homeTeam), escapeHTML(awayClub), escapeHTML(awayTeam), escapeHTML(competitionName), escapeHTML(competitionType), year, id, id, year, id)
		}
		if shown == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No scorecards found on this page.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
		renderStarredDetailPager(w, year, view, page, scorecardTotal, pageSize, query)
		fmt.Fprint(w, `</div>`)
	case "scorecard":
		matchID, parseErr := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("match_id")), 10, 64)
		if parseErr != nil || matchID <= 0 {
			fmt.Fprint(w, `Match details</span></div><div class="alert alert-warning m-3">Choose a valid match from the scorecard list.</div></div>`)
			return
		}
		var date time.Time
		var homeClub, homeTeam, awayClub, awayTeam, competitionType, competitionName string
		err := s.DB.QueryRow(ctx, `
			SELECT sm.match_date,COALESCE(lf.home_club_name,''),COALESCE(lf.home_team_name,''),COALESCE(lf.away_club_name,''),COALESCE(lf.away_team_name,''),COALESCE(sm.competition_type,''),COALESCE(sm.competition_name,'')
			FROM starred_match_imports sm LEFT JOIN league_fixtures lf ON lf.play_cricket_match_id=sm.play_cricket_match_id
			WHERE sm.play_cricket_match_id=$1 AND sm.season_year=$2
			  AND NOT EXISTS (SELECT 1 FROM starred_appearances sa WHERE sa.play_cricket_match_id=sm.play_cricket_match_id AND CONCAT_WS(' ',sa.competition_name,sa.club_name,sa.team_name) ~* '(wom(en|an)|ladies|female|girls)')`, matchID, year).Scan(&date, &homeClub, &homeTeam, &awayClub, &awayTeam, &competitionType, &competitionName)
		if err != nil {
			fmt.Fprintf(w, `Match %d</span><a class="btn btn-sm btn-outline-secondary" href="/admin/starred-players?season=%d&amp;view=scorecards#card-detail">Back to scorecards</a></div><div class="alert alert-danger m-3">%s</div></div>`, matchID, year, escapeHTML(err.Error()))
			return
		}
		fmt.Fprintf(w, `%s v %s</span><a class="btn btn-sm btn-outline-secondary" href="/admin/starred-players?season=%d&amp;view=scorecards#card-detail">Back to scorecards</a></div>`, escapeHTML(homeClub), escapeHTML(awayClub), year)
		fmt.Fprintf(w, `<div class="card-body border-bottom"><div class="row g-3"><div class="col-md-3"><div class="small text-muted">Date</div><strong>%s</strong></div><div class="col-md-3"><div class="small text-muted">Home</div><strong>%s</strong><div>%s</div></div><div class="col-md-3"><div class="small text-muted">Away</div><strong>%s</strong><div>%s</div></div><div class="col-md-3"><div class="small text-muted">Competition</div><strong>%s</strong><div>%s &middot; Match %d</div></div></div></div>`, date.Format("02 Jan 2006"), escapeHTML(homeClub), escapeHTML(homeTeam), escapeHTML(awayClub), escapeHTML(awayTeam), escapeHTML(competitionName), escapeHTML(competitionType), matchID)
		playerRows, playerErr := s.DB.Query(ctx, `SELECT season_year,match_date,COALESCE(competition_type,''),COALESCE(competition_name,''),club_name,club_key,team_name,COALESCE(team_level,0),COALESCE(playing_day,''),COALESCE(play_cricket_player_id,0),player_name,player_key,captain,wicket_keeper FROM starred_appearances WHERE play_cricket_match_id=$1 AND team_level > 0 ORDER BY team_level,club_name,player_name`, matchID)
		if playerErr != nil {
			fmt.Fprintf(w, `<div class="alert alert-danger m-3">%s</div></div>`, escapeHTML(playerErr.Error()))
			return
		}
		defer playerRows.Close()
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Club / team</th><th>Player</th><th>Player ID</th><th>Role</th><th>Starred at match date</th></tr></thead><tbody>`)
		shown := 0
		for playerRows.Next() {
			var appearance starred.Appearance
			var captain, wicketKeeper bool
			if err := playerRows.Scan(&appearance.SeasonYear, &appearance.MatchDate, &appearance.CompetitionType, &appearance.CompetitionName, &appearance.ClubName, &appearance.ClubKey, &appearance.TeamName, &appearance.TeamLevel, &appearance.PlayingDay, &appearance.PlayerID, &appearance.PlayerName, &appearance.PlayerKey, &captain, &wicketKeeper); err != nil {
				continue
			}
			shown++
			roles := make([]string, 0, 2)
			if captain {
				roles = append(roles, "Captain")
			}
			if wicketKeeper {
				roles = append(roles, "Wicketkeeper")
			}
			listType := starredListForAppearance(periods, mappings, appearance)
			listBadge := `<span class="text-muted">Not starred</span>`
			if listType != "" {
				listBadge = fmt.Sprintf(`<span class="badge bg-danger">List %s</span>`, escapeHTML(listType))
			}
			fmt.Fprintf(w, `<tr><td><strong>%s</strong><div class="small text-muted">%s</div></td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td><td><code>%d</code></td><td>%s</td><td>%s</td></tr>`, escapeHTML(appearance.ClubName), escapeHTML(appearance.TeamName), year, url.QueryEscape(appearance.PlayerName), escapeHTML(appearance.PlayerName), appearance.PlayerID, escapeHTML(strings.Join(roles, ", ")), listBadge)
		}
		if shown == 0 {
			fmt.Fprint(w, `<tr><td colspan="5" class="text-center text-muted py-3">No classified open-age players were found for this match.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)
	case "appearances":
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		search := "%" + query + "%"
		if query != "" {
			_ = s.DB.QueryRow(ctx, `SELECT COUNT(*)::int FROM starred_appearances WHERE season_year=$1 AND match_date <= $2::date AND team_level > 0 AND CONCAT_WS(' ',competition_name,club_name,team_name) !~* '(wom(en|an)|ladies|female|girls)' AND CONCAT_WS(' ',player_name,play_cricket_player_id::text,club_name,team_name,competition_name) ILIKE $3`, year, cutoff, search).Scan(&appearanceTotal)
		}
		fmt.Fprintf(w, `Open-age player appearances through 30 June</span><a class="btn btn-sm btn-outline-secondary" href="/admin/starred-players?season=%d">Close</a></div>`, year)
		fmt.Fprintf(w, `<div class="card-body border-bottom"><form method="get" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><input type="hidden" name="view" value="appearances"><div class="col-md-8"><label class="form-label small" for="appearance-search">Search player, club, team or player ID</label><input id="appearance-search" class="form-control" name="q" value="%s" placeholder="e.g. player name, club or ID"></div><div class="col-auto"><button class="btn btn-primary">Search</button></div><div class="col-auto"><a class="btn btn-outline-secondary" href="/admin/starred-players?season=%d&amp;view=appearances#card-detail">Clear</a></div></form></div>`, year, escapeHTML(query), year)
		rows, err := s.DB.Query(ctx, `
			SELECT match_date,club_name,team_name,player_name,COALESCE(play_cricket_player_id,0),COALESCE(competition_name,competition_type,''),play_cricket_match_id
			FROM starred_appearances
			WHERE season_year=$1 AND match_date <= $2::date AND team_level > 0
			  AND CONCAT_WS(' ',competition_name,club_name,team_name) !~* '(wom(en|an)|ladies|female|girls)'
			  AND ($3 = '%%' OR CONCAT_WS(' ',player_name,play_cricket_player_id::text,club_name,team_name,competition_name) ILIKE $3)
			ORDER BY match_date DESC,club_name,team_name,player_name
			LIMIT $4 OFFSET $5`, year, cutoff, search, pageSize, offset)
		if err != nil {
			fmt.Fprintf(w, `<div class="alert alert-danger m-3">%s</div></div>`, escapeHTML(err.Error()))
			return
		}
		defer rows.Close()
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Date</th><th>Club / team</th><th>Player</th><th>Player ID</th><th>Competition</th><th>Match</th></tr></thead><tbody>`)
		shown := 0
		for rows.Next() {
			var date time.Time
			var club, team, player, competition string
			var playerID, matchID int64
			if err := rows.Scan(&date, &club, &team, &player, &playerID, &competition, &matchID); err != nil {
				continue
			}
			shown++
			fmt.Fprintf(w, `<tr><td>%s</td><td><strong>%s</strong><div class="small text-muted">%s</div></td><td>%s</td><td><code>%d</code></td><td>%s</td><td><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=scorecard&amp;match_id=%d#card-detail">View match %d</a></td></tr>`, date.Format("02 Jan 2006"), escapeHTML(club), escapeHTML(team), escapeHTML(player), playerID, escapeHTML(competition), year, matchID, matchID)
		}
		if shown == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No appearances found on this page.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div>`)
		renderStarredDetailPager(w, year, view, page, appearanceTotal, pageSize, query)
		fmt.Fprint(w, `</div>`)
	default:
		fmt.Fprint(w, `Details</span></div><div class="alert alert-warning m-3">Unknown card view.</div></div>`)
	}
}

func activeStarredPeriodsByClub(periods []starred.Period, cutoff time.Time, clubKey string) []starred.Period {
	active := make([]starred.Period, 0)
	for _, period := range periods {
		if period.ClubKey != clubKey || cutoff.Before(period.ValidFrom) || (period.ValidTo != nil && !cutoff.Before(*period.ValidTo)) {
			continue
		}
		active = append(active, period)
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].ListType != active[j].ListType {
			return active[i].ListType < active[j].ListType
		}
		return active[i].PlayerName < active[j].PlayerName
	})
	return active
}

func starredSaturdayTeamCounts(periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping) map[string]map[int]int {
	mappedIDs := make(map[string]int64, len(mappings))
	for _, mapping := range mappings {
		mappedIDs[mapping.ClubKey+"|"+mapping.StarredPlayerKey] = mapping.PlayerID
	}
	counts := make(map[string]map[int]int, len(periods))
	for _, period := range periods {
		key := period.ClubKey + "|" + period.PlayerKey
		counts[key] = make(map[int]int)
		mappedID := mappedIDs[key]
		seen := make(map[string]struct{})
		for _, appearance := range appearances {
			if appearance.ClubKey != period.ClubKey || appearance.TeamLevel < 1 || !strings.EqualFold(appearance.PlayingDay, "Saturday") {
				continue
			}
			matches := appearance.PlayerKey == period.PlayerKey
			if mappedID > 0 {
				matches = appearance.PlayerID == mappedID
			}
			if !matches {
				continue
			}
			appearanceKey := strconv.FormatInt(appearance.MatchID, 10) + "|" + strconv.Itoa(appearance.TeamLevel)
			if _, duplicate := seen[appearanceKey]; duplicate {
				continue
			}
			seen[appearanceKey] = struct{}{}
			counts[key][appearance.TeamLevel]++
		}
	}
	return counts
}

func starredTeamLevelLabel(level int) string {
	switch level {
	case 1:
		return "1st XI"
	case 2:
		return "2nd XI"
	case 3:
		return "3rd XI"
	default:
		return strconv.Itoa(level) + "th XI"
	}
}

type starredSaturdayDivision struct {
	Label string
	Clubs []string
}

func saturdayStarredClubDivisions(clubNames map[string]string, appearances []starred.Appearance, overrides map[string]string) []starredSaturdayDivision {
	type divisionCounts map[string]int
	firstXI := make(map[string]divisionCounts)
	fallback := make(map[string]divisionCounts)
	add := func(target map[string]divisionCounts, clubKey, division string) {
		if target[clubKey] == nil {
			target[clubKey] = make(divisionCounts)
		}
		target[clubKey][division]++
	}
	for _, appearance := range appearances {
		if _, listed := clubNames[appearance.ClubKey]; !listed || !strings.EqualFold(appearance.PlayingDay, "Saturday") || !strings.EqualFold(appearance.CompetitionType, "League") {
			continue
		}
		division := starredDivisionLabel(appearance.CompetitionName, appearance.CompetitionType)
		add(fallback, appearance.ClubKey, division)
		if appearance.TeamLevel == 1 {
			add(firstXI, appearance.ClubKey, division)
		}
	}
	chooseDivision := func(counts divisionCounts) string {
		selected, selectedCount := "", -1
		for division, count := range counts {
			if count > selectedCount || (count == selectedCount && starredDivisionRank(division) < starredDivisionRank(selected)) {
				selected, selectedCount = division, count
			}
		}
		return selected
	}
	clubsByDivision := make(map[string][]string)
	for clubKey := range clubNames {
		division := strings.TrimSpace(overrides[clubKey])
		if division == "" {
			division = chooseDivision(firstXI[clubKey])
		}
		if division == "" {
			division = chooseDivision(fallback[clubKey])
		}
		if division == "" {
			division = "Unassigned / no Saturday division"
		}
		clubsByDivision[division] = append(clubsByDivision[division], clubKey)
	}
	divisions := make([]starredSaturdayDivision, 0, len(clubsByDivision))
	for label, clubs := range clubsByDivision {
		sort.Slice(clubs, func(i, j int) bool { return clubNames[clubs[i]] < clubNames[clubs[j]] })
		divisions = append(divisions, starredSaturdayDivision{Label: label, Clubs: clubs})
	}
	sort.Slice(divisions, func(i, j int) bool {
		left, right := starredDivisionRank(divisions[i].Label), starredDivisionRank(divisions[j].Label)
		if left != right {
			return left < right
		}
		return divisions[i].Label < divisions[j].Label
	})
	return divisions
}

func (s *Server) loadStarredDivisionOverrides(ctx context.Context, year int) map[string]string {
	overrides := make(map[string]string)
	rows, err := s.DB.Query(ctx, `SELECT club_key,division_name FROM starred_club_division_overrides WHERE season_year=$1`, year)
	if err != nil {
		return overrides
	}
	defer rows.Close()
	for rows.Next() {
		var clubKey, division string
		if rows.Scan(&clubKey, &division) == nil {
			overrides[clubKey] = division
		}
	}
	return overrides
}

func (s *Server) renderStarredClubList(w http.ResponseWriter, ctx context.Context, year int, cutoff time.Time, periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping, r *http.Request) {
	selectedClub := strings.TrimSpace(r.URL.Query().Get("club"))
	selectedDivision := strings.TrimSpace(r.URL.Query().Get("division"))
	clubNames := make(map[string]string)
	for _, period := range periods {
		if cutoff.Before(period.ValidFrom) || (period.ValidTo != nil && !cutoff.Before(*period.ValidTo)) {
			continue
		}
		clubNames[period.ClubKey] = period.ClubName
	}
	overrides := s.loadStarredDivisionOverrides(ctx, year)
	divisions := saturdayStarredClubDivisions(clubNames, appearances, overrides)
	clubDivision := make(map[string]string)
	for _, division := range divisions {
		for _, clubKey := range division.Clubs {
			clubDivision[clubKey] = division.Label
		}
	}
	if selectedDivision == "" && selectedClub != "" {
		selectedDivision = clubDivision[selectedClub]
	}

	fmt.Fprintf(w, `Starred list by club at 30 June</span><a class="btn btn-sm btn-outline-secondary" href="/admin/starred-players?season=%d">Close</a></div>`, year)
	fmt.Fprintf(w, `<div class="card-body border-bottom"><form method="get" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><input type="hidden" name="view" value="club-list"><div class="col-md-5"><label class="form-label fw-semibold" for="starred-division-filter">Saturday division</label><select id="starred-division-filter" class="form-select" name="division" required onchange="this.form.elements.club.value='';this.form.submit()"><option value="">Choose a Saturday division…</option>`, year)
	for _, division := range divisions {
		selected := ""
		if division.Label == selectedDivision {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s (%d clubs)</option>`, escapeHTML(division.Label), selected, escapeHTML(division.Label), len(division.Clubs))
	}
	fmt.Fprint(w, `</select></div><div class="col-md-5"><label class="form-label fw-semibold" for="starred-club-filter">Club</label><select id="starred-club-filter" class="form-select" name="club" required><option value="">Choose a club…</option>`)
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
	fmt.Fprint(w, `</select></div><div class="col-auto"><button class="btn btn-primary">Show starred list</button></div></form><div class="form-text">Clubs are assigned from their Saturday 1st XI league division at the review cutoff. Manual corrections take priority.</div></div>`)
	fmt.Fprintf(w, `<div class="card-body border-bottom bg-light"><details><summary class="fw-semibold" style="cursor:pointer">Correct a club's division assignment</summary><form method="post" action="/admin/starred-players/mapping" class="row g-2 align-items-end mt-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="mapping_type" value="division"><input type="hidden" name="season" value="%d"><div class="col-md-5"><label class="form-label" for="override-club">Club</label><select id="override-club" class="form-select" name="club_key" required onchange="document.getElementById('override-division').value=this.options[this.selectedIndex].dataset.division||''"><option value="">Choose a club…</option>`, escapeHTML(middleware.CSRFToken(r)), year)
	allClubKeys := make([]string, 0, len(clubNames))
	for clubKey := range clubNames {
		allClubKeys = append(allClubKeys, clubKey)
	}
	sort.Slice(allClubKeys, func(i, j int) bool { return clubNames[allClubKeys[i]] < clubNames[allClubKeys[j]] })
	for _, clubKey := range allClubKeys {
		selected := ""
		if clubKey == selectedClub {
			selected = " selected"
		}
		marker := " — current: " + clubDivision[clubKey]
		if overrides[clubKey] != "" {
			marker += " (manual)"
		}
		fmt.Fprintf(w, `<option value="%s" data-division="%s"%s>%s%s</option>`, escapeHTML(clubKey), escapeHTML(clubDivision[clubKey]), selected, escapeHTML(clubNames[clubKey]), escapeHTML(marker))
	}
	fmt.Fprintf(w, `</select></div><div class="col-md-4"><label class="form-label" for="override-division">Correct Saturday division</label><input id="override-division" class="form-control" name="division_name" list="starred-division-options" value="%s" placeholder="e.g. Premier 1" required><datalist id="starred-division-options">`, escapeHTML(clubDivision[selectedClub]))
	for _, division := range divisions {
		if division.Label != "Unassigned / no Saturday division" {
			fmt.Fprintf(w, `<option value="%s"></option>`, escapeHTML(division.Label))
		}
	}
	fmt.Fprint(w, `</datalist></div><div class="col-auto"><button class="btn btn-warning" name="override_action" value="save">Save correction</button></div><div class="col-auto"><button class="btn btn-outline-secondary" name="override_action" value="clear" formnovalidate>Use automatic assignment</button></div></form></details></div>`)
	if selectedDivision == "" {
		fmt.Fprint(w, `<div class="card-body text-center text-muted py-4">Choose the Saturday division you have been assigned, then select a club.</div></div>`)
		return
	}
	if selectedClub == "" {
		fmt.Fprintf(w, `<div class="card-body text-center text-muted py-4">Choose a club from %s to see its current List A and List B players.</div></div>`, escapeHTML(selectedDivision))
		return
	}
	clubName, exists := clubNames[selectedClub]
	if !exists || clubDivision[selectedClub] != selectedDivision {
		fmt.Fprint(w, `<div class="alert alert-warning m-3">That club is not assigned to the selected Saturday division.</div></div>`)
		return
	}
	active := activeStarredPeriodsByClub(periods, cutoff, selectedClub)
	teamCounts := starredSaturdayTeamCounts(active, appearances, mappings)
	maxTeamLevel := 4
	for _, playerCounts := range teamCounts {
		for level := range playerCounts {
			if level > maxTeamLevel {
				maxTeamLevel = level
			}
		}
	}
	listACount, listBCount := 0, 0
	for _, period := range active {
		if period.ListType == "A" {
			listACount++
		} else if period.ListType == "B" {
			listBCount++
		}
	}
	fmt.Fprintf(w, `<div class="card-body border-bottom d-flex flex-wrap justify-content-between align-items-center gap-2"><div><h5 class="mb-0">%s</h5><div class="small text-muted">Saturday appearances from imported scorecards through %s.</div></div><div><span class="badge bg-danger me-1">List A: %d</span><span class="badge bg-primary">List B: %d</span></div></div>`, escapeHTML(clubName), cutoff.Format("02 January 2006"), listACount, listBCount)
	fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>List</th><th>Player</th>`)
	for level := 1; level <= maxTeamLevel; level++ {
		fmt.Fprintf(w, `<th class="text-center text-nowrap">%s</th>`, starredTeamLevelLabel(level))
	}
	fmt.Fprint(w, `<th class="text-center">Total</th><th>Effective from</th><th>Tags</th><th>Source</th></tr></thead><tbody>`)
	for _, period := range active {
		badgeClass := "bg-primary"
		if period.ListType == "A" {
			badgeClass = "bg-danger"
		}
		fmt.Fprintf(w, `<tr><td><span class="badge %s">List %s</span></td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td>`, badgeClass, escapeHTML(period.ListType), year, url.QueryEscape(period.PlayerName), escapeHTML(period.PlayerName))
		playerCounts := teamCounts[period.ClubKey+"|"+period.PlayerKey]
		total := 0
		for level := 1; level <= maxTeamLevel; level++ {
			count := playerCounts[level]
			total += count
			cellClass := "text-center"
			if count == 0 {
				cellClass += " text-muted"
			}
			fmt.Fprintf(w, `<td class="%s">%d</td>`, cellClass, count)
		}
		fmt.Fprintf(w, `<td class="text-center fw-semibold">%d</td><td>%s</td><td>%s</td><td>%s</td></tr>`, total, period.ValidFrom.Format("02 Jan 2006"), escapeHTML(strings.Join(period.Tags, ", ")), escapeHTML(period.SourceKind))
	}
	if len(active) == 0 {
		fmt.Fprintf(w, `<tr><td colspan="%d" class="text-center text-muted py-3">No active starred players found for this club.</td></tr>`, maxTeamLevel+6)
	}
	fmt.Fprint(w, `</tbody></table></div></div>`)
}

func starredListForAppearance(periods []starred.Period, mappings []starred.IdentityMapping, appearance starred.Appearance) string {
	mappedIDs := make(map[string]int64, len(mappings))
	for _, mapping := range mappings {
		mappedIDs[mapping.ClubKey+"|"+mapping.StarredPlayerKey] = mapping.PlayerID
	}
	for _, period := range periods {
		if period.ClubKey != appearance.ClubKey || appearance.MatchDate.Before(period.ValidFrom) || (period.ValidTo != nil && !appearance.MatchDate.Before(*period.ValidTo)) {
			continue
		}
		if mappedID := mappedIDs[period.ClubKey+"|"+period.PlayerKey]; mappedID > 0 {
			if appearance.PlayerID == mappedID {
				return period.ListType
			}
			continue
		}
		if period.PlayerKey == appearance.PlayerKey {
			return period.ListType
		}
	}
	return ""
}

func renderStarredDetailPager(w http.ResponseWriter, year int, view string, page, total, pageSize int, query string) {
	start := (page-1)*pageSize + 1
	end := page * pageSize
	if end > total {
		end = total
	}
	if total == 0 {
		start = 0
	}
	fmt.Fprintf(w, `<div class="card-footer d-flex justify-content-between align-items-center"><span class="small text-muted">Showing %d-%d of %d</span><div class="btn-group">`, start, end, total)
	queryParam := ""
	if strings.TrimSpace(query) != "" {
		queryParam = "&amp;q=" + url.QueryEscape(strings.TrimSpace(query))
	}
	if page > 1 {
		fmt.Fprintf(w, `<a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=%s%s&amp;detail_page=%d#card-detail">Previous</a>`, year, view, queryParam, page-1)
	}
	if end < total {
		fmt.Fprintf(w, `<a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=%s%s&amp;detail_page=%d#card-detail">Next</a>`, year, view, queryParam, page+1)
	}
	fmt.Fprint(w, `</div></div>`)
}

func countUnstarredCandidates(c []starred.Candidate) int {
	n := 0
	for _, x := range c {
		if !x.AlreadyStarred {
			n++
		}
	}
	return n
}

func (s *Server) handleAdminStarredPlayersSyncList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		year := starredSeasonYear(r)
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		snapshot, body, source, err := starred.FetchSnapshot(ctx, year)
		if err != nil {
			redirectStarred(w, r, year, "", err.Error())
			return
		}
		result, err := starred.StoreSnapshot(ctx, s.DB, snapshot, body, source, s.starredSeasonStart(ctx, year))
		if err != nil {
			redirectStarred(w, r, year, "", err.Error())
			return
		}
		msg := fmt.Sprintf("Imported %d List A/B entries and %d amendments; %d require review.", result.Entries, result.Amendments, result.Issues)
		if result.AlreadyCurrent {
			msg = "Published starred-player list is already current."
		}
		s.audit(ctx, r, "admin", nil, "starred_list_sync", "starred_import_run", &result.RunID, map[string]any{"season": year, "entries": result.Entries, "amendments": result.Amendments, "issues": result.Issues})
		redirectStarred(w, r, year, msg, "")
	}
}

func (s *Server) handleAdminStarredPlayersSyncAppearances() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		year := starredSeasonYear(r)
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		client := leagueapi.NewClient(leagueapi.NewConfigFromEnv())
		result, err := starred.SyncPendingScorecards(ctx, s.DB, client, year, 50)
		if err != nil {
			if r.FormValue("ajax") == "1" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			redirectStarred(w, r, year, "", err.Error())
			return
		}
		pending, pendingErr := starred.PendingMatchCount(ctx, s.DB, year)
		if pendingErr != nil {
			pending = -1
		}
		msg := fmt.Sprintf("Imported %d scorecards and %d player appearances.", result.Matches, result.Appearances)
		errMsg := ""
		if len(result.Failures) > 0 {
			errMsg = strings.Join(result.Failures, "; ")
		}
		s.audit(ctx, r, "admin", nil, "starred_scorecards_sync", "starred_match_import", nil, map[string]any{"season": year, "matches": result.Matches, "appearances": result.Appearances, "failures": len(result.Failures)})
		if r.FormValue("ajax") == "1" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "matches": result.Matches, "appearances": result.Appearances, "failures": result.Failures, "pending": pending})
			return
		}
		redirectStarred(w, r, year, msg, errMsg)
	}
}

func (s *Server) handleAdminStarredPlayersMapping() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		year := starredSeasonYear(r)
		if r.FormValue("mapping_type") == "division" {
			s.handleStarredDivisionOverride(w, r, year)
			return
		}
		id, err := strconv.ParseInt(r.FormValue("candidate_id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid candidate", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		err = starred.SaveIdentityMapping(ctx, s.DB, year, r.FormValue("club_key"), r.FormValue("player_key"), id, r.FormValue("candidate_name"), adminIDForRequest(r))
		if err != nil {
			redirectStarred(w, r, year, "", err.Error())
			return
		}
		redirectStarred(w, r, year, "Identity mapping confirmed.", "")
	}
}

func (s *Server) handleStarredDivisionOverride(w http.ResponseWriter, r *http.Request, year int) {
	clubKey := strings.TrimSpace(r.FormValue("club_key"))
	division := starredDivisionLabel(strings.TrimSpace(r.FormValue("division_name")), "")
	action := strings.TrimSpace(r.FormValue("override_action"))
	if (action != "save" && action != "clear") || clubKey == "" || (action != "clear" && (division == "" || len(division) > 120)) {
		http.Error(w, "club and a valid division are required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cutoff := starred.ReviewCutoff(year, time.Now())
	var clubName string
	err := s.DB.QueryRow(ctx, `
		SELECT p.club_name
		FROM starred_list_periods p
		WHERE p.import_run_id=(SELECT id FROM starred_import_runs WHERE season_year=$1 AND status='complete' ORDER BY imported_at DESC,id DESC LIMIT 1)
		  AND p.club_key=$2 AND p.valid_from <= $3::date AND (p.valid_to IS NULL OR p.valid_to > $3::date)
		ORDER BY p.id LIMIT 1`, year, clubKey, cutoff).Scan(&clubName)
	if err != nil {
		http.Error(w, "club is not present in the active starred list", http.StatusBadRequest)
		return
	}
	adminID := s.resolveAdminID(r)
	message := "Division assignment corrected for " + clubName + "."
	if action == "clear" {
		_, err = s.DB.Exec(ctx, `DELETE FROM starred_club_division_overrides WHERE season_year=$1 AND club_key=$2`, year, clubKey)
		division = ""
		message = "Manual division assignment removed for " + clubName + "."
	} else {
		_, err = s.DB.Exec(ctx, `INSERT INTO starred_club_division_overrides(season_year,club_key,club_name,division_name,updated_by,updated_at) VALUES($1,$2,$3,$4,$5,now()) ON CONFLICT(season_year,club_key) DO UPDATE SET club_name=EXCLUDED.club_name,division_name=EXCLUDED.division_name,updated_by=EXCLUDED.updated_by,updated_at=now()`, year, clubKey, clubName, division, adminID)
	}
	if err != nil {
		redirectStarred(w, r, year, "", "Could not save division assignment: "+err.Error())
		return
	}
	s.audit(ctx, r, "admin", adminID, "starred_division_override_"+action, "starred_club_division_override", nil, map[string]any{"season": year, "club": clubName, "division": division})
	q := url.Values{"season": {strconv.Itoa(year)}, "view": {"club-list"}, "club": {clubKey}, "message": {message}}
	if division != "" {
		q.Set("division", division)
	}
	http.Redirect(w, r, "/admin/starred-players?"+q.Encode()+"#card-detail", http.StatusSeeOther)
}

func redirectStarred(w http.ResponseWriter, r *http.Request, year int, message, errMsg string) {
	q := url.Values{}
	q.Set("season", strconv.Itoa(year))
	if message != "" {
		q.Set("message", message)
	}
	if errMsg != "" {
		q.Set("error", errMsg)
	}
	http.Redirect(w, r, "/admin/starred-players?"+q.Encode(), http.StatusSeeOther)
}
