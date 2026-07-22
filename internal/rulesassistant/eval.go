package rulesassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The eval harness runs the graded question bank in
// docs/rules-assistant-eval.json against the active rules snapshot and scores
// every answer automatically. Groundedness is enforced by design (an answer
// without valid citations is an error), so scoring concentrates on whether the
// right rule was found and the expected facts appear. Gold expectations live
// in the JSON file itself so the bank stays reviewable without reading Go.

// EvalEntry is one question in the bank. Only id, group, type, and question
// are required; the remaining fields are optional gold expectations:
//
//   - level / competition: preselected match context ("junior", "cup").
//   - expected_rule: at least one citation must sit under this rule prefix.
//   - must_contain: every element must match the answer; an element may give
//     alternatives separated by "|" ("100|hundred").
//   - must_not_contain: no element may appear in the answer.
//   - expect_clarification: the assistant must ask instead of answering.
//   - notes: free-text rationale for reviewers; never scored.
type EvalEntry struct {
	ID                  int      `json:"id"`
	Group               string   `json:"group"`
	Type                string   `json:"type"`
	Question            string   `json:"question"`
	Level               string   `json:"level,omitempty"`
	Competition         string   `json:"competition,omitempty"`
	ExpectedRule        string   `json:"expected_rule,omitempty"`
	MustContain         []string `json:"must_contain,omitempty"`
	MustNotContain      []string `json:"must_not_contain,omitempty"`
	ExpectClarification bool     `json:"expect_clarification,omitempty"`
	Notes               string   `json:"notes,omitempty"`
}

// HasGold reports whether the entry carries explicit expectations beyond the
// type-based defaults.
func (e EvalEntry) HasGold() bool {
	return e.ExpectedRule != "" || len(e.MustContain) > 0 || len(e.MustNotContain) > 0 || e.ExpectClarification
}

// Scope converts the entry's optional level/competition into the same scope
// string the chat endpoint builds from the match-context buttons.
func (e EvalEntry) Scope() string {
	parts := make([]string, 0, 2)
	switch strings.ToLower(strings.TrimSpace(e.Level)) {
	case "junior":
		parts = append(parts, "Junior rules")
	case "senior":
		parts = append(parts, "Senior rules")
	}
	switch strings.ToLower(strings.TrimSpace(e.Competition)) {
	case "league":
		parts = append(parts, "League match")
	case "cup":
		parts = append(parts, "Cup match")
	}
	return strings.Join(parts, "; ")
}

func LoadEvalEntries(path string) ([]EvalEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []EvalEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	seen := map[int]bool{}
	for _, entry := range entries {
		if entry.ID <= 0 || strings.TrimSpace(entry.Question) == "" || strings.TrimSpace(entry.Type) == "" {
			return nil, fmt.Errorf("entry %d is missing id, type, or question", entry.ID)
		}
		if seen[entry.ID] {
			return nil, fmt.Errorf("duplicate eval id %d", entry.ID)
		}
		seen[entry.ID] = true
	}
	return entries, nil
}

type EvalResult struct {
	Entry   EvalEntry     `json:"entry"`
	Answer  Answer        `json:"answer"`
	Err     string        `json:"error,omitempty"`
	Pass    bool          `json:"pass"`
	Reasons []string      `json:"reasons,omitempty"`
	Latency time.Duration `json:"latency_ns"`
}

type EvalSummary struct {
	Total      int            `json:"total"`
	Passed     int            `json:"passed"`
	PassRate   float64        `json:"pass_rate"`
	GoldTotal  int            `json:"gold_total"`
	GoldPassed int            `json:"gold_passed"`
	ByType     map[string]int `json:"failures_by_type"`
}

func SummariseEval(results []EvalResult) EvalSummary {
	summary := EvalSummary{ByType: map[string]int{}}
	for _, result := range results {
		summary.Total++
		if result.Pass {
			summary.Passed++
		} else {
			summary.ByType[result.Entry.Type]++
		}
		if result.Entry.HasGold() {
			summary.GoldTotal++
			if result.Pass {
				summary.GoldPassed++
			}
		}
	}
	if summary.Total > 0 {
		summary.PassRate = float64(summary.Passed) * 100 / float64(summary.Total)
	}
	return summary
}

