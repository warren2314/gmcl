// Command rules-eval runs the A1 rules assistant evaluation bank against the
// active rules snapshot and reports a scored pass rate. It calls the service
// directly (no HTTP rate limits), so it needs DB_DSN and OPENAI_API_KEY and
// costs real OpenAI tokens per question. Run it after every rules sync, prompt
// change, or retrieval change:
//
//	go run ./cmd/rules-eval -min-pass 95 -out output/rules-eval-report.json
//
// The exit code is non-zero when the pass rate is below -min-pass, so it can
// gate a deployment.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/rulesassistant"
)

func main() {
	file := flag.String("file", "docs/rules-assistant-eval.json", "evaluation question bank")
	group := flag.String("group", "", "only run questions from this rule group (1-8)")
	only := flag.Int("id", 0, "only run the question with this id")
	limit := flag.Int("limit", 0, "maximum number of questions to run (0 = all)")
	concurrency := flag.Int("concurrency", 2, "questions answered in parallel")
	minPass := flag.Float64("min-pass", 95, "minimum overall pass percentage for a zero exit code")
	out := flag.String("out", "", "write the full JSON report to this file")
	verbose := flag.Bool("verbose", false, "print every answer in full, not just failures")
	flag.Parse()

	entries, err := rulesassistant.LoadEvalEntries(*file)
	if err != nil {
		fatalf("could not load %s: %v", *file, err)
	}
	filtered := make([]rulesassistant.EvalEntry, 0, len(entries))
	for _, entry := range entries {
		if *group != "" && entry.Group != *group {
			continue
		}
		if *only != 0 && entry.ID != *only {
			continue
		}
		filtered = append(filtered, entry)
		if *limit > 0 && len(filtered) >= *limit {
			break
		}
	}
	if len(filtered) == 0 {
		fatalf("no eval questions matched the filters")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	pool, err := db.NewFromEnv(ctx)
	if err != nil {
		fatalf("database: %v", err)
	}
	defer pool.Close()
	service := rulesassistant.New(pool)
	if service.APIKey == "" {
		fatalf("OPENAI_API_KEY is not configured")
	}

	fmt.Printf("Running %d evaluation question(s) against the active snapshot (model %s, concurrency %d)…\n\n", len(filtered), service.ChatModel, *concurrency)
	done := 0
	results := service.RunEval(ctx, filtered, *concurrency, func(result rulesassistant.EvalResult) {
		done++
		verdict := "PASS"
		if !result.Pass {
			verdict = "FAIL"
		}
		fmt.Printf("[%3d/%d] %s  #%d (%s/%s) %s\n", done, len(filtered), verdict, result.Entry.ID, result.Entry.Group, result.Entry.Type, truncateLine(result.Entry.Question, 90))
		if !result.Pass {
			for _, reason := range result.Reasons {
				fmt.Printf("         - %s\n", reason)
			}
		}
		if *verbose || !result.Pass {
			if result.Answer.Text != "" {
				fmt.Printf("         answer: %s\n", truncateLine(result.Answer.Text, 400))
			}
			if len(result.Answer.Citations) > 0 {
				refs := make([]string, 0, len(result.Answer.Citations))
				for _, citation := range result.Answer.Citations {
					refs = append(refs, citation.RuleReference)
				}
				fmt.Printf("         cited: %s\n", strings.Join(refs, ", "))
			}
		}
	})

	summary := rulesassistant.SummariseEval(results)
	fmt.Printf("\nOverall: %d/%d passed (%.1f%%).", summary.Passed, summary.Total, summary.PassRate)
	if summary.GoldTotal > 0 {
		fmt.Printf(" Gold-answer questions: %d/%d passed.", summary.GoldPassed, summary.GoldTotal)
	}
	fmt.Println()
	if len(summary.ByType) > 0 {
		fmt.Print("Failures by type:")
		for evalType, count := range summary.ByType {
			fmt.Printf(" %s=%d", evalType, count)
		}
		fmt.Println()
	}

	if *out != "" {
		report := map[string]any{
			"generated_at": time.Now().UTC().Format(time.RFC3339),
			"model":        service.ChatModel,
			"file":         *file,
			"summary":      summary,
			"results":      results,
		}
		encoded, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(*out, encoded, 0o644); err != nil {
			fatalf("could not write report: %v", err)
		}
		fmt.Printf("Report written to %s\n", *out)
	}
	if summary.PassRate < *minPass {
		fmt.Printf("FAILED: pass rate %.1f%% is below the required %.1f%%\n", summary.PassRate, *minPass)
		os.Exit(1)
	}
}

func truncateLine(value string, n int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) <= n {
		return value
	}
	return value[:n] + "…"
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
