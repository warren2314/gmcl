package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cricket-ground-feedback/internal/middleware"
	"cricket-ground-feedback/internal/rulesassistant"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const rulesConversationCookie = "gmcl_rules_session"

func (s *Server) rulesService() *rulesassistant.Service { return rulesassistant.New(s.DB) }

func (s *Server) handleRulesAssistantPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "A1 Rules Assistant")
		writeCaptainNav(w)
		fmt.Fprint(w, `<main class="rules-shell">
  <section class="rules-hero">
    <img class="rules-bot rules-bot-large" src="/images/gmcl-rules-bot.webp" alt="Friendly GMCL cricket rules robot">
    <div><p class="rules-kicker">Greater Manchester Cricket League</p><h1>A1 Rules Assistant</h1>
    <p>Ask a question in ordinary language. Answers are based only on the published GMCL rules and include links to their sources.</p></div>
  </section>
  <section class="rules-card" aria-label="Rules chat">
    <div id="rules-messages" class="rules-messages" aria-live="polite">
      <div class="rules-message assistant"><strong>Hello!</strong> Ask me about player eligibility, competitions, weather, junior cricket, penalties, or any other published GMCL rule.</div>
    </div>
    <div class="rules-scope" data-rules-scope aria-label="Optional match context">
      <span class="rules-scope-label">Match context <small>(optional)</small></span>
      <div class="rules-scope-groups">
        <div class="rules-scope-group" aria-label="Player level"><button type="button" data-scope-key="level" data-scope-value="senior" aria-pressed="false">Senior</button><button type="button" data-scope-key="level" data-scope-value="junior" aria-pressed="false">Junior</button></div>
        <div class="rules-scope-group" aria-label="Competition type"><button type="button" data-scope-key="competition" data-scope-value="league" aria-pressed="false">League</button><button type="button" data-scope-key="competition" data-scope-value="cup" aria-pressed="false">Cup</button></div>
      </div>
    </div>
    <form id="rules-form" class="rules-form">
      <label for="rules-question" class="visually-hidden">Your rules question</label>
      <textarea id="rules-question" maxlength="1200" rows="3" placeholder="For example: Can a starred player play in our Sunday team?" required></textarea>
      <div class="rules-form-row"><span id="rules-status" role="status"></span><button type="submit">Ask A1</button></div>
    </form>
  </section>
  <p class="rules-disclaimer">This assistant provides information from the published rules. It does not make an official GMCL ruling. For a formal decision, contact the league.</p>
</main>
`)
		pageFooter(w)
	}
}

type rulesChatRequest struct {
	Question    string `json:"question"`
	Level       string `json:"level"`
	Competition string `json:"competition"`
}

