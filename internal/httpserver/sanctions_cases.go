package httpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"
	sanctiondomain "cricket-ground-feedback/internal/sanctions"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const evidenceRedactionAttestationCode = "reporter_and_reporting_club_identity_removed_v1"

const portalSharedEvidenceListQuery = `SELECT evidence.id,evidence.media_type,evidence.byte_size
	FROM sanction_offending_club_evidence_derivatives allowed
	JOIN sanction_case_evidence evidence
	  ON evidence.id=allowed.evidence_id AND evidence.case_id=allowed.case_id
	JOIN LATERAL (SELECT sharing.action FROM sanction_evidence_sharing_events sharing
		WHERE sharing.case_id=evidence.case_id AND sharing.evidence_id=evidence.id
		  AND sharing.audience='offending_club' ORDER BY sharing.id DESC LIMIT 1) current_share
	  ON current_share.action='shared'
	WHERE allowed.case_id=$1
	ORDER BY evidence.id`

const portalSharedEvidenceDownloadQuery = `SELECT evidence.media_type,evidence.storage_key,lower(evidence.sha256)
	FROM sanction_offending_club_evidence_derivatives allowed
	JOIN sanction_case_evidence evidence
	  ON evidence.id=allowed.evidence_id AND evidence.case_id=allowed.case_id
	JOIN sanction_case_access_tokens token ON token.case_id=evidence.case_id
	JOIN sanction_response_requests request
	  ON request.access_token_id=token.id AND request.case_id=token.case_id AND request.status='pending'
	JOIN LATERAL (SELECT sharing.action FROM sanction_evidence_sharing_events sharing
		WHERE sharing.case_id=evidence.case_id AND sharing.evidence_id=evidence.id
		  AND sharing.audience='offending_club' ORDER BY sharing.id DESC LIMIT 1) current_share
	  ON current_share.action='shared'
	WHERE allowed.evidence_id=$1 AND token.token_hash=$2 AND token.purpose='respond'
	  AND token.revoked_at IS NULL AND token.expires_at>now()`

func requestID(r *http.Request) string {
	if id := strings.TrimSpace(chimiddleware.GetReqID(r.Context())); id != "" {
		return id
	}
	return strings.TrimSpace(r.Header.Get("X-Request-ID"))
}

func sanctionsBaseURL() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("SANCTIONS_BASE_URL")), "/"); v != "" {
		return v
	}
	return "https://sanctions.gmcl.co.uk"
}

func adminActor(r *http.Request) sanctiondomain.Actor {
	sess, _ := getAdminSessionFromRequest(r)
	a := sanctiondomain.Actor{Type: "admin", RequestID: requestID(r)}
	if sess != nil {
		a.ID = &sess.AdminID
		a.Label = sess.Name
	}
	return a
}

func newPublicToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

func tokenHash(raw string) []byte { h := sha256.Sum256([]byte(strings.TrimSpace(raw))); return h[:] }

func sanctionsSearchPatterns(value string) []string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return nil
	}
	seen := map[string]bool{}
	patterns := []string{}
	add := func(term string) {
		term = strings.Join(strings.Fields(strings.TrimSpace(term)), " ")
		if term != "" && !seen[term] {
			seen[term] = true
			patterns = append(patterns, "%"+term+"%")
		}
	}
	add(value)
	lower := strings.ToLower(value)
	if strings.Contains(lower, " and ") {
		add(strings.ReplaceAll(lower, " and ", " & "))
	}
	if strings.Contains(lower, "&") {
		add(strings.ReplaceAll(lower, "&", " and "))
	}
	return patterns
}

func sanctionsCategoryURL(current url.Values, category string) string {
	values := url.Values{}
	for key, items := range current {
		for _, item := range items {
			values.Add(key, item)
		}
	}
	values.Del("type")
	if category == "" {
		values.Del("view")
	} else {
		values.Set("view", category)
	}
	if encoded := values.Encode(); encoded != "" {
		return "/sanctions?" + encoded
	}
	return "/sanctions"
}

func (s *Server) handlePublicSanctionsRegister() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		season := strings.TrimSpace(r.URL.Query().Get("season"))
		search := strings.TrimSpace(r.URL.Query().Get("q"))
		effectFilter := strings.TrimSpace(r.URL.Query().Get("type"))
		category := strings.TrimSpace(r.URL.Query().Get("view"))
		allowedEffects := map[string]bool{"yellow_card": true, "red_card": true, "suspended_red": true, "player_ban": true, "team_ban": true, "fine": true, "card_points": true, "points_adjustment": true, "warning": true}
		if !map[string]bool{"players": true, "clubs": true, "yellow": true, "red": true}[category] {
			category = ""
		}
		if allowedEffects[effectFilter] && (category == "yellow" || category == "red") {
			category = ""
		}
		archive := r.URL.Query().Get("archive") == "1"
		args := []any{}
		where := `c.status='published' AND c.public_status IN ('active','suspended','served','expired') AND e.status IN ('active','suspended','served','expired')`
		if !archive {
			where += ` AND c.public_status IN ('active','suspended') AND e.status IN ('active','suspended') AND (e.ends_at IS NULL OR e.ends_at>=now())`
		}
		if season != "" {
			if y, err := strconv.Atoi(season); err == nil {
				args = append(args, y)
				where += fmt.Sprintf(` AND COALESCE(EXTRACT(YEAR FROM s.start_date)::int,EXTRACT(YEAR FROM e.starts_at)::int)=$%d`, len(args))
			}
		}
		if search != "" {
			clauses := []string{}
			for _, pattern := range sanctionsSearchPatterns(search) {
				args = append(args, pattern)
				position := len(args)
				clauses = append(clauses, fmt.Sprintf(`(COALESCE(effect_club.name,cl.name,'') ILIKE $%d OR COALESCE(effect_team.name,t.name,'') ILIKE $%d OR COALESCE(e.player_name,effect_subject.player_name,c.player_name,'') ILIKE $%d OR c.public_summary ILIKE $%d)`, position, position, position, position))
			}
			where += ` AND (` + strings.Join(clauses, ` OR `) + `)`
		}
		switch category {
		case "players":
			where += ` AND (NULLIF(BTRIM(COALESCE(e.player_name,effect_subject.player_name,'')),'') IS NOT NULL OR e.effect_type='player_ban')`
		case "clubs":
			where += ` AND COALESCE(effect_club.id,c.club_id) IS NOT NULL AND (e.effect_type IN ('team_ban','points_adjustment') OR NULLIF(BTRIM(COALESCE(e.player_name,effect_subject.player_name,'')),'') IS NULL)`
		case "yellow":
			where += ` AND e.effect_type='yellow_card'`
		case "red":
			where += ` AND e.effect_type IN ('red_card','suspended_red')`
		}
		if allowedEffects[effectFilter] {
			args = append(args, effectFilter)
			where += fmt.Sprintf(` AND e.effect_type=$%d`, len(args))
		}
		rows, err := s.DB.Query(ctx, fmt.Sprintf(`
			SELECT c.reference,COALESCE(EXTRACT(YEAR FROM s.start_date)::int,EXTRACT(YEAR FROM e.starts_at)::int,0),COALESCE(effect_club.name,cl.name,''),COALESCE(effect_team.name,t.name,''),
			       CASE WHEN e.subject_type='team' AND e.effect_type NOT IN ('yellow_card','red_card','suspended_red') THEN '' ELSE COALESCE(NULLIF(e.player_name,''),NULLIF(effect_subject.player_name,''),NULLIF(c.player_name,''),'') END,
			       c.public_summary,CASE WHEN e.ends_at<now() THEN 'expired' ELSE c.public_status END,e.effect_type,e.status,COALESCE(e.points,0),COALESCE(e.amount_pence,0),e.starts_at,e.ends_at,c.published_at,
			       COALESCE(balance.yellow_balance,0),COALESCE(balance.red_count,0)
			FROM sanction_cases c
			LEFT JOIN seasons s ON s.id=c.season_id LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id
			JOIN sanction_decision_revisions d ON d.id=(SELECT latest.id FROM sanction_decision_revisions latest WHERE latest.case_id=c.id AND latest.status='approved' ORDER BY latest.revision DESC LIMIT 1)
			JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id)
			LEFT JOIN sanction_case_subjects effect_subject ON effect_subject.id=e.case_subject_id
			LEFT JOIN teams effect_team ON effect_team.id=COALESCE(effect_subject.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END,c.team_id)
			LEFT JOIN clubs effect_club ON effect_club.id=COALESCE(effect_team.club_id,c.club_id)
			LEFT JOIN LATERAL (SELECT
			  (SELECT SUM(yellow_delta) FROM sanction_card_ledger_entries yl WHERE yl.team_id=effect_team.id) yellow_balance,
			  (SELECT SUM(red_delta) FROM sanction_card_ledger_entries rl WHERE rl.team_id=effect_team.id AND (c.season_id IS NULL OR rl.season_id=c.season_id)) red_count
			) balance ON true
			WHERE %s ORDER BY COALESCE(e.starts_at,c.published_at) DESC,c.reference DESC`, where), args...)
		if err != nil {
			slog.Error("load public sanctions register", "error", err)
			http.Error(w, "could not load sanctions", 500)
			return
		}
		defer rows.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "GMCL Sanctions Register")
		writeCaptainNav(w)
		fmt.Fprint(w, `<main class="container py-4" style="max-width:1100px"><div class="d-flex flex-column flex-sm-row justify-content-between align-items-sm-start gap-3 mb-4"><div><h1 class="h2">GMCL sanctions register</h1><p class="text-muted mb-0">Approved and published team cards, bans, fines and points decisions. Private evidence and correspondence are never shown here.</p></div><a class="btn btn-outline-danger" href="/sanctions/report">Report an issue</a></div>`)
		fmt.Fprint(w, `<nav class="row row-cols-2 row-cols-md-5 g-2 mb-3" aria-label="Sanctions register views">`)
		for _, item := range []struct{ value, label, description string }{
			{"", "All", "Every sanction"},
			{"players", "Players", "Player sanctions"},
			{"clubs", "Clubs / teams", "Team decisions"},
			{"yellow", "Yellow cards", "Yellow card records"},
			{"red", "Red cards", "Direct and suspended reds"},
		} {
			active := item.value == category
			classes := "card h-100 text-decoration-none text-body shadow-sm"
			current := ""
			if active {
				classes += " border-danger border-2 bg-light"
				current = ` aria-current="page"`
			}
			fmt.Fprintf(w, `<div class="col"><a class="%s" href="%s"%s><span class="card-body p-3"><strong class="d-block">%s</strong><small class="text-muted">%s</small></span></a></div>`, classes, escapeHTML(sanctionsCategoryURL(r.URL.Query(), item.value)), current, item.label, item.description)
		}
		fmt.Fprint(w, `</nav><form method="GET" class="card card-body mb-3"><input type="hidden" name="view" value="`+escapeHTML(category)+`"><div class="row g-2"><div class="col-12 col-md-4"><label class="form-label" for="sanction-search">Club, team, player or reason</label><input id="sanction-search" class="form-control" type="search" name="q" placeholder="Search register" value="`+escapeHTML(search)+`"></div><div class="col-6 col-md-2"><label class="form-label" for="sanction-season">Season</label><input id="sanction-season" class="form-control" type="number" name="season" min="2016" placeholder="All" value="`+escapeHTML(season)+`"></div><div class="col-6 col-md-3"><label class="form-label" for="sanction-type">Sanction</label><select id="sanction-type" class="form-select" name="type"><option value="">All types</option>`)
		for _, option := range []struct{ value, label string }{{"yellow_card", "Yellow card"}, {"red_card", "Red card"}, {"suspended_red", "Suspended red"}, {"player_ban", "Player ban"}, {"team_ban", "Team ban"}, {"fine", "Fine"}, {"points_adjustment", "Points adjustment"}, {"warning", "Warning"}} {
			selected := ""
			if effectFilter == option.value {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, option.value, selected, option.label)
		}
		fmt.Fprint(w, `</select></div><div class="col-12 col-md-3 d-flex flex-column justify-content-end"><label class="form-check mb-2"><input class="form-check-input" type="checkbox" name="archive" value="1"`)
		if archive {
			fmt.Fprint(w, " checked")
		}
		fmt.Fprint(w, `> <span class="form-check-label">Include served and expired</span></label><button class="btn btn-primary">Apply filters</button></div></div></form><div class="table-responsive"><table class="table table-striped responsive-cards align-middle"><thead><tr><th>Reference</th><th>Season</th><th>Club / team / player</th><th>Sanction</th><th>Status</th><th>Effective</th></tr></thead><tbody>`)
		count := 0
		for rows.Next() {
			var ref, club, team, player, reason, pubStatus, effect, effectStatus string
			var year, points, yellowBalance, redCount int
			var amountPence int64
			var starts, ends, published *time.Time
			if rows.Scan(&ref, &year, &club, &team, &player, &reason, &pubStatus, &effect, &effectStatus, &points, &amountPence, &starts, &ends, &published, &yellowBalance, &redCount) != nil {
				continue
			}
			count++
			subject := strings.TrimSpace(strings.Join(nonEmpty(club, team, player), " — "))
			sanction := effectLabel(effect)
			if points != 0 {
				sanction += fmt.Sprintf(" · %d point deduction", points)
			}
			if amountPence != 0 {
				sanction += fmt.Sprintf(" · £%.2f", float64(amountPence)/100)
			}
			balance := ""
			if effect == "yellow_card" || effect == "red_card" || effect == "suspended_red" {
				toThreshold := 3 - yellowBalance
				if toThreshold < 0 {
					toThreshold = 0
				}
				balance = fmt.Sprintf(`<div class="small text-muted">Current balance: %d yellow, %d red; %d yellow to next threshold</div>`, yellowBalance, redCount, toThreshold)
			}
			dates := "—"
			if starts != nil {
				dates = starts.In(s.LondonLoc).Format("02 Jan 2006")
			}
			if ends != nil {
				dates += " to " + ends.In(s.LondonLoc).Format("02 Jan 2006")
			}
			fmt.Fprintf(w, `<tr><td data-label="Reference"><a href="/sanctions/%s"><strong>%s</strong></a></td><td data-label="Season">%d</td><td data-label="Club / team / player">%s</td><td data-label="Sanction"><strong>%s</strong>%s<div class="small text-muted">%s</div></td><td data-label="Status">%s</td><td data-label="Effective">%s</td></tr>`, escapeHTML(ref), escapeHTML(ref), year, escapeHTML(subject), escapeHTML(sanction), balance, escapeHTML(reason), escapeHTML(pubStatus), escapeHTML(dates))
		}
		if count == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-4">No published sanctions match this view.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></main>`)
		pageFooter(w)
	}
}

