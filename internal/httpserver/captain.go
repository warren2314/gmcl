package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/leagueapi"
	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/timeutil"

	"github.com/google/uuid"
)

// captainSession holds the minimal identity needed for captain actions.
type captainSession struct {
	CaptainID      int32  `json:"cid"`
	SeasonID       int32  `json:"sid"`
	WeekID         int32  `json:"wid"`
	TeamID         int32  `json:"tid"`
	SubmitterName  string `json:"sname"`
	SubmitterEmail string `json:"semail"`
	SubmitterRole  string `json:"srole"`
	Expiry         int64  `json:"exp"`
	Aud            string `json:"aud"`
	JTI            string `json:"jti"`
	IssuedAt       int64  `json:"iat"`
}

const captainSessionCookie = "cap_sess"

func (s *Server) handlePublicEntry() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Ground Feedback")
		writeCaptainNav(w)
		fmt.Fprint(w, `<div class="container" style="max-width:540px">
<div class="card card-gmcl shadow-sm">
  <div class="card-body text-center">
    <img src="/images/logo.webp" alt="GMCL" style="max-width:280px" class="mb-3">
    <h4 class="card-title mb-4">Request match feedback link</h4>
    <form method="POST" action="/magic-link/request" id="entry-form">
      <input type="hidden" name="csrf_token" value="`+csrfToken+`">
      <input type="hidden" name="club_id" id="club_id">
      <input type="hidden" name="team_id" id="team_id">

      <!-- Club typeahead -->
      <div class="mb-3 text-start position-relative">
        <label class="form-label">Club</label>
        <input type="text" class="form-control" id="club_search" placeholder="Start typing your club name..." autocomplete="off" required>
        <div id="club-results" class="list-group position-absolute w-100" style="z-index:1050;max-height:240px;overflow-y:auto;display:none"></div>
      </div>

      <!-- Team select (hidden until club chosen) -->
      <div class="mb-3 text-start" id="team-group" style="display:none">
        <label class="form-label">Team</label>
        <select class="form-select" id="team_select" required disabled>
          <option value="">Select team...</option>
        </select>
      </div>

      <!-- Captain display (hidden until team chosen) -->
      <div class="mb-3 text-start" id="captain-group" style="display:none">
        <label class="form-label">Captain</label>
        <input type="text" class="form-control" id="captain_display" readonly>
      </div>

      <button type="submit" class="btn btn-primary w-100" id="submit-btn" disabled>Send link</button>
    </form>
  </div>
</div>
</div>

<script>
(function() {
  const clubInput  = document.getElementById('club_search');
  const clubIdEl   = document.getElementById('club_id');
  const results    = document.getElementById('club-results');
  const teamGroup  = document.getElementById('team-group');
  const teamSelect = document.getElementById('team_select');
  const teamIdEl   = document.getElementById('team_id');
  const capGroup   = document.getElementById('captain-group');
  const capDisplay = document.getElementById('captain_display');
  const submitBtn  = document.getElementById('submit-btn');

  let debounce = null;

  // Club search
  clubInput.addEventListener('input', function() {
    clearTimeout(debounce);
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
              selectClub(c.id, c.name);
            });
            results.appendChild(a);
          });
          results.style.display = 'block';
        });
    }, 250);
  });

  // Hide results on outside click
  document.addEventListener('click', function(e) {
    if (!results.contains(e.target) && e.target !== clubInput) {
      results.style.display = 'none';
    }
  });

  function selectClub(id, name) {
    clubInput.value = name;
    clubIdEl.value = id;
    results.style.display = 'none';
    // Reset downstream
    teamSelect.innerHTML = '<option value="">Loading...</option>';
    teamSelect.disabled = true;
    teamGroup.style.display = 'block';
    capGroup.style.display = 'none';
    capDisplay.value = '';
    teamIdEl.value = '';
    submitBtn.disabled = true;

    fetch('/api/teams?club_id=' + id)
      .then(function(r) { return r.json(); })
      .then(function(teams) {
        teamSelect.innerHTML = '<option value="">Select team...</option>';
        teams.forEach(function(t) {
          const opt = document.createElement('option');
          opt.value = t.id;
          opt.textContent = t.name;
          teamSelect.appendChild(opt);
        });
        teamSelect.disabled = false;
      });
  }

  // Team change -> load captain
  teamSelect.addEventListener('change', function() {
    const tid = this.value;
    teamIdEl.value = tid;
    if (!tid) {
      capGroup.style.display = 'none';
      submitBtn.disabled = true;
      return;
    }
    capDisplay.value = 'Loading...';
    capGroup.style.display = 'block';
    submitBtn.disabled = true;

    fetch('/api/captain?team_id=' + tid)
      .then(function(r) { return r.json(); })
      .then(function(cap) {
        if (cap.name) {
          capDisplay.value = cap.name;
          submitBtn.disabled = false;
        } else {
          capDisplay.value = 'No captain found for this team';
          submitBtn.disabled = true;
        }
      });
  });

  // Prevent submit if missing data
  document.getElementById('entry-form').addEventListener('submit', function(e) {
    if (!clubIdEl.value || !teamIdEl.value) e.preventDefault();
  });

  // If user edits club text after selection, reset
  clubInput.addEventListener('input', function() {
    if (clubIdEl.value) {
      clubIdEl.value = '';
      teamGroup.style.display = 'none';
      capGroup.style.display = 'none';
      submitBtn.disabled = true;
    }
  });
})();
</script>
`)
		pageFooter(w)
	}
}

