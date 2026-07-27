package portal

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestStaffCampaignCreatesIsolatedClubCasesAndTracksDelivery(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := NewStore(pool, DefaultSessionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeSecurity(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	var (
		adminID       int32
		scopedAdminID int32
		clubA         int32
		clubB         int32
		userA         = uuid.New()
		userB         = uuid.New()
		memberA       = uuid.New()
		memberB       = uuid.New()
		roleA         = uuid.New()
		roleB         = uuid.New()
	)
	if err := func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1804289383, 9051)`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(id), 0) + 2001 FROM clubs
		`).Scan(&clubA); err != nil {
			return err
		}
		clubB = clubA + 1
		if _, err := tx.Exec(ctx, `
			INSERT INTO clubs (id, name, short_name)
			VALUES
				($1, $2, 'CAMA'),
				($3, $4, 'CAMB')
		`, clubA, "Campaign Club A "+suffix,
			clubB, "Campaign Club B "+suffix); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO admin_users (
				username, password_hash, email, is_active, role
			)
			VALUES ($1, decode('00', 'hex'), $2, TRUE, 'super_admin')
			RETURNING id
		`, "campaign-admin-"+suffix, "campaign-admin-"+suffix+"@example.test").
			Scan(&adminID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO admin_users (
				username, password_hash, email, is_active, role
			)
			VALUES ($1, decode('00', 'hex'), $2, TRUE, 'admin')
			RETURNING id
		`, "campaign-scoped-admin-"+suffix, "campaign-scoped-admin-"+suffix+"@example.test").
			Scan(&scopedAdminID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_users (id, display_name, status)
			VALUES
				($1, 'Campaign Recipient A', 'active'),
				($2, 'Campaign Recipient B', 'active')
		`, userA, userB); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_identities (
				user_id, issuer, subject, verified_email, email_verified
			)
			VALUES
				($1, 'https://integration.test', $2, $3, TRUE),
				($4, 'https://integration.test', $5, $6, TRUE)
		`, userA, "campaign-a-"+suffix, "campaign-a-"+suffix+"@example.test",
			userB, "campaign-b-"+suffix, "campaign-b-"+suffix+"@example.test"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_club_memberships (
				id, user_id, club_id, status, approved_at
			)
			VALUES
				($1, $2, $3, 'active', now()),
				($4, $5, $6, 'active', now())
		`, memberA, userA, clubA, memberB, userB, clubB); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_role_assignments (
				id, membership_id, user_id, club_id, role_key, status,
				grant_reason
			)
			VALUES
				($1, $2, $3, $4, 'club_primary_admin', 'active', 'integration test'),
				($5, $6, $7, $8, 'club_primary_admin', 'active', 'integration test')
		`, roleA, memberA, userA, clubA,
			roleB, memberB, userB, clubB); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_club_features (
				club_id, feature_key, enabled, enabled_at,
				enabled_by_admin_user_id
			)
			VALUES
				($1, 'portal_access', TRUE, now(), $3),
				($1, 'secure_messaging', TRUE, now(), $3),
				($2, 'portal_access', TRUE, now(), $3),
				($2, 'secure_messaging', TRUE, now(), $3)
		`, clubA, clubB, adminID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateStaffAssignment(ctx, StaffAssignmentRequest{
		AdminUserID: scopedAdminID,
		Role:        StaffRoleClubLiaison,
		ClubID:      &clubA,
		GrantReason: "integration CLO scope",
		GrantedBy:   adminID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateStaffAssignment(ctx, StaffAssignmentRequest{
		AdminUserID: scopedAdminID,
		Role:        StaffRoleJuniorAdministrator,
		ClubID:      &clubB,
		GrantReason: "integration junior scope",
		GrantedBy:   adminID,
	}); err != nil {
		t.Fatal(err)
	}
	scopedAccess, err := store.LoadStaffAccess(ctx, scopedAdminID)
	if err != nil {
		t.Fatal(err)
	}
	if !scopedAccess.CanAccessCase(clubA, MessageCategoryGeneral, nil) {
		t.Fatal("club-scoped CLO assignment was not effective")
	}
	if scopedAccess.CanAccessCase(clubB, MessageCategoryGeneral, nil) {
		t.Fatal("Junior Administrator received non-junior access")
	}
	if !scopedAccess.CanAccessCase(clubB, MessageCategoryJunior, nil) {
		t.Fatal("club-scoped Junior Administrator assignment was not effective")
	}
	if _, err := store.CreateStaffCampaign(ctx, scopedAdminID, StaffCampaignRequest{
		Category:      MessageCategoryGeneral,
		RecipientRole: RecipientPrimaryContact,
		ClubIDs:       []int32{clubB},
		Subject:       "Out-of-scope campaign " + suffix,
		Body:          "This campaign must be denied.",
		Priority:      "normal",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("out-of-scope campaign error = %v, want forbidden", err)
	}

	result, err := store.CreateStaffCampaign(ctx, adminID, StaffCampaignRequest{
		Category:      MessageCategoryGeneral,
		RecipientRole: RecipientPrimaryContact,
		ClubIDs:       []int32{clubA, clubB},
		Subject:       "Integration campaign " + suffix,
		Body:          "Private message for one club.",
		Priority:      "normal",
		CorrelationID: "integration-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetCount != 2 || len(result.Deliveries) != 2 {
		t.Fatalf("campaign targets=%d deliveries=%d", result.TargetCount, len(result.Deliveries))
	}
	if result.Deliveries[0].SenderRole != StaffRoleSuperAdministrator ||
		!strings.HasPrefix(result.Deliveries[0].SenderName, "campaign-admin-") {
		t.Fatalf("unexpected campaign sender identity: %#v", result.Deliveries[0])
	}

	if err := store.CompleteCampaignDelivery(ctx, result.Deliveries[0].ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCampaignDelivery(ctx, result.Deliveries[1].ID, false, "synthetic SMTP failure"); err != nil {
		t.Fatal(err)
	}

	access, err := store.LoadStaffAccess(ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	campaigns, err := store.ListStaffCampaigns(ctx, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(campaigns) == 0 || campaigns[0].ID != result.ID {
		t.Fatal("created campaign is absent from sender work queue")
	}
	if campaigns[0].Status != "partially_failed" ||
		campaigns[0].SentTargetCount != 1 ||
		campaigns[0].FailedTargetCount != 1 {
		t.Fatalf("unexpected campaign delivery summary: %#v", campaigns[0])
	}

	principalA := Principal{
		UserID: userA,
		Assignment: &Assignment{
			ID:           roleA,
			MembershipID: memberA,
			UserID:       userA,
			Role:         RoleClubPrimaryAdmin,
			Scope:        Scope{ClubID: clubA},
			Status:       "active",
			StartsAt:     time.Now().Add(-time.Minute),
			Version:      1,
		},
	}
	var clubACaseID uuid.UUID
	for _, delivery := range result.Deliveries {
		if delivery.ClubID == clubA {
			clubACaseID = delivery.CaseID
			break
		}
	}
	if clubACaseID == uuid.Nil {
		t.Fatal("Club A case was not returned")
	}
	if _, err := store.ReplyMessageCase(
		ctx,
		principalA,
		clubACaseID,
		"Club A integration reply.",
		"integration-reply-"+suffix,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeMessageCase(
		ctx,
		principalA,
		clubACaseID,
		"integration-ack-"+suffix,
	); err != nil {
		t.Fatal(err)
	}
	campaigns, err = store.ListStaffCampaigns(ctx, access)
	if err != nil {
		t.Fatal(err)
	}
	if campaigns[0].AcknowledgedCount != 1 || campaigns[0].ClubReplyCount != 1 {
		t.Fatalf("campaign response summary = %#v", campaigns[0])
	}
	if err := store.WithTenantTx(ctx, principalA, func(tx pgx.Tx, _ Assignment) error {
		var ownCases int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM portal_message_cases
			WHERE campaign_id = $1
		`, result.ID).Scan(&ownCases); err != nil {
			return err
		}
		if ownCases != 1 {
			t.Fatalf("Club A saw %d campaign cases, want 1", ownCases)
		}
		var internalTargets int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM portal_message_campaign_targets
			WHERE campaign_id = $1
		`, result.ID).Scan(&internalTargets); err != nil {
			return err
		}
		if internalTargets != 0 {
			t.Fatalf("Club A saw %d internal campaign targets", internalTargets)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
