package httpserver

import (
	"testing"

	"cricket-ground-feedback/internal/portal"
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
