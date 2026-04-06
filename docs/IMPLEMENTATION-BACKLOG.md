# Implementation backlog (sprint-style)

Checklist derived from [SCORING-RULES.md](SCORING-RULES.md) and [ALERTING-AND-FINALIZATION.md](ALERTING-AND-FINALIZATION.md). Use this as the build contract; tick items as you ship.

---

## Sprint 0 — Preconditions and data model foundations

**Goal:** You can identify home vs away and attach submissions to a fixture/week before weighted scoring is meaningful.

- [ ] **Fixtures / matches** — Add `fixtures` (or equivalent): `season_id`, `week_id`, `home_team_id`, `away_team_id`, `match_date`, optional `division_id` / metadata.
- [ ] **Submission role** — Extend submissions (or parallel table) with `submission_source` enum: `home_captain`, `away_captain`, `umpire` (and validation rules per source).
- [ ] **Captain form context** — When opening the form from magic link, derive **home vs away** from fixture + team so weights apply to the correct source (not “generic team”).
- [ ] **Umpire capture path** — Decide MVP: in-app umpire form vs ingest later; minimum is a way to store `umpire` source rows linked to the same fixture.
- [ ] **Document required sources** — Config table or env-backed defaults: which sources are **required** for alerts (default: home + away captains per [SCORING-RULES.md](SCORING-RULES.md)).

---

## Sprint 1 — Schema: weights, computed scores, alerts, overrides

**Goal:** DB supports config, computed rows, alerts, overrides.

- [ ] **Migration** — `score_weight_configs` (`season_id`, `effective_from_week_id`, `home_weight`, `away_weight`, `umpire_weight`, `created_by_admin_id`, `created_at`, constraints sum=1, range 0–1).
- [ ] **Migration** — `computed_week_scores` (unique `(season_id, week_id, team_id)` or fixture-level key if you score per fixture instead of per team — align with product).
- [ ] **Migration** — `submission_alerts` (`alert_type`, `missing_sources`, `status`, timestamps, `resolved_by_admin_id`, `notes`).
- [ ] **Migration** — `score_overrides` (`computed_score_id`, old/new score, `reason`, `overridden_by_admin_id`, `created_at`).
- [ ] **Enums** — `computed_score_status`: `provisional`, `final`, `overridden`; `alert_status`: `open`, `acknowledged`, `resolved`.
- [ ] **Seed / bootstrap** — Insert default weight row for current season (home `0.25`, away `0.50`, umpire `0.25` per [SCORING-RULES.md](SCORING-RULES.md)) for dev only; prod via admin UI.

---

## Sprint 2 — Scoring engine (app code, testable)

**Goal:** Pure calculation + persistence; no UI yet.

- [ ] **`internal/scoring` (or similar)** — Function: inputs = component scores by source + weight config + required-sources list; outputs = `score_value`, `missing_sources`, `is_incomplete`, `weights_json` snapshot.
- [ ] **Unit tests** — All present; one missing (home); one missing (umpire); all missing edge cases; weights sum validation.
- [ ] **Recalculation** — Service to upsert `computed_week_scores` for all teams/fixtures in a week (`provisional` until finalization).
- [ ] **Versioning** — Increment `calculation_version` when formula changes; store version in row metadata.

---

## Sprint 3 — Internal API (HMAC) for n8n

**Goal:** n8n can finalize at **Thursday 00:01 Europe/London** and pull report payloads.

- [ ] **`POST /internal/finalize-week`** — Body: optional `season_id`, `week_id`, `dry_run`. Steps: resolve week → recompute → set `final` + `finalized_at` → create/update `submission_alerts` for missing required sources → return JSON summary (counts, errors).
- [ ] **`GET /internal/report-data`** — Query: `season_id`, `week_id`. Response: finalized scores, components, missing alerts, club/team labels, trend slice if defined (HMAC-protected).
- [ ] **Idempotency** — Finalize safe to retry (same week → no duplicate alerts or use upsert + stable keys).
- [ ] **Audit** — Log `internal_finalize_week`, `internal_report_data` with metadata (week id, counts, dry_run).
- [ ] **Wire router** — Mount under existing internal mux + `HMACVerifier` in [router.go](../internal/httpserver/router.go).

