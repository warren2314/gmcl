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

// ineligibleOutboundEmailEnabled is deliberately separate from intake. A
// deployment can reconcile private reports while retaining every ineligible
// response/outcome message in the immutable outbox for later release.
func ineligibleOutboundEmailEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("INELIGIBLE_OUTBOUND_EMAIL_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
