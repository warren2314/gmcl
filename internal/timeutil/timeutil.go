package timeutil

import "time"

// NextWednesdayExpiry returns the next Wednesday 23:59:59 in the given location.
// If today is Wednesday, it returns today 23:59:59 (same weekly window).
// Used for magic link expiry: Sat/Sun/Mon/Tue/Wed requests expire that Wednesday;
// Thu/Fri requests expire the following Wednesday.
func NextWednesdayExpiry(now time.Time, loc *time.Location) time.Time {
	n := now.In(loc)
	// End of today in loc
	endOfToday := time.Date(n.Year(), n.Month(), n.Day(), 23, 59, 59, 0, loc)
	wd := n.Weekday() // Sunday=0, Monday=1, ... Wednesday=3
	target := time.Wednesday
	daysUntil := (int(target) - int(wd) + 7) % 7
	return endOfToday.AddDate(0, 0, daysUntil)
}
