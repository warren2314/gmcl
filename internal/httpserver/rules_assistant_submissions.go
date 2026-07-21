package httpserver

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The captain submission skill answers "has my report gone in?" and "why did
// my sign-in link not work?" with deterministic, session-scoped lookups over
// the same tables the admin Link Diagnostics page uses. Like the sanctions
// lookup, no model is involved: the captain's identity comes from the signed
// session cookie, every query is scoped to that captain and team, and email
// addresses are partially masked in the reply.

// isSubmissionLookupQuestion decides whether an authenticated captain is
// asking about their own match report, submission, or sign-in link/email.
// Rulebook questions about submissions ("When must match details be entered
// on Play-Cricket?") must stay with the cited rules pipeline, so record intent
// needs an ownership marker and no rulebook phrasing.
func isSubmissionLookupQuestion(question string) bool {
	q := normalisedQuestionText(question)
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
	if questionContainsAny(q, "what happens", " if we ", " if i ", " if a ", " can a ", " would ", " should ", " allowed ", " rule ", " rules ", " deadline", " penalty", " fine ", " sanction", " tell me ", " tell us ", " explain ", " what does ", " mean ") {
		return false
	}
	return questionContainsAny(q, " my ", " our ", " we ", " i ", " me ", " us ")
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
	status := captainSubmissionStatus{Now: time.Now().In(s.LondonLoc)}
	err := s.DB.QueryRow(ctx, `
		SELECT c.full_name, TRIM(c.email),
		       COALESCE(CASE WHEN c.email_override IS NOT NULL AND c.email_override_until >= CURRENT_DATE THEN TRIM(c.email_override) END, TRIM(c.email)),
		       (c.email_override IS NOT NULL AND c.email_override_until >= CURRENT_DATE),
		       COALESCE(TO_CHAR(c.email_override_until, 'DD Mon YYYY'), ''),
		       t.name, cl.name
		FROM captains c
		JOIN teams t ON t.id = c.team_id
		JOIN clubs cl ON cl.id = t.club_id
		WHERE c.id = $1`, sess.CaptainID).Scan(&status.CaptainName, &status.PermanentEmail, &status.EffectiveEmail, &status.OverrideActive, &status.OverrideUntil, &status.TeamName, &status.ClubName)
	if err != nil {
		return status, err
	}
	subRows, err := s.DB.Query(ctx, `SELECT match_date, submitted_at FROM submissions WHERE team_id=$1 ORDER BY submitted_at DESC LIMIT 5`, sess.TeamID)
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
	tokenRows, err := s.DB.Query(ctx, `SELECT created_at, expires_at, used_at FROM magic_link_tokens WHERE captain_id=$1 ORDER BY created_at DESC LIMIT 5`, sess.CaptainID)
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
		ORDER BY m.created_at DESC LIMIT 1`, sess.CaptainID).Scan(&send.CreatedAt, &send.SeasonName, &send.WeekNumber); err == nil {
		send.CreatedAt = send.CreatedAt.In(s.LondonLoc)
		status.LastLinkSend = &send
	}
	var reminder captainReminderSend
	if err := s.DB.QueryRow(ctx, `
		SELECT sent_at, match_date, reminder_type, captain_email
		FROM captain_reminder_log
		WHERE team_id=$1
		ORDER BY sent_at DESC LIMIT 1`, sess.TeamID).Scan(&reminder.SentAt, &reminder.MatchDate, &reminder.Type, &reminder.Recipient); err == nil {
		reminder.SentAt = reminder.SentAt.In(s.LondonLoc)
		status.LastReminder = &reminder
	}
	eventRows, err := s.DB.Query(ctx, `
		SELECT COALESCE(occurred_at, created_at), event_type, COALESCE(recipient, '')
		FROM email_events
		WHERE captain_id=$1 OR LOWER(recipient)=LOWER($2)
		ORDER BY COALESCE(occurred_at, created_at) DESC LIMIT 5`, sess.CaptainID, status.EffectiveEmail)
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
