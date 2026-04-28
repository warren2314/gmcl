package httpserver

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// legacySubmissionRow holds one parsed row from the legacy Google-Forms CSV export.
type legacySubmissionRow struct {
	RowNum         int
	MatchDate      time.Time
	ClubName       string
	CaptainName    string
	CaptainEmail   string
	Umpire1Name    string
	Umpire2Name    string
	TeamName       string
	U1Toss         string
	U2Toss         string
	DM1, MM1, PM1, PI1, TW1 int
	U1Reason       string
	DM2, MM2, PM2, PI2, TW2 int
	U2Reason       string
	Unevenness     int
	Seam           int
	Carry          int
	Turn           int
	// resolved
	TeamID    int32
	CaptainID int32
	WeekID    int32
	SeasonID  int32
	Status    string // "ok" | "no_club" | "no_week" | "duplicate" | "error"
	StatusMsg string
}

func parseLegacyPitchScore(text string) int {
	t := strings.ToLower(strings.TrimSpace(text))
	if strings.Contains(t, "unfit") {
		return 6
	}
	switch {
	// Unevenness / seam / carry / turn — detect by leading keywords
	case strings.HasPrefix(t, "no unevenness") || strings.HasPrefix(t, "no uneveness"):
		return 1
	case strings.HasPrefix(t, "little unevenness"):
		return 2
	case strings.HasPrefix(t, "occasional, at most, unevenness"):
		return 3
	case strings.HasPrefix(t, "more than occasional, at most, unevenness"):
		return 4
	case strings.HasPrefix(t, "excessive unevenness"):
		return 5

	case strings.HasPrefix(t, "limited seam movement, at most"):
		return 1
	case strings.HasPrefix(t, "limited seam movement at all"):
		return 2
	case strings.HasPrefix(t, "occasional seam"):
		return 3
	case strings.HasPrefix(t, "more than occasional seam"):
		return 4
	case strings.HasPrefix(t, "excessive seam"):
		return 5

	case strings.HasPrefix(t, "good carry"):
		return 1
	case strings.HasPrefix(t, "average carry"):
		return 2
	case strings.HasPrefix(t, "lacking in carry"):
		return 3
	case strings.HasPrefix(t, "minimal carry") && !strings.HasPrefix(t, "very minimal"):
		return 4
	case strings.HasPrefix(t, "very minimal carry"):
		return 5

	case strings.HasPrefix(t, "little or no turn"):
		return 1
	case strings.HasPrefix(t, "a little turn"):
		return 2
	case strings.HasPrefix(t, "moderate turn"):
		return 3
	case strings.HasPrefix(t, "considerable turn"):
		return 4
	case strings.HasPrefix(t, "excessive assistance"):
		return 5
	}
	return 3 // default to middle if unrecognised
}

func titleCaseName(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, " ")
}

func umpireAvgPerformance(scores ...int) string {
	if len(scores) == 0 {
		return "Average"
	}
	total := 0
	for _, v := range scores {
		total += v
	}
	avg := float64(total) / float64(len(scores))
	switch {
	case avg >= 4:
		return "Good"
	case avg >= 3:
		return "Average"
	default:
		return "Poor"
	}
}

