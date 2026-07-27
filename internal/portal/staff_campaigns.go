package portal

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type StaffRoleKey string

const (
	StaffRoleSuperAdministrator  StaffRoleKey = "super_administrator"
	StaffRoleClubLiaison         StaffRoleKey = "club_liaison_officer"
	StaffRoleJuniorAdministrator StaffRoleKey = "junior_administrator"
)

type RecipientRoleKey string

const (
	RecipientPrimaryContact   RecipientRoleKey = "primary_contact"
	RecipientSecretary        RecipientRoleKey = "secretary"
	RecipientPlayCricketAdmin RecipientRoleKey = "play_cricket_admin"
	RecipientJuniorContact    RecipientRoleKey = "junior_contact"
	RecipientFixturesContact  RecipientRoleKey = "fixtures_contact"
	RecipientRegistration     RecipientRoleKey = "registration_contact"
)

var validRecipientRoles = map[RecipientRoleKey]struct{}{
	RecipientPrimaryContact:   {},
	RecipientSecretary:        {},
	RecipientPlayCricketAdmin: {},
	RecipientJuniorContact:    {},
	RecipientFixturesContact:  {},
	RecipientRegistration:     {},
}

type StaffAssignment struct {
	ID              uuid.UUID
	AdminUserID     int32
	AdminName       string
	AdminEmail      string
	Role            StaffRoleKey
	ClubID          *int32
	ClubName        string
	CompetitionID   *uuid.UUID
	CompetitionName string
	Status          string
	StartsAt        time.Time
	EndsAt          *time.Time
	GrantReason     string
}

type StaffAccess struct {
	AdminUserID int32
	DisplayName string
	Email       string
	SuperAdmin  bool
	Assignments []StaffAssignment
}

type StaffAssignmentRequest struct {
	AdminUserID   int32
	Role          StaffRoleKey
	ClubID        *int32
	CompetitionID *uuid.UUID
	GrantReason   string
	GrantedBy     int32
}

type StaffCampaignRequest struct {
	Category      MessageCategory
	RecipientRole RecipientRoleKey
	CompetitionID *uuid.UUID
	ClubIDs       []int32
	Subject       string
	Body          string
	Priority      string
	CorrelationID string
}

type CampaignDelivery struct {
	ID             uuid.UUID
	CampaignID     uuid.UUID
	TargetID       uuid.UUID
	CaseID         uuid.UUID
	MessageID      uuid.UUID
	ClubID         int32
	ClubName       string
	RecipientEmail string
	RecipientRole  RecipientRoleKey
	Subject        string
	Body           string
	SenderName     string
	SenderRole     StaffRoleKey
}

type StaffCampaignResult struct {
	ID          uuid.UUID
	TargetCount int
	Deliveries  []CampaignDelivery
}

type StaffCampaignSummary struct {
	ID                uuid.UUID
	Subject           string
	Category          MessageCategory
	RecipientRole     RecipientRoleKey
	SenderName        string
	SenderRole        StaffRoleKey
	Status            string
	TargetCount       int
	SentTargetCount   int
	FailedTargetCount int
	AcknowledgedCount int
	ClubReplyCount    int
	CreatedAt         time.Time
}

type StaffCompetition struct {
	ID   uuid.UUID
	Name string
}

func ParseStaffRoleKey(value string) (StaffRoleKey, bool) {
	role := StaffRoleKey(strings.ToLower(strings.TrimSpace(value)))
	switch role {
	case StaffRoleSuperAdministrator, StaffRoleClubLiaison,
		StaffRoleJuniorAdministrator:
		return role, true
	default:
		return "", false
	}
}

func ParseRecipientRoleKey(value string) (RecipientRoleKey, bool) {
	role := RecipientRoleKey(strings.ToLower(strings.TrimSpace(value)))
	_, ok := validRecipientRoles[role]
	return role, ok
}

func StaffRoleLabel(role StaffRoleKey) string {
	switch role {
	case StaffRoleSuperAdministrator:
		return "Super Administrator"
	case StaffRoleClubLiaison:
		return "Club Liaison Officer"
	case StaffRoleJuniorAdministrator:
		return "Junior Administrator"
	default:
		return "GMCL Administrator"
	}
}

func RecipientRoleLabel(role RecipientRoleKey) string {
	switch role {
	case RecipientPrimaryContact:
		return "Primary contact"
	case RecipientSecretary:
		return "Club secretary"
	case RecipientPlayCricketAdmin:
		return "Play-Cricket administrator"
	case RecipientJuniorContact:
		return "Junior contact"
	case RecipientFixturesContact:
		return "Fixtures contact"
	case RecipientRegistration:
		return "Registration contact"
	default:
		return "Verified adult club contact"
	}
}

