package sanctions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// OutcomeDraft is the exact audience-specific wording reviewed by an
// administrator before independent approval. Exists is false when Subject and
// Body are merely deterministic defaults and still need to be explicitly
// saved.
type OutcomeDraft struct {
	ID          int64
	DecisionID  int64
	Revision    int
	Audience    string
	MessageKind string
	Subject     string
	Body        string
	Exists      bool
}

func (s *Service) OutcomeDraft(ctx context.Context, caseID int64, audience string) (OutcomeDraft, error) {
	if !validOutcomeAudience(audience) {
		return OutcomeDraft{}, errors.New("invalid outcome audience")
	}
	var draft OutcomeDraft
	var reference, sourceType, offendingClub, offendingTeam, subject, findings, appeal string
	var matchDate *time.Time
	var offendingClubID *int32
	var rule *string
	var status string
	err := s.DB.QueryRow(ctx, `SELECT d.id,c.reference,c.source_type,c.status,c.club_id,COALESCE(off.name,''),COALESCE(team.name,''),
		COALESCE(d.outcome_subject,'GMCL case outcome '||c.reference),c.match_date,COALESCE(d.outcome_findings,d.public_reason),COALESCE(d.appeal_instructions,''),d.rule_reference
		FROM sanction_cases c JOIN sanction_decision_revisions d ON d.case_id=c.id
		LEFT JOIN clubs off ON off.id=c.club_id LEFT JOIN teams team ON team.id=c.team_id
		WHERE c.id=$1 AND d.status='proposed' ORDER BY d.revision DESC LIMIT 1`, caseID).
		Scan(&draft.DecisionID, &reference, &sourceType, &status, &offendingClubID, &offendingClub, &offendingTeam, &subject, &matchDate, &findings, &appeal, &rule)
	if err != nil {
		return OutcomeDraft{}, err
	}
	if status != "decision_proposed" && status != "triage" {
		return OutcomeDraft{}, ErrNotApprovable
	}

	effects, err := loadOutcomeEffects(ctx, s.DB, draft.DecisionID)
	if err != nil {
		return OutcomeDraft{}, err
	}
	reportingClubs, err := loadReportingOutcomeClubs(ctx, s.DB, caseID)
	if err != nil {
		return OutcomeDraft{}, err
	}
	combined := false
	reportingNames := make([]string, 0, len(reportingClubs))
	for _, club := range reportingClubs {
		reportingNames = append(reportingNames, club.name)
		if offendingClubID != nil && club.id == *offendingClubID {
			combined = true
		}
	}
	noAction := outcomeIsNoAction(effects)
	ruleText := "No specific rule reference recorded."
	if rule != nil && strings.TrimSpace(*rule) != "" {
		ruleText = strings.TrimSpace(*rule)
	}
	rendered := renderOutcomeCommunications(outcomeRenderData{
		reference: reference, sourceType: sourceType, offendingClub: offendingClub, offendingTeam: offendingTeam,
		reportingClub: strings.Join(reportingNames, ", "), subject: subject, offenceDate: formatOutcomeOffenceDate(matchDate),
		findings: findings, appeal: appeal, rule: ruleText,
		effectSummary: approvedEffectSummary(effects), signatoryName: outcomeLetterSignatoryName, combined: combined, noAction: noAction,
	})
	draft.Audience = audience
	draft.Subject = rendered.subject
	draft.MessageKind = outcomeMessageKind(audience, noAction)
	switch audience {
	case "offending_club":
		draft.Body = rendered.offending
	case "reporting_club":
		draft.Body = rendered.reporting
	case "official":
		draft.Body = rendered.official
	}
	generated := draft

	// Only a draft tied to this exact proposed decision is current. A reproposal
	// deliberately makes older wording stale.
	err = s.DB.QueryRow(ctx, `SELECT id,revision,message_kind,subject,body
		FROM sanction_correspondence_revisions
		WHERE case_id=$1 AND decision_revision_id=$2 AND audience=$3 AND status='draft'
		ORDER BY revision DESC,id DESC LIMIT 1`, caseID, draft.DecisionID, audience).
		Scan(&draft.ID, &draft.Revision, &draft.MessageKind, &draft.Subject, &draft.Body)
	if err == nil {
		// Audience correspondence is deliberately deterministic. If a legacy or
		// tampered draft differs from the current decision, present the generated
		// wording as an unsaved replacement instead of allowing contradictory or
		// private free text to be approved.
		if !outcomeDraftMatchesGenerated(draft.Subject, draft.Body, generated.Subject, generated.Body) {
			return generated, nil
		}
		draft.Exists = true
		return draft, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return OutcomeDraft{}, err
	}
	return draft, nil
}

