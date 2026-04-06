# GMCL Cricket Ground Feedback — Project status

This document summarises **what has been built**, **what has been tested**, and **what remains** for the cricket governing body MVP (captain feedback, admin portal, n8n integration).

Related policy docs:

- [SCORING-RULES.md](SCORING-RULES.md)
- [ALERTING-AND-FINALIZATION.md](ALERTING-AND-FINALIZATION.md)
- [IMPLEMENTATION-BACKLOG.md](IMPLEMENTATION-BACKLOG.md) — sprint-style build checklist

---

## 1. What we have done

### 1.1 Infrastructure & tooling

| Item | Notes |
|------|--------|
| **Go module** | `cricket-ground-feedback`, Go 1.22; dependencies: chi, pgx/v5, uuid, godotenv, golang.org/x/crypto |
| **Docker** | `docker-compose.yml`: app (distroless), Caddy, Postgres 16; DB healthcheck; app waits for healthy DB |
| **App image** | `docker/Dockerfile.app`: copies `/migrations` into image; `go mod tidy` during build; `MIGRATE_DIR=/migrations` |
| **Caddy** | `Caddyfile`: reverse proxy to app on `http://localhost:80` |
| **Env** | `.env.example`: `DB_DSN`, `SESSION_SECRET`, `MIGRATE`, `SEED`, `APP_ENV`, optional SMTP/HMAC |
| **Migrations runner** | `MIGRATE=1` in `cmd/app`: connects, runs `internal/migrate` over `.sql` files in order |

### 1.2 Database (migrations)

| File | Purpose |
|------|---------|
| `0001_core_schema.sql` | Seasons, weeks, clubs, teams, umpires, captains, submissions, drafts |
| `0002_auth_tokens_audit.sql` | Magic link tokens, admin users, 2FA codes, ranking/report config, reminder_schedules, audit_logs, ai_summaries |
| `0003_csv_preview_tokens_and_magic_send_log.sql` | `csv_preview_tokens`, `magic_link_send_log` |
| `0004_submissions_form_data.sql` | `submissions.form_data` JSONB for full GMCL questionnaire |

### 1.3 Captain (public) flow

- **`GET /`** — Club ID + Team ID; CSRF; posts to magic link request.
- **`POST /magic-link/request`** — Rate limited; resolves current week from `CURRENT_DATE`; creates token with **next Wednesday 23:59:59 Europe/London** expiry, **7-day cap**, **10-minute floor**; **revokes** prior unused tokens for same captain/week in one transaction; **3/hour** and **10/week** throttling via `magic_link_send_log`; audit; in `APP_ENV=dev` prints magic link to stdout.
- **`GET/POST /magic-link/confirm`** — Human “Continue” page; POST redeems token; `Cache-Control: no-store`; captain session cookie **Path=/captain**, HMAC-signed, 2h.
- **`GET /captain/form`** — **2025 GMCL Captain’s Report** questionnaire (`internal/httpserver/captain_form_gmcl.go`):
  - **Section 1:** Match date, club/team/captain name & email (read-only where appropriate), umpire 1/2 names, optional other names, team (1st–5th XI), umpire performance (Poor/Average/Good), comments + optional detail; HTMX autosave on textareas.
  - **Section 2:** Unevenness of bounce, seam movement, carry/bounce, turn (6-level scales each).
- **`POST /captain/form/autosave`** — Saves full form snapshot to `drafts.draft_data` (JSON).
- **`POST /captain/form/submit`** — Validates required fields; stores **`form_data`** JSON; maps pitch criteria to legacy `pitch_rating` / `outfield_rating` / `facilities_rating` (1–5); `match_date` from form; clears draft; clears session cookie (note: Path may still need alignment with `/captain` — see backlog).

### 1.4 Admin portal

- Login + **2FA** (email code; dev fallback logs to stdout if SMTP unset).
- **Dashboard** — Current week summary (season, week number, submission count); table of last 20 submissions with links to detail; links to weeks, rankings, CSV upload.
- **`/admin/weeks`** — Table of season/week and submission counts.
- **`/admin/submissions/{id}`** — Club, team, captain, match date, legacy ratings, comments, **GMCL form_data** breakdown when present.
- **`/admin/rankings`** — Average pitch rating by club.
- **CSV captains** — Preview/apply with DB-backed single-use preview tokens, audit, formula-injection checks.
- CSRF + rate limiting on admin routes.

### 1.5 Internal (n8n) endpoints

- **`POST /internal/send-reminders`** — HMAC-verified.
- **`POST /internal/generate-weekly-report`** — HMAC-verified.
- Audited; idempotent job behaviour as designed.

### 1.6 Security & middleware

- Security headers, request logging, CSRF (fixed token shadowing bug), rate limits, HMAC for internal routes (nonce + body limits).

### 1.7 Supporting packages

- **`internal/auth`** — Magic token create/consume/revoke; admin password hash (bcrypt); 2FA code flow.
- **`internal/timeutil`** — `NextWednesdayExpiry` for magic links.
- **`internal/migrate`** — Ordered execution of SQL migration files from an `fs.FS`.
- **`internal/seed`** — When `SEED=1`: initial season/week/club/team/captain + admin user (`admin` / `SEED_ADMIN_PASSWORD` default `admin123`); **first-time only** adds Greenfield CC & Riverside CC with teams/captains and **8 sample submissions** with `form_data` (only if that week has zero submissions).

