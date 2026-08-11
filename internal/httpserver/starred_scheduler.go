package httpserver

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"
)

const starredWeeklySyncAction = "automatic_sync_starred_players"

type starredWeeklyWindow struct {
	SeasonStart time.Time
	SeasonEnd   time.Time
	CatchupEnd  time.Time
	Configured  bool
}

func starredWeeklySyncEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("STARRED_WEEKLY_SYNC_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func buildStarredWeeklyWindow(seasonStart, seasonEnd time.Time, loc *time.Location, configured bool) starredWeeklyWindow {
	if loc == nil {
		loc = time.UTC
	}
	start := time.Date(seasonStart.Year(), seasonStart.Month(), seasonStart.Day(), 0, 0, 0, 0, loc)
	end := time.Date(seasonEnd.Year(), seasonEnd.Month(), seasonEnd.Day(), 23, 59, 59, 0, loc)
	catchup := nextStarredWeeklySync(end, loc)
	catchupEnd := time.Date(catchup.Year(), catchup.Month(), catchup.Day(), 23, 59, 59, 0, loc)
	return starredWeeklyWindow{SeasonStart: start, SeasonEnd: end, CatchupEnd: catchupEnd, Configured: configured}
}

func fallbackStarredWeeklyWindow(year int, loc *time.Location) starredWeeklyWindow {
	if loc == nil {
		loc = time.UTC
	}
	// A missing season row must not silently stop compliance imports. April to
	// October safely covers the normal playing season, followed by one Monday
	// catch-up. Configured season dates take precedence whenever available.
	start := time.Date(year, time.April, 1, 0, 0, 0, 0, loc)
	end := time.Date(year, time.October, 31, 0, 0, 0, 0, loc)
	return buildStarredWeeklyWindow(start, end, loc, false)
}

func (window starredWeeklyWindow) Active(now time.Time) bool {
	loc := window.SeasonStart.Location()
	local := now.In(loc)
	return !local.Before(window.SeasonStart) && !local.After(window.CatchupEnd)
}

// starredWeeklySyncWindowActive is retained as the safe, database-free
// fallback used by callers that have not loaded configured season dates.
func starredWeeklySyncWindowActive(now time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	return fallbackStarredWeeklyWindow(now.In(loc).Year(), loc).Active(now)
}

// loadStarredWeeklyWindow returns the configured playing window for the year
// containing now. The final Monday after season end remains active so delayed
// scorecards from the last weekend can be collected. If the season table is
// unavailable or has no row for the year, a conservative playing-season
// fallback keeps automatic compliance monitoring alive.
func (s *Server) loadStarredWeeklyWindow(ctx context.Context, now time.Time, loc *time.Location) starredWeeklyWindow {
	if loc == nil {
		loc = time.UTC
	}
	year := now.In(loc).Year()
	fallback := fallbackStarredWeeklyWindow(year, loc)
	if s == nil || s.DB == nil {
		return fallback
	}
	var start, end time.Time
	err := s.DB.QueryRow(ctx, `
		SELECT MIN(start_date), MAX(end_date)
		FROM seasons
		WHERE EXTRACT(YEAR FROM start_date)::int=$1
		  AND is_archived = FALSE
		HAVING COUNT(*) > 0
	`, year).Scan(&start, &end)
	if err != nil || start.IsZero() || end.IsZero() || end.Before(start) {
		return fallback
	}
	return buildStarredWeeklyWindow(start, end, loc, true)
}

func nextStarredWeeklySync(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	days := (int(time.Monday) - int(local.Weekday()) + 7) % 7
	targetDate := local.AddDate(0, 0, days)
	target := time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), 3, 0, 0, 0, loc)
	if !target.After(local) {
		targetDate = targetDate.AddDate(0, 0, 7)
		target = time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), 3, 0, 0, 0, loc)
	}
	return target
}

