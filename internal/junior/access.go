package junior

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	InviteLifetime  = 7 * 24 * time.Hour
	LoginLifetime   = 15 * time.Minute
	SessionLifetime = 8 * time.Hour
)

// Authorize must be called inside the mutation transaction for writes. The
// club/session/membership locks keep a concurrent revocation from authorising
// work after its transaction commits. Contact roles never confer access.
func Authorize(ctx context.Context, q DBTX, p Principal, clubID int32, write bool) error {
	if write && p.ReadOnly {
		return ErrForbidden
	}
	if p.ReadOnly && (p.ClubID == 0 || p.ClubID != clubID) {
		return ErrForbidden
	}
	if p.AdminID > 0 {
		var role, username, email string
		sql := `SELECT COALESCE(role,'admin'),COALESCE(username,''),COALESCE(email,'') FROM admin_users WHERE id=$1 AND is_active`
		if write {
			sql += ` FOR SHARE`
		}
		if err := q.QueryRow(ctx, sql, p.AdminID).Scan(&role, &username, &email); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return err
		}
		if role == "super_admin" || configuredJuniorSuperAdmin(username, email) {
			return nil
		}
		sql = `SELECT permission FROM admin_user_permissions WHERE admin_user_id=$1 AND permission='junior_manage'`
		if write {
			sql += ` FOR SHARE`
		}
		var permission string
		if err := q.QueryRow(ctx, sql, p.AdminID).Scan(&permission); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return err
		}
		return nil
	}
	if p.SessionID <= 0 || p.AccountID <= 0 || clubID <= 0 || p.ClubID != clubID {
		return ErrForbidden
	}
	if write {
		var id int32
		if err := q.QueryRow(ctx, `SELECT id FROM clubs WHERE id=$1 FOR SHARE`, clubID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return err
		}
	}
	sql := `SELECT s.id FROM junior_sessions s JOIN junior_memberships m ON m.id=s.membership_id JOIN junior_accounts a ON a.id=m.account_id
        WHERE s.id=$1 AND a.id=$2 AND m.club_id=$3 AND a.is_active AND m.revoked_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>now()`
	if write {
		sql += ` FOR SHARE OF s,m,a`
	}
	var id int64
	if err := q.QueryRow(ctx, sql, p.SessionID, p.AccountID, clubID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	return nil
}

func configuredJuniorSuperAdmin(username, email string) bool {
	for _, value := range append([]string{"warren2314"}, strings.Split(os.Getenv("SUPER_ADMIN_EMAILS"), ",")...) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && (value == strings.ToLower(strings.TrimSpace(username)) || value == strings.ToLower(strings.TrimSpace(email))) {
			return true
		}
	}
	return false
}

func newAccessSecret() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, accessHash(token), nil
}
func accessHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func validAccessSecret(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == token
}

type SessionGrant struct {
	Token     string
	ExpiresAt time.Time
	Principal Principal
}
type LeadAccess struct {
	MembershipID int64
	InviteID     int64
	Email        string
	Status       string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
}

