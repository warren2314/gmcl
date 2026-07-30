package portal

import (
	"context"
	"fmt"
	"strings"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type StaffCompetitionRequest struct {
	Name      string
	SeasonID  int32
	ClubIDs   []int32
	CreatedBy int32
}

func (competition StaffCompetition) ContainsClub(clubID int32) bool {
	for _, mappedClubID := range competition.ClubIDs {
		if mappedClubID == clubID {
			return true
		}
	}
	return false
}

func (store *Store) ListStaffCompetitionSeasons(
	ctx context.Context,
) ([]SeasonOption, error) {
	var seasons []SeasonOption
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, start_date, end_date
			FROM seasons
			WHERE NOT is_archived
			ORDER BY start_date DESC, id DESC
		`)
		if err != nil {
			return fmt.Errorf("list competition context seasons: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var season SeasonOption
			if err := rows.Scan(
				&season.ID,
				&season.Name,
				&season.StartDate,
				&season.EndDate,
			); err != nil {
				return fmt.Errorf("scan competition context season: %w", err)
			}
			seasons = append(seasons, season)
		}
		return rows.Err()
	})
	return seasons, err
}

func (store *Store) ListAllStaffCompetitions(
	ctx context.Context,
) ([]StaffCompetition, error) {
	return store.listStaffCompetitions(ctx, false)
}

func (store *Store) listStaffCompetitions(
	ctx context.Context,
	activeOnly bool,
) ([]StaffCompetition, error) {
	var competitions []StaffCompetition
	err := store.withSystemReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
				competition.id,
				competition.name,
				competition.season_id,
				COALESCE(season.name, ''),
				COALESCE(competition.external_source, ''),
				COALESCE(competition.external_id, ''),
				competition.starts_at,
				competition.ends_at,
				competition.ended_by_admin_user_id,
				COALESCE(competition.end_reason, ''),
				COALESCE(
					array_agg(mapping.club_id ORDER BY club.name, mapping.club_id)
						FILTER (WHERE mapping.club_id IS NOT NULL),
					'{}'::integer[]
				),
				COALESCE(
					array_agg(club.name ORDER BY club.name, mapping.club_id)
						FILTER (WHERE mapping.club_id IS NOT NULL),
					'{}'::text[]
				),
				(
					competition.ended_by_admin_user_id IS NULL
					AND (
						competition.starts_at IS NULL
						OR competition.starts_at <= clock_timestamp()
					)
					AND (
						competition.ends_at IS NULL
						OR competition.ends_at > clock_timestamp()
					)
					AND COUNT(mapping.club_id) > 0
				),
				(
					competition.ended_by_admin_user_id IS NULL
					AND (
						competition.ends_at IS NULL
						OR competition.ends_at > clock_timestamp()
					)
				),
				(
					competition.ended_by_admin_user_id IS NULL
					AND (
						competition.starts_at IS NULL
						OR competition.starts_at <= clock_timestamp()
					)
					AND (
						competition.ends_at IS NULL
						OR competition.ends_at > clock_timestamp()
					)
				)
			FROM portal_competitions competition
			LEFT JOIN seasons season ON season.id = competition.season_id
			LEFT JOIN portal_competition_clubs mapping
				ON mapping.competition_id = competition.id
			LEFT JOIN clubs club ON club.id = mapping.club_id
			WHERE NOT $1::boolean
			   OR (
					competition.ended_by_admin_user_id IS NULL
					AND (
						competition.starts_at IS NULL
						OR competition.starts_at <= clock_timestamp()
					)
					AND (
						competition.ends_at IS NULL
						OR competition.ends_at > clock_timestamp()
					)
				)
			GROUP BY competition.id, season.name, season.start_date
			HAVING NOT $1::boolean OR COUNT(mapping.club_id) > 0
			ORDER BY
				CASE
					WHEN competition.ended_by_admin_user_id IS NULL
					 AND (
						competition.starts_at IS NULL
						OR competition.starts_at <= clock_timestamp()
					 )
					 AND (
						competition.ends_at IS NULL
						OR competition.ends_at > clock_timestamp()
					 )
					 AND COUNT(mapping.club_id) > 0
					THEN 0
					ELSE 1
				END,
				season.start_date DESC NULLS LAST,
				lower(competition.name),
				competition.id
		`, activeOnly)
		if err != nil {
			return fmt.Errorf("list portal competition contexts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var competition StaffCompetition
			if err := rows.Scan(
				&competition.ID,
				&competition.Name,
				&competition.SeasonID,
				&competition.SeasonName,
				&competition.ExternalSource,
				&competition.ExternalID,
				&competition.StartsAt,
				&competition.EndsAt,
				&competition.EndedByAdminID,
				&competition.EndReason,
				&competition.ClubIDs,
				&competition.ClubNames,
				&competition.Active,
				&competition.Manageable,
				&competition.Endable,
			); err != nil {
				return fmt.Errorf("scan portal competition context: %w", err)
			}
			competitions = append(competitions, competition)
		}
		return rows.Err()
	})
	return competitions, err
}