func (s *Server) hasRecentAutomaticStarredSync(ctx context.Context) (bool, error) {
	var recent bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_logs WHERE action=$1 AND created_at >= now() - interval '6 days')`, starredWeeklySyncAction).Scan(&recent)
	return recent, err
}

func (s *Server) recordAutomaticStarredSync(ctx context.Context, action string, metadata map[string]any) {
	payload, err := json.Marshal(metadata)
	if err != nil {
		payload = []byte("{}")
	}
	_, _ = s.DB.Exec(ctx, `INSERT INTO audit_logs(actor_type,action,entity_type,metadata,user_agent) VALUES('system',$1,'starred_import_run',$2::jsonb,'GMCL weekly starred-player scheduler')`, action, payload)
}

func (s *Server) runAutomaticStarredSync(parent context.Context, now time.Time, force bool) {
	loc := s.LondonLoc
	if loc == nil {
		loc = time.UTC
	}
	windowCtx, windowCancel := context.WithTimeout(parent, 10*time.Second)
	window := s.loadStarredWeeklyWindow(windowCtx, now, loc)
	windowCancel()
	if !window.Active(now) {
		return
	}
	if !window.Configured {
		log.Printf("weekly starred-player sync using fallback season window: %s through %s", window.SeasonStart.Format("2006-01-02"), window.SeasonEnd.Format("2006-01-02"))
	}
	if !force {
		checkCtx, checkCancel := context.WithTimeout(parent, 10*time.Second)
		recent, err := s.hasRecentAutomaticStarredSync(checkCtx)
		checkCancel()
		if err != nil {
			log.Printf("weekly starred-player sync check failed: %v", err)
			return
		}
		if recent {
			return
		}
	}

	lockCtx, lockCancel := context.WithTimeout(parent, 10*time.Second)
	conn, err := s.DB.Acquire(lockCtx)
	lockCancel()
	if err != nil {
		log.Printf("weekly starred-player sync lock connection failed: %v", err)
		return
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(parent, `SELECT pg_try_advisory_lock(hashtext('gmcl_starred_weekly_sync'))`).Scan(&locked); err != nil || !locked {
		if err != nil {
			log.Printf("weekly starred-player sync lock failed: %v", err)
		}
		return
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext('gmcl_starred_weekly_sync'))`)
	}()

	seasonYear := now.In(loc).Year()
	runCtx, runCancel := context.WithTimeout(parent, 15*time.Minute)
	summary, syncErr := s.syncStarredPlayerData(runCtx, seasonYear, 100, 5)
	runCancel()
	metadata := map[string]any{
		"season": seasonYear, "scheduled_for": now.In(loc).Format(time.RFC3339),
		"season_start": window.SeasonStart.Format("2006-01-02"), "season_end": window.SeasonEnd.Format("2006-01-02"),
		"final_catchup_date": window.CatchupEnd.Format("2006-01-02"), "season_window_configured": window.Configured,
		"list_already_current": summary.List.AlreadyCurrent, "list_entries": summary.List.Entries,
		"list_amendments": summary.List.Amendments, "scorecards": summary.Scorecards.Matches,
		"appearances": summary.Scorecards.Appearances, "failures": len(summary.Scorecards.Failures),
		"pending": summary.Pending, "batches": summary.Batches,
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer auditCancel()
	if syncErr != nil {
		metadata["error"] = syncErr.Error()
		s.recordAutomaticStarredSync(auditCtx, starredWeeklySyncAction+"_failed", metadata)
		log.Printf("weekly starred-player sync failed: %v", syncErr)
		return
	}
	s.recordAutomaticStarredSync(auditCtx, starredWeeklySyncAction, metadata)
	log.Printf("weekly starred-player sync complete: season=%d scorecards=%d appearances=%d pending=%d failures=%d", seasonYear, summary.Scorecards.Matches, summary.Scorecards.Appearances, summary.Pending, len(summary.Scorecards.Failures))
}

func (s *Server) startStarredWeeklySync(parent context.Context) context.CancelFunc {
	ctx, cancel := context.WithCancel(parent)
	if !starredWeeklySyncEnabled() {
		return cancel
	}
	go func() {
		// Perform a bounded catch-up soon after deployment while the season window is active.
		initial := time.NewTimer(30 * time.Second)
		defer initial.Stop()
		select {
		case <-ctx.Done():
			return
		case started := <-initial.C:
			// Always perform the bounded startup refresh. StoreSnapshot's source hash
			// and parser revision prevent duplicate imports while allowing a deployed
			// parser fix to rebuild unchanged published data.
			s.runAutomaticStarredSync(ctx, started, true)
		}
		for {
			now := time.Now()
			next := nextStarredWeeklySync(now, s.LondonLoc)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case scheduled := <-timer.C:
				s.runAutomaticStarredSync(ctx, scheduled, false)
			}
		}
	}()
	log.Printf("weekly starred-player sync enabled for Mondays at 03:00 Europe/London through configured season end plus one catch-up Monday")
	return cancel
}