func (s *Server) handleRulesChat() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		var input rulesChatRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		input.Question = strings.TrimSpace(input.Question)
		if len(input.Question) < 3 || len(input.Question) > 1200 {
			http.Error(w, "question must be between 3 and 1200 characters", http.StatusBadRequest)
			return
		}
		scope, ok := normaliseRulesScope(input.Level, input.Competition)
		if !ok {
			http.Error(w, "invalid match context", http.StatusBadRequest)
			return
		}

		conversationID := s.rulesConversationID(w, r)
		abuseKey := rulesAbuseKey(r)
		var recent int
		_ = s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM rule_chat_messages m JOIN rule_chat_conversations c ON c.id=m.conversation_id WHERE c.abuse_key=$1 AND m.created_at>now()-interval '1 hour'`, abuseKey).Scan(&recent)
		if recent >= 30 {
			http.Error(w, "hourly question limit reached", http.StatusTooManyRequests)
			return
		}
		_, err := s.DB.Exec(ctx, `INSERT INTO rule_chat_conversations(id,abuse_key) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET updated_at=now(),expires_at=now()+interval '90 days'`, conversationID, abuseKey)
		if err != nil {
			http.Error(w, "rules assistant storage unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		// Questions about the asker's own disciplinary records use deterministic,
		// authenticated lookups. Captains remain restricted to their own team;
		// admins can name any club while testing from the protected /admin
		// endpoint. Rulebook questions that merely mention cards, bans, or fines
		// ("how many yellow cards before a suspension?") must continue to the
		// cited retrieval pipeline below, so record routing needs record intent,
		// not just a sanction term.
		recordIntent := isSanctionRecordQuestion(input.Question)
		var recordAnswer func() (string, []map[string]any, error)
		lookupCondition, lookupModel := "", ""
		lookupStatus := "Checking approved sanctions…"
		if strings.HasPrefix(r.URL.Path, "/admin/") {
			sanctionTerms := isSanctionLookupQuestion(input.Question)
			submissionTerms := hasSubmissionLookupTerms(input.Question)
			if _, sessionErr := getAdminSessionFromRequest(r); sessionErr == nil && (sanctionTerms || submissionTerms) {
				club, matched := importClub{}, false
				if clubs, clubsErr := s.loadSanctionLookupClubs(ctx); clubsErr == nil {
					club, matched = matchSanctionLookupClub(input.Question, clubs)
				}
				if sanctionTerms && (recordIntent || matched) {
					recordAnswer = func() (string, []map[string]any, error) {
						return s.adminSanctionsAnswer(ctx, input.Question, club, matched)
					}
					lookupCondition = "Authenticated admin lookup across approved club sanctions"
					lookupModel = "deterministic-admin-sanctions-v1"
				} else if submissionTerms && matched {
					recordAnswer = func() (string, []map[string]any, error) {
						return s.adminSubmissionAnswer(ctx, input.Question, club, matched)
					}
					lookupCondition = "Authenticated admin lookup for a named club"
					lookupModel = "deterministic-admin-submissions-v1"
					lookupStatus = "Checking submissions and sign-in links…"
				}
			}
		} else if captain, sessionErr := getCaptainSessionFromRequest(r); sessionErr == nil {
			if recordIntent {
				recordAnswer = func() (string, []map[string]any, error) {
					return s.captainSanctionsAnswer(ctx, captain, input.Question)
				}
				lookupCondition = "Authenticated lookup for your team only"
				lookupModel = "deterministic-sanctions-v1"
			} else if isSubmissionLookupQuestion(input.Question) {
				recordAnswer = func() (string, []map[string]any, error) {
					return s.captainSubmissionAnswer(ctx, captain, input.Question)
				}
				lookupCondition = "Authenticated lookup for your team only"
				lookupModel = "deterministic-submissions-v1"
				lookupStatus = "Checking your submissions and sign-in links…"
			}
		}
		if recordAnswer != nil {
			writeSSE(w, "status", map[string]any{"message": lookupStatus})
			flusher.Flush()
			answerText, caseCitations, lookupErr := recordAnswer()
			if lookupErr != nil {
				writeSSE(w, "error", map[string]any{"message": "I could not load the authorised record. Please try again shortly."})
				flusher.Flush()
				return
			}
			messageID := uuid.New()
			citationsJSON, _ := json.Marshal(caseCitations)
			_, storeErr := s.DB.Exec(ctx, `INSERT INTO rule_chat_messages(id,conversation_id,release_id,user_question,assistant_answer,clarification_needed,citations,retrieved_chunk_ids,model,prompt_tokens,completion_tokens,latency_ms)
				VALUES($1,$2,NULL,$3,$4,FALSE,$5,'[]'::jsonb,$6,0,0,0)`, messageID, conversationID, input.Question, answerText, citationsJSON, lookupModel)
			if storeErr != nil {
				writeSSE(w, "error", map[string]any{"message": "The lookup completed but could not be recorded."})
				flusher.Flush()
				return
			}
			writeSSE(w, "answer", map[string]any{"message_id": messageID, "answer": answerText, "clarification_needed": false, "clarification_questions": []string{}, "applicable_conditions": []string{lookupCondition}, "citations": caseCitations, "rules_as_of": time.Now().In(s.LondonLoc).Format("2 January 2006")})
			writeSSE(w, "done", map[string]bool{"ok": true})
			flusher.Flush()
			return
		}
		writeSSE(w, "status", map[string]any{"message": "Checking the published rules…"})
		flusher.Flush()
		conversationContext := ""
		previousUserQuestion := ""
		historyRows, _ := s.DB.Query(ctx, `SELECT user_question,assistant_answer FROM rule_chat_messages WHERE conversation_id=$1 ORDER BY created_at DESC LIMIT 3`, conversationID)
		if historyRows != nil {
			var previous []string
			for historyRows.Next() {
				var q, a string
				if historyRows.Scan(&q, &a) == nil {
					if previousUserQuestion == "" {
						previousUserQuestion = q
					}
					previous = append(previous, "User: "+q+"\nA1: "+a)
				}
			}
			historyRows.Close()
			if len(previous) > 0 {
				conversationContext = "Previous conversation turns (newest first):\n\n" + strings.Join(previous, "\n\n")
			}
		}
		started := time.Now()
		answer, err := s.rulesService().Answer(ctx, input.Question, scope, conversationContext, previousUserQuestion)
		latency := int(time.Since(started).Milliseconds())
		if err != nil {
			log.Printf("rules assistant answer failed: %v", err)
			writeSSE(w, "error", map[string]any{"message": "I could not complete that rules search. Please try again shortly."})
			flusher.Flush()
			return
		}
		messageID := uuid.New()
		citationsJSON, _ := json.Marshal(answer.Citations)
		chunkJSON, _ := json.Marshal(answer.RetrievedChunkIDs)
		_, err = s.DB.Exec(ctx, `INSERT INTO rule_chat_messages(id,conversation_id,release_id,user_question,assistant_answer,clarification_needed,citations,retrieved_chunk_ids,model,prompt_tokens,completion_tokens,latency_ms)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, messageID, conversationID, answer.ReleaseID, input.Question, answer.Text, answer.ClarificationNeeded, citationsJSON, chunkJSON, answer.Model, answer.PromptTokens, answer.CompletionTokens, latency)
		if err != nil {
			writeSSE(w, "error", map[string]any{"message": "The answer was generated but could not be recorded. Please try again."})
			flusher.Flush()
			return
		}
		writeSSE(w, "answer", map[string]any{"message_id": messageID, "answer": answer.Text, "clarification_needed": answer.ClarificationNeeded, "clarification_questions": answer.ClarificationQuestions, "applicable_conditions": answer.ApplicableConditions, "citations": answer.Citations, "rules_as_of": answer.RulesAsOf.Format("2 January 2006")})
		writeSSE(w, "done", map[string]bool{"ok": true})
		flusher.Flush()
	}
}