func (store *Store) CreateStaffCompetition(
	ctx context.Context,
	request StaffCompetitionRequest,
) (uuid.UUID, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.ClubIDs = uniquePositiveClubIDs(request.ClubIDs)
	if request.CreatedBy <= 0 || request.SeasonID <= 0 ||
		request.Name == "" || len(request.Name) > 200 ||
		len(request.ClubIDs) == 0 || len(request.ClubIDs) > 500 {
		return uuid.Nil, fmt.Errorf("invalid competition context")
	}

	id := uuid.New()
	now := store.now()
	err := store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if err := requireActiveSuperAdminTx(ctx, tx, request.CreatedBy); err != nil {
			return err
		}
		var seasonExists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM seasons
				WHERE id = $1 AND NOT is_archived
			)
		`, request.SeasonID).Scan(&seasonExists); err != nil {
			return fmt.Errorf("check competition context season: %w", err)
		}
		if !seasonExists {
			return fmt.Errorf("the selected season is not available")
		}
		var clubCount int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM clubs WHERE id = ANY($1::integer[])
		`, request.ClubIDs).Scan(&clubCount); err != nil {
			return fmt.Errorf("check competition context clubs: %w", err)
		}
		if clubCount != len(request.ClubIDs) {
			return fmt.Errorf("one or more selected clubs do not exist")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_competitions (
				id,
				season_id,
				name,
				starts_at,
				created_at
			)
			VALUES (
				$1,
				$2,
				$3,
				clock_timestamp(),
				clock_timestamp()
			)
		`, id, request.SeasonID, request.Name); err != nil {
			return fmt.Errorf("create competition context: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO portal_competition_clubs (
				competition_id,
				club_id,
				created_by_admin_user_id,
				created_at
			)
			SELECT $1, club_id, $2, clock_timestamp()
			FROM unnest($3::integer[]) AS selected(club_id)
		`, id, request.CreatedBy, request.ClubIDs); err != nil {
			return fmt.Errorf("map competition context clubs: %w", err)
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ActorKind:     "legacy_admin",
			LegacyAdminID: &request.CreatedBy,
			Action:        "portal.competition_context.created",
			TargetType:    "portal_competition",
			TargetID:      id.String(),
			Outcome:       "success",
			CorrelationID: uuid.NewString(),
			Metadata: map[string]any{
				"name":       request.Name,
				"season_id":  request.SeasonID,
				"club_count": len(request.ClubIDs),
			},
			OccurredAt: now,
		})
	})
	return id, err
}