func (s *Server) handleMagicLinkRequest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		clubID, _ := strconv.Atoi(r.FormValue("club_id"))
		teamID, _ := strconv.Atoi(r.FormValue("team_id"))

		if clubID <= 0 || teamID <= 0 {
			http.Error(w, "invalid selection", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		// Determine effective week server-side.
		// Priority: active week today -> most recent past week -> nearest upcoming week.
		var weekID int32
		var seasonID int32
		err := s.DB.QueryRow(ctx, `
			WITH active AS (
				SELECT w.id, w.season_id, 1 AS p, w.start_date
				FROM weeks w
				WHERE CURRENT_DATE BETWEEN w.start_date AND w.end_date
				ORDER BY w.start_date
				LIMIT 1
			),
			past AS (
				SELECT w.id, w.season_id, 2 AS p, w.start_date
				FROM weeks w
				WHERE w.end_date < CURRENT_DATE
				ORDER BY w.end_date DESC
				LIMIT 1
			),
			upcoming AS (
				SELECT w.id, w.season_id, 3 AS p, w.start_date
				FROM weeks w
				WHERE w.start_date > CURRENT_DATE
				ORDER BY w.start_date ASC
				LIMIT 1
			)
			SELECT id, season_id
			FROM (
				SELECT * FROM active
				UNION ALL
				SELECT * FROM past
				UNION ALL
				SELECT * FROM upcoming
			) choices
			ORDER BY p, start_date
			LIMIT 1
		`).Scan(&weekID, &seasonID)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "No match week is configured yet. Please contact an administrator.")
			return
		}

		// Look up captain for team (simplified: latest active).
		var captainID int32
		var captainEmail string
		err = s.DB.QueryRow(ctx, `
			SELECT c.id, w.season_id, c.email
			FROM captains c
			JOIN teams t ON c.team_id = t.id
			JOIN weeks w ON w.id = $1
			WHERE c.team_id = $2
			ORDER BY c.active_from DESC
			LIMIT 1
		`, weekID, teamID).Scan(&captainID, &seasonID, &captainEmail)
		if err != nil {
			// Avoid enumeration: always say success.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "If a captain record exists, a link has been emailed.")
			return
		}

		// Per-captain per-week throttling: 3/hour and 10/week.
		var sendCountHour int
		err = s.DB.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM magic_link_send_log
			WHERE captain_id = $1 AND season_id = $2 AND week_id = $3
			  AND created_at > now() - interval '1 hour'
		`, captainID, seasonID, weekID).Scan(&sendCountHour)
		if err != nil {
			http.Error(w, "could not process request", http.StatusInternalServerError)
			return
		}
		if sendCountHour >= 3 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "If a captain record exists, a link has been emailed.")
			return
		}
		var sendCountWeek int
		err = s.DB.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM magic_link_send_log
			WHERE captain_id = $1 AND season_id = $2 AND week_id = $3
			  AND created_at > now() - interval '7 days'
		`, captainID, seasonID, weekID).Scan(&sendCountWeek)
		if err != nil {
			http.Error(w, "could not process request", http.StatusInternalServerError)
			return
		}
		if sendCountWeek >= 10 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "If a captain record exists, a link has been emailed.")
			return
		}

		// Expire at next Wednesday 23:59:59 Europe/London, with hard cap 7 days and floor 10 min.
		now := time.Now()
		loc := s.LondonLoc
		if loc == nil {
			loc = time.UTC
		}
		expiresAt := timeutil.NextWednesdayExpiry(now, loc)
		ttl := time.Until(expiresAt)
		const maxTTL = 7 * 24 * time.Hour
		const minTTL = 10 * time.Minute
		if ttl > maxTTL {
			ttl = maxTTL
			expiresAt = now.Add(maxTTL)
		}
		if ttl < minTTL {
			ttl = minTTL
			expiresAt = now.Add(minTTL)
		}

		ip := r.RemoteAddr
		ua := r.UserAgent()
		token, err := auth.GenerateAndStoreMagicTokenWithRevocation(ctx, s.DB, captainID, seasonID, weekID, expiresAt, ip, ua)
		if err != nil {
			http.Error(w, "could not process request", http.StatusInternalServerError)
			return
		}

		// record send event (after token creation)
		_, _ = s.DB.Exec(ctx, `
			INSERT INTO magic_link_send_log (captain_id, season_id, week_id)
			VALUES ($1, $2, $3)
		`, captainID, seasonID, weekID)

		link := fmt.Sprintf("%s/magic-link/confirm?token=%s", publicBaseURL(r), token)
		if os.Getenv("APP_ENV") == "dev" {
			fmt.Printf("Magic link for captain %d (%s): %s\n", captainID, captainEmail, link)
		}
		mailer := email.NewFromEnv()
		body := "Open this secure link to complete the captain report:\n\n" +
			link + "\n\n" +
			"This link expires automatically."
		if err := mailer.Send(captainEmail, "Captain report access", body); err != nil {
			log.Printf("[captain magic link] captain_id=%d email=%s error=%v", captainID, captainEmail, err)
			http.Error(w, "could not send email", http.StatusInternalServerError)
			return
		}

		// audit log (without target email)
		s.audit(ctx, r, "system", nil, "magic_link_requested", "team", func() *int64 {
			v := int64(teamID)
			return &v
		}(), map[string]any{
			"captain_id": captainID,
			"season_id":  seasonID,
			"week_id":    weekID,
		})

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "If a captain record exists, a link has been emailed.")
	}
}

