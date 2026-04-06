# League information API (Play-Cricket / ECB) — integration roadmap

This document describes what is implemented in the repo, what is still needed from the league API provider, and a practical test plan for next week.

## What we have now (in code)

| Area | Status |
|------|--------|
| **Database** | `migrations/0006_league_fixtures_and_team_mapping.sql`: `league_fixtures` table (cached match rows, umpire names, `payload` JSONB); `teams.play_cricket_team_id` for matching a local team to API `home_team_id` / `away_team_id`. |
| **HTTP client** | `internal/leagueapi`: `Config` from env, `Client.FetchMatchesForDate`, JSON parsing for `match_details` envelope (same shape as your sample). |
| **Upsert** | `UpsertFixtureRow` / `UpsertFixtureBatch` into `league_fixtures`. |
| **Internal sync** | `POST /internal/sync-league-fixtures` (HMAC, same as other `/internal/*` routes). Body: `{ "match_date": "2025-04-26" }` to fetch from API, or `{ "raw_body": { ... full API JSON ... } }` to upsert without outbound HTTP (good for local testing). |
| **Captain prefill** | On `GET /captain/form`, if `teams.play_cricket_team_id` is set and a `league_fixtures` row exists for that team + selected match date, empty `umpire1_name` / `umpire2_name` draft fields are filled and a banner explains the source. |

## What we need from the league API (external)

1. **Official API key** (or OAuth client credentials, if that is how ECB exposes it) for the **league information** / fixtures endpoint your competition uses.
2. **Exact base URL and path** for listing matches by date (or by league + date). The app builds:  
   `PLAY_CRICKET_API_BASE_URL` + `PLAY_CRICKET_MATCHES_URL_TEMPLATE`  
   with placeholders `{leagueId}` and `{date}` (date format configurable via `PLAY_CRICKET_DATE_FORMAT`, default `dd/MM/yyyy` to match your sample).
3. **Auth mechanism** — one of:
   - Bearer token in `Authorization` (set `PLAY_CRICKET_API_KEY` and optionally `PLAY_CRICKET_AUTH_HEADER` for a full header value), or  
   - Query parameter (set `PLAY_CRICKET_AUTH_QUERY_PARAM`, e.g. `api_token`, plus `PLAY_CRICKET_API_KEY`).
4. **Confirm response shape** matches `match_details[]` with at least: `match_id`, `match_date`, `home_team_id`, `away_team_id`, `umpire_1_name`, `umpire_2_name` (your sample is sufficient).

## Data mapping checklist

- **Team link**: For each `teams` row that should prefill, set `play_cricket_team_id` to the string ID from the API (e.g. `"23399"`). Without this, prefill will not run for that team.
- **Fixture cache**: Run sync (cron/n8n) after fixtures are published or on the morning of match day; captains still see editable fields if the API is wrong.

## Roadmap — test next week

1. **Obtain credentials** — API key + documented endpoint for league match list for a given date.
2. **Configure env** in staging (see `.env.example` `PLAY_CRICKET_*` variables); adjust `PLAY_CRICKET_MATCHES_URL_TEMPLATE` until a single `GET` returns `match_details` for a known date.
3. **Run migration** `0006` on staging DB.
4. **Seed or import team IDs** — set `play_cricket_team_id` for at least two test teams that appear in API responses.
5. **Test sync** — `POST /internal/sync-league-fixtures` with HMAC:
   - `{"match_date":"YYYY-MM-DD"}` when live API is configured, or  
   - `{"raw_body":{...}}` with a saved JSON file for offline testing.
6. **Test captain flow** — magic link → form: confirm umpire names prefill and banner; change names and submit; verify admin submission shows expected data.
7. **Schedule** — optional n8n job: Sat/Sun morning `sync-league-fixtures` + reminder emails later.

## Open questions for Stuart / API owner

- Rate limits and allowed polling frequency.
- Whether Saturday vs Sunday use different competition IDs (and if we need two sync jobs or one combined endpoint).
- Whether `umpire_3_name` should appear in the form when non-empty (currently stored in `payload` only).
