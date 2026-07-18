package httpserver

import (
	"bytes"
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
		fmt.Fprintf(w, `<main class="container py-4"><h1 class="h2">Historical sanctions staging</h1><p class="text-muted">Uploads are stored unchanged and hashed. Rows remain pending until reconciled; this screen never guesses team, player, or rule mappings.</p><form method="POST" action="/admin/cases/imports" enctype="multipart/form-data" class="card mb-4"><input type="hidden" name="csrf_token" value="%s"><div class="card-body row g-3"><div class="col-md-4"><label class="form-label">Source name</label><input class="form-control" name="source_name" required placeholder="Google penalties sheet"></div><div class="col-md-4"><label class="form-label">Source URL</label><input class="form-control" type="url" name="source_url"></div><div class="col-md-4"><label class="form-label">CSV export</label><input class="form-control" type="file" name="file" accept=".csv,text/csv" required></div></div><div class="card-footer"><button class="btn btn-primary">Store and stage rows</button></div></form><table class="table"><thead><tr><th>ID</th><th>Source</th><th>File</th><th>SHA-256</th><th>Rows</th><th>Exceptions</th><th>Status</th><th>Uploaded</th></tr></thead><tbody>`, csrf)
		for rows.Next() {
			var id, rowsN, exceptions int64
			var source, file, sum, status string
			var at time.Time
			if rows.Scan(&id, &source, &file, &sum, &status, &at, &rowsN, &exceptions) == nil {
				fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%s</td><td><code>%s</code></td><td>%d</td><td>%d</td><td>%s</td><td>%s</td></tr>`, id, escapeHTML(source), escapeHTML(file), escapeHTML(sum[:minInt(12, len(sum))]), rowsN, exceptions, escapeHTML(status), at.In(s.LondonLoc).Format("02 Jan 2006 15:04"))
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
		sum := sha256.Sum256(data)
		sumText := hex.EncodeToString(sum[:])
		if err = os.MkdirAll(sanctionImportDir(), 0700); err != nil {
			http.Error(w, "import storage unavailable", 500)
			return
		}
		storageKey := sumText + ".csv"
		path := filepath.Join(sanctionImportDir(), storageKey)
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			if err = os.WriteFile(path, data, 0600); err != nil {
				http.Error(w, "could not store source export", 500)
				return
			}
		}
		reader := csv.NewReader(bytes.NewReader(data))
		reader.FieldsPerRecord = -1
		records, err := reader.ReadAll()
		if err != nil || len(records) < 1 {
			http.Error(w, "invalid or empty CSV", 400)
			return
		}
		headers := records[0]
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
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			http.Error(w, "staging unavailable", 500)
			return
		}
		defer tx.Rollback(r.Context())
		sess, _ := getAdminSessionFromRequest(r)
		var admin any
		if sess != nil {
			admin = sess.AdminID
		}
		var batchID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO sanction_import_batches(source_name,source_url,original_filename,storage_key,byte_size,sha256,extracted_at,imported_by_admin_id) VALUES($1,$2,$3,$4,$5,$6,now(),$7) ON CONFLICT(source_name,sha256) DO UPDATE SET source_url=EXCLUDED.source_url RETURNING id`, source, nullIfEmptyHTTP(r.FormValue("source_url")), filepath.Base(header.Filename), storageKey, len(data), sumText, admin).Scan(&batchID)
		if err != nil {
			http.Error(w, "could not create batch", 500)
			return
		}
		for rowIndex, row := range records[1:] {
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
			scanErr := tx.QueryRow(r.Context(), `INSERT INTO sanction_import_rows(batch_id,row_number,raw_data,raw_sha256) VALUES($1,$2,$3,$4) ON CONFLICT(batch_id,row_number) DO NOTHING RETURNING id`, batchID, rowIndex+2, raw, hex.EncodeToString(rowHash[:])).Scan(&rowID)
			if scanErr != nil {
				scanErr = tx.QueryRow(r.Context(), `SELECT id FROM sanction_import_rows WHERE batch_id=$1 AND row_number=$2`, batchID, rowIndex+2).Scan(&rowID)
			}
			if scanErr != nil {
				http.Error(w, "could not stage rows", 500)
				return
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO sanction_import_mappings(import_row_id) VALUES($1) ON CONFLICT DO NOTHING`, rowID)
			if err != nil {
				http.Error(w, "could not stage mappings", 500)
				return
			}
		}
		if err = tx.Commit(r.Context()); err != nil {
			http.Error(w, "could not commit import", 500)
			return
		}
		http.Redirect(w, r, "/admin/cases/imports", 303)
	}
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
