package httpserver

import (
	"fmt"
	"strings"
	"time"
)

func publicSanctionEffectLabel(effect string, redCardCount int) string {
	if redCardCount < 1 {
		redCardCount = 1
	}
	switch effect {
	case "scheduled_red":
		return fmt.Sprintf("%d scheduled red card%s", redCardCount, pluralSuffix(redCardCount))
	case "suspended_red":
		return fmt.Sprintf("%d suspended red card%s", redCardCount, pluralSuffix(redCardCount))
	default:
		return effectLabel(effect)
	}
}

func publicSanctionEffectStatus(status string, starts, ends *time.Time, now time.Time) string {
	if status != "active" && status != "suspended" {
		return status
	}
	if ends != nil && ends.Before(now) {
		return "Expired"
	}
	if status == "suspended" {
		return "Conditional — not applied"
	}
	if starts != nil && starts.After(now) {
		return "Scheduled"
	}
	return "Active"
}

func publicSanctionPoints(effect string, points int) string {
	if effect == "suspended_red" {
		return "No points deducted unless the suspended sanction is activated"
	}
	if points == 0 {
		return "No points deduction"
	}
	if effect == "scheduled_red" {
		return fmt.Sprintf("%d point%s to deduct from the effective date", points, pluralSuffix(points))
	}
	if effect == "points_adjustment" {
		if points > 0 {
			return fmt.Sprintf("%d point%s added", points, pluralSuffix(points))
		}
		points = -points
	}
	return fmt.Sprintf("%d point%s deducted", points, pluralSuffix(points))
}

func publicSanctionCondition(effect, condition string) string {
	if effect != "suspended_red" {
		return ""
	}
	text := "Conditional sanction: reaching the effective date does not activate these cards or deduct points."
	if condition = strings.TrimSpace(condition); condition != "" {
		text += " Activation condition: " + condition
	}
	return text
}
