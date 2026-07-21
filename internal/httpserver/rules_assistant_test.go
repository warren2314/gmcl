package httpserver

import (
	"strings"
	"testing"
)

func TestRulesAssistantEnabledDefaultsOff(t *testing.T) {
	t.Setenv("RULES_ASSISTANT_ENABLED", "")
	if rulesAssistantEnabled() {
		t.Fatal("rules assistant must default to disabled")
	}
}

func TestRulesAssistantEnabledExplicitly(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RULES_ASSISTANT_ENABLED", value)
			if !rulesAssistantEnabled() {
				t.Fatalf("expected %q to enable rules assistant", value)
			}
		})
	}
}

func TestNormaliseRulesScope(t *testing.T) {
	got, ok := normaliseRulesScope(" Junior ", "CUP")
	if !ok || got != "Junior rules; Cup match" {
		t.Fatalf("scope=%q ok=%v", got, ok)
	}
	if _, ok := normaliseRulesScope("academy", "cup"); ok {
		t.Fatal("invalid scope was accepted")
	}
}

func TestSanctionLookupRecognisesGenericCardQuestion(t *testing.T) {
	if !isSanctionLookupQuestion("Why does Woodley have cards?") {
		t.Fatal("generic card question was not recognised as a sanctions lookup")
	}
}

func TestMatchSanctionLookupClubUsesNamedLongestMatch(t *testing.T) {
	clubs := []importClub{
		{ID: 1, Name: "Bolton Cricket Club"},
		{ID: 2, Name: "Bolton Indians Cricket Club"},
		{ID: 3, Name: "Woodley Cricket Club"},
	}
	got, ok := matchSanctionLookupClub("Why does Bolton Indians have cards?", clubs)
	if !ok || got.ID != 2 {
		t.Fatalf("matched club = %#v, ok=%v; want Bolton Indians", got, ok)
	}
	if _, ok = matchSanctionLookupClub("Why does this team have cards?", clubs); ok {
		t.Fatal("question without a named club unexpectedly matched")
	}
}

func TestMatchSanctionLookupClubTreatsAndAsAmpersand(t *testing.T) {
	clubs := []importClub{{ID: 8, Name: "Deane & Derby CC"}}
	for _, question := range []string{"Why does Deane and Derby have cards?", "Why does Deane & Derby have cards?"} {
		got, ok := matchSanctionLookupClub(question, clubs)
		if !ok || got.ID != 8 {
			t.Fatalf("question %q matched %#v, ok=%v", question, got, ok)
		}
	}
}

func TestIsSanctionLookupQuestionMatchesWholeWordsOnly(t *testing.T) {
	for question, want := range map[string]bool{
		"Why do we have a yellow card?":              true,
		"Has our player been banned?":                true,
		"Do we owe a fine?":                          true,
		"The match was abandoned, do we get points?": false, // "abandoned" must not count as "ban"
		"What is the tea interval rule?":             false,
		"Explain the totting up process":             true,
		"Why was our team docked points?":            true,
		"When does a suspension start?":              true,
	} {
		if got := isSanctionLookupQuestion(question); got != want {
			t.Errorf("isSanctionLookupQuestion(%q)=%v want %v", question, got, want)
		}
	}
}

func TestIsSanctionRecordQuestionSeparatesRecordsFromRulebook(t *testing.T) {
	recordQuestions := []string{
		"Why do we have a yellow card?",
		"How many cards do we have?",
		"Do I have any bans?",
		"What are our current sanctions?",
		"Why did Joe Bloggs get a red card?",
		"Why does Woodley have cards?",
		"Show sanctions for Woodley",
		"List our fines",
	}
	for _, question := range recordQuestions {
		if !isSanctionRecordQuestion(question) {
			t.Errorf("expected record intent for %q", question)
		}
	}
	rulebookQuestions := []string{
		"How many yellow cards before a suspension?",
		"What happens if we get 3 yellow cards?",
		"Can we appeal a card?",
		"Can a player appeal a red card?",
		"What is the fine for a late start?",
		"How does totting up work?",
		"Explain the card system to me",
		"Why would a player be banned?",
		"Are we fine to declare early?",
		"When does a suspension start?",
		"The match was abandoned, do we get points?",
	}
	for _, question := range rulebookQuestions {
		if isSanctionRecordQuestion(question) {
			t.Errorf("expected rulebook routing for %q", question)
		}
	}
}

