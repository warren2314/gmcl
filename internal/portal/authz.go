package portal

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// RoleKey is an application-owned appointment. Identity-provider groups and
// claims must never be converted directly into one of these roles.
type RoleKey string

const (
	RoleClubPrimaryAdmin  RoleKey = "club_primary_admin"
	RoleClubAdmin         RoleKey = "club_admin"
	RoleClubSecretary     RoleKey = "club_secretary"
	RoleCaptainManager    RoleKey = "captain_manager"
	RoleReadOnlyClubUser  RoleKey = "read_only_club_user"
	RoleClubJuniorOfficer RoleKey = "club_junior_officer"
	RoleClubSafeguarding  RoleKey = "club_safeguarding_officer"
)

// Permission names server-side operations. Unknown permissions are denied.
type Permission string

const (
	PermissionPortalView         Permission = "portal.view"
	PermissionReportsView        Permission = "reports.view"
	PermissionSanctionsView      Permission = "sanctions.view"
	PermissionStarredPlayersView Permission = "starred_players.view"
	PermissionClubProfileView    Permission = "club_profile.view"
	PermissionClubProfileManage  Permission = "club_profile.manage"
	PermissionMessagesView       Permission = "messages.view"
	PermissionMessagesReply      Permission = "messages.reply"
	PermissionMembershipsView    Permission = "memberships.view"
	PermissionMembershipsManage  Permission = "memberships.manage"
	PermissionJuniorAdminView    Permission = "junior_admin.view"
	PermissionJuniorAdminManage  Permission = "junior_admin.manage"
	PermissionSafeguardingView   Permission = "safeguarding.view"
	PermissionSafeguardingManage Permission = "safeguarding.manage"
)

var rolePermissions = map[RoleKey]map[Permission]struct{}{
	RoleClubPrimaryAdmin: permissionSet(
		PermissionPortalView,
		PermissionReportsView,
		PermissionSanctionsView,
		PermissionStarredPlayersView,
		PermissionClubProfileView,
		PermissionClubProfileManage,
		PermissionMessagesView,
		PermissionMessagesReply,
		PermissionMembershipsView,
		PermissionMembershipsManage,
	),
	RoleClubAdmin: permissionSet(
		PermissionPortalView,
		PermissionReportsView,
		PermissionSanctionsView,
		PermissionStarredPlayersView,
		PermissionClubProfileView,
		PermissionClubProfileManage,
		PermissionMessagesView,
		PermissionMessagesReply,
		PermissionMembershipsView,
	),
	RoleClubSecretary: permissionSet(
		PermissionPortalView,
		PermissionReportsView,
		PermissionSanctionsView,
		PermissionStarredPlayersView,
		PermissionClubProfileView,
		PermissionMessagesView,
		PermissionMessagesReply,
	),
	RoleCaptainManager: permissionSet(
		PermissionPortalView,
		PermissionReportsView,
		PermissionSanctionsView,
		PermissionClubProfileView,
	),
	RoleReadOnlyClubUser: permissionSet(
		PermissionPortalView,
		PermissionReportsView,
		PermissionSanctionsView,
		PermissionStarredPlayersView,
		PermissionClubProfileView,
		PermissionMessagesView,
	),
	RoleClubJuniorOfficer: permissionSet(
		PermissionPortalView,
		PermissionClubProfileView,
		PermissionMessagesView,
		PermissionMessagesReply,
		PermissionJuniorAdminView,
		PermissionJuniorAdminManage,
	),
	RoleClubSafeguarding: permissionSet(
		PermissionPortalView,
		PermissionSafeguardingView,
		PermissionSafeguardingManage,
	),
}

func permissionSet(permissions ...Permission) map[Permission]struct{} {
	result := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		result[permission] = struct{}{}
	}
	return result
}

// Scope identifies the tenant and optional appointment boundaries of a
// requested record. A zero optional identifier means the resource is not
// narrower than club scope.
type Scope struct {
	ClubID        int32
	TeamID        *int32
	SeasonID      *int32
	CompetitionID *uuid.UUID
}

// Assignment is the immutable authorization snapshot loaded for one request.
// The database remains authoritative and is consulted on every request.
type Assignment struct {
	ID           uuid.UUID
	MembershipID uuid.UUID
	UserID       uuid.UUID
	Role         RoleKey
	Scope        Scope
	Status       string
	StartsAt     time.Time
	EndsAt       *time.Time
	Version      int64
}

// Authorize is deny-by-default. The assignment must be active, effective,
// grant the named permission and contain the complete resource scope.
func Authorize(assignment Assignment, permission Permission, resource Scope, now time.Time) bool {
	if assignment.Status != "active" || assignment.StartsAt.After(now) {
		return false
	}
	if assignment.EndsAt != nil && !assignment.EndsAt.After(now) {
		return false
	}

	grants, knownRole := rolePermissions[assignment.Role]
	if !knownRole {
		return false
	}
	if _, granted := grants[permission]; !granted {
		return false
	}

	if assignment.Scope.ClubID <= 0 || resource.ClubID <= 0 ||
		assignment.Scope.ClubID != resource.ClubID {
		return false
	}
	if !containsInt32(assignment.Scope.TeamID, resource.TeamID) {
		return false
	}
	if !containsInt32(assignment.Scope.SeasonID, resource.SeasonID) {
		return false
	}
	if !containsUUID(assignment.Scope.CompetitionID, resource.CompetitionID) {
		return false
	}
	return true
}

func containsInt32(assignment, resource *int32) bool {
	if assignment == nil {
		return true
	}
	return resource != nil && *assignment == *resource
}

func containsUUID(assignment, resource *uuid.UUID) bool {
	if assignment == nil {
		return true
	}
	return resource != nil && *assignment == *resource
}

func validRole(role RoleKey) bool {
	_, ok := rolePermissions[role]
	return ok
}

func ParseRoleKey(value string) (RoleKey, bool) {
	role := RoleKey(strings.ToLower(strings.TrimSpace(value)))
	return role, validRole(role)
}