func RecipientRoleAllowedForCategory(
	category MessageCategory,
	role RecipientRoleKey,
) bool {
	allowed := map[MessageCategory]map[RecipientRoleKey]struct{}{
		MessageCategoryGeneral: {
			RecipientPrimaryContact: {}, RecipientSecretary: {},
		},
		MessageCategoryCompliance: {
			RecipientPrimaryContact: {}, RecipientSecretary: {},
		},
		MessageCategoryFixtures: {
			RecipientFixturesContact: {}, RecipientSecretary: {},
			RecipientPrimaryContact: {},
		},
		MessageCategoryRegistration: {
			RecipientRegistration: {}, RecipientPlayCricketAdmin: {},
			RecipientSecretary: {}, RecipientPrimaryContact: {},
		},
		MessageCategoryStarred: {
			RecipientSecretary: {}, RecipientPlayCricketAdmin: {},
			RecipientPrimaryContact: {},
		},
		MessageCategoryJunior: {
			RecipientJuniorContact: {}, RecipientSecretary: {},
			RecipientPrimaryContact: {},
		},
		MessageCategoryContact: {
			RecipientPrimaryContact: {}, RecipientSecretary: {},
		},
		MessageCategoryPlayerIdentity: {
			RecipientRegistration: {}, RecipientPlayCricketAdmin: {},
			RecipientSecretary: {}, RecipientPrimaryContact: {},
		},
	}
	roles, ok := allowed[category]
	if !ok {
		return false
	}
	_, ok = roles[role]
	return ok
}

func (access StaffAccess) HasPortalStaffAccess() bool {
	return access.SuperAdmin || len(access.Assignments) > 0
}

func (access StaffAccess) CanAccessCase(
	clubID int32,
	category MessageCategory,
	competitionID *uuid.UUID,
) bool {
	if access.SuperAdmin {
		return true
	}
	for _, assignment := range access.Assignments {
		if !assignmentCovers(assignment, clubID, competitionID) {
			continue
		}
		if assignment.Role == StaffRoleClubLiaison ||
			(assignment.Role == StaffRoleJuniorAdministrator &&
				category == MessageCategoryJunior) {
			return true
		}
	}
	return false
}

func (access StaffAccess) RoleForCase(
	clubID int32,
	category MessageCategory,
	competitionID *uuid.UUID,
) (StaffRoleKey, bool) {
	return access.senderRoleFor(category, []int32{clubID}, competitionID)
}

func (access StaffAccess) senderRoleFor(
	category MessageCategory,
	clubIDs []int32,
	competitionID *uuid.UUID,
) (StaffRoleKey, bool) {
	if access.SuperAdmin {
		return StaffRoleSuperAdministrator, true
	}
	candidates := []StaffRoleKey{StaffRoleClubLiaison}
	if category == MessageCategoryJunior {
		candidates = []StaffRoleKey{
			StaffRoleJuniorAdministrator,
			StaffRoleClubLiaison,
		}
	}
	for _, role := range candidates {
		allCovered := true
		for _, clubID := range clubIDs {
			covered := false
			for _, assignment := range access.Assignments {
				if assignment.Role == role &&
					assignmentCovers(assignment, clubID, competitionID) {
					covered = true
					break
				}
			}
			if !covered {
				allCovered = false
				break
			}
		}
		if allCovered {
			return role, true
		}
	}
	return "", false
}

func assignmentCovers(
	assignment StaffAssignment,
	clubID int32,
	competitionID *uuid.UUID,
) bool {
	if assignment.ClubID == nil && assignment.CompetitionID == nil {
		return true
	}
	if assignment.ClubID != nil {
		return *assignment.ClubID == clubID
	}
	return competitionID != nil && assignment.CompetitionID != nil &&
		*assignment.CompetitionID == *competitionID
}

func (store *Store) LoadStaffAccess(
	ctx context.Context,
	adminID int32,
) (StaffAccess, error) {
	if adminID <= 0 {
		return StaffAccess{}, ErrForbidden
	}
	var access StaffAccess
	now := store.now()
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		var legacyRole string
		var active bool
		if err := tx.QueryRow(ctx, `
			SELECT
				id,
				COALESCE(NULLIF(btrim(username), ''), NULLIF(btrim(email), ''), 'Administrator'),
				COALESCE(email, ''),
				COALESCE(role, 'admin'),
				is_active
			FROM admin_users
			WHERE id = $1
		`, adminID).Scan(
			&access.AdminUserID,
			&access.DisplayName,
			&access.Email,
			&legacyRole,
			&active,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return fmt.Errorf("load portal staff account: %w", err)
		}
		if !active {
			return ErrForbidden
		}
		access.SuperAdmin = legacyRole == "super_admin"
		if access.SuperAdmin {
			return nil
		}
		rows, err := tx.Query(ctx, `
			SELECT
				assignment.id,
				assignment.admin_user_id,
				admin.username,
				admin.email,
				assignment.role_key,
				assignment.club_id,
				COALESCE(club.name, ''),
				assignment.competition_id,
				COALESCE(competition.name, ''),
				assignment.status,
				assignment.starts_at,
				assignment.ends_at,
				assignment.grant_reason
			FROM portal_staff_assignments assignment
			JOIN admin_users admin ON admin.id = assignment.admin_user_id
			LEFT JOIN clubs club ON club.id = assignment.club_id
			LEFT JOIN portal_competitions competition
				ON competition.id = assignment.competition_id
			WHERE assignment.admin_user_id = $1
			  AND assignment.status = 'active'
			  AND assignment.starts_at <= $2
			  AND (assignment.ends_at IS NULL OR assignment.ends_at > $2)
			ORDER BY assignment.role_key, club.name, competition.name, assignment.id
		`, adminID, now)
		if err != nil {
			return fmt.Errorf("load portal staff assignments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var assignment StaffAssignment
			if err := rows.Scan(
				&assignment.ID,
				&assignment.AdminUserID,
				&assignment.AdminName,
				&assignment.AdminEmail,
				&assignment.Role,
				&assignment.ClubID,
				&assignment.ClubName,
				&assignment.CompetitionID,
				&assignment.CompetitionName,
				&assignment.Status,
				&assignment.StartsAt,
				&assignment.EndsAt,
				&assignment.GrantReason,
			); err != nil {
				return fmt.Errorf("scan portal staff assignment: %w", err)
			}
			access.Assignments = append(access.Assignments, assignment)
		}
		return rows.Err()
	})
	return access, err
}

