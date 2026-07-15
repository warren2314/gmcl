package httpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const pitchPreviewCookie = "pitch_csv_preview"

var pitchCompetitionNames = []string{
	"GMCL Saturday Premier",
	"GMCL Saturday Premier 2",
	"GMCL Saturday Championship",
	"GMCL Saturday Division 1",
}

type pitchVector struct {
	Uneven float64
	Seam   float64
	Carry  float64
	Turn   float64
}

func (v pitchVector) add(o pitchVector) pitchVector {
	return pitchVector{v.Uneven + o.Uneven, v.Seam + o.Seam, v.Carry + o.Carry, v.Turn + o.Turn}
}

func (v pitchVector) div(n float64) pitchVector {
	if n == 0 {
		return pitchVector{}
	}
	return pitchVector{v.Uneven / n, v.Seam / n, v.Carry / n, v.Turn / n}
}

func (v pitchVector) overall() float64 { return (v.Uneven + v.Seam + v.Carry + v.Turn) / 4 }

type umpirePitchParsedRow struct {
	Index      int               `json:"index"`
	Timestamp  time.Time         `json:"timestamp"`
	MatchDate  time.Time         `json:"match_date"`
	Division   string            `json:"division"`
	HomeClub   string            `json:"home_club"`
	AwayClub   string            `json:"away_club"`
	Marks      pitchVector       `json:"marks"`
	Hash       string            `json:"hash"`
	Raw        map[string]string `json:"raw"`
	Errors     []string          `json:"errors,omitempty"`
	Status     string            `json:"status"`
	Candidates []pitchCandidate  `json:"candidates,omitempty"`
	SelectedID int64             `json:"selected_id,omitempty"`
}

type pitchCandidate struct {
	MatchID     int64     `json:"match_id"`
	MatchDate   time.Time `json:"match_date"`
	Competition string    `json:"competition"`
	HomeClub    string    `json:"home_club"`
	AwayClub    string    `json:"away_club"`
}

func (c pitchCandidate) label() string {
	return fmt.Sprintf("%s — %s v %s (%s)", c.MatchDate.Format("2 Jan 2006"), c.HomeClub, c.AwayClub, c.Competition)
}

type pitchImportPreview struct {
	Filename string                 `json:"filename"`
	Checksum string                 `json:"checksum"`
	Rows     []umpirePitchParsedRow `json:"rows"`
}

func parseUmpirePitchFile(data []byte) ([]umpirePitchParsedRow, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("file is empty")
	}
	firstLine := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		firstLine = data[:i]
	}
	delimiter := ','
	if bytes.Count(firstLine, []byte{'\t'}) > bytes.Count(firstLine, []byte{','}) {
		delimiter = '\t'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delimiter
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse delimited file: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("file has no data rows")
	}
	header := make(map[string]int, len(records[0]))
	for i, h := range records[0] {
		header[normalizePitchHeader(h)] = i
	}
	required := []string{
		"timestamp", "which division was your game in?", "date of game",
		"home club full formal name", "away club full formal name",
		"unevenness of bounce", "seam movement", "carry and / or bounce", "turn",
	}
	for _, h := range required {
		if _, ok := header[normalizePitchHeader(h)]; !ok {
			return nil, fmt.Errorf("required column missing: %s", h)
		}
	}

	field := func(record []string, name string) string {
		i, ok := header[normalizePitchHeader(name)]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	loc, _ := time.LoadLocation("Europe/London")
	if loc == nil {
		loc = time.UTC
	}
	rows := make([]umpirePitchParsedRow, 0, len(records)-1)
	for i, record := range records[1:] {
		if len(record) == 0 || strings.TrimSpace(strings.Join(record, "")) == "" {
			continue
		}
		row := umpirePitchParsedRow{Index: i + 1, Raw: map[string]string{}, Status: "invalid"}
		for name, idx := range header {
			if idx < len(record) {
				row.Raw[name] = strings.TrimSpace(record[idx])
			}
		}
		row.Division = field(record, "Which Division was your game in?")
		row.HomeClub = pitchClubField(field(record, "Home Club Full Formal Name"), field(record, "If Home Club Not Listed, enter club name here"))
		row.AwayClub = pitchClubField(field(record, "Away Club Full Formal Name"), field(record, "If Away Club Not Listed, enter club name here"))
		row.MatchDate, err = time.ParseInLocation("02/01/2006", field(record, "Date of Game"), loc)
		if err != nil {
			row.Errors = append(row.Errors, "invalid date of game")
		}
		row.Timestamp, err = time.ParseInLocation("02/01/2006 15:04:05", field(record, "Timestamp"), loc)
		if err != nil {
			row.Errors = append(row.Errors, "invalid timestamp")
		}
		if row.HomeClub == "" || row.AwayClub == "" || row.Division == "" {
			row.Errors = append(row.Errors, "home club, away club and division are required")
		}
		mark := func(name string) float64 {
			v, parseErr := strconv.Atoi(field(record, name))
			if parseErr != nil || v < 1 || v > 5 {
				row.Errors = append(row.Errors, name+" must be an integer from 1 to 5")
				return 0
			}
			return float64(v)
		}
		row.Marks = pitchVector{
			Uneven: mark("Unevenness of bounce"),
			Seam:   mark("Seam movement"),
			Carry:  mark("Carry and / or bounce"),
			Turn:   mark("Turn"),
		}
		canonical := fmt.Sprintf("%s|%s|%s|%s|%s|%.0f|%.0f|%.0f|%.0f",
			row.Timestamp.Format(time.RFC3339), row.MatchDate.Format("2006-01-02"),
			normalizeCaptainCSVClubKey(row.HomeClub), normalizeCaptainCSVClubKey(row.AwayClub), strings.ToLower(row.Division),
			row.Marks.Uneven, row.Marks.Seam, row.Marks.Carry, row.Marks.Turn)
		h := sha256.Sum256([]byte(canonical))
		row.Hash = hex.EncodeToString(h[:])
		rows = append(rows, row)
	}
	return rows, nil
}