// LeadAccess is staff-only: a contact's public role assignment is not an account.
func (s *Service) LeadAccess(ctx context.Context, p Principal, clubID int32) ([]LeadAccess, error) {
	if p.AdminID <= 0 {
		return nil, ErrForbidden
	}
	if err := Authorize(ctx, s.DB, p, clubID, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT m.id,0::bigint,a.email,'active',m.accepted_at,NULL::timestamptz FROM junior_memberships m JOIN junior_accounts a ON a.id=m.account_id WHERE m.club_id=$1 AND m.revoked_at IS NULL
        UNION ALL SELECT 0::bigint,t.id,a.email,t.delivery_state,t.created_at,t.expires_at FROM junior_access_tokens t JOIN junior_accounts a ON a.id=t.account_id WHERE t.club_id=$1 AND t.purpose='invite' AND t.consumed_at IS NULL AND t.revoked_at IS NULL AND t.expires_at>now() ORDER BY 5 DESC`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []LeadAccess{}
	for rows.Next() {
		var item LeadAccess
		if err := rows.Scan(&item.MembershipID, &item.InviteID, &item.Email, &item.Status, &item.CreatedAt, &item.ExpiresAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// InviteLead preserves the current lead until the new mailbox accepts the
// club-bound invitation. Only a hash is stored; failed delivery cannot activate it.
func (s *Service) InviteLead(ctx context.Context, p Principal, clubID int32, email string) error {
	if p.AdminID <= 0 {
		return ErrForbidden
	}
	canonical, err := CanonicalEmail(email)
	if err != nil {
		return err
	}
	if err = s.accessDeliveryReady(); err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	var id int32
	if err = tx.QueryRow(ctx, `SELECT id FROM clubs WHERE id=$1 FOR UPDATE`, clubID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var accountID int64
	if err = tx.QueryRow(ctx, `INSERT INTO junior_accounts(email) VALUES($1) ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email RETURNING id`, canonical).Scan(&accountID); err != nil {
		return err
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT is_active FROM junior_accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ErrForbidden
	}
	var expected *int64
	if err = tx.QueryRow(ctx, `SELECT id FROM junior_memberships WHERE club_id=$1 AND revoked_at IS NULL`, clubID).Scan(&expected); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var recent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_access_tokens WHERE club_id=$1 AND purpose='invite' AND account_id=$2 AND created_at>now()-interval '1 minute' AND revoked_at IS NULL AND consumed_at IS NULL)`, clubID, accountID).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return fmt.Errorf("%w: wait one minute before resending", ErrConflict)
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_access_tokens SET revoked_at=now() WHERE club_id=$1 AND purpose='invite' AND consumed_at IS NULL AND revoked_at IS NULL`, clubID); err != nil {
		return err
	}
	raw, hash, err := newAccessSecret()
	if err != nil {
		return err
	}
	var tokenID int64
	if err = tx.QueryRow(ctx, `INSERT INTO junior_access_tokens(token_hash,purpose,account_id,club_id,expected_membership_id,created_by_admin_id,expires_at) VALUES($1,'invite',$2,$3,$4,$5,now()+interval '7 days') RETURNING id`, hash, accountID, clubID, expected, p.AdminID).Scan(&tokenID); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, clubID, "access_invite", tokenID, "invited", map[string]any{"email": canonical, "replaces_membership_id": expected}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return s.deliverAccess(ctx, tokenID, canonical, "GMCL Junior Lead invitation", raw, clubID)
}

func (s *Service) RevokeLead(ctx context.Context, p Principal, clubID int32, membershipID int64) error {
	if p.AdminID <= 0 || membershipID <= 0 {
		return ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	var locked int32
	if err = tx.QueryRow(ctx, `SELECT id FROM clubs WHERE id=$1 FOR UPDATE`, clubID).Scan(&locked); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE junior_memberships SET revoked_at=now(),revoked_by_admin_id=$3 WHERE id=$1 AND club_id=$2 AND revoked_at IS NULL`, membershipID, clubID, p.AdminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_sessions SET revoked_at=now() WHERE membership_id=$1 AND revoked_at IS NULL`, membershipID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_access_tokens SET revoked_at=now() WHERE club_id=$1 AND consumed_at IS NULL AND revoked_at IS NULL`, clubID); err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, clubID, "membership", membershipID, "revoked", map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) RevokeInvite(ctx context.Context, p Principal, clubID int32, inviteID int64) error {
	if p.AdminID <= 0 || inviteID <= 0 {
		return ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = Authorize(ctx, tx, p, clubID, true); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE junior_access_tokens SET revoked_at=now() WHERE id=$1 AND club_id=$2 AND purpose='invite' AND consumed_at IS NULL AND revoked_at IS NULL`, inviteID, clubID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	if err = Audit(ctx, tx, p, clubID, "access_invite", inviteID, "revoked", map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RequestLogin always succeeds for an unknown mailbox. A mailbox can lead more
// than one club, but each emailed link and resulting session binds one membership.
func (s *Service) RequestLogin(ctx context.Context, email string) error {
	if err := s.accessDeliveryReady(); err != nil {
		return err
	}
	canonical, err := CanonicalEmail(email)
	if err != nil {
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accountID int64
	if err = tx.QueryRow(ctx, `SELECT id FROM junior_accounts WHERE email=$1 AND is_active`, canonical).Scan(&accountID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	// Use the same club-before-account order as invitations and acceptance.
	// Token foreign keys acquire club key-share locks too. Locking the account
	// first could deadlock with a concurrent handover holding that club.
	clubRows, err := tx.Query(ctx, `SELECT club_id FROM junior_memberships WHERE account_id=$1 AND revoked_at IS NULL ORDER BY club_id`, accountID)
	if err != nil {
		return err
	}
	clubIDs := []int32{}
	for clubRows.Next() {
		var clubID int32
		if err = clubRows.Scan(&clubID); err != nil {
			clubRows.Close()
			return err
		}
		clubIDs = append(clubIDs, clubID)
	}
	clubRows.Close()
	if err = clubRows.Err(); err != nil {
		return err
	}
	if len(clubIDs) == 0 {
		return nil
	}
	clubRows, err = tx.Query(ctx, `SELECT id FROM clubs WHERE id=ANY($1::integer[]) ORDER BY id FOR SHARE`, clubIDs)
	if err != nil {
		return err
	}
	for clubRows.Next() {
		var id int32
		if err = clubRows.Scan(&id); err != nil {
			clubRows.Close()
			return err
		}
	}
	clubRows.Close()
	if err = clubRows.Err(); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT id FROM junior_accounts WHERE id=$1 AND is_active FOR UPDATE`, accountID).Scan(&accountID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var recent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM junior_access_tokens WHERE account_id=$1 AND purpose='login' AND created_at>now()-interval '1 minute')`, accountID).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return nil
	}
	// A newly accepted membership outside this locked set is picked up by the
	// next request, rather than introducing an account-before-club lock here.
	rows, err := tx.Query(ctx, `SELECT id,club_id FROM junior_memberships WHERE account_id=$1 AND revoked_at IS NULL AND club_id=ANY($2::integer[]) ORDER BY club_id`, accountID, clubIDs)
	if err != nil {
		return err
	}
	type pending struct {
		membershipID int64
		clubID       int32
		tokenID      int64
		raw          string
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.membershipID, &item.clubID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range items {
		raw, hash, e := newAccessSecret()
		if e != nil {
			return e
		}
		items[i].raw = raw
		if err = tx.QueryRow(ctx, `INSERT INTO junior_access_tokens(token_hash,purpose,account_id,club_id,membership_id,expires_at) VALUES($1,'login',$2,$3,$4,now()+interval '15 minutes') RETURNING id`, hash, accountID, items[i].clubID, items[i].membershipID).Scan(&items[i].tokenID); err != nil {
			return err
		}
		if err = Audit(ctx, tx, Principal{AccountID: accountID}, items[i].clubID, "access_login", items[i].tokenID, "requested", map[string]any{}); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, item := range items {
		if err = s.deliverAccess(ctx, item.tokenID, canonical, "GMCL junior portal sign-in", item.raw, item.clubID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RedeemAccess(ctx context.Context, raw string) (SessionGrant, error) {
	if !PortalEnabled() {
		return SessionGrant{}, ErrTransportUnavailable
	}
	if !validAccessSecret(raw) {
		return SessionGrant{}, ErrForbidden
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return SessionGrant{}, err
	}
	defer tx.Rollback(ctx)
	hash := accessHash(raw)
	var clubID int32
	if err = tx.QueryRow(ctx, `SELECT club_id FROM junior_access_tokens WHERE token_hash=$1`, hash).Scan(&clubID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SessionGrant{}, ErrForbidden
		}
		return SessionGrant{}, err
	}
	var locked int32
	if err = tx.QueryRow(ctx, `SELECT id FROM clubs WHERE id=$1 FOR UPDATE`, clubID).Scan(&locked); err != nil {
		return SessionGrant{}, err
	}
	var tokenID, accountID int64
	var purpose string
	var membershipID, expected *int64
	if err = tx.QueryRow(ctx, `SELECT t.id,t.account_id,t.purpose,t.membership_id,t.expected_membership_id FROM junior_access_tokens t JOIN junior_accounts a ON a.id=t.account_id WHERE t.token_hash=$1 AND a.is_active AND t.revoked_at IS NULL AND t.consumed_at IS NULL AND t.expires_at>now() FOR UPDATE OF t,a`, hash).Scan(&tokenID, &accountID, &purpose, &membershipID, &expected); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SessionGrant{}, ErrForbidden
		}
		return SessionGrant{}, err
	}
	if purpose == "invite" {
		var current *int64
		if err = tx.QueryRow(ctx, `SELECT id FROM junior_memberships WHERE club_id=$1 AND revoked_at IS NULL FOR UPDATE`, clubID).Scan(&current); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return SessionGrant{}, err
		}
		if (current == nil) != (expected == nil) || (current != nil && *current != *expected) {
			return SessionGrant{}, ErrConflict
		}
		if current != nil {
			if _, err = tx.Exec(ctx, `UPDATE junior_memberships SET revoked_at=now() WHERE id=$1`, *current); err != nil {
				return SessionGrant{}, err
			}
			if _, err = tx.Exec(ctx, `UPDATE junior_sessions SET revoked_at=now() WHERE membership_id=$1 AND revoked_at IS NULL`, *current); err != nil {
				return SessionGrant{}, err
			}
		}
		var created int64
		if err = tx.QueryRow(ctx, `INSERT INTO junior_memberships(account_id,club_id) VALUES($1,$2) RETURNING id`, accountID, clubID).Scan(&created); err != nil {
			return SessionGrant{}, err
		}
		membershipID = &created
		if _, err = tx.Exec(ctx, `UPDATE junior_access_tokens SET revoked_at=now() WHERE club_id=$1 AND id<>$2 AND consumed_at IS NULL AND revoked_at IS NULL`, clubID, tokenID); err != nil {
			return SessionGrant{}, err
		}
	} else if purpose == "login" && membershipID != nil {
		var id int64
		if err = tx.QueryRow(ctx, `SELECT id FROM junior_memberships WHERE id=$1 AND account_id=$2 AND club_id=$3 AND revoked_at IS NULL FOR SHARE`, *membershipID, accountID, clubID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return SessionGrant{}, ErrForbidden
			}
			return SessionGrant{}, err
		}
	} else {
		return SessionGrant{}, ErrForbidden
	}
	sessionToken, sessionHash, err := newAccessSecret()
	if err != nil {
		return SessionGrant{}, err
	}
	grant := SessionGrant{Token: sessionToken, Principal: Principal{AccountID: accountID, ClubID: clubID}}
	if err = tx.QueryRow(ctx, `INSERT INTO junior_sessions(token_hash,membership_id,expires_at) VALUES($1,$2,now()+interval '8 hours') RETURNING id,expires_at`, sessionHash, *membershipID).Scan(&grant.Principal.SessionID, &grant.ExpiresAt); err != nil {
		return SessionGrant{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE junior_access_tokens SET consumed_at=now() WHERE id=$1`, tokenID); err != nil {
		return SessionGrant{}, err
	}
	if err = Audit(ctx, tx, grant.Principal, clubID, "access_token", tokenID, "accepted_"+purpose, map[string]any{"membership_id": *membershipID}); err != nil {
		return SessionGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SessionGrant{}, err
	}
	return grant, nil
}

func (s *Service) SessionPrincipal(ctx context.Context, raw string) (Principal, error) {
	if !validAccessSecret(raw) {
		return Principal{}, ErrForbidden
	}
	var p Principal
	err := s.DB.QueryRow(ctx, `SELECT s.id,a.id,m.club_id FROM junior_sessions s JOIN junior_memberships m ON m.id=s.membership_id JOIN junior_accounts a ON a.id=m.account_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND m.revoked_at IS NULL AND a.is_active`, accessHash(raw)).Scan(&p.SessionID, &p.AccountID, &p.ClubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrForbidden
	}
	return p, err
}

func (s *Service) Logout(ctx context.Context, raw string) error {
	if !validAccessSecret(raw) {
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var p Principal
	err = tx.QueryRow(ctx, `UPDATE junior_sessions s SET revoked_at=now() FROM junior_memberships m WHERE s.membership_id=m.id AND s.token_hash=$1 AND s.revoked_at IS NULL RETURNING s.id,m.account_id,m.club_id`, accessHash(raw)).Scan(&p.SessionID, &p.AccountID, &p.ClubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = Audit(ctx, tx, p, p.ClubID, "session", p.SessionID, "logged_out", map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
