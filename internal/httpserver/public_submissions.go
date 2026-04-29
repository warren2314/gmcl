package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// handlePublicSubmissionsStatus serves GET /submissions — a public read-only page
// where anyone can select their club and see which teams have submitted this week.
func (s *Server) handlePublicSubmissionsStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		clubID, _ := strconv.Atoi(r.URL.Query().Get("club_id"))

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Submission Status")
		writeCaptainNav(w)
		fmt.Fprint(w, `<div class="container" style="max-width:720px">`)

		// Club selector card
		fmt.Fprintf(w, `
<div class="card card-gmcl shadow-sm mb-4">
  <div class="card-body">
    <h5 class="card-title mb-3">Check submission status</h5>
    <form method="GET" action="/submissions" id="club-form">
      <input type="hidden" name="club_id" id="club_id" value="%d">
      <div class="mb-0 position-relative">
        <label class="form-label">Club</label>
        <input type="text" class="form-control" id="club_search" placeholder="Start typing your club name..." autocomplete="off">
        <div id="club-results" class="list-group position-absolute w-100" style="z-index:1050;max-height:240px;overflow-y:auto;display:none"></div>
      </div>
    </form>
  </div>
</div>`, clubID)

		// Results section
		if clubID > 0 {
			s.renderSubmissionsStatusTable(ctx, w, clubID)
		}

		// JS: club typeahead — on selection auto-submit the form
		fmt.Fprint(w, `
<script>
(function() {
  const clubInput = document.getElementById('club_search');
  const clubIdEl  = document.getElementById('club_id');
  const results   = document.getElementById('club-results');
  let debounce    = null;

  clubInput.addEventListener('input', function() {
    clearTimeout(debounce);
    clubIdEl.value = '';
    const q = this.value.trim();
    if (q.length < 2) { results.style.display = 'none'; return; }
    debounce = setTimeout(function() {
      fetch('/api/clubs/search?q=' + encodeURIComponent(q))
        .then(function(r) { return r.json(); })
        .then(function(clubs) {
          results.innerHTML = '';
          if (!clubs.length) { results.style.display = 'none'; return; }
          clubs.forEach(function(c) {
            const a = document.createElement('a');
            a.href = '#';
            a.className = 'list-group-item list-group-item-action';
            a.textContent = c.name;
            a.addEventListener('click', function(e) {
              e.preventDefault();
              clubInput.value = c.name;
              clubIdEl.value  = c.id;
              results.style.display = 'none';
              document.getElementById('club-form').submit();
            });
            results.appendChild(a);
          });
          results.style.display = 'block';
        });
    }, 250);
  });

  document.addEventListener('click', function(e) {
    if (!results.contains(e.target) && e.target !== clubInput) {
      results.style.display = 'none';
    }
  });
})();
</script>
`)
		fmt.Fprint(w, `</div>`)
		pageFooter(w)
	}
}

func (s *Server) renderSubmissionsStatusTable(ctx context.Context, w http.ResponseWriter, clubID int) {
	// Load current week
	type weekInfo struct {
		ID         int32
		Number     int
		Start, End time.Time
		SeasonName string
	}
	var wk weekInfo
	err := s.DB.QueryRow(ctx, `
		SELECT w.id, w.week_number, w.start_date, w.end_date, se.name
		FROM weeks w
		JOIN seasons se ON se.id = w.season_id
		WHERE se.is_archived = FALSE
		  AND w.start_date <= CURRENT_DATE
		  AND w.end_date   >= CURRENT_DATE
		LIMIT 1
	`).Scan(&wk.ID, &wk.Number, &wk.Start, &wk.End, &wk.SeasonName)
	if err != nil {
		fmt.Fprint(w, `<div class="alert alert-warning">No active week found — check back during the season.</div>`)
		return
	}

	// Load club name
	var clubName string
	if err := s.DB.QueryRow(ctx, `SELECT name FROM clubs WHERE id = $1`, clubID).Scan(&clubName); err != nil {
		fmt.Fprint(w, `<div class="alert alert-danger">Club not found.</div>`)
		return
	}

	// Load teams with submission status
	type teamRow struct {
		Name      string
		Captain   string
		Submitted bool
	}
	rows, err := s.DB.Query(ctx, `
		SELECT t.name,
		       COALESCE(c.full_name, ''),
		       EXISTS (
		           SELECT 1 FROM submissions sub
		           WHERE sub.team_id = t.id AND sub.week_id = $2
		       ) AS submitted
		FROM teams t
		LEFT JOIN captains c ON c.team_id = t.id
		    AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
		    AND c.active_from <= CURRENT_DATE
		WHERE t.club_id = $1 AND t.active = TRUE
		ORDER BY t.name
	`, clubID, wk.ID)
	if err != nil {
		fmt.Fprint(w, `<div class="alert alert-danger">Error loading teams.</div>`)
		return
	}
	defer rows.Close()

	var teams []teamRow
	for rows.Next() {
		var t teamRow
		if err := rows.Scan(&t.Name, &t.Captain, &t.Submitted); err != nil {
			continue
		}
		teams = append(teams, t)
	}

	if len(teams) == 0 {
		fmt.Fprintf(w, `<div class="alert alert-info">No active teams found for %s.</div>`, escapeHTML(clubName))
		return
	}

	submitted := 0
	for _, t := range teams {
		if t.Submitted {
			submitted++
		}
	}

	fmt.Fprintf(w, `
<div class="card shadow-sm">
  <div class="card-header bg-gmcl text-white d-flex justify-content-between align-items-center">
    <strong>%s</strong>
    <span class="small">Week %d &mdash; %s to %s (%s)</span>
  </div>
  <div class="card-body p-0">
    <div class="px-3 py-2 border-bottom text-muted small">%d of %d teams submitted</div>
    <table class="table table-hover mb-0">
      <thead class="table-light">
        <tr><th>Team</th><th>Captain</th><th>Status</th></tr>
      </thead>
      <tbody>`,
		escapeHTML(clubName),
		wk.Number,
		wk.Start.Format("2 Jan"),
		wk.End.Format("2 Jan 2006"),
		wk.SeasonName,
		submitted, len(teams),
	)

	for _, t := range teams {
		var badge string
		if t.Submitted {
			badge = `<span class="badge bg-success">&#10003; Submitted</span>`
		} else {
			badge = `<span class="badge bg-danger">&#10007; Not submitted</span>`
		}
		captain := t.Captain
		if captain == "" {
			captain = `<span class="text-muted">—</span>`
		} else {
			captain = escapeHTML(captain)
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`,
			escapeHTML(t.Name), captain, badge)
	}

	fmt.Fprint(w, `</tbody></table></div></div>`)
}
