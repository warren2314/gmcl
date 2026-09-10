package httpserver

import (
	"fmt"
	"time"
)

func validateScheduledTaskUpdate(status string, notBefore *time.Time, now time.Time, linkedAward, currentAward bool) error {
	if linkedAward && !currentAward && status != "cancelled" {
		return fmt.Errorf("this award has been replaced or withdrawn; its task cannot be reopened or completed")
	}
	if status == "complete" && notBefore != nil && now.Before(*notBefore) {
		return fmt.Errorf("this scheduled award cannot be marked applied before %s", notBefore.Format("02 Jan 2006"))
	}
	return nil
}