func nonEmpty(v ...string) []string {
	out := []string{}
	for _, x := range v {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}
func effectLabel(v string) string {
	return map[string]string{"yellow_card": "Yellow card", "red_card": "Red card", "suspended_red": "Suspended red card", "player_ban": "Player ban", "team_ban": "Team ban", "fine": "Fine", "card_points": "Card-system points", "points_adjustment": "Points adjustment", "warning": "Warning", "no_action": "No action"}[v]
}

func publicEffectSubject(effectType, team, player string) string {
	if effectType == "team_ban" || effectType == "points_adjustment" {
		return strings.TrimSpace(team)
	}
	if player = strings.TrimSpace(player); player != "" {
		return player
	}
	return strings.TrimSpace(team)
}

func textContainsPrivateIdentity(body string, values ...string) bool {
	return sanctiondomain.ContainsPrivateIdentity(body, values...)
}

func (s *Server) handlePublicSanctionDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ref := chi.URLParam(r, "reference")
		var caseID int64
		var club, team, player, summary, status, ruleRef string
		err := s.DB.QueryRow(r.Context(), `SELECT c.id,COALESCE(cl.name,''),COALESCE(t.name,''),COALESCE(c.player_name,''),c.public_summary,c.public_status,COALESCE(d.rule_reference,'') FROM sanction_cases c LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id JOIN sanction_decision_revisions d ON d.id=(SELECT latest.id FROM sanction_decision_revisions latest WHERE latest.case_id=c.id AND latest.status='approved' ORDER BY latest.revision DESC LIMIT 1) WHERE c.reference=$1 AND c.status='published'`, ref).Scan(&caseID, &club, &team, &player, &summary, &status, &ruleRef)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		effects, err := s.DB.Query(r.Context(), `SELECT e.effect_type,e.status,e.starts_at,e.ends_at,e.points,e.amount_pence,
			COALESCE(effect_team.name,''),COALESCE(NULLIF(e.player_name,''),NULLIF(effect_subject.player_name,''),'')
			FROM sanction_effect_revisions e
			JOIN sanction_decision_revisions d ON d.id=e.decision_revision_id
			LEFT JOIN sanction_case_subjects effect_subject ON effect_subject.id=e.case_subject_id
			LEFT JOIN teams effect_team ON effect_team.id=COALESCE(effect_subject.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END)
			WHERE d.case_id=$1 AND d.status='approved' AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id) ORDER BY e.id`, caseID)
		if err != nil {
			http.Error(w, "could not load sanction effects", 500)
			return
		}
		defer effects.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanction "+ref)
		writeCaptainNav(w)
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:800px"><a href="/sanctions" class="btn btn-sm btn-outline-secondary mb-3">Back to register</a><article class="card mb-3"><div class="card-header d-flex justify-content-between gap-2"><strong>%s</strong><span class="badge text-bg-danger">%s</span></div><div class="card-body"><h1 class="h3">%s</h1><p>%s</p><dl class="row mb-0"><dt class="col-sm-4">Status</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Applicable rule</dt><dd class="col-sm-8">%s</dd></dl></div></article><h2 class="h4">Decision effects</h2><div class="row g-3">`, escapeHTML(ref), escapeHTML(status), escapeHTML(strings.Join(nonEmpty(club, team, player), " — ")), escapeHTML(summary), escapeHTML(status), escapeHTML(ruleRef))
		for effects.Next() {
			var effect, effectStatus, effectTeam, effectPlayer string
			var starts, ends *time.Time
			var points *int
			var amountPence *int64
			if effects.Scan(&effect, &effectStatus, &starts, &ends, &points, &amountPence, &effectTeam, &effectPlayer) != nil {
				continue
			}
			if ends != nil && ends.Before(time.Now()) {
				effectStatus = "expired"
			}
			dates := "No fixed dates"
			if starts != nil {
				dates = starts.In(s.LondonLoc).Format("02 Jan 2006")
			}
			if ends != nil {
				dates += " to " + ends.In(s.LondonLoc).Format("02 Jan 2006")
			}
			fmt.Fprintf(w, `<div class="col-12"><section class="card card-gmcl"><div class="card-body"><div class="d-flex justify-content-between gap-2"><h3 class="h5">%s</h3><span class="badge text-bg-secondary align-self-start">%s</span></div>`, escapeHTML(effectLabel(effect)), escapeHTML(effectStatus))
			if effectSubject := publicEffectSubject(effect, effectTeam, effectPlayer); effectSubject != "" {
				fmt.Fprintf(w, `<p class="mb-1"><strong>Subject:</strong> %s</p>`, escapeHTML(effectSubject))
			}
			fmt.Fprintf(w, `<p class="mb-1">%s</p>`, escapeHTML(dates))
			if points != nil {
				fmt.Fprintf(w, `<p class="mb-1"><strong>Points consequence:</strong> %d point deduction</p>`, *points)
			}
			if amountPence != nil {
				fmt.Fprintf(w, `<p class="mb-1"><strong>Fine:</strong> £%.2f</p>`, float64(*amountPence)/100)
			}
			fmt.Fprint(w, `</div></section></div>`)
		}
		fmt.Fprint(w, `</div><p class="small text-muted mt-3 mb-0">This public record excludes evidence, correspondence, contact details, and internal notes.</p></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleSanctionReportForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := s.loadSanctionReportIdentity(r)
		if !requireCaptainReportIdentity(w, r, identity) {
			return
		}
		reportAction := "/sanctions/report"
		if identity.Authenticated {
			reportAction = "/captain/sanctions/report"
		}
		nativeActive, _, rolloutErr := s.nativeIneligibleRolloutActive(r.Context())
		if rolloutErr != nil {
			http.Error(w, "reporting is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("type") == "ineligible_player" && !nativeActive {
			s.redirectToPrivateGoogleForm(w, r)
			return
		}
		rows, _ := s.DB.Query(r.Context(), `SELECT t.id,cl.name,t.name FROM teams t JOIN clubs cl ON cl.id=t.club_id WHERE t.active ORDER BY cl.name,t.name`)
		if rows != nil {
			defer rows.Close()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Report a sanctions issue")
		writeCaptainNav(w)
		csrf := middleware.CSRFToken(r)
		if identity.Authenticated {
			fmt.Fprint(w, `<main class="container pt-4" style="max-width:760px"><div class="alert alert-success mb-0"><strong>Captain portal details loaded.</strong> Your verified name, email and reporting club have been filled in below. Check them before submitting.</div></main>`)
		}
		if !nativeActive {
			if privateURL, privateErr := configuredPrivateGoogleFormURL(); privateErr == nil {
				fmt.Fprintf(w, `<main class="container pt-4" style="max-width:760px"><div class="alert alert-info mb-0">Ineligible-player reports are still collected through the <a href="%s">private Google form</a> during reconciliation.</div></main>`, escapeHTML(privateURL))
			}
		}
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:760px"><h1 class="h2">Report an issue</h1><p class="text-muted">Ineligible-player reports enter a private triage queue and do not contact a club automatically. Other report types continue to use email verification. Reports and evidence are retained as part of the official record.</p><form method="POST" action="%s" enctype="multipart/form-data" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-6"><label class="form-label">Your name</label><input class="form-control" name="reporter_name" value="%s" required maxlength="150"></div><div class="col-md-6"><label class="form-label">Your email</label><input class="form-control" type="email" name="reporter_email" value="%s" required maxlength="320"></div><div class="col-md-6"><label class="form-label">Report type</label><select class="form-select" id="sanction-report-type" name="source_type" required><option value="discipline">Disciplinary issue</option><option value="ineligible_player">Ineligible player</option><option value="grounds_facilities">Grounds or facilities</option><option value="forfeit">Match forfeit</option><option value="manual">Other</option></select></div><div class="col-md-6"><label class="form-label">Offending club and team</label><select class="form-select" name="team_id" required><option value="">Choose…</option>`, escapeHTML(reportAction), csrf, escapeHTML(identity.Name), escapeHTML(identity.Email))
		if rows != nil {
			for rows.Next() {
				var id int
				var club, team string
				if rows.Scan(&id, &club, &team) == nil {
					fmt.Fprintf(w, `<option value="%d">%s — %s</option>`, id, escapeHTML(club), escapeHTML(team))
				}
			}
		}
		fmt.Fprint(w, `</select></div><div class="col-md-6"><label class="form-label">Match date <span class="ineligible-required-label">(if relevant)</span></label><input class="form-control" type="date" name="match_date" data-ineligible-required></div><div class="col-md-6"><label class="form-label"><span id="sanction-player-label">Affected player</span> <span class="ineligible-required-label">(if relevant)</span></label><input class="form-control" name="player_name" maxlength="200" data-ineligible-required></div>`)
		if nativeActive {
			fmt.Fprint(w, s.nativeIneligibleFormFields(r, identity.ReportingClubID, identity.Role))
		} else {
			fmt.Fprint(w, `<div id="ineligible-player-fields" hidden></div>`)
		}
		fmt.Fprint(w, `<div class="col-12"><label class="form-label" id="sanction-summary-label">What happened?</label><textarea class="form-control" name="summary" rows="7" required maxlength="10000"></textarea></div><div class="col-12"><label class="form-label">Evidence (optional PDF, JPEG, PNG, WebP, MP4, or text; max 10 MB)</label><input class="form-control" type="file" name="evidence" accept=".pdf,image/jpeg,image/png,image/webp,video/mp4,.mp4,.txt"></div><div class="col-12 form-check ms-2"><input class="form-check-input" type="checkbox" name="consent" value="yes" required id="consent"><label class="form-check-label" for="consent">I confirm this report is accurate to the best of my knowledge. The league may use the allegation and approved evidence while protecting personal contact details.</label></div></div><div class="card-footer"><button class="btn btn-danger" id="sanction-report-submit">Submit and verify email</button></div></form></main><script>(function(){var select=document.getElementById('sanction-report-type');var section=document.getElementById('ineligible-player-fields');var button=document.getElementById('sanction-report-submit');var playerLabel=document.getElementById('sanction-player-label');var summaryLabel=document.getElementById('sanction-summary-label');var labels=document.querySelectorAll('.ineligible-required-label');var requested=new URLSearchParams(window.location.search).get('type');if(requested==='ineligible_player'){select.value=requested;}function update(){var active=select.value==='ineligible_player';section.hidden=!active;document.querySelectorAll('[data-ineligible-required]').forEach(function(field){field.required=active;});labels.forEach(function(label){label.textContent=active?'(required)':'(if relevant)';});playerLabel.textContent=active?'Name of defaulting player as shown on scorecard':'Affected player';summaryLabel.textContent=active?'Reason you believe the player is ineligible':'What happened?';button.textContent=active?'Submit for private triage':'Submit and verify email';}select.addEventListener('change',update);update();}());</script>`)
		if !nativeActive {
			fmt.Fprint(w, `<script>(function(){var option=document.querySelector('#sanction-report-type option[value="ineligible_player"]');if(option){option.remove();}}());</script>`)
		}
		pageFooter(w)
	}
}

func (s *Server) handleSanctionReportSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity := s.loadSanctionReportIdentity(r)
		if !requireCaptainReportIdentity(w, r, identity) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(512<<10))
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "invalid or oversized submission", 400)
			return
		}
		name := strings.TrimSpace(r.FormValue("reporter_name"))
		emailAddr := strings.ToLower(strings.TrimSpace(r.FormValue("reporter_email")))
		summary := strings.TrimSpace(r.FormValue("summary"))
		source := r.FormValue("source_type")
		teamID, _ := strconv.Atoi(r.FormValue("team_id"))
		if name == "" || emailAddr == "" || !strings.Contains(emailAddr, "@") || summary == "" || teamID == 0 || r.FormValue("consent") != "yes" {
			http.Error(w, "all required fields and consent are required", 400)
			return
		}
		allowed := map[string]bool{"discipline": true, "ineligible_player": true, "grounds_facilities": true, "forfeit": true, "manual": true}
		if !allowed[source] {
			http.Error(w, "invalid report type", 400)
			return
		}
		var clubID int32
		var offendingClub, teamName string
		if s.DB.QueryRow(r.Context(), `SELECT t.club_id,c.name,t.name FROM teams t JOIN clubs c ON c.id=t.club_id WHERE t.id=$1 AND t.active`, teamID).Scan(&clubID, &offendingClub, &teamName) != nil {
			http.Error(w, "team not found", 400)
			return
		}
		if source == "ineligible_player" {
			nativeActive, _, rolloutErr := s.nativeIneligibleRolloutActive(r.Context())
			if rolloutErr != nil {
				http.Error(w, "ineligible-player reporting is temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			if !nativeActive {
				s.redirectToPrivateGoogleForm(w, r)
				return
			}
			s.stageNativeIneligibleReport(w, r, name, emailAddr, summary, teamID, offendingClub, teamName, false)
			return
		}
		var matchDate any
		if v := r.FormValue("match_date"); v != "" {
			if d, err := time.Parse("2006-01-02", v); err == nil {
				matchDate = d
			}
		}
		var seasonID, weekID any
		lookupDate := time.Now().In(s.LondonLoc).Format("2006-01-02")
		if d, ok := matchDate.(time.Time); ok {
			lookupDate = d.Format("2006-01-02")
		}
		var sid, wid int32
		if s.DB.QueryRow(r.Context(), `SELECT season_id,id FROM weeks WHERE $1::date BETWEEN start_date AND end_date ORDER BY id DESC LIMIT 1`, lookupDate).Scan(&sid, &wid) == nil {
			seasonID = sid
			weekID = wid
		} else {
			_ = s.DB.QueryRow(r.Context(), `SELECT id FROM seasons ORDER BY start_date DESC LIMIT 1`).Scan(&sid)
			if sid != 0 {
				seasonID = sid
			}
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not create report", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var caseID int64
		var ref string
		if err = tx.QueryRow(r.Context(), `INSERT INTO sanction_cases(source_type,status,season_id,week_id,club_id,team_id,player_name,match_date,public_summary,private_summary,reporter_name,reporter_email) VALUES($1,'submitted',$2,$3,$4,$5,$6,$7,'Report awaiting investigation',$8,$9,$10) RETURNING id,reference`, source, seasonID, weekID, clubID, teamID, nullIfEmptyHTTP(r.FormValue("player_name")), matchDate, summary, name, emailAddr).Scan(&caseID, &ref); err != nil {
			http.Error(w, "could not create report", 500)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_label,reason,after_data,request_id) VALUES($1,'report_submitted','reporter',$2,$3,$4,$5)`, caseID, name, "Public report submitted", []byte(`{"reporter_consent":true}`), requestID(r))
		if err != nil {
			http.Error(w, "could not create report", 500)
			return
		}
		if file, header, fileErr := r.FormFile("evidence"); fileErr == nil {
			defer file.Close()
			if err = storeCaseEvidence(r.Context(), tx, caseID, nil, "private", file, header, "reporter", nil); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		raw, hash, err := newPublicToken()
		if err != nil {
			http.Error(w, "could not create verification", 500)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_access_tokens(case_id,token_hash,purpose,expires_at) VALUES($1,$2,'verify_reporter',now()+interval '24 hours')`, caseID, hash)
		if err != nil {
			http.Error(w, "could not create verification", 500)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "could not create report", 500)
			return
		}
		link := sanctionsBaseURL() + "/sanctions/report/verify?token=" + raw
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageTitle := "Check your email"
		message := "Check your email and use the verification link within 24 hours."
		if sanctionsEmailDisabled() {
			pageTitle = "Report received"
			message = "Testing mode is active, so no verification email was sent. The report has been retained for workflow testing."
		} else {
			_ = email.NewFromEnv().Send(emailAddr, "Verify GMCL sanctions report "+ref, "Please verify your report.\n\n"+link+"\n\nCase reference: "+ref)
		}
		pageHead(w, pageTitle)
		writeCaptainNav(w)
		fmt.Fprintf(w, `<main class="container py-5" style="max-width:650px"><div class="alert alert-success"><h1 class="h4">Report received</h1><p>%s</p><p class="mb-0">Reference: <strong>%s</strong></p></div></main>`, escapeHTML(message), escapeHTML(ref))
		pageFooter(w)
	}
}

func storeCaseEvidence(ctx context.Context, tx pgx.Tx, caseID int64, eventID *int64, visibility string, file multipart.File, header *multipart.FileHeader, uploader string, uploaderID any) error {
	key, sum, size, media, err := copyEvidence(file, header)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_case_evidence(case_id,event_id,visibility,original_name,media_type,byte_size,storage_key,sha256,uploaded_by_type,uploaded_by_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, caseID, eventID, visibility, filepath.Base(header.Filename), media, size, key, sum, uploader, uploaderID)
	return err
}

func (s *Server) handleSanctionReportVerify() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := tokenHash(r.URL.Query().Get("token"))
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "verification unavailable", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var tokenID, caseID int64
		err = tx.QueryRow(r.Context(), `SELECT id,case_id FROM sanction_case_access_tokens WHERE token_hash=$1 AND purpose='verify_reporter' AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, h).Scan(&tokenID, &caseID)
		if err != nil {
			http.Error(w, "verification link is invalid or expired", 400)
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE sanction_case_access_tokens SET revoked_at=now(),last_used_at=now() WHERE id=$1`, tokenID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET reporter_verified_at=now(),status='triage',updated_at=now() WHERE id=$1`, caseID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,reason,request_id) VALUES($1,'reporter_verified','reporter','Reporter email verified',$2)`, caseID, requestID(r))
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "verification failed", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Report verified")
		writeCaptainNav(w)
		fmt.Fprint(w, `<main class="container py-5" style="max-width:650px"><div class="alert alert-success"><h1 class="h4">Email verified</h1><p class="mb-0">Your report is now in the league triage queue.</p></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleSanctionCaseResponseForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secureResponseHeaders(w)
		raw := r.URL.Query().Get("token")
		var ref, summary, party string
		var caseID int64
		var isTest bool
		err := s.DB.QueryRow(r.Context(), `SELECT c.id,c.reference,request.allegation_snapshot,COALESCE(p.name,'Club representative'),c.is_test
			FROM sanction_case_access_tokens tok
			JOIN sanction_cases c ON c.id=tok.case_id
			JOIN sanction_response_requests request ON request.access_token_id=tok.id AND request.case_id=tok.case_id AND request.status='pending'
			LEFT JOIN sanction_case_parties p ON p.id=tok.party_id AND p.case_id=tok.case_id
			WHERE tok.token_hash=$1 AND tok.purpose='respond' AND tok.revoked_at IS NULL AND tok.expires_at>now()`, tokenHash(raw)).Scan(&caseID, &ref, &summary, &party, &isTest)
		if err != nil {
			http.Error(w, "case link is invalid or expired", 400)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		secureResponsePageHead(w, "Respond to "+ref)
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:760px"><h1 class="h2">Respond to case %s</h1><div class="alert alert-secondary"><strong>%s</strong><br>%s</div>`, escapeHTML(ref), escapeHTML(party), escapeHTML(summary))
		if isTest {
			fmt.Fprintf(w, `<div class="alert alert-success"><strong>PRIVATE TEST ? no club has been contacted.</strong><br>Enter exactly <code>%s</code> below, submit it, then return to the test-status page.</div>`, privateLinkTestResponse)
		}
		sharedRows, _ := s.DB.Query(r.Context(), portalSharedEvidenceListQuery, caseID)
		if sharedRows != nil {
			defer sharedRows.Close()
			items := 0
			var shared strings.Builder
			for sharedRows.Next() {
				var evidenceID, size int64
				var media string
				if sharedRows.Scan(&evidenceID, &media, &size) == nil {
					items++
					fmt.Fprintf(&shared, `<li class="list-group-item d-flex justify-content-between gap-3"><span>Evidence item %d <small class="text-muted">%s, %d bytes</small></span><a class="btn btn-sm btn-outline-primary" href="/sanctions/case/evidence/%d?token=%s">Download</a></li>`, items, escapeHTML(media), size, evidenceID, url.QueryEscape(raw))
				}
			}
			if items > 0 {
				fmt.Fprintf(w, `<section class="card mb-3"><div class="card-header">Evidence shared for your response</div><ul class="list-group list-group-flush">%s</ul></section>`, shared.String())
			}
		}
		appealControl := `<label class="form-check"><input class="form-check-input" type="checkbox" name="appeal" value="yes"> <span class="form-check-label">This response is a formal appeal against a published decision</span></label>`
		if isTest {
			appealControl = ""
		}
		fmt.Fprintf(w, `<form method="POST" action="/sanctions/case/respond" enctype="multipart/form-data" class="card"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="token" value="%s"><div class="card-body"><label class="form-label">Your explanation</label><textarea class="form-control mb-3" name="response" rows="8" required maxlength="20000"></textarea><label class="form-label">Evidence (optional PDF, JPEG, PNG, WebP, MP4, or text; max 10 MB)</label><input class="form-control mb-3" type="file" name="evidence" accept=".pdf,image/jpeg,image/png,image/webp,video/mp4,.mp4,.txt">%s</div><div class="card-footer"><button class="btn btn-danger">Submit response</button></div></form></main>`, csrf, escapeHTML(raw), appealControl)
		secureResponsePageFooter(w)
	}
}

func (s *Server) handleSanctionCaseResponseSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secureResponseHeaders(w)
		r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(256<<10))
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "invalid or oversized response", 400)
			return
		}
		raw := r.FormValue("token")
		response := strings.TrimSpace(r.FormValue("response"))
		if response == "" {
			http.Error(w, "response is required", 400)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "response unavailable", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var tokenID, caseID int64
		var partyID *int64
		var isTest bool
		var partyName, caseStatus string
		err = tx.QueryRow(r.Context(), `SELECT tok.id,tok.case_id,request.party_id,COALESCE(p.name,'Club representative'),c.status,c.is_test
			FROM sanction_case_access_tokens tok
			JOIN sanction_cases c ON c.id=tok.case_id
			JOIN sanction_response_requests request ON request.access_token_id=tok.id AND request.case_id=tok.case_id AND request.status='pending'
			LEFT JOIN sanction_case_parties p ON p.id=request.party_id AND p.case_id=request.case_id
			WHERE tok.token_hash=$1 AND tok.purpose='respond' AND tok.revoked_at IS NULL AND tok.expires_at>now()
			FOR UPDATE OF tok,c,request`, tokenHash(raw)).Scan(&tokenID, &caseID, &partyID, &partyName, &caseStatus, &isTest)
		if err != nil {
			http.Error(w, "case link is invalid or expired", 400)
			return
		}
		if isTest && response != privateLinkTestResponse {
			http.Error(w, "enter the exact private-test phrase shown on the page", http.StatusBadRequest)
			return
		}
		eventType := "party_response"
		nextStatus := "investigating"
		if r.FormValue("appeal") == "yes" && !isTest {
			if caseStatus != "published" && caseStatus != "closed" {
				http.Error(w, "only a published decision can be appealed", 400)
				return
			}
			eventType = "appeal_submitted"
			nextStatus = "appealed"
		} else if caseStatus != "response_pending" {
			http.Error(w, "this case is no longer accepting an allegation response", http.StatusConflict)
			return
		}
		var eventID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id) VALUES($1,$2,'captain',$3,$4,$5,$6) RETURNING id`, caseID, eventType, partyID, partyName, response, requestID(r)).Scan(&eventID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_case_access_tokens SET last_used_at=now(),revoked_at=now() WHERE id=$1`, tokenID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_response_requests SET status='responded',responded_at=now(),closed_at=now() WHERE access_token_id=$1 AND status='pending'`, tokenID)
		}
		if err == nil {
			// Suppress the already queued day-five reminder. Outbox content is
			// immutable; its processed marker is the permitted cancellation edge.
			_, err = tx.Exec(r.Context(), `UPDATE sanction_notification_outbox SET processed_at=now() WHERE case_id=$1 AND message_kind='response_reminder' AND processed_at IS NULL`, caseID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status=$2,updated_at=now() WHERE id=$1`, caseID, nextStatus)
		}
		if err == nil {
			if file, header, fileErr := r.FormFile("evidence"); fileErr == nil {
				defer file.Close()
				err = storeCaseEvidence(r.Context(), tx, caseID, &eventID, "party", file, header, "captain", partyID)
			}
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not store response", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		secureResponsePageHead(w, "Response recorded")
		message := "Your explanation and evidence are saved in the case history and cannot be edited. Contact GMCL if a correction is needed."
		if isTest {
			message = "Private end-to-end test response recorded successfully. Return to the administrator test-status page to see the completed result."
		}
		fmt.Fprintf(w, `<main class="container py-5" style="max-width:650px"><div class="alert alert-success"><h1 class="h4">Response recorded</h1><p class="mb-0">%s</p></div></main>`, escapeHTML(message))
		secureResponsePageFooter(w)
	}
}

func secureResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}

func secureResponsePageHead(w io.Writer, title string) {
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s - GMCL secure response</title><style>
	:root{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;color:#202124;background:#f5f6f8}*{box-sizing:border-box}body{margin:0}.container{width:min(100%% - 2rem,760px);margin-inline:auto}.py-4{padding-block:2rem}.py-5{padding-block:3rem}h1{line-height:1.2}.h2{font-size:1.7rem}.h4{font-size:1.25rem}.alert,.card{background:#fff;border:1px solid #d9dde3;border-radius:.5rem;padding:1rem;margin-bottom:1rem}.alert-secondary{border-left:5px solid #6c757d}.alert-success{border-left:5px solid #198754}.card-body{padding:.25rem}.card-footer{border-top:1px solid #e6e8eb;margin-top:1rem;padding-top:1rem}.form-label{display:block;font-weight:650;margin:.8rem 0 .35rem}.form-control{display:block;width:100%%;font:inherit;padding:.7rem;border:1px solid #8a9099;border-radius:.35rem;background:#fff}.form-check{display:flex;gap:.5rem;align-items:flex-start;margin-top:.75rem}.btn{display:inline-block;border:0;border-radius:.35rem;padding:.65rem 1rem;font:inherit;font-weight:700;cursor:pointer}.btn-danger{color:#fff;background:#b01932}.list-group{list-style:none;padding:0}.list-group-item{padding:.65rem;border-top:1px solid #e6e8eb}.text-muted{color:#626973}a{color:#7f1226}</style></head><body>`, escapeHTML(title))
}

func secureResponsePageFooter(w io.Writer) {
	fmt.Fprint(w, `<footer class="container" style="padding:1rem 0 2rem;color:#626973;font-size:.9rem">Greater Manchester Cricket League secure case response</footer></body></html>`)
}

func (s *Server) handleAdminCaseRequestResponse() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sanctionsEmailDisabled() {
			http.Error(w, "sanctions email queueing is disabled in this environment", http.StatusServiceUnavailable)
			return
		}
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "could not create link", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var liveResponseRequest bool
		_ = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sanction_response_requests WHERE case_id=$1 AND status IN ('queued','pending'))`, id).Scan(&liveResponseRequest)
		if liveResponseRequest {
			http.Error(w, "this case already has a queued or pending club response request", http.StatusConflict)
			return
		}
		var sourceType, caseStatus, clubName, officialEmail, publicSummary string
		var teamID, clubID int32
		err = tx.QueryRow(r.Context(), `SELECT c.source_type,c.status,c.team_id,c.club_id,cl.name,contact.email,COALESCE(c.public_summary,'')
			FROM sanction_cases c JOIN teams t ON t.id=c.team_id JOIN clubs cl ON cl.id=c.club_id
			JOIN sanction_club_contacts contact ON contact.club_id=c.club_id AND contact.contact_type='official_mailbox' AND contact.active AND contact.verified_at IS NOT NULL
			WHERE c.id=$1 ORDER BY contact.verified_at DESC NULLS LAST,contact.id DESC LIMIT 1 FOR UPDATE OF c`, id).
			Scan(&sourceType, &caseStatus, &teamID, &clubID, &clubName, &officialEmail, &publicSummary)
		if err != nil {
			http.Error(w, "the offending club's official mailbox is unresolved; update the club-contact directory before sending", 400)
			return
		}
		if !map[string]bool{"submitted": true, "triage": true, "investigating": true}[caseStatus] {
			http.Error(w, "a club response can only be requested before a decision is proposed", http.StatusConflict)
			return
		}
		parsedOfficial, parseErr := mail.ParseAddress(strings.TrimSpace(officialEmail))
		if parseErr != nil || parsedOfficial.Address == "" || !strings.EqualFold(parsedOfficial.Address, strings.TrimSpace(officialEmail)) {
			http.Error(w, "the offending club's verified official mailbox is invalid; correct the club-contact directory before sending", http.StatusBadRequest)
			return
		}
		officialEmail = strings.ToLower(parsedOfficial.Address)
		if sourceType == "ineligible_player" && !ineligibleOutboundEmailEnabled() {
			http.Error(w, "ineligible-player outbound email is not enabled", http.StatusServiceUnavailable)
			return
		}
		if sourceType == "ineligible_player" {
			if err = sanctiondomain.EnsureIneligibleLinkedIntakesCurrent(r.Context(), tx, id); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		allegedRule, ruleErr := loadCaseAllegedRule(r.Context(), tx, id)
		if ruleErr != nil {
			http.Error(w, "record the published rule alleged in this investigation before requesting a club response", http.StatusConflict)
			return
		}
		allegedRuleParagraph := allegedRuleCorrespondenceParagraph(allegedRule)
		type savedDraft struct {
			id            int64
			subject, body string
		}
		loadDraft := func(kind string) (savedDraft, error) {
			var draft savedDraft
			err := tx.QueryRow(r.Context(), `SELECT id,subject,body FROM sanction_correspondence_revisions
				WHERE case_id=$1 AND message_kind=$2 AND audience='offending_club' AND status='draft'
				ORDER BY revision DESC,id DESC LIMIT 1`, id, kind).Scan(&draft.id, &draft.subject, &draft.body)
			return draft, err
		}
		requestDraft, requestErr := loadDraft("response_request")
		reminderDraft, reminderErr := loadDraft("response_reminder")
		if requestErr != nil || reminderErr != nil {
			http.Error(w, "save both the response request and reminder drafts before sending", http.StatusConflict)
			return
		}
		if validationErr := validateResponseDraftContent("response_request", requestDraft.body, publicSummary, allegedRuleParagraph); validationErr != nil {
			http.Error(w, "saved response request is invalid or stale: "+validationErr.Error()+"; save a new request draft", http.StatusConflict)
			return
		}
		if validationErr := validateResponseDraftContent("response_reminder", reminderDraft.body, publicSummary, allegedRuleParagraph); validationErr != nil {
			http.Error(w, "saved response reminder is invalid or stale: "+validationErr.Error()+"; save a new reminder draft", http.StatusConflict)
			return
		}
		sensitiveValues, privacyErr := sanctiondomain.CaseReporterIdentityValues(r.Context(), tx, id)
		if privacyErr != nil {
			http.Error(w, "could not validate correspondence privacy", http.StatusInternalServerError)
			return
		}
		reportingAliases, privacyErr := sanctiondomain.CaseReportingClubIdentityValues(r.Context(), tx, id, &clubID)
		if privacyErr != nil {
			http.Error(w, "could not validate correspondence privacy", http.StatusInternalServerError)
			return
		}
		sensitiveValues = append(sensitiveValues, reportingAliases...)
		if sanctiondomain.ContainsPrivateIdentity(publicSummary+"\n"+requestDraft.subject+"\n"+requestDraft.body+"\n"+reminderDraft.subject+"\n"+reminderDraft.body, sensitiveValues...) {
			http.Error(w, "the response portal allegation or saved drafts contain reporter or reporting-club identity", http.StatusBadRequest)
			return
		}
		var partyID int64
		err = tx.QueryRow(r.Context(), `SELECT id FROM sanction_case_parties WHERE case_id=$1 AND team_id=$2 AND relationship='offending_club' ORDER BY id LIMIT 1`, id, teamID).Scan(&partyID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_parties(case_id,party_type,name,email,team_id,relationship) VALUES($1,'club',$2,$3,$4,'offending_club') RETURNING id`, id, clubName, officialEmail, teamID).Scan(&partyID)
		}
		if err != nil {
			http.Error(w, "could not create party", 500)
			return
		}
		raw, hash, err := newPublicToken()
		if err != nil {
			http.Error(w, "could not create link", 500)
			return
		}
		var tokenID int64
		// The token is intentionally unusable until the initial email is accepted.
		// The outbox worker activates its seven-day expiry in the same transaction
		// that records successful delivery and schedules the day-five reminder.
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_access_tokens(case_id,party_id,token_hash,purpose,expires_at) VALUES($1,$2,$3,'respond',now()) RETURNING id`, id, partyID, hash).Scan(&tokenID)
		link := sanctionsBaseURL() + "/sanctions/case/respond?token=" + raw
		body := strings.Replace(requestDraft.body, responseLinkPlaceholder, link, 1)
		reminderBody := strings.Replace(reminderDraft.body, responseLinkPlaceholder, link, 1)
		actor := adminActor(r)
		var requestCorrespondenceID, reminderCorrespondenceID int64
		recipientsJSON, _ := json.Marshal([]string{officialEmail})
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_correspondence_revisions(case_id,supersedes_id,message_kind,audience,revision,status,recipients,subject,body,created_by_admin_id)
				VALUES($1,$2,'response_request','offending_club',(SELECT COALESCE(MAX(revision),0)+1 FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind='response_request' AND audience='offending_club'),'queued',$3,$4,$5,$6) RETURNING id`, id, requestDraft.id, recipientsJSON, requestDraft.subject, body, actorIDAny(actor)).Scan(&requestCorrespondenceID)
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_correspondence_revisions(case_id,supersedes_id,message_kind,audience,revision,status,recipients,subject,body,created_by_admin_id)
				VALUES($1,$2,'response_reminder','offending_club',(SELECT COALESCE(MAX(revision),0)+1 FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind='response_reminder' AND audience='offending_club'),'queued',$3,$4,$5,$6) RETURNING id`, id, reminderDraft.id, recipientsJSON, reminderDraft.subject, reminderBody, actorIDAny(actor)).Scan(&reminderCorrespondenceID)
		}
		var policyID *int64
		_ = tx.QueryRow(r.Context(), `SELECT id FROM sanction_notification_policy_versions WHERE active AND source_type='*' AND event_type='decision_published' ORDER BY version DESC LIMIT 1`).Scan(&policyID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_notification_outbox(case_id,policy_version_id,correspondence_revision_id,message_kind,idempotency_key,recipient,subject,body)
				VALUES($1,$2,$3,'response_request',$4,$5,$6,$7)`, id, policyID, requestCorrespondenceID, fmt.Sprintf("case:%d:response-request:%d", id, requestCorrespondenceID), officialEmail, requestDraft.subject, body)
		}
		if err == nil {
			allegationSnapshot := publicSummary
			if allegedRuleParagraph != "" {
				allegationSnapshot += "\n\n" + allegedRuleParagraph
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_response_requests(case_id,party_id,access_token_id,correspondence_revision_id,reminder_correspondence_revision_id,status,allegation_snapshot)
				VALUES($1,$2,$3,$4,$5,'queued',$6)`, id, partyID, tokenID, requestCorrespondenceID, reminderCorrespondenceID, allegationSnapshot)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id) VALUES($1,'response_request_queued','admin',$2,$3,$4,$5)`, id, actorIDAny(actor), actor.Label, "Initial secure response request queued for the official mailbox for "+clubName+"; the response clock starts only after successful delivery", actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status='response_pending',updated_at=now() WHERE id=$1`, id)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not create link", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}

func (s *Server) handleAdminCases() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		group := strings.TrimSpace(r.URL.Query().Get("group"))
		predicate := "c.status != 'withdrawn'"
		title := "Sanctions cases"
		if group == "investigating" || group == "awaiting_decision" || group == "closed" {
			predicate = ineligibleCaseGroupPredicate(group, "c")
			title = map[string]string{"investigating": "Ineligible-player cases under investigation", "awaiting_decision": "Ineligible-player cases awaiting decision", "closed": "Closed ineligible-player cases"}[group]
		}
		rows, err := s.DB.Query(r.Context(), `SELECT c.id,c.reference,c.source_type,c.status,COALESCE(c.player_name,''),COALESCE(cl.name,''),COALESCE(t.name,''),c.created_at,COALESCE(a.username,'') FROM sanction_cases c LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id LEFT JOIN admin_users a ON a.id=c.assigned_admin_id WHERE NOT c.is_test AND NOT EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=c.id AND training.event_type='case_training_designated') AND `+predicate+` ORDER BY CASE c.status WHEN 'submitted' THEN 0 WHEN 'triage' THEN 1 WHEN 'decision_proposed' THEN 2 ELSE 3 END,c.created_at DESC LIMIT 300`)
		if err != nil {
			http.Error(w, "could not load cases", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, title)
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4"><div class="d-flex flex-column flex-lg-row justify-content-between gap-3 mb-3"><div><h1 class="h2">%s</h1><p class="text-muted mb-0">Investigation, two-stage approval, publication and immutable history.</p></div><div class="d-grid d-sm-flex flex-wrap gap-2 align-self-lg-start"><a href="/admin/cases" class="btn btn-outline-secondary">All cases</a><a href="/admin/cases/new" class="btn btn-danger">Add card, ban, fine or points decision</a><form method="POST" action="/admin/cases/link-tests" class="d-inline"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-outline-success">Create private link test</button></form><a href="/admin/cases/imports" class="btn btn-primary">Import legacy bans &amp; cards</a><a href="https://sanctions.gmcl.co.uk/" target="_blank" rel="noopener" class="btn btn-outline-primary">Public register</a><a href="/admin/cases/automation" class="btn btn-outline-secondary">Automation</a><a href="/admin/cases/recipients" class="btn btn-outline-secondary">Recipients</a></div></div><div class="alert alert-info"><strong>Manual sanctions use the case workflow:</strong> create the case, propose its effect and reason, then have a separately authorised admin approve it before publication. Every step is retained in the immutable timeline.</div><div class="table-responsive"><table class="table table-hover responsive-cards align-middle" id="cases"><thead><tr><th>Reference</th><th>Source</th><th>Player</th><th>Status</th><th>Assigned</th><th>Opened</th></tr></thead><tbody>`, escapeHTML(title), escapeHTML(csrf))
		shown := 0
		for rows.Next() {
			var id int64
			var ref, source, status, player, club, team, assigned string
			var created time.Time
			if rows.Scan(&id, &ref, &source, &status, &player, &club, &team, &created, &assigned) == nil {
				club, team = adminCaseListSubject(player, club, team)
				shown++
				fmt.Fprintf(w, `<tr><td data-label="Reference"><a href="/admin/cases/%d"><strong>%s</strong></a></td><td data-label="Source">%s</td><td data-label="Club / team">%s — %s</td><td data-label="Status">%s</td><td data-label="Assigned">%s</td><td data-label="Opened">%s</td></tr>`, id, escapeHTML(ref), escapeHTML(source), escapeHTML(club), escapeHTML(team), escapeHTML(status), escapeHTML(assigned), created.In(s.LondonLoc).Format("02 Jan 2006 15:04"))
			}
		}
		if shown == 0 {
			fmt.Fprint(w, `<tr><td colspan="6" class="text-center text-muted py-4">No cases match this status.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table></div></main>`)
		pageFooter(w)
	}
}

