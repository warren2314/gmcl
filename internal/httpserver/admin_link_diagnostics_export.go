package httpserver

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleAdminLinkDiagnosticsExportCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, now, ok := s.loadLinkDiagnosticsExportData(w, r)
		if !ok {
			return
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+linkDiagExportFilename("csv", data.Query, now)+`"`)
		w.Header().Set("Cache-Control", "no-store")

		cw := csv.NewWriter(w)
		writeLinkDiagnosticsCSV(cw, data, now)
		cw.Flush()
	}
}

func (s *Server) handleAdminLinkDiagnosticsExportPDF() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, now, ok := s.loadLinkDiagnosticsExportData(w, r)
		if !ok {
			return
		}

		pdf := buildLinkDiagnosticsPDF(data, now)
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="`+linkDiagExportFilename("pdf", data.Query, now)+`"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(pdf)
	}
}

func (s *Server) loadLinkDiagnosticsExportData(w http.ResponseWriter, r *http.Request) (linkDiagPageData, time.Time, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		query = strings.TrimSpace(r.URL.Query().Get("email"))
	}
	if query == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return linkDiagPageData{}, time.Time{}, false
	}

	data := linkDiagPageData{Query: query}
	if err := s.loadLinkDiagnostics(ctx, &data); err != nil {
		http.Error(w, "could not load diagnostics", http.StatusInternalServerError)
		return linkDiagPageData{}, time.Time{}, false
	}
	if len(data.Captains) == 0 {
		http.Error(w, "no matching captain/team records found", http.StatusNotFound)
		return linkDiagPageData{}, time.Time{}, false
	}

	now := time.Now()
	if s.LondonLoc != nil {
		now = now.In(s.LondonLoc)
	}
	return data, now, true
}

func linkDiagExportButtons(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	q := url.QueryEscape(query)
	return fmt.Sprintf(`<div class="col-auto"><a class="btn btn-outline-primary" href="/admin/link-diagnostics/export.csv?q=%s">Export CSV</a></div><div class="col-auto"><a class="btn btn-outline-danger" href="/admin/link-diagnostics/export.pdf?q=%s">Download PDF</a></div>`, q, q)
}

func linkDiagExportFilename(ext, query string, now time.Time) string {
	return fmt.Sprintf("gmcl-link-diagnostics-%s-%s.%s",
		adminSubmissionsExportSlug(query),
		now.Format("20060102-150405"),
		ext)
}

