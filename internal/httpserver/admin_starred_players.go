package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
			if !appearance.MatchDate.After(cutoff) {
				reviewApps = append(reviewApps, appearance)
			}
		}
		eval := starred.Evaluation{}
		var suggestions []starred.MappingSuggestion
		if loadErr == nil {
			eval = starred.Evaluate(periods, reviewApps, mappings, cutoff)
			suggestions = starred.SuggestMappings(periods, reviewApps, mappings, cutoff)
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
		_ = s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM starred_match_imports WHERE season_year=$1 AND match_date <= $2::date`, year, cutoff).Scan(&matchCount)
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
<form method="get" class="d-flex gap-2"><input class="form-control" style="width:110px" type="number" name="season" value="%d"><button class="btn btn-outline-primary">Load</button></form></div>`, year)
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
<div class="col-md-2"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">List A at cutoff</div><div class="display-6">%d</div></div></div></div>
<div class="col-md-2"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">List B at cutoff</div><div class="display-6">%d</div></div></div></div>
<div class="col-md-2"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Scorecards</div><div class="display-6">%d</div><div class="small text-muted">%d pending</div></div></div></div>
<div class="col-md-2"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Appearances</div><div class="display-6">%d</div></div></div></div>
<div class="col-md-2"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Potential breaches</div><div class="display-6 text-danger">%d</div></div></div></div>
<div class="col-md-2"><div class="card shadow-sm h-100"><div class="card-body"><div class="text-muted small">Unstarred ≥50%%</div><div class="display-6 text-warning">%d</div><div class="small text-muted">%d club list issues</div></div></div></div>
</div>`, currentA, currentB, matchCount, pendingCount, len(reviewApps), len(eval.Breaches), countUnstarredCandidates(eval.Candidates), clubIssueCount)
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

		fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">Potential List A / List B breaches</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Date</th><th>Club</th><th>Player</th><th>List</th><th>Team</th><th>Competition</th><th>Evidence</th></tr></thead><tbody>`)
		if len(eval.Breaches) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-3">No potential breaches found in imported scorecards.</td></tr>`)
		}
		for i, b := range eval.Breaches {
			if i >= 200 {
				break
			}
			evidence := "Review"
			if b.NeedsExemptionReview {
				evidence = "Junior tag — verify exemption"
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td><span class="badge bg-danger">%s</span></td><td>%s</td><td>%s</td><td>%s · match %d</td></tr>`, b.Appearance.MatchDate.Format("02 Jan 2006"), escapeHTML(b.Appearance.ClubName), escapeHTML(b.Appearance.PlayerName), escapeHTML(b.ListType), escapeHTML(b.Appearance.TeamName), escapeHTML(b.Appearance.CompetitionType), escapeHTML(evidence), b.Appearance.MatchID)
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

		fmt.Fprint(w, `<div class="card shadow-sm mb-4"><div class="card-header fw-semibold">30 June league-appearance test</div><div class="table-responsive"><table class="table table-sm table-hover mb-0"><thead><tr><th>Club</th><th>Player</th><th>1st XI</th><th>All league</th><th>Percentage</th><th>Status</th></tr></thead><tbody>`)
		shown := 0
		for _, c := range eval.Candidates {
			if c.AlreadyStarred {
				continue
			}
			shown++
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%.1f%%</td><td><span class="badge bg-warning text-dark">List B review</span></td></tr>`, escapeHTML(c.ClubName), escapeHTML(c.PlayerName), c.FirstXILeague, c.AllLeague, c.Percentage*100)
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
