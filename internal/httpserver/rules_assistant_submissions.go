package httpserver

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// The captain submission skill answers "has my report gone in?" and "why did
// my sign-in link not work?" with deterministic, session-scoped lookups over
// the same tables the admin Link Diagnostics page uses. Like the sanctions
// lookup, no model is involved: the captain's identity comes from the signed
// session cookie, every query is scoped to that captain and team, and email
// addresses are partially masked in the reply.

// hasSubmissionLookupTerms reports whether a question is about match reports,
// submissions, or sign-in link/email delivery, and is not rulebook phrasing.
// Rulebook questions about submissions ("When must match details be entered
// on Play-Cricket?") must stay with the cited rules pipeline.
func hasSubmissionLookupTerms(question string) bool {
	words := map[string]bool{}
	for _, word := range sanctionQuestionWordRE.FindAllString(strings.ToLower(question), -1) {
		words[word] = true
	}
	hasTerm := false
	for _, term := range []string{"report", "reports", "submission", "submissions", "submit", "submitted", "resubmit", "link", "links", "email", "emails", "emailed", "form"} {
		if words[term] {
			hasTerm = true
			break
		}
	}
	if !hasTerm {
		return false
	}
	return !questionContainsAny(normalisedQuestionText(question), "what happens", " if we ", " if i ", " if a ", " can a ", " would ", " should ", " allowed ", " rule ", " rules ", " deadline", " penalty", " fine ", " sanction", " tell me ", " tell us ", " explain ", " what does ", " mean ")
}

// isSubmissionLookupQuestion decides whether an authenticated captain is
// asking about their own report or sign-in link: submission terms plus an
// ownership marker. Admins instead name a club, checked in the chat handler.
func isSubmissionLookupQuestion(question string) bool {
	return hasSubmissionLookupTerms(question) && questionContainsAny(normalisedQuestionText(question), " my ", " our ", " we ", " i ", " me ", " us ")
}

// maskEmail keeps the first character and the domain so a captain can confirm
// which address the league holds without the full address appearing in chat
// history: "warren@example.com" becomes "w…@example.com".
func maskEmail(address string) string {
	address = strings.TrimSpace(address)
	at := strings.Index(address, "@")
	if at < 1 {
		return address
	}
	return address[:1] + "…" + address[at:]
}

type captainSubmissionRecord struct {
	MatchDate   time.Time
	SubmittedAt time.Time
}