func writeLinkDiagnosticsCSV(cw *csv.Writer, data linkDiagPageData, generatedAt time.Time) {
	writeCSVSection(cw, "Report", []string{"Query", "Generated"}, [][]string{{
		data.Query,
		generatedAt.Format("2006-01-02 15:04"),
	}})

	missing, submitted, resolved, byes := linkDiagFixtureCounts(data.Fixtures)
	yellows, reds := linkDiagSanctionCounts(data.Sanctions)
	countByEvent := linkDiagEventActionCounts(data.Events, data.AuditRows)
	writeCSVSection(cw, "Evidence Summary", []string{"Yellow cards", "Red cards", "Missing fixtures", "Submitted fixtures", "Admin resolved", "Byes", "SES clicks", "App link landings", "Confirmed links", "Form opens", "Autosaves", "Submissions"}, [][]string{{
		strconv.Itoa(yellows),
		strconv.Itoa(reds),
		strconv.Itoa(missing),
		strconv.Itoa(submitted),
		strconv.Itoa(resolved),
		strconv.Itoa(byes),
		strconv.Itoa(countByEvent["click"]),
		strconv.Itoa(countByEvent["magic_link_clicked"]),
		strconv.Itoa(countByEvent["magic_link_redeemed"]),
		strconv.Itoa(countByEvent["captain_form_opened"]),
		strconv.Itoa(countByEvent["draft_autosaved"]),
		strconv.Itoa(countByEvent["submission_created"]),
	}})

	var captainRows [][]string
	for _, row := range data.Captains {
		captainRows = append(captainRows, []string{
			strconv.Itoa(int(row.ID)),
			strconv.Itoa(int(row.TeamID)),
			row.FullName,
			row.Email,
			row.EffectiveEmail,
			row.EmailOverride,
			row.EmailOverrideUntil,
			row.ClubName,
			row.TeamName,
			row.ActiveFrom,
			row.ActiveTo,
			strconv.FormatBool(row.IsActive),
		})
	}
	writeCSVSection(cw, "Captain Records", []string{"Captain ID", "Team ID", "Captain", "Email", "Effective email", "Override", "Override until", "Club", "Team", "Active from", "Active to", "Current"}, captainRows)

	var sanctionRows [][]string
	for _, row := range data.Sanctions {
		points := ""
		if row.PointsDeduction.Valid {
			points = strconv.Itoa(int(row.PointsDeduction.Int32))
		}
		sent := ""
		if row.EmailSentAt.Valid {
			sent = row.EmailSentAt.Time.Format("2006-01-02 15:04")
		}
		sanctionRows = append(sanctionRows, []string{
			strconv.FormatInt(row.ID, 10),
			strconv.Itoa(int(row.TeamID)),
			row.ClubName,
			row.TeamName,
			row.SeasonName,
			strconv.Itoa(int(row.WeekNumber)),
			row.MatchDate.Format("2006-01-02"),
			sanctionsExportCardLabel(row.Colour),
			reasonLabel(row.Reason),
			statusLabel(row.Status),
			sanctionsExportEmailStatusLabel(row.EmailStatus),
			points,
			row.IssuedAt.Format("2006-01-02 15:04"),
			row.IssuedBy,
			sent,
			row.Notes,
		})
	}
	writeCSVSection(cw, "Card / Sanction Records", []string{"Sanction ID", "Team ID", "Club", "Team", "Season", "Week", "Match date", "Card", "Reason", "Status", "Email status", "Points", "Issued", "Issued by", "Email sent", "Notes"}, sanctionRows)

	var fixtureRows [][]string
	for _, row := range data.Fixtures {
		status, _ := linkDiagFixtureStatus(row)
		submissionID := ""
		if row.SubmissionID.Valid {
			submissionID = strconv.FormatInt(row.SubmissionID.Int64, 10)
		}
		submittedAt := ""
		if row.SubmittedAt.Valid {
			submittedAt = row.SubmittedAt.Time.Format("2006-01-02 15:04")
		}
		fixtureRows = append(fixtureRows, []string{
			strconv.Itoa(int(row.TeamID)),
			strconv.Itoa(int(row.WeekNumber)),
			row.MatchDate.Format("2006-01-02"),
			row.ClubName,
			row.TeamName,
			strconv.FormatInt(row.PlayCricketMatchID, 10),
			row.Opponent,
			row.Ground,
			status,
			strconv.FormatBool(row.HasSubmission),
			strconv.FormatBool(row.LegacyCovered),
			submissionID,
			submittedAt,
			row.ExemptionReason,
			strconv.FormatInt(row.ReminderCount, 10),
			row.ReminderTypes,
			row.SanctionStatus,
		})
	}
	writeCSVSection(cw, "Fixture Report Evidence", []string{"Team ID", "Week", "Match date", "Club", "Team", "Play-Cricket match ID", "Opponent", "Ground", "Report status", "Has submission", "Legacy covered", "Submission ID", "Submitted", "Exemption reason", "Reminder count", "Reminder types", "Sanction status"}, fixtureRows)

	var reminderRows [][]string
	for _, row := range data.Reminders {
		reminderRows = append(reminderRows, []string{
			row.SentAt.Format("2006-01-02 15:04"),
			row.MatchDate.Format("2006-01-02"),
			row.ReminderType,
			row.Recipient,
			row.ClubName,
			row.TeamName,
			nullInt64CSV(row.TokenID),
		})
	}
	writeCSVSection(cw, "Reminder Email Sends", []string{"Sent", "Match date", "Type", "Recipient", "Club", "Team", "Token ID"}, reminderRows)

	var submissionRows [][]string
	for _, row := range data.Submits {
		submissionRows = append(submissionRows, []string{
			strconv.FormatInt(row.ID, 10),
			row.MatchDate.Format("2006-01-02"),
			row.Submitted.Format("2006-01-02 15:04"),
			row.TeamName,
		})
	}
	writeCSVSection(cw, "Submissions", []string{"Submission ID", "Match date", "Submitted", "Team"}, submissionRows)

	var tokenRows [][]string
	for _, row := range data.Tokens {
		usedAt := ""
		if row.UsedAt.Valid {
			usedAt = row.UsedAt.Time.Format("2006-01-02 15:04")
		}
		matchDate := ""
		if row.MatchDate.Valid {
			matchDate = row.MatchDate.Time.Format("2006-01-02")
		}
		tokenRows = append(tokenRows, []string{
			row.CreatedAt.Format("2006-01-02 15:04"),
			row.ExpiresAt.Format("2006-01-02 15:04"),
			usedAt,
			matchDate,
			row.Status,
			row.RequestIP,
			row.UserAgent,
		})
	}
	writeCSVSection(cw, "Magic-Link Tokens", []string{"Created", "Expires", "Used", "Match date", "Status", "Request IP", "User agent"}, tokenRows)

	var sendRows [][]string
	for _, row := range data.Sends {
		sendRows = append(sendRows, []string{
			row.CreatedAt.Format("2006-01-02 15:04"),
			row.SeasonName,
			strconv.Itoa(int(row.WeekNumber)),
			nullInt64CSV(row.TokenID),
		})
	}
	writeCSVSection(cw, "Magic-Link Send Log", []string{"Sent/logged", "Season", "Week", "Token ID"}, sendRows)

	var eventRows [][]string
	for _, row := range data.Events {
		occurredAt := ""
		if row.OccurredAt.Valid {
			occurredAt = row.OccurredAt.Time.Format("2006-01-02 15:04")
		}
		eventRows = append(eventRows, []string{
			row.CreatedAt.Format("2006-01-02 15:04"),
			occurredAt,
			row.EventType,
			row.Recipient,
			row.Subject,
			redactMagicTokenInText(row.Detail),
			redactMagicTokenInText(row.LinkURL),
			row.ClickIP,
			row.ClickUA,
		})
	}
	writeCSVSection(cw, "SES Events Stored By App", []string{"Received by app", "Event time", "Event", "Recipient", "Subject", "Detail", "Link URL", "Click IP", "Click user agent"}, eventRows)

	var auditRows [][]string
	for _, row := range data.AuditRows {
		auditRows = append(auditRows, []string{
			row.CreatedAt.Format("2006-01-02 15:04"),
			row.Action,
			redactMagicTokenInText(row.Metadata),
			row.UserAgent,
		})
	}
	writeCSVSection(cw, "Recent Audit Activity", []string{"Time", "Action", "Metadata", "User agent"}, auditRows)
}