### 1.8 Documentation

- `README.md` — Stack, features, quick start, env.
- `docs/DESIGN.md` — Magic link rules, security rationale, known gaps (partially superseded by this file for “what’s left”).
- `docs/TEST-SCENARIO.md` — Step-by-step local Docker test (captain + admin).

### 1.9 Fixes applied during development

- **go.mod** — Removed invalid separate `require` lines for `pgx/v5/pgxpool` and `pgx/v5/stdlib` (same module as `pgx/v5`).
- **Docker build** — Added `go mod tidy` in Dockerfile so `go.sum` is generated in-container when absent on the host.

---

## 2. What has been tested (reported / documented)

| Area | What was verified |
|------|-------------------|
| **Docker** | `docker compose up -d --build` after go.mod + Dockerfile fixes; images build; stack starts |
| **Migrations** | App logs `migrations completed` when `MIGRATE=1` |
| **Admin auth** | User reported **all logins work** (password + 2FA with dev log fallback) |
| **Captain flow** | Documented in `TEST-SCENARIO.md`: magic link from logs, confirm, form, submit |
| **GMCL form** | Implemented and wired to `form_data` + admin detail view |
| **Admin UI with data** | Seed creates 8 test submissions; dashboard and submission detail show data after fresh `docker compose down -v` + up |
| **n8n / internal HMAC** | **Not** part of routine testing (no signed requests in test scenario) |

Automated test suite / CI (if present) should be run separately; this list reflects **manual** and **documented** verification.

---

## 3. What is left to complete

### 3.1 High value / product gaps

| Item | Description |
|------|-------------|
| **Real email** | SMTP for magic links, 2FA codes, and “copy to captain email” on submit (currently dev log / TODO). |
| **n8n integration** | End-to-end test of HMAC-signed calls to `/internal/*`; production secrets and monitoring. |
| **Public UX** | Replace numeric Club ID / Team ID with searchable club/team picker (names, not IDs). |
| **Umpire linkage** | Form collects umpire names as text; optional future link to `umpires` table / appointments URL workflow. |
| **Mandatory comments for low scores** | GMCL rule: comments required when umpire score ≤ 2 — add client + server validation. |
| **Form copy** | Match full Google Form wording/help text and ECB criteria descriptions if legal/comms require 1:1 parity. |

### 3.2 Admin & operations

| Item | Description |
|------|-------------|
| **“No active week” banner** | Prominent admin warning when no week spans `CURRENT_DATE`. |
| **Admin lockout** | After N failed logins (e.g. 10), 15 min lockout; clear on successful 2FA; audit. |
| **CSV improvements** | Bulk club/team lookups; tighter rate limit on `/admin/csv/*`; shared `normName` for CSV vs DB. |
| **Week drill-down** | Link from `/admin/weeks` to a list of submissions for that week (if not already sufficient). |
| **AI summaries / reports** | `ai_summaries`, `report_config` — wire UI and n8n jobs beyond MVP stubs if required. |

### 3.3 Captain / session hygiene

| Item | Description |
|------|-------------|
| **Cookie Path on logout** | Clear captain session with **Path=/captain** to match set cookie (see `DESIGN.md`). |
| **Draft hydration** | Server-side pre-fill of all GMCL fields from `draft_data` (partial today: mainly textareas + some selects). |

### 3.4 Engineering & polish

| Item | Description |
|------|-------------|
| **go.sum on host** | Run `go mod tidy` locally and commit `go.sum` for reproducible non-Docker builds. |
| **docker-compose `version`** | Remove obsolete `version:` key to silence Compose warning. |
| **README** | Update migration list to include `0004`; mention `internal/migrate`, `internal/seed`, GMCL form. |
| **Tests / CI** | Expand unit/integration tests; pipeline (lint, test, build image) if not fully green. |
| **Templ + Tailwind** | README mentions templ/Tailwind; much UI is raw HTML — align or simplify README. |

### 3.5 Privacy / compliance (if required for go-live)

- Retention policies, DPIA notes, cookie consent if tracking added, secure SMTP (TLS).

---

## 4. Quick reference — env vars

| Variable | Role |
|----------|------|
| `DB_DSN` | Postgres connection string |
| `SESSION_SECRET` | Captain (and admin) session signing |
| `MIGRATE=1` | Run SQL migrations on startup |
| `MIGRATE_DIR` | Default `/migrations` in Docker, `migrations` locally |
| `SEED=1` | Seed baseline + test data on first empty DB |
| `SEED_ADMIN_PASSWORD` | Admin password (default `admin123`) |
| `APP_ENV=dev` | Log magic link URL to stdout |
| `SMTP_*` | Real email when configured |
| `HMAC_SECRET` (or as per `middleware/hmac`) | Internal endpoint signing |

---

*Last updated to reflect: GMCL questionnaire, `form_data`, admin dashboard with data, seed test submissions, migration 0004, Docker/go.mod fixes.*