func TestSanctionKindFilterNarrowsToTheKindAsked(t *testing.T) {
	kinds, noun := sanctionKindFilter("Why do we have a yellow card?")
	if noun != "card" || len(kinds) != 3 {
		t.Fatalf("card question: kinds=%v noun=%q", kinds, noun)
	}
	kinds, noun = sanctionKindFilter("List our fines")
	if noun != "fine" || len(kinds) != 1 || kinds[0] != "fine" {
		t.Fatalf("fine question: kinds=%v noun=%q", kinds, noun)
	}
	kinds, noun = sanctionKindFilter("Show our sanctions")
	if kinds != nil || noun != "sanction" {
		t.Fatalf("generic question: kinds=%v noun=%q", kinds, noun)
	}
	kinds, noun = sanctionKindFilter("Do we have cards or fines?")
	if noun != "sanction" || len(kinds) != 4 {
		t.Fatalf("mixed question: kinds=%v noun=%q", kinds, noun)
	}
}

func TestFocusSanctionRowsMatchesRecordedNamesOnly(t *testing.T) {
	rows := []sanctionRecordRow{
		{Ref: "S-1", Player: "Joe Bloggs", Effect: "yellow_card"},
		{Ref: "S-2", Player: "Sam Ball", Effect: "red_card"},
		{Ref: "S-3", Player: "", Effect: "fine"},
	}
	focused := focusSanctionRows("Why did Joe Bloggs get a card?", rows)
	if len(focused) != 1 || focused[0].Ref != "S-1" {
		t.Fatalf("full-name focus failed: %+v", focused)
	}
	focused = focusSanctionRows("Why did Bloggs get a card?", rows)
	if len(focused) != 1 || focused[0].Ref != "S-1" {
		t.Fatalf("surname focus failed: %+v", focused)
	}
	// "ball" is everyday cricket vocabulary, so it must not focus on Sam Ball.
	focused = focusSanctionRows("Why do we have a card for throwing the ball?", rows)
	if len(focused) != 3 {
		t.Fatalf("common-word surname wrongly focused: %+v", focused)
	}
	focused = focusSanctionRows("Show our sanctions", rows)
	if len(focused) != 3 {
		t.Fatalf("no named player must return every row: %+v", focused)
	}
}

func TestFilterSanctionRowsByKind(t *testing.T) {
	rows := []sanctionRecordRow{
		{Ref: "S-1", Effect: "yellow_card"},
		{Ref: "S-2", Effect: "fine"},
		{Ref: "S-3", Effect: "player_ban"},
	}
	if got := filterSanctionRowsByKind(rows, nil); len(got) != 3 {
		t.Fatalf("nil filter must keep every row, got %d", len(got))
	}
	got := filterSanctionRowsByKind(rows, []string{"yellow_card", "red_card", "suspended_red"})
	if len(got) != 1 || got[0].Ref != "S-1" {
		t.Fatalf("card filter failed: %+v", got)
	}
}

func TestSanctionRecordLineIncludesPlayerAndRule(t *testing.T) {
	line := sanctionRecordLine(sanctionRecordRow{Ref: "S-9", Player: "Joe Bloggs", Team: "1st XI", Reason: "Dissent", Status: "active", Effect: "yellow_card", RuleRef: "8.2", Date: "01 Jun 2026", Points: 5}, true)
	for _, want := range []string{"S-9", "Yellow card", "Joe Bloggs", "1st XI", "Dissent", "active", "01 Jun 2026", "5-point deduction", "rule 8.2"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q missing %q", line, want)
		}
	}
}