func writeCSVSection(cw *csv.Writer, title string, header []string, rows [][]string) {
	_ = cw.Write([]string{title})
	_ = cw.Write(header)
	if len(rows) == 0 {
		_ = cw.Write([]string{"No rows"})
	} else {
		for _, row := range rows {
			_ = cw.Write(row)
		}
	}
	_ = cw.Write([]string{})
}

func nullInt64CSV(v sql.NullInt64) string {
	if !v.Valid || v.Int64 == 0 {
		return ""
	}
	return strconv.FormatInt(v.Int64, 10)
}

func linkDiagSanctionCounts(rows []linkDiagSanction) (yellows, reds int) {
	for _, row := range rows {
		switch row.Colour {
		case "red":
			reds++
		case "yellow":
			yellows++
		}
	}
	return yellows, reds
}

func linkDiagEventActionCounts(events []linkDiagEmailEvent, audits []linkDiagAudit) map[string]int {
	out := map[string]int{}
	for _, row := range events {
		out[row.EventType]++
	}
	for _, row := range audits {
		out[row.Action]++
	}
	return out
}

const (
	linkDiagPDFWidth  = 842.0
	linkDiagPDFHeight = 595.0
	linkDiagPDFMargin = 30.0
)

type linkDiagPDFRenderer struct {
	doc         *simplePDFDoc
	content     strings.Builder
	data        linkDiagPageData
	generatedAt time.Time
	pageNo      int
	y           float64
}