func (s *Service) SaveOutcomeDraft(ctx context.Context, caseID int64, audience, subject, body string, actor Actor) (OutcomeDraft, error) {
	if actor.ID == nil {
		return OutcomeDraft{}, errors.New("an authenticated administrator is required")
	}
	subject = strings.TrimSpace(subject)
	body = strings.TrimSpace(body)
	if subject == "" || len(subject) > 300 || body == "" || len(body) > 30000 {
		return OutcomeDraft{}, errors.New("subject and body are required and must fit the correspondence limits")
	}
	current, err := s.OutcomeDraft(ctx, caseID, audience)
	if err != nil {
		return OutcomeDraft{}, err
	}
	if !outcomeDraftMatchesGenerated(subject, body, current.Subject, current.Body) {
		return OutcomeDraft{}, errors.New("outcome wording is generated from the proposed decision and cannot be edited directly")
	}
	// Store the canonical generated bytes, rather than browser-specific line
	// endings, so approval can lock one deterministic audience-safe snapshot.
	subject = current.Subject
	body = current.Body
	if err = validateOutcomeDraftCompleteness(audience, body); err != nil {
		return OutcomeDraft{}, err
	}
	if err = s.validateOutcomeDraftPrivacy(ctx, caseID, audience, subject+"\n"+body); err != nil {
		return OutcomeDraft{}, err
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return OutcomeDraft{}, err
	}
	defer tx.Rollback(ctx)
	var decisionID int64
	var status string
	if err = tx.QueryRow(ctx, `SELECT d.id,c.status FROM sanction_cases c JOIN sanction_decision_revisions d ON d.case_id=c.id AND d.status='proposed' WHERE c.id=$1 ORDER BY d.revision DESC LIMIT 1 FOR UPDATE OF c`, caseID).Scan(&decisionID, &status); err != nil {
		return OutcomeDraft{}, err
	}
	if decisionID != current.DecisionID || (status != "decision_proposed" && status != "triage") {
		return OutcomeDraft{}, errors.New("the proposed decision changed; reload the draft before saving")
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO sanction_correspondence_revisions(case_id,decision_revision_id,supersedes_id,message_kind,audience,revision,status,subject,body,created_by_admin_id)
		VALUES($1,$2,(SELECT id FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind=$3 AND audience=$4 ORDER BY revision DESC,id DESC LIMIT 1),
		$3,$4,(SELECT COALESCE(MAX(revision),0)+1 FROM sanction_correspondence_revisions WHERE case_id=$1 AND message_kind=$3 AND audience=$4),'draft',$5,$6,$7)
		RETURNING id,revision`, caseID, decisionID, current.MessageKind, audience, subject, body, actorID(actor)).Scan(&id, &current.Revision)
	if err != nil {
		return OutcomeDraft{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,reason,request_id,after_data)
		VALUES($1,'correspondence_draft_saved',$2,$3,$4,$5,$6,jsonb_build_object('correspondence_revision_id',$7::bigint,'decision_revision_id',$8::bigint,'audience',$9::text))`,
		caseID, actor.Type, actorID(actor), actor.Label, "Saved an append-only "+strings.ReplaceAll(audience, "_", " ")+" outcome draft", actor.RequestID, id, decisionID, audience); err != nil {
		return OutcomeDraft{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return OutcomeDraft{}, err
	}
	current.ID, current.DecisionID, current.Subject, current.Body, current.Exists = id, decisionID, subject, body, true
	return current, nil
}

func (s *Service) validateOutcomeDraftPrivacy(ctx context.Context, caseID int64, audience, text string) error {
	var offendingClubID *int32
	if err := s.DB.QueryRow(ctx, `SELECT club_id FROM sanction_cases WHERE id=$1`, caseID).
		Scan(&offendingClubID); err != nil {
		return err
	}
	values, err := CaseReporterIdentityValues(ctx, s.DB, caseID)
	if err != nil {
		return err
	}
	if audience == "offending_club" || audience == "reporting_club" {
		var excluded *int32
		if audience == "offending_club" {
			excluded = offendingClubID
		}
		aliases, aliasErr := CaseReportingClubIdentityValues(ctx, s.DB, caseID, excluded)
		if aliasErr != nil {
			return aliasErr
		}
		values = append(values, aliases...)
	}
	if ContainsPrivateIdentity(text, values...) {
		return errors.New("draft contains reporter or audience-private identity information")
	}
	if audience == "reporting_club" {
		restricted, restrictedErr := CaseReportingOutcomeRestrictedValues(ctx, s.DB, caseID)
		if restrictedErr != nil {
			return restrictedErr
		}
		if ContainsRestrictedContent(text, restricted...) {
			return errors.New("reporting-club draft contains an offending-club response, internal note, private rationale, or private evidence label")
		}
	}
	return nil
}

func validOutcomeAudience(audience string) bool {
	return audience == "offending_club" || audience == "reporting_club" || audience == "official"
}

type outcomeRequiredSection struct {
	label  string
	inline bool
}

func outcomeDraftMatchesGenerated(subject, body, generatedSubject, generatedBody string) bool {
	return strings.TrimSpace(subject) == strings.TrimSpace(generatedSubject) &&
		canonicalOutcomeBody(body) == canonicalOutcomeBody(generatedBody)
}

func canonicalOutcomeBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.TrimSpace(body)
}

