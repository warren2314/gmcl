package sanctions

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// OutcomeLetter is the exact audience-safe text locked by independent
// approval. The PDF and email are generated from the same snapshot.
type OutcomeLetter struct {
	Reference  string
	Audience   string
	Subject    string
	Body       string
	ApprovedAt time.Time
	Draft      bool
}

// BuildOutcomeLetterPDF creates a small, deterministic, letterheaded PDF. It
// deliberately uses built-in PDF fonts so no mutable template or host font can
// change an already approved outcome.
func BuildOutcomeLetterPDF(letter OutcomeLetter) []byte {
	const width, height = 595.0, 842.0
	lines := wrapOutcomeText(letter.Body, 86)
	if len(lines) == 0 {
		lines = []string{" "}
	}
	perPage := 49
	if letter.Draft {
		perPage = 44
	}
	pages := make([]string, 0, (len(lines)+perPage-1)/perPage)
	for start := 0; start < len(lines); start += perPage {
		end := start + perPage
		if end > len(lines) {
			end = len(lines)
		}
		var content strings.Builder
		// GMCL red letterhead band.
		fmt.Fprintf(&content, "0.65 0.00 0.08 rg 0 %.2f %.2f 72 re f\n", height-72, width)
		pdfText(&content, 38, height-38, 20, "F2", "GREATER MANCHESTER CRICKET LEAGUE", 1, 1, 1)
		pdfText(&content, 38, height-57, 9, "F1", "Discipline and Ineligible Player Case Management", 1, 1, 1)
		pdfText(&content, 38, height-101, 9, "F2", "Case reference: "+letter.Reference, 0.15, 0.15, 0.15)
		pdfText(&content, 330, height-101, 9, "F1", "Decision record date: "+letter.ApprovedAt.Format("2 January 2006"), 0.15, 0.15, 0.15)
		pdfText(&content, 38, height-130, 13, "F2", letter.Subject, 0.08, 0.08, 0.08)
		if letter.Draft {
			pdfText(&content, 145, height-205, 28, "F2", "DRAFT - NOT APPROVED", 0.75, 0.05, 0.08)
		}
		y := height - 158
		if letter.Draft {
			y = height - 225
		}
		for _, line := range lines[start:end] {
			font := "F1"
			size := 10.2
			if strings.HasSuffix(line, ":") && len(line) < 70 {
				font = "F2"
			}
			pdfText(&content, 38, y, size, font, line, 0.12, 0.12, 0.12)
			y -= 13.3
		}
		pdfText(&content, 38, 27, 8, "F1", "Greater Manchester Cricket League", 0.35, 0.35, 0.35)
		pdfText(&content, 470, 27, 8, "F1", fmt.Sprintf("Page %d", len(pages)+1), 0.35, 0.35, 0.35)
		pages = append(pages, content.String())
	}
	return outcomePDFBytes(width, height, pages)
}

func wrapOutcomeText(value string, maxRunes int) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	var out []string
	for _, raw := range strings.Split(value, "\n") {
		words := strings.Fields(raw)
		if len(words) == 0 {
			out = append(out, " ")
			continue
		}
		line := ""
		for _, word := range words {
			candidate := word
			if line != "" {
				candidate = line + " " + word
			}
			if len([]rune(candidate)) <= maxRunes || line == "" {
				line = candidate
				continue
			}
			out = append(out, line)
			line = word
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func pdfText(out *strings.Builder, x, y, size float64, font, value string, red, green, blue float64) {
	fmt.Fprintf(out, "%.3f %.3f %.3f rg BT /%s %.2f Tf %.2f %.2f Td (%s) Tj ET\n", red, green, blue, font, size, x, y, escapeOutcomePDF(value))
}

func escapeOutcomePDF(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsSpace(r) {
			out.WriteByte(' ')
			continue
		}
		encoded, ok := outcomeWinAnsiByte(r)
		if !ok {
			encoded = '?'
		}
		switch encoded {
		case '\\', '(', ')':
			out.WriteByte('\\')
			out.WriteByte(encoded)
		default:
			if encoded >= 32 && encoded <= 126 {
				out.WriteByte(encoded)
			} else {
				// Octal escapes keep the PDF source deterministic while the
				// WinAnsi font encoding renders the original glyph.
				fmt.Fprintf(&out, "\\%03o", encoded)
			}
		}
	}
	return out.String()
}

var outcomeWinAnsiExtras = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85,
	'†': 0x86, '‡': 0x87, 'ˆ': 0x88, '‰': 0x89, 'Š': 0x8a,
	'‹': 0x8b, 'Œ': 0x8c, 'Ž': 0x8e, '‘': 0x91, '’': 0x92,
	'“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9a, '›': 0x9b, 'œ': 0x9c,
	'ž': 0x9e, 'Ÿ': 0x9f,
}

func outcomeWinAnsiByte(r rune) (byte, bool) {
	if r >= 32 && r <= 126 {
		return byte(r), true
	}
	if r >= 0x00a0 && r <= 0x00ff {
		return byte(r), true
	}
	b, ok := outcomeWinAnsiExtras[r]
	return b, ok
}

func outcomePDFBytes(width, height float64, pages []string) []byte {
	// 1 catalog, 2 pages tree, 3/4 fonts, then page/content object pairs.
	objects := make([][]byte, 4+len(pages)*2)
	objects[0] = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, 0, len(pages))
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 5+i*2))
	}
	objects[1] = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	objects[2] = []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	objects[3] = []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	for i, content := range pages {
		pageObject := 5 + i*2
		contentObject := pageObject + 1
		objects[pageObject-1] = []byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", width, height, contentObject))
		objects[contentObject-1] = []byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len([]byte(content)), content))
	}

	var file bytes.Buffer
	file.WriteString("%PDF-1.4\n%GMCL\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = file.Len()
		fmt.Fprintf(&file, "%d 0 obj\n", i+1)
		file.Write(object)
		file.WriteString("\nendobj\n")
	}
	xref := file.Len()
	fmt.Fprintf(&file, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&file, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&file, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return file.Bytes()
}