func buildLinkDiagnosticsPDF(data linkDiagPageData, generatedAt time.Time) []byte {
	r := &linkDiagPDFRenderer{
		doc:         newSimplePDFDoc(linkDiagPDFWidth, linkDiagPDFHeight),
		data:        data,
		generatedAt: generatedAt,
	}
	r.startPage(true)
	r.summary()
	r.captains()
	r.sanctions()
	r.fixtures()
	r.reminders()
	r.submissions()
	r.emailAndLinkActivity()
	r.audit()
	r.finishPage()
	return r.doc.bytes()
}

func (r *linkDiagPDFRenderer) startPage(first bool) {
	r.pageNo++
	r.content.Reset()
	r.y = 0
	r.fillRect(0, 0, linkDiagPDFWidth, 40, 0.76, 0.02, 0.12)
	if first {
		r.textRGB(linkDiagPDFMargin, 18, 18, "F2", "GMCL Link Diagnostics Evidence Pack", 1, 1, 1)
	} else {
		r.textRGB(linkDiagPDFMargin, 18, 13, "F2", "GMCL Link Diagnostics Evidence Pack", 1, 1, 1)
	}
	r.textRGB(linkDiagPDFWidth-230, 17, 8.5, "F1", "Generated "+r.generatedAt.Format("2 Jan 2006 15:04"), 1, 1, 1)
	r.textRGB(linkDiagPDFWidth-230, 29, 8.5, "F1", "Search: "+r.data.Query, 1, 1, 1)
	r.y = 58
}

func (r *linkDiagPDFRenderer) finishPage() {
	r.line(linkDiagPDFMargin, linkDiagPDFHeight-27, linkDiagPDFWidth-linkDiagPDFMargin, linkDiagPDFHeight-27, 0.82)
	r.text(linkDiagPDFMargin, linkDiagPDFHeight-14, 8, "F1", "GMCL administration evidence pack")
	r.text(linkDiagPDFWidth-linkDiagPDFMargin-45, linkDiagPDFHeight-14, 8, "F1", fmt.Sprintf("Page %d", r.pageNo))
	r.doc.addPage(r.content.String())
}

func (r *linkDiagPDFRenderer) newPage() {
	r.finishPage()
	r.startPage(false)
}

func (r *linkDiagPDFRenderer) ensureSpace(height float64) {
	if r.y+height > linkDiagPDFHeight-linkDiagPDFMargin-26 {
		r.newPage()
	}
}

func (r *linkDiagPDFRenderer) summary() {
	yellows, reds := linkDiagSanctionCounts(r.data.Sanctions)
	missing, submitted, resolved, byes := linkDiagFixtureCounts(r.data.Fixtures)
	counts := linkDiagEventActionCounts(r.data.Events, r.data.AuditRows)

	r.sectionTitle("Evidence Summary")
	items := []struct {
		label string
		value string
		fill  []float64
	}{
		{"Yellow cards", strconv.Itoa(yellows), []float64{1.0, 0.94, 0.67}},
		{"Red cards", strconv.Itoa(reds), []float64{1.0, 0.82, 0.82}},
		{"Missing fixtures", strconv.Itoa(missing), []float64{1.0, 0.88, 0.88}},
		{"Submitted", strconv.Itoa(submitted), []float64{0.82, 0.95, 0.86}},
		{"Admin resolved", strconv.Itoa(resolved), []float64{0.80, 0.92, 1.0}},
		{"Byes", strconv.Itoa(byes), []float64{0.90, 0.90, 0.92}},
	}
	x := linkDiagPDFMargin
	for _, item := range items {
		r.fillRect(x, r.y, 120, 42, item.fill[0], item.fill[1], item.fill[2])
		r.strokeRect(x, r.y, 120, 42, 0.82)
		r.text(x+8, r.y+17, 16, "F2", item.value)
		r.text(x+8, r.y+31, 8, "F1", strings.ToUpper(item.label))
		x += 128
	}
	r.y += 54

	lines := []string{
		fmt.Sprintf("Search term: %s", r.data.Query),
		fmt.Sprintf("Link activity: SES clicks %d, app landings %d, confirmed links %d, form opens %d, autosaves %d, submissions %d.",
			counts["click"], counts["magic_link_clicked"], counts["magic_link_redeemed"], counts["captain_form_opened"], counts["draft_autosaved"], counts["submission_created"]),
		"Fixture rows marked missing, with no bye or admin resolution, are the evidence rows that support non-submission cards.",
	}
	r.note(lines)
}

