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
		reviewApps = remapStarredAppearanceClubs(reviewApps, s.loadStarredAppearanceClubOverrides(ctx, year), activeStarredClubNames(periods, cutoff))
		eval := starred.Evaluation{}
		var suggestions []starred.MappingSuggestion
		var unmappedPeriods []starred.Period
		if loadErr == nil {
			eval = starred.Evaluate(periods, reviewApps, mappings, cutoff)
			suggestions = starred.SuggestMappings(periods, reviewApps, mappings, cutoff)
			unmappedPeriods = activeUnmappedStarredPeriods(periods, mappings, cutoff)
		}
		findingStates := s.loadStarredFindingStates(ctx, year)
		outstandingBreaches := filterOutstandingStarredBreaches(eval.Breaches, findingStates)
		breachFrom, breachTo, breachFilterErr := parseStarredBreachDateRange(r)
		filteredBreaches := outstandingBreaches
		if breachFilterErr == nil {
			filteredBreaches = filterStarredBreachesByDate(outstandingBreaches, breachFrom, breachTo)
		}
		mappingSource := strings.TrimSpace(r.URL.Query().Get("mapping_source"))
		mappingQuery := strings.TrimSpace(r.URL.Query().Get("mapping_q"))
		if runes := []rune(mappingQuery); len(runes) > 100 {
			mappingQuery = string(runes[:100])
		}
		selectedMappingPeriod, mappingSourceValid := findUnmappedStarredPeriod(unmappedPeriods, mappingSource)
		var identitySearchResults []starred.IdentitySearchResult
		if mappingSourceValid && len([]rune(mappingQuery)) >= 2 {
			identitySearchResults = starred.SearchAppearanceIdentities(reviewApps, mappingQuery, 100)
		}
		if strings.TrimSpace(r.URL.Query().Get("view")) == "player-review" && r.URL.Query().Get("export") == "csv" {
			s.writeStarredPlayerReviewCSV(w, ctx, year, cutoff, periods, reviewApps, mappings, r)
			return
		}
		if r.URL.Query().Get("export") == "breaches-csv" {
			if breachFilterErr != nil {
				http.Error(w, breachFilterErr.Error(), http.StatusBadRequest)
				return
			}
			writeStarredBreachesCSV(w, year, filteredBreaches, findingStates, breachFrom, breachTo)
			return
		}
		candidateReviewStates := s.loadStarredCandidateReviewStates(ctx, year)
		acceptedCandidateCount := 0
		for _, state := range candidateReviewStates {
			if state.Status == "accepted" {
				acceptedCandidateCount++
			}
		}
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
		automaticSyncStatus := "Automatic weekly refresh is enabled for Mondays at 03:00 Europe/London through 31 July."
		var lastAutomaticSync time.Time
		if err := s.DB.QueryRow(ctx, `SELECT created_at FROM audit_logs WHERE action=$1 ORDER BY created_at DESC LIMIT 1`, starredWeeklySyncAction).Scan(&lastAutomaticSync); err == nil {
			automaticSyncStatus += " Last completed: " + lastAutomaticSync.In(s.LondonLoc).Format("02 Jan 2006 15:04") + "."
		}
		if starredWeeklySyncWindowActive(time.Now(), s.LondonLoc) {
			automaticSyncStatus += " Next scheduled: " + nextStarredWeeklySync(time.Now(), s.LondonLoc).Format("02 Jan 2006 15:04") + "."
		} else {
			automaticSyncStatus += " The automatic import window is currently closed."
		}
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
<style>
#starred-overview,#card-detail,#sync,#identity-matches,#amendments,#potential-breaches,#club-lists,#july-31-test{scroll-margin-top:4.5rem}
.starred-section-nav{position:sticky;top:0;z-index:1020;background:var(--bs-body-bg);border-bottom:1px solid var(--bs-border-color)}
</style>
<div class="d-flex flex-wrap justify-content-between align-items-center mb-3 gap-2"><div><h3 class="mb-1">Starred Player Compliance%s</h3><p class="text-muted mb-0">Rule 3.5 review from the published lists and Play-Cricket team sheets through 31 July.</p></div>
<div class="d-flex flex-wrap gap-2"><a class="btn btn-primary" href="/admin/starred-players?season=%d&amp;view=club-list#card-detail">Starred list by club</a><a class="btn btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=player-review#card-detail">Player list review</a><form method="get" class="d-flex gap-2"><input class="form-control" style="width:110px" type="number" name="season" value="%d" aria-label="Season year"><button class="btn btn-outline-primary">Load</button></form></div></div>`,
			starredHelpIcon("What this page does", "Rule 3.5 protects lower divisions: each club nominates its strongest players as List A (1st XI only) or List B (1st or 2nd XI only). This page checks every imported Play-Cricket scorecard against the published lists so a person can review possible breaches — nothing is decided automatically. Work through the numbered steps in order: import data, match identities, review breaches, then run the 31 July test."),
			year, year, year)
		if msg := r.URL.Query().Get("message"); msg != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(msg))
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			fmt.Fprintf(w, `<div class="alert alert-danger">%s</div>`, escapeHTML(errMsg))
		}
		if loadErr != nil {
			fmt.Fprintf(w, `<div class="alert alert-warning">No imported starred list is available for %d yet. Sync the published list below.</div>`, year)
		}
		unmappedCount := len(unmappedPeriods)
		outstandingCandidates := countOutstandingUnstarredCandidates(year, eval.Candidates, candidateReviewStates)
		fmt.Fprintf(w, `<div class="starred-section-nav py-2 mb-4"><div class="d-flex flex-wrap gap-2 align-items-center"><span class="small fw-semibold text-muted me-1">Jump to</span>%s%s%s%s%s%s%s</div></div>`,
			starredNavPill("#starred-overview", "Overview", -1, ""),
			starredNavPill("#sync", "1&nbsp;&middot; Import data", pendingCount, "text-bg-primary"),
			starredNavPill("#identity-matches", "2&nbsp;&middot; Identities", unmappedCount, "text-bg-info"),
			starredNavPill("#amendments", "Amendments", len(issues), "text-bg-secondary"),
			starredNavPill("#potential-breaches", "3&nbsp;&middot; Breaches", len(outstandingBreaches), "text-bg-danger"),
			starredNavPill("#club-lists", "Club lists", clubIssueCount, "text-bg-warning text-dark"),
			starredNavPill("#july-31-test", "4&nbsp;&middot; 31 July test", outstandingCandidates, "text-bg-warning text-dark"))
		fmt.Fprintf(w, `<div id="starred-overview" class="row g-3 mb-4">
