package portal

import (
	"testing"

	"github.com/google/uuid"
)

func TestRecipientRoleAllowedForCategory(t *testing.T) {
	tests := []struct {
		name     string
		category MessageCategory
		role     RecipientRoleKey
		want     bool
	}{
		{"general primary contact", MessageCategoryGeneral, RecipientPrimaryContact, true},
		{"general rejects junior contact", MessageCategoryGeneral, RecipientJuniorContact, false},
		{"fixtures contact", MessageCategoryFixtures, RecipientFixturesContact, true},
		{"registration Play-Cricket", MessageCategoryRegistration, RecipientPlayCricketAdmin, true},
		{"junior adult contact", MessageCategoryJunior, RecipientJuniorContact, true},
		{"junior rejects fixtures contact", MessageCategoryJunior, RecipientFixturesContact, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RecipientRoleAllowedForCategory(test.category, test.role); got != test.want {
				t.Fatalf("allowed = %t, want %t", got, test.want)
			}
		})
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
				Role:          StaffRoleClubLiaison,
				CompetitionID: &competition,
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
