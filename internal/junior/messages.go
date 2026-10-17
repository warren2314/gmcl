package junior

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Enqueue participates in the caller's transaction. It stores immutable
// content/audience snapshots and never sends mail. Zero recipients is a visible
// needs_recipients gap; it does not block the official business action.
func Enqueue(ctx context.Context, q DBTX, p Principal, intent MessageIntent) (int64, error) {
	if err := Authorize(ctx, q, p, intent.ClubID, true); err != nil {
		return 0, err
	}
	if intent.SeasonID <= 0 || intent.Key == "" || len(intent.Key) > 250 || strings.TrimSpace(intent.Kind) == "" || len(intent.Kind) > 100 || intent.RecordID < 0 || len(intent.RecordType) > 100 || strings.ContainsRune(intent.Key+intent.Kind+intent.RecordType, 0) || strings.TrimSpace(intent.Subject) == "" || len(intent.Subject) > 250 || strings.ContainsAny(intent.Subject, "\x00\r\n") || strings.TrimSpace(intent.Body) == "" || len(intent.Body) > 50000 || strings.ContainsRune(intent.Body, 0) {
		return 0, ErrValidation
	}
	var id int64
	var subject, body, kind, recordType string
	var seasonID, recordID int64
	var clubID int32
	err := q.QueryRow(ctx, `SELECT id,season_id,COALESCE(club_id,0),kind,record_type,record_id,subject,body FROM junior_messages WHERE idempotency_key=$1`, intent.Key).Scan(&id, &seasonID, &clubID, &kind, &recordType, &recordID, &subject, &body)
	if err == nil {
		if seasonID != intent.SeasonID || clubID != intent.ClubID || kind != intent.Kind || recordType != intent.RecordType || recordID != intent.RecordID || subject != intent.Subject || body != intent.Body {
			return 0, ErrConflict
		}
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if err = q.QueryRow(ctx, `INSERT INTO junior_messages(season_id,club_id,kind,record_type,record_id,idempotency_key,subject,body,created_by_admin_id,created_by_account_id) VALUES($1,NULLIF($2,0),$3,$4,$5,$6,$7,$8,NULLIF($9,0),NULLIF($10,0)) RETURNING id`, intent.SeasonID, intent.ClubID, intent.Kind, intent.RecordType, intent.RecordID, intent.Key, intent.Subject, intent.Body, p.AdminID, p.AccountID).Scan(&id); err != nil {
		return 0, err
	}
	if err = appendMessageRecipients(ctx, q, id, intent.ClubID, intent.Recipients); err != nil {
		return 0, err
	}
	if err = Audit(ctx, q, p, intent.ClubID, "message", id, "queued", map[string]any{"kind": intent.Kind, "recipient_selections": len(intent.Recipients), "needs_recipients": len(intent.Recipients) == 0}); err != nil {
		return 0, err
	}
	return id, nil
}

func appendMessageRecipients(ctx context.Context, q DBTX, messageID int64, messageClubID int32, recipients []Recipient) error {
	for _, recipient := range recipients {
		email, err := CanonicalEmail(recipient.Email)
		if err != nil {
			return err
		}
		if recipient.ClubID <= 0 || (messageClubID > 0 && recipient.ClubID != messageClubID) || len(recipient.Name) > 250 || strings.ContainsRune(recipient.Name, 0) {
			return ErrForbidden
		}
		var valid bool
		if recipient.ContactID > 0 {
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_contacts WHERE id=$1 AND club_id=$2 AND NOT archived AND lower(btrim(email))=$3)`, recipient.ContactID, recipient.ClubID, email).Scan(&valid)
		} else {
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_memberships m JOIN junior_accounts a ON a.id=m.account_id WHERE m.club_id=$1 AND m.revoked_at IS NULL AND a.is_active AND a.email=$2) OR EXISTS(SELECT 1 FROM junior_contacts WHERE club_id=$1 AND NOT archived AND lower(btrim(email))=$2)`, recipient.ClubID, email).Scan(&valid)
		}
		if err != nil {
			return err
		}
		if !valid {
			return ErrForbidden
		}
		var recipientID int64
		if err = q.QueryRow(ctx, `INSERT INTO junior_message_recipients(message_id,email,name) VALUES($1,$2,$3) ON CONFLICT(message_id,email) DO NOTHING RETURNING id`, messageID, email, recipient.Name).Scan(&recipientID); errors.Is(err, pgx.ErrNoRows) {
			err = q.QueryRow(ctx, `SELECT id FROM junior_message_recipients WHERE message_id=$1 AND email=$2`, messageID, email).Scan(&recipientID)
		}
		if err != nil {
			return err
		}
		if _, err = q.Exec(ctx, `INSERT INTO junior_message_audiences(message_id,recipient_id,club_id,contact_id,name) VALUES($1,$2,$3,NULLIF($4,0),$5) ON CONFLICT DO NOTHING`, messageID, recipientID, recipient.ClubID, recipient.ContactID, recipient.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) PreviewAudience(ctx context.Context, p Principal, filter AudienceFilter) (AudiencePreview, error) {
	if p.AdminID <= 0 {
		return AudiencePreview{}, ErrForbidden
	}
	if err := Authorize(ctx, s.DB, p, filter.ClubID, false); err != nil {
		return AudiencePreview{}, err
	}
	return previewAudience(ctx, s.DB, filter)
}

func previewAudience(ctx context.Context, q DBTX, filter AudienceFilter) (AudiencePreview, error) {
	if filter.SeasonID <= 0 || filter.CompetitionID < 0 || filter.ClubID < 0 || (filter.Role != "" && filter.Role != "junior_lead" && filter.Role != "coach" && filter.Role != "safeguarding_officer") || (filter.AgeGroup != "" && filter.AgeGroup != "U9" && filter.AgeGroup != "U11" && filter.AgeGroup != "U13" && filter.AgeGroup != "U15" && filter.AgeGroup != "U18") {
		return AudiencePreview{}, ErrValidation
	}
	// Contact eligibility is dated and season-specific. A verified lead account
	// is included independently of contact roles, only for a lead/all audience.
	rows, err := q.Query(ctx, `SELECT DISTINCT c.club_id,c.id,c.email,c.full_name FROM junior_contacts c JOIN junior_contact_roles r ON r.contact_id=c.id AND r.club_id=c.club_id
        WHERE NOT c.archived AND btrim(c.email)<>'' AND r.season_id=$1 AND r.starts_on<=(now() AT TIME ZONE 'Europe/London')::date AND (r.ends_on IS NULL OR r.ends_on>=(now() AT TIME ZONE 'Europe/London')::date)
        AND EXISTS(SELECT 1 FROM junior_club_seasons cs WHERE cs.club_id=c.club_id AND cs.season_id=$1 AND cs.state='active')
        AND ($2::integer=0 OR c.club_id=$2) AND ($3::text='' OR r.role=$3) AND ($4::text='' OR r.age_group=$4)
        AND ($5::bigint=0 OR EXISTS(SELECT 1 FROM junior_competition_entries e JOIN junior_teams t ON t.id=e.team_id JOIN junior_competitions competition ON competition.id=e.competition_id WHERE e.competition_id=$5 AND competition.season_id=$1 AND t.club_id=c.club_id AND e.active AND NOT t.archived AND (r.role<>'coach' OR (r.age_group=competition.age_group AND (r.team_id IS NULL OR r.team_id=t.id)))))
        UNION ALL SELECT m.club_id,0::bigint,a.email,a.email FROM junior_memberships m JOIN junior_accounts a ON a.id=m.account_id
        WHERE m.revoked_at IS NULL AND a.is_active AND ($2::integer=0 OR m.club_id=$2) AND ($3::text='' OR $3='junior_lead') AND $4::text=''
        AND EXISTS(SELECT 1 FROM junior_club_seasons cs WHERE cs.club_id=m.club_id AND cs.season_id=$1 AND cs.state='active')
        AND ($5::bigint=0 OR EXISTS(SELECT 1 FROM junior_competition_entries e JOIN junior_teams t ON t.id=e.team_id JOIN junior_competitions competition ON competition.id=e.competition_id WHERE e.competition_id=$5 AND competition.season_id=$1 AND t.club_id=m.club_id AND e.active AND NOT t.archived))`, filter.SeasonID, filter.ClubID, filter.Role, filter.AgeGroup, filter.CompetitionID)
	if err != nil {
		return AudiencePreview{}, err
	}
	defer rows.Close()
	result := AudiencePreview{Recipients: []Recipient{}}
	for rows.Next() {
		var item Recipient
		if err := rows.Scan(&item.ClubID, &item.ContactID, &item.Email, &item.Name); err != nil {
			return AudiencePreview{}, err
		}
		canonical, err := CanonicalEmail(item.Email)
		if err != nil {
			continue
		}
		item.Email = canonical
		result.Recipients = append(result.Recipients, item)
	}
	if err = rows.Err(); err != nil {
		return AudiencePreview{}, err
	}
	sort.Slice(result.Recipients, func(i, j int) bool {
		a, b := result.Recipients[i], result.Recipients[j]
		if a.ClubID != b.ClubID {
			return a.ClubID < b.ClubID
		}
		if a.Email != b.Email {
			return a.Email < b.Email
		}
		return a.ContactID < b.ContactID
	})
	encoded, err := json.Marshal(struct {
		Filter     AudienceFilter
		Recipients []Recipient
	}{filter, result.Recipients})
	if err != nil {
		return AudiencePreview{}, err
	}
	sum := sha256.Sum256(encoded)
	result.Hash = hex.EncodeToString(sum[:])
	return result, nil
}

func (s *Service) ComposeMessage(ctx context.Context, p Principal, filter AudienceFilter, expectedHash, subject, body, key string) (int64, error) {
	if p.AdminID <= 0 {
		return 0, ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, filter.ClubID, true); err != nil {
		return 0, err
	}
	preview, err := previewAudience(ctx, tx, filter)
	if err != nil {
		return 0, err
	}
	if expectedHash == "" || preview.Hash != expectedHash {
		return 0, ErrConflict
	}
	id, err := Enqueue(ctx, tx, p, MessageIntent{SeasonID: filter.SeasonID, ClubID: filter.ClubID, Kind: "staff_message", Key: key, Subject: subject, Body: body, Recipients: preview.Recipients})
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) ResolveMessageRecipients(ctx context.Context, p Principal, messageID int64, filter AudienceFilter, expectedHash string) error {
	if p.AdminID <= 0 {
		return ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, filter.ClubID, true); err != nil {
		return err
	}
	var clubID int32
	var seasonID int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(club_id,0),season_id FROM junior_messages WHERE id=$1 FOR UPDATE`, messageID).Scan(&clubID, &seasonID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if clubID != filter.ClubID || seasonID != filter.SeasonID {
		return ErrForbidden
	}
	var hasRecipients bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_message_recipients WHERE message_id=$1)`, messageID).Scan(&hasRecipients); err != nil {
		return err
	}
	if hasRecipients {
		return ErrConflict
	}
	preview, err := previewAudience(ctx, tx, filter)
	if err != nil {
		return err
	}
	if expectedHash == "" || preview.Hash != expectedHash {
		return ErrConflict
	}
	if len(preview.Recipients) == 0 {
		return fmt.Errorf("%w: no eligible email recipients", ErrValidation)
	}
	if err = appendMessageRecipients(ctx, tx, messageID, clubID, preview.Recipients); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, clubID, "message", messageID, "audience_resolved", map[string]any{"audience_hash": expectedHash}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