<div class="col-xl-5"><div class="card shadow-sm h-100"><div class="card-header"><span class="fw-semibold">Imported season data</span>%s</div><div class="card-body"><div class="row g-2 row-cols-2">%s%s%s%s</div></div></div></div>
<div class="col-xl-7"><div class="card shadow-sm h-100"><div class="card-header"><span class="fw-semibold">Action queue — what needs a decision</span>%s</div><div class="card-body"><div class="row g-2 row-cols-2 row-cols-md-3 row-cols-lg-5">%s%s%s%s%s</div></div></div></div>
</div>`,
			starredHelpIcon("Imported season data", "How much source data has been imported for this season. Every check on this page is only as complete as these numbers — if scorecards are still pending, run the import in step 1 before trusting the results."),
			starredDataTileHTML("List A at cutoff", "List A", "Players on the published List A on the review date. A List A player may only play for their club's 1st XI in League and Cup matches.", strconv.Itoa(currentA), "", fmt.Sprintf("/admin/starred-players?season=%d&view=list-a#card-detail", year), "View players"),
			starredDataTileHTML("List B at cutoff", "List B", "Players on the published List B on the review date. A List B player may play 1st or 2nd XI, but not lower.", strconv.Itoa(currentB), "", fmt.Sprintf("/admin/starred-players?season=%d&view=list-b#card-detail", year), "View players"),
			starredDataTileHTML("Scorecards", "Scorecards", "Men's open-age match scorecards imported from Play-Cricket through 31 July. Pending fixtures have not been imported yet — run the import in step 1 to fetch them.", strconv.Itoa(matchCount), fmt.Sprintf("%d pending import", pendingCount), fmt.Sprintf("/admin/starred-players?season=%d&view=scorecards#card-detail", year), "View scorecards"),
			starredDataTileHTML("Appearances", "Appearances", "Individual player entries taken from the imported scorecards. These rows are the evidence behind every check on this page.", strconv.Itoa(len(reviewApps)), "", fmt.Sprintf("/admin/starred-players?season=%d&view=appearances#card-detail", year), "View appearances"),
			starredHelpIcon("Action queue", "Everything that currently needs a human decision, in the order it is best worked through. Each tile links to its section below; when every tile is green there is nothing outstanding."),
			starredActionTileHTML(unmappedCount, "Identities to match", "Identities to match", "Published players not yet linked to a Play-Cricket ID. Match these first — breach detection is unreliable for unmatched players whose scorecard name is spelt differently.", "#identity-matches", "Match now", "info"),
			starredActionTileHTML(len(issues), "Amendments to review", "Amendments", "Dated changes to the published sheet that the importer could not apply automatically. A person needs to decide what each one means.", "#amendments", "Review", "secondary"),
			starredActionTileHTML(len(outstandingBreaches), "Potential breaches", "Potential breaches", "Appearances by starred players below their permitted team in League or Cup matches. Each needs to be accepted and closed, or escalated as a letter to the club captain.", "#potential-breaches", "Review evidence", "danger"),
			starredActionTileHTML(clubIssueCount, "Club list issues", "Club list issues", "Clubs whose published list is missing, or does not have the size the rules require (standard 5, reduced List B 8, large List B 16).", "#club-lists", "View clubs", "warning"),
			starredActionTileHTML(outstandingCandidates, "Unstarred ≥ 50%", "31 July candidates", "Players who are not starred but played half or more of their league cricket in the top two XIs by 31 July. They may need adding to a list — review or accept each one.", "#july-31-test", "View calculation", "warning"))
		s.renderStarredCardDetail(w, ctx, year, cutoff, strings.TrimSpace(r.URL.Query().Get("view")), periods, reviewApps, mappings, matchCount, len(reviewApps), r)
		fmt.Fprintf(w, `<div id="sync" class="card shadow-sm mb-4"><div class="card-header">%s</div><div class="card-body d-flex flex-wrap gap-3">