var sanctionQuestionWordRE = regexp.MustCompile(`[a-z']+`)

// normalisedQuestionText lowercases the question and strips punctuation so
// phrase markers like " me " still match at the end of a sentence ("for me?").
func normalisedQuestionText(question string) string {
	return " " + strings.Join(sanctionQuestionWordRE.FindAllString(strings.ToLower(question), -1), " ") + " "
}

func questionContainsAny(paddedQuestion string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(paddedQuestion, marker) {
			return true
		}
	}
	return false
}

// isSanctionLookupQuestion reports whether a question mentions disciplinary
// sanctions at all. Matching is on whole words so "abandoned" does not count
// as "ban". It is deliberately broad: isSanctionRecordQuestion decides whether
// the asker wants their actual records rather than the rulebook.
func isSanctionLookupQuestion(q string) bool {
	words := map[string]bool{}
	for _, word := range sanctionQuestionWordRE.FindAllString(strings.ToLower(q), -1) {
		words[word] = true
	}
	for _, term := range []string{"sanction", "sanctions", "card", "cards", "carded", "ban", "bans", "banned", "fine", "fines", "fined", "appeal", "appeals", "appealed", "totting", "suspension", "suspensions", "suspended", "deduction", "deductions", "docked"} {
		if words[term] {
			return true
		}
	}
	return false
}

// isSanctionRecordQuestion decides whether an authenticated question asks
// about the asker's actual disciplinary records ("why do we have a yellow
// card?") rather than what the rulebook says ("how many yellow cards before a
// suspension?"). Only record questions may use the deterministic database
// lookup; everything else must reach the cited rules pipeline. When phrasing
// is ambiguous the rules pipeline wins, because it can never leak or misstate
// a case record.
func isSanctionRecordQuestion(question string) bool {
	if !isSanctionLookupQuestion(question) {
		return false
	}
	q := normalisedQuestionText(question)
	if questionContainsAny(q, "what happens", " if we ", " if i ", " if a ", " can we ", " can i ", " can a ", " could ", " would ", " should ", " how do ", " how does ", " allowed ", " rule say", " rules say", " under rule ", " process ", " procedure ", " explain ", " tell me ", " what does ", " mean ", " fine to ", " fine if ", " okay ", " ok ") {
		return false
	}
	if questionContainsAny(q, " my ", " our ", " we ", " i ") {
		return true
	}
	// Receipt phrasing about a named person: "Why did Joe Bloggs get a red
	// card?". Generic subjects stay with the rulebook.
	if questionContainsAny(q, "why do ", "why does ", "why did ", "why has ", "why have ", "why was ", "why were ") &&
		questionContainsAny(q, " got ", " get ", " given ", " issued ", " received ", " receive ", " has ", " have ", " was ", " were ") {
		return !questionContainsAny(q, " a player ", " a team ", " a club ", " the league ", " gmcl ", " someone ", " anyone ")
	}
	return questionContainsAny(q, " show ", " list ")
}

