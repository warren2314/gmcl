package httpserver

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	cardReportPDFWidth  = 842.0
	cardReportPDFHeight = 595.0
	cardReportPDFMargin = 32.0
)

type simplePDFDoc struct {
	width  float64
	height float64
	pages  []string
}

func newSimplePDFDoc(width, height float64) *simplePDFDoc {
	return &simplePDFDoc{width: width, height: height}
}

func (d *simplePDFDoc) addPage(content string) {
	d.pages = append(d.pages, content)
}

func (d *simplePDFDoc) bytes() []byte {
	var objects []string
	objects = append(objects, `<< /Type /Catalog /Pages 2 0 R >>`)

	var kids []string
	for i := range d.pages {
		pageObj := 5 + i*2
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObj))
	}
	objects = append(objects, fmt.Sprintf(`<< /Type /Pages /Kids [%s] /Count %d >>`, strings.Join(kids, " "), len(d.pages)))
	objects = append(objects, `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`)
	objects = append(objects, `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>`)

	for i, page := range d.pages {
		pageObj := 5 + i*2
		contentObj := pageObj + 1
		objects = append(objects, fmt.Sprintf(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>`,
			d.width, d.height, contentObj))
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len([]byte(page)), page))
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects)+1)
	offsets = append(offsets, 0)
	for i, obj := range objects {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return buf.Bytes()
}

type cardReportPDFRenderer struct {
	doc         *simplePDFDoc
	content     strings.Builder
	week        cardReportWeek
	generatedAt time.Time
	pageNo      int
	y           float64
}

func buildWeeklyCardReportPDF(week cardReportWeek, rows []weeklyCardReportRow, generatedAt time.Time) []byte {
	r := &cardReportPDFRenderer{
		doc:         newSimplePDFDoc(cardReportPDFWidth, cardReportPDFHeight),
		week:        week,
		generatedAt: generatedAt,
	}
	r.startPage(true)
	r.drawSummary(rows)
	r.drawPolicyNote()
	r.drawTableHeader()
	for i, row := range rows {
		r.drawRow(i, row)
	}
	r.finishPage()
	return r.doc.bytes()
}

func (r *cardReportPDFRenderer) startPage(first bool) {
	r.pageNo++
	r.content.Reset()
	r.y = cardReportPDFMargin

	if first {
		r.text(cardReportPDFMargin, r.y, 18, "F2", "GMCL Weekly Card Report")
		r.y += 22
		r.text(cardReportPDFMargin, r.y, 10.5, "F1", fmt.Sprintf("%s - Week %d - %s to %s",
			r.week.SeasonName,
			r.week.Number,
			r.week.StartDate.Format("2 Jan 2006"),
			r.week.EndDate.Format("2 Jan 2006")))
		r.y += 15
		r.text(cardReportPDFMargin, r.y, 9, "F1", "Generated "+r.generatedAt.Format("2 Jan 2006 15:04"))
		r.y += 18
	} else {
		r.text(cardReportPDFMargin, r.y, 12, "F2", fmt.Sprintf("GMCL Weekly Card Report - Week %d", r.week.Number))
		r.y += 18
	}
}

func (r *cardReportPDFRenderer) finishPage() {
	r.line(cardReportPDFMargin, cardReportPDFHeight-28, cardReportPDFWidth-cardReportPDFMargin, cardReportPDFHeight-28, 0.82)
	r.text(cardReportPDFMargin, cardReportPDFHeight-15, 8, "F1", "GMCL Discipline Committee")
	r.text(cardReportPDFWidth-cardReportPDFMargin-45, cardReportPDFHeight-15, 8, "F1", fmt.Sprintf("Page %d", r.pageNo))
	r.doc.addPage(r.content.String())
}

func (r *cardReportPDFRenderer) newPageWithTableHeader() {
	r.finishPage()
	r.startPage(false)
	r.drawTableHeader()
}

func (r *cardReportPDFRenderer) drawSummary(rows []weeklyCardReportRow) {
	yellowDue, redDue, issuedCount := cardReportCounts(rows)
	boxW := 124.0
	gap := 10.0
	x := cardReportPDFMargin
	items := []struct {
		label string
		value string
		fill  []float64
	}{
		{"Rows selected", fmt.Sprintf("%d", len(rows)), []float64{0.94, 0.96, 1.0}},
		{"Yellow due", fmt.Sprintf("%d", yellowDue), []float64{1.0, 0.96, 0.78}},
		{"Red due", fmt.Sprintf("%d", redDue), []float64{1.0, 0.88, 0.88}},
		{"Already issued", fmt.Sprintf("%d", issuedCount), []float64{0.94, 0.9, 1.0}},
	}
	for _, item := range items {
		r.fillRect(x, r.y, boxW, 42, item.fill[0], item.fill[1], item.fill[2])
		r.strokeRect(x, r.y, boxW, 42, 0.82)
		r.text(x+9, r.y+16, 16, "F2", item.value)
		r.text(x+9, r.y+31, 8, "F1", strings.ToUpper(item.label))
		x += boxW + gap
	}
	r.y += 54
}

func (r *cardReportPDFRenderer) drawPolicyNote() {
	lines := []string{
		"Basis: card due is calculated from active/served non-submission offences before this week.",
		"Every 3rd offence is a red card. Red card point deduction equals the red-card count after this issue.",
		"Rows removed on the review screen are not included in this PDF.",
	}
	r.fillRect(cardReportPDFMargin, r.y, cardReportPDFWidth-cardReportPDFMargin*2, 44, 0.97, 0.97, 0.97)
	r.strokeRect(cardReportPDFMargin, r.y, cardReportPDFWidth-cardReportPDFMargin*2, 44, 0.83)
	for i, line := range lines {
		r.text(cardReportPDFMargin+9, r.y+13+float64(i)*11, 8.5, "F1", line)
	}
	r.y += 56
}

func (r *cardReportPDFRenderer) drawTableHeader() {
	cols := cardReportColumns()
	x := cardReportPDFMargin
	r.fillRect(x, r.y, cardReportPDFWidth-cardReportPDFMargin*2, 22, 0.55, 0.0, 0.0)
	for _, col := range cols {
		r.textRGB(x+4, r.y+14, 8, "F2", col.label, 1, 1, 1)
		x += col.width
	}
	r.y += 22
}

func (r *cardReportPDFRenderer) drawRow(index int, row weeklyCardReportRow) {
	cols := cardReportColumns()
	cellText := []string{
		row.ClubName + "\n" + row.TeamName,
		cardReportMissingText(row),
		cardReportPriorText(row),
		cardReportDueText(row),
		cardReportCurrentText(row),
		reasonLabel(row.ExistingReason),
	}
	wrapped := make([][]string, len(cols))
	maxLines := 1
	for i, col := range cols {
		wrapped[i] = wrapPDFText(cellText[i], col.width-8, 8)
		if len(wrapped[i]) > maxLines {
			maxLines = len(wrapped[i])
		}
	}
	rowHeight := math.Max(30, 12+float64(maxLines)*9.2)
	if r.y+rowHeight > cardReportPDFHeight-cardReportPDFMargin-32 {
		r.newPageWithTableHeader()
	}

	if index%2 == 1 {
		r.fillRect(cardReportPDFMargin, r.y, cardReportPDFWidth-cardReportPDFMargin*2, rowHeight, 0.985, 0.985, 0.985)
	}
	if row.CardDue == "red" {
		r.fillRect(cardReportPDFMargin, r.y, 4, rowHeight, 0.75, 0.0, 0.0)
	} else {
		r.fillRect(cardReportPDFMargin, r.y, 4, rowHeight, 0.96, 0.76, 0.18)
	}

	x := cardReportPDFMargin
	for i, col := range cols {
		r.strokeRect(x, r.y, col.width, rowHeight, 0.88)
		font := "F1"
		if i == 0 || i == 3 {
			font = "F2"
		}
		for j, line := range wrapped[i] {
			if float64(j) > (rowHeight-10)/9.2 {
				break
			}
			r.text(x+5, r.y+12+float64(j)*9.2, 8, font, line)
			if i == 0 && j == 0 {
				font = "F1"
			}
		}
		x += col.width
	}
	r.y += rowHeight
}

type cardReportColumn struct {
	label string
	width float64
}

func cardReportColumns() []cardReportColumn {
	return []cardReportColumn{
		{label: "Club / Team", width: 155},
		{label: "Missing Details", width: 235},
		{label: "Prior", width: 86},
		{label: "Card Due", width: 105},
		{label: "Current Card / Letter", width: 132},
		{label: "Reason", width: 65},
	}
}

func (r *cardReportPDFRenderer) text(x, yTop, size float64, font, text string) {
	r.textRGB(x, yTop, size, font, text, 0, 0, 0)
}

func (r *cardReportPDFRenderer) textRGB(x, yTop, size float64, font, text string, red, green, blue float64) {
	y := cardReportPDFHeight - yTop
	fmt.Fprintf(&r.content, "%.3f %.3f %.3f rg BT /%s %.2f Tf %.2f %.2f Td (%s) Tj ET\n", red, green, blue, font, size, x, y, escapePDFString(text))
}

func (r *cardReportPDFRenderer) fillRect(x, yTop, w, h, red, green, blue float64) {
	y := cardReportPDFHeight - yTop - h
	fmt.Fprintf(&r.content, "%.3f %.3f %.3f rg %.2f %.2f %.2f %.2f re f\n", red, green, blue, x, y, w, h)
}

func (r *cardReportPDFRenderer) strokeRect(x, yTop, w, h, grey float64) {
	y := cardReportPDFHeight - yTop - h
	fmt.Fprintf(&r.content, "%.3f G %.2f %.2f %.2f %.2f re S\n", grey, x, y, w, h)
}

func (r *cardReportPDFRenderer) line(x1, y1Top, x2, y2Top, grey float64) {
	y1 := cardReportPDFHeight - y1Top
	y2 := cardReportPDFHeight - y2Top
	fmt.Fprintf(&r.content, "%.3f G %.2f %.2f m %.2f %.2f l S\n", grey, x1, y1, x2, y2)
}

func wrapPDFText(text string, width, fontSize float64) []string {
	var lines []string
	for _, rawLine := range strings.Split(normalizePDFText(text), "\n") {
		words := strings.Fields(rawLine)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		maxChars := int(width / (fontSize * 0.48))
		if maxChars < 8 {
			maxChars = 8
		}
		current := ""
		for _, word := range words {
			for len(word) > maxChars {
				if current != "" {
					lines = append(lines, current)
					current = ""
				}
				lines = append(lines, word[:maxChars])
				word = word[maxChars:]
			}
			if current == "" {
				current = word
				continue
			}
			if len(current)+1+len(word) <= maxChars {
				current += " " + word
			} else {
				lines = append(lines, current)
				current = word
			}
		}
		if current != "" {
			lines = append(lines, current)
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func escapePDFString(text string) string {
	text = normalizePDFText(text)
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, "(", `\(`)
	text = strings.ReplaceAll(text, ")", `\)`)
	return text
}

func normalizePDFText(text string) string {
	replacer := strings.NewReplacer(
		"\r\n", "\n",
		"\r", "\n",
		"\u2013", "-",
		"\u2014", "-",
		"\u2011", "-",
		"\u201c", `"`,
		"\u201d", `"`,
		"\u2018", "'",
		"\u2019", "'",
		"\u2026", "...",
		"\u00a3", "GBP",
	)
	text = replacer.Replace(text)

	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r >= 32 && r <= 126:
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}