// validateOutcomeDraftCompleteness prevents editable audience drafts from
// dropping the findings, rule, effect or appeal material that the approved
// decision requires. Section labels must occur once, in order, and block
// sections must contain text before the next required section (or sign-off).
func validateOutcomeDraftCompleteness(audience, body string) error {
	requirements := map[string][]outcomeRequiredSection{
		"offending_club": {
			{label: "Offence date:"},
			{label: "Findings:"},
			{label: "Rule determination:"},
			{label: "Decision and sanctions:"},
			{label: "Appeal instructions:"},
		},
		"reporting_club": {
			{label: "Offence date:"},
			{label: "Confirmed findings:"},
			{label: "Rule determination:"},
			{label: "Final outcome:"},
		},
		"official": {
			{label: "Case:", inline: true},
			{label: "Source:", inline: true},
			{label: "Offence date:", inline: true},
			{label: "Offending club:", inline: true},
			{label: "Reporting club:", inline: true},
			{label: "Findings:"},
			{label: "Rule determination:"},
			{label: "Decision and sanctions:"},
			{label: "Appeal instructions:"},
		},
	}
	sections, ok := requirements[audience]
	if !ok {
		return errors.New("invalid outcome audience")
	}
	lines := strings.Split(canonicalOutcomeBody(body), "\n")
	positions := make([]int, len(sections))
	previous := -1
	for index, section := range sections {
		matches := make([]int, 0, 1)
		for lineIndex, rawLine := range lines {
			line := strings.TrimSpace(rawLine)
			if section.inline {
				if strings.HasPrefix(line, section.label) {
					matches = append(matches, lineIndex)
				}
			} else if line == section.label {
				matches = append(matches, lineIndex)
			}
		}
		if len(matches) != 1 {
			return fmt.Errorf("%s outcome must contain exactly one %q section", strings.ReplaceAll(audience, "_", " "), strings.TrimSuffix(section.label, ":"))
		}
		positions[index] = matches[0]
		if positions[index] <= previous {
			return fmt.Errorf("%s outcome sections are out of order", strings.ReplaceAll(audience, "_", " "))
		}
		previous = positions[index]
		if section.inline && strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[positions[index]]), section.label)) == "" {
			return fmt.Errorf("%s outcome %q section is empty", strings.ReplaceAll(audience, "_", " "), strings.TrimSuffix(section.label, ":"))
		}
	}
	for index, section := range sections {
		if section.inline {
			continue
		}
		end := len(lines)
		if index+1 < len(sections) {
			end = positions[index+1]
		} else if audience == "offending_club" || audience == "reporting_club" {
			for lineIndex := positions[index] + 1; lineIndex < len(lines); lineIndex++ {
				if strings.EqualFold(strings.TrimSpace(lines[lineIndex]), "Regards,") {
					end = lineIndex
					break
				}
			}
		}
		if strings.TrimSpace(strings.Join(lines[positions[index]+1:end], "\n")) == "" {
			return fmt.Errorf("%s outcome %q section is empty", strings.ReplaceAll(audience, "_", " "), strings.TrimSuffix(section.label, ":"))
		}
	}
	return nil
}

func outcomeMessageKind(audience string, noAction bool) string {
	if noAction {
		return "no_action_outcome"
	}
	return map[string]string{"offending_club": "outcome_offending_club", "reporting_club": "outcome_reporting_club", "official": "outcome_official"}[audience]
}

func outcomeIsNoAction(effects []approvedOutcomeEffect) bool {
	if len(effects) == 0 {
		return false
	}
	for _, effect := range effects {
		if effect.typeName != "no_action" {
			return false
		}
	}
	return true
}

type outcomeEffectQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadOutcomeEffects(ctx context.Context, queryer outcomeEffectQueryer, decisionID int64) ([]approvedOutcomeEffect, error) {
	rows, err := queryer.Query(ctx, `SELECT e.effect_type,e.subject_type,COALESCE(e.player_name,cs.player_name,''),COALESCE(t.name,''),e.amount_pence,e.points,e.starts_at,e.ends_at
		FROM sanction_effect_revisions e LEFT JOIN sanction_case_subjects cs ON cs.id=e.case_subject_id
		LEFT JOIN teams t ON t.id=COALESCE(cs.team_id,CASE WHEN e.subject_type='team' THEN e.subject_id::integer END)
		WHERE e.decision_revision_id=$1 ORDER BY e.id`, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var effects []approvedOutcomeEffect
	for rows.Next() {
		var effect approvedOutcomeEffect
		if err = rows.Scan(&effect.typeName, &effect.subjectType, &effect.playerName, &effect.teamName, &effect.amount, &effect.points, &effect.startsAt, &effect.endsAt); err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(effects) == 0 {
		return nil, fmt.Errorf("proposed decision has no effects")
	}
	return effects, nil
}