func (s *Server) handleMagicLinkConfirm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Render an intermediate page that posts the token in the body to avoid
			// keeping it in referrers or logs beyond the initial click.
			token := r.URL.Query().Get("token")
			if token == "" {
				http.Error(w, "missing token", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			pageHead(w, "Confirm Link")
			writeCaptainNav(w)
			fmt.Fprintf(w, `<div class="container" style="max-width:540px">
<div class="card card-gmcl shadow-sm">
  <div class="card-body text-center">
    <img src="/images/logo.webp" alt="GMCL" style="max-width:220px" class="mb-3">
    <h4 class="card-title">Open feedback form</h4>
    <p class="text-muted">Click continue to open your feedback form. If you did not request this email, you can safely ignore it.</p>
    <form method="POST" action="/magic-link/confirm">
      <input type="hidden" name="token" value="%s">
      <button type="submit" class="btn btn-primary w-100">Continue</button>
    </form>
  </div>
</div>
</div>
`, token)
			pageFooter(w)
			return
		case http.MethodPost:
			// proceed below
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		token := r.FormValue("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		mt, err := auth.ConsumeMagicToken(ctx, s.DB, token)
		if err != nil {
			http.Error(w, "link invalid or expired", http.StatusBadRequest)
			return
		}

		// Determine team from captain.
		var teamID int32
		err = s.DB.QueryRow(ctx, `SELECT team_id FROM captains WHERE id = $1`, mt.CaptainID).Scan(&teamID)
		if err != nil {
			http.Error(w, "could not load captain", http.StatusInternalServerError)
			return
		}
		now := time.Now().Unix()
		sess := captainSession{
			CaptainID:      mt.CaptainID,
			SeasonID:       mt.SeasonID,
			WeekID:         mt.WeekID,
			TeamID:         teamID,
			SubmitterName:  "",
			SubmitterEmail: "",
			SubmitterRole:  "captain",
			Expiry:         now + int64((2 * time.Hour).Seconds()),
			Aud:            "cap",
			JTI:            uuid.NewString(),
			IssuedAt:       now,
		}
		if strings.TrimSpace(mt.DelegateEmail) != "" {
			sess.SubmitterRole = "delegate"
			sess.SubmitterEmail = strings.TrimSpace(mt.DelegateEmail)
			sess.SubmitterName = strings.TrimSpace(mt.DelegateName)
		}

		if err := setCaptainSessionCookie(w, &sess); err != nil {
			http.Error(w, "could not set session", http.StatusInternalServerError)
			return
		}

		// audit log redemption
		s.audit(ctx, r, "system", nil, "magic_link_redeemed", "captain", func() *int64 {
			v := int64(mt.CaptainID)
			return &v
		}(), map[string]any{
			"season_id":      mt.SeasonID,
			"week_id":        mt.WeekID,
			"submitter_role": sess.SubmitterRole,
		})

		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/captain/form", http.StatusSeeOther)
	}
}

func (s *Server) handleCaptainForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := getCaptainSessionFromRequest(r)
		if err != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var clubName, teamName, captainName, captainEmail string
		err = s.DB.QueryRow(ctx, `
			SELECT cl.name, t.name, c.full_name, c.email
			FROM captains c
			JOIN teams t ON c.team_id = t.id
			JOIN clubs cl ON t.club_id = cl.id
			WHERE c.id = $1
		`, sess.CaptainID).Scan(&clubName, &teamName, &captainName, &captainEmail)
		if err != nil {
			http.Error(w, "could not load captain", http.StatusInternalServerError)
			return
		}
		submitterName := captainName
		submitterEmail := captainEmail
		if sess.SubmitterRole == "delegate" {
			if strings.TrimSpace(sess.SubmitterName) != "" {
				submitterName = strings.TrimSpace(sess.SubmitterName)
			}
			if strings.TrimSpace(sess.SubmitterEmail) != "" {
				submitterEmail = strings.TrimSpace(sess.SubmitterEmail)
			}
		}

		var draftJSON []byte
		_ = s.DB.QueryRow(ctx, `
			SELECT draft_data
			FROM drafts
			WHERE season_id = $1 AND week_id = $2 AND team_id = $3
		`, sess.SeasonID, sess.WeekID, sess.TeamID).Scan(&draftJSON)

		draft := make(map[string]any)
		if len(draftJSON) > 0 {
			_ = json.Unmarshal(draftJSON, &draft)
		}

		matchDateStr := formVal(draft, "match_date")
		if matchDateStr == "" {
			matchDateStr = time.Now().Format("2006-01-02")
		}
		if md, err := time.Parse("2006-01-02", matchDateStr); err == nil {
			u1, u2, ok := leagueapi.LookupUmpirePrefill(ctx, s.DB, sess.TeamID, md)
			if ok {
				filled := false
				if formVal(draft, "umpire1_name") == "" && u1 != "" {
					draft["umpire1_name"] = u1
					filled = true
				}
				if formVal(draft, "umpire2_name") == "" && u2 != "" {
					draft["umpire2_name"] = u2
					filled = true
				}
				if filled {
					draft["prefill_source"] = "league_api"
				}
			}
		}

		csrfToken := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrfToken = c.Value
		}

		today := time.Now().Format("2006-01-02")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.renderGMCLForm(w, csrfToken, clubName, teamName, captainName, captainEmail, submitterName, submitterEmail, sess.SubmitterRole, today, draft)
	}
}