<form method="post" action="/admin/starred-players/sync-list"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><button class="btn btn-primary">Sync published list</button><div class="form-text">Imports base lists and applies dated amendments. Automatic refresh: Mondays at 03:00 through 31 July.</div></form>
<form id="starred-scorecard-sync-form" method="post" action="/admin/starred-players/sync-appearances"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><button id="starred-scorecard-sync-button" class="btn btn-primary">Import all pending scorecards</button><div class="form-text">Runs automatically in batches of 50. Keep this page open until complete.</div><div id="starred-scorecard-sync-progress" class="small mt-2" aria-live="polite"></div></form>
<div class="w-100 small text-muted border-top pt-2">%s</div>
</div></div>`,
			starredSectionTitle("Step 1", "Import data", "Bring in the published lists and the Play-Cricket scorecards. Both imports are safe to repeat — only new or changed data is fetched.",
				"Where the data comes from", "Two sources feed this page: the published GMCL List A/List B sheet, and Play-Cricket scorecards for every open-age fixture through 31 July. Sync the list first, then import scorecards. A weekly automatic refresh also runs on Mondays at 03:00 during the season, so manual imports are usually only needed for the initial backfill or an urgent check."),
			escapeHTML(csrf), year, escapeHTML(csrf), year, escapeHTML(automaticSyncStatus))
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

		fmt.Fprintf(w, `<div id="identity-matches" class="card shadow-sm mb-4"><div class="card-header">%s</div>`,
			starredSectionTitle("Step 2", "Match player identities", "Confirmed matches leave this action list immediately. The mapping and a separate administrator audit event are retained.",
				"Why identities must be matched", "The published lists only give a player's name, but scorecards identify players by a Play-Cricket ID. Confirming a match ties every appearance to the right published entry, so breaches are detected even when a name is spelt differently on a scorecard. Suggested matches are ranked by name similarity; use the search below for anyone not suggested."))
		if len(suggestions) > 0 {
			fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm align-middle mb-0"><caption class="caption-top px-3 pb-1 fw-semibold">Suggested from scorecards</caption><thead><tr><th>Club</th><th>Published name</th><th>Play-Cricket candidate</th><th></th></tr></thead><tbody>`)
			for i, x := range suggestions {
				if i >= 100 {
					break
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s <code>%d</code></td><td><form method="post" action="/admin/starred-players/mapping"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="club_key" value="%s"><input type="hidden" name="player_key" value="%s"><input type="hidden" name="candidate_id" value="%d"><input type="hidden" name="candidate_name" value="%s"><button class="btn btn-sm btn-outline-primary">Confirm</button></form></td></tr>`, escapeHTML(x.ClubName), escapeHTML(x.StarredName), escapeHTML(x.CandidateName), x.CandidateID, escapeHTML(csrf), year, escapeHTML(x.ClubKey), escapeHTML(x.StarredPlayerKey), x.CandidateID, escapeHTML(x.CandidateName))
			}
			fmt.Fprint(w, `</tbody></table></div>`)
		}
		fmt.Fprint(w, `<div class="card-body border-top"><h6 class="mb-2">Search all imported scorecards</h6><p class="small text-muted">Choose the unmatched published player, then search by any part of a scorecard name or by Play-Cricket player ID.</p>`)
		if loadErr != nil {
			fmt.Fprint(w, `<div class="alert alert-warning mb-0">Synchronise the published list before matching identities.</div></div></div>`)
		} else if len(unmappedPeriods) == 0 {
			fmt.Fprint(w, `<div class="alert alert-success mb-0">Every active published player has an accepted identity mapping.</div></div></div>`)
		} else {
			fmt.Fprintf(w, `<form method="get" action="/admin/starred-players" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><div class="col-lg-5"><label class="form-label" for="mapping-source">Published starred player</label><select class="form-select" id="mapping-source" name="mapping_source" required><option value="">Choose a player</option>`, year)
			for _, period := range unmappedPeriods {
				sourceID := starredMappingSourceID(period)
				selected := ""
				if sourceID == mappingSource {
					selected = " selected"
				}
				fmt.Fprintf(w, `<option value="%s"%s>%s — %s (List %s)</option>`, escapeHTML(sourceID), selected, escapeHTML(period.ClubName), escapeHTML(period.PlayerName), escapeHTML(period.ListType))
			}
			fmt.Fprintf(w, `</select></div><div class="col-lg-5"><label class="form-label" for="mapping-query">Scorecard name or player ID</label><input class="form-control" id="mapping-query" name="mapping_q" value="%s" minlength="2" maxlength="100" placeholder="e.g. Phillips" required></div><div class="col-lg-2"><button class="btn btn-primary w-100">Search</button></div></form>`, escapeHTML(mappingQuery))
			if mappingQuery != "" && !mappingSourceValid {
				fmt.Fprint(w, `<div class="alert alert-warning mt-3 mb-0">Choose an unmatched published player before searching.</div>`)
			} else if mappingSourceValid && len([]rune(mappingQuery)) < 2 && mappingQuery != "" {
				fmt.Fprint(w, `<div class="alert alert-warning mt-3 mb-0">Enter at least two characters.</div>`)
			} else if mappingSourceValid && len([]rune(mappingQuery)) >= 2 {
				fmt.Fprintf(w, `<div class="mt-4"><h6>Potential matches for %s — %s</h6><div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Scorecard player</th><th>Player ID</th><th>Clubs found</th><th>Scorecards</th><th>Dates</th><th></th></tr></thead><tbody>`, escapeHTML(selectedMappingPeriod.ClubName), escapeHTML(selectedMappingPeriod.PlayerName))
				if len(identitySearchResults) == 0 {
					fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No scorecard identities matched this search.</td></tr>`)
				}
				for _, candidate := range identitySearchResults {
					dates := ""
					if !candidate.FirstSeen.IsZero() {
						dates = candidate.FirstSeen.Format("02 Jan") + " – " + candidate.LastSeen.Format("02 Jan 2006")
					}
					fmt.Fprintf(w, `<tr><td>%s</td><td><code>%d</code></td><td>%s</td><td>%d</td><td>%s</td><td><form method="post" action="/admin/starred-players/mapping"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="season" value="%d"><input type="hidden" name="club_key" value="%s"><input type="hidden" name="player_key" value="%s"><input type="hidden" name="candidate_id" value="%d"><input type="hidden" name="candidate_name" value="%s"><button class="btn btn-sm btn-outline-primary">Confirm</button></form></td></tr>`, escapeHTML(candidate.PlayerName), candidate.PlayerID, escapeHTML(strings.Join(candidate.ClubNames, ", ")), candidate.MatchCount, escapeHTML(dates), escapeHTML(csrf), year, escapeHTML(selectedMappingPeriod.ClubKey), escapeHTML(selectedMappingPeriod.PlayerKey), candidate.PlayerID, escapeHTML(candidate.PlayerName))
				}
				fmt.Fprint(w, `</tbody></table></div></div>`)
			}
			fmt.Fprint(w, `</div></div>`)
		}
		amendmentsHeader := starredSectionTitle("", "Amendments requiring review", "Dated changes to the published sheet that could not be applied automatically. Nothing is guessed — decide what each change was meant to do, then correct the published sheet and re-sync.",
			"What is an amendment issue", "Clubs change their lists during the season by adding dated amendments to the published sheet. When the wording of an amendment is ambiguous — for example a name that matches nobody on the list — the importer keeps it here for a person instead of guessing. The published text column shows exactly what the sheet says.")
		if len(issues) > 0 {
			fmt.Fprintf(w, `<div id="amendments" class="card border-warning shadow-sm mb-4"><div class="card-header">%s</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Club</th><th>Amendment</th><th>Published text</th><th>Reason</th></tr></thead><tbody>`, amendmentsHeader)
			for _, i := range issues {
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%s</td><td>%s</td></tr>`, escapeHTML(i.ClubName), i.Sequence, escapeHTML(i.RawValue), escapeHTML(i.Reason))
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		} else {
			fmt.Fprintf(w, `<div id="amendments" class="card shadow-sm mb-4"><div class="card-header">%s</div><div class="card-body text-success py-3">&#10003; Every published amendment was applied automatically — nothing needs review.</div></div>`, amendmentsHeader)
		}

		breachGroups := groupStarredBreaches(filteredBreaches)
		fmt.Fprintf(w, `<div id="potential-breaches" class="card shadow-sm mb-4"><div class="card-header">%s</div>`,
			starredSectionTitle("Step 3", "Potential List A / List B breaches by division", "Accept and close a finding where no offence should be pursued, or create an editable letter for separate approval before it is sent.",
				"What counts as a breach", "Each row is one appearance where a starred player turned out below their permitted team in a League or Cup match: List A below the 1st XI, or List B below the 2nd XI. Open the match to see the full team sheets, then either accept and close the finding (no offence) or draft a letter to the club captain — letters always need a separate approval before anything is sent. Findings tagged Junior may qualify for an exemption, so verify before escalating."))
		breachExportQuery := url.Values{"season": {strconv.Itoa(year)}, "export": {"breaches-csv"}}
		if value := strings.TrimSpace(r.URL.Query().Get("breach_from")); value != "" {
			breachExportQuery.Set("breach_from", value)
		}
		if value := strings.TrimSpace(r.URL.Query().Get("breach_to")); value != "" {
			breachExportQuery.Set("breach_to", value)
		}
		fmt.Fprintf(w, `<div class="card-body border-bottom"><form method="get" action="/admin/starred-players" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><div class="col-sm-3 col-lg-2"><label class="form-label" for="breach-from">From date</label><input class="form-control" id="breach-from" type="date" name="breach_from" value="%s"></div><div class="col-sm-3 col-lg-2"><label class="form-label" for="breach-to">To date</label><input class="form-control" id="breach-to" type="date" name="breach_to" value="%s"></div><div class="col-auto"><button class="btn btn-primary">Filter breaches</button></div><div class="col-auto"><a class="btn btn-outline-primary" href="/admin/starred-players?season=%d#potential-breaches">Clear dates</a></div><div class="col-auto"><a class="btn btn-outline-primary" href="/admin/starred-players?%s">Export CSV</a></div></form><div class="form-text">Showing %d of %d potential breaches. Either date can be used on its own, or use both for an inclusive range.</div>`, year, escapeHTML(r.URL.Query().Get("breach_from")), escapeHTML(r.URL.Query().Get("breach_to")), year, escapeHTML(breachExportQuery.Encode()), len(filteredBreaches), len(outstandingBreaches))
		if breachFilterErr != nil {
			fmt.Fprintf(w, `<div class="alert alert-danger mt-3 mb-0">%s</div>`, escapeHTML(breachFilterErr.Error()))
		}
		fmt.Fprint(w, `</div>`)
		if len(breachGroups) > 0 {
			fmt.Fprint(w, `<div class="card-body border-bottom d-flex flex-wrap gap-2">`)
			for index, group := range breachGroups {
				fmt.Fprintf(w, `<a class="btn btn-sm btn-outline-primary" href="#starred-division-%d">%s — %s <span class="badge bg-secondary">%d</span></a>`, index, escapeHTML(group.Day), escapeHTML(group.Division), len(group.Breaches))
			}
			fmt.Fprint(w, `</div>`)
		}
		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Date</th><th>Club</th><th>Player</th><th>List</th><th>Team</th><th>Format</th><th>Evidence</th><th>Review</th></tr></thead><tbody>`)
		if len(filteredBreaches) == 0 {
			fmt.Fprint(w, `<tr><td colspan="8" class="text-center text-muted py-3">No potential breaches found for the selected dates.</td></tr>`)
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

		clubListHeader := starredSectionTitle("", "Club list completeness", "Only clubs with a problem are listed here.",
			"How list sizes are checked", "Every club must publish a starred list of the size the rules require: 5 players as standard, 8 under the reduced List B option, or 16 under the large List B option — and must have submitted its form. Clubs listed here either sent no form or have the wrong number of active players after amendments.")
		if clubIssueCount > 0 {
			fmt.Fprintf(w, `<div id="club-lists" class="card border-warning shadow-sm mb-4"><div class="card-header">%s</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Club</th><th>Active players</th><th>Expected</th><th>Reason</th></tr></thead><tbody>`, clubListHeader)
			for _, x := range clubStatuses {
				if x.Compliant {
					continue
				}
				fmt.Fprintf(w, `<tr><td>%s</td><td>%d</td><td>%d</td><td>%s</td></tr>`, escapeHTML(x.ClubName), x.CurrentCount, x.ExpectedCount, escapeHTML(x.Reason))
			}
			fmt.Fprint(w, `</tbody></table></div></div>`)
		} else {
			fmt.Fprintf(w, `<div id="club-lists" class="card shadow-sm mb-4"><div class="card-header">%s</div><div class="card-body text-success py-3">&#10003; Every club's published list has the required size and a submitted form.</div></div>`, clubListHeader)
		}

		fmt.Fprintf(w, `<div id="july-31-test" class="card shadow-sm mb-4"><div class="card-header">%s</div><div class="table-responsive"><table class="table table-sm table-hover align-middle mb-0"><thead><tr><th>Club</th><th>Player</th><th>1st + 2nd XI</th><th>All league</th><th>Percentage</th><th>Actions</th></tr></thead><tbody>`,
			starredSectionTitle("Step 4", "31 July league-appearance test", fmt.Sprintf("List B reviews use combined 1st XI and 2nd XI league appearances. Review a player's games or accept and close the review. Accepted decisions leave this outstanding list and remain in the audit trail. %d accepted.", acceptedCandidateCount),
				"How the 31 July test works", "At 31 July every unstarred player's league appearances are checked: 1st XI + 2nd XI league games divided by all their league games. A share of 50% or more means the player may need adding to a starred list. Cup matches are deliberately excluded from this calculation. Nothing is changed automatically — review the player's games or accept and close the review."))
		shown := 0
		for _, c := range eval.Candidates {
			if c.AlreadyStarred || candidateReviewStates[starredCandidateKey(year, c)].Status == "accepted" {
				continue
			}
			shown++
			appearanceSearch := starredAppearanceSearch(c.PlayerName, c.PlayerID)
			fmt.Fprintf(w, `<tr><td>%s</td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a><div class="small text-muted">List B review</div></td><td>%d</td><td>%d</td><td>%.1f%%</td><td>%s</td></tr>`, escapeHTML(c.ClubName), year, url.QueryEscape(appearanceSearch), escapeHTML(c.PlayerName), c.TopTwoXILeague, c.AllLeague, c.Percentage*100, starredCandidateActionsHTML(c, csrf, year))
		}
		if shown == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-3">No unstarred candidates currently meet the threshold.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)
		fmt.Fprint(w, `</div>`)
		pageFooterWithScript(w, starredPopoverInitScript)
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
	case "player-review":
		s.renderStarredPlayerReview(w, ctx, year, cutoff, periods, reviewApps, mappings, r)
	case "club-list":
		s.renderStarredClubList(w, ctx, year, cutoff, periods, reviewApps, mappings, r)
	case "list-a", "list-b":
		listType := "A"
		if view == "list-b" {
			listType = "B"
		}
		fmt.Fprintf(w, `List %s players at 31 July%s</span><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d">Close</a></div>`, listType,
			starredHelpIcon("List "+listType+" players", "Everyone active on this list at the review date, built from the published sheet plus any dated amendments. Effective from shows when the player joined the list; the source column shows whether the entry came from the original list or an amendment. Click a player to see their imported appearances."), year)
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
			appearanceSearch := starredAppearanceSearch(period.PlayerName, starredMappedPlayerID(period, mappings))
			fmt.Fprintf(w, `<tr><td><a href="/admin/starred-players?season=%d&amp;view=club-list&amp;club=%s#card-detail">%s</a></td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>`, year, url.QueryEscape(period.ClubKey), escapeHTML(period.ClubName), year, url.QueryEscape(appearanceSearch), escapeHTML(period.PlayerName), period.ValidFrom.Format("02 Jan 2006"), escapeHTML(strings.Join(period.Tags, ", ")), escapeHTML(period.SourceKind))
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
		fmt.Fprintf(w, `Open-age scorecards through 31 July%s</span><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d">Close</a></div>`,
			starredHelpIcon("Imported scorecards", "All men's open-age scorecards imported from Play-Cricket for this season, capped at 31 July. Open a match to see both team sheets with each player's starred status on the match date — this is the evidence used by the breach review."), year)
		fmt.Fprintf(w, `<div class="card-body border-bottom"><form method="get" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><input type="hidden" name="view" value="scorecards"><div class="col-md-8"><label class="form-label small" for="scorecard-search">Search club, team, competition or match ID</label><input id="scorecard-search" class="form-control" name="q" value="%s" placeholder="e.g. Droylsden, 2nd XI or 7458963"></div><div class="col-auto"><button class="btn btn-primary">Search</button></div><div class="col-auto"><a class="btn btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=scorecards#card-detail">Clear</a></div></form><div class="form-text">Only classified men's open-age XI fixtures are shown; women's and junior scorecards are excluded from this review.</div></div>`, year, escapeHTML(query), year)
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
			fmt.Fprintf(w, `Match %d</span><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=scorecards#card-detail">Back to scorecards</a></div><div class="alert alert-danger m-3">%s</div></div>`, matchID, year, escapeHTML(err.Error()))
			return
		}
		fmt.Fprintf(w, `%s v %s</span><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=scorecards#card-detail">Back to scorecards</a></div>`, escapeHTML(homeClub), escapeHTML(awayClub), year)
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
			appearanceSearch := starredAppearanceSearch(appearance.PlayerName, appearance.PlayerID)
			fmt.Fprintf(w, `<tr><td><strong>%s</strong><div class="small text-muted">%s</div></td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td><td><code>%d</code></td><td>%s</td><td>%s</td></tr>`, escapeHTML(appearance.ClubName), escapeHTML(appearance.TeamName), year, url.QueryEscape(appearanceSearch), escapeHTML(appearance.PlayerName), appearance.PlayerID, escapeHTML(strings.Join(roles, ", ")), listBadge)
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
		fmt.Fprintf(w, `Open-age player appearances through 31 July%s</span><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d">Close</a></div>`,
			starredHelpIcon("Player appearances", "One row per player per scorecard, taken from the imported team sheets. Search by name, club, team or Play-Cricket ID to check an individual player's season — every compliance check on this page is calculated from these rows."), year)
		fmt.Fprintf(w, `<div class="card-body border-bottom"><form method="get" class="row g-2 align-items-end"><input type="hidden" name="season" value="%d"><input type="hidden" name="view" value="appearances"><div class="col-md-8"><label class="form-label small" for="appearance-search">Search player, club, team or player ID</label><input id="appearance-search" class="form-control" name="q" value="%s" placeholder="e.g. player name, club or ID"></div><div class="col-auto"><button class="btn btn-primary">Search</button></div><div class="col-auto"><a class="btn btn-outline-primary" href="/admin/starred-players?season=%d&amp;view=appearances#card-detail">Clear</a></div></form></div>`, year, escapeHTML(query), year)
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

