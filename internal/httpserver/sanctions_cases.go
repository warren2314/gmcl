package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/email"
	"cricket-ground-feedback/internal/middleware"
	sanctiondomain "cricket-ground-feedback/internal/sanctions"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func requestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-ID")); id != "" {
		return id
	}
	return strings.TrimSpace(r.Header.Get("X-Request-Id"))
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

func (s *Server) handlePublicSanctionsRegister() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		season := strings.TrimSpace(r.URL.Query().Get("season"))
		archive := r.URL.Query().Get("archive") == "1"
		args := []any{}
		where := `c.status='published' AND c.public_status IN ('active','suspended','served','expired')`
		if !archive {
			where += ` AND c.public_status IN ('active','suspended')`
		}
		if season != "" {
			if y, err := strconv.Atoi(season); err == nil {
				args = append(args, y)
				where += fmt.Sprintf(` AND EXTRACT(YEAR FROM s.start_date)::int=$%d`, len(args))
			}
		}
		rows, err := s.DB.Query(ctx, fmt.Sprintf(`
			SELECT c.reference,COALESCE(EXTRACT(YEAR FROM s.start_date)::int,EXTRACT(YEAR FROM e.starts_at)::int,0),COALESCE(cl.name,''),COALESCE(t.name,''),COALESCE(c.player_name,''),
			       c.public_summary,c.public_status,e.effect_type,e.status,COALESCE(e.points,0),COALESCE(e.amount_pence,0),e.starts_at,e.ends_at,c.published_at
			FROM sanction_cases c
			LEFT JOIN seasons s ON s.id=c.season_id LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id
			JOIN LATERAL (SELECT er.* FROM sanction_effect_revisions er JOIN sanction_decision_revisions dr ON dr.id=er.decision_revision_id WHERE dr.case_id=c.id AND dr.status='approved' AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=er.id) ORDER BY er.id DESC LIMIT 1) e ON true
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
		fmt.Fprint(w, `<main class="container py-4" style="max-width:1100px"><div class="d-flex flex-wrap justify-content-between align-items-start gap-3 mb-4"><div><h1 class="h2">GMCL sanctions register</h1><p class="text-muted">Approved and published team cards, bans, fines and points decisions. Private evidence and correspondence are never shown here.</p></div><a class="btn btn-outline-danger" href="/sanctions/report">Report an issue</a></div><form method="GET" class="row g-2 mb-3"><div class="col-auto"><input class="form-control" type="number" name="season" min="2016" placeholder="Season" value="`+escapeHTML(season)+`"></div><div class="col-auto"><label class="form-check mt-2"><input class="form-check-input" type="checkbox" name="archive" value="1"`)
		if archive {
			fmt.Fprint(w, " checked")
		}
		fmt.Fprint(w, `> <span class="form-check-label">Include archive</span></label></div><div class="col-auto"><button class="btn btn-primary">Filter</button></div></form><div class="table-responsive"><table class="table table-striped align-middle"><thead><tr><th>Reference</th><th>Season</th><th>Club / team / player</th><th>Sanction</th><th>Status</th><th>Effective</th></tr></thead><tbody>`)
		count := 0
		for rows.Next() {
			var ref, club, team, player, reason, pubStatus, effect, effectStatus string
			var year, points int
			var amountPence int64
			var starts, ends, published *time.Time
			if rows.Scan(&ref, &year, &club, &team, &player, &reason, &pubStatus, &effect, &effectStatus, &points, &amountPence, &starts, &ends, &published) != nil {
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
			dates := "—"
			if starts != nil {
				dates = starts.In(s.LondonLoc).Format("02 Jan 2006")
			}
			if ends != nil {
				dates += " to " + ends.In(s.LondonLoc).Format("02 Jan 2006")
			}
			fmt.Fprintf(w, `<tr><td><a href="/sanctions/%s">%s</a></td><td>%d</td><td>%s</td><td><strong>%s</strong><div class="small text-muted">%s</div></td><td>%s</td><td>%s</td></tr>`, escapeHTML(ref), escapeHTML(ref), year, escapeHTML(subject), escapeHTML(sanction), escapeHTML(reason), escapeHTML(pubStatus), escapeHTML(dates))
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

func (s *Server) handlePublicSanctionDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ref := chi.URLParam(r, "reference")
		var club, team, player, summary, status, effect, ruleRef string
		var starts, ends *time.Time
		var points *int
		var amountPence *int64
		err := s.DB.QueryRow(r.Context(), `SELECT COALESCE(cl.name,''),COALESCE(t.name,''),COALESCE(c.player_name,''),c.public_summary,c.public_status,e.effect_type,COALESCE(d.rule_reference,''),e.starts_at,e.ends_at,e.points,e.amount_pence FROM sanction_cases c LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='approved' JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id WHERE c.reference=$1 AND c.status='published' AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id) ORDER BY d.revision DESC LIMIT 1`, ref).Scan(&club, &team, &player, &summary, &status, &effect, &ruleRef, &starts, &ends, &points, &amountPence)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanction "+ref)
		writeCaptainNav(w)
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:800px"><a href="/sanctions" class="btn btn-sm btn-outline-secondary mb-3">Back to register</a><article class="card"><div class="card-header d-flex justify-content-between"><strong>%s</strong><span class="badge text-bg-danger">%s</span></div><div class="card-body"><h1 class="h3">%s</h1><p>%s</p><dl class="row"><dt class="col-sm-4">Status</dt><dd class="col-sm-8">%s</dd><dt class="col-sm-4">Applicable rule</dt><dd class="col-sm-8">%s</dd>`, escapeHTML(ref), escapeHTML(effectLabel(effect)), escapeHTML(strings.Join(nonEmpty(club, team, player), " — ")), escapeHTML(summary), escapeHTML(status), escapeHTML(ruleRef))
		if points != nil {
			fmt.Fprintf(w, `<dt class="col-sm-4">Points consequence</dt><dd class="col-sm-8">%d point deduction</dd>`, *points)
		}
		if amountPence != nil {
			fmt.Fprintf(w, `<dt class="col-sm-4">Fine</dt><dd class="col-sm-8">£%.2f</dd>`, float64(*amountPence)/100)
		}
		fmt.Fprint(w, `</dl><p class="small text-muted mb-0">This public record excludes evidence, correspondence, contact details, and internal notes.</p></div></article></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleSanctionReportForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, _ := s.DB.Query(r.Context(), `SELECT t.id,cl.name,t.name FROM teams t JOIN clubs cl ON cl.id=t.club_id WHERE t.active ORDER BY cl.name,t.name`)
		if rows != nil {
			defer rows.Close()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Report a sanctions issue")
		writeCaptainNav(w)
		csrf := middleware.CSRFToken(r)
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:760px"><h1 class="h2">Report an issue</h1><p class="text-muted">Your email must be verified before the league reviews this report. Reports and evidence are retained as part of the case record.</p><form method="POST" action="/sanctions/report" enctype="multipart/form-data" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-6"><label class="form-label">Your name</label><input class="form-control" name="reporter_name" required maxlength="150"></div><div class="col-md-6"><label class="form-label">Your email</label><input class="form-control" type="email" name="reporter_email" required maxlength="320"></div><div class="col-md-6"><label class="form-label">Report type</label><select class="form-select" name="source_type" required><option value="discipline">Disciplinary issue</option><option value="ineligible_player">Ineligible player</option><option value="grounds_facilities">Grounds or facilities</option><option value="forfeit">Match forfeit</option><option value="manual">Other</option></select></div><div class="col-md-6"><label class="form-label">Affected team</label><select class="form-select" name="team_id" required><option value="">Choose…</option>`, csrf)
		if rows != nil {
			for rows.Next() {
				var id int
				var club, team string
				if rows.Scan(&id, &club, &team) == nil {
					fmt.Fprintf(w, `<option value="%d">%s — %s</option>`, id, escapeHTML(club), escapeHTML(team))
				}
			}
		}
		fmt.Fprint(w, `</select></div><div class="col-md-6"><label class="form-label">Match date (if relevant)</label><input class="form-control" type="date" name="match_date"></div><div class="col-md-6"><label class="form-label">Affected player (if relevant)</label><input class="form-control" name="player_name" maxlength="200"></div><div class="col-12"><label class="form-label">What happened?</label><textarea class="form-control" name="summary" rows="7" required maxlength="10000"></textarea></div><div class="col-12"><label class="form-label">Evidence (optional PDF, image, or text; max 10 MB)</label><input class="form-control" type="file" name="evidence" accept=".pdf,image/*,.txt"></div><div class="col-12 form-check ms-2"><input class="form-check-input" type="checkbox" name="consent" value="yes" required id="consent"><label class="form-check-label" for="consent">I confirm this report is accurate to the best of my knowledge and may be shared with relevant parties.</label></div></div><div class="card-footer"><button class="btn btn-danger">Submit and verify email</button></div></form></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleSanctionReportSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		if s.DB.QueryRow(r.Context(), `SELECT club_id FROM teams WHERE id=$1 AND active`, teamID).Scan(&clubID) != nil {
			http.Error(w, "team not found", 400)
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
		_ = email.NewFromEnv().Send(emailAddr, "Verify GMCL sanctions report "+ref, "Please verify your report.\n\n"+link+"\n\nCase reference: "+ref)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Check your email")
		writeCaptainNav(w)
		fmt.Fprintf(w, `<main class="container py-5" style="max-width:650px"><div class="alert alert-success"><h1 class="h4">Report received</h1><p>Check your email and use the verification link within 24 hours.</p><p class="mb-0">Reference: <strong>%s</strong></p></div></main>`, escapeHTML(ref))
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
		raw := r.URL.Query().Get("token")
		var ref, summary, party string
		var caseID int64
		err := s.DB.QueryRow(r.Context(), `SELECT c.id,c.reference,c.public_summary,COALESCE(p.name,'Club representative') FROM sanction_case_access_tokens tok JOIN sanction_cases c ON c.id=tok.case_id LEFT JOIN sanction_case_parties p ON p.id=tok.party_id WHERE tok.token_hash=$1 AND tok.purpose='respond' AND tok.revoked_at IS NULL AND tok.expires_at>now()`, tokenHash(raw)).Scan(&caseID, &ref, &summary, &party)
		if err != nil {
			http.Error(w, "case link is invalid or expired", 400)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Respond to "+ref)
		writeCaptainNav(w)
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:760px"><h1 class="h2">Respond to case %s</h1><div class="alert alert-secondary"><strong>%s</strong><br>%s</div><form method="POST" action="/sanctions/case/respond" enctype="multipart/form-data" class="card"><input type="hidden" name="csrf_token" value="%s"><input type="hidden" name="token" value="%s"><div class="card-body"><label class="form-label">Your explanation</label><textarea class="form-control mb-3" name="response" rows="8" required maxlength="20000"></textarea><label class="form-label">Evidence (optional PDF, image, or text; max 10 MB)</label><input class="form-control mb-3" type="file" name="evidence" accept=".pdf,image/*,.txt"><label class="form-check"><input class="form-check-input" type="checkbox" name="appeal" value="yes"> <span class="form-check-label">This response is a formal appeal against a published decision</span></label></div><div class="card-footer"><button class="btn btn-danger">Submit response</button></div></form></main>`, escapeHTML(ref), escapeHTML(party), escapeHTML(summary), csrf, escapeHTML(raw))
		pageFooter(w)
	}
}

func (s *Server) handleSanctionCaseResponseSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		var partyName, caseStatus string
		err = tx.QueryRow(r.Context(), `SELECT tok.id,tok.case_id,tok.party_id,COALESCE(p.name,'Club representative'),c.status FROM sanction_case_access_tokens tok JOIN sanction_cases c ON c.id=tok.case_id LEFT JOIN sanction_case_parties p ON p.id=tok.party_id WHERE tok.token_hash=$1 AND tok.purpose='respond' AND tok.revoked_at IS NULL AND tok.expires_at>now() FOR UPDATE OF tok`, tokenHash(raw)).Scan(&tokenID, &caseID, &partyID, &partyName, &caseStatus)
		if err != nil {
			http.Error(w, "case link is invalid or expired", 400)
			return
		}
		eventType := "party_response"
		nextStatus := "investigating"
		if r.FormValue("appeal") == "yes" {
			if caseStatus != "published" && caseStatus != "closed" {
				http.Error(w, "only a published decision can be appealed", 400)
				return
			}
			eventType = "appeal_submitted"
			nextStatus = "appealed"
		}
		var eventID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id) VALUES($1,$2,'reporter',$3,$4,$5,$6) RETURNING id`, caseID, eventType, partyID, partyName, response, requestID(r)).Scan(&eventID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_case_access_tokens SET last_used_at=now() WHERE id=$1`, tokenID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status=$2,updated_at=now() WHERE id=$1`, caseID, nextStatus)
		}
		if err == nil {
			if file, header, fileErr := r.FormFile("evidence"); fileErr == nil {
				defer file.Close()
				err = storeCaseEvidence(r.Context(), tx, caseID, &eventID, "party", file, header, "reporter", partyID)
			}
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not store response", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Response recorded")
		writeCaptainNav(w)
		fmt.Fprint(w, `<main class="container py-5" style="max-width:650px"><div class="alert alert-success"><h1 class="h4">Response recorded</h1><p class="mb-0">Your explanation and any evidence are now part of the immutable case timeline.</p></div></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminCaseRequestResponse() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "could not create link", 500)
			return
		}
		defer tx.Rollback(r.Context())
		var ref, summary, teamName, captainName, captainEmail string
		var teamID int32
		err = tx.QueryRow(r.Context(), `SELECT c.reference,c.public_summary,c.team_id,t.name,cap.full_name,cap.email FROM sanction_cases c JOIN teams t ON t.id=c.team_id JOIN captains cap ON cap.team_id=c.team_id AND cap.active_from<=CURRENT_DATE AND (cap.active_to IS NULL OR cap.active_to>=CURRENT_DATE) WHERE c.id=$1 ORDER BY cap.active_from DESC LIMIT 1`, id).Scan(&ref, &summary, &teamID, &teamName, &captainName, &captainEmail)
		if err != nil {
			http.Error(w, "active captain not found", 400)
			return
		}
		var partyID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_case_parties(case_id,party_type,name,email,team_id) VALUES($1,'captain',$2,$3,$4) RETURNING id`, id, captainName, captainEmail, teamID).Scan(&partyID)
		if err != nil {
			http.Error(w, "could not create party", 500)
			return
		}
		raw, hash, err := newPublicToken()
		if err != nil {
			http.Error(w, "could not create link", 500)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_access_tokens(case_id,party_id,token_hash,purpose,expires_at) VALUES($1,$2,$3,'respond',now()+interval '14 days')`, id, partyID, hash)
		if err == nil {
			actor := adminActor(r)
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id) VALUES($1,'response_requested','admin',$2,$3,$4,$5)`, id, actorIDAny(actor), actor.Label, "Secure response requested from "+captainName, actor.RequestID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET status='response_pending',updated_at=now() WHERE id=$1`, id)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "could not create link", 500)
			return
		}
		link := sanctionsBaseURL() + "/sanctions/case/respond?token=" + raw
		body := "The GMCL requests a response from " + teamName + " concerning " + summary + "\n\n" + link + "\n\nCase reference: " + ref + "\nThis secure link expires in 14 days."
		if err = email.NewFromEnv().Send(captainEmail, "Response requested for GMCL case "+ref, body); err != nil {
			http.Error(w, "link created but email failed: "+err.Error(), 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
}

func (s *Server) handleAdminCases() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.DB.Query(r.Context(), `SELECT c.id,c.reference,c.source_type,c.status,COALESCE(cl.name,''),COALESCE(t.name,''),c.created_at,COALESCE(a.username,'') FROM sanction_cases c LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id LEFT JOIN admin_users a ON a.id=c.assigned_admin_id ORDER BY CASE c.status WHEN 'submitted' THEN 0 WHEN 'triage' THEN 1 WHEN 'decision_proposed' THEN 2 ELSE 3 END,c.created_at DESC LIMIT 300`)
		if err != nil {
			http.Error(w, "could not load cases", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions cases")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprint(w, `<main class="container py-4"><div class="d-flex flex-wrap justify-content-between gap-3"><div><h1 class="h2">Sanctions cases</h1><p class="text-muted">Investigation, two-stage approval, publication and immutable history.</p></div><div class="d-flex flex-wrap gap-2 align-self-start"><a href="/admin/cases/new" class="btn btn-danger">Add card, ban, fine or points decision</a><div class="btn-group"><a href="/admin/cases/automation" class="btn btn-outline-secondary">Automation</a><a href="/admin/cases/recipients" class="btn btn-outline-secondary">Recipients</a><a href="/admin/cases/imports" class="btn btn-outline-secondary">History imports</a><a href="/admin/sanctions" class="btn btn-outline-secondary">Legacy card ledger</a></div></div></div><div class="alert alert-info"><strong>Manual sanctions use the case workflow:</strong> create the case, propose its effect and reason, then have a separately authorised admin approve it before publication. Every step is retained in the immutable timeline.</div><div class="table-responsive"><table class="table table-hover"><thead><tr><th>Reference</th><th>Source</th><th>Club / team</th><th>Status</th><th>Assigned</th><th>Opened</th></tr></thead><tbody>`)
		for rows.Next() {
			var id int64
			var ref, source, status, club, team, assigned string
			var created time.Time
			if rows.Scan(&id, &ref, &source, &status, &club, &team, &created, &assigned) == nil {
				fmt.Fprintf(w, `<tr><td><a href="/admin/cases/%d">%s</a></td><td>%s</td><td>%s — %s</td><td>%s</td><td>%s</td><td>%s</td></tr>`, id, escapeHTML(ref), escapeHTML(source), escapeHTML(club), escapeHTML(team), escapeHTML(status), escapeHTML(assigned), created.In(s.LondonLoc).Format("02 Jan 2006 15:04"))
			}
		}
		fmt.Fprint(w, `</tbody></table></div></main>`)
		pageFooter(w)
	}
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
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:900px"><a href="/admin/cases" class="btn btn-sm btn-outline-secondary mb-3">Back to cases</a><h1 class="h2">Add a card, ban, fine or points decision</h1><p class="text-muted">This creates an attributed manual case. It does not issue a sanction immediately: a decision must be proposed, independently approved and then published.</p><form method="POST" action="/admin/cases" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-6"><label class="form-label">Case source</label><select class="form-select" name="source_type" required><option value="manual">Manual referral</option><option value="discipline">Discipline</option><option value="ineligible_player">Ineligible player</option><option value="grounds_facilities">Grounds or facilities</option><option value="forfeit">Forfeit / withdrawal</option><option value="play_cricket">Play-Cricket finding</option></select></div><div class="col-md-6"><label class="form-label">Offence / match date</label><input class="form-control" type="date" name="match_date" required value="%s"></div><div class="col-12"><label class="form-label">Affected team</label><select class="form-select" name="team_id" required><option value="">Choose club and team...</option>`, csrf, time.Now().In(s.LondonLoc).Format("2006-01-02"))
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

func (s *Server) handleAdminCaseDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		var ref, source, status, publicSummary, privateSummary, club, team string
		var proposer, approver *int32
		var hasProposed bool
		err := s.DB.QueryRow(r.Context(), `SELECT c.reference,c.source_type,c.status,COALESCE(c.public_summary,''),COALESCE(c.private_summary,''),COALESCE(cl.name,''),COALESCE(t.name,''),c.proposed_by_admin_id,c.approved_by_admin_id,EXISTS(SELECT 1 FROM sanction_decision_revisions d WHERE d.case_id=c.id AND d.status='proposed') FROM sanction_cases c LEFT JOIN clubs cl ON cl.id=c.club_id LEFT JOIN teams t ON t.id=c.team_id WHERE c.id=$1`, id).Scan(&ref, &source, &status, &publicSummary, &privateSummary, &club, &team, &proposer, &approver, &hasProposed)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Case "+ref)
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4" style="max-width:1000px"><a href="/admin/cases" class="btn btn-sm btn-outline-secondary mb-3">Back to cases</a><div class="d-flex justify-content-between"><div><h1 class="h2">%s</h1><p>%s — %s — %s</p></div><span class="badge text-bg-secondary align-self-start">%s</span></div><div class="row g-4"><div class="col-lg-7"><section class="card mb-4"><div class="card-header">Case record</div><div class="card-body"><h2 class="h5">Public summary</h2><p>%s</p><h2 class="h5">Private summary</h2><p>%s</p></div></section>`, escapeHTML(ref), escapeHTML(source), escapeHTML(club), escapeHTML(team), escapeHTML(status), escapeHTML(publicSummary), escapeHTML(privateSummary))
		if !hasProposed && status != "approved" && status != "published" {
			fmt.Fprintf(w, `<section class="card mb-4"><div class="card-header">Propose decision</div><form method="POST" action="/admin/cases/%d/propose"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-6"><label class="form-label">Effect</label><select class="form-select" name="effect_type"><option value="yellow_card">Yellow card</option><option value="red_card">Direct red card</option><option value="suspended_red">Suspended red</option><option value="player_ban">Player ban</option><option value="team_ban">Team ban</option><option value="fine">Fine</option><option value="points_adjustment">Points adjustment</option><option value="warning">Warning</option><option value="no_action">No action</option></select></div><div class="col-md-6"><label class="form-label">Rule reference</label><input class="form-control" name="rule_reference"></div><div class="col-12"><label class="form-label">Public reason</label><textarea class="form-control" name="public_reason" required rows="4">%s</textarea></div><div class="col-12"><label class="form-label">Private rationale</label><textarea class="form-control" name="private_reason" rows="4"></textarea></div><div class="col-md-4"><label class="form-label">Fine, pounds</label><input class="form-control" name="fine_pounds" type="number" min="0" step="0.01"></div><div class="col-md-4"><label class="form-label">Points</label><input class="form-control" name="points" type="number"></div><div class="col-md-4"><label class="form-label">Remedy / end date</label><input class="form-control" name="ends_at" type="date"></div><div class="col-12"><label class="form-check"><input class="form-check-input" type="checkbox" name="rescindable" value="yes"> <span class="form-check-label">Yellow is rescindable and does not enter the effective balance until the remedy is missed</span></label></div></div><div class="card-footer"><button class="btn btn-primary">Submit for separate approval</button></div></form></section>`, id, csrf, escapeHTML(publicSummary))
		}
		if status == "triage" && hasProposed {
			fmt.Fprint(w, `<div class="alert alert-info">This is a shadow-mode calculated candidate. Change the source to manual mode before a future run; this candidate remains available for reconciliation but cannot be published.</div>`)
		}
		fmt.Fprint(w, `<section class="card"><div class="card-header">Immutable timeline</div><ul class="list-group list-group-flush">`)
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
		fmt.Fprint(w, `</ul></section></div><aside class="col-lg-5">`)
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/assign-self" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><button class="btn btn-outline-primary">Assign investigation to me</button></div></form>`, id, csrf)
		evidenceRows, _ := s.DB.Query(r.Context(), `SELECT id,original_name,media_type,byte_size,sha256,created_at FROM sanction_case_evidence WHERE case_id=$1 ORDER BY id`, id)
		if evidenceRows != nil {
			defer evidenceRows.Close()
			fmt.Fprint(w, `<section class="card mb-3"><div class="card-header">Evidence</div><ul class="list-group list-group-flush">`)
			count := 0
			for evidenceRows.Next() {
				var evidenceID, size int64
				var name, media, sum string
				var at time.Time
				if evidenceRows.Scan(&evidenceID, &name, &media, &size, &sum, &at) == nil {
					count++
					fmt.Fprintf(w, `<li class="list-group-item"><a href="/admin/cases/%d/evidence/%d">%s</a><div class="small text-muted">%s · %d bytes · SHA-256 %s</div></li>`, id, evidenceID, escapeHTML(name), escapeHTML(media), size, escapeHTML(sum[:minInt(12, len(sum))]))
				}
			}
			if count == 0 {
				fmt.Fprint(w, `<li class="list-group-item text-muted">No evidence uploaded.</li>`)
			}
			fmt.Fprint(w, `</ul></section>`)
		}
		if status == "decision_proposed" {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/approve" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Independent approval</div><div class="card-body"><p>The proposer cannot approve this decision.</p><label class="form-label">Emergency override reason (super-admin only)</label><textarea class="form-control" name="emergency_reason" rows="2"></textarea></div><div class="card-footer"><button class="btn btn-success">Approve decision</button></div></form>`, id, csrf)
		}
		if status == "decision_proposed" || (status == "triage" && hasProposed) {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/reject" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Reject calculated proposal</div><div class="card-body"><label class="form-label">Reason</label><textarea class="form-control" name="reason" required rows="2"></textarea></div><div class="card-footer"><button class="btn btn-outline-secondary">Reject proposal</button></div></form>`, id, csrf)
		}
		if status == "approved" {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/publish" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><p>Publication exposes only the approved public facts and queues immutable notice snapshots.</p><button class="btn btn-danger">Publish and queue notices</button></div></form>`, id, csrf)
		}
		if status == "approved" || status == "published" || status == "appealed" {
			fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/overturn" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Overturn decision</div><div class="card-body"><label class="form-label">Reason</label><textarea class="form-control" name="reason" required rows="3"></textarea></div><div class="card-footer"><button class="btn btn-outline-danger">Record reversal</button></div></form>`, id, csrf)
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/%d/request-response" class="card mb-3"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><p class="mb-2">Send the affected team captain a 14-day secure case-response link.</p><button class="btn btn-outline-danger">Request club response</button></div></form><form method="POST" action="/admin/cases/%d/correct" class="card"><input type="hidden" name="csrf_token" value="%s"><div class="card-header">Correct case summary</div><div class="card-body"><label class="form-label">Public summary</label><textarea class="form-control mb-2" name="public_summary" rows="3">%s</textarea><label class="form-label">Private summary</label><textarea class="form-control mb-2" name="private_summary" rows="3">%s</textarea><label class="form-label">Correction reason</label><textarea class="form-control" name="reason" required rows="2"></textarea></div><div class="card-footer"><button class="btn btn-outline-primary">Record immutable correction</button></div></form></aside></div></main>`, id, csrf, id, csrf, escapeHTML(publicSummary), escapeHTML(privateSummary))
		pageFooter(w)
	}
}

