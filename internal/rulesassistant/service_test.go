package rulesassistant

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestParseHTMLPreservesRuleHeadings(t *testing.T) {
	raw := `<html><title>Player rules</title><body>WELCOME TO GMCL FOR YOUR MOBILE
<h1>GMCL Rules</h1><h2>Rule 3.5 Starred Players</h2><p>A starred player must satisfy the published eligibility restrictions for the relevant team.</p>
<h3>3.5.2 Sunday cricket</h3><p>The competition-specific conditions also apply.</p><h2>Proud Sponsors</h2></body></html>`
	doc := parseHTML("https://example.test/pages/rules-players", raw)
	if doc.Title != "Player rules" {
		t.Fatalf("title=%q", doc.Title)
	}
	if len(doc.Chunks) < 2 {
		t.Fatalf("expected heading chunks, got %d", len(doc.Chunks))
	}
	found := false
	for _, chunk := range doc.Chunks {
		if strings.HasPrefix(chunk.RuleReference, "3.5") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected Rule 3.5 reference")
	}
}

func TestDiscoverLinksReadsShopblocksDynamicTileLinks(t *testing.T) {
	// The Rule 4 competition pages are only reachable through data-dynamic
	// tile JSON, not href attributes; missing them removed a fifth of the
	// rulebook from the corpus.
	root, _ := url.Parse("https://gmcl.test/pages/rules-menu-comps")
	raw := `WELCOME TO GMCL FOR YOUR MOBILE
<div class=block data-dynamic='{"dynamic_compact":"1","text":"4.6. GMCL Saturday Division 3","link_custom":"https://gmcl.test/pages/rules-d3"}'></div>
<div class=block data-dynamic='{"text":"4.40. GMCL20 Competition","link_custom":"https:\/\/gmcl.test\/pages\/rules-20"}'></div>
<div class=block data-dynamic='{"text":"External","link_custom":"https://evil.test/pages/rules-x"}'></div>
Proud Sponsors`
	links := discoverLinks(root, root.String(), raw)
	want := []string{"https://gmcl.test/pages/rules-20", "https://gmcl.test/pages/rules-d3"}
	if len(links) != len(want) || links[0] != want[0] || links[1] != want[1] {
		t.Fatalf("links=%v want %v", links, want)
	}
}

func TestDiscoverLinksUsesRulesContentAndAcceptsUnnumberedSlugs(t *testing.T) {
	root, _ := url.Parse("https://gmcl.test/pages/rules-main-menu")
	raw := `<a href="/pages/site-navigation">Global navigation</a>
WELCOME TO GMCL FOR YOUR MOBILE
<a href="
	/pages/category-3-registrations">Registrations</a>
<a href="/pages/match-forfeits?from=menu#section">Forfeits</a>
<a href="https://evil.test/pages/rules">Bad</a>
Proud Sponsors
<a href="/pages/sponsor">Sponsor</a>`
	links := discoverLinks(root, root.String(), raw)
	if len(links) != 2 {
		t.Fatalf("links=%v", links)
	}
	if links[0] != "https://gmcl.test/pages/category-3-registrations" || links[1] != "https://gmcl.test/pages/match-forfeits" {
		t.Fatalf("unexpected links=%v", links)
	}
}

