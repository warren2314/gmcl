package portal

import (
	"testing"

	"github.com/google/uuid"
)

func TestRecipientRolesForCategoryReturnsExactOrderedRoles(t *testing.T) {
	tests := []struct {
		category MessageCategory
		want     []RecipientRoleKey
	}{
		{MessageCategoryGeneral, []RecipientRoleKey{
			RecipientPrimaryContact, RecipientSecretary,
		}},
		{MessageCategoryCompliance, []RecipientRoleKey{
			RecipientPrimaryContact, RecipientSecretary,
		}},
		{MessageCategoryFixtures, []RecipientRoleKey{
			RecipientFixturesContact, RecipientSecretary, RecipientPrimaryContact,
		}},
		{MessageCategoryRegistration, []RecipientRoleKey{
			RecipientRegistration, RecipientPlayCricketAdmin,
			RecipientSecretary, RecipientPrimaryContact,
		}},
		{MessageCategoryStarred, []RecipientRoleKey{
			RecipientPlayCricketAdmin, RecipientSecretary, RecipientPrimaryContact,
		}},
		{MessageCategoryJunior, []RecipientRoleKey{
			RecipientJuniorContact, RecipientSecretary, RecipientPrimaryContact,
		}},
		{MessageCategoryContact, []RecipientRoleKey{
			RecipientPrimaryContact, RecipientSecretary,
		}},
		{MessageCategoryPlayerIdentity, []RecipientRoleKey{
			RecipientRegistration, RecipientPlayCricketAdmin,
			RecipientSecretary, RecipientPrimaryContact,
		}},
	}
	for _, test := range tests {
		t.Run(string(test.category), func(t *testing.T) {
			got := RecipientRolesForCategory(test.category)
			if len(got) != len(test.want) {
				t.Fatalf("roles = %#v, want %#v", got, test.want)
			}
			for index := range test.want {
				if got[index] != test.want[index] {
					t.Fatalf("roles = %#v, want %#v", got, test.want)
				}
			}
		})
	}
}

func TestRecipientRoleAllowedForCategoryMatchesRoleOptions(t *testing.T) {
	categories := []MessageCategory{
		MessageCategoryGeneral,
		MessageCategoryCompliance,
		MessageCategoryFixtures,
		MessageCategoryRegistration,
		MessageCategoryStarred,
		MessageCategoryJunior,
		MessageCategoryContact,
		MessageCategoryPlayerIdentity,
	}
	roles := []RecipientRoleKey{
		RecipientPrimaryContact,
		RecipientSecretary,
		RecipientPlayCricketAdmin,
		RecipientJuniorContact,
		RecipientFixturesContact,
		RecipientRegistration,
	}
	for _, category := range categories {
		allowed := make(map[RecipientRoleKey]bool)
		for _, role := range RecipientRolesForCategory(category) {
			allowed[role] = true
		}
		for _, role := range roles {
			if got := RecipientRoleAllowedForCategory(category, role); got != allowed[role] {
				t.Fatalf(
					"category %q role %q allowed = %t, want %t",
					category,
					role,
					got,
					allowed[role],
				)
			}
		}
	}
}

func TestStaffAccessScopesRolesAndCategories(t *testing.T) {
	clubA := int32(10)
	clubB := int32(20)
	competition := uuid.New()
	access := StaffAccess{
		AdminUserID: 7,
		Assignments: []StaffAssignment{
			{
				Role:   StaffRoleJuniorAdministrator,
				ClubID: &clubA,
			},
			{
				Role:               StaffRoleClubLiaison,
				CompetitionID:      &competition,
				CompetitionClubIDs: []int32{clubB},
				CompetitionActive:  true,
			},
		},
	}

	if !access.CanAccessCase(clubA, MessageCategoryJunior, nil) {
		t.Fatal("club-scoped junior case was denied")
	}
	if access.CanAccessCase(clubA, MessageCategoryGeneral, nil) {
		t.Fatal("junior assignment granted a general case")
	}
	if access.CanAccessCase(clubB, MessageCategoryJunior, nil) {
		t.Fatal("junior assignment escaped its club scope")
	}
	if !access.CanAccessCase(clubB, MessageCategoryGeneral, &competition) {
		t.Fatal("competition-scoped CLO case was denied")
	}
	if access.CanAccessCase(clubB, MessageCategoryGeneral, nil) {
		t.Fatal("competition-scoped CLO escaped its competition context")
	}
	if !access.CanSendAs(
		StaffRoleJuniorAdministrator,
		clubA,
		MessageCategoryJunior,
		nil,
	) {
		t.Fatal("junior sender role did not cover its club")
	}
	if !access.CanSendAs(
		StaffRoleClubLiaison,
		clubB,
		MessageCategoryGeneral,
		&competition,
	) {
		t.Fatal("CLO sender role did not cover its competition club")
	}
}

func TestCompetitionAssignmentOnlyGrantsPortalAccessWhileUsable(t *testing.T) {
	competitionID := uuid.New()
	access := StaffAccess{Assignments: []StaffAssignment{{
		Role:          StaffRoleClubLiaison,
		CompetitionID: &competitionID,
	}}}
	if access.HasPortalStaffAccess() {
		t.Fatal("inactive competition assignment granted portal staff access")
	}
	access.Assignments[0].CompetitionActive = true
	if access.HasPortalStaffAccess() {
		t.Fatal("unmapped competition assignment granted portal staff access")
	}
	access.Assignments[0].CompetitionClubIDs = []int32{10}
	if !access.HasPortalStaffAccess() {
		t.Fatal("active mapped competition assignment was denied portal staff access")
	}
}

func TestStaffCampaignRequiresOneSenderRoleToCoverEveryClub(t *testing.T) {
	clubA := int32(10)
	clubB := int32(20)
	access := StaffAccess{Assignments: []StaffAssignment{
		{
			Role:   StaffRoleJuniorAdministrator,
			ClubID: &clubA,
		},
		{
			Role:   StaffRoleClubLiaison,
			ClubID: &clubB,
		},
	}}
	if _, ok := access.senderRoleFor(
		MessageCategoryJunior,
		[]int32{clubA, clubB},
		nil,
	); ok {
		t.Fatal("mixed role scopes covered one multi-club campaign")
	}
}

func TestSuperAdministratorCanUseEveryClubAndCategory(t *testing.T) {
	access := StaffAccess{AdminUserID: 1, SuperAdmin: true}
	if !access.CanAccessCase(999, MessageCategoryJunior, nil) {
		t.Fatal("super administrator was denied a junior case")
	}
	role, ok := access.RoleForCase(999, MessageCategoryGeneral, nil)
	if !ok || role != StaffRoleSuperAdministrator {
		t.Fatalf("role = %q, allowed = %t", role, ok)
	}
}

func TestUniquePositiveClubIDs(t *testing.T) {
	got := uniquePositiveClubIDs([]int32{4, 2, 4, 0, -1, 3})
	want := []int32{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("club IDs = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("club IDs = %#v", got)
		}
	}
}