func (store *Store) ListStaffAssignments(
	ctx context.Context,
) ([]StaffAssignment, error) {
	var assignments []StaffAssignment
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				assignment.id,
				assignment.admin_user_id,
				admin.username,
				admin.email,
				assignment.role_key,
				assignment.club_id,
				COALESCE(club.name, ''),
				assignment.competition_id,
				COALESCE(competition.name, ''),
				assignment.status,
				assignment.starts_at,
				assignment.ends_at,
				assignment.grant_reason
			FROM portal_staff_assignments assignment
			JOIN admin_users admin ON admin.id = assignment.admin_user_id
			LEFT JOIN clubs club ON club.id = assignment.club_id
			LEFT JOIN portal_competitions competition
				ON competition.id = assignment.competition_id
			ORDER BY
				CASE assignment.status WHEN 'active' THEN 0 ELSE 1 END,
				admin.username,
				assignment.role_key,
				club.name,
				competition.name
		`)
		if err != nil {
			return fmt.Errorf("list portal staff assignments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var assignment StaffAssignment
			if err := rows.Scan(
				&assignment.ID,
				&assignment.AdminUserID,
				&assignment.AdminName,
				&assignment.AdminEmail,
				&assignment.Role,
				&assignment.ClubID,
				&assignment.ClubName,
				&assignment.CompetitionID,
				&assignment.CompetitionName,
				&assignment.Status,
				&assignment.StartsAt,
				&assignment.EndsAt,
				&assignment.GrantReason,
			); err != nil {
				return fmt.Errorf("scan portal staff assignment: %w", err)
			}
			assignments = append(assignments, assignment)
		}
		return rows.Err()
	})
	return assignments, err
}

func (store *Store) CreateStaffAssignment(
	ctx context.Context,
	request StaffAssignmentRequest,
) (uuid.UUID, error) {
	request.GrantReason = strings.TrimSpace(request.GrantReason)
	if request.AdminUserID <= 0 || request.GrantedBy <= 0 ||
		request.GrantReason == "" || len(request.GrantReason) > 500 ||
		(request.ClubID != nil && request.CompetitionID != nil) {
		return uuid.Nil, fmt.Errorf("invalid portal staff assignment")
	}
	if request.Role != StaffRoleClubLiaison &&
		request.Role != StaffRoleJuniorAdministrator {
		return uuid.Nil, fmt.Errorf("invalid portal staff role")
	}
	id := uuid.New()
	now := store.now()
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(ctx, `
			SELECT is_active FROM admin_users WHERE id = $1
		`, request.AdminUserID).Scan(&active); err != nil || !active {
			return fmt.Errorf("staff account is not active")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_staff_assignments (
				id,
				admin_user_id,
				role_key,
				club_id,
				competition_id,
				status,
				starts_at,
				granted_by_admin_user_id,
				grant_reason,
				created_at,
				updated_at
			)
			VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, $8, $6, $6)
		`, id, request.AdminUserID, request.Role, request.ClubID,
			request.CompetitionID, now, request.GrantedBy,
			request.GrantReason); err != nil {
			return fmt.Errorf("create portal staff assignment: %w", err)
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ActorKind:     "legacy_admin",
			LegacyAdminID: &request.GrantedBy,
			Action:        "portal.staff_assignment.created",
			TargetType:    "portal_staff_assignment",
			TargetID:      id.String(),
			Outcome:       "success",
			CorrelationID: uuid.NewString(),
			Metadata: map[string]any{
				"admin_user_id": request.AdminUserID,
				"role":          request.Role,
			},
			OccurredAt: now,
		})
	})
	return id, err
}

