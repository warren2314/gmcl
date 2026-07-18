package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
)

const liveSanctionsRegisterURL = "https://www.gtrmcrcricket.co.uk/pages/live-disciplinary-bans-penalties"

type liveSanctionFeed struct {
	Name     string
	URL      string
	Filename string
}

var liveSanctionFeeds = []liveSanctionFeed{
	{
		Name:     "Published live individual bans (club order)",
		URL:      "https://docs.google.com/spreadsheets/d/e/2PACX-1vRfbJQCWcNGs8jOntQbFfSch4s2BPqezlDdTZJbh_2lHNm_SvzkReb7SkWzgq0N8SIadgq3SgOwOPdu/pub?gid=826530816&single=true&output=csv",
		Filename: "live-individual-bans.csv",
	},
	{
		Name:     "Published live team card register",
		URL:      "https://docs.google.com/spreadsheets/d/e/2PACX-1vRRjFep1piIz069oCqkNUHFN8bY7Caxz_qruLfJxUsehoRhykakNkDNGMKvgA-Wc3gDO8DiKVeiw7UX/pub?gid=929509918&single=true&output=csv",
		Filename: "live-team-card-register.csv",
	},
	{
		Name:     "Published live team and cup bans",
		URL:      "https://docs.google.com/spreadsheets/d/e/2PACX-1vRhGbPCfC3oNYwL-dcgtYsE2CBah_uS-venNG8FCTPnqR-FyLygOQ7OCF-IgANbC6i9g5pCSKTFMhtN/pub?gid=568312690&single=true&output=csv",
		Filename: "live-team-cup-bans.csv",
	},
}

func sanctionImportDir() string {
	if v := strings.TrimSpace(os.Getenv("SANCTIONS_IMPORT_DIR")); v != "" {
		return v
	}
	return filepath.Join("data", "sanction-imports")
}

