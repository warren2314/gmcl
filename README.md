# Cricket Ground Feedback

Production-ready MVP for a cricket governing body: collect match-day ground feedback from captains (and optionally umpires), admin analytics, rankings, exports, and weekly reporting. Designed for UK/EU privacy and integration with n8n on a separate droplet (HTTPS only, no direct DB access).

## Stack

- **Backend**: Go 1.22, `net/http` + [chi](https://github.com/go-chi/chi), [pgx](https://github.com/jackc/pgx/v5)
- **Frontend**: Server-rendered HTML (templ + HTMX + Tailwind where used)
- **Database**: PostgreSQL 16
- **Deploy**: Docker + Caddy (reverse proxy, TLS)

## Layout

```
cmd/app/                 # main entry
internal/
  auth/                  # magic link token create/consume, revocation
  db/                    # pgx pool, NewFromEnv
  email/                 # placeholder for SMTP
  httpserver/            # chi router, captain + admin + internal handlers
  middleware/             # CSRF, rate limit, security headers, HMAC verifier
  timeutil/              # NextWednesdayExpiry (Europe/London)
migrations/              # 0001 core, 0002 auth/audit, 0003 csv preview + magic send log
docker/                  # Dockerfile.app, Dockerfile.caddy
Caddyfile
docker-compose.yml
```

## Features

### Captain flow

- **Entry**: `GET /` — club/team selection (IDs); current week derived server-side from date.
- **Magic link**: `POST /magic-link/request` — rate-limited; looks up captain for team + current week; creates single-use token; records send in `magic_link_send_log`.
  - **Expiry**: Next Wednesday 23:59:59 Europe/London (or today if Wednesday), hard cap 7 days, floor 10 minutes.
  - **Revocation**: On each new request, any previous unused tokens for that captain/week are invalidated (“latest link wins”).
  - **Throttle**: 3/hour and 10/week per captain (generic response when hit).
- **Confirm**: `GET /magic-link/confirm?token=...` — human “Continue” page (no auto-submit); `POST /magic-link/confirm` redeems token; `Cache-Control: no-store`.
- **Session**: Captain session cookie Path=`/captain`, HMAC-signed (aud, jti, iat), 2h expiry.
- **Form**: `GET /captain/form` (CSRF), autosave + submit with CSRF; drafts keyed by (season, week, team).

### Admin

- Login + 2FA (email code); session Path=`/admin`.
- CSV upload: DB-backed `csv_preview_tokens` (single-use; apply consumes row with `SELECT ... FOR UPDATE`, sets `used_at`); preview/apply audited; formula injection checks.
- Dashboard, weekly view, submission detail, rankings (basic).

### Internal (n8n)

- `POST /internal/send-reminders` — HMAC-signed; idempotent send of magic link reminders.
- `POST /internal/generate-weekly-report` — HMAC-signed; idempotent weekly report generation.
- `POST /internal/sync-league-fixtures` — HMAC-signed; fetches league / Play-Cricket match details (or accepts `raw_body` JSON) and upserts `league_fixtures` for umpire prefill. See [docs/LEAGUE-API-ROADMAP.md](docs/LEAGUE-API-ROADMAP.md).
- No direct DB access from n8n; all via HTTPS to this app.

### Security

- **CSRF**: Context-based double-submit token; validation on POST/PUT/DELETE/PATCH for admin and captain.
- **Internal HMAC**: Signature over `ts|nonce|method|path|Content-Type|body`; nonce cache with TTL and cap; replay rejected; `MaxBytesReader` 1MB.
- **Audit**: `audit_logs` with `request_id` (from `X-Request-ID`), events for magic link request/redeem, draft autosave, submission, admin login/2FA, CSV preview/apply, internal reminders/report.
- **Rate limiting**: Applied to magic link request and other sensitive routes.

## Database

- Migrations: `0001_core_schema.sql`, `0002_auth_tokens_audit.sql`, `0003_csv_preview_tokens_and_magic_send_log.sql`, `0004_submissions_form_data.sql`, `0005_delegate_and_club_umpires.sql`, `0006_league_fixtures_and_team_mapping.sql`.
- Run in order (e.g. on app startup or via a migration runner). Requires `DB_DSN` (e.g. `postgres://user:pass@host:5432/dbname?sslmode=disable`).

## Running

### Quick start (local test with Docker Desktop)

1. Copy `.env.example` to `.env` and set **`SESSION_SECRET`** (long random string).
2. From repo root: **`docker compose up -d`** (or `docker compose up` to see logs).
3. Wait for logs: `migrations completed` and `listening on :8080`.
4. Open **http://localhost** — use **Club ID 1**, **Team ID 1** to request a magic link; get the link from app logs (`docker compose logs app`). Then admin: **http://localhost/admin/login** with **admin** / **admin123**; 2FA code appears in logs if SMTP is not set.

Full step-by-step: **[docs/TEST-SCENARIO.md](docs/TEST-SCENARIO.md)**.

### General

- **With Docker**: set `MIGRATE=1` and `SEED=1` in the app service to run migrations and seed on startup (already set in `docker-compose.yml` for local). App listens on `:8080`; Caddy on 80/443.
- **Without Docker**: run Postgres, set `DB_DSN`, `APP_ENV=dev`, `SESSION_SECRET`; run migrations manually or with `MIGRATE=1` and `MIGRATE_DIR=migrations`; then `go run ./cmd/app`.

## Environment

- `DB_DSN` — PostgreSQL connection string (required).
- `APP_HTTP_ADDR` — e.g. `:8080`.
- `APP_ENV` — `dev` enables magic link printing to stdout instead of email.
- HMAC shared secret and (if used) SMTP/email config as needed for internal endpoints and 2FA.

## Design notes

See [docs/DESIGN.md](docs/DESIGN.md) for magic link TTL/revocation rules, throttle behaviour, and security rationale.

**Full status (done / tested / backlog):** [docs/PROJECT-STATUS.md](docs/PROJECT-STATUS.md).
**Scoring policy:** [docs/SCORING-RULES.md](docs/SCORING-RULES.md).
**Finalization and alerting plan:** [docs/ALERTING-AND-FINALIZATION.md](docs/ALERTING-AND-FINALIZATION.md).
**Sprint implementation checklist:** [docs/IMPLEMENTATION-BACKLOG.md](docs/IMPLEMENTATION-BACKLOG.md).
**League / Play-Cricket API sync and test plan:** [docs/LEAGUE-API-ROADMAP.md](docs/LEAGUE-API-ROADMAP.md).