func TestValidateCorpusRequiresEveryRuleGroup(t *testing.T) {
	var docs []parsedDocument
	for i := 1; i <= 8; i++ {
		var chunks []parsedChunk
		for j := 0; j < 4; j++ {
			chunks = append(chunks, parsedChunk{RuleReference: string(rune('0'+i)) + "." + string(rune('1'+j)), Content: "Enough rule content for validation and retrieval tests."})
		}
		docs = append(docs, parsedDocument{URL: "https://example.test/rules/" + string(rune('0'+i)), Text: "rules", Chunks: chunks})
	}
	if err := validateCorpus(docs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs = docs[:7]
	if err := validateCorpus(docs); err == nil {
		t.Fatal("expected missing group error")
	}
}

func TestExtractRuleReference(t *testing.T) {
	for input, want := range map[string]string{
		"What does rule 3.5.2 mean?": "3.5.2",
		"Penalty under 8.1":          "8.1",
		"no reference here":          "",
		// Bare digits are list numbers or counts, never rule references.
		"Penalties Section 1":                       "",
		"What happens if we get 3 yellow cards?":    "",
		"Rule 8":                                    "8",
		"8.1.1.4. Offence - Failure to submit form": "8.1.1.4",
		"See Rule 3 for eligibility, then 8.1":      "3",
	} {
		if got := extractRuleReference(input); got != want {
			t.Errorf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestParseHTMLKeepsSingleLineLeafRules(t *testing.T) {
	// Many published rules are one short numbered line. They must become
	// chunks with their text and reference intact — heading detection used to
	// swallow the line and then drop the empty chunk, erasing the rule.
	raw := `<html><title>Rules-Junior</title><body>WELCOME TO GMCL FOR YOUR MOBILE
<h2>7.10.2. U11 Pairs Cricket</h2>
<p>7.10.2.11.2. LBW : There are no LBW&rsquo;s in this competition</p>
<p>7.10.4.8.2. In the Summer Cup the full duration is 100 deliveries per innings</p>
Proud Sponsors</body></html>`
	doc := parseHTML("https://example.test/pages/rules-junior", raw)
	wantRefs := map[string]string{"7.10.2.11.2": "no LBW", "7.10.4.8.2": "100 deliveries"}
	for ref, needle := range wantRefs {
		found := false
		for _, chunk := range doc.Chunks {
			if chunk.RuleReference == ref && strings.Contains(chunk.Content, needle) {
				found = true
			}
		}
		if !found {
			t.Fatalf("leaf rule %s (%q) missing from chunks: %+v", ref, needle, doc.Chunks)
		}
	}
	// The ancestor heading supplies the age-group context that the leaf line
	// itself lacks — an "Are LBWs in play in U11?" search can only match this
	// chunk through "U11 Pairs Cricket" in its heading trail.
	for _, chunk := range doc.Chunks {
		if chunk.RuleReference == "7.10.2.11.2" && !strings.Contains(chunk.Heading, "U11 Pairs Cricket") {
			t.Fatalf("leaf chunk lost its ancestor heading: %q", chunk.Heading)
		}
	}
}

func TestIsJuniorRulesQueryTreatsAgeGroupsAsJunior(t *testing.T) {
	for _, question := range []string{"Are LBWs in play in U11 cricket?", "Are LBW in play in U/11 cricket", "How many overs does an Under 13 bowler get?"} {
		if !isJuniorRulesQuery(question) {
			t.Fatalf("age-group question %q was not routed to the junior rules", question)
		}
	}
	if isJuniorRulesQuery("Can a U15 play open age senior cricket?") {
		t.Fatal("cross-over question into senior cricket must not be restricted to Rule 7")
	}
}

func TestDeepestRuleReferencePrefersTheLeafLevel(t *testing.T) {
	heading := "7.10. LEAGUE AND SUMMER CUP RULES › 7.10.2. Under 11s › 7.10.2.11. Scoring"
	if got := deepestRuleReference(heading); got != "7.10.2.11" {
		t.Fatalf("deepest reference=%q", got)
	}
}

func TestParseHTMLDoesNotTreatSectionNumbersAsRuleReferences(t *testing.T) {
	raw := `<html><title>GMCL RULES - PENALTIES</title><body>WELCOME TO GMCL FOR YOUR MOBILE
<h1>GMCL RULES - PENALTIES</h1>
<h2>Penalties Section 1</h2><p>Debts to league or other clubs and late withdrawal of teams are covered by the penalty tables on the linked pages.</p>
<h2>8.1.1.4. Offence - Failure to submit complete starred player form</h2><p>Yellow for the club's highest placed team, and a red card when the club fails to submit by the subsequently required date.</p>
Proud Sponsors</body></html>`
	doc := parseHTML("https://example.test/pages/rules-pen-menu", raw)
	for _, chunk := range doc.Chunks {
		if chunk.RuleReference == "1" {
			t.Fatalf("section list number extracted as rule reference: %+v", chunk)
		}
	}
	foundDotted := false
	for _, chunk := range doc.Chunks {
		if chunk.RuleReference == "8.1.1.4" {
			foundDotted = true
		}
	}
	if !foundDotted {
		t.Fatalf("dotted penalty reference was not extracted: %+v", doc.Chunks)
	}
}

func TestBuildLexicalQueryExpandsSummerCampAndDropsFiller(t *testing.T) {
	got := buildLexicalQuery("Explain the junior rule and point out where it says one league game before Summer Camp")
	for _, term := range []string{"junior", "league", "game", "summer", "camp", "cup"} {
		if !strings.Contains(got, term) {
			t.Fatalf("query %q does not contain %q", got, term)
		}
	}
	for _, filler := range []string{"explain", "where", "rule", "one", "before"} {
		if strings.Contains(got, filler) {
			t.Fatalf("query %q retained filler %q", got, filler)
		}
	}
}

func TestCitationMatchesQuestionRejectsUnrelatedJuniorEvidence(t *testing.T) {
	question := "Explain the junior rule before the Summer Cup"
	if citationMatchesQuestion(question, Chunk{RuleReference: "1.5.2", Title: "Rules-1-5", Content: "Contact the junior board."}) {
		t.Fatal("unrelated Rule 1 citation was accepted for a junior-rules question")
	}
	if !citationMatchesQuestion(question, Chunk{RuleReference: "7.5.1.2", Title: "Rules-Junior"}) {
		t.Fatal("Rule 7 citation was rejected for a junior-rules question")
	}
}

func TestIsJuniorRulesQueryDoesNotHideOpenAgeRules(t *testing.T) {
	if !isJuniorRulesQuery("Explain the junior rule for the Summer Cup") {
		t.Fatal("expected explicit junior rules question to be routed to Rule 7")
	}
	if isJuniorRulesQuery("Can a junior play open age senior cricket?") {
		t.Fatal("cross-rule open-age question must not be restricted to Rule 7")
	}
	if !isJuniorRulesQuery("I mean the Under 13 Summer Cup") || !isJuniorRulesQuery("What about U15 summer cup eligibility?") {
		t.Fatal("junior age-group Summer Cup follow-up was not routed to Rule 7")
	}
}

func TestIsJuniorCupEligibilityQuery(t *testing.T) {
	if !isJuniorCupEligibilityQuery("Where does the junior rule say a player must play one League game before the Summer Cup?") {
		t.Fatal("expected Junior Cup eligibility intent")
	}
	if isJuniorCupEligibilityQuery("What are the bowling limits in the Summer Cup?") {
		t.Fatal("bowling question must not trigger Junior Cup entry routing")
	}
}

func TestFallbackClarificationQuestionsAreTargeted(t *testing.T) {
	questions := fallbackClarificationQuestions("Can a junior play in summer camp?")
	if len(questions) != 2 || !strings.Contains(strings.ToLower(questions[0]), "age group") || !strings.Contains(strings.ToLower(questions[1]), "summer cup") {
		t.Fatalf("unexpected questions: %v", questions)
	}
}

func TestNeedsPreviousQuestionOnlyForDependentFollowups(t *testing.T) {
	if !needsPreviousQuestion("Yes, that one") || !needsPreviousQuestion("What about Sunday?") {
		t.Fatal("short dependent follow-up did not retain the previous question")
	}
	if needsPreviousQuestion("What happens in bad weather?") || needsPreviousQuestion("Can a junior play senior cricket?") {
		t.Fatal("self-contained question was incorrectly tied to conversation history")
	}
}

func TestNeedsPreviousQuestionKeepsContextForConnectivesAndAnaphora(t *testing.T) {
	// Domain keywords used to sever context even when the question cannot
	// stand alone: "And what about the cup?" is meaningless without history.
	for _, question := range []string{"And what about the cup?", "But does that apply to junior players?", "Also in the league?", "Does that rule apply on Sunday?"} {
		if !needsPreviousQuestion(question) {
			t.Fatalf("dependent follow-up %q lost the previous question", question)
		}
	}
	if needsPreviousQuestion("How many overs does a junior bowl in a league match?") {
		t.Fatal("long self-contained question was tied to conversation history")
	}
}

func TestBuildLexicalQueryExpandsEverydayPhrasing(t *testing.T) {
	got := buildLexicalQuery("What if the game is rained off?")
	for _, term := range []string{"weather", "abandoned", "rained"} {
		if !strings.Contains(got, term) {
			t.Fatalf("query %q does not contain synonym %q", got, term)
		}
	}
	got = buildLexicalQuery("Can the keeper use a sub?")
	for _, term := range []string{"wicketkeeper", "substitute"} {
		if !strings.Contains(got, term) {
			t.Fatalf("query %q does not contain synonym %q", got, term)
		}
	}
	// Gold question 102: the rulebook writes a competition's format as
	// duration and deliveries ("100 deliveries per innings"), never "format".
	got = buildLexicalQuery("What format is the junior summer cup played in?")
	for _, term := range []string{"duration", "deliveries", "overs"} {
		if !strings.Contains(got, term) {
			t.Fatalf("query %q does not contain synonym %q", got, term)
		}
	}
	got = buildLexicalQuery("Is the summer cup hundred ball?")
	for _, term := range []string{"100", "deliveries"} {
		if !strings.Contains(got, term) {
			t.Fatalf("query %q does not contain synonym %q", got, term)
		}
	}
}

func TestStripInternalCitationMarkers(t *testing.T) {
	input := "Weather applies here. [chunk 491] It may use DLS. [Chunk 498] Final point. [505]"
	want := "Weather applies here. It may use DLS. Final point."
	if got := stripInternalCitationMarkers(input); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmbedRetriesTemporaryUpstreamErrors(t *testing.T) {
	attempts := 0
	embedding := strings.TrimSuffix(strings.Repeat("0,", 1536), ",")
	service := &Service{
		APIKey: "test-key", EmbedModel: "test-embedding-model",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts < 3 {
				return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("temporary")), Header: make(http.Header)}, nil
			}
			body := fmt.Sprintf(`{"data":[{"embedding":[%s]}]}`, embedding)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})},
	}
	got, err := service.embed(context.Background(), "rule question")
	if err != nil {
		t.Fatalf("embed returned error: %v", err)
	}
	if attempts != 3 || len(got) != 1536 {
		t.Fatalf("attempts=%d dimensions=%d", attempts, len(got))
	}
}
