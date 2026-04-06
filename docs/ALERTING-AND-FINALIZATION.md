# Alerting and Finalization Plan

Implementation plan for Thursday finalization, missing-submission alerts, and n8n-triggered reporting.

## Finalization schedule

- Timezone: `Europe/London`
- Run time: **Thursday 00:01**
- Trigger source: n8n scheduler calling app internal endpoint (HMAC-protected)

## End-to-end flow at 00:01

1. n8n calls app endpoint (e.g. `/internal/finalize-week`) with HMAC headers.
2. App resolves active week (or explicit week id payload).
3. App computes/finalizes scores for that week.
4. App detects missing required submissions and creates alerts.
5. App returns summary payload:
   - finalized score count
   - incomplete score count
   - alert count
6. n8n uses summary + app data endpoint(s) to generate AI narrative report and distribute.

## Proposed schema additions

## 1) Weight configuration

`score_weight_configs`

- `id` (pk)
- `season_id` (fk)
- `effective_from_week_id` (fk)
- `home_weight` numeric(5,4)
- `away_weight` numeric(5,4)
- `umpire_weight` numeric(5,4)
- `created_by_admin_id` (fk)
- `created_at` timestamptz
- optional: `is_active` bool

Constraints:

- each weight between 0 and 1
- sum equals 1 (enforced by check or service validation)

## 2) Computed scores

`computed_week_scores`

- `id` (pk)
- `season_id`, `week_id`, `team_id`
- `score_value` numeric(6,3)
- `components_json` jsonb (raw component values by source)
- `weights_json` jsonb (weights used at calc time)
- `missing_sources` jsonb (array)
- `is_incomplete` bool
- `status` enum (`provisional`, `final`, `overridden`)
- `calculation_version` int
- `calculated_at` timestamptz
- `finalized_at` timestamptz nullable

Unique key:

- `(season_id, week_id, team_id)`

## 3) Missing-submission alerts

`submission_alerts`

- `id` (pk)
- `season_id`, `week_id`, `team_id`
- `alert_type` text (`missing_submission`)
- `missing_sources` jsonb
- `status` enum (`open`, `acknowledged`, `resolved`)
- `created_at` timestamptz
- `acknowledged_at` timestamptz nullable
- `resolved_at` timestamptz nullable
- `resolved_by_admin_id` nullable fk
- `notes` text nullable

## 4) Score overrides

`score_overrides`

- `id` (pk)
- `computed_score_id` fk
- `old_score` numeric(6,3)
- `new_score` numeric(6,3)
- `reason` text (required)
- `overridden_by_admin_id` fk
- `created_at` timestamptz

## API endpoints (app)

Internal (HMAC):

- `POST /internal/finalize-week`
  - body: `{ "season_id": optional, "week_id": optional, "dry_run": optional }`
  - action: calculate + finalize + alerts
  - response: counts + status

Admin:

- `GET /admin/weights` + `POST /admin/weights`
- `GET /admin/scores?season=&week=&status=&missing=`
- `GET /admin/alerts?status=open`
- `POST /admin/alerts/{id}/ack`
- `POST /admin/alerts/{id}/resolve`
- `POST /admin/scores/{id}/override`

Read endpoints for n8n reporting:

- `GET /internal/report-data?season_id=&week_id=` (HMAC-protected)
  - includes finalized scores, missing alerts, trend slices

## Admin UX requirements

- Weights page:
  - show current and upcoming effective config
  - enforce sum=1 with inline validation
- Scores page:
  - badges: `final`, `provisional`, `overridden`, `missing source`
- Alerts page:
  - open queue by week/division/club/team
  - ack/resolve workflow with notes
- Score detail:
  - component breakdown
  - weights used
  - override history and reason

## Audit requirements

Write audit events for:

- weight config changes
- score calculation runs
- week finalization start/end
- alert create/ack/resolve
- score override (with reason)

Each event should include:

- actor type/id
- request id
- season/week/team identifiers
- before/after values where relevant

## n8n AI reporting role

n8n should:

1. Trigger finalization endpoint at Thursday 00:01.
2. Fetch reporting data from app endpoint(s).
3. Generate AI narrative summary.
4. Distribute output (email/slack/file) and record delivery status.

The app should remain the authoritative source of raw submissions and computed/finalized scores.

## Rollout sequence

1. Add migrations for the 4 new tables.
2. Implement score calculator service in app (pure function + tests).
3. Implement finalization endpoint and alert generation.
4. Add admin UI (weights, scores, alerts, override).
5. Add n8n workflow for scheduled finalization + AI report.
6. Run shadow mode for 1-2 weeks (compute but do not publish as final).
7. Go live and monitor.