func (store *Store) RevokeStaffAssignment(
	ctx context.Context,
	assignmentID uuid.UUID,
	revokedBy int32,
	reason string,
) error {
	reason = strings.TrimSpace(reason)
	if assignmentID == uuid.Nil || revokedBy <= 0 ||
		reason == "" || len(reason) > 500 {
		return fmt.Errorf("invalid staff assignment revocation")
	}
	now := store.now()
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE portal_staff_assignments
			SET status = 'revoked',
			    revoked_at = $2,
			    revoked_by_admin_user_id = $3,
			    revocation_reason = $4,
			    updated_at = $2
			WHERE id = $1 AND status IN ('active', 'suspended')
		`, assignmentID, now, revokedBy, reason)
		if err != nil {
			return fmt.Errorf("revoke portal staff assignment: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ActorKind:     "legacy_admin",
			LegacyAdminID: &revokedBy,
			Action:        "portal.staff_assignment.revoked",
			TargetType:    "portal_staff_assignment",
			TargetID:      assignmentID.String(),
			Outcome:       "success",
			CorrelationID: uuid.NewString(),
			Metadata:      map[string]any{"reason": reason},
			OccurredAt:    now,
		})
	})
}

func (store *Store) ListStaffCompetitions(
	ctx context.Context,
) ([]StaffCompetition, error) {
	var competitions []StaffCompetition
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name
			FROM portal_competitions
			WHERE (starts_at IS NULL OR starts_at <= $1)
			  AND (ends_at IS NULL OR ends_at > $1)
			ORDER BY name, id
		`, store.now())
		if err != nil {
			return fmt.Errorf("list portal competitions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var competition StaffCompetition
			if err := rows.Scan(&competition.ID, &competition.Name); err != nil {
				return fmt.Errorf("scan portal competition: %w", err)
			}
			competitions = append(competitions, competition)
		}
		return rows.Err()
	})
	return competitions, err
}