func normalizePitchHeader(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(s)), "\ufeff")
	return strings.Join(strings.Fields(s), " ")
}

func pitchClubField(formal, fallback string) string {
	formal = strings.TrimSpace(formal)
	lower := strings.ToLower(formal)
	if formal == "" || lower == "other" || lower == "not listed" || lower == "n/a" || lower == "na" || lower == "none" {
		return strings.TrimSpace(fallback)
	}
	return formal
}

func matchPitchRow(row umpirePitchParsedRow, candidates []pitchCandidate) (string, []pitchCandidate, int64) {
	var sameDivision []pitchCandidate
	for _, c := range candidates {
		if c.MatchDate.Format("2006-01-02") == row.MatchDate.Format("2006-01-02") && (c.Competition == "" || strings.EqualFold(strings.TrimSpace(c.Competition), strings.TrimSpace(row.Division))) {
			sameDivision = append(sameDivision, c)
		}
	}
	var exact []pitchCandidate
	homeKey, awayKey := normalizeCaptainCSVClubKey(row.HomeClub), normalizeCaptainCSVClubKey(row.AwayClub)
	for _, c := range sameDivision {
		if normalizeCaptainCSVClubKey(c.HomeClub) == homeKey && normalizeCaptainCSVClubKey(c.AwayClub) == awayKey {
			exact = append(exact, c)
		}
	}
	if len(exact) == 1 {
		return "exact", exact, exact[0].MatchID
	}
	if len(exact) > 1 {
		return "ambiguous", exact, 0
	}
	type scored struct {
		candidate pitchCandidate
		distance  int
	}
	var scores []scored
	for _, c := range sameDivision {
		hd := levenshtein(homeKey, normalizeCaptainCSVClubKey(c.HomeClub))
		ad := levenshtein(awayKey, normalizeCaptainCSVClubKey(c.AwayClub))
		if hd <= 2 && ad <= 2 {
			scores = append(scores, scored{c, hd + ad})
		}
	}
	if len(scores) == 0 {
		return "unmatched", nil, 0
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].distance < scores[j].distance })
	best := scores[0].distance
	var bestCandidates []pitchCandidate
	for _, s := range scores {
		if s.distance == best {
			bestCandidates = append(bestCandidates, s.candidate)
		}
	}
	if len(bestCandidates) == 1 {
		return "suggested", bestCandidates, bestCandidates[0].MatchID
	}
	return "ambiguous", bestCandidates, 0
}