func (s *Server) handleCaptainDelegateInvite() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := getCaptainSessionFromRequest(r)
		if err != nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		if sess.SubmitterRole != "captain" {
			http.Error(w, "only the main captain can invite stand-ins", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		delegateName := strings.TrimSpace(r.FormValue("delegate_name"))
		delegateEmail := strings.ToLower(strings.TrimSpace(r.FormValue("delegate_email")))
		if delegateEmail == "" || !strings.Contains(delegateEmail, "@") {
			http.Error(w, "valid delegate email is required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
		defer cancel()

		_, _ = s.DB.Exec(ctx, `
			INSERT INTO captain_delegations (season_id, week_id, team_id, captain_id, delegate_name, delegate_email)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)
		`, sess.SeasonID, sess.WeekID, sess.TeamID, sess.CaptainID, delegateName, delegateEmail)

		now := time.Now()
		loc := s.LondonLoc
		if loc == nil {
			loc = time.UTC
		}
		expiresAt := timeutil.NextWednesdayExpiry(now, loc)
		token, err := auth.GenerateAndStoreMagicTokenWithDelegate(
			ctx, s.DB, sess.CaptainID, sess.SeasonID, sess.WeekID, expiresAt, r.RemoteAddr, r.UserAgent(), delegateName, delegateEmail,
		)
		if err != nil {
			http.Error(w, "could not create invite", http.StatusInternalServerError)
			return
		}

		mailer := email.NewFromEnv()
		link := fmt.Sprintf("%s/magic-link/confirm?token=%s", publicBaseURL(r), token)
		body := "You have been invited as a stand-in captain for this match week.\n\n" +
			"Open this secure link to complete the captain report:\n" + link + "\n\n" +
			"This link expires automatically."
		if err := mailer.Send(delegateEmail, "Stand-in captain access", body); err != nil {
			http.Error(w, "could not send invite", http.StatusInternalServerError)
			return
		}

		_, _ = s.DB.Exec(ctx, `
			INSERT INTO magic_link_send_log (captain_id, season_id, week_id)
			VALUES ($1, $2, $3)
		`, sess.CaptainID, sess.SeasonID, sess.WeekID)

		s.audit(ctx, r, "system", nil, "delegate_invited", "captain", func() *int64 {
			v := int64(sess.CaptainID)
			return &v
		}(), map[string]any{
			"season_id":      sess.SeasonID,
			"week_id":        sess.WeekID,
			"team_id":        sess.TeamID,
			"delegate_email": delegateEmail,
		})

		http.Redirect(w, r, "/captain/form", http.StatusSeeOther)
	}
}

func (s *Server) handleCaptainAutosave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := getCaptainSessionFromRequest(r)
		if err != nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid data", http.StatusBadRequest)
			return
		}

		draft := buildGMCLDraftFromRequest(r)
		payload, err := json.Marshal(draft)
		if err != nil {
			http.Error(w, "invalid data", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		_, err = s.DB.Exec(ctx, `
			INSERT INTO drafts (season_id, week_id, team_id, captain_id, draft_data, last_autosaved_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (season_id, week_id, team_id)
			DO UPDATE SET draft_data = EXCLUDED.draft_data,
			             captain_id = EXCLUDED.captain_id,
			             last_autosaved_at = now()
		`, sess.SeasonID, sess.WeekID, sess.TeamID, sess.CaptainID, payload)
		if err != nil {
			http.Error(w, "could not save draft", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "Saved at "+time.Now().Format("15:04:05"))

		s.audit(ctx, r, "system", nil, "draft_autosaved", "draft", nil, map[string]any{
			"season_id": sess.SeasonID,
			"week_id":   sess.WeekID,
			"team_id":   sess.TeamID,
		})
	}
}

// buildGMCLDraftFromRequest extracts all GMCL form fields from r.PostForm into a map for draft_data.
func buildGMCLDraftFromRequest(r *http.Request) map[string]any {
	draft := make(map[string]any)
	for k, v := range r.PostForm {
		if len(v) > 0 {
			draft[k] = v[0]
		}
	}
	// Normalise numeric fields from string to int for dropdowns
	for _, key := range []string{"unevenness_of_bounce", "seam_movement", "carry_bounce", "turn"} {
		if v := r.FormValue(key); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				draft[key] = i
			}
		}
	}
	return draft
}