func (store *Store) CreateStaffCampaign(
	ctx context.Context,
	adminID int32,
	request StaffCampaignRequest,
) (StaffCampaignResult, error) {
	request.Subject = strings.TrimSpace(request.Subject)
	request.Body = strings.TrimSpace(request.Body)
	request.Priority = strings.ToLower(strings.TrimSpace(request.Priority))
	request.CorrelationID = strings.TrimSpace(request.CorrelationID)
	if request.CorrelationID == "" {
		request.CorrelationID = uuid.NewString()
	}
	if _, ok := validMessageCategories[request.Category]; !ok ||
		len(request.Subject) < 3 || len(request.Subject) > 200 ||
		request.Body == "" || len(request.Body) > 10000 ||
		(request.Priority != "normal" && request.Priority != "urgent") {
		return StaffCampaignResult{}, fmt.Errorf("invalid staff campaign")
	}
	if _, ok := validRecipientRoles[request.RecipientRole]; !ok {
		return StaffCampaignResult{}, fmt.Errorf("invalid campaign recipient role")
	}
	if !RecipientRoleAllowedForCategory(
		request.Category,
		request.RecipientRole,
	) {
		return StaffCampaignResult{}, fmt.Errorf(
			"the selected recipient role is not valid for this message category",
		)
	}
	clubIDs := uniquePositiveClubIDs(request.ClubIDs)
	if len(clubIDs) == 0 || len(clubIDs) > 100 {
		return StaffCampaignResult{}, fmt.Errorf("select between 1 and 100 clubs")
	}
	access, err := store.LoadStaffAccess(ctx, adminID)
	if err != nil {
		return StaffCampaignResult{}, err
	}
	senderRole, ok := access.senderRoleFor(
		request.Category,
		clubIDs,
		request.CompetitionID,
	)
	if !ok {
		return StaffCampaignResult{}, ErrForbidden
	}

	result := StaffCampaignResult{
		ID:          uuid.New(),
		TargetCount: len(clubIDs),
	}
	now := store.now()
	err = store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if err := revalidateStaffCampaignAccess(
			ctx,
			tx,
			adminID,
			senderRole,
			request.Category,
			clubIDs,
			request.CompetitionID,
			now,
		); err != nil {
			return err
		}
		type targetRecipients struct {
			clubID     int32
			clubName   string
			recipients []string
		}
		targets := make([]targetRecipients, 0, len(clubIDs))
		for _, clubID := range clubIDs {
			var clubName string
			var enabled bool
			if err := tx.QueryRow(ctx, `
				SELECT
					club.name,
					EXISTS (
						SELECT 1 FROM portal_club_features access
						WHERE access.club_id = club.id
						  AND access.feature_key = 'portal_access'
						  AND access.enabled
					)
					AND EXISTS (
						SELECT 1 FROM portal_club_features messaging
						WHERE messaging.club_id = club.id
						  AND messaging.feature_key = 'secure_messaging'
						  AND messaging.enabled
					)
				FROM clubs club
				WHERE club.id = $1
			`, clubID).Scan(&clubName, &enabled); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("selected club does not exist")
				}
				return fmt.Errorf("load campaign club: %w", err)
			}
			if !enabled {
				return fmt.Errorf("%s does not have secure messaging enabled", clubName)
			}
			recipients, err := resolveCampaignRecipients(
				ctx,
				tx,
				clubID,
				request.RecipientRole,
				now,
			)
			if err != nil {
				return err
			}
			if len(recipients) == 0 {
				return fmt.Errorf("%s has no verified %s recipient",
					clubName, strings.ToLower(RecipientRoleLabel(request.RecipientRole)))
			}
			targets = append(targets, targetRecipients{
				clubID: clubID, clubName: clubName, recipients: recipients,
			})
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_message_campaigns (
				id,
				sender_admin_user_id,
				sender_role_key,
				competition_id,
				category,
				recipient_role_key,
				subject,
				body,
				priority,
				status,
				target_count,
				created_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'sending', $10, $11)
		`, result.ID, adminID, senderRole, request.CompetitionID,
			request.Category, request.RecipientRole, request.Subject,
			request.Body, request.Priority, len(targets), now); err != nil {
			return fmt.Errorf("create portal message campaign: %w", err)
		}

		for _, target := range targets {
			targetID := uuid.New()
			caseID := uuid.New()
			messageID := uuid.New()
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_message_cases (
					id,
					club_id,
					category,
					subject,
					status,
					priority,
					created_by_user_id,
					created_by_admin_user_id,
					campaign_id,
					sender_staff_role_key,
					recipient_role_key,
					assigned_admin_user_id,
					created_at,
					updated_at
				)
				VALUES (
					$1, $2, $3, $4, 'awaiting_club', $5,
					NULL, $6, $7, $8, $9, $6, $10, $10
				)
			`, caseID, target.clubID, request.Category, request.Subject,
				request.Priority, adminID, result.ID, senderRole,
				request.RecipientRole, now); err != nil {
				return fmt.Errorf("create campaign club case: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_club_visible_messages (
					id,
					case_id,
					club_id,
					author_kind,
					author_admin_user_id,
					author_staff_role_key,
					body,
					email_status,
					created_at
				)
				VALUES ($1, $2, $3, 'gmcl_admin', $4, $5, $6, 'pending', $7)
			`, messageID, caseID, target.clubID, adminID, senderRole,
				request.Body, now); err != nil {
				return fmt.Errorf("create campaign club message: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_message_campaign_targets (
					id,
					campaign_id,
					club_id,
					case_id,
					status,
					recipient_count,
					created_at
				)
				VALUES ($1, $2, $3, $4, 'pending', $5, $6)
			`, targetID, result.ID, target.clubID, caseID,
				len(target.recipients), now); err != nil {
				return fmt.Errorf("create campaign target: %w", err)
			}
			for _, recipient := range target.recipients {
				deliveryID := uuid.New()
				if _, err := tx.Exec(ctx, `
					INSERT INTO portal_message_deliveries (
						id,
						campaign_target_id,
						message_id,
						case_id,
						club_id,
						recipient_role_key,
						recipient_email,
						status,
						created_at,
						updated_at
					)
					VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $8)
				`, deliveryID, targetID, messageID, caseID, target.clubID,
					request.RecipientRole, recipient, now); err != nil {
					return fmt.Errorf("create campaign delivery: %w", err)
				}
				result.Deliveries = append(result.Deliveries, CampaignDelivery{
					ID:             deliveryID,
					CampaignID:     result.ID,
					TargetID:       targetID,
					CaseID:         caseID,
					MessageID:      messageID,
					ClubID:         target.clubID,
					ClubName:       target.clubName,
					RecipientEmail: recipient,
					RecipientRole:  request.RecipientRole,
					Subject:        request.Subject,
					Body:           request.Body,
					SenderName:     access.DisplayName,
					SenderRole:     senderRole,
				})
			}
			clubID := target.clubID
			if err := store.appendAuditTx(ctx, tx, AuditEvent{
				ClubID:        &clubID,
				ActorKind:     "legacy_admin",
				LegacyAdminID: &adminID,
				ActingRoleKey: string(senderRole),
				Action:        "portal.campaign.case_created",
				TargetType:    "portal_message_case",
				TargetID:      caseID.String(),
				Outcome:       "success",
				CorrelationID: request.CorrelationID,
				Metadata: map[string]any{
					"campaign_id":    result.ID,
					"category":       request.Category,
					"recipient_role": request.RecipientRole,
				},
				OccurredAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func revalidateStaffCampaignAccess(
	ctx context.Context,
	tx pgx.Tx,
	adminID int32,
	role StaffRoleKey,
	category MessageCategory,
	clubIDs []int32,
	competitionID *uuid.UUID,
	now time.Time,
) error {
	var legacyRole string
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(role, 'admin'), is_active
		FROM admin_users
		WHERE id = $1
	`, adminID).Scan(&legacyRole, &active); err != nil || !active {
		return ErrForbidden
	}
	if role == StaffRoleSuperAdministrator {
		if legacyRole == "super_admin" {
			return nil
		}
		return ErrForbidden
	}
	if role == StaffRoleJuniorAdministrator &&
		category != MessageCategoryJunior {
		return ErrForbidden
	}
	for _, clubID := range clubIDs {
		var authorized bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM portal_staff_assignments assignment
				WHERE assignment.admin_user_id = $1
				  AND assignment.role_key = $2
				  AND assignment.status = 'active'
				  AND assignment.starts_at <= $5
				  AND (assignment.ends_at IS NULL OR assignment.ends_at > $5)
				  AND (
				      (assignment.club_id IS NULL AND assignment.competition_id IS NULL)
				      OR assignment.club_id = $3
				      OR (
				          $4::uuid IS NOT NULL
				          AND assignment.competition_id = $4::uuid
				      )
				  )
			)
		`, adminID, role, clubID, competitionID, now).Scan(&authorized); err != nil {
			return fmt.Errorf("revalidate portal staff scope: %w", err)
		}
		if !authorized {
			return ErrForbidden
		}
	}
	return nil
}

