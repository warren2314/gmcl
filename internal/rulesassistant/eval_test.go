package rulesassistant

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLoadEvalEntriesAcceptsTheLiveBank(t *testing.T) {
	entries, err := LoadEvalEntries(filepath.Join("..", "..", "docs", "rules-assistant-eval.json"))
	if err != nil {
		t.Fatalf("live eval bank failed to load: %v", err)
	}
	if len(entries) < 100 {
		t.Fatalf("expected at least 100 questions, got %d", len(entries))
	}
	gold := 0
	for _, entry := range entries {
		if entry.HasGold() {
			gold++
		}
	}
	if gold < 3 {
		t.Fatalf("expected at least 3 gold-answer questions, got %d", gold)
	}
}

func TestEvalEntryScopeMatchesChatScopeFormat(t *testing.T) {
	entry := EvalEntry{Level: "junior", Competition: "cup"}
	if got := entry.Scope(); got != "Junior rules; Cup match" {
		t.Fatalf("scope=%q", got)
	}
	if got := (EvalEntry{}).Scope(); got != "" {
		t.Fatalf("empty scope=%q", got)
	}
}

func TestScoreEvalAnswerDirectQuestionNeedsCitationsAndGoldFacts(t *testing.T) {
	entry := EvalEntry{Type: "direct", ExpectedRule: "7.10", MustContain: []string{"100|hundred", "deliveries|ball"}}
	answer := Answer{
		Text:      "The Summer Cup is played over 100 deliveries per innings.",
		Citations: []Citation{{ChunkID: 1, RuleReference: "7.10.4.8.2"}},
	}
	if pass, reasons := ScoreEvalAnswer(entry, answer, nil); !pass {
		t.Fatalf("expected pass, got %v", reasons)
	}
	answer.Citations = []Citation{{ChunkID: 2, RuleReference: "3.5"}}
	if pass, reasons := ScoreEvalAnswer(entry, answer, nil); pass || len(reasons) != 1 {
		t.Fatalf("wrong rule citation must fail with one reason, got pass=%v %v", pass, reasons)
	}
	answer.Citations = nil
	answer.Text = "It is a limited format."
	pass, reasons := ScoreEvalAnswer(entry, answer, nil)
	if pass || len(reasons) < 3 {
		t.Fatalf("missing facts and citations must fail, got pass=%v %v", pass, reasons)
	}
}

func TestScoreEvalAnswerRespectsMustNotContain(t *testing.T) {
	entry := EvalEntry{Type: "direct", MustNotContain: []string{"fee is waived"}}
	answer := Answer{Text: "Your membership fee is waived.", Citations: []Citation{{ChunkID: 1, RuleReference: "1.5"}}}
	if pass, reasons := ScoreEvalAnswer(entry, answer, nil); pass || len(reasons) == 0 {
		t.Fatalf("banned phrase must fail, got pass=%v %v", pass, reasons)
	}
}

func TestScoreEvalAnswerUnavailableMustRefuse(t *testing.T) {
	entry := EvalEntry{Type: "unavailable"}
	confident := Answer{Text: "Woodley will win the league.", Citations: []Citation{{ChunkID: 1, RuleReference: "4.1"}}}
	if pass, _ := ScoreEvalAnswer(entry, confident, nil); pass {
		t.Fatal("a confident answer to an unanswerable question must fail")
	}
	refusal := Answer{Text: "The published rules do not contain that; I cannot predict results.", Citations: []Citation{{ChunkID: 1, RuleReference: "4.1"}}}
	if pass, reasons := ScoreEvalAnswer(entry, refusal, nil); !pass {
		t.Fatalf("a refusal must pass, got %v", reasons)
	}
	clarification := Answer{Text: "Could you say which rule you mean?", ClarificationNeeded: true}
	if pass, reasons := ScoreEvalAnswer(entry, clarification, nil); !pass {
		t.Fatalf("a clarification must pass, got %v", reasons)
	}
}

func TestScoreEvalAnswerAcceptsVariedRefusalWording(t *testing.T) {
	entry := EvalEntry{Type: "unavailable"}
	// Grounded answers that decline to assert the unknown, then add helpful
	// context, must count as refusals however they are phrased.
	for _, text := range []string{
		"The published rules do not identify a named winner. The award goes to the highest average.",
		"The rules do not confirm that the child is safe to play. The club must assess welfare.",
		"I cannot determine the result from the published rules.",
		"The supplied rules do not predict this season's outcome.",
	} {
		answer := Answer{Text: text, Citations: []Citation{{ChunkID: 1, RuleReference: "6.2"}}}
		if pass, reasons := ScoreEvalAnswer(entry, answer, nil); !pass {
			t.Fatalf("refusal %q scored as fail: %v", text, reasons)
		}
	}
	// A confident assertion with no hedge must still fail an unavailable item.
	confident := Answer{Text: "Woodley will win the batting award this year.", Citations: []Citation{{ChunkID: 1, RuleReference: "6.2"}}}
	if pass, _ := ScoreEvalAnswer(entry, confident, nil); pass {
		t.Fatal("a confident prediction must fail an unavailable question")
	}
}

func TestScoreEvalAnswerClarificationExpectations(t *testing.T) {
	entry := EvalEntry{Type: "ambiguous", ExpectClarification: true}
	if pass, _ := ScoreEvalAnswer(entry, Answer{Text: "Here is a full answer.", Citations: []Citation{{ChunkID: 1}}}, nil); pass {
		t.Fatal("expected-clarification entry answered outright must fail")
	}
	if pass, reasons := ScoreEvalAnswer(entry, Answer{Text: "Which competition?", ClarificationNeeded: true}, nil); !pass {
		t.Fatalf("clarification must pass, got %v", reasons)
	}
	direct := EvalEntry{Type: "direct"}
	if pass, _ := ScoreEvalAnswer(direct, Answer{Text: "Which competition?", ClarificationNeeded: true}, nil); pass {
		t.Fatal("clarification on a direct question must fail so a human reviews it")
	}
}

func TestScoreEvalAnswerErrorAlwaysFails(t *testing.T) {
	pass, reasons := ScoreEvalAnswer(EvalEntry{Type: "direct"}, Answer{}, errors.New("boom"))
	if pass || len(reasons) != 1 {
		t.Fatalf("error must fail with a single reason, got pass=%v %v", pass, reasons)
	}
}

func TestSummariseEvalCountsGoldSeparately(t *testing.T) {
	results := []EvalResult{
		{Entry: EvalEntry{Type: "direct", ExpectedRule: "7.10"}, Pass: true},
		{Entry: EvalEntry{Type: "direct"}, Pass: true},
		{Entry: EvalEntry{Type: "unavailable"}, Pass: false},
	}
	summary := SummariseEval(results)
	if summary.Total != 3 || summary.Passed != 2 || summary.GoldTotal != 1 || summary.GoldPassed != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.ByType["unavailable"] != 1 {
		t.Fatalf("failures by type=%v", summary.ByType)
	}
}