func activeUnmappedStarredPeriods(periods []starred.Period, mappings []starred.IdentityMapping, cutoff time.Time) []starred.Period {
	mapped := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		mapped[mapping.ClubKey+"|"+mapping.StarredPlayerKey] = struct{}{}
	}
	seen := make(map[string]struct{})
	out := make([]starred.Period, 0)
	for _, period := range periods {
		if cutoff.Before(period.ValidFrom) || (period.ValidTo != nil && !cutoff.Before(*period.ValidTo)) {
			continue
		}
		key := period.ClubKey + "|" + period.PlayerKey
		if _, exists := mapped[key]; exists {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, period)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClubName != out[j].ClubName {
			return out[i].ClubName < out[j].ClubName
		}
		if out[i].ListType != out[j].ListType {
			return out[i].ListType < out[j].ListType
		}
		return out[i].PlayerName < out[j].PlayerName
	})
	return out
}

func starredMappingSourceID(period starred.Period) string {
	return period.ClubKey + ":" + period.PlayerKey
}

func findUnmappedStarredPeriod(periods []starred.Period, sourceID string) (starred.Period, bool) {
	for _, period := range periods {
		if starredMappingSourceID(period) == sourceID {
			return period, true
		}
	}
	return starred.Period{}, false
}

func starredAppearanceSearch(playerName string, playerID int64) string {
	if playerID > 0 {
		return strconv.FormatInt(playerID, 10)
	}
	return playerName
}