---

## Sprint 4 — Admin UI and governance

**Goal:** Ops can manage weights, see scores, handle alerts, override with reason.

- [ ] **`GET/POST /admin/weights`** — List current + history; create new config with `effective_from_week_id`; validate sum=1; CSRF + audit.
- [ ] **`GET /admin/scores`** — Filters: season, week, status, missing-only; table with badges: `provisional`, `final`, `overridden`, missing sources.
- [ ] **`GET /admin/scores/{id}`** — Detail: components, weights used, missing list, override history, link to related submissions.
- [ ] **`GET /admin/alerts`** — Filter `status=open`; sort by week/club.
- [ ] **`POST /admin/alerts/{id}/ack`** and **`/resolve`** — Notes optional on ack; audit.
- [ ] **`POST /admin/scores/{id}/override`** — Require `reason` (min length e.g. 20); update score row status `overridden`; insert `score_overrides`; audit.
- [ ] **Admin user management** (if not done) — Create/deactivate admin, role flag (`super_admin` for weights?), reset 2FA; all audited.

---

## Sprint 5 — n8n workflows (scheduler + AI reporting)

**Goal:** Thursday 00:01 automation and AI narrative from app data.

- [ ] **Cron workflow** — Trigger **Thursday 00:01 Europe/London** → `POST /internal/finalize-week` with HMAC.
- [ ] **Branching** — On success → `GET /internal/report-data` → AI node (OpenAI/other) → email/Slack/drive output.
- [ ] **Failure handling** — Retry, alert ops if finalize fails; log n8n execution id in app audit optional.
- [ ] **Secrets** — Store `N8N_HMAC_SECRET` and AI keys in n8n credentials; never in repo.

---

## Sprint 6 — Production hardening (parallel or after Sprint 3–4)

**Goal:** Safe to run in prod alongside new features.

- [ ] **Disable seed in prod** — `SEED` off; no test admin in production deploy templates.
- [ ] **Migrations** — Run via deploy job, not only `MIGRATE=1` on every replica (document pattern).
- [ ] **Startup validation** — Fail if `SESSION_SECRET` missing in prod; document required env vars.
- [ ] **DB TLS** — `sslmode=require` (or stricter) for prod `DB_DSN`.
- [ ] **Structured logging** — JSON logs, request_id propagation; no tokens in logs.
- [ ] **Readiness** — `/ready` or extend `/health` with DB ping for orchestrators.
- [ ] **Admin lockout** — Failed login threshold + audit (see [DESIGN.md](DESIGN.md)).
- [ ] **Captain cookie** — Clear session on submit with `Path=/captain` (match set-cookie).

---

## Sprint 7 — Quality and regression

- [ ] **Integration tests** — Finalize-week happy path; missing submission creates alert; override requires reason; weights reject sum ≠ 1.
- [ ] **E2E smoke** — Optional: docker-compose + script hitting internal endpoint with signed request.
- [ ] **CI** — Keep `go test`, gosec, govulncheck green; add packages under test as scoring grows.

---

## Dependency graph (short)

```text
Sprint 0 (fixtures + submission source)
    → Sprint 1 (migrations)
        → Sprint 2 (scoring engine + tests)
            → Sprint 3 (internal finalize + report-data)
            → Sprint 4 (admin UI)
                → Sprint 5 (n8n + AI)
Sprint 6 (hardening) can overlap Sprint 3–5
Sprint 7 (QA) continuous from Sprint 2 onward
```

---

## Definition of done (release gate)

- [ ] Thursday 00:01 n8n run finalizes week in staging without duplicate alerts.
- [ ] Admin can change weights; next provisional recalculation uses new config from effective week onward.
- [ ] Missing required submissions visible in admin alerts after finalization.
- [ ] Override requires reason and appears in audit + score detail.
- [ ] AI report generated from `report-data` only (no DB access from n8n).

---

*Update this file as scope changes; keep [SCORING-RULES.md](SCORING-RULES.md) as the policy source of truth.*
