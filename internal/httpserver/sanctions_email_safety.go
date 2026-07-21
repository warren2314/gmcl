package httpserver

import (
	"os"
	"strings"
)

// sanctionsEmailDisabled is an environment-level safety switch. It is checked
// at every sanctions SMTP boundary so test environments cannot accidentally
// deliver case or sanction notices, even if SMTP credentials are later added.
func sanctionsEmailDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SANCTIONS_EMAIL_DISABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