func adminCaseListSubject(player, club, team string) (string, string) {
	player = strings.TrimSpace(player)
	club = strings.TrimSpace(club)
	team = strings.TrimSpace(team)
	if player == "" {
		return club, team
	}
	context := strings.Trim(club+" / "+team, " / ")
	return player, context
}
func (s *Server) handleAdminCaseNew() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		rows, err := s.DB.Query(r.Context(), `SELECT t.id,cl.name,t.name FROM teams t JOIN clubs cl ON cl.id=t.club_id WHERE t.active ORDER BY cl.name,t.name`)
		if err != nil {
			http.Error(w, "could not load teams", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Add sanction case")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><a href="/admin/cases" class="btn btn-sm btn-outline-secondary mb-3">Back to cases</a><h1 class="h2">Add a card, ban, fine or points decision</h1><p class="text-muted">This creates an attributed manual case. Ineligible-player reports must use the private intake workflow so reporter provenance, club parties and Hussan assignment cannot be bypassed.</p><form method="POST" action="/admin/cases" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-6"><label class="form-label">Case source</label><select class="form-select" name="source_type" required><option value="manual">Manual referral</option><option value="discipline">Discipline</option><option value="grounds_facilities">Grounds or facilities</option><option value="forfeit">Forfeit / withdrawal</option><option value="play_cricket">Play-Cricket finding</option></select></div><div class="col-md-6"><label class="form-label">Offence / match date</label><input class="form-control" type="date" name="match_date" required value="%s"></div><div class="col-12"><label class="form-label">Affected team</label><select class="form-select" name="team_id" required><option value="">Choose club and team...</option>`, csrf, time.Now().In(s.LondonLoc).Format("2006-01-02"))
		for rows.Next() {
			var id int32
			var club, team string
			if rows.Scan(&id, &club, &team) == nil {
				fmt.Fprintf(w, `<option value="%d">%s — %s</option>`, id, escapeHTML(club), escapeHTML(team))
			}
		}
		fmt.Fprint(w, `</select></div><div class="col-md-6"><label class="form-label">Player name <span class="text-muted">(if applicable)</span></label><input class="form-control" name="player_name" maxlength="200"></div><div class="col-12"><label class="form-label">Public reason / recorded facts</label><textarea class="form-control" name="public_summary" rows="4" required maxlength="5000"></textarea><div class="form-text">This may appear in the public register after approval and publication. Do not include evidence or private correspondence.</div></div><div class="col-12"><label class="form-label">Private investigation note</label><textarea class="form-control" name="private_summary" rows="4" maxlength="10000"></textarea></div></div><div class="card-footer"><button class="btn btn-danger">Create case and continue to decision</button></div></form></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminCaseCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		source := strings.TrimSpace(r.FormValue("source_type"))
		if source == "ineligible_player" {
			http.Error(w, "ineligible-player cases must be created from the private intake queue", http.StatusBadRequest)
			return
		}
		allowedSources := map[string]bool{"manual": true, "discipline": true, "ineligible_player": true, "grounds_facilities": true, "forfeit": true, "play_cricket": true}
		teamID, teamErr := strconv.ParseInt(r.FormValue("team_id"), 10, 32)
		matchDate, dateErr := time.Parse("2006-01-02", r.FormValue("match_date"))
		publicSummary := strings.TrimSpace(r.FormValue("public_summary"))
		privateSummary := strings.TrimSpace(r.FormValue("private_summary"))
		if !allowedSources[source] || teamErr != nil || teamID <= 0 || dateErr != nil || publicSummary == "" {
			http.Error(w, "source, team, date and public reason are required", 400)
			return
		}
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", 401)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not create case", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var clubID int32
		if err = tx.QueryRow(r.Context(), `SELECT club_id FROM teams WHERE id=$1 AND active`, teamID).Scan(&clubID); err != nil {
			http.Error(w, "active team not found", 400)
			return
		}
		var seasonID int32
		var weekID *int32
		var matchedWeek int32
		if tx.QueryRow(r.Context(), `SELECT season_id,id FROM weeks WHERE $1::date BETWEEN start_date AND end_date ORDER BY id DESC LIMIT 1`, matchDate).Scan(&seasonID, &matchedWeek) == nil {
			weekID = &matchedWeek
		} else if err = tx.QueryRow(r.Context(), `SELECT id FROM seasons WHERE $1::date BETWEEN start_date AND end_date ORDER BY id DESC LIMIT 1`, matchDate).Scan(&seasonID); err != nil {
			http.Error(w, "no season covers the selected date", 400)
			return
		}
		var caseID int64
		var ref string
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_cases(source_type,status,season_id,week_id,club_id,team_id,player_name,match_date,public_summary,private_summary,assigned_admin_id) VALUES($1,'investigating',$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id,reference`, source, seasonID, weekID, clubID, teamID, nullIfEmptyHTTP(r.FormValue("player_name")), matchDate, publicSummary, nullIfEmptyHTTP(privateSummary), *actor.ID).Scan(&caseID, &ref)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id) VALUES($1,'manual_case_created','admin',$2,$3,$4,jsonb_build_object('reference',$5,'source_type',$6,'team_id',$7,'match_date',$8::date),$9)`, caseID, *actor.ID, actor.Label, "Manual case created by administrator", ref, source, teamID, matchDate, actor.RequestID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not create case", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", caseID), http.StatusSeeOther)
	}
}

type adminCaseDecision struct {
	Status        string
	Revision      int
	PublicReason  string
	RuleReference string
	EffectiveAt   time.Time
}

type adminCaseEffect struct {
	EffectType         string
	Status             string
	PlayerName         string
	AmountPence        *int64
	Points             *int
	StartsAt           *time.Time
	EndsAt             *time.Time
	TriggerCondition   string
	CountsForTotting   bool
	Explanation        string
	YellowBalanceAfter string
	TeamRedCountAfter  string
}

func (s *Server) loadAdminCaseDecision(ctx context.Context, caseID int64) (adminCaseDecision, []adminCaseEffect, bool) {
	var decisionID int64
	var decision adminCaseDecision
	err := s.DB.QueryRow(ctx, `
		SELECT id,status,revision,public_reason,COALESCE(rule_reference,''),effective_at
		FROM sanction_decision_revisions
		WHERE case_id=$1
		ORDER BY revision DESC,id DESC
		LIMIT 1`, caseID).Scan(&decisionID, &decision.Status, &decision.Revision, &decision.PublicReason, &decision.RuleReference, &decision.EffectiveAt)
	if err != nil {
		return adminCaseDecision{}, nil, false
	}
	rows, err := s.DB.Query(ctx, `
		SELECT effect_type,status,COALESCE(player_name,''),amount_pence,points,starts_at,ends_at,
		       COALESCE(trigger_condition,''),counts_for_totting,
		       COALESCE(NULLIF(public_details->>'explanation',''),public_details->>'calculation_explanation',''),
		       COALESCE(public_details->>'yellow_balance_after',''),
		       COALESCE(public_details->>'team_red_count_after','')
		FROM sanction_effect_revisions e
		WHERE decision_revision_id=$1
		  AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id)
		ORDER BY id`, decisionID)
	if err != nil {
		return adminCaseDecision{}, nil, false
	}
	defer rows.Close()
	effects := make([]adminCaseEffect, 0, 1)
	for rows.Next() {
		var effect adminCaseEffect
		if err := rows.Scan(&effect.EffectType, &effect.Status, &effect.PlayerName, &effect.AmountPence, &effect.Points, &effect.StartsAt, &effect.EndsAt, &effect.TriggerCondition, &effect.CountsForTotting, &effect.Explanation, &effect.YellowBalanceAfter, &effect.TeamRedCountAfter); err != nil {
			return adminCaseDecision{}, nil, false
		}
		effects = append(effects, effect)
	}
	if rows.Err() != nil {
		return adminCaseDecision{}, nil, false
	}
	return decision, effects, true
}

func adminCaseDecisionHTML(decision adminCaseDecision, effects []adminCaseEffect) string {
	title := "Current decision"
	switch decision.Status {
	case "proposed":
		title = "Proposed punishment"
	case "approved":
		title = "Approved punishment"
	case "rejected":
		title = "Rejected proposal"
	case "overturned":
		title = "Overturned punishment"
	case "corrected":
		title = "Superseded approved decision"
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<section class="card border-primary mb-4"><div class="card-header d-flex justify-content-between align-items-center"><strong>%s</strong><span class="badge text-bg-primary">%s</span></div><div class="card-body">`, escapeHTML(title), escapeHTML(decision.Status))
	if len(effects) == 0 {
		fmt.Fprint(&out, `<div class="alert alert-warning mb-3">This decision has no recorded sanction effect.</div>`)
	}
	for _, effect := range effects {
		fmt.Fprintf(&out, `<div class="border rounded p-3 mb-3"><div class="d-flex justify-content-between gap-2"><h3 class="h4 mb-2">%s</h3><span class="badge text-bg-secondary align-self-start">%s</span></div>`, escapeHTML(adminSanctionEffectLabel(effect.EffectType)), escapeHTML(effect.Status))
		if effect.Explanation != "" {
			fmt.Fprintf(&out, `<p class="mb-2">%s</p>`, escapeHTML(effect.Explanation))
		}
		fmt.Fprint(&out, `<dl class="row mb-0">`)
		if effect.PlayerName != "" {
			fmt.Fprintf(&out, `<dt class="col-sm-5">Player</dt><dd class="col-sm-7">%s</dd>`, escapeHTML(effect.PlayerName))
		}
		if effect.AmountPence != nil {
			fmt.Fprintf(&out, `<dt class="col-sm-5">Fine</dt><dd class="col-sm-7">£%.2f</dd>`, float64(*effect.AmountPence)/100)
		}
		if effect.Points != nil {
			label := "Points adjustment"
			if effect.EffectType == "yellow_card" || effect.EffectType == "red_card" || effect.EffectType == "suspended_red" {
				label = "Card-system deduction"
			}
			fmt.Fprintf(&out, `<dt class="col-sm-5">%s</dt><dd class="col-sm-7">%d point%s</dd>`, label, *effect.Points, pluralSuffix(*effect.Points))
		}
		if effect.YellowBalanceAfter != "" {
			fmt.Fprintf(&out, `<dt class="col-sm-5">Yellow balance after</dt><dd class="col-sm-7">%s</dd>`, escapeHTML(effect.YellowBalanceAfter))
		}
		if effect.TeamRedCountAfter != "" {
			fmt.Fprintf(&out, `<dt class="col-sm-5">Team red count after</dt><dd class="col-sm-7">%s</dd>`, escapeHTML(effect.TeamRedCountAfter))
		}
		if effect.StartsAt != nil {
			fmt.Fprintf(&out, `<dt class="col-sm-5">Starts</dt><dd class="col-sm-7">%s</dd>`, effect.StartsAt.Format("02 Jan 2006"))
		}
		if effect.EndsAt != nil {
			fmt.Fprintf(&out, `<dt class="col-sm-5">Ends / remedy date</dt><dd class="col-sm-7">%s</dd>`, effect.EndsAt.Format("02 Jan 2006"))
		}
		if effect.TriggerCondition != "" {
			fmt.Fprintf(&out, `<dt class="col-sm-5">Trigger</dt><dd class="col-sm-7">%s</dd>`, escapeHTML(effect.TriggerCondition))
		}
		totting := "No"
		if effect.CountsForTotting {
			totting = "Yes"
		}
		fmt.Fprintf(&out, `<dt class="col-sm-5">Counts towards card totting</dt><dd class="col-sm-7">%s</dd></dl></div>`, totting)
	}
	fmt.Fprintf(&out, `<dl class="row small mb-0"><dt class="col-sm-5">Decision reason</dt><dd class="col-sm-7">%s</dd>`, escapeHTML(decision.PublicReason))
	if decision.RuleReference != "" {
		fmt.Fprintf(&out, `<dt class="col-sm-5">Rule reference</dt><dd class="col-sm-7">%s</dd>`, escapeHTML(decision.RuleReference))
	}
	fmt.Fprintf(&out, `<dt class="col-sm-5">Revision</dt><dd class="col-sm-7">%d</dd></dl></div></section>`, decision.Revision)
	return out.String()
}

func adminSanctionEffectLabel(effect string) string {
	labels := map[string]string{
		"yellow_card":       "Yellow card",
		"red_card":          "Red card",
		"suspended_red":     "Suspended red card",
		"player_ban":        "Player ban",
		"team_ban":          "Team ban",
		"fine":              "Fine",
		"card_points":       "Card-system points",
		"points_adjustment": "Separate points adjustment",
		"warning":           "Warning",
		"no_action":         "No action",
	}
	if label := labels[effect]; label != "" {
		return label
	}
	return strings.ReplaceAll(effect, "_", " ")
}

func pluralSuffix(value int) string {
	if value == 1 || value == -1 {
		return ""
	}
	return "s"
}

func adminCaseAssignmentHTML(caseID int64, csrf string, assignedAdminID *int32, assignedAdminName string, currentAdminID *int32) string {
	if sameAdminAssignment(assignedAdminID, currentAdminID) {
		return `<section class="card mb-3 border-success"><div class="card-body"><strong>Investigator: assigned to you</strong><div class="text-muted small mt-1">No further assignment action is needed.</div></div></section>`
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<form method="POST" action="/admin/cases/%d/assign-self" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-body">`, caseID, escapeHTML(csrf))
	button := "Assign investigation to me"
	if assignedAdminID != nil {
		name := strings.TrimSpace(assignedAdminName)
		if name == "" {
			name = "another administrator"
		}
		fmt.Fprintf(&out, `<p class="mb-2"><strong>Current investigator:</strong> %s</p>`, escapeHTML(name))
		button = "Reassign investigation to me"
	}
	fmt.Fprintf(&out, `<button class="btn btn-outline-primary">%s</button></div></form>`, button)
	return out.String()
}

func sameAdminAssignment(assignedAdminID, currentAdminID *int32) bool {
	return assignedAdminID != nil && currentAdminID != nil && *assignedAdminID == *currentAdminID
}

type adminDecisionSubject struct {
	id    int64
	label string
}

func adminDecisionEffectsHTML(subjects []adminDecisionSubject) string {
	effectOptions := []struct{ value, label string }{
		{"yellow_card", "Yellow card"}, {"red_card", "Direct red card"}, {"suspended_red", "Suspended red"},
		{"player_ban", "Player ban"}, {"team_ban", "Team ban"}, {"fine", "Fine"},
		{"points_adjustment", "League-table points adjustment"}, {"warning", "Warning"}, {"no_action", "No action"},
	}
	var out strings.Builder
	for i := 0; i < 5; i++ {
		if i == 0 {
			fmt.Fprint(&out, `<fieldset class="border rounded p-3"><legend class="float-none w-auto px-2 fs-6">Primary effect</legend>`)
		} else {
			fmt.Fprintf(&out, `<details class="border rounded bg-light"><summary class="p-3 fw-semibold">Add another effect <span class="text-muted fw-normal">(optional %d of 4)</span></summary><div class="p-3 border-top">`, i)
		}
		fmt.Fprintf(&out, `<div class="row g-3"><div class="col-md-6"><label class="form-label">Effect</label><select class="form-select" name="effect_type"><option value="">%s</option>`, map[bool]string{true: "Select effect", false: "None"}[i == 0])
		for _, option := range effectOptions {
			fmt.Fprintf(&out, `<option value="%s">%s</option>`, option.value, option.label)
		}
		fmt.Fprint(&out, `</select></div><div class="col-md-6"><label class="form-label">Who or what does it apply to?</label><select class="form-select" name="case_subject_id"><option value="">Primary offending team / case</option>`)
		for _, subject := range subjects {
			fmt.Fprintf(&out, `<option value="%d">%s</option>`, subject.id, escapeHTML(subject.label))
		}
		fmt.Fprint(&out, `</select></div><div class="col-md-6"><label class="form-label">Fine amount <span class="text-muted">(GBP, fine only)</span></label><input class="form-control" name="fine_pounds" type="number" min="0.01" step="0.01"></div><div class="col-md-6"><label class="form-label">League points <span class="text-muted">(points adjustment only)</span></label><input class="form-control" name="points" type="number" step="1"><div class="form-text">Card deductions are calculated automatically from league policy after submission; do not enter them here.</div></div><div class="col-md-6"><label class="form-label">End or remedy date</label><input class="form-control" name="ends_at" type="date"></div><div class="col-md-6"><label class="form-label">Card remedy</label><select class="form-select" name="rescindable"><option value="no">Normal</option><option value="yes">Rescindable yellow</option></select></div><div class="col-12"><label class="form-label">Trigger or condition <span class="text-muted">(optional)</span></label><input class="form-control" name="trigger_condition"></div></div>`)
		if i == 0 {
			fmt.Fprint(&out, `</fieldset>`)
		} else {
			fmt.Fprint(&out, `</div></details>`)
		}
	}
	return out.String()
}

