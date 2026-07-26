package portal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SeasonOption struct {
	ID        int32
	Name      string
	StartDate time.Time
	EndDate   time.Time
}

type TeamOption struct {
	ID   int32
	Name string
}

type ReadScopeSelection struct {
	Principal           Principal
	Seasons             []SeasonOption
	Teams               []TeamOption
	SelectedSeasonID    int32
	SelectedTeamID      *int32
	TeamSelectionLocked bool
}

// ResolveReadScope narrows an authenticated appointment to a selected season
// and optional team. It never broadens an appointment and does not disclose
// whether an unavailable identifier exists outside the acting club.
func (store *Store) ResolveReadScope(
	ctx context.Context,
	principal Principal,
	requestedSeasonID *int32,
	requestedTeamID *int32,
) (ReadScopeSelection, error) {
	if principal.Assignment == nil {
		return ReadScopeSelection{}, ErrForbidden
	}
	original := *principal.Assignment
	now := store.now()
	if !Authorize(original, PermissionPortalView, original.Scope, now) {
		return ReadScopeSelection{}, ErrForbidden
	}

	selection := ReadScopeSelection{Principal: principal}
	selection.TeamSelectionLocked = original.Scope.TeamID != nil
	err := store.WithTenantTx(ctx, principal, func(tx pgx.Tx, assignment Assignment) error {
		seasonRows, err := tx.Query(ctx, `
			SELECT id, name, start_date, end_date
			FROM seasons
			WHERE ($1::integer IS NULL OR id = $1)
			ORDER BY start_date DESC, id DESC
		`, assignment.Scope.SeasonID)
		if err != nil {
			return fmt.Errorf("list portal season filters: %w", err)
		}
		defer seasonRows.Close()
		for seasonRows.Next() {
			var option SeasonOption
			if err := seasonRows.Scan(
				&option.ID,
				&option.Name,
				&option.StartDate,
				&option.EndDate,
			); err != nil {
				return fmt.Errorf("scan portal season filter: %w", err)
			}
			selection.Seasons = append(selection.Seasons, option)
		}
		if err := seasonRows.Err(); err != nil {
			return fmt.Errorf("iterate portal season filters: %w", err)
		}

		teamRows, err := tx.Query(ctx, `
			SELECT id, name
			FROM teams
			WHERE club_id = $1
			  AND ($2::integer IS NULL OR id = $2)
			ORDER BY name, id
		`, assignment.Scope.ClubID, assignment.Scope.TeamID)
		if err != nil {
			return fmt.Errorf("list portal team filters: %w", err)
		}
		defer teamRows.Close()
		for teamRows.Next() {
			var option TeamOption
			if err := teamRows.Scan(&option.ID, &option.Name); err != nil {
				return fmt.Errorf("scan portal team filter: %w", err)
			}
			selection.Teams = append(selection.Teams, option)
		}
		return teamRows.Err()
	})
	if err != nil {
		return ReadScopeSelection{}, err
	}
	if len(selection.Seasons) == 0 {
		return ReadScopeSelection{}, fmt.Errorf("no season is available for the selected appointment")
	}

	selection.SelectedSeasonID = chooseSeason(
		selection.Seasons,
		original.Scope.SeasonID,
		requestedSeasonID,
		now,
	)
	if selection.SelectedSeasonID == 0 {
		return ReadScopeSelection{}, store.recordAndDenyScope(
			ctx,
			principal,
			requestedSeasonID,
			requestedTeamID,
		)
	}

	if original.Scope.TeamID != nil {
		if requestedTeamID != nil && *requestedTeamID != *original.Scope.TeamID {
			return ReadScopeSelection{}, store.recordAndDenyScope(
				ctx,
				principal,
				requestedSeasonID,
				requestedTeamID,
			)
		}
		value := *original.Scope.TeamID
		selection.SelectedTeamID = &value
	} else if requestedTeamID != nil {
		if !containsTeam(selection.Teams, *requestedTeamID) {
			return ReadScopeSelection{}, store.recordAndDenyScope(
				ctx,
				principal,
				requestedSeasonID,
				requestedTeamID,
			)
		}
		value := *requestedTeamID
		selection.SelectedTeamID = &value
	}

	narrowed := original
	seasonID := selection.SelectedSeasonID
	narrowed.Scope.SeasonID = &seasonID
	narrowed.Scope.TeamID = selection.SelectedTeamID
	selection.Principal.Assignment = &narrowed
	return selection, nil
}

func (store *Store) recordAndDenyScope(
	ctx context.Context,
	principal Principal,
	requestedSeasonID *int32,
	requestedTeamID *int32,
) error {
	if err := store.recordScopeDenied(
		ctx,
		principal,
		requestedSeasonID,
		requestedTeamID,
	); err != nil {
		return fmt.Errorf("record denied portal scope: %w", err)
	}
	return ErrNotFound
}

func chooseSeason(
	options []SeasonOption,
	assignedID *int32,
	requestedID *int32,
	now time.Time,
) int32 {
	if assignedID != nil {
		if requestedID != nil && *requestedID != *assignedID {
			return 0
		}
		if containsSeason(options, *assignedID) {
			return *assignedID
		}
		return 0
	}
	if requestedID != nil {
		if containsSeason(options, *requestedID) {
			return *requestedID
		}
		return 0
	}
	location, err := time.LoadLocation("Europe/London")
	if err != nil {
		location = time.UTC
	}
	currentDate := dateKey(now.In(location))
	for _, option := range options {
		if currentDate >= dateKey(option.StartDate) && currentDate <= dateKey(option.EndDate) {
			return option.ID
		}
	}
	return options[0].ID
}

func dateKey(value time.Time) int {
	year, month, day := value.Date()
	return year*10000 + int(month)*100 + day
}

func containsSeason(options []SeasonOption, id int32) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func containsTeam(options []TeamOption, id int32) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func (store *Store) recordScopeDenied(
	ctx context.Context,
	principal Principal,
	requestedSeasonID *int32,
	requestedTeamID *int32,
) error {
	if principal.Assignment == nil {
		return ErrForbidden
	}
	now := store.now()
	clubID := principal.Assignment.Scope.ClubID
	return store.withSystemTx(ctx, func(tx pgx.Tx) error {
		return store.appendAuditTx(ctx, tx, AuditEvent{
			ClubID:        &clubID,
			ActorUserID:   &principal.UserID,
			ActorKind:     "portal_user",
			ActingRoleKey: string(principal.Assignment.Role),
			Action:        "portal.scope.denied",
			TargetType:    "portal_read_scope",
			Outcome:       "denied",
			CorrelationID: uuid.NewString(),
			Metadata: map[string]any{
				"requested_season_id": requestedSeasonID,
				"requested_team_id":   requestedTeamID,
			},
			OccurredAt: now,
		})
	})
}

func IsUnavailableScope(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden)
}