func (s *Server) handleAdminCasePropose() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		_ = r.ParseForm()
		var amount *int64
		if f, err := strconv.ParseFloat(r.FormValue("fine_pounds"), 64); err == nil && f >= 0 {
			v := int64(f*100 + 0.5)
			amount = &v
		}
		var points *int
		if v, err := strconv.Atoi(r.FormValue("points")); err == nil {
			points = &v
		}
		var ends *time.Time
		if v, err := time.Parse("2006-01-02", r.FormValue("ends_at")); err == nil {
			ends = &v
		}
		_, err := sanctiondomain.NewService(s.DB).ProposeDecision(r.Context(), sanctiondomain.DecisionRequest{CaseID: id, EffectType: r.FormValue("effect_type"), PublicReason: r.FormValue("public_reason"), PrivateReason: r.FormValue("private_reason"), RuleReference: r.FormValue("rule_reference"), AmountPence: amount, Points: points, EndsAt: ends, Rescindable: r.FormValue("rescindable") == "yes", Actor: adminActor(r)})
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
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
		err := sanctiondomain.NewService(s.DB).ApproveCase(r.Context(), id, adminActor(r), emergency)
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
		var beforePublic, beforePrivate string
		if tx.QueryRow(r.Context(), `SELECT COALESCE(public_summary,''),COALESCE(private_summary,'') FROM sanction_cases WHERE id=$1 FOR UPDATE`, id).Scan(&beforePublic, &beforePrivate) != nil {
			http.NotFound(w, r)
			return
		}
		afterPublic := strings.TrimSpace(r.FormValue("public_summary"))
		afterPrivate := strings.TrimSpace(r.FormValue("private_summary"))
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
		_, err = tx.Exec(r.Context(), `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,before_data,after_data,request_id) VALUES($1,'investigator_assigned','admin',$2,$3,'Investigation assigned',jsonb_build_object('assigned_admin_id',$4),jsonb_build_object('assigned_admin_id',$2),$5)`, id, *actor.ID, actor.Label, previous, actor.RequestID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE sanction_cases SET assigned_admin_id=$2,status=CASE WHEN status IN ('submitted','triage') THEN 'investigating' ELSE status END,updated_at=now() WHERE id=$1`, id, *actor.ID)
		}
		if err != nil || tx.Commit(r.Context()) != nil {
			http.Error(w, "assignment failed", 500)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/admin/cases/%d", id), 303)
	}
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

func evidenceDir() string {
	if v := strings.TrimSpace(os.Getenv("SANCTIONS_EVIDENCE_DIR")); v != "" {
		return v
	}
	return filepath.Join("data", "sanction-evidence")
}
func copyEvidence(file multipart.File, header *multipart.FileHeader) (string, string, int64, string, error) {
	media := header.Header.Get("Content-Type")
	allowed := strings.HasPrefix(media, "image/") || media == "application/pdf" || media == "text/plain"
	if !allowed {
		return "", "", 0, "", fmt.Errorf("unsupported evidence type")
	}
	if header.Size > 10<<20 {
		return "", "", 0, "", fmt.Errorf("evidence exceeds 10 MB")
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
	defer out.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(file, (10<<20)+1))
	if err != nil {
		return "", "", 0, "", err
	}
	if n > 10<<20 {
		return "", "", 0, "", fmt.Errorf("evidence exceeds 10 MB")
	}
	return key, hex.EncodeToString(hash.Sum(nil)), n, media, nil
}

func (s *Server) createGroundsReviewFromSubmission(ctx context.Context, r *http.Request, submissionID int64, sess *captainSession, matchDate time.Time, scores map[string]int) {
	var clubID int32
	if s.DB.QueryRow(ctx, `SELECT club_id FROM teams WHERE id=$1`, sess.TeamID).Scan(&clubID) != nil {
		return
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	var caseID int64
	err = tx.QueryRow(ctx, `INSERT INTO sanction_cases(source_type,status,season_id,week_id,club_id,team_id,match_date,submission_id,public_summary,private_summary) VALUES('grounds_facilities','triage',$1,$2,$3,$4,$5,$6,'Captain report recorded an unfit pitch or ground rating requiring review',$7) ON CONFLICT(submission_id,source_type) WHERE submission_id IS NOT NULL DO NOTHING RETURNING id`, sess.SeasonID, sess.WeekID, clubID, sess.TeamID, matchDate, submissionID, fmt.Sprintf("Unfit criteria: %+v", scores)).Scan(&caseID)
	if err != nil {
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,after_data,request_id) VALUES($1,'automated_finding','captain',$2,$3,'Unfit rating requires Grounds and Facilities review',$4,$5)`, caseID, sess.CaptainID, sess.SubmitterName, mapJSONHTTP(scores), requestID(r))
	if err == nil {
		_ = tx.Commit(ctx)
	}
}

func mapJSONHTTP(v any) []byte { b, _ := json.Marshal(v); return b }