func resolveCampaignRecipients(
	ctx context.Context,
	tx pgx.Tx,
	clubID int32,
	role RecipientRoleKey,
	now time.Time,
) ([]string, error) {
	portalRoles := recipientPortalRoles(role)
	rows, err := tx.Query(ctx, `
		SELECT email
		FROM (
			SELECT lower(btrim(contact.email)) AS email
			FROM portal_club_contacts contact
			WHERE contact.club_id = $1
			  AND contact.role_key = $2
			  AND contact.status = 'verified'
			  AND contact.effective_from <= $3
			  AND (contact.effective_until IS NULL OR contact.effective_until > $3)
			UNION
			SELECT lower(btrim(identity.verified_email)) AS email
			FROM portal_club_memberships membership
			JOIN portal_role_assignments assignment
			  ON assignment.membership_id = membership.id
			 AND assignment.club_id = membership.club_id
			 AND assignment.user_id = membership.user_id
			JOIN portal_identities identity
			  ON identity.user_id = membership.user_id
			WHERE membership.club_id = $1
			  AND membership.status = 'active'
			  AND membership.starts_at <= $3
			  AND (membership.ends_at IS NULL OR membership.ends_at > $3)
			  AND assignment.status = 'active'
			  AND assignment.starts_at <= $3
			  AND (assignment.ends_at IS NULL OR assignment.ends_at > $3)
			  AND assignment.role_key = ANY($4::text[])
			  AND identity.email_verified
			  AND NULLIF(btrim(identity.verified_email), '') IS NOT NULL
		) recipients
		ORDER BY email
	`, clubID, role, now, portalRoles)
	if err != nil {
		return nil, fmt.Errorf("resolve campaign recipients: %w", err)
	}
	defer rows.Close()
	var recipients []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan campaign recipient: %w", err)
		}
		if parsed, err := mail.ParseAddress(value); err == nil {
			recipients = append(recipients, strings.ToLower(parsed.Address))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(recipients) > 0 {
		return recipients, nil
	}

	fallbackRows, err := tx.Query(ctx, `
		SELECT DISTINCT lower(btrim(identity.verified_email))
		FROM portal_club_memberships membership
		JOIN portal_role_assignments assignment
		  ON assignment.membership_id = membership.id
		 AND assignment.club_id = membership.club_id
		 AND assignment.user_id = membership.user_id
		JOIN portal_identities identity ON identity.user_id = membership.user_id
		WHERE membership.club_id = $1
		  AND membership.status = 'active'
		  AND membership.starts_at <= $2
		  AND (membership.ends_at IS NULL OR membership.ends_at > $2)
		  AND assignment.status = 'active'
		  AND assignment.starts_at <= $2
		  AND (assignment.ends_at IS NULL OR assignment.ends_at > $2)
		  AND assignment.role_key IN (
		      'club_primary_admin',
		      'club_admin',
		      'club_secretary'
		  )
		  AND identity.email_verified
		  AND NULLIF(btrim(identity.verified_email), '') IS NOT NULL
		ORDER BY 1
	`, clubID, now)
	if err != nil {
		return nil, fmt.Errorf("resolve campaign fallback recipients: %w", err)
	}
	defer fallbackRows.Close()
	for fallbackRows.Next() {
		var value string
		if err := fallbackRows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan campaign fallback recipient: %w", err)
		}
		if parsed, err := mail.ParseAddress(value); err == nil {
			recipients = append(recipients, strings.ToLower(parsed.Address))
		}
	}
	return recipients, fallbackRows.Err()
}

func recipientPortalRoles(role RecipientRoleKey) []string {
	switch role {
	case RecipientPrimaryContact:
		return []string{"club_primary_admin", "club_admin"}
	case RecipientSecretary:
		return []string{"club_secretary"}
	case RecipientJuniorContact:
		return []string{"club_junior_officer"}
	default:
		return []string{}
	}
}