func starredMappedPlayerID(period starred.Period, mappings []starred.IdentityMapping) int64 {
	for _, mapping := range mappings {
		if mapping.ClubKey == period.ClubKey && mapping.StarredPlayerKey == period.PlayerKey {
			return mapping.PlayerID
		}
	}
	return 0
}

func starredTeamCounts(periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping) map[string]map[int]int {
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
			if appearance.ClubKey != period.ClubKey || appearance.TeamLevel < 1 {
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

func activeStarredClubNames(periods []starred.Period, cutoff time.Time) map[string]string {
	clubNames := make(map[string]string)
	for _, period := range periods {
		if !cutoff.Before(period.ValidFrom) && (period.ValidTo == nil || cutoff.Before(*period.ValidTo)) {
			clubNames[period.ClubKey] = period.ClubName
		}
	}
	return clubNames
}

func remapStarredAppearanceClubs(appearances []starred.Appearance, publishedToAppearance map[string]string, publishedClubNames map[string]string) []starred.Appearance {
	appearanceToPublished := make(map[string]string)
	for publishedClubKey, appearanceClubKey := range publishedToAppearance {
		if publishedClubKey != "" && appearanceClubKey != "" {
			appearanceToPublished[appearanceClubKey] = publishedClubKey
		}
	}
	out := make([]starred.Appearance, len(appearances))
	copy(out, appearances)
	for index := range out {
		publishedClubKey := appearanceToPublished[out[index].ClubKey]
		if publishedClubKey == "" {
			continue
		}
		out[index].ClubKey = publishedClubKey
		if clubName := publishedClubNames[publishedClubKey]; clubName != "" {
			out[index].ClubName = clubName
		}
	}
	return out
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

func (s *Server) loadStarredAppearanceClubOverrides(ctx context.Context, year int) map[string]string {
	overrides := make(map[string]string)
	rows, err := s.DB.Query(ctx, `SELECT club_key,COALESCE(appearance_club_key,'') FROM starred_club_division_overrides WHERE season_year=$1`, year)
	if err != nil {
		return overrides
	}
	defer rows.Close()
	for rows.Next() {
		var publishedClubKey, appearanceClubKey string
		if rows.Scan(&publishedClubKey, &appearanceClubKey) == nil && appearanceClubKey != "" {
			overrides[publishedClubKey] = appearanceClubKey
		}
	}
	return overrides
}

type starredAppearanceClubOption struct {
	Key        string
	Name       string
	MatchCount int
}

func (s *Server) loadStarredAppearanceClubOptions(ctx context.Context, year int, cutoff time.Time) []starredAppearanceClubOption {
	rows, err := s.DB.Query(ctx, `
		SELECT club_key,MIN(club_name),COUNT(DISTINCT play_cricket_match_id)::int
		FROM starred_appearances
		WHERE season_year=$1 AND match_date <= $2::date AND team_level > 0
		  AND CONCAT_WS(' ',competition_name,club_name,team_name) !~* '(wom(en|an)|ladies|female|girls)'
		GROUP BY club_key ORDER BY MIN(club_name)`, year, cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var options []starredAppearanceClubOption
	for rows.Next() {
		var option starredAppearanceClubOption
		if rows.Scan(&option.Key, &option.Name, &option.MatchCount) == nil {
			options = append(options, option)
		}
	}
	return options
}

func (s *Server) renderStarredClubList(w http.ResponseWriter, ctx context.Context, year int, cutoff time.Time, periods []starred.Period, appearances []starred.Appearance, mappings []starred.IdentityMapping, r *http.Request) {
	selectedClub := strings.TrimSpace(r.URL.Query().Get("club"))
	selectedDivision := strings.TrimSpace(r.URL.Query().Get("division"))
	clubNames := activeStarredClubNames(periods, cutoff)
	overrides := s.loadStarredDivisionOverrides(ctx, year)
	appearanceClubOverrides := s.loadStarredAppearanceClubOverrides(ctx, year)
	appearanceClubOptions := s.loadStarredAppearanceClubOptions(ctx, year, cutoff)
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

	fmt.Fprintf(w, `Starred list by club at 31 July%s</span><a class="btn btn-sm btn-outline-primary" href="/admin/starred-players?season=%d">Close</a></div>`,
		starredHelpIcon("Starred list by club", "The current List A and List B players for one club, with appearance counts by team so you can see where each starred player has actually been playing. Clubs are grouped by their Saturday 1st XI league division; use the correction panel if a club is shown in the wrong division or its Play-Cricket data is filed under a different club name."), year)
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
	fmt.Fprintf(w, `<div class="card-body border-bottom bg-light"><details><summary class="fw-semibold" style="cursor:pointer">Correct a club's division or Play-Cricket data assignment</summary><form method="post" action="/admin/starred-players/mapping" class="row g-2 align-items-end mt-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="mapping_type" value="division"><input type="hidden" name="season" value="%d"><div class="col-md-3"><label class="form-label" for="override-club">Published-list club</label><select id="override-club" class="form-select" name="club_key" required onchange="document.getElementById('override-division').value=this.options[this.selectedIndex].dataset.division||'';document.getElementById('override-appearance-club').value=this.options[this.selectedIndex].dataset.appearance||''"><option value="">Choose a club…</option>`, escapeHTML(middleware.CSRFToken(r)), year)
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
		if appearanceClubOverrides[clubKey] != "" {
			marker += " — Play-Cricket data linked"
		}
		fmt.Fprintf(w, `<option value="%s" data-division="%s" data-appearance="%s"%s>%s%s</option>`, escapeHTML(clubKey), escapeHTML(clubDivision[clubKey]), escapeHTML(appearanceClubOverrides[clubKey]), selected, escapeHTML(clubNames[clubKey]), escapeHTML(marker))
	}
	fmt.Fprintf(w, `</select></div><div class="col-md-3"><label class="form-label" for="override-division">Correct Saturday division</label><input id="override-division" class="form-control" name="division_name" list="starred-division-options" value="%s" placeholder="e.g. Premier 1" required><datalist id="starred-division-options">`, escapeHTML(clubDivision[selectedClub]))
	for _, division := range divisions {
		if division.Label != "Unassigned / no Saturday division" {
			fmt.Fprintf(w, `<option value="%s"></option>`, escapeHTML(division.Label))
		}
	}
	fmt.Fprint(w, `</datalist></div><div class="col-md-3"><label class="form-label" for="override-appearance-club">Play-Cricket club data</label><select id="override-appearance-club" class="form-select" name="appearance_club_key"><option value="">Use normal club-name matching</option>`)
	for _, option := range appearanceClubOptions {
		selected := ""
		if option.Key == appearanceClubOverrides[selectedClub] {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s (%d matches)</option>`, escapeHTML(option.Key), selected, escapeHTML(option.Name), option.MatchCount)
	}
	fmt.Fprint(w, `</select><div class="form-text">Choose this when a corrected club still shows no games.</div></div><div class="col-auto"><button class="btn btn-primary" name="override_action" value="save">Save correction</button></div><div class="col-auto"><button class="btn btn-outline-primary" name="override_action" value="clear" formnovalidate>Use automatic assignment</button></div></form></details></div>`)
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
	teamCounts := starredTeamCounts(active, appearances, mappings)
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
	fmt.Fprintf(w, `<div class="card-body border-bottom d-flex flex-wrap justify-content-between align-items-center gap-2"><div><h5 class="mb-0">%s</h5><div class="small text-muted">All classified open-age XI appearances from imported scorecards through %s.</div></div><div><span class="badge bg-danger me-1">List A: %d</span><span class="badge bg-primary">List B: %d</span></div></div>`, escapeHTML(clubName), cutoff.Format("02 January 2006"), listACount, listBCount)
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
		appearanceSearch := starredAppearanceSearch(period.PlayerName, starredMappedPlayerID(period, mappings))
		fmt.Fprintf(w, `<tr><td><span class="badge %s">List %s</span></td><td><a href="/admin/starred-players?season=%d&amp;view=appearances&amp;q=%s#card-detail">%s</a></td>`, badgeClass, escapeHTML(period.ListType), year, url.QueryEscape(appearanceSearch), escapeHTML(period.PlayerName))
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

func countOutstandingUnstarredCandidates(year int, candidates []starred.Candidate, states map[string]starredCandidateReviewState) int {
	n := 0
	for _, candidate := range candidates {
		if !candidate.AlreadyStarred && states[starredCandidateKey(year, candidate)].Status != "accepted" {
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
		clubKey := strings.TrimSpace(r.FormValue("club_key"))
		playerKey := strings.TrimSpace(r.FormValue("player_key"))
		periods, appearances, mappings, _, loadErr := starred.LoadEvaluationInputs(ctx, s.DB, year)
		if loadErr != nil {
			redirectStarredAnchor(w, r, year, "", "Could not validate the identity mapping: "+loadErr.Error(), "identity-matches")
			return
		}
		cutoff := starred.ReviewCutoff(year, time.Now())
		target, targetFound := starred.Period{}, false
		for _, period := range activeUnmappedStarredPeriods(periods, mappings, cutoff) {
			if period.ClubKey == clubKey && period.PlayerKey == playerKey {
				target, targetFound = period, true
				break
			}
		}
		if !targetFound {
			redirectStarredAnchor(w, r, year, "", "That published player is no longer awaiting an identity match.", "identity-matches")
			return
		}
		reviewAppearances := make([]starred.Appearance, 0, len(appearances))
		for _, appearance := range appearances {
			if appearance.TeamLevel > 0 && !appearance.MatchDate.After(cutoff) && !starred.IsWomensAppearance(appearance) {
				reviewAppearances = append(reviewAppearances, appearance)
			}
		}
		candidateName := ""
		for _, candidate := range starred.SearchAppearanceIdentities(reviewAppearances, strconv.FormatInt(id, 10), 200) {
			if candidate.PlayerID == id {
				candidateName = candidate.PlayerName
				break
			}
		}
		if candidateName == "" {
			redirectStarredAnchor(w, r, year, "", "That Play-Cricket identity was not found in the imported scorecards.", "identity-matches")
			return
		}
		adminID := adminIDForRequest(r)
		err = starred.SaveIdentityMapping(ctx, s.DB, year, clubKey, playerKey, id, candidateName, adminID)
		if err != nil {
			redirectStarredAnchor(w, r, year, "", err.Error(), "identity-matches")
			return
		}
		var auditAdminID *int32
		if adminID > 0 {
			auditAdminID = &adminID
		}
		s.audit(ctx, r, "admin", auditAdminID, "starred_identity_mapping_confirmed", "starred_identity_mapping", nil, map[string]any{
			"season": year, "club_key": clubKey, "club_name": target.ClubName,
			"starred_player_key": playerKey, "starred_player_name": target.PlayerName,
			"play_cricket_player_id": id, "play_cricket_player_name": candidateName,
		})
		redirectStarredAnchor(w, r, year, "Identity mapping confirmed for "+target.PlayerName+". It has been removed from the action list.", "", "identity-matches")
	}
}

func (s *Server) handleStarredDivisionOverride(w http.ResponseWriter, r *http.Request, year int) {
	clubKey := strings.TrimSpace(r.FormValue("club_key"))
	division := starredDivisionLabel(strings.TrimSpace(r.FormValue("division_name")), "")
	appearanceClubKey := strings.TrimSpace(r.FormValue("appearance_club_key"))
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
	appearanceClubName := ""
	if action == "save" && appearanceClubKey != "" {
		err = s.DB.QueryRow(ctx, `SELECT club_name FROM starred_appearances WHERE season_year=$1 AND club_key=$2 AND LOWER(COALESCE(playing_day,''))='saturday' ORDER BY match_date DESC LIMIT 1`, year, appearanceClubKey).Scan(&appearanceClubName)
		if err != nil {
			http.Error(w, "selected Play-Cricket club data was not found", http.StatusBadRequest)
			return
		}
	}
	adminID := s.resolveAdminID(r)
	message := "Division assignment corrected for " + clubName + "."
	if action == "clear" {
		_, err = s.DB.Exec(ctx, `DELETE FROM starred_club_division_overrides WHERE season_year=$1 AND club_key=$2`, year, clubKey)
		division = ""
		message = "Manual division assignment removed for " + clubName + "."
	} else {
		_, err = s.DB.Exec(ctx, `INSERT INTO starred_club_division_overrides(season_year,club_key,club_name,division_name,appearance_club_key,appearance_club_name,updated_by,updated_at) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,now()) ON CONFLICT(season_year,club_key) DO UPDATE SET club_name=EXCLUDED.club_name,division_name=EXCLUDED.division_name,appearance_club_key=EXCLUDED.appearance_club_key,appearance_club_name=EXCLUDED.appearance_club_name,updated_by=EXCLUDED.updated_by,updated_at=now()`, year, clubKey, clubName, division, appearanceClubKey, appearanceClubName, adminID)
	}
	if err != nil {
		redirectStarred(w, r, year, "", "Could not save division assignment: "+err.Error())
		return
	}
	s.audit(ctx, r, "admin", adminID, "starred_division_override_"+action, "starred_club_division_override", nil, map[string]any{"season": year, "club": clubName, "division": division, "appearance_club": appearanceClubName})
	q := url.Values{"season": {strconv.Itoa(year)}, "view": {"club-list"}, "club": {clubKey}, "message": {message}}
	if division != "" {
		q.Set("division", division)
	}
	http.Redirect(w, r, "/admin/starred-players?"+q.Encode()+"#card-detail", http.StatusSeeOther)
}

func redirectStarred(w http.ResponseWriter, r *http.Request, year int, message, errMsg string) {
	redirectStarredAnchor(w, r, year, message, errMsg, "")
}

func redirectStarredAnchor(w http.ResponseWriter, r *http.Request, year int, message, errMsg, anchor string) {
	q := url.Values{}
	q.Set("season", strconv.Itoa(year))
	if message != "" {
		q.Set("message", message)
	}
	if errMsg != "" {
		q.Set("error", errMsg)
	}
	destination := "/admin/starred-players?" + q.Encode()
	if anchor != "" {
		destination += "#" + anchor
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