func (store *Store) UpdateStaffCompetitionClubs(
	ctx context.Context,
	competitionID uuid.UUID,
	clubIDs []int32,
	updatedBy int32,
) error {
	clubIDs = uniquePositiveClubIDs(clubIDs)
	if competitionID == uuid.Nil || updatedBy <= 0 ||
		len(clubIDs) == 0 || len(clubIDs) > 500 {
		return fmt.Errorf("invalid competition club mapping")
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if err := requireActiveSuperAdminTx(ctx, tx, updatedBy); err != nil {
			return err
		}
		if err := lockManageableCompetitionContextTx(
			ctx,
			tx,
			competitionID,
		); err != nil {
			return err
		}
		now := store.now()
		var clubCount int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM clubs WHERE id = ANY($1::integer[])
		`, clubIDs).Scan(&clubCount); err != nil {
			return fmt.Errorf("check competition clubs: %w", err)
		}
		if clubCount != len(clubIDs) {
			return fmt.Errorf("one or more selected clubs do not exist")
		}
		var previousClubIDs []int32
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(
				array_agg(club_id ORDER BY club_id),
				'{}'::integer[]
			)
			FROM portal_competition_clubs
			WHERE competition_id = $1
		`, competitionID).Scan(&previousClubIDs); err != nil {
			return fmt.Errorf("list existing competition clubs: %w", err)
		}
		addedClubIDs := int32SetDifference(clubIDs, previousClubIDs)
		removedClubIDs := int32SetDifference(previousClubIDs, clubIDs)
		if len(removedClubIDs) > 0 {
			if _, err := tx.Exec(ctx, `
				DELETE FROM portal_competition_clubs
				WHERE competition_id = $1
				  AND club_id = ANY($2::integer[])
			`, competitionID, removedClubIDs); err != nil {
				return fmt.Errorf("remove competition clubs: %w", err)
			}
		}
		if len(addedClubIDs) > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO portal_competition_clubs (
					competition_id,
					club_id,
					created_by_admin_user_id,
					created_at
				)
				SELECT $1, club_id, $2, clock_timestamp()
				FROM unnest($3::integer[]) AS selected(club_id)
			`, competitionID, updatedBy, addedClubIDs); err != nil {
				return fmt.Errorf("add competition clubs: %w", err)
			}
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ActorKind:     "legacy_admin",
			LegacyAdminID: &updatedBy,
			Action:        "portal.competition_context.clubs_updated",
			TargetType:    "portal_competition",
			TargetID:      competitionID.String(),
			Outcome:       "success",
			CorrelationID: uuid.NewString(),
			Metadata: map[string]any{
				"previous_club_ids":   previousClubIDs,
				"club_ids":            clubIDs,
				"added_club_ids":      addedClubIDs,
				"removed_club_ids":    removedClubIDs,
				"previous_club_count": len(previousClubIDs),
				"club_count":          len(clubIDs),
			},
			OccurredAt: now,
		})
	})
}

func (store *Store) EndStaffCompetition(
	ctx context.Context,
	competitionID uuid.UUID,
	endedBy int32,
	reason string,
) error {
	reason = strings.TrimSpace(reason)
	if competitionID == uuid.Nil || endedBy <= 0 ||
		reason == "" || len(reason) > 500 {
		return fmt.Errorf("invalid competition context end request")
	}
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		if err := requireActiveSuperAdminTx(ctx, tx, endedBy); err != nil {
			return err
		}
		if err := lockEndableCompetitionContextTx(
			ctx,
			tx,
			competitionID,
		); err != nil {
			return err
		}
		now := store.now()
		tag, err := tx.Exec(ctx, `
			UPDATE portal_competitions
			SET ends_at = clock_timestamp(),
			    ended_by_admin_user_id = $2,
			    end_reason = $3
			WHERE id = $1
		`, competitionID, endedBy, reason)
		if err != nil {
			return fmt.Errorf("end competition context: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ActorKind:     "legacy_admin",
			LegacyAdminID: &endedBy,
			Action:        "portal.competition_context.ended",
			TargetType:    "portal_competition",
			TargetID:      competitionID.String(),
			Outcome:       "success",
			CorrelationID: uuid.NewString(),
			Metadata:      map[string]any{"reason": reason},
			OccurredAt:    now,
		})
	})
}

func requireActiveSuperAdminTx(
	ctx context.Context,
	tx pgx.Tx,
	adminID int32,
) error {
	if err := lockAdminUserSharedTx(ctx, tx, adminID); err != nil {
		return err
	}
	var (
		role     string
		username string
		email    string
		active   bool
	)
	err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(role, 'admin'),
			COALESCE(username, ''),
			COALESCE(email, ''),
			is_active
		FROM admin_users
		WHERE id = $1
	`, adminID).Scan(&role, &username, &email, &active)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrForbidden
		}
		return fmt.Errorf("check competition administrator: %w", err)
	}
	if !active ||
		(role != "super_admin" &&
			!auth.IsConfiguredSuperAdmin(username, email)) {
		return ErrForbidden
	}
	return nil
}

