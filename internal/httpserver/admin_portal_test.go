package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"cricket-ground-feedback/internal/portal"

	"github.com/google/uuid"
)

func TestAdminPortalInviteRoleAllowlist(t *testing.T) {
	for _, role := range []portal.RoleKey{
		portal.RoleClubPrimaryAdmin,
		portal.RoleClubAdmin,
		portal.RoleClubSecretary,
		portal.RoleReadOnlyClubUser,
	} {
		if !adminPortalInviteRoleAllowed(role) {
			t.Fatalf("expected role %q to be allowed", role)
		}
	}
	for _, role := range []portal.RoleKey{
		portal.RoleCaptainManager,
		portal.RoleClubJuniorOfficer,
		portal.RoleClubSafeguarding,
		portal.RoleKey("idp-admin"),
	} {
		if adminPortalInviteRoleAllowed(role) {
			t.Fatalf("restricted role %q was allowed by generic onboarding", role)
		}
	}
}

func TestPortalFeatureBadge(t *testing.T) {
	if got := portalFeatureBadge(true); got == portalFeatureBadge(false) {
		t.Fatal("enabled and disabled feature badges are identical")
	}
}

func TestAdminPortalRecipientRoleCategoryMetadataMatchesServerPolicy(t *testing.T) {
	for _, role := range adminPortalRecipientRoles() {
		metadata := strings.Fields(adminPortalAllowedCategoryValues(role))
		allowed := make(map[string]bool, len(metadata))
		for _, category := range metadata {
			allowed[category] = true
		}
		for _, category := range []portal.MessageCategory{
			portal.MessageCategoryGeneral,
			portal.MessageCategoryCompliance,
			portal.MessageCategoryFixtures,
			portal.MessageCategoryRegistration,
			portal.MessageCategoryStarred,
			portal.MessageCategoryJunior,
			portal.MessageCategoryContact,
			portal.MessageCategoryPlayerIdentity,
		} {
			if got, want := allowed[string(category)],
				portal.RecipientRoleAllowedForCategory(category, role); got != want {
				t.Fatalf(
					"role %q category %q metadata = %t, policy = %t",
					role,
					category,
					got,
					want,
				)
			}
		}
	}
}

func TestAdminPortalCampaignControlScriptSynchronizesBothSelectors(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeAdminPortalCampaignControlScript(recorder)
	body := recorder.Body.String()
	for _, required := range []string{
		`portal-message-category`,
		`portal-recipient-role`,
		`portal-competition-context`,
		`portal-message-clubs`,
		`syncCompetitionOptions`,
		`option.dataset.scopeRoleKeys`,
		`competition.value = firstValidOption`,
		`sharedRoles`,
		`competition.addEventListener("change", syncCompetitionClubs)`,
		`clubs.addEventListener("change", syncCompetitionClubs)`,
		`recipientRole.value = firstValid`,
		`option.selected = false`,
		`syncRecipientRoles();`,
		`syncCompetitionOptions();`,
		`syncCompetitionClubs();`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("control script is missing %q", required)
		}
	}
}

func TestAdminPortalClubScopeKeysRequireMatchingCompetition(t *testing.T) {
	competitionID := uuid.New()
	access := portal.StaffAccess{
		Assignments: []portal.StaffAssignment{{
			Role:               portal.StaffRoleClubLiaison,
			CompetitionID:      &competitionID,
			CompetitionClubIDs: []int32{10, 20},
			CompetitionActive:  true,
		}},
	}
	competition := portal.StaffCompetition{
		ID:      competitionID,
		ClubIDs: []int32{10, 20},
	}
	categories := []portal.MessageCategory{
		portal.MessageCategoryGeneral,
		portal.MessageCategoryJunior,
	}
	keys := adminPortalClubScopeRoleKeys(
		access,
		categories,
		[]portal.StaffCompetition{competition},
		10,
	)
	joined := strings.Join(keys, " ")
	if strings.Contains(joined, "|none|") {
		t.Fatalf("competition-only scope included no-context key: %q", joined)
	}
	for _, category := range categories {
		want := string(category) + "|" + competitionID.String() +
			"|" + string(portal.StaffRoleClubLiaison)
		if !strings.Contains(joined, want) {
			t.Fatalf("scope keys %q are missing %q", joined, want)
		}
	}
	if keys := adminPortalClubScopeRoleKeys(
		access,
		categories,
		[]portal.StaffCompetition{competition},
		30,
	); len(keys) != 0 {
		t.Fatalf("unmapped club scope keys = %#v", keys)
	}
}

func TestParsePortalClubIDsRequiresPositiveUniqueValues(t *testing.T) {
	got, err := parsePortalClubIDs([]string{"7", " 8 ", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("club IDs = %#v", got)
	}
	for _, values := range [][]string{{}, {"0"}, {"not-a-club"}} {
		if _, err := parsePortalClubIDs(values); err == nil {
			t.Fatalf("values %#v unexpectedly passed", values)
		}
	}
}