// sanctionKindFilter narrows a record lookup to the kinds of sanction the
// question actually asks about. A nil slice means every kind; the noun is
// used in the reply ("card", "ban", "fine", "points", "sanction").
func sanctionKindFilter(question string) ([]string, string) {
	q := strings.ToLower(question)
	var kinds []string
	var nouns []string
	add := func(noun string, types ...string) {
		nouns = append(nouns, noun)
		for _, t := range types {
			duplicate := false
			for _, existing := range kinds {
				if existing == t {
					duplicate = true
					break
				}
			}
			if !duplicate {
				kinds = append(kinds, t)
			}
		}
	}
	if questionContainsAny(q, "card", "totting", "yellow", "red") {
		add("card", "yellow_card", "red_card", "suspended_red")
	}
	if questionContainsAny(q, "ban", "suspen") {
		add("ban", "player_ban", "team_ban", "suspended_red")
	}
	if questionContainsAny(q, "fine", "fined") {
		add("fine", "fine")
	}
	if questionContainsAny(q, "deduct", "docked", "points") {
		add("points", "card_points", "points_adjustment")
	}
	if len(nouns) != 1 {
		if len(nouns) == 0 {
			return nil, "sanction"
		}
		return kinds, "sanction"
	}
	return kinds, nouns[0]
}

type sanctionRecordRow struct {
	CaseID                                                   int64
	Ref, Team, Player, Reason, Status, Effect, RuleRef, Date string
	Points                                                   int
}

func sanctionRecordLine(row sanctionRecordRow, includeTeam bool) string {
	detail := fmt.Sprintf("%s: %s", row.Ref, effectLabel(row.Effect))
	if row.Player != "" {
		detail += " for " + row.Player
	}
	if includeTeam && row.Team != "" {
		detail += " (" + row.Team + ")"
	}
	detail += " — " + row.Reason + " (" + row.Status
	if row.Date != "" {
		detail += ", effective " + row.Date
	}
	if row.Points != 0 {
		detail += fmt.Sprintf(", %d-point deduction", row.Points)
	}
	if row.RuleRef != "" {
		detail += ", rule " + row.RuleRef
	}
	detail += ")"
	return detail
}

// focusSanctionRows narrows records to any player actually named in the
// question. Matching compares the recorded names against the question — never
// a name parsed out of the question — so it stays deterministic. Surnames that
// double as everyday cricket words are ignored to avoid false focus.
func focusSanctionRows(question string, rows []sanctionRecordRow) []sanctionRecordRow {
	q := normalisedQuestionText(question)
	commonWords := map[string]bool{"ball": true, "wood": true, "field": true, "green": true, "day": true, "may": true, "west": true, "east": true, "north": true, "south": true, "young": true, "long": true, "small": true, "little": true, "white": true, "black": true, "brown": true}
	var focused []sanctionRecordRow
	for _, row := range rows {
		name := strings.ToLower(strings.TrimSpace(row.Player))
		if name == "" {
			continue
		}
		if strings.Contains(q, " "+name+" ") {
			focused = append(focused, row)
			continue
		}
		parts := strings.Fields(name)
		surname := parts[len(parts)-1]
		if len(surname) >= 4 && !commonWords[surname] && strings.Contains(q, " "+surname+" ") {
			focused = append(focused, row)
		}
	}
	if len(focused) == 0 {
		return rows
	}
	return focused
}

