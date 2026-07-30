package auth

import "testing"

func TestIsConfiguredSuperAdmin(t *testing.T) {
	t.Setenv(
		"SUPER_ADMIN_EMAILS",
		" configured@example.test, OtherAdmin ",
	)
	for _, test := range []struct {
		name     string
		username string
		email    string
		want     bool
	}{
		{"legacy username", " Warren2314 ", "", true},
		{"configured email", "", "CONFIGURED@example.test", true},
		{"configured username", "otheradmin", "", true},
		{"ordinary administrator", "administrator", "admin@example.test", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsConfiguredSuperAdmin(
				test.username,
				test.email,
			); got != test.want {
				t.Fatalf("IsConfiguredSuperAdmin() = %t, want %t", got, test.want)
			}
		})
	}
}