func (s *Server) adminDecisionBundleFormHTML(ctx context.Context, caseID int64, csrf, publicSummary string) string {
	allegedRuleReference := ""
	allegedRule := caseAllegedRule{}
	if loadedRule, ruleErr := loadCaseAllegedRule(ctx, s.DB, caseID); ruleErr == nil {
		allegedRule = loadedRule
		allegedRuleReference = allegedRule.Reference
	}
	appealGuidance := hawkAppealGuidanceForRule(allegedRule)
	var subjects []adminDecisionSubject
	rows, err := s.DB.Query(ctx, `SELECT cs.id,cs.subject_type,COALESCE(t.name,''),COALESCE(cs.player_name,''),cs.is_primary
		FROM sanction_case_subjects cs LEFT JOIN teams t ON t.id=cs.team_id
		WHERE cs.case_id=$1 AND (
			NOT EXISTS(SELECT 1 FROM sanction_case_subject_intakes bridge WHERE bridge.subject_id=cs.id)
			OR EXISTS(
				SELECT 1 FROM sanction_case_intake_merge_resolutions resolution
				WHERE resolution.case_id=cs.case_id
				  AND resolution.id=(SELECT latest.id FROM sanction_case_intake_merge_resolutions latest
					WHERE latest.case_id=resolution.case_id AND latest.intake_id=resolution.intake_id
					ORDER BY latest.id DESC LIMIT 1)
				  AND cs.id IN (resolution.team_subject_id,resolution.player_subject_id,COALESCE(resolution.match_subject_id,0))
			)
		) ORDER BY cs.is_primary DESC,cs.id`, caseID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var item adminDecisionSubject
			var subjectType, team, player string
			var primary bool
			if rows.Scan(&item.id, &subjectType, &team, &player, &primary) == nil {
				item.label = strings.TrimSpace(strings.Join([]string{subjectType, team, player}, " - "))
				item.label = strings.Trim(item.label, " -")
				if primary {
					item.label += " (primary)"
				}
				subjects = append(subjects, item)
			}
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<section class="card mb-4"><div class="card-header">Prepare decision for approval</div><form method="POST" action="/admin/cases/%d/propose"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><div class="row g-3 mb-4"><div class="col-md-6"><label class="form-label">Final rule determination or explicit not-applicable determination</label><input class="form-control" name="rule_reference" value="%s" required><div class="form-text">Prefilled from the alleged rule. Change it if the investigation establishes a different rule or no breach.</div></div><div class="col-md-6"><label class="form-label">Outcome email / letter subject</label><input class="form-control" name="outcome_subject" placeholder="GMCL ineligible-player case outcome"></div><div class="col-12"><label class="form-label">Approved public reason</label><textarea class="form-control" name="public_reason" required rows="4">%s</textarea><div class="form-text">Public-register wording. Do not include correspondence, private evidence, or reporter details.</div></div><div class="col-12"><label class="form-label">Findings for club outcome letters</label><textarea class="form-control" name="outcome_findings" required rows="4">%s</textarea><div class="form-text">This is sent to both clubs. It must be safe for the reporting club and must not quote the offending club's response.</div></div><div class="col-12"><label class="form-label">Appeal instructions</label><textarea class="form-control" name="appeal_instructions" rows="2">Any appeal must be submitted to the league in accordance with the current GMCL regulations.</textarea></div><div class="col-12"><label class="form-label">Private rationale</label><textarea class="form-control" name="private_reason" rows="4"></textarea></div></div><h3 class="h6">Decision effects</h3><p class="small text-muted">Add up to five subject-specific effects. Card-system points are calculated by policy; only a separate points adjustment creates Denver's Play-Cricket task. Enter a fine or league-points value only on its matching effect; mixed values are rejected.</p><div class="vstack gap-3">`, caseID, escapeHTML(csrf), escapeHTML(allegedRuleReference), escapeHTML(publicSummary), escapeHTML(publicSummary))
	if review, ok := s.loadScorecardPointsReview(ctx, caseID); ok {
		fmt.Fprint(&out, scorecardPointsReviewHTML(review))
	}
	fmt.Fprint(&out, adminDecisionEffectsHTML(subjects))
	fmt.Fprint(&out, `</div></div><div class="card-footer d-flex flex-column flex-md-row justify-content-between align-items-md-center gap-3"><span class="small text-muted">This first saves the decision and generates all three complete audience versions. You review them on the next screen before anything is sent to Denver.</span><button class="btn btn-primary align-self-start align-self-md-auto">Save decision and review complete emails</button></div></form></section>`)
	html := out.String()
	html = strings.Replace(html, "Any appeal must be submitted to the league in accordance with the current GMCL regulations.", escapeHTML(appealGuidance.Instructions), 1)
	html = strings.Replace(html, `</textarea></div><div class="col-12"><label class="form-label">Private rationale`, `</textarea><div class="form-text"><strong>HawkAI published-rule check:</strong> `+escapeHTML(appealGuidance.Explanation)+`</div></div><div class="col-12"><label class="form-label">Private rationale`, 1)
	return html
}

func adminCaseBackDestination(source string, assignedAdminID, currentAdminID *int32) (string, string) {
	if assignedAdminID != nil && currentAdminID != nil && *assignedAdminID == *currentAdminID {
		return "Back to my cases", "/admin/dashboard#my-cases"
	}
	return "Back to cases", "/admin/cases"
}

func adminCaseFailureHTML(failure string, blockingCaseID int64) string {
	var out strings.Builder
	fmt.Fprintf(&out, `<div class="alert alert-warning"><div>%s</div>`, escapeHTML(failure))
	if blockingCaseID > 0 {
		fmt.Fprintf(&out, `<a class="btn btn-sm btn-outline-dark mt-2" href="/admin/cases/%d">Open blocking card proposal</a>`, blockingCaseID)
	}
	fmt.Fprint(&out, `</div>`)
	return out.String()
}

type adminCaseReporterView struct {
	Name, Email, Role, Phone, ReportingClub string
}

func adminCaseReporterHTML(reporter adminCaseReporterView) string {
	if strings.TrimSpace(reporter.Name+reporter.Email+reporter.Role+reporter.Phone+reporter.ReportingClub) == "" {
		return `<section class="card mb-4 border-warning"><div class="card-header"><strong>Reported by</strong></div><div class="card-body text-muted">Reporter details were not captured for this case.</div></section>`
	}
	value := func(input string) string {
		if strings.TrimSpace(input) == "" {
			return `<span class="text-muted">Not recorded</span>`
		}
		return escapeHTML(input)
	}
	email := value(reporter.Email)
	if strings.TrimSpace(reporter.Email) != "" {
		email = fmt.Sprintf(`<a href="mailto:%s">%s</a>`, escapeHTML(reporter.Email), escapeHTML(reporter.Email))
	}
	return fmt.Sprintf(`<section class="card mb-4 border-info"><div class="card-header d-flex justify-content-between gap-2"><strong>Reported by</strong><span class="badge text-bg-light border">Private case information</span></div><div class="card-body"><dl class="row mb-0"><dt class="col-sm-3">Name</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Role</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Email</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Telephone</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Reporting club</dt><dd class="col-sm-9">%s</dd></dl><p class="small text-muted mb-0">When an email address is recorded, this reporter receives the reporting-side final outcome communication.</p></div></section>`, value(reporter.Name), value(reporter.Role), email, value(reporter.Phone), value(reporter.ReportingClub))
}

type adminCaseResponseView struct {
	ID         int64
	EventType  string
	ActorType  string
	ActorLabel string
	Body       string
	Channel    string
	Respondent string
	ReceivedAt time.Time
	Unreviewed bool
}

func adminCaseResponseHTML(caseID int64, csrf string, response adminCaseResponseView, loc *time.Location) string {
	if response.ID == 0 {
		return ""
	}
	if loc == nil {
		loc = time.Local
	}
	source := strings.TrimSpace(response.Respondent)
	if source == "" {
		source = strings.TrimSpace(response.ActorLabel)
	}
	if source == "" {
		source = strings.ReplaceAll(response.ActorType, "_", " ")
	}
	channel := strings.TrimSpace(response.Channel)
	if channel == "" {
		channel = map[bool]string{true: "secure club portal", false: "recorded response"}[response.EventType == "party_response"]
	}
	badge := `<span class="badge text-bg-success">Reviewed</span>`
	action := ""
	border := "border-success"
	if response.Unreviewed {
		badge = `<span class="badge text-bg-danger">Needs review</span>`
		border = "border-danger border-3"
		action = fmt.Sprintf(`<form method="POST" action="/admin/cases/%d/response-reviewed" class="mt-3"><input type="hidden" name="csrf_token" value="%s"><label class="form-label">Review note (optional)</label><input class="form-control" name="note" maxlength="2000"><button class="btn btn-danger mt-2">Mark reply reviewed and continue</button></form>`, caseID, escapeHTML(csrf))
	}
	return fmt.Sprintf(`<section class="card mb-4 %s" id="club-response"><div class="card-header d-flex flex-wrap justify-content-between gap-2"><strong>Club reply received</strong>%s</div><div class="card-body"><dl class="row small mb-3"><dt class="col-sm-3">From</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Via</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Received</dt><dd class="col-sm-9">%s</dd></dl><div class="alert alert-light border mb-0"><div class="fw-semibold mb-2">Their response</div><div style="white-space:pre-wrap">%s</div></div>%s</div></section>`, border, badge, escapeHTML(source), escapeHTML(channel), escapeHTML(response.ReceivedAt.In(loc).Format("02 Jan 2006 15:04")), escapeHTML(response.Body), action)
}

func adminCaseNextStageHTML(hasResponse, unreviewed bool) string {
	if !hasResponse {
		return ""
	}
	responseStep := `<li><span class="badge text-bg-success">Complete</span> Club reply reviewed.</li>`
	if unreviewed {
		responseStep = `<li><strong>Read the club reply and mark it reviewed.</strong></li>`
	}
	return `<section class="card mb-4 border-primary" id="next-stage"><div class="card-header"><strong>Next stage: review, decide and issue</strong></div><div class="card-body"><ol class="mb-0">` + responseStep + `<li>Confirm or revise the published rule being applied.</li><li>Set out the findings and sanctions, then submit the complete decision.</li><li>Denver or another authorised independent approver reviews the decision.</li><li>After approval, issue the locked outcome emails and letters.</li></ol></div></section>`
}
func (s *Server) handleAdminCaseDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		var ref, source, status, publicSummary, privateSummary, club, team string
		var reporter adminCaseReporterView
		var assignedAdminID *int32
		var assignedAdminName string
		var hasProposed, isTest, isTraining, ownerReviewRequired, sentForApproval bool
		err := s.DB.QueryRow(r.Context(), `SELECT c.reference,c.source_type,c.status,COALESCE(c.public_summary,''),COALESCE(c.private_summary,''),COALESCE(cl.name,''),COALESCE(t.name,''),COALESCE(c.reporter_name,''),COALESCE(c.reporter_email,''),COALESCE(c.reporter_role,''),COALESCE(c.reporter_phone,''),COALESCE(reporting.name,''),c.assigned_admin_id,COALESCE(assigned.username,''),EXISTS(
			SELECT 1 FROM sanction_decision_revisions d WHERE d.case_id=c.id AND d.status='proposed'
			  AND NOT EXISTS(SELECT 1 FROM sanction_decision_revisions newer WHERE newer.supersedes_id=d.id)
		),c.is_test,
		EXISTS(SELECT 1 FROM sanction_case_events training WHERE training.case_id=c.id AND training.event_type='case_training_designated'),
		EXISTS(SELECT 1 FROM sanction_case_events review WHERE review.case_id=c.id AND review.event_type='decision_owner_review_required'
			AND review.metadata->>'decision_revision_id'=(SELECT d.id::text FROM sanction_decision_revisions d WHERE d.case_id=c.id AND d.status='proposed' ORDER BY d.revision DESC,d.id DESC LIMIT 1)),
		EXISTS(SELECT 1 FROM sanction_case_events sent WHERE sent.case_id=c.id AND sent.event_type='decision_sent_for_approval'
			AND sent.metadata->>'decision_revision_id'=(SELECT d.id::text FROM sanction_decision_revisions d WHERE d.case_id=c.id AND d.status='proposed' ORDER BY d.revision DESC,d.id DESC LIMIT 1))
		FROM sanction_cases c LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id LEFT JOIN clubs reporting ON reporting.id=c.reporting_club_id LEFT JOIN admin_users assigned ON assigned.id=c.assigned_admin_id WHERE c.id=$1`, id).Scan(&ref, &source, &status, &publicSummary, &privateSummary, &club, &team, &reporter.Name, &reporter.Email, &reporter.Role, &reporter.Phone, &reporter.ReportingClub, &assignedAdminID, &assignedAdminName, &hasProposed, &isTest, &isTraining, &ownerReviewRequired, &sentForApproval)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var latestResponse adminCaseResponseView
		_ = s.DB.QueryRow(r.Context(), `SELECT response.id,response.event_type,response.actor_type,COALESCE(response.actor_label,''),COALESCE(response.reason,''),
			COALESCE(response.metadata->>'channel',''),COALESCE(response.metadata->>'respondent',''),response.created_at,
			NOT EXISTS(SELECT 1 FROM sanction_case_events reviewed WHERE reviewed.case_id=response.case_id
				AND reviewed.event_type='response_reviewed' AND reviewed.metadata->>'response_event_id'=response.id::text)
			FROM sanction_case_events response
			WHERE response.id=(SELECT event.id FROM sanction_case_events event WHERE event.case_id=$1
				AND event.event_type IN ('party_response','external_response_recorded') ORDER BY event.id DESC LIMIT 1)`, id).
			Scan(&latestResponse.ID, &latestResponse.EventType, &latestResponse.ActorType, &latestResponse.ActorLabel, &latestResponse.Body,
				&latestResponse.Channel, &latestResponse.Respondent, &latestResponse.ReceivedAt, &latestResponse.Unreviewed)
		var hasResponseRequest bool
		_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sanction_case_events WHERE case_id=$1 AND event_type='response_request_queued')`, id).Scan(&hasResponseRequest)
		csrf := middleware.CSRFToken(r)
		backLabel, backURL := adminCaseBackDestination(source, assignedAdminID, adminActor(r).ID)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Case "+ref)
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:1280px"><a href="%s" class="btn btn-sm btn-outline-secondary mb-3">%s</a><div class="d-flex justify-content-between"><div><h1 class="h2">%s</h1><p>%s - %s - %s</p></div><span class="badge text-bg-secondary align-self-start">%s</span></div><div class="row g-4"><div class="col-xl-8"><section class="card mb-4"><div class="card-header">Case record</div><div class="card-body"><h2 class="h5">Public summary</h2><p>%s</p><h2 class="h5">Private summary</h2><p>%s</p></div></section>`, escapeHTML(backURL), escapeHTML(backLabel), escapeHTML(ref), escapeHTML(source), escapeHTML(club), escapeHTML(team), escapeHTML(status), escapeHTML(publicSummary), escapeHTML(privateSummary))
		fmt.Fprint(w, adminCaseReporterHTML(reporter))
		fmt.Fprint(w, s.loadAdminCaseSourceReportHTML(r.Context(), id))
		if success := strings.TrimSpace(r.URL.Query().Get("success")); success != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">%s</div>`, escapeHTML(success))
		}
		if isTraining {
			fmt.Fprint(w, `<div class="alert alert-warning"><strong>Training case - real email enabled.</strong> This case is excluded from live workload totals, but response requests and approved outcomes use the normal recipients and delivery system. Check recipients before each send.</div>`)
		}
		if failure := strings.TrimSpace(r.URL.Query().Get("error")); failure != "" {
			blockingCaseID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("blocking_case")), 10, 64)
			fmt.Fprint(w, adminCaseFailureHTML(failure, blockingCaseID))
		}
		fmt.Fprint(w, adminCaseResponseHTML(id, csrf, latestResponse, s.LondonLoc))
		fmt.Fprint(w, adminCaseNextStageHTML(latestResponse.ID > 0, latestResponse.Unreviewed))
		s.writeAdminHistoricalOutcomeSnapshots(w, r, id)
		canRequestResponse := map[string]bool{"submitted": true, "triage": true, "investigating": true}[status]
		s.writeAdminCaseAllegedRule(w, r.Context(), id, status, csrf, source)
		if source == "ineligible_player" {
			s.writeAdminScorecardEvidence(w, r.Context(), id, csrf)
		}
		if canRequestResponse && !hasResponseRequest && latestResponse.ID == 0 {
			s.writeAdminResponseDraftForms(w, r, id, csrf, ref, team, publicSummary, source)
		}
		if source == "ineligible_player" {
			if hasResponseRequest || latestResponse.ID > 0 {
				fmt.Fprint(w, `<details class="card mb-4"><summary class="card-header"><strong>Completed correspondence and email history</strong></summary><div class="card-body">`)
				s.writeAdminCaseEmailPreviews(w, r, id, ref, team, publicSummary, hasProposed)
				fmt.Fprint(w, `</div></details>`)
			} else {
				s.writeAdminCaseEmailPreviews(w, r, id, ref, team, publicSummary, hasProposed)
			}
		}
		canPropose := map[string]bool{"submitted": true, "triage": true, "investigating": true}[status]
		if !hasProposed && canPropose && !latestResponse.Unreviewed && sameAdminAssignment(assignedAdminID, adminActor(r).ID) {
			fmt.Fprint(w, s.adminDecisionBundleFormHTML(r.Context(), id, csrf, publicSummary))
		} else if !hasProposed && canPropose && !latestResponse.Unreviewed {
			fmt.Fprint(w, `<div class="alert alert-warning"><strong>Case owner action required.</strong> Only the assigned case owner can decide the sanctions, card points, league-points deductions and fines after checking the previous system.</div>`)
		}
		if status == "triage" && hasProposed {
			fmt.Fprint(w, `<div class="alert alert-info">This is a shadow-mode calculated candidate. Change the source to manual mode before a future run; this candidate remains available for reconciliation but cannot be published.</div>`)
		}
		if decision, effects, ok := s.loadAdminCaseDecision(r.Context(), id); ok {
			fmt.Fprint(w, adminCaseDecisionHTML(decision, effects))
			if status == "decision_proposed" || (status == "triage" && hasProposed) {
				s.writeAdminOutcomeDraftForms(w, r, id, csrf)
				currentActor := adminActor(r)
				ownerCanSend := !sentForApproval && sanctiondomain.CanSubmitDecisionForApproval(status, assignedAdminID, currentActor.ID)
				if ownerCanSend {
					fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/send-for-approval" class="card mb-4 border-primary"><input type="hidden" name="csrf_token" value="%s"><div class="card-header"><strong>Final owner check</strong></div><div class="card-body"><p class="mb-0">Read the complete offending-club, reporting-club and official versions above. Only continue when the findings, rule, sanctions, appeal wording and audience differences are correct.</p></div><div class="card-footer"><button class="btn btn-primary">Save all three and send to Denver for approval</button></div></form>`, id, escapeHTML(csrf))
				} else if ownerReviewRequired && !sentForApproval {
					fmt.Fprint(w, `<div class="alert alert-info"><strong>The case owner is reviewing the three complete email versions.</strong> This decision has not yet been sent for independent approval.</div>`)
				} else if sentForApproval {
					fmt.Fprint(w, `<div class="alert alert-success"><strong>Owner review complete.</strong> The three email versions have been sent to Denver for independent approval.</div>`)
				}
			} else if map[string]bool{"approved": true, "published": true, "appealed": true, "closed": true}[status] {
				fmt.Fprintf(w, `<div class="d-flex flex-wrap gap-2 mb-4"><a class="btn btn-sm btn-outline-primary" target="_blank" rel="noopener" href="/admin/cases/%d/outcome-preview?audience=offending_club">View offending-club PDF</a><a class="btn btn-sm btn-outline-primary" target="_blank" rel="noopener" href="/admin/cases/%d/outcome-preview?audience=reporting_club">View reporting-club PDF</a><a class="btn btn-sm btn-outline-secondary" target="_blank" rel="noopener" href="/admin/cases/%d/outcome-preview?audience=official">View league-official PDF</a></div>`, id, id, id)
			}
		}
		fmt.Fprint(w, `<section class="card"><div class="card-header">Case history</div><div class="card-body py-2 small text-muted">This history cannot be edited. If something needs correcting, add a new correction so the original action remains visible.</div><ul class="list-group list-group-flush">`)
		events, _ := s.DB.Query(r.Context(), `SELECT event_type,actor_type,COALESCE(actor_label,''),COALESCE(reason,''),created_at,emergency_override FROM sanction_case_events WHERE case_id=$1 ORDER BY id DESC`, id)
		if events != nil {
			defer events.Close()
			for events.Next() {
				var typ, actor, label, reason string
				var at time.Time
				var emergency bool
				if events.Scan(&typ, &actor, &label, &reason, &at, &emergency) == nil {
					flag := ""
					if emergency {
						flag = ` <span class="badge text-bg-danger">emergency override</span>`
					}
					fmt.Fprintf(w, `<li class="list-group-item"><strong>%s</strong>%s<div>%s</div><small class="text-muted">%s · %s %s</small></li>`, escapeHTML(typ), flag, escapeHTML(reason), at.In(s.LondonLoc).Format("02 Jan 2006 15:04"), escapeHTML(actor), escapeHTML(label))
				}
			}
		}
		fmt.Fprint(w, `</ul></section>`)
		s.writeAdminCaseDeliveryHistory(w, r, id, csrf)
		fmt.Fprint(w, `</div><aside class="col-xl-4">`)
		actor := adminActor(r)
		s.writeAdminCaseDelegationControls(w, r, id, csrf, assignedAdminID, assignedAdminName, actor.ID)
		evidenceRows, _ := s.DB.Query(r.Context(), `SELECT evidence.id,evidence.original_name,evidence.media_type,evidence.byte_size,evidence.sha256,evidence.created_at,evidence.visibility,
			COALESCE((SELECT sharing.action FROM sanction_evidence_sharing_events sharing WHERE sharing.case_id=evidence.case_id AND sharing.evidence_id=evidence.id AND sharing.audience='offending_club' ORDER BY sharing.id DESC LIMIT 1),''),
			evidence.redacted_at IS NULL,provenance.source_evidence_id,COALESCE(reviewer.username,''),provenance.reviewed_at,allowed.evidence_id IS NOT NULL
			FROM sanction_case_evidence evidence
			LEFT JOIN sanction_case_evidence_derivatives provenance ON provenance.case_id=evidence.case_id AND provenance.derivative_evidence_id=evidence.id
			LEFT JOIN admin_users reviewer ON reviewer.id=provenance.reviewer_admin_id
			LEFT JOIN sanction_offending_club_evidence_derivatives allowed ON allowed.case_id=evidence.case_id AND allowed.evidence_id=evidence.id
			WHERE evidence.case_id=$1 ORDER BY evidence.id`, id)
		if evidenceRows != nil {
			defer evidenceRows.Close()
			fmt.Fprint(w, `<section class="card mb-3"><div class="card-header">Evidence</div><ul class="list-group list-group-flush">`)
			count := 0
			for evidenceRows.Next() {
				var evidenceID, size int64
				var name, media, sum, visibility, sharingAction string
				var at time.Time
				var sourceEvidenceID *int64
				var reviewer string
				var reviewedAt *time.Time
				var available, eligible bool
				if evidenceRows.Scan(&evidenceID, &name, &media, &size, &sum, &at, &visibility, &sharingAction, &available, &sourceEvidenceID, &reviewer, &reviewedAt, &eligible) == nil {
					count++
					controls := adminEvidenceDisclosureControlsHTML(id, evidenceID, csrf, adminEvidenceDisclosureState{
						SourceEvidenceID: sourceEvidenceID,
						Reviewer:         reviewer,
						ReviewedAt:       reviewedAt,
						Eligible:         eligible,
						Available:        available,
						SharingAction:    sharingAction,
					})
					fmt.Fprintf(w, `<li class="list-group-item"><a href="/admin/cases/%d/evidence/%d">%s</a><div class="small text-muted">%s · %d bytes · %s · SHA-256 %s</div>%s</li>`, id, evidenceID, escapeHTML(name), escapeHTML(media), size, escapeHTML(visibility), escapeHTML(sum[:minInt(12, len(sum))]), controls)
				}
			}
			if count == 0 {
				fmt.Fprint(w, `<li class="list-group-item text-muted">No evidence uploaded.</li>`)
			}
			fmt.Fprint(w, `</ul></section>`)
		}
		if status == "decision_proposed" && (!ownerReviewRequired || sentForApproval) {
			actor := adminActor(r)
			if actor.ID != nil && s.adminHasPermission(r.Context(), *actor.ID, "sanctions_approve") {
				fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/approve" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Independent approval</div><div class="card-body"><p>Review the email versions above. One approval action saves and locks the exact emails and PDFs; it does not send them until the separate issue step.</p><label class="form-label">Additional outcome recipients (optional)</label><textarea class="form-control" name="additional_recipients" rows="2" placeholder="stuart@example.org, gary@example.org"></textarea><div class="form-text mb-3">Play-Cricket recipients are added automatically for red-card/card points and league points. Finance recipients are added automatically for fines.</div><label class="form-label">Emergency override reason (super-admin only)</label><textarea class="form-control" name="emergency_reason" rows="2"></textarea></div><div class="card-footer"><button class="btn btn-success">Save email versions and approve decision</button></div></form>`, id, csrf)
			} else {
				fmt.Fprint(w, `<div class="alert alert-warning"><strong>Awaiting an authorised independent approver.</strong> You can review the decision and exact email formatting above, but your account does not currently have sanctions approval access.</div>`)
			}
		}
		if (status == "decision_proposed" && (!ownerReviewRequired || sentForApproval)) || (status == "triage" && hasProposed) {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/reject" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Reject calculated proposal</div><div class="card-body"><label class="form-label">Reason</label><textarea class="form-control" name="reason" required rows="2"></textarea></div><div class="card-footer"><button class="btn btn-outline-secondary">Reject proposal</button></div></form>`, id, csrf)
		}
		if status == "approved" {
			s.writeAdminIneligibleReopenControl(w, r, id, source, status, csrf)
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/publish" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><p>This queues the exact locked email and PDF for the offending club, reporting club and required league officials. A no-action decision is delivered and closed without public-register publication.</p><button class="btn btn-danger">Issue approved outcomes</button></div></form>`, id, csrf)
		}
		if status == "approved" || status == "published" || status == "appealed" {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/overturn" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Overturn decision</div><div class="card-body"><label class="form-label">Reason</label><textarea class="form-control" name="reason" required rows="3"></textarea></div><div class="card-footer"><button class="btn btn-outline-danger">Record reversal</button></div></form>`, id, csrf)
		}
		if map[string]bool{"submitted": true, "triage": true, "investigating": true, "response_pending": true, "decision_proposed": true}[status] {
			fmt.Fprint(w, adminCloseCaseNoActionHTML(id, csrf, status, hasProposed, assignedAdminID, actor.ID))
		}
		if map[string]bool{"submitted": true, "triage": true, "investigating": true, "response_pending": true}[status] {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/investigation-note" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Investigation note</div><div class="card-body"><textarea class="form-control" name="note" rows="3" maxlength="20000" required placeholder="Add a private investigation note"></textarea><div class="form-text">Saved notes cannot be edited later. Add a correction if something changes.</div></div><div class="card-footer"><button class="btn btn-outline-primary">Save note</button></div></form>`, id, escapeHTML(csrf))
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/manual-response" enctype="multipart/form-data" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Record external club response</div><div class="card-body"><div class="row g-2"><div class="col-md-5"><label class="form-label">Channel</label><select class="form-select" name="channel" required><option value="email">Email</option><option value="phone">Phone</option><option value="meeting">Meeting</option><option value="other">Other</option></select></div><div class="col-md-7"><label class="form-label">Respondent / mailbox</label><input class="form-control" name="respondent" maxlength="300"></div><div class="col-12"><label class="form-label">Response</label><textarea class="form-control" name="response" rows="4" maxlength="20000" required></textarea></div><div class="col-12"><label class="form-label">Attachment (optional PDF, JPEG, PNG, WebP, MP4, or text; max 10 MB)</label><input class="form-control" type="file" name="evidence" accept="application/pdf,image/jpeg,image/png,image/webp,video/mp4,text/plain"></div></div></div><div class="card-footer"><button class="btn btn-outline-success">Record response</button></div></form>`, id, escapeHTML(csrf))
		}
		fmt.Fprint(w, adminUndoCaseOpeningHTML(id, csrf, source, status, isTest))
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/correct" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Correct case summary</div><div class="card-body"><label class="form-label">Public summary</label><textarea class="form-control mb-2" name="public_summary" rows="3">%s</textarea><label class="form-label">Private summary</label><textarea class="form-control mb-2" name="private_summary" rows="3">%s</textarea><label class="form-label">Why is this correction needed?</label><textarea class="form-control" name="reason" required rows="2"></textarea><div class="form-text">The corrected version is added to the case history; the earlier wording remains visible.</div></div><div class="card-footer"><button class="btn btn-outline-primary">Save correction</button></div></form></aside></div></main>`, id, csrf, escapeHTML(publicSummary), escapeHTML(privateSummary))
		pageFooter(w)
	}
}