func (s *Server) loadSanctionLookupClubs(ctx context.Context) ([]importClub, error) {
	clubRows, err := s.DB.Query(ctx, `SELECT id,name FROM clubs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer clubRows.Close()
	clubs := []importClub{}
	for clubRows.Next() {
		var club importClub
		if err = clubRows.Scan(&club.ID, &club.Name); err != nil {
			return nil, err
		}
		clubs = append(clubs, club)
	}
	return clubs, clubRows.Err()
}

func matchSanctionLookupClub(question string, clubs []importClub) (importClub, bool) {
	normalisedQuestion := " " + normaliseImportName(question) + " "
	best := importClub{}
	bestLength := 0
	for _, club := range clubs {
		name := normaliseImportName(club.Name)
		if name != "" && strings.Contains(normalisedQuestion, " "+name+" ") && len(name) > bestLength {
			best = club
			bestLength = len(name)
		}
	}
	return best, bestLength > 0
}

func filterSanctionRowsByKind(rows []sanctionRecordRow, kinds []string) []sanctionRecordRow {
	if kinds == nil {
		return rows
	}
	var out []sanctionRecordRow
	for _, row := range rows {
		for _, kind := range kinds {
			if row.Effect == kind {
				out = append(out, row)
				break
			}
		}
	}
	return out
}

func (s *Server) adminSanctionsAnswer(ctx context.Context, question string, club importClub, matched bool) (string, []map[string]any, error) {
	if !matched {
		return "Please name the club in your sanctions question, for example: “Why does Woodley have cards?” I will only return approved records and will not expose evidence, correspondence, reporter details, or internal notes.", nil, nil
	}
	kinds, kindNoun := sanctionKindFilter(question)
	rows, err := s.DB.Query(ctx, `
		SELECT c.id,c.reference,COALESCE(t.name,''),COALESCE(NULLIF(e.player_name,''),COALESCE(c.player_name,'')),COALESCE(c.public_summary,''),e.status,e.effect_type,
		       COALESCE(e.points,0),COALESCE(d.rule_reference,''),
		       COALESCE(to_char(COALESCE(e.starts_at,c.match_date::timestamptz,c.approved_at) AT TIME ZONE 'Europe/London','DD Mon YYYY'),'')
		FROM sanction_cases c
		JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='approved'
		JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id
		LEFT JOIN teams t ON t.id=c.team_id
		WHERE c.club_id=$1 AND c.status IN ('approved','published','appealed','closed')
		  AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id)
		ORDER BY COALESCE(e.starts_at,c.match_date::timestamptz,c.approved_at) DESC LIMIT 25`, club.ID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var all []sanctionRecordRow
	for rows.Next() {
		var row sanctionRecordRow
		if err = rows.Scan(&row.CaseID, &row.Ref, &row.Team, &row.Player, &row.Reason, &row.Status, &row.Effect, &row.Points, &row.RuleRef, &row.Date); err != nil {
			return "", nil, err
		}
		all = append(all, row)
	}
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	if len(all) == 0 {
		return fmt.Sprintf("I found no approved sanctions for %s. Staged imports, draft proposals, evidence, and internal notes are deliberately excluded from this lookup.", club.Name), []map[string]any{}, nil
	}
	shown := focusSanctionRows(question, filterSanctionRowsByKind(all, kinds))
	intro := fmt.Sprintf("%s has %d approved %s record(s). These are the recorded reasons:", club.Name, len(shown), kindNoun)
	if len(shown) == 0 {
		shown = all
		intro = fmt.Sprintf("%s has no approved %s records. It does have %d other approved sanction record(s):", club.Name, kindNoun, len(shown))
	} else if len(shown) < len(all) {
		intro = fmt.Sprintf("%s has %d approved %s record(s) matching your question (of %d approved records in total). These are the recorded reasons:", club.Name, len(shown), kindNoun, len(all))
	}
	lines := make([]string, 0, len(shown))
	citations := make([]map[string]any, 0, len(shown))
	for _, row := range shown {
		lines = append(lines, sanctionRecordLine(row, true))
		citations = append(citations, map[string]any{"title": "Case " + row.Ref, "url": fmt.Sprintf("/admin/cases/%d", row.CaseID), "rule_reference": row.RuleRef})
	}
	return intro + "\n\n" + strings.Join(lines, "\n") + "\n\nThis admin lookup excludes evidence, correspondence, reporter details, and internal notes. Open a cited case to inspect the authorised case record.", citations, nil
}

func (s *Server) captainSanctionsAnswer(ctx context.Context, sess *captainSession, question string) (string, []map[string]any, error) {
	kinds, kindNoun := sanctionKindFilter(question)
	rows, err := s.DB.Query(ctx, `
		SELECT c.reference,COALESCE(NULLIF(e.player_name,''),COALESCE(c.player_name,'')),c.public_summary,c.public_status,e.effect_type,COALESCE(e.points,0),
		       COALESCE(d.rule_reference,''),COALESCE(to_char(e.starts_at AT TIME ZONE 'Europe/London','DD Mon YYYY'),'')
		FROM sanction_cases c
		JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='approved'
		JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id
		WHERE c.team_id=$1 AND c.status IN ('approved','published','appealed','closed')
		  AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id)
		ORDER BY COALESCE(e.starts_at,c.approved_at) DESC LIMIT 25`, sess.TeamID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var all []sanctionRecordRow
	yellowBalance, redCount := 0, 0
	for rows.Next() {
		var row sanctionRecordRow
		if err = rows.Scan(&row.Ref, &row.Player, &row.Reason, &row.Status, &row.Effect, &row.Points, &row.RuleRef, &row.Date); err != nil {
			return "", nil, err
		}
		all = append(all, row)
		if row.Effect == "yellow_card" && (row.Status == "active" || row.Status == "suspended") {
			yellowBalance++
		}
		if row.Effect == "red_card" {
			redCount++
		}
	}
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	if len(all) == 0 {
		return "There are no approved sanctions recorded for your team. I can still answer a general question about the published rules.", []map[string]any{}, nil
	}
	shown := focusSanctionRows(question, filterSanctionRowsByKind(all, kinds))
	intro := fmt.Sprintf("Your team has %d approved %s record(s).", len(shown), kindNoun)
	if len(shown) == 0 {
		shown = all
		intro = fmt.Sprintf("Your team has no approved %s records. It does have %d other approved sanction record(s).", kindNoun, len(shown))
	} else if len(shown) < len(all) {
		intro = fmt.Sprintf("Your team has %d approved %s record(s) matching your question, of %d approved records in total.", len(shown), kindNoun, len(all))
	}
	// The card balance only helps when the captain asked about cards (or asked
	// generally); a fines question should not lead with yellow-card arithmetic.
	includeBalance := kinds == nil
	for _, kind := range kinds {
		if kind == "yellow_card" || kind == "red_card" || kind == "suspended_red" {
			includeBalance = true
		}
	}
	if includeBalance {
		remaining := 3 - (yellowBalance % 3)
		if remaining == 0 {
			remaining = 3
		}
		intro += fmt.Sprintf(" The current recorded balance is %d effective yellow card(s) and %d red card(s); on that balance, %d further yellow card(s) would reach the next three-yellow threshold.", yellowBalance, redCount, remaining)
	}
	lines := make([]string, 0, len(shown))
	citations := make([]map[string]any, 0, len(shown))
	for _, row := range shown {
		lines = append(lines, sanctionRecordLine(row, false))
		citations = append(citations, map[string]any{"title": "Case " + row.Ref, "url": "/captain/discipline", "rule_reference": row.RuleRef})
	}
	return intro + "\n\n" + strings.Join(lines, "\n") + "\n\nThis lookup excludes evidence, correspondence, reporter details, and internal notes. Quote the case reference if you need to challenge or appeal a record. If you wanted to know what the rulebook says instead, ask the question in general terms — for example: “What happens after three yellow cards?”", citations, nil
}

func normaliseRulesScope(level, competition string) (string, bool) {
	level = strings.ToLower(strings.TrimSpace(level))
	competition = strings.ToLower(strings.TrimSpace(competition))
	levels := map[string]string{"": "", "senior": "Senior rules", "junior": "Junior rules"}
	competitions := map[string]string{"": "", "league": "League match", "cup": "Cup match"}
	levelLabel, levelOK := levels[level]
	competitionLabel, competitionOK := competitions[competition]
	if !levelOK || !competitionOK {
		return "", false
	}
	parts := make([]string, 0, 2)
	if levelLabel != "" {
		parts = append(parts, levelLabel)
	}
	if competitionLabel != "" {
		parts = append(parts, competitionLabel)
	}
	return strings.Join(parts, "; "), true
}

func (s *Server) handleRulesFeedback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		var in struct {
			MessageID string `json:"message_id"`
			Rating    string `json:"rating"`
			Comment   string `json:"comment"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		id, err := uuid.Parse(in.MessageID)
		if err != nil {
			http.Error(w, "invalid message", 400)
			return
		}
		if in.Rating != "helpful" && in.Rating != "unhelpful" && in.Rating != "report" {
			http.Error(w, "invalid rating", 400)
			return
		}
		in.Comment = strings.TrimSpace(in.Comment)
		if len(in.Comment) > 1000 {
			http.Error(w, "comment too long", 400)
			return
		}
		conversationID := s.rulesConversationID(w, r)
		tag, err := s.DB.Exec(r.Context(), `INSERT INTO rule_chat_feedback(message_id,rating,comment)
			SELECT m.id,$2,$3 FROM rule_chat_messages m WHERE m.id=$1 AND m.conversation_id=$4
			ON CONFLICT(message_id,rating) DO UPDATE SET comment=EXCLUDED.comment`, id, in.Rating, nullString(in.Comment), conversationID)
		if err != nil || tag.RowsAffected() == 0 {
			http.Error(w, "message not found", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}
}

