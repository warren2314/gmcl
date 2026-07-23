package httpserver

import (
	"strings"
	"testing"
)

const completeNarrativeJSON = `{
	"executive_summary":"Executive summary",
	"latest_report":"Latest report",
	"season_report":"Season report",
	"latest_umpire_report":"Latest umpire report",
	"season_umpire_report":"Season umpire report",
	"conclusion":"Conclusion"
}`

func TestParseOpenAIExecutiveNarrativeResponseCompleted(t *testing.T) {
	body := `{"status":"completed","output_text":` + quotedJSON(completeNarrativeJSON) + `}`

	narrative, err := parseOpenAIExecutiveNarrativeResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse completed response: %v", err)
	}
	if narrative.ExecutiveSummary != "Executive summary" || narrative.Conclusion != "Conclusion" {
		t.Fatalf("unexpected narrative: %#v", narrative)
	}
}

func TestParseOpenAIExecutiveNarrativeResponseIncompleteDoesNotLeakOutput(t *testing.T) {
	const privateFragment = "PRIVATE CLUB DISCIPLINE DETAIL"
	body := `{
		"status":"incomplete",
		"incomplete_details":{"reason":"max_output_tokens"},
		"output_text":"{\"conclusion\":\"` + privateFragment + `"
	}`

	_, err := parseOpenAIExecutiveNarrativeResponse([]byte(body))
	if err == nil {
		t.Fatal("expected incomplete response to fail")
	}
	if !retryableOpenAINarrativeError(err) {
		t.Fatalf("expected retryable error, got %T: %v", err, err)
	}
	if strings.Contains(err.Error(), privateFragment) {
		t.Fatalf("error leaked model output: %v", err)
	}
}

func TestParseOpenAIExecutiveNarrativeResponseMalformedDoesNotLeakOutput(t *testing.T) {
	const privateFragment = "PRIVATE REPORT CONTENT"
	body := `{"status":"completed","output_text":"{\"executive_summary\":\"` + privateFragment + `"}`

	_, err := parseOpenAIExecutiveNarrativeResponse([]byte(body))
	if err == nil {
		t.Fatal("expected malformed structured output to fail")
	}
	if !retryableOpenAINarrativeError(err) {
		t.Fatalf("expected retryable error, got %T: %v", err, err)
	}
	if strings.Contains(err.Error(), privateFragment) {
		t.Fatalf("error leaked model output: %v", err)
	}
}

func TestParseOpenAIExecutiveNarrativeResponseRequiresEverySection(t *testing.T) {
	body := `{"status":"completed","output_text":"{\"executive_summary\":\"Only one section\"}"}`

	_, err := parseOpenAIExecutiveNarrativeResponse([]byte(body))
	if err == nil {
		t.Fatal("expected response with missing sections to fail")
	}
	if !strings.Contains(err.Error(), "omitted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSafeOpenAIReasonRemovesUnsafeCharacters(t *testing.T) {
	got := safeOpenAIReason(`max_output_tokens<script>alert("x")</script>`)
	if got != "max_output_tokensscriptalertxscript" {
		t.Fatalf("unexpected safe reason %q", got)
	}
}

func quotedJSON(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