func parseLegacyCSV(records [][]string) ([]legacySubmissionRow, error) {
	var rows []legacySubmissionRow
	for i, rec := range records {
		if len(rec) < 26 {
			continue
		}
		// skip blank rows (empty timestamp)
		if strings.TrimSpace(rec[0]) == "" {
			continue
		}
		row := legacySubmissionRow{RowNum: i + 2} // +2: 1-based + header
		// Date (DD/MM/YYYY)
		d, err := time.Parse("02/01/2006", strings.TrimSpace(rec[1]))
		if err != nil {
			// try timestamp column
			d, err = time.Parse("02/01/2006 15:04", strings.TrimSpace(rec[0]))
			if err != nil {
				row.Status = "error"
				row.StatusMsg = "unparseable date"
				rows = append(rows, row)
				continue
			}
		}
		row.MatchDate = d.Truncate(24 * time.Hour)
		row.ClubName = strings.TrimSpace(rec[2])
		row.CaptainEmail = strings.TrimSpace(rec[3])
		row.CaptainName = strings.TrimSpace(rec[4])
		row.Umpire1Name = titleCaseName(strings.TrimSpace(rec[5]))
		row.Umpire2Name = titleCaseName(strings.TrimSpace(rec[6]))
		row.TeamName = strings.TrimSpace(rec[7])
		row.U1Toss = strings.TrimSpace(rec[8])
		row.U2Toss = strings.TrimSpace(rec[9])

		parseInt := func(s string) int {
			v, _ := strconv.Atoi(strings.TrimSpace(s))
			return v
		}
		row.DM1 = parseInt(rec[10])
		row.MM1 = parseInt(rec[11])
		row.PM1 = parseInt(rec[12])
		row.PI1 = parseInt(rec[13])
		row.TW1 = parseInt(rec[14])
		row.U1Reason = strings.TrimSpace(rec[15])
		row.DM2 = parseInt(rec[16])
		row.MM2 = parseInt(rec[17])
		row.PM2 = parseInt(rec[18])
		row.PI2 = parseInt(rec[19])
		row.TW2 = parseInt(rec[20])
		row.U2Reason = strings.TrimSpace(rec[21])
		row.Unevenness = parseLegacyPitchScore(rec[22])
		row.Seam = parseLegacyPitchScore(rec[23])
		row.Carry = parseLegacyPitchScore(rec[24])
		row.Turn = parseLegacyPitchScore(rec[25])

		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Server) resolveLegacyRows(ctx context.Context, rows []legacySubmissionRow) []legacySubmissionRow {
	resolver, err := newCaptainCSVResolver(ctx, s)
	if err != nil {
		for i := range rows {
			rows[i].Status = "error"
			rows[i].StatusMsg = "resolver init failed: " + err.Error()
		}
		return rows
	}

	for i := range rows {
		row := &rows[i]
		if row.Status == "error" {
			continue
		}

		// Resolve club using the same fuzzy logic as the captain CSV uploader.
		club, ok := resolver.resolveClub(row.ClubName)
		if !ok {
			row.Status = "no_club"
			row.StatusMsg = "club not found: " + row.ClubName
			continue
		}

		// Resolve team within that club; fall back to 1st XI, then any team.
		var teamID int32
		tr, hasTeams := resolver.teamsByClubID[club.ID]
		if !hasTeams {
			row.Status = "no_club"
			row.StatusMsg = "no teams found for club: " + club.Name
			continue
		}
		team, found := resolveCaptainCSVTeam(tr, row.TeamName)
		if !found {
			// Try canonical 1st XI key
			if ts := tr.byCanonicalKey[normalizeCaptainCSVTeamKey("1st XI")]; len(ts) > 0 {
				team, found = ts[0], true
			}
		}
		if !found {
			// Pick any team
			for _, t := range tr.byExactKey {
				team, found = t, true
				break
			}
		}
		if !found {
			row.Status = "no_club"
			row.StatusMsg = "no team resolved for: " + club.Name + " / " + row.TeamName
			continue
		}
		teamID = team.ID
		row.TeamID = teamID

		var captainID int32
		var weekID, seasonID int32

		// Active captain for this team
		err = s.DB.QueryRow(ctx, `
			SELECT id FROM captains
			WHERE team_id = $1
			  AND (active_to IS NULL OR active_to >= $2)
			ORDER BY active_from DESC LIMIT 1
		`, teamID, row.MatchDate).Scan(&captainID)
		if err != nil {
			// no captain on record — use 0 sentinel, still importable
			captainID = 0
		}
		row.CaptainID = captainID

		// Week lookup
		err = s.DB.QueryRow(ctx, `
			SELECT w.id, w.season_id FROM weeks w
			WHERE $1 BETWEEN w.start_date AND w.end_date
			LIMIT 1
		`, row.MatchDate.Format("2006-01-02")).Scan(&weekID, &seasonID)
		if err == pgx.ErrNoRows {
			row.Status = "no_week"
			row.StatusMsg = fmt.Sprintf("no week covers %s", row.MatchDate.Format("2 Jan 2006"))
			continue
		} else if err != nil {
			row.Status = "error"
			row.StatusMsg = err.Error()
			continue
		}
		row.WeekID = weekID
		row.SeasonID = seasonID

		// Duplicate check
		var exists bool
		_ = s.DB.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM submissions
				WHERE team_id = $1 AND match_date = $2
			)
		`, teamID, row.MatchDate.Format("2006-01-02")).Scan(&exists)
		if exists {
			row.Status = "duplicate"
			row.StatusMsg = "submission already exists for this team and date"
			continue
		}

		row.Status = "ok"
	}
	return rows
}

func buildLegacyFormData(row legacySubmissionRow) map[string]any {
	u1type := "panel"
	if strings.Contains(strings.ToLower(row.Umpire1Name), "not listed") {
		u1type = "club"
	}
	u2type := "panel"
	if strings.Contains(strings.ToLower(row.Umpire2Name), "not listed") {
		u2type = "club"
	}
	comments := buildCombinedUmpireComments(row.U1Reason, row.U2Reason)
	return map[string]any{
		"match_date":                    row.MatchDate.Format("2006-01-02"),
		"match_outcome":                 "played",
		"your_team":                     row.TeamName,
		"umpire1_name":                  row.Umpire1Name,
		"umpire2_name":                  row.Umpire2Name,
		"umpire1_type":                  u1type,
		"umpire2_type":                  u2type,
		"umpire1_toss_attended":         row.U1Toss,
		"umpire2_toss_attended":         row.U2Toss,
		"decision_making_umpire1":       strconv.Itoa(row.DM1),
		"match_management_umpire1":      strconv.Itoa(row.MM1),
		"player_management_umpire1":     strconv.Itoa(row.PM1),
		"presence_image_umpire1":        strconv.Itoa(row.PI1),
		"teamwork_umpire1":              strconv.Itoa(row.TW1),
		"umpire1_reason":                row.U1Reason,
		"decision_making_umpire2":       strconv.Itoa(row.DM2),
		"match_management_umpire2":      strconv.Itoa(row.MM2),
		"player_management_umpire2":     strconv.Itoa(row.PM2),
		"presence_image_umpire2":        strconv.Itoa(row.PI2),
		"teamwork_umpire2":              strconv.Itoa(row.TW2),
		"umpire2_reason":                row.U2Reason,
		"umpire1_performance":           umpireAvgPerformance(row.DM1, row.MM1, row.PM1, row.PI1, row.TW1),
		"umpire2_performance":           umpireAvgPerformance(row.DM2, row.MM2, row.PM2, row.PI2, row.TW2),
		"umpire_comments":               comments,
		"unevenness_of_bounce":          strconv.Itoa(row.Unevenness),
		"seam_movement":                 strconv.Itoa(row.Seam),
		"carry_bounce":                  strconv.Itoa(row.Carry),
		"turn":                          strconv.Itoa(row.Turn),
		"import_source":                 "legacy_csv",
	}
}

func derivePitchRatings(unevenness, seam, carry, turn int) (pitch, outfield, facilities int) {
	clamp := func(v int) int {
		if v < 1 {
			return 1
		}
		if v > 5 {
			return 5
		}
		return v
	}
	pitch = clamp(7 - unevenness)
	outfield = clamp(7 - seam)
	facilities = clamp((7 - carry + 7 - turn) / 2)
	return
}

func (s *Server) handleAdminSubmissionImportGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := ""
		if c, err := r.Cookie("csrf_token"); err == nil {
			csrfToken = c.Value
		}
		pageHead(w, "Import Submissions")
		writeAdminNav(w, csrfToken, "/admin/submissions/import")
		fmt.Fprint(w, `<div class="container-fluid px-4">
<h4 class="mb-4">Import Legacy Submissions (CSV)</h4>
<div class="card shadow-sm mb-4">
  <div class="card-body">
    <p class="text-muted">Upload the CSV export from the old captain report Google Form. A preview will be shown before anything is saved.</p>
    <form method="POST" action="/admin/submissions/import/preview" enctype="multipart/form-data">
      <input type="hidden" name="csrf_token" value="`+csrfToken+`">
      <div class="mb-3">
        <input type="file" class="form-control" name="csv_file" accept=".csv" required>
      </div>
      <button class="btn btn-gmcl" type="submit">Upload &amp; Preview</button>
    </form>
  </div>
</div>
</div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSubmissionImportPreview() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("csv_file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer f.Close()

		reader := csv.NewReader(f)
		reader.LazyQuotes = true
		reader.FieldsPerRecord = -1
		allRecords, err := reader.ReadAll()
		if err != nil {
			http.Error(w, "csv parse error: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(allRecords) < 2 {
			http.Error(w, "CSV is empty", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		rows, err := parseLegacyCSV(allRecords[1:]) // skip header
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rows = s.resolveLegacyRows(ctx, rows)

		// Serialise raw CSV (excluding header) for the apply step
		rawJSON, _ := json.Marshal(allRecords[1:])

		csrfToken := ""
		if c, err2 := r.Cookie("csrf_token"); err2 == nil {
			csrfToken = c.Value
		}

		okCount := 0
		for _, row := range rows {
			if row.Status == "ok" {
				okCount++
			}
		}

		pageHead(w, "Import Preview")
		writeAdminNav(w, csrfToken, "/admin/submissions/import")
		fmt.Fprintf(w, `<div class="container-fluid px-4">
<h4 class="mb-3">Import Preview — %d rows, %d ready to import</h4>`, len(rows), okCount)

		if okCount > 0 {
			fmt.Fprintf(w, `
<form method="POST" action="/admin/submissions/import/apply">
  <input type="hidden" name="csrf_token" value="%s">
  <input type="hidden" name="raw_csv" value="%s">
  <button class="btn btn-success mb-3" type="submit">Import %d submissions</button>
</form>`, csrfToken, escapeHTML(string(rawJSON)), okCount)
		}

		fmt.Fprint(w, `<div class="table-responsive"><table class="table table-sm table-bordered small">
<thead class="table-light"><tr>
  <th>#</th><th>Date</th><th>Club</th><th>Captain</th><th>Umpires</th><th>Status</th>
</tr></thead><tbody>`)

		for _, row := range rows {
			badgeClass := "bg-success"
			statusText := "Ready"
			switch row.Status {
			case "duplicate":
				badgeClass = "bg-secondary"
				statusText = "Duplicate"
			case "no_club":
				badgeClass = "bg-danger"
				statusText = "Club not found"
			case "no_week":
				badgeClass = "bg-warning text-dark"
				statusText = "No week"
			case "error":
				badgeClass = "bg-danger"
				statusText = "Error"
			}
			detail := ""
			if row.StatusMsg != "" {
				detail = fmt.Sprintf(`<br><span class="text-muted">%s</span>`, escapeHTML(row.StatusMsg))
			}
			fmt.Fprintf(w, `<tr>
  <td>%d</td>
  <td class="text-nowrap">%s</td>
  <td>%s</td>
  <td>%s</td>
  <td>%s / %s</td>
  <td><span class="badge %s">%s</span>%s</td>
</tr>`, row.RowNum, row.MatchDate.Format("2 Jan 2006"),
				escapeHTML(row.ClubName), escapeHTML(row.CaptainName),
				escapeHTML(row.Umpire1Name), escapeHTML(row.Umpire2Name),
				badgeClass, statusText, detail)
		}
		fmt.Fprint(w, `</tbody></table></div></div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminSubmissionImportApply() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawJSON := r.FormValue("raw_csv")
		if rawJSON == "" {
			http.Error(w, "missing data", http.StatusBadRequest)
			return
		}
		var allRecords [][]string
		if err := json.Unmarshal([]byte(rawJSON), &allRecords); err != nil {
			http.Error(w, "invalid data", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		rows, _ := parseLegacyCSV(allRecords)
		rows = s.resolveLegacyRows(ctx, rows)

		inserted := 0
		skipped := 0
		for _, row := range rows {
			if row.Status != "ok" {
				skipped++
				continue
			}
			if row.CaptainID == 0 {
				// try to find captain by name from CSV
				_ = s.DB.QueryRow(ctx, `
					SELECT id FROM captains
					WHERE team_id = $1
					ORDER BY active_from DESC LIMIT 1
				`, row.TeamID).Scan(&row.CaptainID)
			}
			if row.CaptainID == 0 {
				log.Printf("[import] row %d: no captain for team %d, skipping", row.RowNum, row.TeamID)
				skipped++
				continue
			}

			fd := buildLegacyFormData(row)
			formDataJSON, _ := json.Marshal(fd)
			pitch, outfield, facilities := derivePitchRatings(row.Unevenness, row.Seam, row.Carry, row.Turn)
			comments := buildCombinedUmpireComments(row.U1Reason, row.U2Reason)

			u1type := "panel"
			if strings.Contains(strings.ToLower(row.Umpire1Name), "not listed") {
				u1type = "club"
			}
			u2type := "panel"
			if strings.Contains(strings.ToLower(row.Umpire2Name), "not listed") {
				u2type = "club"
			}

			_, err := s.DB.Exec(ctx, `
				INSERT INTO submissions (
					season_id, week_id, team_id, captain_id,
					match_date, pitch_rating, outfield_rating, facilities_rating,
					comments, umpire1_type, umpire2_type,
					submitted_by_name, submitted_by_email, submitted_by_role,
					form_data, submitted_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'captain',$14,now())
			`,
				row.SeasonID, row.WeekID, row.TeamID, row.CaptainID,
				row.MatchDate.Format("2006-01-02"),
				pitch, outfield, facilities,
				comments, u1type, u2type,
				row.CaptainName, row.CaptainEmail,
				formDataJSON,
			)
			if err != nil {
				log.Printf("[import] row %d insert error: %v", row.RowNum, err)
				skipped++
				continue
			}
			inserted++
		}

		s.audit(ctx, r, "admin", nil, "legacy_csv_import", "submission", nil, map[string]any{
			"inserted": inserted,
			"skipped":  skipped,
		})

		csrfToken := ""
		if c, err := r.Cookie("csrf_token"); err == nil {
			csrfToken = c.Value
		}
		pageHead(w, "Import Complete")
		writeAdminNav(w, csrfToken, "/admin/submissions/import")
		fmt.Fprintf(w, `<div class="container-fluid px-4">
<div class="alert alert-success mt-4">
  <strong>Import complete.</strong> %d submissions inserted, %d skipped.
</div>
<a href="/admin/weeks" class="btn btn-outline-secondary">View Weeks</a>
</div>`, inserted, skipped)
		pageFooter(w)
	}
}