func (s *Server) handleInternalSyncRules() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			service := s.rulesService()
			_, _ = service.PurgeExpired(ctx)
			_, _ = service.Sync(ctx, nil)
		}()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "sync_started"})
	}
}

func (s *Server) handleAdminRulesAssistant() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		releases, _ := s.rulesService().ListReleases(ctx, 20)
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		var testChunks []rulesassistant.Chunk
		if query != "" {
			_, _, testChunks, _ = s.rulesService().Retrieve(ctx, query, 8)
		}
		type sourceRow struct{ Title, URL, Updated, Hash string }
		var sources []sourceRow
		sourceRows, _ := s.DB.Query(ctx, `SELECT d.title,d.canonical_url,COALESCE(d.page_updated_label,''),d.content_hash FROM rule_documents d JOIN rule_releases r ON r.id=d.release_id WHERE r.status='active' ORDER BY d.title`)
		if sourceRows != nil {
			defer sourceRows.Close()
			for sourceRows.Next() {
				var x sourceRow
				if sourceRows.Scan(&x.Title, &x.URL, &x.Updated, &x.Hash) == nil {
					sources = append(sources, x)
				}
			}
		}
		type chatRow struct {
			ID, Question, Answer, Model string
			Created                     time.Time
			Latency                     int
			Feedback                    string
		}
		rows, _ := s.DB.Query(ctx, `SELECT m.id::text,m.user_question,m.assistant_answer,m.model,m.created_at,m.latency_ms,COALESCE(string_agg(f.rating,', '),'') FROM rule_chat_messages m LEFT JOIN rule_chat_feedback f ON f.message_id=m.id GROUP BY m.id ORDER BY m.created_at DESC LIMIT 50`)
		var chats []chatRow
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var c chatRow
				if rows.Scan(&c.ID, &c.Question, &c.Answer, &c.Model, &c.Created, &c.Latency, &c.Feedback) == nil {
					chats = append(chats, c)
				}
			}
		}
		csrf := middleware.CSRFToken(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pageHead(w, "A1 Rules Assistant Admin")
		writeAdminNav(w, csrf, r.URL.Path, adminRoleForRequest(r))
		syncButton := ""
		if adminRoleForRequest(r) == "super_admin" {
			syncButton = fmt.Sprintf(`<form method="POST" action="/admin/rules-assistant/sync"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-primary">Sync rules now</button></form>`, escapeHTML(csrf))
		}
		fmt.Fprintf(w, `<div class="container py-4"><div class="d-flex justify-content-between align-items-center mb-3"><div><h2>A1 Rules Assistant</h2><p class="text-muted mb-0">Published sources, sync health, and recent answer quality.</p></div>%s</div>`, syncButton)
		fmt.Fprint(w, `<div class="card mb-4"><div class="card-header fw-semibold">Rules snapshots</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>ID</th><th>Status</th><th>Started</th><th>Sources</th><th>Chunks</th><th>Changed</th><th>Result</th><th></th></tr></thead><tbody>`)
		for _, rel := range releases {
			action := ""
			if rel.Status == "archived" && adminRoleForRequest(r) == "super_admin" {
				action = fmt.Sprintf(`<form method="POST" action="/admin/rules-assistant/releases/%d/activate"><input type="hidden" name="csrf_token" value="%s"><button class="btn btn-sm btn-outline-secondary">Activate</button></form>`, rel.ID, escapeHTML(csrf))
			}
			fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td class="text-danger">%s</td><td>%s</td></tr>`, rel.ID, escapeHTML(rel.Status), rel.StartedAt.In(s.LondonLoc).Format("02 Jan 15:04"), rel.SourceCount, rel.ChunkCount, rel.ChangedSourceCount, escapeHTML(rel.ErrorMessage), action)
		}
		fmt.Fprint(w, `</tbody></table></div></div><div class="card mb-4"><div class="card-header fw-semibold">Active sources</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Page</th><th>Reported update</th><th>Content hash</th></tr></thead><tbody>`)
		for _, source := range sources {
			fmt.Fprintf(w, `<tr><td><a href="%s" target="_blank" rel="noopener">%s</a></td><td>%s</td><td><code>%s</code></td></tr>`, escapeHTML(source.URL), escapeHTML(source.Title), escapeHTML(source.Updated), escapeHTML(shortText(source.Hash, 12)))
		}
		if len(sources) == 0 {
			fmt.Fprint(w, `<tr><td colspan="3" class="text-muted">No active rules snapshot. Run the initial sync.</td></tr>`)
		}
		fmt.Fprintf(w, `</tbody></table></div></div><div class="card mb-4"><div class="card-header fw-semibold">Test retrieval</div><div class="card-body"><form method="GET" class="d-flex gap-2"><input class="form-control" name="q" value="%s" placeholder="Enter a rules question"><button class="btn btn-outline-primary">Retrieve</button></form>`, escapeHTML(query))
		if query != "" {
			fmt.Fprint(w, `<ol class="mt-3 mb-0">`)
			for _, chunk := range testChunks {
				fmt.Fprintf(w, `<li class="mb-2"><strong>Rule %s</strong> · score %.3f · <a href="%s" target="_blank" rel="noopener">%s</a><div class="small text-muted">%s</div></li>`, escapeHTML(chunk.RuleReference), chunk.Score, escapeHTML(chunk.URL), escapeHTML(chunk.Title), escapeHTML(shortText(chunk.Content, 300)))
			}
			fmt.Fprint(w, `</ol>`)
		}
		fmt.Fprint(w, `</div></div><div class="card"><div class="card-header fw-semibold">Recent conversations</div><div class="table-responsive"><table class="table table-sm mb-0"><thead><tr><th>Time</th><th>Question</th><th>Answer</th><th>Feedback</th><th>Latency</th></tr></thead><tbody>`)
		for _, c := range chats {
			fmt.Fprintf(w, `<tr><td class="text-nowrap">%s</td><td>%s</td><td>%s</td><td>%s</td><td>%d ms</td></tr>`, c.Created.In(s.LondonLoc).Format("02 Jan 15:04"), escapeHTML(c.Question), escapeHTML(shortText(c.Answer, 260)), escapeHTML(c.Feedback), c.Latency)
		}
		fmt.Fprint(w, `</tbody></table></div></div></div>`)
		pageFooter(w)
	}
}

func (s *Server) handleAdminRulesSync() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admin := s.resolveAdminID(r)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			_, _ = s.rulesService().Sync(ctx, admin)
		}()
		http.Redirect(w, r, "/admin/rules-assistant?sync=started", http.StatusSeeOther)
	}
}
func (s *Server) handleAdminRulesRollback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if id < 1 || s.rulesService().Rollback(r.Context(), id) != nil {
			http.Error(w, "release could not be activated", 400)
			return
		}
		http.Redirect(w, r, "/admin/rules-assistant", http.StatusSeeOther)
	}
}

func (s *Server) rulesConversationID(w http.ResponseWriter, r *http.Request) uuid.UUID {
	if c, err := r.Cookie(rulesConversationCookie); err == nil {
		if id, e := uuid.Parse(c.Value); e == nil {
			return id
		}
	}
	id := uuid.New()
	http.SetCookie(w, &http.Cookie{Name: rulesConversationCookie, Value: id.String(), Path: "/", MaxAge: 90 * 24 * 3600, HttpOnly: true, Secure: os.Getenv("APP_ENV") != "dev", SameSite: http.SameSiteLaxMode})
	return id
}
func rulesAbuseKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	secret := os.Getenv("RULES_ASSISTANT_SECRET")
	if secret == "" {
		secret = os.Getenv("ADMIN_SESSION_SECRET")
	}
	day := time.Now().UTC().Format("2006-01-02")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(day + "|" + host))
	return hex.EncodeToString(mac.Sum(nil))
}
func writeSSE(w http.ResponseWriter, event string, value any) {
	body, _ := json.Marshal(value)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func shortText(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n] + "…"
}
