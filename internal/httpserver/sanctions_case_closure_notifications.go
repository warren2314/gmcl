package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Fixture sides are only inferred from mapped Play-Cricket team IDs. Legacy
// cases can instead select their named case clubs without guessing a side.
const closureClubsSQL = `WITH current_resolution AS (
 SELECT DISTINCT ON (resolution.intake_id) resolution.reporting_club_id,resolution.league_origin
 FROM sanction_case_intake_merge_resolutions resolution
 JOIN sanction_intake_case_links link ON link.case_id=resolution.case_id
 AND link.intake_id=resolution.intake_id AND link.relationship=resolution.relationship
 WHERE resolution.case_id=$1 ORDER BY resolution.intake_id,resolution.id DESC
), fixture_clubs AS (
 SELECT home.club_id,'Home' AS side FROM sanction_cases c
 JOIN league_fixtures f ON f.play_cricket_match_id=c.play_cricket_match_id
 JOIN teams home ON home.play_cricket_team_id=f.home_team_pc_id WHERE c.id=$1
 UNION
 SELECT away.club_id,'Away' FROM sanction_cases c
 JOIN league_fixtures f ON f.play_cricket_match_id=c.play_cricket_match_id
 JOIN teams away ON away.play_cricket_team_id=f.away_team_pc_id WHERE c.id=$1
), allowed AS (
 SELECT club_id FROM sanction_cases WHERE id=$1
 UNION SELECT reporting_club_id FROM sanction_cases WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM current_resolution)
 UNION SELECT reporting_club_id FROM current_resolution WHERE NOT league_origin
 UNION SELECT club_id FROM fixture_clubs
)
SELECT club.id,club.name,COALESCE((SELECT string_agg(side,' / ' ORDER BY side) FROM fixture_clubs WHERE club_id=club.id),'Case club'),
 COALESCE((SELECT email FROM sanction_club_contacts WHERE club_id=club.id AND contact_type='official_mailbox'
 AND active AND verified_at IS NOT NULL ORDER BY verified_at DESC,id DESC LIMIT 1),'')
FROM clubs club WHERE club.id IN (SELECT club_id FROM allowed) ORDER BY club.name,club.id`

type closureClub struct {
	id                int32
	name, side, email string
}
type closureClubQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadClosureClubs(ctx context.Context, q closureClubQueryer, caseID int64) ([]closureClub, error) {
	rows, err := q.Query(ctx, closureClubsSQL, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var clubs []closureClub
	for rows.Next() {
		var c closureClub
		if err = rows.Scan(&c.id, &c.name, &c.side, &c.email); err != nil {
			return nil, err
		}
		clubs = append(clubs, c)
	}
	return clubs, rows.Err()
}

func (s *Server) caseClosureNotificationControls(ctx context.Context, caseID int64) string {
	clubs, err := loadClosureClubs(ctx, s.DB, caseID)
	if err != nil {
		return `<p class="text-danger">Closure recipients could not be loaded. Refresh before closing if a notification is needed.</p>`
	}
	var out strings.Builder
	out.WriteString(`<fieldset class="mb-3"><legend class="h6">Notify clubs that the case has been dropped</legend><p class="small">Select the home club, away club, or both by name. Leave unticked to close without email. Each selected club receives a separate email at its verified official mailbox.</p>`)
	for _, c := range clubs {
		disabled, address := "", c.email
		if _, err := closureMailbox(c.email); err != nil {
			disabled = " disabled"
			address = "Verified official mailbox required"
		}
		fmt.Fprintf(&out, `<div class="form-check"><input class="form-check-input" type="checkbox" name="notify_club" value="%d" id="closure-club-%d"%s><label class="form-check-label" for="closure-club-%d">%s — %s (%s)</label></div>`, c.id, c.id, disabled, c.id, escapeHTML(c.side), escapeHTML(c.name), escapeHTML(address))
	}
	if len(clubs) == 0 {
		out.WriteString(`<p class="text-warning">Map the case clubs or linked fixture before sending closure notices.</p>`)
	}
	out.WriteString(`<p class="small mt-2">Email preview: The case referenced in the subject has been dropped and closed with no further action. No response is required. Private reasons and evidence are not included.</p></fieldset>`)
	return out.String()
}

func parseClosureClubIDs(values []string) ([]int32, error) {
	if len(values) > 20 {
		return nil, errors.New("too many closure recipients")
	}
	var ids []int32
	seen := map[int32]bool{}
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 32)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid closure club")
		}
		if !seen[int32(id)] {
			ids = append(ids, int32(id))
			seen[int32(id)] = true
		}
	}
	return ids, nil
}

func closureMailbox(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("verified official mailbox is invalid or missing")
	}
	return strings.ToLower(parsed.Address), nil
}

func queueCaseClosureNotices(ctx context.Context, tx pgx.Tx, caseID int64, reference string, ids []int32, actorID int32, actorLabel, requestID string) error {
	if len(ids) == 0 {
		return nil
	}
	var source string
	if err := tx.QueryRow(ctx, `SELECT source_type FROM sanction_cases WHERE id=$1`, caseID).Scan(&source); err != nil {
		return err
	}
	if sanctionsEmailDisabled() || (source == "ineligible_player" && !ineligibleOutboundEmailEnabled()) {
		return errors.New("outbound email is disabled")
	}
	clubs, err := loadClosureClubs(ctx, tx, caseID)
	if err != nil {
		return err
	}
	addresses, err := closureRecipients(clubs, ids)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		var outboxID int64
		err = tx.QueryRow(ctx, `INSERT INTO sanction_notification_outbox(case_id,message_kind,idempotency_key,recipient,subject,body)
		 VALUES($1,'case_dropped',$2,$3,$4,$5) RETURNING id`, caseID, fmt.Sprintf("case-dropped:%d:%s", caseID, address), address,
			"GMCL case dropped — "+reference, "The case referenced in the subject has been dropped and closed with no further action. No response is required.\n\nGMCL").Scan(&outboxID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO sanction_case_events(case_id,event_type,actor_type,actor_id,actor_label,request_id,reason,metadata)
		 VALUES($1,'case_dropped_notice_queued','admin',$2,$3,$4,'Closure notice queued to verified official mailbox',
		 jsonb_build_object('club_ids',$5::integer[],'recipient',$6::text,'outbox_id',$7::bigint))`, caseID, actorID, actorLabel, requestID, ids, address, outboxID)
		if err != nil {
			return err
		}
	}
	return nil
}

func closureRecipients(clubs []closureClub, ids []int32) ([]string, error) {
	var addresses []string
	allowed := map[int32]closureClub{}
	for _, c := range clubs {
		allowed[c.id] = c
	}
	seen := map[string]bool{}
	for _, id := range ids {
		club, ok := allowed[id]
		if !ok {
			return nil, errors.New("selected club is not linked to this case")
		}
		address, err := closureMailbox(club.email)
		if err != nil {
			return nil, err
		}
		if seen[address] {
			continue
		}
		seen[address] = true
		addresses = append(addresses, address)
	}
	return addresses, nil
}