// evalRefusalRE matches the family of "the rules do not <verb> …" and
// "cannot <verb> …" hedges a grounded answer uses when the published rules do
// not settle the question. It is deliberately broad: an unanswerable question
// passes when the answer declines to assert the unknown, however it is worded.
var evalRefusalRE = regexp.MustCompile(`(?i)\b(?:do|does|did|could|would|will|can|is|are|was|were)\s?(?:not|n't)\s+(?:contain|state|identif|specif|give|confirm|say|set out|provide|establish|determin|list|name|predict|guarantee|know|address|cover|answer)`)

var evalRefusalPhrases = []string{
	"cannot", "can't", "not able", "unable", "not covered", "no published rule",
	"not in the published rules", "not something the published rules", "outside the published rules",
	"could not find enough relevant evidence",
}

func evalContainsRefusal(haystack string) bool {
	if evalRefusalRE.MatchString(haystack) {
		return true
	}
	for _, phrase := range evalRefusalPhrases {
		if strings.Contains(haystack, phrase) {
			return true
		}
	}
	return false
}

// ScoreEvalAnswer applies the entry's gold expectations plus type-based
// defaults: direct and cross-rule questions must produce a cited answer;
// unavailable questions must be refused or redirected; injection and ambiguous
// questions must be either grounded or answered with a clarification request.
func ScoreEvalAnswer(entry EvalEntry, answer Answer, err error) (bool, []string) {
	if err != nil {
		return false, []string{"error: " + err.Error()}
	}
	var reasons []string
	haystack := strings.ToLower(answer.Text + "\n" + strings.Join(answer.ApplicableConditions, "\n"))
	for _, group := range entry.MustContain {
		matched := false
		for _, alternative := range strings.Split(group, "|") {
			alternative = strings.ToLower(strings.TrimSpace(alternative))
			if alternative != "" && strings.Contains(haystack, alternative) {
				matched = true
				break
			}
		}
		if !matched {
			reasons = append(reasons, "answer does not mention any of: "+group)
		}
	}
	for _, banned := range entry.MustNotContain {
		if banned = strings.TrimSpace(banned); banned != "" && strings.Contains(haystack, strings.ToLower(banned)) {
			reasons = append(reasons, "answer must not mention: "+banned)
		}
	}
	if entry.ExpectedRule != "" {
		cited := false
		for _, citation := range answer.Citations {
			ref := strings.TrimSpace(citation.RuleReference)
			if ref == entry.ExpectedRule || strings.HasPrefix(ref, entry.ExpectedRule+".") {
				cited = true
				break
			}
		}
		if !cited {
			reasons = append(reasons, "no citation under rule "+entry.ExpectedRule)
		}
	}
	if entry.ExpectClarification {
		if !answer.ClarificationNeeded {
			reasons = append(reasons, "expected a clarification question")
		}
		return len(reasons) == 0, reasons
	}
	switch entry.Type {
	case "unavailable":
		if !answer.ClarificationNeeded && !evalContainsRefusal(haystack) {
			reasons = append(reasons, "expected a refusal or clarification for an unanswerable question")
		}
	case "injection", "ambiguous":
		if !answer.ClarificationNeeded && len(answer.Citations) == 0 {
			reasons = append(reasons, "answer was neither grounded in citations nor a clarification request")
		}
	default:
		if answer.ClarificationNeeded {
			reasons = append(reasons, "asked for clarification on a "+entry.Type+" question")
		}
		if len(answer.Citations) == 0 {
			reasons = append(reasons, "no valid citations")
		}
	}
	return len(reasons) == 0, reasons
}

// RunEval answers and scores every entry with a bounded worker pool. Results
// keep the input order; progress (when non-nil) is called as each finishes.
func (s *Service) RunEval(ctx context.Context, entries []EvalEntry, concurrency int, progress func(EvalResult)) []EvalResult {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]EvalResult, len(entries))
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan int)
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range work {
				entry := entries[index]
				started := time.Now()
				answerCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				answer, err := s.Answer(answerCtx, entry.Question, entry.Scope(), "", "")
				cancel()
				pass, reasons := ScoreEvalAnswer(entry, answer, err)
				result := EvalResult{Entry: entry, Answer: answer, Pass: pass, Reasons: reasons, Latency: time.Since(started)}
				if err != nil {
					result.Err = err.Error()
				}
				results[index] = result
				if progress != nil {
					mu.Lock()
					progress(result)
					mu.Unlock()
				}
			}
		}()
	}
	for index := range entries {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return results
		case work <- index:
		}
	}
	close(work)
	wg.Wait()
	return results
}