func (r *linkDiagPDFRenderer) captains() {
	var rows [][]string
	for _, c := range r.data.Captains {
		status := "Inactive"
		if c.IsActive {
			status = "Current"
		}
		rows = append(rows, []string{
			c.FullName + "\n" + c.EffectiveEmail,
			c.ClubName + "\n" + c.TeamName,
			c.ActiveFrom + " to " + blankDash(c.ActiveTo),
			status,
		})
	}
	r.table("Captain Records", []linkDiagPDFColumn{
		{"Captain", 220},
		{"Club / Team", 250},
		{"Dates", 150},
		{"Status", 100},
	}, rows, nil)
}

func (r *linkDiagPDFRenderer) sanctions() {
	var rows [][]string
	var fills [][]float64
	for _, s := range r.data.Sanctions {
		points := "-"
		if s.PointsDeduction.Valid {
			points = strconv.Itoa(int(s.PointsDeduction.Int32))
		}
		sent := "-"
		if s.EmailSentAt.Valid {
			sent = s.EmailSentAt.Time.Format("02 Jan 15:04")
		}
		rows = append(rows, []string{
			fmt.Sprintf("%s #%d\n%s", sanctionsExportCardLabel(s.Colour), s.ID, reasonLabel(s.Reason)),
			fmt.Sprintf("Week %d\n%s", s.WeekNumber, s.MatchDate.Format("02 Jan 2006")),
			s.ClubName + "\n" + s.TeamName,
			statusLabel(s.Status) + "\nEmail " + sanctionsExportEmailStatusLabel(s.EmailStatus),
			s.IssuedAt.Format("02 Jan 15:04") + "\n" + s.IssuedBy,
			"Sent " + sent + "\nPoints " + points,
		})
		if s.Colour == "red" {
			fills = append(fills, []float64{1.0, 0.92, 0.92})
		} else {
			fills = append(fills, []float64{1.0, 0.97, 0.80})
		}
	}
	r.table("Card / Sanction Records", []linkDiagPDFColumn{
		{"Card", 120},
		{"Week / Match", 110},
		{"Club / Team", 210},
		{"Status", 130},
		{"Issued", 120},
		{"Email / Points", 120},
	}, rows, fills)
}

func (r *linkDiagPDFRenderer) fixtures() {
	var rows [][]string
	var fills [][]float64
	for _, f := range r.data.Fixtures {
		status, _ := linkDiagFixtureStatus(f)
		reportDetail := status
		if f.SubmissionID.Valid {
			reportDetail += fmt.Sprintf("\nSubmission #%d", f.SubmissionID.Int64)
		}
		if f.SubmittedAt.Valid {
			reportDetail += "\n" + f.SubmittedAt.Time.Format("02 Jan 15:04")
		}
		if f.ExemptionReason != "" {
			reportDetail += "\nResolved: " + f.ExemptionReason
		}
		reminders := "-"
		if f.ReminderCount > 0 {
			reminders = strconv.FormatInt(f.ReminderCount, 10)
			if f.ReminderTypes != "" {
				reminders += "\n" + f.ReminderTypes
			}
		}
		sanction := f.SanctionStatus
		if sanction == "" {
			sanction = "-"
		}
		rows = append(rows, []string{
			fmt.Sprintf("Week %d\n%s\nMatch %d", f.WeekNumber, f.MatchDate.Format("02 Jan 2006"), f.PlayCricketMatchID),
			f.ClubName + "\n" + f.TeamName,
			f.Opponent + "\n" + blankDash(f.Ground),
			reportDetail,
			reminders,
			sanction,
		})
		fills = append(fills, linkDiagPDFStatusFill(f))
	}
	r.table("Fixture Report Evidence", []linkDiagPDFColumn{
		{"Week / Match", 120},
		{"Club / Team", 160},
		{"Opponent / Ground", 190},
		{"Report Status", 130},
		{"Reminders", 85},
		{"Sanction", 125},
	}, rows, fills)
}

