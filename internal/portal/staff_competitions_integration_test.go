package portal

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestStaffCompetitionContextsCreateMapListAndEnd(t *testing.T) {
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
	configuredAdminEmail := "configured-competition-admin-" + suffix +
		"@example.test"
	var (
		adminID           int32
		configuredAdminID int32
		scopedAdminID     int32
		seasonID          int32
		clubA             int32
		clubB             int32
		legacyID          uuid.UUID
		scheduledID       uuid.UUID
	)
	if err := func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := tx.QueryRow(ctx, `
			INSERT INTO admin_users (
				username, password_hash, email, is_active, role
			)
			VALUES ($1, decode('00', 'hex'), $2, TRUE, 'super_admin')
			RETURNING id
		`, "competition-admin-"+suffix,
			"competition-admin-"+suffix+"@example.test").Scan(&adminID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO admin_users (
				username, password_hash, email, is_active, role
			)
			VALUES ($1, decode('00', 'hex'), $2, TRUE, 'admin')
			RETURNING id
		`, "configured-competition-admin-"+suffix,
			configuredAdminEmail).Scan(&configuredAdminID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO admin_users (
				username, password_hash, email, is_active, role
			)
			VALUES ($1, decode('00', 'hex'), $2, TRUE, 'admin')
			RETURNING id
		`, "competition-scoped-admin-"+suffix,
			"competition-scoped-admin-"+suffix+"@example.test").Scan(&scopedAdminID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO seasons (name, start_date, end_date)
			VALUES ($1, CURRENT_DATE, CURRENT_DATE + 180)
			RETURNING id
		`, "Competition Season "+suffix).Scan(&seasonID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO clubs (name, short_name)
			VALUES ($1, 'CCA')
			RETURNING id
		`, "Competition Club A "+suffix).Scan(&clubA); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO clubs (name, short_name)
			VALUES ($1, 'CCB')
			RETURNING id
		`, "Competition Club B "+suffix).Scan(&clubB); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO portal_competitions (
				season_id, name, starts_at
			)
			VALUES ($1, $2, now())
			RETURNING id
		`, seasonID, "Legacy Competition "+suffix).Scan(&legacyID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO portal_competitions (
				season_id, name, starts_at
			)
			VALUES ($1, $2, now() + interval '1 day')
			RETURNING id
		`, seasonID, "Scheduled Competition "+suffix).Scan(&scheduledID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUPER_ADMIN_EMAILS", configuredAdminEmail)

	configuredAccess, err := store.LoadStaffAccess(ctx, configuredAdminID)
	if err != nil {
		t.Fatalf("load configured super-admin access: %v", err)
	}
	if !configuredAccess.SuperAdmin {
		t.Fatal("configured super-admin was not recognized by portal access")
	}
	if err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if err := requireActiveSuperAdminTx(
			ctx,
			tx,
			configuredAdminID,
		); err != nil {
			return err
		}
		return revalidateStaffCampaignAccess(
			ctx,
			tx,
			configuredAdminID,
			StaffRoleSuperAdministrator,
			MessageCategoryGeneral,
			[]int32{clubA},
			nil,
		)
	}); err != nil {
		t.Fatalf("configured super-admin transaction authorization: %v", err)
	}

	all, err := store.ListAllStaffCompetitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var legacy *StaffCompetition
	for index := range all {
		if all[index].ID == legacyID {
			legacy = &all[index]
			break
		}
	}
	if legacy == nil || legacy.Active || !legacy.Manageable ||
		!legacy.Endable || len(legacy.ClubIDs) != 0 {
		t.Fatalf("legacy unmapped context = %#v", legacy)
	}
	var scheduled *StaffCompetition
	for index := range all {
		if all[index].ID == scheduledID {
			scheduled = &all[index]
			break
		}
	}
	if scheduled == nil || scheduled.Active || !scheduled.Manageable ||
		scheduled.Endable {
		t.Fatalf("scheduled context = %#v", scheduled)
	}
	if err := store.UpdateStaffCompetitionClubs(
		ctx,
		scheduledID,
		[]int32{clubA},
		adminID,
	); err != nil {
		t.Fatalf("map scheduled context: %v", err)
	}
	if err := store.EndStaffCompetition(
		ctx,
		scheduledID,
		adminID,
		"cancel before start",
	); err == nil {
		t.Fatal("scheduled context was ended with an invalid date range")
	}
	if err := store.UpdateStaffCompetitionClubs(
		ctx,
		legacyID,
		[]int32{clubA},
		adminID,
	); err != nil {
		t.Fatalf("seed first mapping on legacy context: %v", err)
	}
	activeAfterLegacyMapping, err := store.ListStaffCompetitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacyActive := false
	for _, competition := range activeAfterLegacyMapping {
		if competition.ID == legacyID && competition.ContainsClub(clubA) {
			legacyActive = true
			break
		}
	}
	if !legacyActive {
		t.Fatal("legacy context did not become active after its first mapping")
	}

	firstID, err := store.CreateStaffCompetition(ctx, StaffCompetitionRequest{
		Name:      "  Premier " + suffix + "  ",
		SeasonID:  seasonID,
		ClubIDs:   []int32{clubA, clubB},
		CreatedBy: adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateStaffCompetition(ctx, StaffCompetitionRequest{
		Name:      "Division One " + suffix,
		SeasonID:  seasonID,
		ClubIDs:   []int32{clubA},
		CreatedBy: adminID,
	}); err != nil {
		t.Fatalf("second manual context for the same season failed: %v", err)
	}

	active, err := store.ListStaffCompetitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var first *StaffCompetition
	for index := range active {
		if active[index].ID == firstID {
			first = &active[index]
			break
		}
	}
	if first == nil || first.Name != "Premier "+suffix ||
		!first.ContainsClub(clubA) || !first.ContainsClub(clubB) {
		t.Fatalf("created context was not listed correctly: %#v", first)
	}
	if _, err := store.CreateStaffAssignment(ctx, StaffAssignmentRequest{
		AdminUserID:   scopedAdminID,
		Role:          StaffRoleClubLiaison,
		CompetitionID: &firstID,
		GrantReason:   "forged assignment",
		GrantedBy:     scopedAdminID,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-super-admin assignment error = %v", err)
	}
	assignmentID, err := store.CreateStaffAssignment(ctx, StaffAssignmentRequest{
		AdminUserID:   scopedAdminID,
		Role:          StaffRoleClubLiaison,
		CompetitionID: &firstID,
		GrantReason:   "competition scope integration test",
		GrantedBy:     adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeStaffAssignment(
		ctx,
		assignmentID,
		scopedAdminID,
		"forged revocation",
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-super-admin revocation error = %v", err)
	}
	scopedAccess, err := store.LoadStaffAccess(ctx, scopedAdminID)
	if err != nil {
		t.Fatal(err)
	}
	if !scopedAccess.CanAccessCase(clubA, MessageCategoryGeneral, &firstID) ||
		!scopedAccess.CanAccessCase(clubB, MessageCategoryGeneral, &firstID) {
		t.Fatal("competition scope did not include its mapped clubs")
	}

	var (
		clubBMappingCreatedAt time.Time
		clubBMappingCreator   int32
	)
	if err := pool.QueryRow(ctx, `
		SELECT created_at, created_by_admin_user_id
		FROM portal_competition_clubs
		WHERE competition_id = $1 AND club_id = $2
	`, firstID, clubB).Scan(
		&clubBMappingCreatedAt,
		&clubBMappingCreator,
	); err != nil {
		t.Fatal(err)
	}
	validationLocked := make(chan struct{})
	releaseValidation := make(chan struct{})
	validationDone := make(chan error, 1)
	go func() {
		validationDone <- store.withSystemTx(ctx, func(tx pgx.Tx) error {
			if err := validateActiveCompetitionContextTx(
				ctx,
				tx,
				firstID,
				[]int32{clubB},
			); err != nil {
				return err
			}
			close(validationLocked)
			<-releaseValidation
			return nil
		})
	}()
	select {
	case <-validationLocked:
	case err := <-validationDone:
		t.Fatalf("lock validation failed early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out acquiring campaign validation lock")
	}
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- store.UpdateStaffCompetitionClubs(
			ctx,
			firstID,
			[]int32{clubB},
			adminID,
		)
	}()
	select {
	case err := <-updateDone:
		t.Fatalf("mapping update bypassed campaign validation lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseValidation)
	if err := <-validationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	active, err = store.ListStaffCompetitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scopedAccess, err = store.LoadStaffAccess(ctx, scopedAdminID)
	if err != nil {
		t.Fatal(err)
	}
	if scopedAccess.CanAccessCase(clubA, MessageCategoryGeneral, &firstID) {
		t.Fatal("competition scope retained a removed club")
	}
	if !scopedAccess.CanAccessCase(clubB, MessageCategoryGeneral, &firstID) {
		t.Fatal("competition scope lost a mapped club")
	}
	var (
		clubBMappingCreatedAtAfter time.Time
		clubBMappingCreatorAfter   int32
	)
	if err := pool.QueryRow(ctx, `
		SELECT created_at, created_by_admin_user_id
		FROM portal_competition_clubs
		WHERE competition_id = $1 AND club_id = $2
	`, firstID, clubB).Scan(
		&clubBMappingCreatedAtAfter,
		&clubBMappingCreatorAfter,
	); err != nil {
		t.Fatal(err)
	}
	if !clubBMappingCreatedAtAfter.Equal(clubBMappingCreatedAt) ||
		clubBMappingCreatorAfter != clubBMappingCreator {
		t.Fatal("unchanged club mapping provenance was replaced")
	}
	var mappingAuditComplete bool
	if err := pool.QueryRow(ctx, `
		SELECT
			metadata->'previous_club_ids' = to_jsonb($2::integer[])
			AND metadata->'club_ids' = to_jsonb($3::integer[])
			AND metadata->'added_club_ids' = '[]'::jsonb
			AND metadata->'removed_club_ids' = to_jsonb($4::integer[])
		FROM portal_audit_events
		WHERE target_id = $1
		  AND action = 'portal.competition_context.clubs_updated'
		ORDER BY occurred_at DESC
		LIMIT 1
	`, firstID.String(), []int32{clubA, clubB},
		[]int32{clubB}, []int32{clubA}).Scan(&mappingAuditComplete); err != nil {
		t.Fatal(err)
	}
	if !mappingAuditComplete {
		t.Fatal("competition mapping audit did not record before/after IDs")
	}
	for index := range active {
		if active[index].ID == firstID {
			if active[index].ContainsClub(clubA) ||
				!active[index].ContainsClub(clubB) {
				t.Fatalf("updated mapping = %#v", active[index].ClubIDs)
			}
		}
	}

	authorizationLocked := make(chan struct{})
	releaseAuthorization := make(chan struct{})
	authorizationDone := make(chan error, 1)
	go func() {
		authorizationDone <- store.withSystemTx(ctx, func(tx pgx.Tx) error {
			if err := validateActiveCompetitionContextTx(
				ctx,
				tx,
				firstID,
				[]int32{clubB},
			); err != nil {
				return err
			}
			if err := revalidateStaffCampaignAccess(
				ctx,
				tx,
				scopedAdminID,
				StaffRoleClubLiaison,
				MessageCategoryGeneral,
				[]int32{clubB},
				&firstID,
			); err != nil {
				return err
			}
			close(authorizationLocked)
			<-releaseAuthorization
			return nil
		})
	}()
	select {
	case <-authorizationLocked:
	case err := <-authorizationDone:
		t.Fatalf("sender authorization failed early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out locking sender authorization")
	}
	revokeDone := make(chan error, 1)
	go func() {
		revokeDone <- store.RevokeStaffAssignment(
			ctx,
			assignmentID,
			adminID,
			"authorization lock regression test",
		)
	}()
	accountMutationDone := make(chan error, 1)
	go func() {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			accountMutationDone <- err
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock($1, $2)`,
			db.AdminUserAdvisoryLockNamespace,
			scopedAdminID,
		); err != nil {
			accountMutationDone <- err
			return
		}
		if _, err := tx.Exec(ctx, `
			UPDATE admin_users SET role = role WHERE id = $1
		`, scopedAdminID); err != nil {
			accountMutationDone <- err
			return
		}
		accountMutationDone <- tx.Commit(ctx)
	}()
	select {
	case err := <-revokeDone:
		t.Fatalf("staff revocation bypassed campaign authorization lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case err := <-accountMutationDone:
		t.Fatalf("account mutation bypassed campaign authorization lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseAuthorization)
	if err := <-authorizationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-revokeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-accountMutationDone; err != nil {
		t.Fatal(err)
	}
	scopedAccess, err = store.LoadStaffAccess(ctx, scopedAdminID)
	if err != nil {
		t.Fatal(err)
	}
	if scopedAccess.CanAccessCase(
		clubB,
		MessageCategoryGeneral,
		&firstID,
	) {
		t.Fatal("revoked assignment continued to grant access")
	}

	if err := store.EndStaffCompetition(
		ctx,
		firstID,
		adminID,
		"competition completed",
	); err != nil {
		t.Fatal(err)
	}
	scopedAccess, err = store.LoadStaffAccess(ctx, scopedAdminID)
	if err != nil {
		t.Fatal(err)
	}
	if scopedAccess.CanAccessCase(clubB, MessageCategoryGeneral, &firstID) {
		t.Fatal("ended competition context continued to grant access")
	}
	active, err = store.ListStaffCompetitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, competition := range active {
		if competition.ID == firstID {
			t.Fatal("ended context remained active")
		}
	}
	all, err = store.ListAllStaffCompetitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ended *StaffCompetition
	for index := range all {
		if all[index].ID == firstID {
			ended = &all[index]
			break
		}
	}
	if ended == nil || ended.Active ||
		ended.EndReason != "competition completed" {
		t.Fatalf("ended context = %#v", ended)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM portal_audit_events
		WHERE target_id = $1
		  AND action IN (
		      'portal.competition_context.created',
		      'portal.competition_context.clubs_updated',
		      'portal.competition_context.ended'
		  )
	`, firstID.String()).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Fatalf("competition audit count = %d, want 3", auditCount)
	}
}
