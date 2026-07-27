package portal

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAuthorizeDenyByDefault(t *testing.T) {
	now := time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)
	base := Assignment{
		ID:           uuid.New(),
		MembershipID: uuid.New(),
		UserID:       uuid.New(),
		Role:         RoleReadOnlyClubUser,
		Scope:        Scope{ClubID: 10},
		Status:       "active",
		StartsAt:     now.Add(-time.Hour),
		Version:      1,
	}

	tests := []struct {
		name       string
		assignment Assignment
		permission Permission
		resource   Scope
		want       bool
	}{
		{
			name:       "known read permission in own club",
			assignment: base,
			permission: PermissionReportsView,
			resource:   Scope{ClubID: 10},
			want:       true,
		},
		{
			name:       "unknown permission",
			assignment: base,
			permission: Permission("reports.guessed"),
			resource:   Scope{ClubID: 10},
		},
		{
			name:       "foreign club",
			assignment: base,
			permission: PermissionReportsView,
			resource:   Scope{ClubID: 11},
		},
		{
			name: "unknown role",
			assignment: func() Assignment {
				a := base
				a.Role = RoleKey("identity_provider_admin")
				return a
			}(),
			permission: PermissionReportsView,
			resource:   Scope{ClubID: 10},
		},
		{
			name: "pending appointment",
			assignment: func() Assignment {
				a := base
				a.Status = "pending"
				return a
			}(),
			permission: PermissionReportsView,
			resource:   Scope{ClubID: 10},
		},
		{
			name: "future appointment",
			assignment: func() Assignment {
				a := base
				a.StartsAt = now.Add(time.Minute)
				return a
			}(),
			permission: PermissionReportsView,
			resource:   Scope{ClubID: 10},
		},
		{
			name: "expired appointment",
			assignment: func() Assignment {
				a := base
				end := now
				a.EndsAt = &end
				return a
			}(),
			permission: PermissionReportsView,
			resource:   Scope{ClubID: 10},
		},
		{
			name: "ordinary primary administrator cannot enter safeguarding",
			assignment: func() Assignment {
				a := base
				a.Role = RoleClubPrimaryAdmin
				return a
			}(),
			permission: PermissionSafeguardingView,
			resource:   Scope{ClubID: 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Authorize(tt.assignment, tt.permission, tt.resource, now); got != tt.want {
				t.Fatalf("Authorize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthorizeScopedAppointment(t *testing.T) {
	now := time.Now().UTC()
	teamID := int32(4)
	otherTeamID := int32(5)
	seasonID := int32(2026)
	otherSeasonID := int32(2025)
	competitionID := uuid.New()
	otherCompetitionID := uuid.New()

	assignment := Assignment{
		ID:           uuid.New(),
		MembershipID: uuid.New(),
		UserID:       uuid.New(),
		Role:         RoleCaptainManager,
		Scope: Scope{
			ClubID:        8,
			TeamID:        &teamID,
			SeasonID:      &seasonID,
			CompetitionID: &competitionID,
		},
		Status:   "active",
		StartsAt: now.Add(-time.Hour),
		Version:  1,
	}

	if !Authorize(assignment, PermissionReportsView, Scope{
		ClubID:        8,
		TeamID:        &teamID,
		SeasonID:      &seasonID,
		CompetitionID: &competitionID,
	}, now) {
		t.Fatal("expected exact scoped resource to be authorized")
	}
	if Authorize(assignment, PermissionReportsView, Scope{
		ClubID:        8,
		TeamID:        &otherTeamID,
		SeasonID:      &seasonID,
		CompetitionID: &competitionID,
	}, now) {
		t.Fatal("foreign team was authorized")
	}
	if Authorize(assignment, PermissionReportsView, Scope{
		ClubID:        8,
		TeamID:        &teamID,
		SeasonID:      &otherSeasonID,
		CompetitionID: &competitionID,
	}, now) {
		t.Fatal("foreign season was authorized")
	}
	if Authorize(assignment, PermissionReportsView, Scope{
		ClubID:        8,
		TeamID:        &teamID,
		SeasonID:      &seasonID,
		CompetitionID: &otherCompetitionID,
	}, now) {
		t.Fatal("foreign competition was authorized")
	}
}

func TestParseRoleKeyRejectsProviderClaims(t *testing.T) {
	if role, ok := ParseRoleKey(" CLUB_SECRETARY "); !ok || role != RoleClubSecretary {
		t.Fatalf("expected club secretary, got %q, %v", role, ok)
	}
	if _, ok := ParseRoleKey("oidc-admins"); ok {
		t.Fatal("provider group unexpectedly accepted as an application role")
	}
}

func TestPortalOperationsPermissionMatrix(t *testing.T) {
	now := time.Now().UTC()
	assignment := Assignment{
		ID: uuid.New(), MembershipID: uuid.New(), UserID: uuid.New(),
		Scope: Scope{ClubID: 12}, Status: "active",
		StartsAt: now.Add(-time.Hour), Version: 1,
	}
	tests := []struct {
		name       string
		role       RoleKey
		permission Permission
		want       bool
	}{
		{"primary admin can send messages", RoleClubPrimaryAdmin, PermissionMessagesReply, true},
		{"secretary can manage club profile requests", RoleClubSecretary, PermissionClubProfileManage, true},
		{"secretary can manage starred reviews", RoleClubSecretary, PermissionStarredPlayersManage, true},
		{"secretary can manage identity reconciliation", RoleClubSecretary, PermissionPlayerIdentityManage, true},
		{"secretary can manage registration handoff", RoleClubSecretary, PermissionRegistrationManage, true},
		{"secretary can capture fixture constraints", RoleClubSecretary, PermissionFixturesManage, true},
		{"junior officer can manage junior administration", RoleClubJuniorOfficer, PermissionJuniorAdminManage, true},
		{"junior officer cannot manage player identity", RoleClubJuniorOfficer, PermissionPlayerIdentityManage, false},
		{"captain can see fixtures", RoleCaptainManager, PermissionFixturesView, true},
		{"captain cannot alter fixture constraints", RoleCaptainManager, PermissionFixturesManage, false},
		{"read-only user can see messages", RoleReadOnlyClubUser, PermissionMessagesView, true},
		{"read-only user cannot send messages", RoleReadOnlyClubUser, PermissionMessagesReply, false},
		{"ordinary club role cannot see safeguarding", RoleClubJuniorOfficer, PermissionSafeguardingView, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assignment.Role = test.role
			if got := Authorize(assignment, test.permission, assignment.Scope, now); got != test.want {
				t.Fatalf("Authorize(%s, %s) = %v, want %v", test.role, test.permission, got, test.want)
			}
		})
	}
}
