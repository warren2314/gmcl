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
		statusMessage := "Checking the published rules…"
		if strings.HasPrefix(r.URL.Path, "/admin/") && isSanctionLookupQuestion(input.Question) {
			statusMessage = "Checking approved sanctions…"
		}
		writeSSE(w, "status", map[string]any{"message": statusMessage})
		flusher.Flush()
		// Sanction questions use deterministic, authenticated lookups. Captains
		// remain restricted to their own team; admins can name any club while
		// testing from the protected /admin endpoint.
		if isSanctionLookupQuestion(input.Question) {
			var answerText, lookupCondition, lookupModel string
			var caseCitations []map[string]any
			var lookupErr error
			authorisedLookup := false
			if strings.HasPrefix(r.URL.Path, "/admin/") {
				if _, sessionErr := getAdminSessionFromRequest(r); sessionErr == nil {
					authorisedLookup = true
					answerText, caseCitations, lookupErr = s.adminSanctionsAnswer(ctx, input.Question)
					lookupCondition = "Authenticated admin lookup across approved club sanctions"
					lookupModel = "deterministic-admin-sanctions-v1"
				}
			} else if captain, sessionErr := getCaptainSessionFromRequest(r); sessionErr == nil {
				authorisedLookup = true
				answerText, caseCitations, lookupErr = s.captainSanctionsAnswer(ctx, captain)
				lookupCondition = "Authenticated lookup for your team only"
				lookupModel = "deterministic-sanctions-v1"
			}
			if authorisedLookup {
				if lookupErr != nil {
					writeSSE(w, "error", map[string]any{"message": "I could not load the authorised sanction record. Please try again shortly."})
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
		}
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

func isSanctionLookupQuestion(q string) bool {
	q = strings.ToLower(q)
	for _, term := range []string{"sanction", "card", "ban", "fine", "points deduction", "appeal", "totting"} {
		if strings.Contains(q, term) {
			return true
		}
	}
	return false
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

func (s *Server) adminSanctionsAnswer(ctx context.Context, question string) (string, []map[string]any, error) {
	clubRows, err := s.DB.Query(ctx, `SELECT id,name FROM clubs ORDER BY name`)
	if err != nil {
		return "", nil, err
	}
	clubs := []importClub{}
	for clubRows.Next() {
		var club importClub
		if err = clubRows.Scan(&club.ID, &club.Name); err != nil {
			clubRows.Close()
			return "", nil, err
		}
		clubs = append(clubs, club)
	}
	if err = clubRows.Err(); err != nil {
		clubRows.Close()
		return "", nil, err
	}
	clubRows.Close()
	club, matched := matchSanctionLookupClub(question, clubs)
	if !matched {
		return "Please name the club in your sanctions question, for example: “Why does Woodley have cards?” I will only return approved records and will not expose evidence, correspondence, reporter details, or internal notes.", nil, nil
	}

	cardsOnly := strings.Contains(strings.ToLower(question), "card") || strings.Contains(strings.ToLower(question), "totting")
	rows, err := s.DB.Query(ctx, `
		SELECT c.id,c.reference,COALESCE(t.name,''),COALESCE(c.public_summary,''),e.status,e.effect_type,
		       COALESCE(e.points,0),COALESCE(d.rule_reference,''),
		       COALESCE(to_char(COALESCE(e.starts_at,c.match_date::timestamptz,c.approved_at) AT TIME ZONE 'Europe/London','DD Mon YYYY'),'')
		FROM sanction_cases c
		JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='approved'
		JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id
		LEFT JOIN teams t ON t.id=c.team_id
		WHERE c.club_id=$1 AND c.status IN ('approved','published','appealed','closed')
		  AND (NOT $2::boolean OR e.effect_type IN ('yellow_card','red_card','suspended_red'))
		  AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id)
		ORDER BY COALESCE(e.starts_at,c.match_date::timestamptz,c.approved_at) DESC LIMIT 20`, club.ID, cardsOnly)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	lines := []string{}
	citations := []map[string]any{}
	for rows.Next() {
		var caseID int64
		var ref, team, reason, status, effect, ruleRef, date string
		var points int
		if err = rows.Scan(&caseID, &ref, &team, &reason, &status, &effect, &points, &ruleRef, &date); err != nil {
			return "", nil, err
		}
		detail := fmt.Sprintf("%s: %s", ref, effectLabel(effect))
		if team != "" {
			detail += " for " + team
		}
		detail += " — " + reason + " (" + status
		if date != "" {
			detail += ", effective " + date
		}
		if points != 0 {
			detail += fmt.Sprintf(", %d-point deduction", points)
		}
		if ruleRef != "" {
			detail += ", rule " + ruleRef
		}
		detail += ")"
		lines = append(lines, detail)
		citations = append(citations, map[string]any{"title": "Case " + ref, "url": fmt.Sprintf("/admin/cases/%d", caseID), "rule_reference": ruleRef})
	}
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	if len(lines) == 0 {
		kind := "sanctions"
		if cardsOnly {
			kind = "card sanctions"
		}
		return fmt.Sprintf("I found no approved %s for %s. Staged imports, draft proposals, evidence, and internal notes are deliberately excluded from this lookup.", kind, club.Name), citations, nil
	}
	kind := "approved sanction"
	if cardsOnly {
		kind = "approved card"
	}
	return fmt.Sprintf("%s has %d %s record(s). These are the recorded reasons:\n\n%s\n\nThis admin lookup excludes evidence, correspondence, reporter details, and internal notes. Open a cited case to inspect the authorised case record.", club.Name, len(lines), kind, strings.Join(lines, "\n")), citations, nil
}

func (s *Server) captainSanctionsAnswer(ctx context.Context, sess *captainSession) (string, []map[string]any, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT c.reference,c.public_summary,c.public_status,e.effect_type,COALESCE(e.points,0),
		       COALESCE(d.rule_reference,''),COALESCE(to_char(e.starts_at AT TIME ZONE 'Europe/London','DD Mon YYYY'),'')
		FROM sanction_cases c
		JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='approved'
		JOIN sanction_effect_revisions e ON e.decision_revision_id=d.id
		WHERE c.team_id=$1 AND c.status IN ('approved','published','appealed','closed')
		  AND NOT EXISTS(SELECT 1 FROM sanction_effect_revisions n WHERE n.supersedes_id=e.id)
		ORDER BY COALESCE(e.starts_at,c.approved_at) DESC LIMIT 10`, sess.TeamID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	lines := []string{}
	citations := []map[string]any{}
	yellowBalance, redCount := 0, 0
	for rows.Next() {
		var ref, reason, status, effect, ruleRef, date string
		var points int
		if err = rows.Scan(&ref, &reason, &status, &effect, &points, &ruleRef, &date); err != nil {
			return "", nil, err
		}
		detail := fmt.Sprintf("%s: %s — %s (%s", ref, effectLabel(effect), reason, status)
		if date != "" {
			detail += ", effective " + date
		}
		if points != 0 {
			detail += fmt.Sprintf(", %d-point deduction", points)
		}
		if ruleRef != "" {
			detail += ", rule " + ruleRef
		}
		detail += ")"
		lines = append(lines, detail)
		citations = append(citations, map[string]any{"title": "Case " + ref, "url": "/captain/discipline", "rule_reference": ruleRef})
		if effect == "yellow_card" && (status == "active" || status == "suspended") {
			yellowBalance++
		}
		if effect == "red_card" {
			redCount++
		}
	}
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	if len(lines) == 0 {
		return "There are no approved sanctions recorded for your team. I can still answer a general question about the published rules.", citations, nil
	}
	remaining := 3 - (yellowBalance % 3)
	if remaining == 0 {
		remaining = 3
	}
	answer := fmt.Sprintf("Your team has %d approved sanction record(s). The current recorded balance is %d effective yellow card(s) and %d red card(s); on that balance, %d further yellow card(s) would reach the next three-yellow threshold.\n\n%s\n\nThis lookup excludes evidence, correspondence, reporter details, and internal notes. Quote the case reference if you need to challenge or appeal a record.", len(lines), yellowBalance, redCount, remaining, strings.Join(lines, "\n"))
	return answer, citations, nil
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