func (s *Server) handleAdminSanctionImports() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.DB.Query(r.Context(), `SELECT b.id,b.source_name,b.original_filename,b.sha256,b.status,b.created_at,COUNT(ir.id),COUNT(*) FILTER(WHERE m.status='exception') FROM sanction_import_batches b LEFT JOIN sanction_import_rows ir ON ir.batch_id=b.id LEFT JOIN sanction_import_mappings m ON m.import_row_id=ir.id GROUP BY b.id ORDER BY b.id DESC`)
		if err != nil {
			http.Error(w, "imports unavailable", 500)
			return
		}
		defer rows.Close()
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "Sanctions history imports")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		fmt.Fprintf(w, `<main class="container py-4"><h1 class="h2">Historical sanctions staging</h1><p class="text-muted">Uploads are stored unchanged and hashed. Rows remain pending until reconciled; this screen never guesses team, player, or rule mappings.</p>`)
		if r.URL.Query().Get("live_imported") != "" {
			fmt.Fprintf(w, `<div class="alert alert-success">The current published register was fetched and staged. Identical source snapshots were reused, so running this again will not duplicate rows.</div>`)
		}
		fmt.Fprintf(w, `<form method="POST" action="/admin/cases/imports/live" class="card mb-4"><input type="hidden" name="csrf_token" value="%s"><div class="card-body"><h2 class="h5">Import current published register</h2><p class="mb-0">Fetches the three unique public datasets from <a href="%s" target="_blank" rel="noopener">the existing live bans page</a>: individual bans, team cards, and team/cup bans. The duplicate offence-order view is intentionally excluded.</p></div><div class="card-footer"><button class="btn btn-primary">Fetch and stage live register</button></div></form><form method="POST" action="/admin/cases/imports" enctype="multipart/form-data" class="card mb-4"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-12"><h2 class="h5 mb-0">Upload another source export</h2></div><div class="col-md-4"><label class="form-label">Source name</label><input class="form-control" name="source_name" required placeholder="Google penalties sheet"></div><div class="col-md-4"><label class="form-label">Source URL</label><input class="form-control" type="url" name="source_url"></div><div class="col-md-4"><label class="form-label">CSV export</label><input class="form-control" type="file" name="file" accept=".csv,text/csv" required></div></div><div class="card-footer"><button class="btn btn-outline-primary">Store and stage uploaded rows</button></div></form><div class="alert alert-info"><strong>Staged rows are not public yet.</strong> Use <em>Review &amp; apply</em> for each batch below to create the published sanctions. Rows that cannot be matched remain as exceptions for correction.</div><table class="table"><thead><tr><th>ID</th><th>Source</th><th>File</th><th>SHA-256</th><th>Rows</th><th>Exceptions</th><th>Status</th><th>Uploaded</th><th>Action</th></tr></thead><tbody>`, csrf, liveSanctionsRegisterURL, csrf)
		for rows.Next() {
			var id, rowsN, exceptions int64
			var source, file, sum, status string
			var at time.Time
			if rows.Scan(&id, &source, &file, &sum, &status, &at, &rowsN, &exceptions) == nil {
				fmt.Fprintf(w, `<tr><td><a href="/admin/cases/imports/%d">%d</a></td><td><a href="/admin/cases/imports/%d">%s</a></td><td>%s</td><td><code>%s</code></td><td>%d</td><td>%d</td><td>%s</td><td>%s</td><td><a class="btn btn-sm btn-primary text-nowrap" href="/admin/cases/imports/%d">Review &amp; apply</a></td></tr>`, id, id, id, escapeHTML(source), escapeHTML(file), escapeHTML(sum[:minInt(12, len(sum))]), rowsN, exceptions, escapeHTML(status), at.In(s.LondonLoc).Format("02 Jan 2006 15:04"), id)
			}
		}
		fmt.Fprint(w, `</tbody></table></main>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSanctionImportStage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(50 << 20); err != nil {
			http.Error(w, "invalid or oversized upload", 400)
			return
		}
		source := strings.TrimSpace(r.FormValue("source_name"))
		if source == "" {
			http.Error(w, "source name required", 400)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "CSV required", 400)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, (50<<20)+1))
		if err != nil || len(data) > 50<<20 {
			http.Error(w, "CSV exceeds 50 MB", 400)
			return
		}
		if _, _, err = parseSanctionImportCSV(data); err != nil {
			http.Error(w, "invalid or empty CSV", 400)
			return
		}
		sess, _ := getAdminSessionFromRequest(r)
		var admin any
		if sess != nil {
			admin = sess.AdminID
		}
		if _, err = s.stageSanctionImport(r.Context(), source, r.FormValue("source_url"), header.Filename, data, admin); err != nil {
			http.Error(w, "could not stage import", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/imports", 303)
	}
}

func (s *Server) handleAdminSanctionLiveImport() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, _ := getAdminSessionFromRequest(r)
		var admin any
		if sess != nil {
			admin = sess.AdminID
		}
		client := &http.Client{Timeout: 30 * time.Second}
		for _, feed := range liveSanctionFeeds {
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, feed.URL, nil)
			if err != nil {
				http.Error(w, "could not prepare live register import", 500)
				return
			}
			req.Header.Set("User-Agent", "GMCL-Sanctions-Importer/1.0")
			resp, err := client.Do(req)
			if err != nil {
				http.Error(w, "could not fetch the published live register", 502)
				return
			}
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, (50<<20)+1))
			resp.Body.Close()
			if readErr != nil || resp.StatusCode != http.StatusOK || len(data) > 50<<20 {
				http.Error(w, "published live register returned an invalid response", 502)
				return
			}
			if _, _, err = parseSanctionImportCSV(data); err != nil {
				http.Error(w, "published live register did not return valid CSV", 502)
				return
			}
			if _, err = s.stageSanctionImport(r.Context(), feed.Name, feed.URL, feed.Filename, data, admin); err != nil {
				http.Error(w, "could not stage the published live register", 500)
				return
			}
		}
		http.Redirect(w, r, "/admin/cases/imports?live_imported=1", http.StatusSeeOther)
	}
}

func parseSanctionImportCSV(data []byte) ([]string, [][]string, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil || len(records) < 1 {
		return nil, nil, fmt.Errorf("invalid or empty CSV")
	}
	headers := append([]string(nil), records[0]...)
	seen := map[string]int{}
	for i, h := range headers {
		h = strings.TrimSpace(h)
		if h == "" {
			h = "column_" + strconv.Itoa(i+1)
		}
		seen[h]++
		if seen[h] > 1 {
			h = h + "_" + strconv.Itoa(seen[h])
		}
		headers[i] = h
	}
	return headers, records[1:], nil
}

func (s *Server) stageSanctionImport(ctx context.Context, source, sourceURL, filename string, data []byte, admin any) (int64, error) {
	headers, records, err := parseSanctionImportCSV(data)
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256(data)
	sumText := hex.EncodeToString(sum[:])
	if err = os.MkdirAll(sanctionImportDir(), 0700); err != nil {
		return 0, err
	}
	storageKey := sumText + ".csv"
	path := filepath.Join(sanctionImportDir(), storageKey)
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err = os.WriteFile(path, data, 0600); err != nil {
			return 0, err
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var batchID int64
	err = tx.QueryRow(ctx, `INSERT INTO sanction_import_batches(source_name,source_url,original_filename,storage_key,byte_size,sha256,extracted_at,imported_by_admin_id) VALUES($1,$2,$3,$4,$5,$6,now(),$7) ON CONFLICT(source_name,sha256) DO UPDATE SET source_url=EXCLUDED.source_url RETURNING id`, source, nullIfEmptyHTTP(sourceURL), filepath.Base(filename), storageKey, len(data), sumText, admin).Scan(&batchID)
	if err != nil {
		return 0, err
	}
	for rowIndex, row := range records {
		obj := map[string]string{}
		for i, h := range headers {
			if i < len(row) {
				obj[h] = row[i]
			} else {
				obj[h] = ""
			}
		}
		raw, _ := json.Marshal(obj)
		rowHash := sha256.Sum256(raw)
		var rowID int64
		scanErr := tx.QueryRow(ctx, `INSERT INTO sanction_import_rows(batch_id,row_number,raw_data,raw_sha256) VALUES($1,$2,$3,$4) ON CONFLICT(batch_id,row_number) DO NOTHING RETURNING id`, batchID, rowIndex+2, raw, hex.EncodeToString(rowHash[:])).Scan(&rowID)
		if scanErr != nil {
			scanErr = tx.QueryRow(ctx, `SELECT id FROM sanction_import_rows WHERE batch_id=$1 AND row_number=$2`, batchID, rowIndex+2).Scan(&rowID)
		}
		if scanErr != nil {
			return 0, scanErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO sanction_import_mappings(import_row_id) VALUES($1) ON CONFLICT DO NOTHING`, rowID); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return batchID, nil
}

func nullIfEmptyHTTP(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