func (r *linkDiagPDFRenderer) reminders() {
	var rows [][]string
	for _, row := range r.data.Reminders {
		rows = append(rows, []string{
			row.SentAt.Format("02 Jan 15:04"),
			row.MatchDate.Format("02 Jan 2006"),
			row.ReminderType,
			row.Recipient,
			row.ClubName + "\n" + row.TeamName,
		})
	}
	r.table("Reminder Email Sends", []linkDiagPDFColumn{
		{"Sent", 105},
		{"Match", 95},
		{"Type", 95},
		{"Recipient", 245},
		{"Club / Team", 250},
	}, rows, nil)
}

func (r *linkDiagPDFRenderer) submissions() {
	var rows [][]string
	for _, row := range r.data.Submits {
		rows = append(rows, []string{
			strconv.FormatInt(row.ID, 10),
			row.MatchDate.Format("02 Jan 2006"),
			row.Submitted.Format("02 Jan 15:04"),
			row.TeamName,
		})
	}
	r.table("Submissions", []linkDiagPDFColumn{
		{"ID", 70},
		{"Match Date", 130},
		{"Submitted", 130},
		{"Team", 460},
	}, rows, nil)
}

func (r *linkDiagPDFRenderer) emailAndLinkActivity() {
	var rows [][]string
	for _, row := range r.data.Tokens {
		matchDate := "-"
		if row.MatchDate.Valid {
			matchDate = row.MatchDate.Time.Format("02 Jan 2006")
		}
		rows = append(rows, []string{
			"Token",
			row.CreatedAt.Format("02 Jan 15:04"),
			row.Status,
			matchDate,
			row.RequestIP,
		})
	}
	for _, row := range r.data.Events {
		eventTime := row.CreatedAt.Format("02 Jan 15:04")
		if row.OccurredAt.Valid {
			eventTime = row.OccurredAt.Time.Format("02 Jan 15:04")
		}
		rows = append(rows, []string{
			"Email " + row.EventType,
			eventTime,
			row.Recipient,
			row.Subject,
			strings.TrimSpace(row.ClickIP + " " + row.ClickUA),
		})
	}
	r.table("Delivery And Link Activity", []linkDiagPDFColumn{
		{"Type", 105},
		{"Time", 100},
		{"Status / Recipient", 165},
		{"Subject / Match", 275},
		{"Client", 145},
	}, rows, nil)
}

func (r *linkDiagPDFRenderer) audit() {
	var rows [][]string
	for _, row := range r.data.AuditRows {
		rows = append(rows, []string{
			row.CreatedAt.Format("02 Jan 15:04"),
			row.Action,
			redactMagicTokenInText(row.Metadata),
		})
	}
	r.table("Recent Audit Activity", []linkDiagPDFColumn{
		{"Time", 105},
		{"Action", 180},
		{"Metadata", 505},
	}, rows, nil)
}

func linkDiagPDFStatusFill(row linkDiagFixtureEvidence) []float64 {
	switch {
	case row.IsBye:
		return []float64{0.94, 0.94, 0.96}
	case row.ExemptionReason != "":
		return []float64{0.86, 0.95, 1.0}
	case row.HasSubmission:
		return []float64{0.88, 0.97, 0.90}
	default:
		return []float64{1.0, 0.90, 0.90}
	}
}

type linkDiagPDFColumn struct {
	label string
	width float64
}

func (r *linkDiagPDFRenderer) table(title string, cols []linkDiagPDFColumn, rows [][]string, fills [][]float64) {
	r.sectionTitle(title)
	if len(rows) == 0 {
		r.note([]string{"No rows found."})
		return
	}
	r.tableHeader(cols)
	for i, row := range rows {
		fill := []float64(nil)
		if i < len(fills) {
			fill = fills[i]
		}
		r.tableRow(cols, row, fill)
	}
	r.y += 10
}

func (r *linkDiagPDFRenderer) tableHeader(cols []linkDiagPDFColumn) {
	r.ensureSpace(28)
	x := linkDiagPDFMargin
	r.fillRect(x, r.y, linkDiagPDFWidth-linkDiagPDFMargin*2, 20, 0.23, 0.25, 0.29)
	for _, col := range cols {
		r.textRGB(x+4, r.y+13, 7.4, "F2", col.label, 1, 1, 1)
		x += col.width
	}
	r.y += 20
}