type adminCaseEmailPreview struct {
	kind          string
	audience      string
	subject       string
	body          string
	recipientText string
	status        string
	revision      int
	savedAt       *time.Time
}

func pendingOutcomeEmailTemplate(ref, audience string) (string, string) {
	subject := "GMCL case outcome " + ref
	switch audience {
	case "offending_club":
		return subject, "Dear Club Secretary,\n\nCase reference: " + ref +
			"\n\nFindings:\n[Added after the decision is proposed]" +
			"\n\nRule determination:\n[Added after the decision is proposed]" +
			"\n\nDecision and sanctions:\n[Added after the decision is proposed]" +
			"\n\nAppeal instructions:\n[Added after the decision is proposed]" +
			"\n\nRegards,\nGreater Manchester Cricket League"
	case "reporting_club":
		return subject, "Dear Club Secretary,\n\nGMCL case " + ref + " has reached an outcome." +
			"\n\nFindings:\n[Added after the decision is proposed]" +
			"\n\nRule determination:\n[Added after the decision is proposed]" +
			"\n\nFinal outcome:\n[Added after the decision is proposed]" +
			"\n\nRegards,\nGreater Manchester Cricket League"
	default:
		return subject, "Approved league outcome record\n\nCase: " + ref +
			"\nSource: ineligible player" +
			"\nOffending club: [Added after the decision is proposed]" +
			"\nReporting club: [Added after the decision is proposed]" +
			"\n\nFindings:\n[Added after the decision is proposed]" +
			"\n\nRule determination:\n[Added after the decision is proposed]" +
			"\n\nDecision and sanctions:\n[Added after the decision is proposed]" +
			"\n\nAppeal instructions:\n[Added after the decision is proposed]"
	}
}

func correspondenceStatusBadgeClass(status string) string {
	status = strings.TrimSpace(status)
	switch status {
	case "sent", "delivered", "approved", "locked":
		return "text-bg-success"
	case "failed", "bounced", "complained", "revoked":
		return "text-bg-danger"
	case "draft":
		return "text-bg-warning"
	case "queued", "sending", "processed":
		return "text-bg-primary"
	default:
		if strings.Contains(status, "not saved") || strings.Contains(status, "pending") {
			return "text-bg-warning"
		}
		return "text-bg-secondary"
	}
}

func effectiveCorrespondenceDisplayStatus(snapshotStatus, deliveryStatus string) string {
	deliveryStatus = strings.TrimSpace(deliveryStatus)
	if deliveryStatus != "" {
		return deliveryStatus
	}
	return strings.TrimSpace(snapshotStatus)
}

func (s *Server) writeAdminCaseEmailPreviews(w http.ResponseWriter, r *http.Request, caseID int64, ref, teamName, publicSummary string, hasProposed bool) {
	allegedRuleParagraph := "[Save a reviewed alleged rule to insert it here.]"
	if allegedRule, err := loadCaseAllegedRule(r.Context(), s.DB, caseID); err == nil {
		allegedRuleParagraph = allegedRuleCorrespondenceParagraph(allegedRule)
	}
	responseDefaults := defaultAdminResponseDraftViews(ref, teamName, publicSummary, allegedRuleParagraph)
	previews := []adminCaseEmailPreview{
		{kind: "response_request", audience: "offending_club", subject: responseDefaults["response_request"].subject, body: responseDefaults["response_request"].body, recipientText: "Verified offending-club recipient is added when queued", status: "template - not saved"},
		{kind: "response_reminder", audience: "offending_club", subject: responseDefaults["response_reminder"].subject, body: responseDefaults["response_reminder"].body, recipientText: "Verified offending-club recipient is added when queued", status: "template - not saved"},
	}
	for _, audience := range []string{"offending_club", "reporting_club", "official"} {
		subject, body := pendingOutcomeEmailTemplate(ref, audience)
		previews = append(previews, adminCaseEmailPreview{
			kind: "outcome_" + audience, audience: audience, subject: subject, body: body,
			recipientText: "Verified " + strings.ReplaceAll(audience, "_", " ") + " recipient is added when queued",
			status:        "template - decision pending",
		})
		if audience == "offending_club" {
			previews[len(previews)-1].recipientText = "Verified offending-club recipient plus " + sanctiondomain.PlayCricketHelpCopyRecipient
		}
	}
	if hasProposed {
		service := sanctiondomain.NewService(s.DB)
		for i := 2; i < len(previews); i++ {
			draft, err := service.OutcomeDraft(r.Context(), caseID, previews[i].audience)
			if err != nil {
				continue
			}
			previews[i].kind = draft.MessageKind
			previews[i].subject = draft.Subject
			previews[i].body = draft.Body
			previews[i].status = "generated - not saved"
			if draft.Exists {
				previews[i].status = "draft"
				previews[i].revision = draft.Revision
			}
		}
	}

	rows, err := s.DB.Query(r.Context(), `
		SELECT DISTINCT ON (correspondence.message_kind,correspondence.audience)
		       correspondence.message_kind,correspondence.audience,correspondence.revision,
		       correspondence.status,COALESCE(correspondence.recipients,'[]'::jsonb)::text,
		       correspondence.subject,correspondence.body,correspondence.created_at,
		       COALESCE(delivery.status,'')
		FROM sanction_correspondence_revisions correspondence
		LEFT JOIN LATERAL (
			SELECT CASE
				WHEN outbox.revoked_at IS NOT NULL THEN 'revoked'
				ELSE COALESCE(attempt.status,CASE WHEN outbox.processed_at IS NULL THEN 'queued' ELSE 'processed' END)
			END AS status
			FROM sanction_notification_outbox outbox
			LEFT JOIN LATERAL (
				SELECT latest.status
				FROM sanction_notification_attempts latest
				WHERE latest.outbox_id=outbox.id
				ORDER BY latest.attempt_number DESC,latest.id DESC LIMIT 1
			) attempt ON TRUE
			WHERE outbox.correspondence_revision_id=correspondence.id
			ORDER BY outbox.id DESC LIMIT 1
		) delivery ON TRUE
		WHERE correspondence.case_id=$1
		ORDER BY correspondence.message_kind,correspondence.audience,
		         correspondence.revision DESC,correspondence.id DESC`, caseID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind, audience, status, recipientsJSON, subject, body, deliveryStatus string
		var revision int
		var createdAt time.Time
		if rows.Scan(&kind, &audience, &revision, &status, &recipientsJSON, &subject, &body, &createdAt, &deliveryStatus) != nil {
			continue
		}
		recipients := []string{}
		_ = json.Unmarshal([]byte(recipientsJSON), &recipients)
		recipientText := strings.Join(recipients, ", ")
		if recipientText == "" {
			recipientText = "Recipients are added when this message is queued"
		}
		for i := range previews {
			isResponse := previews[i].kind == kind && previews[i].audience == audience
			isOutcome := i >= 2 && previews[i].audience == audience && (strings.HasPrefix(kind, "outcome_") || kind == "no_action_outcome")
			if !isResponse && !isOutcome {
				continue
			}
			if previews[i].savedAt != nil && !createdAt.After(*previews[i].savedAt) {
				continue
			}
			previews[i].kind = kind
			previews[i].subject = subject
			previews[i].body = body
			previews[i].recipientText = recipientText
			previews[i].status = effectiveCorrespondenceDisplayStatus(status, deliveryStatus)
			previews[i].revision = revision
			savedAt := createdAt
			previews[i].savedAt = &savedAt
		}
	}

	fmt.Fprint(w, `<section class="card mb-4 border-primary"><div class="card-header d-flex flex-wrap justify-content-between align-items-center gap-2"><span>Email templates and previews</span><span class="badge text-bg-light border">Nothing sends from this screen</span></div><div class="card-body"><p class="mb-1">These are the five messages used during an ineligible-player case.</p><p class="small text-muted">Templates are visible before they are saved. Once a workflow message is saved, its exact latest wording and recipients replace the template here.</p>`)
	for i, preview := range previews {
		statusClass := correspondenceStatusBadgeClass(preview.status)
		versionText := ""
		if preview.revision > 0 {
			versionText = fmt.Sprintf(` <span class="small text-muted">version %d</span>`, preview.revision)
		}
		savedText := "Not saved yet"
		if preview.savedAt != nil {
			savedText = preview.savedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")
		}
		open := ""
		if i == 0 {
			open = " open"
		}
		displayBody := strings.ReplaceAll(preview.body, responseLinkPlaceholder, "[secure response link generated when queued]")
		fmt.Fprintf(w, `<details class="border rounded mb-2"%s><summary class="p-3 d-flex flex-wrap justify-content-between gap-2"><span><strong>%s</strong> <span class="text-muted">to %s</span></span><span><span class="badge %s">%s</span>%s</span></summary><div class="border-top p-3"><dl class="row small mb-3"><dt class="col-sm-3">To</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Subject</dt><dd class="col-sm-9">%s</dd><dt class="col-sm-3">Saved</dt><dd class="col-sm-9">%s</dd></dl><div class="bg-light border rounded p-3" style="white-space:pre-wrap">%s</div></div></details>`,
			open, escapeHTML(plainIneligibleStatus(preview.kind)), escapeHTML(plainIneligibleStatus(preview.audience)),
			statusClass, escapeHTML(preview.status), versionText, escapeHTML(preview.recipientText),
			escapeHTML(preview.subject), escapeHTML(savedText), escapeHTML(displayBody))
	}
	fmt.Fprint(w, `<p class="small text-muted mb-0 mt-3">Outcome placeholders are filled automatically from the proposed findings, rule determination, effects and appeal instructions.</p>`)
	fmt.Fprint(w, `</div></section>`)
}

