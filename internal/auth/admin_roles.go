package auth

import (
	"os"
	"strings"
)

// IsConfiguredSuperAdmin applies the legacy/configured Super Administrator
// override consistently anywhere current administrator authority is checked.
// Database role checks remain authoritative for every other administrator.
func IsConfiguredSuperAdmin(username, email string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	email = strings.ToLower(strings.TrimSpace(email))
	configured := []string{"warren2314"}
	configured = append(
		configured,
		strings.Split(os.Getenv("SUPER_ADMIN_EMAILS"), ",")...,
	)
	for _, entry := range configured {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry != "" && (entry == username || entry == email) {
			return true
		}
	}
	return false
}
