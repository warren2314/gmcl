# Starred-player compliance

The super-admin page at `/admin/starred-players` combines the published GMCL
List A/List B sheet with Play-Cricket team sheets.

## Page layout

The page is organised as a workflow: an overview (imported-data counts and an
action queue whose tiles turn green when nothing is outstanding), then numbered
sections — Step 1 import data, Step 2 match identities (with the amendments
review), Step 3 review potential breaches (with club list completeness), and
Step 4 the 31 July test. A sticky "Jump to" bar shows outstanding counts per
section, and every section heading carries a "?" help popover explaining what
the section is for and how its numbers are calculated. Section anchors
(`#potential-breaches`, `#july-31-test`, `#identity-matches`, `#card-detail`)
are stable and used by post-action redirects.

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
- Junior-tagged findings are labelled for exemption review. Accepting a junior
  exemption creates one approved exemption for that player, club and season,
  closes the player's current findings, and suppresses later imported findings
  through the configured season end. It never carries into the next season.
- Sunday single-match and development exemptions remain limited to their
  approved Sunday League dates; they do not suppress Saturday, Cup or GMCL20
  findings.
- The separate 31 July candidate review uses top-two-XI League appearances
  divided by all personal League appearances. Cup matches are deliberately
  excluded from this calculation.
- Breach monitoring and scorecard imports continue throughout the playing
  season. Matches after 31 July are displayed and evaluated for breaches.
- Club list-size and missing-form checks.

Approved exemptions are stored in `starred_exemptions`. Junior approvals use a
season-scoped `junior_season` record, while published Sunday approvals retain
their narrower match/date restrictions. Migration
`0071_starred_junior_season_exemptions.sql` backfills previously accepted
junior decisions from their `junior_bulk: true` audit entries. If a best-effort
audit write was missed, the migration can also recover an accepted finding by
rechecking the junior tag and identity in the stored starred-list history.

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

## Weekly automation

Production enables `STARRED_WEEKLY_SYNC_ENABLED`. The active window comes from
the configured `seasons.start_date` and `seasons.end_date`. During that window,
the app refreshes the published list and imports up to five batches of 100
missing or changed season-to-date scorecards every Monday at 03:00
Europe/London. The first Monday after the configured season end is retained as
a final catch-up for delayed scorecards. If configured season dates are
unavailable, the scheduler safely falls back to 1 April through 31 October plus
one catch-up Monday.

A bounded startup refresh also runs 30 seconds after deployment when the season
window is active. Every run is recorded in `audit_logs`; an advisory database
lock prevents overlapping instances.

The same operation can be triggered by n8n or another scheduler through the
HMAC-protected endpoint:

`POST /internal/sync-starred-players`

Example body:

```json
{"season_year": 2026, "scorecard_limit": 25}
```

A scorecard limit up to 100 is accepted for a controlled manual backfill. The
endpoint returns individual scorecard failures without discarding successful
imports.

## Configuration

- `PLAY_CRICKET_API_BASE_URL`
- `PLAY_CRICKET_API_KEY`
- `PLAY_CRICKET_AUTH_QUERY_PARAM=api_token`
- `PLAY_CRICKET_MATCH_DETAIL_URL_TEMPLATE` (optional)
- `STARRED_PLAYERS_CSV_URL` (optional)
- `STARRED_WEEKLY_SYNC_ENABLED=true` (production weekly scheduler)

Keep API tokens in the deployment secret store, never in source control.