func (s *Server) loadPitchCandidates(ctx context.Context, from, to time.Time) (map[string][]pitchCandidate, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT play_cricket_match_id, match_date,
		       COALESCE(payload->>'competition_name',''),
		       COALESCE(home_club_name,''), COALESCE(away_club_name,'')
		FROM league_fixtures
		WHERE match_date BETWEEN $1 AND $2
		ORDER BY match_date, home_club_name, away_club_name
	`, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]pitchCandidate{}
	for rows.Next() {
		var c pitchCandidate
		if err := rows.Scan(&c.MatchID, &c.MatchDate, &c.Competition, &c.HomeClub, &c.AwayClub); err != nil {
			return nil, err
		}
		key := c.MatchDate.Format("2006-01-02")
		result[key] = append(result[key], c)
	}
	return result, rows.Err()
}

func (s *Server) handleAdminPitchMarksGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrf := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrf = c.Value
		}
		var imports, reports int64
		_ = s.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM umpire_pitch_imports`).Scan(&imports)
		_ = s.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM umpire_pitch_reports`).Scan(&reports)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Pitch Mark Comparison")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<div class="container-fluid px-4">
<h4 class="mb-1 fw-bold">Pitch Mark Comparison</h4>
<p class="text-muted">Import umpire pitch reports and download the club comparison CSV.</p>
<div class="row g-3 mb-4"><div class="col-md-6"><div class="card shadow-sm h-100"><div class="card-body">
<h5>Import umpire reports</h5><p class="small text-muted">Accepts CSV, TSV or tab-separated TXT exports. Every row is previewed and matched to a Play-Cricket fixture before import.</p>
<form method="POST" action="/admin/pitch-marks/import/preview" enctype="multipart/form-data">
<input type="hidden" name="csrf_token" value="%s"><input class="form-control mb-3" type="file" name="pitch_file" accept=".csv,.tsv,.txt" required>
<button class="btn btn-primary" type="submit">Upload and preview</button></form>
<p class="small text-muted mt-3 mb-0">%d imports · %d reports stored</p></div></div></div>
<div class="col-md-6"><div class="card shadow-sm h-100"><div class="card-body"><h5>Download comparison CSV</h5>
<form method="GET" action="/admin/pitch-marks/export.csv">
<div class="row g-2"><div class="col-6"><label class="form-label">From</label><input class="form-control" type="date" name="from" value="2026-04-18" required></div>
<div class="col-6"><label class="form-label">To</label><input class="form-control" type="date" name="to" value="2026-06-27" required></div>
<div class="col-4"><label class="form-label">Home %%</label><input class="form-control" type="number" name="home_weight" value="10" min="0" max="100" step="0.01" required></div>
<div class="col-4"><label class="form-label">Away %%</label><input class="form-control" type="number" name="away_weight" value="40" min="0" max="100" step="0.01" required></div>
<div class="col-4"><label class="form-label">Umpire %%</label><input class="form-control" type="number" name="umpire_weight" value="50" min="0" max="100" step="0.01" required></div></div>
<p class="form-text">Weights must total 100%%. Missing sources are rebalanced automatically.</p>
<button class="btn btn-success" type="submit">Download CSV</button></form></div></div></div></div>
<div class="alert alert-info"><strong>Included competitions:</strong> %s. Fixture dates must be Saturdays.</div></div>`,
			escapeHTML(csrf), imports, reports, escapeHTML(strings.Join(pitchCompetitionNames, ", ")))
		pageFooter(w)
	}
}

func (s *Server) handleAdminPitchMarksPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		f, fh, err := r.FormFile("pitch_file")
		if err != nil {
			http.Error(w, "pitch file is required", http.StatusBadRequest)
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16<<20))
		if err != nil {
			http.Error(w, "could not read upload", http.StatusBadRequest)
			return
		}
		parsed, err := parseUmpirePitchFile(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		var from, to time.Time
		for _, row := range parsed {
			if !row.MatchDate.IsZero() && (from.IsZero() || row.MatchDate.Before(from)) {
				from = row.MatchDate
			}
			if !row.MatchDate.IsZero() && (to.IsZero() || row.MatchDate.After(to)) {
				to = row.MatchDate
			}
		}
		if from.IsZero() || to.IsZero() {
			http.Error(w, "file contains no valid match dates", http.StatusBadRequest)
			return
		}
		fixtures, err := s.loadPitchCandidates(ctx, from, to)
		if err != nil {
			http.Error(w, "could not load fixtures", http.StatusInternalServerError)
			return
		}
		seenHashes := map[string]bool{}
		for i := range parsed {
			row := &parsed[i]
			if len(row.Errors) > 0 {
				continue
			}
			if seenHashes[row.Hash] {
				row.Status = "duplicate"
				continue
			}
			seenHashes[row.Hash] = true
			var duplicate bool
			_ = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM umpire_pitch_reports WHERE source_row_hash=$1)`, row.Hash).Scan(&duplicate)
			if duplicate {
				row.Status = "duplicate"
				continue
			}
			row.Status, row.Candidates, row.SelectedID = matchPitchRow(*row, fixtures[row.MatchDate.Format("2006-01-02")])
		}
		sum := sha256.Sum256(data)
		preview := pitchImportPreview{Filename: fh.Filename, Checksum: base64.RawURLEncoding.EncodeToString(sum[:]), Rows: parsed}
		previewJSON, _ := json.Marshal(preview)
		token, err := s.storePitchPreview(ctx, r, preview.Checksum, previewJSON)
		if err != nil {
			http.Error(w, "could not store preview", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: pitchPreviewCookie, Value: token, Path: "/admin", Expires: time.Now().Add(30 * time.Minute), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		csrf := ""
		if c, err := r.Cookie(middleware.CSRFCookieName); err == nil {
			csrf = c.Value
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Pitch Import Preview")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<div class="container-fluid px-4"><h4>Pitch import preview</h4><p class="text-muted">%d source rows. Suggested and ambiguous matches can be confirmed below.</p>
<form method="POST" action="/admin/pitch-marks/import/apply"><input type="hidden" name="csrf_token" value="%s">
<div class="table-responsive"><table class="table table-sm table-bordered align-middle"><thead><tr><th>#</th><th>Date</th><th>Division</th><th>Fixture from file</th><th>Marks</th><th>Status / fixture</th><th>Import</th></tr></thead><tbody>`, len(parsed), escapeHTML(csrf))
		for _, row := range parsed {
			badge := map[string]string{"exact": "bg-success", "suggested": "bg-warning text-dark", "ambiguous": "bg-warning text-dark", "duplicate": "bg-secondary", "unmatched": "bg-danger", "invalid": "bg-danger"}[row.Status]
			selectHTML := ""
			canImport := row.Status == "exact" || row.Status == "suggested" || row.Status == "ambiguous"
			if canImport {
				selectHTML = fmt.Sprintf(`<select class="form-select form-select-sm" name="fixture_%d">`, row.Index)
				if row.Status == "ambiguous" {
					selectHTML += `<option value="">Select fixture…</option>`
				}
				for _, c := range row.Candidates {
					sel := ""
					if c.MatchID == row.SelectedID {
						sel = " selected"
					}
					selectHTML += fmt.Sprintf(`<option value="%d"%s>%s</option>`, c.MatchID, sel, escapeHTML(c.label()))
				}
				selectHTML += `</select>`
			}
			reason := strings.Join(row.Errors, "; ")
			check := "—"
			if canImport {
				check = fmt.Sprintf(`<input class="form-check-input" type="checkbox" name="include" value="%d" checked>`, row.Index)
			}
			fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%s</td><td>%s v %s</td><td>%.0f / %.0f / %.0f / %.0f</td><td><span class="badge %s">%s</span><div class="small text-danger">%s</div>%s</td><td class="text-center">%s</td></tr>`,
				row.Index, row.MatchDate.Format("02/01/2006"), escapeHTML(row.Division), escapeHTML(row.HomeClub), escapeHTML(row.AwayClub),
				row.Marks.Uneven, row.Marks.Seam, row.Marks.Carry, row.Marks.Turn, badge, escapeHTML(row.Status), escapeHTML(reason), selectHTML, check)
		}
		fmt.Fprint(w, `</tbody></table></div><button class="btn btn-success" type="submit">Import selected rows</button> <a class="btn btn-outline-secondary" href="/admin/pitch-marks">Cancel</a></form></div>`)
		pageFooter(w)
		s.audit(ctx, r, "admin", nil, "umpire_pitch_import_preview", "csv_upload", nil, map[string]any{"filename": fh.Filename, "rows": len(parsed), "checksum": preview.Checksum})
	}
}

func (s *Server) storePitchPreview(ctx context.Context, r *http.Request, checksum string, previewJSON []byte) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(token))
	adminID := adminIDForRequest(r)
	_, err := s.DB.Exec(ctx, `INSERT INTO csv_preview_tokens(id,token_hash,admin_user_id,checksum,preview_json,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), h[:], adminID, checksum, previewJSON, time.Now().Add(30*time.Minute))
	return token, err
}

func (s *Server) handleAdminPitchMarksApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		cookie, err := r.Cookie(pitchPreviewCookie)
		if err != nil {
			http.Error(w, "preview expired", http.StatusBadRequest)
			return
		}
		h := sha256.Sum256([]byte(cookie.Value))
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			http.Error(w, "could not start import", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(ctx)
		var storedHash, previewJSON []byte
		var checksum string
		var expires time.Time
		var used *time.Time
		err = tx.QueryRow(ctx, `SELECT token_hash,preview_json,checksum,expires_at,used_at FROM csv_preview_tokens WHERE token_hash=$1 AND admin_user_id=$2 FOR UPDATE`, h[:], adminIDForRequest(r)).Scan(&storedHash, &previewJSON, &checksum, &expires, &used)
		if err != nil || subtle.ConstantTimeCompare(storedHash, h[:]) != 1 || used != nil || time.Now().After(expires) {
			http.Error(w, "preview invalid, expired or already used", http.StatusBadRequest)
			return
		}
		var preview pitchImportPreview
		if json.Unmarshal(previewJSON, &preview) != nil {
			http.Error(w, "preview invalid", http.StatusBadRequest)
			return
		}
		included := map[int]bool{}
		for _, v := range r.Form["include"] {
			if n, e := strconv.Atoi(v); e == nil {
				included[n] = true
			}
		}
		var importID int64
		err = tx.QueryRow(ctx, `INSERT INTO umpire_pitch_imports(source_filename,source_checksum,imported_by_admin_id,row_count) VALUES($1,$2,$3,$4) RETURNING id`, preview.Filename, checksum, adminIDForRequest(r), len(preview.Rows)).Scan(&importID)
		if err != nil {
			http.Error(w, "could not create import", http.StatusInternalServerError)
			return
		}
		imported := 0
		for _, row := range preview.Rows {
			if !included[row.Index] || (row.Status != "exact" && row.Status != "suggested" && row.Status != "ambiguous") {
				continue
			}
			selected, _ := strconv.ParseInt(r.FormValue(fmt.Sprintf("fixture_%d", row.Index)), 10, 64)
			validCandidate := false
			for _, c := range row.Candidates {
				if c.MatchID == selected {
					validCandidate = true
					break
				}
			}
			if !validCandidate {
				continue
			}
			rawJSON, _ := json.Marshal(row.Raw)
			cmd, execErr := tx.Exec(ctx, `
				INSERT INTO umpire_pitch_reports(import_id,play_cricket_match_id,source_timestamp,match_date,division_label,home_club_label,away_club_label,unevenness_mark,seam_mark,carry_mark,turn_mark,source_row_hash,source_row)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
				ON CONFLICT(source_row_hash) DO NOTHING
			`, importID, selected, row.Timestamp, row.MatchDate, row.Division, row.HomeClub, row.AwayClub, int(row.Marks.Uneven), int(row.Marks.Seam), int(row.Marks.Carry), int(row.Marks.Turn), row.Hash, rawJSON)
			if execErr != nil {
				http.Error(w, "could not import selected rows", http.StatusInternalServerError)
				return
			}
			imported += int(cmd.RowsAffected())
		}
		_, err = tx.Exec(ctx, `UPDATE umpire_pitch_imports SET imported_count=$2,skipped_count=$3 WHERE id=$1`, importID, imported, len(preview.Rows)-imported)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE csv_preview_tokens SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL`, h[:])
		}
		if err != nil || tx.Commit(ctx) != nil {
			http.Error(w, "could not finish import", http.StatusInternalServerError)
			return
		}
		s.audit(ctx, r, "admin", nil, "umpire_pitch_import_apply", "umpire_pitch_import", &importID, map[string]any{"imported": imported, "skipped": len(preview.Rows) - imported, "checksum": checksum})
		http.Redirect(w, r, "/admin/pitch-marks?imported="+strconv.Itoa(imported), http.StatusSeeOther)
	}
}

type pitchWeights struct{ Home, Away, Umpire float64 }

func parsePitchWeights(r *http.Request) (pitchWeights, error) {
	parse := func(name string, fallback float64) (float64, error) {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			return fallback, nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 0 || v > 100 {
			return 0, fmt.Errorf("%s must be between 0 and 100", name)
		}
		return v, nil
	}
	h, err := parse("home_weight", 10)
	if err != nil {
		return pitchWeights{}, err
	}
	a, err := parse("away_weight", 40)
	if err != nil {
		return pitchWeights{}, err
	}
	u, err := parse("umpire_weight", 50)
	if err != nil {
		return pitchWeights{}, err
	}
	if math.Abs(h+a+u-100) > 0.001 {
		return pitchWeights{}, fmt.Errorf("weights must total 100")
	}
	return pitchWeights{h, a, u}, nil
}

func captainPitchMark(level int) float64 {
	switch level {
	case 1:
		return 5
	case 2:
		return 4
	case 3:
		return 3
	case 4:
		return 2
	case 5, 6:
		return 1
	default:
		return 0
	}
}

func captainPitchVector(data []byte) (pitchVector, bool) {
	var form map[string]any
	if json.Unmarshal(data, &form) != nil {
		return pitchVector{}, false
	}
	if outcome := strings.TrimSpace(fmt.Sprint(form["match_outcome"])); outcome != "" && outcome != "played" && outcome != "play_started_abandoned" {
		return pitchVector{}, false
	}
	read := func(key string) int {
		switch v := form[key].(type) {
		case float64:
			return int(v)
		case string:
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		default:
			return 0
		}
	}
	v := pitchVector{captainPitchMark(read("unevenness_of_bounce")), captainPitchMark(read("seam_movement")), captainPitchMark(read("carry_bounce")), captainPitchMark(read("turn"))}
	return v, v.Uneven > 0 && v.Seam > 0 && v.Carry > 0 && v.Turn > 0
}

func weightedPitchVector(sources map[string]pitchVector, weights pitchWeights) (pitchVector, map[string]float64, []string, bool) {
	configured := map[string]float64{"home": weights.Home, "away": weights.Away, "umpire": weights.Umpire}
	effective := map[string]float64{"home": 0, "away": 0, "umpire": 0}
	missing := []string{}
	total := 0.0
	for _, name := range []string{"home", "away", "umpire"} {
		if _, ok := sources[name]; !ok {
			missing = append(missing, name)
			continue
		}
		total += configured[name]
	}
	if total == 0 {
		return pitchVector{}, effective, missing, false
	}
	result := pitchVector{}
	for name, v := range sources {
		w := configured[name] / total
		effective[name] = w * 100
		result = result.add(pitchVector{v.Uneven * w, v.Seam * w, v.Carry * w, v.Turn * w})
	}
	return result, effective, missing, true
}

type pitchFixture struct {
	ID, HomeTeamPC, AwayTeamPC string
	MatchID                    int64
	Date                       time.Time
	Competition, HomeClub      string
}

type pitchSourceValue struct {
	Vector  pitchVector
	Reports int
}
type pitchClubAggregate struct {
	Name             string
	Divisions        map[string]bool
	FixtureCount     int
	Sources          map[string][]pitchSourceValue
	ExcludedCaptains int
}

func (s *Server) handleAdminPitchMarksExportCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		weights, err := parsePitchWeights(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fromRaw, toRaw := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		if fromRaw == "" {
			fromRaw = "2026-04-18"
		}
		if toRaw == "" {
			toRaw = "2026-06-27"
		}
		from, err1 := time.Parse("2006-01-02", fromRaw)
		to, err2 := time.Parse("2006-01-02", toRaw)
		if err1 != nil || err2 != nil || to.Before(from) {
			http.Error(w, "invalid date range", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		fixtures, clubs, err := s.loadPitchExportFixtures(ctx, from, to)
		if err != nil {
			http.Error(w, "could not load fixtures", http.StatusInternalServerError)
			return
		}
		if err := s.addCaptainPitchSources(ctx, from, to, fixtures, clubs); err != nil {
			http.Error(w, "could not load captain marks", http.StatusInternalServerError)
			return
		}
		if err := s.addUmpirePitchSources(ctx, from, to, fixtures, clubs); err != nil {
			http.Error(w, "could not load umpire marks", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gmcl-pitch-comparison-%s-to-%s.csv"`, from.Format("20060102"), to.Format("20060102")))
		w.Header().Set("Cache-Control", "no-store")
		cw := csv.NewWriter(w)
		header := []string{"Club", "Divisions", "Eligible fixtures",
			"Home captain fixtures", "Home unevenness", "Home seam", "Home carry/bounce", "Home turn", "Home overall",
			"Away captain fixtures", "Away unevenness", "Away seam", "Away carry/bounce", "Away turn", "Away overall",
			"Combined captain unevenness", "Combined captain seam", "Combined captain carry/bounce", "Combined captain turn", "Combined captain overall",
			"Umpire fixtures", "Umpire reports", "Umpire unevenness", "Umpire seam", "Umpire carry/bounce", "Umpire turn", "Umpire overall",
			"Weighted unevenness", "Weighted seam", "Weighted carry/bounce", "Weighted turn", "Weighted overall",
			"Configured home %", "Configured away %", "Configured umpire %", "Effective home %", "Effective away %", "Effective umpire %",
			"Missing sources", "Excluded captain reports"}
		_ = cw.Write(header)
		keys := make([]string, 0, len(clubs))
		for k := range clubs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return clubs[keys[i]].Name < clubs[keys[j]].Name })
		for _, key := range keys {
			club := clubs[key]
			home, homeOK, homeFixtures, _ := averagePitchSource(club.Sources["home"])
			away, awayOK, awayFixtures, _ := averagePitchSource(club.Sources["away"])
			umpire, umpOK, umpFixtures, umpReports := averagePitchSource(club.Sources["umpire"])
			captainSources := map[string]pitchVector{}
			if homeOK {
				captainSources["home"] = home
			}
			if awayOK {
				captainSources["away"] = away
			}
			combined, _, _, combinedOK := weightedPitchVector(captainSources, pitchWeights{Home: weights.Home, Away: weights.Away})
			allSources := map[string]pitchVector{}
			if homeOK {
				allSources["home"] = home
			}
			if awayOK {
				allSources["away"] = away
			}
			if umpOK {
				allSources["umpire"] = umpire
			}
			weighted, effective, missing, weightedOK := weightedPitchVector(allSources, weights)
			divisions := make([]string, 0, len(club.Divisions))
			for d := range club.Divisions {
				divisions = append(divisions, d)
			}
			sort.Strings(divisions)
			record := []string{safeCSVCell(club.Name), safeCSVCell(strings.Join(divisions, "; ")), strconv.Itoa(club.FixtureCount), strconv.Itoa(homeFixtures)}
			record = append(record, pitchVectorCells(home, homeOK)...)
			record = append(record, strconv.Itoa(awayFixtures))
			record = append(record, pitchVectorCells(away, awayOK)...)
			record = append(record, pitchVectorCells(combined, combinedOK)...)
			record = append(record, strconv.Itoa(umpFixtures), strconv.Itoa(umpReports))
			record = append(record, pitchVectorCells(umpire, umpOK)...)
			record = append(record, pitchVectorCells(weighted, weightedOK)...)
			record = append(record, format2(weights.Home), format2(weights.Away), format2(weights.Umpire), format2(effective["home"]), format2(effective["away"]), format2(effective["umpire"]), strings.Join(missing, "; "), strconv.Itoa(club.ExcludedCaptains))
			_ = cw.Write(record)
		}
		cw.Flush()
		if cw.Error() != nil {
			return
		}
		s.audit(ctx, r, "admin", nil, "pitch_mark_comparison_export", "csv_export", nil, map[string]any{"from": fromRaw, "to": toRaw, "home_weight": weights.Home, "away_weight": weights.Away, "umpire_weight": weights.Umpire, "clubs": len(clubs)})
	}
}