func (s *Server) writeAdminCaseDeliveryHistory(w http.ResponseWriter, r *http.Request, caseID int64, csrf string) {
	rows, err := s.DB.Query(r.Context(), `SELECT outbox.id,outbox.message_kind,outbox.recipient,outbox.subject,outbox.created_at,outbox.available_at,outbox.processed_at,
		outbox.revoked_at,COALESCE(outbox.revocation_reason,''),
		CASE WHEN outbox.revoked_at IS NOT NULL THEN 'revoked' ELSE COALESCE(latest.status,CASE WHEN outbox.processed_at IS NULL THEN 'queued' ELSE 'processed' END) END,latest.occurred_at,
		COALESCE(latest.provider_message_id,''),COALESCE(latest.error_message,'')
		FROM sanction_notification_outbox outbox
		LEFT JOIN LATERAL (SELECT attempt.status,attempt.occurred_at,attempt.provider_message_id,attempt.error_message
			FROM sanction_notification_attempts attempt WHERE attempt.outbox_id=outbox.id ORDER BY attempt.attempt_number DESC,attempt.id DESC LIMIT 1) latest ON TRUE
		WHERE outbox.case_id=$1 ORDER BY outbox.id DESC`, caseID)
	if err != nil {
		return
	}
	defer rows.Close()
	fmt.Fprint(w, `<section class="card mt-4"><div class="card-header">Correspondence and delivery</div><div class="table-responsive"><table class="table table-sm align-middle mb-0"><thead><tr><th>Notice</th><th>Recipient</th><th>Status</th><th>Created / latest attempt</th><th></th></tr></thead><tbody>`)
	count := 0
	for rows.Next() {
		var outboxID int64
		var kind, recipient, subject, status, providerID, errorMessage, revocationReason string
		var createdAt, availableAt time.Time
		var processedAt, revokedAt, attemptedAt *time.Time
		if rows.Scan(&outboxID, &kind, &recipient, &subject, &createdAt, &availableAt, &processedAt, &revokedAt, &revocationReason, &status, &attemptedAt, &providerID, &errorMessage) != nil {
			continue
		}
		count++
		statusClass := map[string]string{"sent": "text-bg-success", "bounced": "text-bg-danger", "complained": "text-bg-danger", "failed": "text-bg-warning", "queued": "text-bg-secondary", "revoked": "text-bg-dark"}[status]
		if statusClass == "" {
			statusClass = "text-bg-light"
		}
		when := createdAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")
		if attemptedAt != nil {
			when += `<div class="small text-muted">Attempt ` + escapeHTML(attemptedAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")) + `</div>`
		} else if availableAt.After(time.Now()) {
			when += `<div class="small text-muted">Available ` + escapeHTML(availableAt.In(s.LondonLoc).Format("02 Jan 2006 15:04")) + `</div>`
		}
		detail := ""
		if errorMessage != "" {
			detail += `<div class="small text-danger">` + escapeHTML(errorMessage) + `</div>`
		}
		if providerID != "" {
			detail += `<div class="small text-muted">Message ` + escapeHTML(providerID) + `</div>`
		}
		if revocationReason != "" {
			detail += `<div class="small text-muted">Revoked: ` + escapeHTML(revocationReason) + `</div>`
		}
		if status == "failed" && processedAt == nil && revokedAt == nil {
			detail += `<div class="small text-muted">Pending automatic retry</div>`
		}
		action := ""
		if revokedAt == nil && (status == "bounced" || status == "complained" || (status == "failed" && processedAt != nil)) {
			action = fmt.Sprintf(`<form method="POST" action="/admin/cases/%d/notices/%d/resend"><input type="hidden" name="csrf_token" value="%s"><input class="form-control form-control-sm mb-1" name="reason" required placeholder="Reason after resolving delivery issue"><button class="btn btn-sm btn-outline-danger">Queue exact resend</button></form>`, caseID, outboxID, escapeHTML(csrf))
		}
		fmt.Fprintf(w, `<tr><td><strong>%s</strong><div class="small text-muted">%s</div></td><td>%s</td><td><span class="badge %s">%s</span>%s</td><td>%s</td><td>%s</td></tr>`, escapeHTML(strings.ReplaceAll(kind, "_", " ")), escapeHTML(subject), escapeHTML(recipient), statusClass, escapeHTML(status), detail, when, action)
	}
	if count == 0 {
		fmt.Fprint(w, `<tr><td colspan="5" class="text-center text-muted py-3">No correspondence has been queued.</td></tr>`)
	}
	fmt.Fprint(w, `</tbody></table></div></section>`)
}

func (s *Server) handleAdminCaseNoticeResend() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, caseErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		outboxID, outboxErr := strconv.ParseInt(chi.URLParam(r, "outboxID"), 10, 64)
		if r.ParseForm() != nil || caseErr != nil || outboxErr != nil || caseID < 1 || outboxID < 1 {
			http.Error(w, "invalid resend request", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		actor := adminActor(r)
		if reason == "" || actor.ID == nil {
			http.Error(w, "an audit reason is required", http.StatusBadRequest)
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "resend could not be queued", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var problemAttemptID int64
		var problemStatus, messageKind string
		if err = tx.QueryRow(r.Context(), `SELECT attempt.id,attempt.status,outbox.message_kind FROM sanction_notification_attempts attempt
			JOIN sanction_notification_outbox outbox ON outbox.id=attempt.outbox_id
			WHERE outbox.id=$1 AND outbox.case_id=$2 AND attempt.status IN ('bounced','complained','failed')
			  AND outbox.revoked_at IS NULL
			  AND (attempt.status<>'failed' OR outbox.processed_at IS NOT NULL)
			ORDER BY attempt.attempt_number DESC,attempt.id DESC LIMIT 1 FOR UPDATE OF attempt,outbox`, outboxID, caseID).Scan(&problemAttemptID, &problemStatus, &messageKind); err != nil {
			http.Error(w, "only a terminal failed, bounced or complained notice can be manually resent", http.StatusConflict)
			return
		}
		if messageKind == "response_request" || messageKind == "response_reminder" {
			var responseWindowLive bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(
				SELECT 1 FROM sanction_response_requests request
				JOIN sanction_case_access_tokens token ON token.id=request.access_token_id AND token.case_id=request.case_id
				WHERE request.case_id=$1 AND request.status='pending' AND request.due_at>now()
				  AND token.revoked_at IS NULL AND token.expires_at>now()
			)`, caseID).Scan(&responseWindowLive); err != nil || !responseWindowLive {
				http.Error(w, "a response notice cannot be resent after the club replied or its secure window expired; create a new reviewed request instead", http.StatusConflict)
				return
			}
		}
		idempotencySuffix := fmt.Sprintf(":resolved-resend:%d", problemAttemptID)
		var resendID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_notification_outbox(case_id,decision_revision_id,policy_version_id,correspondence_revision_id,message_kind,idempotency_key,recipient,subject,body,attachment_manifest,available_at)
			SELECT case_id,decision_revision_id,policy_version_id,correspondence_revision_id,message_kind,idempotency_key||$3,recipient,subject,body,attachment_manifest,now()
			FROM sanction_notification_outbox WHERE id=$1 AND case_id=$2 AND revoked_at IS NULL
			ON CONFLICT(idempotency_key) DO NOTHING RETURNING id`, outboxID, caseID, idempotencySuffix).Scan(&resendID)
		newlyQueued := err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(r.Context(), `SELECT resend.id FROM sanction_notification_outbox original
				JOIN sanction_notification_outbox resend ON resend.idempotency_key=original.idempotency_key||$3
				WHERE original.id=$1 AND original.case_id=$2 AND original.revoked_at IS NULL AND resend.revoked_at IS NULL`, outboxID, caseID, idempotencySuffix).Scan(&resendID)
		}
		if err != nil {
			http.Error(w, "resend could not be queued", http.StatusInternalServerError)
			return
		}
		if newlyQueued {
			if _, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data)
				VALUES($1,'notice_resend_queued','admin',$2,$3,$4,$5,$6)`, caseID, *actor.ID, actor.Label, reason, actor.RequestID, mapJSONHTTP(map[string]any{"original_outbox_id": outboxID, "resend_outbox_id": resendID, "delivery_status": problemStatus})); err != nil {
				http.Error(w, "resend audit could not be recorded", http.StatusInternalServerError)
				return
			}
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "resend could not be queued", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", caseID), http.StatusSeeOther)
	}
}

func parseAdminDecisionEffects(form url.Values) []sanctiondomain.DecisionEffectRequest {
	at := func(name string, index int) string {
		values := form[name]
		if index < 0 || index >= len(values) {
			return ""
		}
		return strings.TrimSpace(values[index])
	}
	var effects []sanctiondomain.DecisionEffectRequest
	for index, effectType := range form["effect_type"] {
		effectType = strings.TrimSpace(effectType)
		if effectType == "" {
			continue
		}
		effect := sanctiondomain.DecisionEffectRequest{
			EffectType:  effectType,
			Trigger:     at("trigger_condition", index),
			Rescindable: effectType == "yellow_card" && at("rescindable", index) == "yes",
		}
		if value, parseErr := strconv.ParseInt(at("case_subject_id", index), 10, 64); parseErr == nil && value > 0 {
			effect.CaseSubjectID = &value
		}
		if effectType == "fine" {
			if value, parseErr := strconv.ParseFloat(at("fine_pounds", index), 64); parseErr == nil && value > 0 {
				pence := int64(value*100 + 0.5)
				effect.AmountPence = &pence
			}
		}
		if effectType == "points_adjustment" {
			if value, parseErr := strconv.Atoi(at("points", index)); parseErr == nil {
				effect.Points = &value
			}
		}
		if effectType == "player_ban" || effectType == "team_ban" || effectType == "suspended_red" {
			if value, parseErr := time.Parse("2006-01-02", at("ends_at", index)); parseErr == nil {
				effect.EndsAt = &value
			}
		}
		effects = append(effects, effect)
	}
	return effects
}
func (s *Server) handleAdminCasePropose() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		_ = r.ParseForm()
		redirectError := func(message string, blockingCaseID int64) {
			query := url.Values{"error": {message}}
			if blockingCaseID > 0 {
				query.Set("blocking_case", strconv.FormatInt(blockingCaseID, 10))
			}
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?%s", id, query.Encode()), http.StatusSeeOther)
		}
		if _, reviewRequired := s.loadScorecardPointsReview(r.Context(), id); reviewRequired && r.FormValue("league_points_reviewed") != "yes" {
			redirectError("Check the recorded Play-Cricket match points and confirm whether a separate league-table points adjustment is required before saving the decision.", 0)
			return
		}
		appealInstructions := r.FormValue("appeal_instructions")
		if allegedRule, ruleErr := loadCaseAllegedRule(r.Context(), s.DB, id); ruleErr == nil {
			guidance := hawkAppealGuidanceForRule(allegedRule)
			if err := validateHawkAppealInstructions(guidance, appealInstructions); err != nil {
				redirectError(err.Error(), 0)
				return
			}
		}
		effects := parseAdminDecisionEffects(r.Form)
		_, err := sanctiondomain.NewService(s.DB).ProposeDecisionBundle(r.Context(), sanctiondomain.DecisionBundleRequest{
			CaseID: id, PublicReason: r.FormValue("public_reason"), PrivateReason: r.FormValue("private_reason"), RuleReference: r.FormValue("rule_reference"),
			OutcomeSubject: r.FormValue("outcome_subject"), OutcomeFindings: r.FormValue("outcome_findings"), AppealInstructions: appealInstructions,
			Effects: effects, Actor: adminActor(r),
		})
		if err != nil {
			var conflict *sanctiondomain.UnresolvedCardProposalError
			if errors.As(err, &conflict) {
				redirectError(conflict.Error(), conflict.CaseID)
				return
			}
			redirectError(err.Error(), 0)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", id, urlQueryEscape("Decision saved. Review the complete offending-club, reporting-club and official versions below, then send them to Denver.")), http.StatusSeeOther)
	}
}

func (s *Server) handleAdminCaseSendForApproval() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id < 1 {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		service := sanctiondomain.NewService(s.DB)
		actor := adminActor(r)
		for _, audience := range []string{"offending_club", "reporting_club", "official"} {
			draft, draftErr := service.OutcomeDraft(r.Context(), id, audience)
			if draftErr != nil {
				http.Error(w, draftErr.Error(), http.StatusBadRequest)
				return
			}
			if _, draftErr = service.SaveOutcomeDraft(r.Context(), id, audience, draft.Subject, draft.Body, actor); draftErr != nil {
				http.Error(w, draftErr.Error(), http.StatusBadRequest)
				return
			}
		}
		if err = service.SubmitDecisionForApproval(r.Context(), id, actor); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d?success=%s", id, urlQueryEscape("Owner review recorded. The complete decision and all three email versions are now in Denver's approval queue.")), http.StatusSeeOther)
	}
}

func splitAdditionalOutcomeRecipients(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
}

func (s *Server) handleAdminCaseApprove() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		_ = r.ParseForm()
		emergency := strings.TrimSpace(r.FormValue("emergency_reason"))
		if emergency != "" {
			sess, _ := getAdminSessionFromRequest(r)
			if sess == nil || s.effectiveAdminRole(r.Context(), sess.AdminID) != "super_admin" {
				http.Error(w, "emergency override requires super-admin", 403)
				return
			}
		}
		err := sanctiondomain.NewService(s.DB).ApproveCaseWithOptions(r.Context(), id, adminActor(r), sanctiondomain.ApprovalOptions{
			EmergencyReason:      emergency,
			AdditionalRecipients: splitAdditionalOutcomeRecipients(r.FormValue("additional_recipients")),
		})
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}

func (s *Server) handleAdminCaseReject() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		_ = r.ParseForm()
		if err := sanctiondomain.NewService(s.DB).RejectProposedCase(r.Context(), id, adminActor(r), r.FormValue("reason")); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}

func (s *Server) handleAdminCaseOutcomePreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id < 1 {
			http.Error(w, "invalid case", http.StatusBadRequest)
			return
		}
		pdf, filename, err := sanctiondomain.NewService(s.DB).PreviewOutcomeLetter(r.Context(), id, r.URL.Query().Get("audience"))
		if err != nil {
			http.Error(w, "outcome preview unavailable: "+err.Error(), http.StatusBadRequest)
			return
		}
		actor := adminActor(r)
		_, _ = s.DB.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,metadata,request_id)
			VALUES($1,'outcome_pdf_previewed','admin',$2,$3,'Audience-safe outcome PDF preview viewed',jsonb_build_object('audience',$4),$5)`, id, actorIDAny(actor), actor.Label, r.URL.Query().Get("audience"), actor.RequestID)
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filepath.Base(filename)))
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(pdf)
	}
}

func (s *Server) handleAdminCasePublish() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err := sanctiondomain.NewService(s.DB).PublishCase(r.Context(), id, adminActor(r)); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}

func (s *Server) handleAdminCaseOverturn() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		_ = r.ParseForm()
		if err := sanctiondomain.NewService(s.DB).OverturnCase(r.Context(), id, adminActor(r), r.FormValue("reason")); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}

func (s *Server) handleAdminCaseCorrect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		_ = r.ParseForm()
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" {
			http.Error(w, "correction reason required", 400)
			return
		}
		actor := adminActor(r)
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "correction failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var beforePublic, beforePrivate, status string
		if tx.QueryRow(r.Context(), `SELECT COALESCE(public_summary,''),COALESCE(private_summary,''),status FROM sanction_cases WHERE id=$1 FOR UPDATE`, id).Scan(&beforePublic, &beforePrivate, &status) != nil {
			http.NotFound(w, r)
			return
		}
		if !map[string]bool{"submitted": true, "triage": true, "investigating": true, "response_pending": true}[status] {
			http.Error(w, "case summaries are locked once a decision is proposed; record a new investigation note or use the decision revision workflow", http.StatusConflict)
			return
		}
		afterPublic := strings.TrimSpace(r.FormValue("public_summary"))
		afterPrivate := strings.TrimSpace(r.FormValue("private_summary"))
		if status == "response_pending" && afterPublic != strings.TrimSpace(beforePublic) {
			http.Error(w, "the allegation shown in the active response portal is locked until the response window closes; record private notes meanwhile", http.StatusConflict)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'case_corrected','admin',$2,$3,$4,jsonb_build_object('public_summary',$5,'private_summary',$6),jsonb_build_object('public_summary',$7,'private_summary',$8),$9)`, id, actorIDAny(actor), actor.Label, reason, beforePublic, beforePrivate, afterPublic, afterPrivate, actor.RequestID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET public_summary=$2,private_summary=$3,current_revision=current_revision+1,updated_at=now() WHERE id=$1`, id, afterPublic, afterPrivate)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "correction failed", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}
