package sanctions

import (
	"strings"
	"testing"
	"time"
)

func TestBuildOutcomeLetterPDFLocksAudienceSafeText(t *testing.T) {
	pdf := BuildOutcomeLetterPDF(OutcomeLetter{
		Reference:  "GMCL-2026-001234",
		Audience:   "reporting_club",
		Subject:    "Ineligible player case outcome",
		Body:       "Findings:\nThe player was ineligible.\n\nOutcome:\nTwo league points deducted.",
		ApprovedAt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	})
	got := string(pdf)
	if !strings.HasPrefix(got, "%PDF-1.4") {
		t.Fatalf("expected PDF header, got %q", got[:min(8, len(got))])
	}
	for _, want := range []string{"GREATER MANCHESTER CRICKET LEAGUE", "GMCL-2026-001234", "The player was ineligible", "Two league points deducted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("PDF is missing %q", want)
		}
	}
}

func TestWrapOutcomeTextPreservesParagraphBreak(t *testing.T) {
	lines := wrapOutcomeText("One two three four\n\nFive", 8)
	if strings.Join(lines, "|") != "One two|three|four| |Five" {
		t.Fatalf("unexpected wrapping: %#v", lines)
	}
}

func TestBuildOutcomeLetterPDFPreservesWinAnsiNamesAndPunctuation(t *testing.T) {
	pdf := string(BuildOutcomeLetterPDF(OutcomeLetter{
		Reference:  "GMCL-2026-001235",
		Subject:    "Findings for José",
		Body:       "José O’Brien – fine: £12.50 — no ban.",
		ApprovedAt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	}))
	for _, encoded := range []string{`Jos\351`, `O\222Brien`, `\226`, `\24312.50`, `\227`} {
		if !strings.Contains(pdf, encoded) {
			t.Fatalf("PDF is missing WinAnsi sequence %q", encoded)
		}
	}
	if strings.Contains(pdf, "Jos?") || strings.Contains(pdf, "O?Brien") {
		t.Fatal("supported WinAnsi glyphs were replaced with question marks")
	}
}
