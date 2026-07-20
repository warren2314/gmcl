package httpserver

import (
	"context"

	"cricket-ground-feedback/internal/leagueapi"
	"cricket-ground-feedback/internal/starred"
)

type starredSyncSummary struct {
	List       starred.ImportResult
	Scorecards starred.ScorecardSyncResult
	Pending    int
	Batches    int
}

// syncStarredPlayerData refreshes the published list and then consumes bounded
// batches from the incremental scorecard queue. A scorecard fetch failure stays
// pending for the next weekly run and does not discard successful imports.
func (s *Server) syncStarredPlayerData(ctx context.Context, seasonYear, batchLimit, maxBatches int) (starredSyncSummary, error) {
	if batchLimit < 1 || batchLimit > 100 {
		batchLimit = 100
	}
	if maxBatches < 1 || maxBatches > 10 {
		maxBatches = 1
	}
	snapshot, body, source, err := starred.FetchSnapshot(ctx, seasonYear)
	if err != nil {
		return starredSyncSummary{}, err
	}
	listResult, err := starred.StoreSnapshot(ctx, s.DB, snapshot, body, source, s.starredSeasonStart(ctx, seasonYear))
	if err != nil {
		return starredSyncSummary{}, err
	}
	summary := starredSyncSummary{List: listResult}
	client := leagueapi.NewClient(leagueapi.NewConfigFromEnv())
	for batch := 0; batch < maxBatches; batch++ {
		result, syncErr := starred.SyncPendingScorecards(ctx, s.DB, client, seasonYear, batchLimit)
		if syncErr != nil {
			return summary, syncErr
		}
		summary.Batches++
		summary.Scorecards.Matches += result.Matches
		summary.Scorecards.Appearances += result.Appearances
		summary.Scorecards.Failures = append(summary.Scorecards.Failures, result.Failures...)
		pending, pendingErr := starred.PendingMatchCount(ctx, s.DB, seasonYear)
		if pendingErr != nil {
			return summary, pendingErr
		}
		summary.Pending = pending
		if pending == 0 || result.Matches == 0 {
			break
		}
	}
	return summary, nil
}
