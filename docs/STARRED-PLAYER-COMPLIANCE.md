# Starred-player compliance

The super-admin page at `/admin/starred-players` combines the published GMCL
List A/List B sheet with Play-Cricket team sheets.

## Data sources

- The published Google Sheet embedded at
  `https://www.gtrmcrcricket.co.uk/pages/starred-players-latest` is read through
  its public CSV endpoint. Override `STARRED_PLAYERS_CSV_URL` only if the
  published sheet changes.
- Cached rows in `league_fixtures` provide the bounded scorecard work queue.
- `/api/v2/match_detail.json` supplies both team sheets and stable player IDs.

The importer stores the original lists and dated amendments separately, then
materialises effective membership periods. Ambiguous changes remain visible in
the admin review instead of being guessed.

## Checks

- List A appearances below the 1st XI in League or Cup matches.
- List B appearances below the 2nd XI in League or Cup matches.
- Junior-tagged findings are retained but labelled for exemption review.
- At 30 June: 1st XI League appearances divided by all personal League
  appearances. Cup matches are deliberately excluded from this calculation.
- Club list-size and missing-form checks.

The first version does not automatically approve junior, Category 3, temporary
or exceptional exemptions. Use `starred_exemptions` as the durable source when
those admin controls are added; findings should be reviewed before sanctions.

## Initial backfill

1. Deploy migration `0033_starred_player_compliance.sql`.
2. Open `/admin/starred-players` and click **Sync published list**.
3. Click **Import all pending scorecards** once. Keep the page open while it
   automatically works through the queue in batches of 50 and displays live
   progress. Refreshing or closing the page safely stops after the current
   batch; clicking the button again resumes from the remaining scorecards.
4. Confirm suggested identity mappings and review ambiguous amendments.

Scorecard imports are incremental. A fixture is revisited only when its
Play-Cricket `last_updated` value changes.

## Overnight automation

Call the HMAC-protected endpoint:

`POST /internal/sync-starred-players`

Example body:

```json
{"season_year": 2026, "scorecard_limit": 25}
```

Repeat nightly. A higher limit up to 100 is accepted for a controlled initial
backfill. The endpoint returns individual scorecard failures without discarding
successful imports.

## Configuration

- `PLAY_CRICKET_API_BASE_URL`
- `PLAY_CRICKET_API_KEY`
- `PLAY_CRICKET_AUTH_QUERY_PARAM=api_token`
- `PLAY_CRICKET_MATCH_DETAIL_URL_TEMPLATE` (optional)
- `STARRED_PLAYERS_CSV_URL` (optional)

Keep API tokens in the deployment secret store, never in source control.
