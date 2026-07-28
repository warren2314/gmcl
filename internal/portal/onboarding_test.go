package portal

import "testing"

func TestNormalizeOnboardingFeaturesAlwaysIncludesPortalAccess(t *testing.T) {
	features, err := normalizeOnboardingFeatures([]FeatureKey{
		FeatureSecureMessaging,
		FeatureSecureMessaging,
	})
	if err != nil {
		t.Fatalf("normalizeOnboardingFeatures returned error: %v", err)
	}
	if len(features) != 2 ||
		features[0] != FeaturePortalAccess ||
		features[1] != FeatureSecureMessaging {
		t.Fatalf("unexpected normalized features: %#v", features)
	}
}

func TestNormalizeOnboardingFeaturesRejectsUnknownFeature(t *testing.T) {
	if _, err := normalizeOnboardingFeatures([]FeatureKey{"unknown"}); err == nil {
		t.Fatal("expected unknown feature to be rejected")
	}
}

func TestOnboardingIdentityReady(t *testing.T) {
	ready := []OnboardingIdentityStatus{
		OnboardingIdentityCreated,
		OnboardingIdentityExisting,
		OnboardingIdentityConfirmed,
		OnboardingIdentityInvitationResent,
		OnboardingIdentityManualConfirmed,
	}
	for _, status := range ready {
		if !(OnboardingRun{IdentityStatus: status}).IdentityReady() {
			t.Fatalf("expected %q to be ready", status)
		}
	}
	if (OnboardingRun{IdentityStatus: OnboardingIdentityManualRequired}).IdentityReady() {
		t.Fatal("manual checkpoint must not be ready until explicitly confirmed")
	}
}