type captainLinkToken struct {
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

type captainLinkSend struct {
	CreatedAt  time.Time
	SeasonName string
	WeekNumber int32
}

type captainReminderSend struct {
	SentAt    time.Time
	MatchDate time.Time
	Type      string
	Recipient string
}

type captainEmailEvent struct {
	At        time.Time
	Type      string
	Recipient string
}

type captainSubmissionStatus struct {
	CaptainName    string
	TeamName       string
	ClubName       string
	PermanentEmail string
	EffectiveEmail string
	OverrideActive bool
	OverrideUntil  string
	Submissions    []captainSubmissionRecord
	Tokens         []captainLinkToken
	LastLinkSend   *captainLinkSend
	LastReminder   *captainReminderSend
	EmailEvents    []captainEmailEvent
	Now            time.Time
}

func (s *Server) loadCaptainSubmissionStatus(ctx context.Context, sess *captainSession) (captainSubmissionStatus, error) {
	return s.loadSubmissionStatusByIDs(ctx, sess.CaptainID, sess.TeamID)
}

// loadSubmissionStatusByIDs gathers the diagnosis for one team and captain.
// captainID may be zero (a team with no active captain): report status still
// loads, while the captain-scoped link and email lookups are skipped.
func (s *Server) loadSubmissionStatusByIDs(ctx context.Context, captainID, teamID int32) (captainSubmissionStatus, error) {
	status := captainSubmissionStatus{Now: time.Now().In(s.LondonLoc)}
	if captainID > 0 {
		err := s.DB.QueryRow(ctx, `
			SELECT c.full_name, TRIM(c.email),
			       COALESCE(CASE WHEN c.email_override IS NOT NULL AND c.email_override_until >= CURRENT_DATE THEN TRIM(c.email_override) END, TRIM(c.email)),
			       (c.email_override IS NOT NULL AND c.email_override_until >= CURRENT_DATE),
			       COALESCE(TO_CHAR(c.email_override_until, 'DD Mon YYYY'), ''),
			       t.name, cl.name
			FROM captains c
			JOIN teams t ON t.id = c.team_id
			JOIN clubs cl ON cl.id = t.club_id
			WHERE c.id = $1`, captainID).Scan(&status.CaptainName, &status.PermanentEmail, &status.EffectiveEmail, &status.OverrideActive, &status.OverrideUntil, &status.TeamName, &status.ClubName)
		if err != nil {
			return status, err
		}
	}
	subRows, err := s.DB.Query(ctx, `SELECT match_date, submitted_at FROM submissions WHERE team_id=$1 ORDER BY submitted_at DESC LIMIT 5`, teamID)
	if err != nil {
		return status, err
	}
	for subRows.Next() {
		var record captainSubmissionRecord
		if err := subRows.Scan(&record.MatchDate, &record.SubmittedAt); err != nil {
			subRows.Close()
			return status, err
		}
		record.SubmittedAt = record.SubmittedAt.In(s.LondonLoc)
		status.Submissions = append(status.Submissions, record)
	}
	subRows.Close()
	if err := subRows.Err(); err != nil {
		return status, err
	}
	var reminder captainReminderSend
	if err := s.DB.QueryRow(ctx, `
		SELECT sent_at, match_date, reminder_type, captain_email
		FROM captain_reminder_log
		WHERE team_id=$1
		ORDER BY sent_at DESC LIMIT 1`, teamID).Scan(&reminder.SentAt, &reminder.MatchDate, &reminder.Type, &reminder.Recipient); err == nil {
		reminder.SentAt = reminder.SentAt.In(s.LondonLoc)
		status.LastReminder = &reminder
	}
	if captainID == 0 {
		return status, nil
	}
	tokenRows, err := s.DB.Query(ctx, `SELECT created_at, expires_at, used_at FROM magic_link_tokens WHERE captain_id=$1 ORDER BY created_at DESC LIMIT 5`, captainID)
	if err != nil {
		return status, err
	}
	for tokenRows.Next() {
		var token captainLinkToken
		if err := tokenRows.Scan(&token.CreatedAt, &token.ExpiresAt, &token.UsedAt); err != nil {
			tokenRows.Close()
			return status, err
		}
		token.CreatedAt = token.CreatedAt.In(s.LondonLoc)
		token.ExpiresAt = token.ExpiresAt.In(s.LondonLoc)
		status.Tokens = append(status.Tokens, token)
	}
	tokenRows.Close()
	if err := tokenRows.Err(); err != nil {
		return status, err
	}
	var send captainLinkSend
	if err := s.DB.QueryRow(ctx, `
		SELECT m.created_at, se.name, w.week_number
		FROM magic_link_send_log m
		JOIN seasons se ON se.id = m.season_id
		JOIN weeks w ON w.id = m.week_id
		WHERE m.captain_id=$1
		ORDER BY m.created_at DESC LIMIT 1`, captainID).Scan(&send.CreatedAt, &send.SeasonName, &send.WeekNumber); err == nil {
		send.CreatedAt = send.CreatedAt.In(s.LondonLoc)
		status.LastLinkSend = &send
	}
	eventRows, err := s.DB.Query(ctx, `
		SELECT COALESCE(occurred_at, created_at), event_type, COALESCE(recipient, '')
		FROM email_events
		WHERE captain_id=$1 OR LOWER(recipient)=LOWER($2)
		ORDER BY COALESCE(occurred_at, created_at) DESC LIMIT 5`, captainID, status.EffectiveEmail)
	if err != nil {
		return status, err
	}
	for eventRows.Next() {
		var event captainEmailEvent
		if err := eventRows.Scan(&event.At, &event.Type, &event.Recipient); err != nil {
			eventRows.Close()
			return status, err
		}
		event.At = event.At.In(s.LondonLoc)
		status.EmailEvents = append(status.EmailEvents, event)
	}
	eventRows.Close()
	return status, eventRows.Err()
}

const captainSubmissionTimeFormat = "Mon 2 Jan 15:04"

// formatCaptainSubmissionAnswer turns the loaded status into a readable,
// deterministic reply. The section the question asked about comes first.
func formatCaptainSubmissionAnswer(status captainSubmissionStatus, question string) (string, []map[string]any) {
	var submissionSection, linkSection strings.Builder

	if len(status.Submissions) == 0 {
		submissionSection.WriteString("No submitted match reports are recorded for " + status.TeamName + " yet.")
	} else {
		latest := status.Submissions[0]
		fmt.Fprintf(&submissionSection, "The most recent match report for %s was submitted on %s, covering the %s fixture.", status.TeamName, latest.SubmittedAt.Format(captainSubmissionTimeFormat), latest.MatchDate.Format("2 January"))
		if len(status.Submissions) > 1 {
			submissionSection.WriteString(" Recent reports:")
			for _, record := range status.Submissions {
				fmt.Fprintf(&submissionSection, "\n- %s fixture — submitted %s", record.MatchDate.Format("2 January"), record.SubmittedAt.Format(captainSubmissionTimeFormat))
			}
		}
	}

	if status.LastLinkSend != nil {
		fmt.Fprintf(&linkSection, "The most recent sign-in link was emailed on %s (%s, week %d) to %s.", status.LastLinkSend.CreatedAt.Format(captainSubmissionTimeFormat), status.LastLinkSend.SeasonName, status.LastLinkSend.WeekNumber, maskEmail(status.EffectiveEmail))
	} else if status.LastReminder != nil {
		fmt.Fprintf(&linkSection, "No requested sign-in link is on record; the most recent automatic reminder went to %s on %s.", maskEmail(status.LastReminder.Recipient), status.LastReminder.SentAt.Format(captainSubmissionTimeFormat))
	} else {
		fmt.Fprintf(&linkSection, "No sign-in link emails are on record for you yet; links are sent to %s when requested from the home page.", maskEmail(status.EffectiveEmail))
	}
	if len(status.Tokens) > 0 {
		token := status.Tokens[0]
		switch {
		case token.UsedAt != nil:
			fmt.Fprintf(&linkSection, " The latest link was used on %s, which signs you in and then retires that link.", token.UsedAt.In(token.CreatedAt.Location()).Format(captainSubmissionTimeFormat))
		case status.Now.After(token.ExpiresAt):
			fmt.Fprintf(&linkSection, " The latest link expired on %s without being used, so it will no longer work.", token.ExpiresAt.Format(captainSubmissionTimeFormat))
		default:
			fmt.Fprintf(&linkSection, " The latest link is still valid until %s.", token.ExpiresAt.Format(captainSubmissionTimeFormat))
		}
	}
	if status.OverrideActive {
		fmt.Fprintf(&linkSection, " An email override is active until %s: link emails go to %s instead of %s.", status.OverrideUntil, maskEmail(status.EffectiveEmail), maskEmail(status.PermanentEmail))
	}
	for _, event := range status.EmailEvents {
		eventType := strings.ToLower(event.Type)
		if strings.Contains(eventType, "bounce") || strings.Contains(eventType, "complaint") {
			fmt.Fprintf(&linkSection, " Warning: the most recent delivery problem for %s was a %s on %s — if that address is wrong, ask the league to correct it.", maskEmail(event.Recipient), eventType, event.At.Format(captainSubmissionTimeFormat))
			break
		}
	}

	q := strings.ToLower(question)
	linkFirst := strings.Contains(q, "link") || strings.Contains(q, "email") || strings.Contains(q, "magic") || strings.Contains(q, "sign")
	sections := []string{submissionSection.String(), linkSection.String()}
	if linkFirst {
		sections[0], sections[1] = sections[1], sections[0]
	}
	answer := strings.Join(sections, "\n\n") + "\n\nIf you need to submit now, request a fresh sign-in link from the home page; if nothing arrives within a few minutes, check your spam folder and confirm the address above is right. This lookup covers only your own team and shows masked addresses."
	citations := []map[string]any{
		{"title": "Request a new sign-in link", "url": "/", "rule_reference": ""},
		{"title": "Submission status", "url": "/submissions", "rule_reference": ""},
	}
	return answer, citations
}

func (s *Server) captainSubmissionAnswer(ctx context.Context, sess *captainSession, question string) (string, []map[string]any, error) {
	status, err := s.loadCaptainSubmissionStatus(ctx, sess)
	if err != nil {
		return "", nil, err
	}
	answer, citations := formatCaptainSubmissionAnswer(status, question)
	return answer, citations, nil
}

// adminSubmissionTeam is one team's diagnosis in an admin club lookup.
type adminSubmissionTeam struct {
	TeamName   string
	HasCaptain bool
	Status     captainSubmissionStatus
}

// adminSubmissionAnswer lets an admin ask about any club's reports and
// sign-in links from the protected admin chat — the staging test path when no
// captain is logged in, and a support tool in production. The club must be
// named in the question; a team mention ("2nd XI") narrows the answer.
func (s *Server) adminSubmissionAnswer(ctx context.Context, question string, club importClub, matched bool) (string, []map[string]any, error) {
	if !matched {
		return "Please name the club in your submission question, for example: “Has Woodley submitted their report?” I will show each team's report and sign-in-link status with masked email addresses.", nil, nil
	}
	rows, err := s.DB.Query(ctx, `
		SELECT DISTINCT ON (t.id) t.id, t.name, COALESCE(c.id, 0)
		FROM teams t
		LEFT JOIN captains c ON c.team_id = t.id
		  AND c.active_from <= CURRENT_DATE AND (c.active_to IS NULL OR c.active_to >= CURRENT_DATE)
		WHERE t.club_id = $1
		ORDER BY t.id, c.active_from DESC, c.id DESC`, club.ID)
	if err != nil {
		return "", nil, err
	}
	type teamRow struct {
		TeamID, CaptainID int32
		Name              string
	}
	var teamRows []teamRow
	for rows.Next() {
		var row teamRow
		if err = rows.Scan(&row.TeamID, &row.Name, &row.CaptainID); err != nil {
			rows.Close()
			return "", nil, err
		}
		teamRows = append(teamRows, row)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	if len(teamRows) == 0 {
		return fmt.Sprintf("%s has no teams on record, so there is nothing to diagnose.", club.Name), []map[string]any{}, nil
	}
	// A named team ("2nd XI") narrows the lookup; otherwise every team is
	// shown, capped to keep the answer readable.
	normalisedQuestion := normaliseImportName(question)
	var focused []teamRow
	for _, row := range teamRows {
		name := normaliseImportName(row.Name)
		if len(name) >= 3 && strings.Contains(" "+normalisedQuestion+" ", " "+name+" ") {
			focused = append(focused, row)
		}
	}
	if len(focused) == 0 {
		focused = teamRows
	}
	const maxTeams = 6
	truncated := 0
	if len(focused) > maxTeams {
		truncated = len(focused) - maxTeams
		focused = focused[:maxTeams]
	}
	teams := make([]adminSubmissionTeam, 0, len(focused))
	for _, row := range focused {
		status, loadErr := s.loadSubmissionStatusByIDs(ctx, row.CaptainID, row.TeamID)
		if loadErr != nil {
			return "", nil, loadErr
		}
		teams = append(teams, adminSubmissionTeam{TeamName: row.Name, HasCaptain: row.CaptainID > 0, Status: status})
	}
	answer, citations := formatAdminSubmissionAnswer(club.Name, teams, truncated)
	return answer, citations, nil
}

// formatAdminSubmissionAnswer renders one compact diagnosis block per team.
func formatAdminSubmissionAnswer(clubName string, teams []adminSubmissionTeam, truncated int) (string, []map[string]any) {
	blocks := make([]string, 0, len(teams))
	for _, team := range teams {
		var b strings.Builder
		if team.HasCaptain {
			fmt.Fprintf(&b, "%s — captain %s (%s):", team.TeamName, team.Status.CaptainName, maskEmail(team.Status.EffectiveEmail))
		} else {
			fmt.Fprintf(&b, "%s — no active captain on record, so sign-in links cannot be diagnosed:", team.TeamName)
		}
		if len(team.Status.Submissions) == 0 {
			b.WriteString("\n- Reports: none recorded.")
		} else {
			latest := team.Status.Submissions[0]
			fmt.Fprintf(&b, "\n- Reports: latest submitted %s for the %s fixture.", latest.SubmittedAt.Format(captainSubmissionTimeFormat), latest.MatchDate.Format("2 January"))
		}
		if team.HasCaptain {
			if team.Status.LastLinkSend != nil {
				fmt.Fprintf(&b, "\n- Sign-in link: last emailed %s (%s, week %d)", team.Status.LastLinkSend.CreatedAt.Format(captainSubmissionTimeFormat), team.Status.LastLinkSend.SeasonName, team.Status.LastLinkSend.WeekNumber)
			} else {
				b.WriteString("\n- Sign-in link: none requested yet")
			}
			if len(team.Status.Tokens) > 0 {
				token := team.Status.Tokens[0]
				switch {
				case token.UsedAt != nil:
					fmt.Fprintf(&b, "; latest link used %s.", token.UsedAt.In(token.CreatedAt.Location()).Format(captainSubmissionTimeFormat))
				case team.Status.Now.After(token.ExpiresAt):
					fmt.Fprintf(&b, "; latest link expired %s unused.", token.ExpiresAt.Format(captainSubmissionTimeFormat))
				default:
					fmt.Fprintf(&b, "; latest link still valid until %s.", token.ExpiresAt.Format(captainSubmissionTimeFormat))
				}
			} else {
				b.WriteString(".")
			}
			if team.Status.OverrideActive {
				fmt.Fprintf(&b, "\n- Email override active until %s: links go to %s instead of %s.", team.Status.OverrideUntil, maskEmail(team.Status.EffectiveEmail), maskEmail(team.Status.PermanentEmail))
			}
			for _, event := range team.Status.EmailEvents {
				eventType := strings.ToLower(event.Type)
				if strings.Contains(eventType, "bounce") || strings.Contains(eventType, "complaint") {
					fmt.Fprintf(&b, "\n- Delivery warning: %s for %s on %s.", eventType, maskEmail(event.Recipient), event.At.Format(captainSubmissionTimeFormat))
					break
				}
			}
		}
		if team.Status.LastReminder != nil {
			fmt.Fprintf(&b, "\n- Last automatic reminder: %s to %s on %s.", team.Status.LastReminder.Type, maskEmail(team.Status.LastReminder.Recipient), team.Status.LastReminder.SentAt.Format(captainSubmissionTimeFormat))
		}
		blocks = append(blocks, b.String())
	}
	answer := fmt.Sprintf("Report and sign-in-link status for %s:\n\n%s", clubName, strings.Join(blocks, "\n\n"))
	if truncated > 0 {
		answer += fmt.Sprintf("\n\n%d further team(s) not shown — name the team to see it.", truncated)
	}
	answer += "\n\nEmail addresses are masked in chat; open the link diagnostics page for full detail."
	citations := []map[string]any{
		{"title": "Link diagnostics for " + clubName, "url": "/admin/link-diagnostics?q=" + url.QueryEscape(clubName), "rule_reference": ""},
		{"title": "Search submissions", "url": "/admin/submissions", "rule_reference": ""},
	}
	return answer, citations
}