func (r *linkDiagPDFRenderer) tableRow(cols []linkDiagPDFColumn, row []string, fill []float64) {
	wrapped := make([][]string, len(cols))
	maxLines := 1
	for i, col := range cols {
		text := ""
		if i < len(row) {
			text = row[i]
		}
		wrapped[i] = wrapPDFText(text, col.width-8, 7.1)
		if len(wrapped[i]) > 8 {
			wrapped[i] = append(wrapped[i][:7], "...")
		}
		if len(wrapped[i]) > maxLines {
			maxLines = len(wrapped[i])
		}
	}
	rowHeight := 11 + float64(maxLines)*8.2
	if rowHeight < 25 {
		rowHeight = 25
	}
	if r.y+rowHeight > linkDiagPDFHeight-linkDiagPDFMargin-32 {
		r.newPage()
		r.tableHeader(cols)
	}
	if fill != nil {
		r.fillRect(linkDiagPDFMargin, r.y, linkDiagPDFWidth-linkDiagPDFMargin*2, rowHeight, fill[0], fill[1], fill[2])
	}
	x := linkDiagPDFMargin
	for i, col := range cols {
		r.strokeRect(x, r.y, col.width, rowHeight, 0.88)
		font := "F1"
		if i == 0 {
			font = "F2"
		}
		for j, line := range wrapped[i] {
			if float64(j) > (rowHeight-9)/8.2 {
				break
			}
			r.text(x+4, r.y+11+float64(j)*8.2, 7.1, font, line)
			if i == 0 && j == 0 {
				font = "F1"
			}
		}
		x += col.width
	}
	r.y += rowHeight
}

func (r *linkDiagPDFRenderer) sectionTitle(title string) {
	r.ensureSpace(30)
	r.text(linkDiagPDFMargin, r.y, 12, "F2", title)
	r.line(linkDiagPDFMargin, r.y+6, linkDiagPDFWidth-linkDiagPDFMargin, r.y+6, 0.78)
	r.y += 18
}

func (r *linkDiagPDFRenderer) note(lines []string) {
	height := 12 + float64(len(lines))*10
	r.ensureSpace(height)
	r.fillRect(linkDiagPDFMargin, r.y, linkDiagPDFWidth-linkDiagPDFMargin*2, height, 0.96, 0.98, 1.0)
	r.strokeRect(linkDiagPDFMargin, r.y, linkDiagPDFWidth-linkDiagPDFMargin*2, height, 0.84)
	for i, line := range lines {
		r.text(linkDiagPDFMargin+8, r.y+13+float64(i)*10, 8, "F1", line)
	}
	r.y += height + 10
}

func (r *linkDiagPDFRenderer) text(x, yTop, size float64, font, text string) {
	r.textRGB(x, yTop, size, font, text, 0, 0, 0)
}

func (r *linkDiagPDFRenderer) textRGB(x, yTop, size float64, font, text string, red, green, blue float64) {
	y := linkDiagPDFHeight - yTop
	fmt.Fprintf(&r.content, "%.3f %.3f %.3f rg BT /%s %.2f Tf %.2f %.2f Td (%s) Tj ET\n", red, green, blue, font, size, x, y, escapePDFString(text))
}

func (r *linkDiagPDFRenderer) fillRect(x, yTop, w, h, red, green, blue float64) {
	y := linkDiagPDFHeight - yTop - h
	fmt.Fprintf(&r.content, "%.3f %.3f %.3f rg %.2f %.2f %.2f %.2f re f\n", red, green, blue, x, y, w, h)
}

func (r *linkDiagPDFRenderer) strokeRect(x, yTop, w, h, grey float64) {
	y := linkDiagPDFHeight - yTop - h
	fmt.Fprintf(&r.content, "%.3f G %.2f %.2f %.2f %.2f re S\n", grey, x, y, w, h)
}

func (r *linkDiagPDFRenderer) line(x1, y1Top, x2, y2Top, grey float64) {
	y1 := linkDiagPDFHeight - y1Top
	y2 := linkDiagPDFHeight - y2Top
	fmt.Fprintf(&r.content, "%.3f G %.2f %.2f m %.2f %.2f l S\n", grey, x1, y1, x2, y2)
}