func uniquePositiveClubIDs(values []int32) []int32 {
	seen := make(map[int32]struct{}, len(values))
	result := make([]int32, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (store *Store) CompleteCampaignDelivery(
	ctx context.Context,
	deliveryID uuid.UUID,
	sent bool,
	deliveryError string,
) error {
	if deliveryID == uuid.Nil {
		return ErrNotFound
	}
	now := store.now()
	status := "failed"
	var sentAt *time.Time
	if sent {
		status = "sent"
		sentAt = &now
		deliveryError = ""
	}
	deliveryError = truncateNotificationError(deliveryError)
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		var (
			targetID   uuid.UUID
			messageID  uuid.UUID
			campaignID uuid.UUID
		)
		if err := tx.QueryRow(ctx, `
			UPDATE portal_message_deliveries delivery
			SET status = $2,
			    attempt_count = delivery.attempt_count + 1,
			    sent_at = $3,
			    last_error = $4,
			    updated_at = $5
			FROM portal_message_campaign_targets target
			WHERE delivery.id = $1
			  AND target.id = delivery.campaign_target_id
			RETURNING delivery.campaign_target_id, delivery.message_id, target.campaign_id
		`, deliveryID, status, sentAt, nullableString(deliveryError), now).Scan(
			&targetID, &messageID, &campaignID,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("complete campaign delivery: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_message_campaign_targets target
			SET sent_count = summary.sent_count,
			    failed_count = summary.failed_count,
			    status = CASE
			        WHEN summary.sent_count = summary.recipient_count THEN 'sent'
			        WHEN summary.failed_count = summary.recipient_count THEN 'failed'
			        WHEN summary.sent_count + summary.failed_count = summary.recipient_count
			            THEN 'partially_failed'
			        ELSE 'pending'
			    END,
			    completed_at = CASE
			        WHEN summary.sent_count + summary.failed_count = summary.recipient_count
			            THEN $2::timestamptz
			        ELSE NULL::timestamptz
			    END
			FROM (
				SELECT
					campaign_target_id,
					COUNT(*) AS recipient_count,
					COUNT(*) FILTER (WHERE status = 'sent') AS sent_count,
					COUNT(*) FILTER (WHERE status = 'failed') AS failed_count
				FROM portal_message_deliveries
				WHERE campaign_target_id = $1
				GROUP BY campaign_target_id
			) summary
			WHERE target.id = summary.campaign_target_id
		`, targetID, now); err != nil {
			return fmt.Errorf("summarize campaign target delivery: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_club_visible_messages message
			SET email_status = summary.email_status,
			    email_sent_at = CASE
			        WHEN summary.email_status = 'sent' THEN $2::timestamptz
			        ELSE NULL::timestamptz
			    END,
			    email_last_error = CASE
			        WHEN summary.email_status = 'failed' THEN 'one or more recipient deliveries failed'
			        ELSE NULL
			    END
			FROM (
				SELECT
					message_id,
					CASE
						WHEN COUNT(*) FILTER (WHERE status = 'failed') > 0 THEN 'failed'
						WHEN COUNT(*) FILTER (WHERE status = 'sent') = COUNT(*) THEN 'sent'
						ELSE 'pending'
					END AS email_status
				FROM portal_message_deliveries
				WHERE message_id = $1
				GROUP BY message_id
			) summary
			WHERE message.id = summary.message_id
		`, messageID, now); err != nil {
			return fmt.Errorf("summarize campaign message delivery: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE portal_message_campaigns campaign
			SET sent_target_count = summary.sent_targets,
			    failed_target_count = summary.failed_targets,
			    status = CASE
			        WHEN summary.sent_targets = summary.target_count THEN 'sent'
			        WHEN summary.wholly_failed_targets = summary.target_count THEN 'failed'
			        WHEN summary.completed_targets = summary.target_count THEN 'partially_failed'
			        ELSE 'sending'
			    END,
			    completed_at = CASE
			        WHEN summary.completed_targets = summary.target_count
			            THEN $2::timestamptz
			        ELSE NULL::timestamptz
			    END
			FROM (
				SELECT
					campaign_id,
					COUNT(*) AS target_count,
					COUNT(*) FILTER (WHERE status = 'sent') AS sent_targets,
					COUNT(*) FILTER (
						WHERE status IN ('failed', 'partially_failed')
					) AS failed_targets,
					COUNT(*) FILTER (WHERE status = 'failed') AS wholly_failed_targets,
					COUNT(*) FILTER (
						WHERE status IN ('sent', 'failed', 'partially_failed')
					) AS completed_targets
				FROM portal_message_campaign_targets
				WHERE campaign_id = $1
				GROUP BY campaign_id
			) summary
			WHERE campaign.id = summary.campaign_id
		`, campaignID, now); err != nil {
			return fmt.Errorf("summarize campaign delivery: %w", err)
		}
		return nil
	})
}

func (store *Store) ListRetryableCampaignDeliveries(
	ctx context.Context,
	messageID uuid.UUID,
) ([]CampaignDelivery, error) {
	if messageID == uuid.Nil {
		return nil, ErrNotFound
	}
	var deliveries []CampaignDelivery
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				delivery.id,
				campaign.id,
				target.id,
				delivery.case_id,
				delivery.message_id,
				delivery.club_id,
				club.name,
				delivery.recipient_email,
				delivery.recipient_role_key,
				campaign.subject,
				message.body,
				admin.username,
				campaign.sender_role_key
			FROM portal_message_deliveries delivery
			JOIN portal_message_campaign_targets target
			  ON target.id = delivery.campaign_target_id
			JOIN portal_message_campaigns campaign ON campaign.id = target.campaign_id
			JOIN portal_club_visible_messages message ON message.id = delivery.message_id
			JOIN clubs club ON club.id = delivery.club_id
			JOIN admin_users admin ON admin.id = campaign.sender_admin_user_id
			WHERE delivery.message_id = $1
			  AND delivery.status IN ('pending', 'failed')
			ORDER BY delivery.recipient_email
		`, messageID)
		if err != nil {
			return fmt.Errorf("list retryable campaign deliveries: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var delivery CampaignDelivery
			if err := rows.Scan(
				&delivery.ID,
				&delivery.CampaignID,
				&delivery.TargetID,
				&delivery.CaseID,
				&delivery.MessageID,
				&delivery.ClubID,
				&delivery.ClubName,
				&delivery.RecipientEmail,
				&delivery.RecipientRole,
				&delivery.Subject,
				&delivery.Body,
				&delivery.SenderName,
				&delivery.SenderRole,
			); err != nil {
				return fmt.Errorf("scan retryable campaign delivery: %w", err)
			}
			deliveries = append(deliveries, delivery)
		}
		return rows.Err()
	})
	return deliveries, err
}

func (store *Store) ListStaffCampaigns(
	ctx context.Context,
	access StaffAccess,
) ([]StaffCampaignSummary, error) {
	if !access.HasPortalStaffAccess() {
		return nil, ErrForbidden
	}
	var campaigns []StaffCampaignSummary
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				campaign.id,
				campaign.subject,
				campaign.category,
				campaign.recipient_role_key,
				admin.username,
				campaign.sender_role_key,
				campaign.status,
				campaign.target_count,
				campaign.sent_target_count,
				campaign.failed_target_count,
				COUNT(target.id) FILTER (WHERE message_case.acknowledged_at IS NOT NULL),
				COUNT(target.id) FILTER (
					WHERE EXISTS (
						SELECT 1
						FROM portal_club_visible_messages reply
						WHERE reply.case_id = target.case_id
						  AND reply.author_kind = 'club_user'
					)
				),
				campaign.created_at
			FROM portal_message_campaigns campaign
			JOIN admin_users admin ON admin.id = campaign.sender_admin_user_id
			JOIN portal_message_campaign_targets target
			  ON target.campaign_id = campaign.id
			JOIN portal_message_cases message_case ON message_case.id = target.case_id
			WHERE $1::boolean OR campaign.sender_admin_user_id = $2
			GROUP BY campaign.id, admin.username
			ORDER BY campaign.created_at DESC
			LIMIT 100
		`, access.SuperAdmin, access.AdminUserID)
		if err != nil {
			return fmt.Errorf("list portal staff campaigns: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var campaign StaffCampaignSummary
			if err := rows.Scan(
				&campaign.ID,
				&campaign.Subject,
				&campaign.Category,
				&campaign.RecipientRole,
				&campaign.SenderName,
				&campaign.SenderRole,
				&campaign.Status,
				&campaign.TargetCount,
				&campaign.SentTargetCount,
				&campaign.FailedTargetCount,
				&campaign.AcknowledgedCount,
				&campaign.ClubReplyCount,
				&campaign.CreatedAt,
			); err != nil {
				return fmt.Errorf("scan portal staff campaign: %w", err)
			}
			campaigns = append(campaigns, campaign)
		}
		return rows.Err()
	})
	return campaigns, err
}

func (store *Store) ListCaseOfficialRecipients(
	ctx context.Context,
	caseID uuid.UUID,
) ([]string, error) {
	if caseID == uuid.Nil {
		return nil, ErrNotFound
	}
	var recipients []string
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT recipient
			FROM (
				SELECT DISTINCT lower(btrim(delivery.recipient_email)) AS recipient
				FROM portal_message_deliveries delivery
				WHERE delivery.case_id = $1
				UNION
				SELECT lower(btrim(identity.verified_email)) AS recipient
				FROM portal_message_cases message_case
				JOIN portal_identities identity
				  ON identity.user_id = message_case.created_by_user_id
				WHERE message_case.id = $1
				  AND identity.email_verified
				  AND NULLIF(btrim(identity.verified_email), '') IS NOT NULL
			) resolved
			WHERE NULLIF(recipient, '') IS NOT NULL
			ORDER BY recipient
		`, caseID)
		if err != nil {
			return fmt.Errorf("list case official recipients: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var recipient string
			if err := rows.Scan(&recipient); err != nil {
				return fmt.Errorf("scan case official recipient: %w", err)
			}
			recipients = append(recipients, recipient)
		}
		return rows.Err()
	})
	return recipients, err
}