func lockAdminUserSharedTx(
	ctx context.Context,
	tx pgx.Tx,
	adminID int32,
) error {
	if adminID <= 0 {
		return ErrForbidden
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock_shared($1, $2)
	`, db.AdminUserAdvisoryLockNamespace, adminID); err != nil {
		return fmt.Errorf("lock administrator security state: %w", err)
	}
	return nil
}

func validateActiveCompetitionContextTx(
	ctx context.Context,
	tx pgx.Tx,
	competitionID uuid.UUID,
	clubIDs []int32,
) error {
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT
			ended_by_admin_user_id IS NULL
			AND (starts_at IS NULL OR starts_at <= clock_timestamp())
			AND (ends_at IS NULL OR ends_at > clock_timestamp())
		FROM portal_competitions
		WHERE id = $1
		FOR SHARE
	`, competitionID).Scan(&active)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("the selected competition context is not active")
		}
		return fmt.Errorf("validate competition context: %w", err)
	}
	if !active {
		return fmt.Errorf("the selected competition context is not active")
	}
	clubIDs = uniquePositiveClubIDs(clubIDs)
	if len(clubIDs) == 0 {
		var mapped bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM portal_competition_clubs
				WHERE competition_id = $1
			)
		`, competitionID).Scan(&mapped); err != nil {
			return fmt.Errorf("validate competition club mappings: %w", err)
		}
		if !mapped {
			return fmt.Errorf("the selected competition context has no mapped clubs")
		}
		return nil
	}
	var mappedCount int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM portal_competition_clubs
		WHERE competition_id = $1
		  AND club_id = ANY($2::integer[])
	`, competitionID, clubIDs).Scan(&mappedCount); err != nil {
		return fmt.Errorf("validate competition clubs: %w", err)
	}
	if mappedCount != len(clubIDs) {
		return fmt.Errorf("every selected club must belong to the competition context")
	}
	return nil
}

func lockManageableCompetitionContextTx(
	ctx context.Context,
	tx pgx.Tx,
	competitionID uuid.UUID,
) error {
	var manageable bool
	err := tx.QueryRow(ctx, `
		SELECT
			ended_by_admin_user_id IS NULL
			AND (ends_at IS NULL OR ends_at > clock_timestamp())
		FROM portal_competitions
		WHERE id = $1
		FOR UPDATE
	`, competitionID).Scan(&manageable)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return fmt.Errorf("lock competition context: %w", err)
	}
	if !manageable {
		return fmt.Errorf("the selected competition context has ended")
	}
	return nil
}

func lockEndableCompetitionContextTx(
	ctx context.Context,
	tx pgx.Tx,
	competitionID uuid.UUID,
) error {
	var endable bool
	err := tx.QueryRow(ctx, `
		SELECT
			ended_by_admin_user_id IS NULL
			AND (starts_at IS NULL OR starts_at <= clock_timestamp())
			AND (ends_at IS NULL OR ends_at > clock_timestamp())
		FROM portal_competitions
		WHERE id = $1
		FOR UPDATE
	`, competitionID).Scan(&endable)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return fmt.Errorf("lock competition context for ending: %w", err)
	}
	if !endable {
		return fmt.Errorf("the selected competition context cannot be ended")
	}
	return nil
}

func int32SetDifference(left []int32, right []int32) []int32 {
	rightSet := make(map[int32]struct{}, len(right))
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	difference := make([]int32, 0, len(left))
	for _, value := range left {
		if _, found := rightSet[value]; !found {
			difference = append(difference, value)
		}
	}
	return difference
}