func (s *Server) handleCaptainSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := getCaptainSessionFromRequest(r)
		if err != nil {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid data", http.StatusBadRequest)
			return
		}

		// Required GMCL fields
		matchDateStr := r.FormValue("match_date")
		umpire1 := strings.TrimSpace(r.FormValue("umpire1_name"))
		umpire2 := strings.TrimSpace(r.FormValue("umpire2_name"))
		yourTeam := r.FormValue("your_team")
		if matchDateStr == "" || umpire1 == "" || umpire2 == "" || yourTeam == "" {
			http.Error(w, "missing required fields (date, umpire names, your team)", http.StatusBadRequest)
			return
		}

		matchDate, err := time.Parse("2006-01-02", matchDateStr)
		if err != nil {
			http.Error(w, "invalid date format", http.StatusBadRequest)
			return
		}

		// Pitch criteria 1–6 (1=best, 6=unfit) -> map to 1–5 for legacy columns: rating = max(1, min(5, 7 - score))
		score := func(name string) int {
			i, _ := strconv.Atoi(r.FormValue(name))
			if i < 1 {
				i = 1
			}
			if i > 6 {
				i = 6
			}
			return i
		}
		unevenness := score("unevenness_of_bounce")
		seam := score("seam_movement")
		carry := score("carry_bounce")
		turn := score("turn")
		pitchRating := 7 - unevenness
		if pitchRating < 1 {
			pitchRating = 1
		}
		if pitchRating > 5 {
			pitchRating = 5
		}
		outfieldRating := 7 - seam
		if outfieldRating < 1 {
			outfieldRating = 1
		}
		if outfieldRating > 5 {
			outfieldRating = 5
		}
		facilitiesRating := (7 - carry + 7 - turn) / 2
		if facilitiesRating < 1 {
			facilitiesRating = 1
		}
		if facilitiesRating > 5 {
			facilitiesRating = 5
		}

		comments := strings.TrimSpace(r.FormValue("umpire_comments"))
		if d := strings.TrimSpace(r.FormValue("umpire_comments_detail")); d != "" {
			comments = comments + "\n\n" + d
		}

		formData := buildGMCLDraftFromRequest(r)
		formDataJSON, _ := json.Marshal(formData)
		umpire1Type := strings.ToLower(strings.TrimSpace(r.FormValue("umpire1_type")))
		umpire2Type := strings.ToLower(strings.TrimSpace(r.FormValue("umpire2_type")))
		if umpire1Type != "panel" && umpire1Type != "club" {
			http.Error(w, "umpire 1 type must be panel or club", http.StatusBadRequest)
			return
		}
		if umpire2Type != "panel" && umpire2Type != "club" {
			http.Error(w, "umpire 2 type must be panel or club", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var submissionID int64
		submittedByRole := "captain"
		submittedByName := ""
		submittedByEmail := ""
		if sess.SubmitterRole == "delegate" {
			submittedByRole = "delegate"
			submittedByName = strings.TrimSpace(sess.SubmitterName)
			submittedByEmail = strings.TrimSpace(sess.SubmitterEmail)
		}
		err = s.DB.QueryRow(ctx, `
			INSERT INTO submissions (season_id, week_id, team_id, captain_id, match_date,
			                         pitch_rating, outfield_rating, facilities_rating, comments, form_data,
			                         submitted_by_name, submitted_by_email, submitted_by_role, umpire1_type, umpire2_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), NULLIF($12, ''), $13, $14, $15)
			RETURNING id
		`, sess.SeasonID, sess.WeekID, sess.TeamID, sess.CaptainID, matchDate.Format("2006-01-02"),
			pitchRating, outfieldRating, facilitiesRating, comments, formDataJSON,
			submittedByName, submittedByEmail, submittedByRole, umpire1Type, umpire2Type).Scan(&submissionID)
		if err != nil {
			http.Error(w, "could not save submission", http.StatusInternalServerError)
			return
		}

		_, _ = s.DB.Exec(ctx, `
			DELETE FROM drafts
			WHERE season_id = $1 AND week_id = $2 AND team_id = $3
		`, sess.SeasonID, sess.WeekID, sess.TeamID)

		http.SetCookie(w, &http.Cookie{
			Name:     captainSessionCookie,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Submitted")
		writeCaptainNav(w)
		fmt.Fprint(w, `<div class="container" style="max-width:540px">
<div class="alert alert-success shadow-sm text-center" role="alert">
  <h4 class="alert-heading">Thank you!</h4>
  <p>Your submission has been recorded. A copy will be sent to your email.</p>
  <a href="/" class="btn btn-outline-primary">Back to home</a>
</div>
</div>
`)
		pageFooter(w)

		s.audit(ctx, r, "system", nil, "submission_created", "submission", &submissionID, map[string]any{
			"season_id":         sess.SeasonID,
			"week_id":           sess.WeekID,
			"team_id":           sess.TeamID,
			"captain_id":        sess.CaptainID,
			"submitted_by_role": submittedByRole,
			"umpire1_type":      umpire1Type,
			"umpire2_type":      umpire2Type,
		})
	}
}

func getCaptainSessionFromRequest(r *http.Request) (*captainSession, error) {
	c, err := r.Cookie(captainSessionCookie)
	if err != nil {
		return nil, err
	}
	secret := []byte(os.Getenv("SESSION_SECRET"))
	if len(secret) == 0 {
		return nil, fmt.Errorf("session secret not configured")
	}

	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, err
	}

	if len(raw) < sha256.Size {
		return nil, fmt.Errorf("token too short")
	}
	sig := raw[:sha256.Size]
	payload := raw[sha256.Size:]

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, fmt.Errorf("invalid signature")
	}

	var sess captainSession
	if err := json.Unmarshal(payload, &sess); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if now > sess.Expiry {
		return nil, fmt.Errorf("session expired")
	}
	// reject sessions from the future with a small skew allowance
	if sess.IssuedAt > now+60 {
		return nil, fmt.Errorf("session issued in the future")
	}
	if sess.Aud != "cap" {
		return nil, fmt.Errorf("invalid session audience")
	}
	if sess.SubmitterRole == "" {
		sess.SubmitterRole = "captain"
	}
	return &sess, nil
}

func setCaptainSessionCookie(w http.ResponseWriter, sess *captainSession) error {
	secret := []byte(os.Getenv("SESSION_SECRET"))
	if len(secret) == 0 {
		return fmt.Errorf("SESSION_SECRET not configured")
	}

	payload, err := json.Marshal(sess)
	if err != nil {
		return err
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)

	token := append(sig, payload...)
	val := base64.RawURLEncoding.EncodeToString(token)

	cookie := &http.Cookie{
		Name:     captainSessionCookie,
		Value:    val,
		Path:     "/captain",
		Expires:  time.Unix(sess.Expiry, 0),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, cookie)
	return nil
}

func publicBaseURL(r *http.Request) string {
	if base := os.Getenv("PUBLIC_BASE_URL"); base != "" {
		return base
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	return scheme + "://" + host
}