func (s *Server) loadPitchExportFixtures(ctx context.Context, from, to time.Time) (map[int64]pitchFixture, map[string]*pitchClubAggregate, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT play_cricket_match_id,match_date,COALESCE(payload->>'competition_name',''),COALESCE(home_club_name,''),COALESCE(home_team_pc_id,''),COALESCE(away_team_pc_id,'')
		FROM league_fixtures
		WHERE match_date BETWEEN $1 AND $2 AND EXTRACT(ISODOW FROM match_date)=6
		  AND COALESCE(payload->>'competition_name','') IN ('GMCL Saturday Premier','GMCL Saturday Premier 2','GMCL Saturday Championship','GMCL Saturday Division 1')
	`, from, to)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	fixtures := map[int64]pitchFixture{}
	clubs := map[string]*pitchClubAggregate{}
	for rows.Next() {
		var f pitchFixture
		if err := rows.Scan(&f.MatchID, &f.Date, &f.Competition, &f.HomeClub, &f.HomeTeamPC, &f.AwayTeamPC); err != nil {
			return nil, nil, err
		}
		fixtures[f.MatchID] = f
		key := normalizeCaptainCSVClubKey(f.HomeClub)
		if clubs[key] == nil {
			clubs[key] = &pitchClubAggregate{Name: f.HomeClub, Divisions: map[string]bool{}, Sources: map[string][]pitchSourceValue{}}
		}
		clubs[key].FixtureCount++
		clubs[key].Divisions[f.Competition] = true
	}
	return fixtures, clubs, rows.Err()
}

func (s *Server) addCaptainPitchSources(ctx context.Context, from, to time.Time, fixtures map[int64]pitchFixture, clubs map[string]*pitchClubAggregate) error {
	rows, err := s.DB.Query(ctx, `
		SELECT sub.play_cricket_match_id,COALESCE(t.play_cricket_team_id,''),sub.form_data,sub.submitted_at
		FROM submissions sub JOIN teams t ON t.id=sub.team_id
		JOIN league_fixtures lf ON lf.play_cricket_match_id=sub.play_cricket_match_id
		WHERE lf.match_date BETWEEN $1 AND $2 AND EXTRACT(ISODOW FROM lf.match_date)=6
		  AND COALESCE(lf.payload->>'competition_name','') IN ('GMCL Saturday Premier','GMCL Saturday Premier 2','GMCL Saturday Championship','GMCL Saturday Division 1')
	`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	type latest struct {
		vector    pitchVector
		submitted time.Time
	}
	latestBySide := map[string]latest{}
	for rows.Next() {
		var matchID int64
		var teamPC string
		var data []byte
		var submitted time.Time
		if err := rows.Scan(&matchID, &teamPC, &data, &submitted); err != nil {
			return err
		}
		f, ok := fixtures[matchID]
		if !ok {
			continue
		}
		side := ""
		teamPC = strings.TrimSpace(teamPC)
		if teamPC != "" && teamPC == strings.TrimSpace(f.HomeTeamPC) {
			side = "home"
		} else if teamPC != "" && teamPC == strings.TrimSpace(f.AwayTeamPC) {
			side = "away"
		}
		vector, valid := captainPitchVector(data)
		club := clubs[normalizeCaptainCSVClubKey(f.HomeClub)]
		if side == "" || !valid {
			club.ExcludedCaptains++
			continue
		}
		key := fmt.Sprintf("%d|%s", matchID, side)
		if previous, exists := latestBySide[key]; !exists || submitted.After(previous.submitted) {
			latestBySide[key] = latest{vector, submitted}
		}
	}
	for key, value := range latestBySide {
		parts := strings.Split(key, "|")
		matchID, _ := strconv.ParseInt(parts[0], 10, 64)
		side := parts[1]
		f := fixtures[matchID]
		club := clubs[normalizeCaptainCSVClubKey(f.HomeClub)]
		club.Sources[side] = append(club.Sources[side], pitchSourceValue{Vector: value.vector, Reports: 1})
	}
	return rows.Err()
}

func (s *Server) addUmpirePitchSources(ctx context.Context, from, to time.Time, fixtures map[int64]pitchFixture, clubs map[string]*pitchClubAggregate) error {
	rows, err := s.DB.Query(ctx, `SELECT play_cricket_match_id,unevenness_mark,seam_mark,carry_mark,turn_mark FROM umpire_pitch_reports WHERE match_date BETWEEN $1 AND $2`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	type acc struct {
		vector pitchVector
		count  int
	}
	byFixture := map[int64]acc{}
	for rows.Next() {
		var id int64
		var u, se, c, t float64
		if err := rows.Scan(&id, &u, &se, &c, &t); err != nil {
			return err
		}
		if _, ok := fixtures[id]; !ok {
			continue
		}
		a := byFixture[id]
		a.vector = a.vector.add(pitchVector{u, se, c, t})
		a.count++
		byFixture[id] = a
	}
	for id, a := range byFixture {
		f := fixtures[id]
		club := clubs[normalizeCaptainCSVClubKey(f.HomeClub)]
		club.Sources["umpire"] = append(club.Sources["umpire"], pitchSourceValue{Vector: a.vector.div(float64(a.count)), Reports: a.count})
	}
	return rows.Err()
}

func averagePitchSource(values []pitchSourceValue) (pitchVector, bool, int, int) {
	if len(values) == 0 {
		return pitchVector{}, false, 0, 0
	}
	total := pitchVector{}
	reports := 0
	for _, v := range values {
		total = total.add(v.Vector)
		reports += v.Reports
	}
	return total.div(float64(len(values))), true, len(values), reports
}

func pitchVectorCells(v pitchVector, ok bool) []string {
	if !ok {
		return []string{"", "", "", "", ""}
	}
	return []string{format2(v.Uneven), format2(v.Seam), format2(v.Carry), format2(v.Turn), format2(v.overall())}
}

func format2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func safeCSVCell(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}