func actorIDAny(a sanctiondomain.Actor) any {
	if a.ID == nil {
		return nil
	}
	return *a.ID
}

func (s *Server) handleAdminCaseAssignSelf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		actor := adminActor(r)
		if actor.ID == nil {
			http.Error(w, "unauthorised", 401)
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "assignment failed", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var previous *int32
		if tx.QueryRow(r.Context(), `SELECT assigned_admin_id FROM sanction_cases WHERE id=$1 FOR UPDATE`, id).Scan(&previous) != nil {
			http.NotFound(w, r)
			return
		}
		// Assignment is an attributed state change, not a repeatable activity.
		// A refresh or duplicate form submission by the current investigator
		// must not add another immutable event for the same state.
		if sameAdminAssignment(previous, actor.ID) {
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), http.StatusSeeOther)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'investigator_assigned','admin',$2,$3,'Investigation assigned',jsonb_build_object('assigned_admin_id',$4::integer),jsonb_build_object('assigned_admin_id',$2::bigint),$5)`, id, *actor.ID, actor.Label, previous, actor.RequestID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET assigned_admin_id=$2,status=CASE WHEN status IN ('submitted','triage') THEN 'investigating' ELSE status END,updated_at=now() WHERE id=$1`, id, *actor.ID)
		}
		if err == nil {
			_, err = reassignOpenCaseOwnerTasks(r.Context(), tx, id, previous, *actor.ID, *actor.ID, actor.Label, actor.Label, "Investigation assigned", actor.RequestID)
		}
		if err != nil {
			slog.Error("assign sanction case", "case_id", id, "admin_id", *actor.ID, "error", err)
			http.Error(w, "assignment failed", 500)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			slog.Error("commit sanction case assignment", "case_id", id, "admin_id", *actor.ID, "error", err)
			http.Error(w, "assignment failed", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}

type adminEvidenceDisclosureState struct {
	SourceEvidenceID *int64
	Reviewer         string
	ReviewedAt       *time.Time
	Eligible         bool
	Available        bool
	SharingAction    string
}

func adminEvidenceDisclosureControlsHTML(caseID, evidenceID int64, csrf string, state adminEvidenceDisclosureState) string {
	var output strings.Builder
	endpoint := fmt.Sprintf("/admin/cases/%d/evidence/%d/share-with-offending-club", caseID, evidenceID)
	if state.SourceEvidenceID != nil {
		reviewed := ""
		if state.ReviewedAt != nil {
			reviewed = " on " + state.ReviewedAt.UTC().Format("02 Jan 2006 15:04 UTC")
		}
		fmt.Fprintf(&output, `<div class="small text-success fw-semibold mt-1">Reviewed redacted derivative of evidence #%d; attested by %s%s.</div>`, *state.SourceEvidenceID, escapeHTML(state.Reviewer), escapeHTML(reviewed))
	} else {
		fmt.Fprint(&output, `<div class="small text-danger fw-semibold mt-1">Private source evidence — direct portal sharing is blocked.</div>`)
	}

	if state.SharingAction == "shared" {
		fmt.Fprintf(&output, `<div class="small fw-semibold mt-1">Shared with offending club</div><form method="POST" action="%s" class="mt-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="action" value="revoked"><div class="input-group input-group-sm"><input class="form-control" name="reason" required placeholder="Audit reason for revocation"><button class="btn btn-outline-danger">Revoke portal access</button></div></form>`, endpoint, escapeHTML(csrf))
		return output.String()
	}

	if state.SourceEvidenceID != nil {
		if !state.Eligible || !state.Available {
			fmt.Fprint(&output, `<div class="small text-danger mt-1">Portal sharing is unavailable because this derivative or its reviewed source revision is no longer current.</div>`)
			return output.String()
		}
		fmt.Fprintf(&output, `<div class="small fw-semibold mt-1">Not shared</div><form method="POST" action="%s" class="mt-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="action" value="shared"><div class="input-group input-group-sm"><input class="form-control" name="reason" required placeholder="Audit reason for disclosure"><button class="btn btn-outline-warning">Share reviewed derivative</button></div></form>`, endpoint, escapeHTML(csrf))
		return output.String()
	}

	if !state.Available {
		fmt.Fprint(&output, `<div class="small text-muted mt-1">This source record is unavailable and cannot be used to create a derivative.</div>`)
		return output.String()
	}
	fmt.Fprintf(&output, `<form method="POST" action="%s" enctype="multipart/form-data" class="mt-2 border rounded p-2"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="action" value="create_redacted_derivative"><label class="form-label small fw-semibold">Upload a separately redacted copy (PDF, JPEG, PNG, WebP, MP4, or text)</label><input class="form-control form-control-sm mb-2" type="file" name="redacted_evidence" accept="application/pdf,image/jpeg,image/png,image/webp,video/mp4,text/plain" required><label class="form-check small mb-2"><input class="form-check-input" type="checkbox" name="reviewer_attestation" value="confirmed" required> <span class="form-check-label">I reviewed this copy and confirm that reporter name, role, email, phone and reporting-club identity have been removed.</span></label><textarea class="form-control form-control-sm mb-2" name="reason" rows="2" maxlength="2000" required placeholder="Describe the redactions and review performed"></textarea><button class="btn btn-sm btn-outline-primary">Create reviewed redacted derivative</button></form>`, endpoint, escapeHTML(csrf))
	return output.String()
}

func (s *Server) handleAdminCaseEvidenceDownload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		evidenceID, _ := strconv.ParseInt(chi.URLParam(r, "evidenceID"), 10, 64)
		var name, media, key string
		if s.DB.QueryRow(r.Context(), `SELECT original_name,media_type,storage_key FROM sanction_case_evidence WHERE id=$1 AND case_id=$2`, evidenceID, caseID).Scan(&name, &media, &key) != nil {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(evidenceDir(), filepath.Base(key))
		w.Header().Set("Content-Type", media)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, strings.ReplaceAll(filepath.Base(name), `"`, "")))
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, path)
	}
}

func (s *Server) handleAdminCaseEvidenceShare() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, caseErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		evidenceID, evidenceErr := strconv.ParseInt(chi.URLParam(r, "evidenceID"), 10, 64)
		r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(256<<10))
		var parseErr error
		if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
			parseErr = r.ParseMultipartForm(10 << 20)
		} else {
			parseErr = r.ParseForm()
		}
		if parseErr != nil || caseErr != nil || evidenceErr != nil || caseID < 1 || evidenceID < 1 {
			http.Error(w, "invalid evidence sharing request", http.StatusBadRequest)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		action := strings.TrimSpace(r.FormValue("action"))
		reason := strings.TrimSpace(r.FormValue("reason"))
		actor := adminActor(r)
		if reason == "" || len(reason) > 2000 || actor.ID == nil {
			http.Error(w, "action and audit reason are required", http.StatusBadRequest)
			return
		}

		if action == "create_redacted_derivative" {
			if r.FormValue("reviewer_attestation") != "confirmed" {
				http.Error(w, "reviewer attestation is required", http.StatusBadRequest)
				return
			}
			file, header, fileErr := r.FormFile("redacted_evidence")
			if fileErr != nil {
				http.Error(w, "a separately redacted evidence file is required", http.StatusBadRequest)
				return
			}
			defer file.Close()

			tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
			if err != nil {
				http.Error(w, "redacted derivative could not be recorded", http.StatusInternalServerError)
				return
			}
			defer tx.Rollback(r.Context())
			var sourceSHA string
			err = tx.QueryRow(r.Context(), `SELECT lower(evidence.sha256)
				FROM sanction_case_evidence evidence
				WHERE evidence.id=$1 AND evidence.case_id=$2 AND evidence.redacted_at IS NULL
				  AND NOT EXISTS(SELECT 1 FROM sanction_case_evidence_derivatives prior WHERE prior.derivative_evidence_id=evidence.id)
				FOR UPDATE OF evidence`, evidenceID, caseID).Scan(&sourceSHA)
			if err != nil {
				http.Error(w, "only an available private source record can have a redacted derivative", http.StatusConflict)
				return
			}

			key, derivativeSHA, size, media, copyErr := copyEvidence(file, header)
			if copyErr != nil {
				http.Error(w, copyErr.Error(), http.StatusBadRequest)
				return
			}
			keepFile := false
			defer func() {
				if !keepFile {
					_ = os.Remove(filepath.Join(evidenceDir(), filepath.Base(key)))
				}
			}()
			if strings.EqualFold(sourceSHA, derivativeSHA) {
				http.Error(w, "the reviewed derivative must be a distinct redacted copy of the source", http.StatusBadRequest)
				return
			}

			var derivativeID int64
			err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_evidence(
				case_id,visibility,original_name,media_type,byte_size,storage_key,sha256,uploaded_by_type,uploaded_by_id
			) VALUES($1,'party',$2,$3,$4,$5,$6,'admin',$7) RETURNING id`, caseID,
				filepath.Base(header.Filename), media, size, key, strings.ToLower(derivativeSHA), *actor.ID).Scan(&derivativeID)
			if err == nil {
				_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_evidence_derivatives(
					case_id,source_evidence_id,derivative_evidence_id,source_sha256,derivative_sha256,
					reviewer_admin_id,attestation_code,review_notes
				) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, caseID, evidenceID, derivativeID,
					strings.ToLower(sourceSHA), strings.ToLower(derivativeSHA), *actor.ID,
					evidenceRedactionAttestationCode, reason)
			}
			if err == nil {
				_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(
					case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data
				) VALUES($1,'evidence_redacted_derivative_created','admin',$2,$3,$4,$5,$6)`,
					caseID, *actor.ID, actor.Label, reason, actor.RequestID, mapJSONHTTP(map[string]any{
						"source_evidence_id":     evidenceID,
						"derivative_evidence_id": derivativeID,
						"source_sha256":          strings.ToLower(sourceSHA),
						"derivative_sha256":      strings.ToLower(derivativeSHA),
						"attestation_code":       evidenceRedactionAttestationCode,
					}))
			}
			if err != nil || tx.Commit(r.Context()) != nil {
				http.Error(w, "redacted derivative could not be recorded", http.StatusInternalServerError)
				return
			}
			keepFile = true
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", caseID), http.StatusSeeOther)
			return
		}

		if action != "shared" && action != "revoked" {
			http.Error(w, "invalid evidence sharing action", http.StatusBadRequest)
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			http.Error(w, "evidence sharing could not be recorded", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())
		var currentAction string
		if action == "shared" {
			err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT sharing.action FROM sanction_evidence_sharing_events sharing
				WHERE sharing.case_id=evidence.case_id AND sharing.evidence_id=evidence.id AND sharing.audience='offending_club' ORDER BY sharing.id DESC LIMIT 1),'')
				FROM sanction_offending_club_evidence_derivatives allowed
				JOIN sanction_case_evidence evidence ON evidence.id=allowed.evidence_id AND evidence.case_id=allowed.case_id
				WHERE allowed.evidence_id=$1 AND allowed.case_id=$2 FOR UPDATE OF evidence`, evidenceID, caseID).Scan(&currentAction)
		} else {
			err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT sharing.action FROM sanction_evidence_sharing_events sharing
				WHERE sharing.case_id=evidence.case_id AND sharing.evidence_id=evidence.id AND sharing.audience='offending_club' ORDER BY sharing.id DESC LIMIT 1),'')
				FROM sanction_case_evidence evidence WHERE evidence.id=$1 AND evidence.case_id=$2 FOR UPDATE OF evidence`, evidenceID, caseID).Scan(&currentAction)
		}
		if err != nil {
			http.Error(w, "only a current reviewed redacted derivative can be shared", http.StatusConflict)
			return
		}
		if currentAction == action || (action == "revoked" && currentAction != "shared") {
			http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", caseID), http.StatusSeeOther)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO sanction_evidence_sharing_events(case_id,evidence_id,audience,action,reason,actor_admin_id,request_id)
			VALUES($1,$2,'offending_club',$3,$4,$5,$6)`, caseID, evidenceID, action, reason, *actor.ID, actor.RequestID); err != nil {
			http.Error(w, "evidence sharing could not be recorded", http.StatusInternalServerError)
			return
		}
		eventType := "evidence_shared_with_offending_club"
		if action == "revoked" {
			eventType = "evidence_share_revoked"
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data)
			VALUES($1,$2,'admin',$3,$4,$5,$6,$7)`, caseID, eventType, *actor.ID, actor.Label, reason, actor.RequestID, mapJSONHTTP(map[string]any{"evidence_id": evidenceID, "audience": "offending_club", "action": action})); err != nil {
			http.Error(w, "evidence sharing audit could not be recorded", http.StatusInternalServerError)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "evidence sharing could not be recorded", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", caseID), http.StatusSeeOther)
	}
}

func (s *Server) handleSanctionCasePartyEvidenceDownload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		evidenceID, parseErr := strconv.ParseInt(chi.URLParam(r, "evidenceID"), 10, 64)
		rawToken := strings.TrimSpace(r.URL.Query().Get("token"))
		if parseErr != nil || evidenceID < 1 || rawToken == "" {
			http.NotFound(w, r)
			return
		}
		var media, key, expectedSHA string
		err := s.DB.QueryRow(r.Context(), portalSharedEvidenceDownloadQuery, evidenceID, tokenHash(rawToken)).Scan(&media, &key, &expectedSHA)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data, err := readVerifiedCaseEvidence(key, expectedSHA)
		if err != nil {
			slog.Error("serve reviewed evidence derivative", "evidence_id", evidenceID, "error", err)
			http.NotFound(w, r)
			return
		}
		extension := map[string]string{"application/pdf": ".pdf", "image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "video/mp4": ".mp4", "text/plain": ".txt"}[strings.ToLower(media)]
		w.Header().Set("Content-Type", media)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="GMCL-case-evidence-%d%s"`, evidenceID, extension))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, fmt.Sprintf("GMCL-case-evidence-%d%s", evidenceID, extension), time.Time{}, bytes.NewReader(data))
	}
}

func readVerifiedCaseEvidence(storageKey, expectedSHA string) ([]byte, error) {
	storageKey = filepath.Base(strings.TrimSpace(storageKey))
	expectedSHA = strings.ToLower(strings.TrimSpace(expectedSHA))
	if storageKey == "." || storageKey == "" || len(expectedSHA) != 64 {
		return nil, errors.New("invalid evidence derivative provenance")
	}
	data, err := os.ReadFile(filepath.Join(evidenceDir(), storageKey))
	if err != nil {
		return nil, err
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != expectedSHA {
		return nil, errors.New("evidence derivative checksum mismatch")
	}
	return data, nil
}

func evidenceDir() string {
	if v := strings.TrimSpace(os.Getenv("SANCTIONS_EVIDENCE_DIR")); v != "" {
		return v
	}
	return filepath.Join("data", "sanction-evidence")
}
func detectedEvidenceContentType(prefix []byte) string {
	media := strings.ToLower(strings.TrimSpace(strings.SplitN(http.DetectContentType(prefix), ";", 2)[0]))
	if media == "application/octet-stream" && hasMP4FileTypeBox(prefix) {
		return "video/mp4"
	}
	return media
}

func hasMP4FileTypeBox(prefix []byte) bool {
	if len(prefix) < 12 || string(prefix[4:8]) != "ftyp" {
		return false
	}
	boxSize := uint32(prefix[0])<<24 | uint32(prefix[1])<<16 | uint32(prefix[2])<<8 | uint32(prefix[3])
	if boxSize < 12 {
		return false
	}
	switch string(prefix[8:12]) {
	case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "iso7", "iso8", "iso9",
		"avc1", "mp41", "mp42", "M4V ", "MSNV", "dash", "3gp4", "3gp5", "3gp6":
		return true
	default:
		return false
	}
}

func copyEvidence(file multipart.File, header *multipart.FileHeader) (string, string, int64, string, error) {
	if header.Size > 10<<20 {
		return "", "", 0, "", fmt.Errorf("evidence exceeds 10 MB")
	}
	prefix := make([]byte, 512)
	n, readErr := io.ReadFull(file, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", "", 0, "", readErr
	}
	if n == 0 {
		return "", "", 0, "", fmt.Errorf("evidence file is empty")
	}
	prefix = prefix[:n]
	media := detectedEvidenceContentType(prefix)
	switch media {
	case "application/pdf", "image/jpeg", "image/png", "image/webp", "video/mp4":
	case "text/plain":
		plainPrefix := strings.ToLower(strings.TrimSpace(string(prefix)))
		if strings.HasPrefix(plainPrefix, "<svg") || strings.HasPrefix(plainPrefix, "<?xml") ||
			strings.Contains(plainPrefix, "<script") {
			return "", "", 0, "", fmt.Errorf("unsupported evidence type")
		}
	default:
		return "", "", 0, "", fmt.Errorf("unsupported evidence type")
	}
	if err := os.MkdirAll(evidenceDir(), 0700); err != nil {
		return "", "", 0, "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", "", 0, "", err
	}
	key := hex.EncodeToString(random)
	path := filepath.Join(evidenceDir(), key)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", "", 0, "", err
	}
	keep := false
	defer func() {
		_ = out.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	n64, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(io.MultiReader(bytes.NewReader(prefix), file), (10<<20)+1))
	if err != nil {
		return "", "", 0, "", err
	}
	if n64 > 10<<20 {
		return "", "", 0, "", fmt.Errorf("evidence exceeds 10 MB")
	}
	if err = out.Close(); err != nil {
		return "", "", 0, "", err
	}
	keep = true
	return key, hex.EncodeToString(hash.Sum(nil)), n64, media, nil
}

func mapJSONHTTP(v any) []byte { b, _ := json.Marshal(v); return b }
