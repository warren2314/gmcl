# Scoring Rules and Governance

This document is the policy source for weighted scoring, missing-source handling, finalization, and admin overrides.

## Locked business rules

- **Weights are admin-managed** in the app (configurable, validated, audited).
- **Missing source behavior**: if one or more sources are missing, still compute using currently configured weights and clearly flag missing sources.
- **Finalization time**: Thursday **00:01 Europe/London**.
- **Non-submission alerting**: after finalization, any team without required submission(s) is flagged to admin.
- **Admin override**: allowed, but reason is mandatory and must be audited.
- **AI reporting owner**: n8n orchestrates AI report generation; app remains source-of-truth for validated data and score calculations.

## Source types

Primary source types for a fixture/week score:

- `home_captain`
- `away_captain`
- `umpire`

Default initial weights (editable by admins):

- `away_captain`: `0.50`
- `home_captain`: `0.25`
- `umpire`: `0.25`

## Required submissions policy

Recommended default (can be changed by governance decision):

- Required: `home_captain` + `away_captain`
- Optional (but used if present): `umpire`

If governance later requires umpire submission as mandatory, update required-source configuration and alert logic.

## Calculation behavior

For each team/week or fixture/week score:

1. Collect available source submissions.
2. Apply configured weights for the week.
3. Compute weighted score from available values.
4. Persist:
   - component values
   - weights used
   - missing sources list
   - completeness/provisional status
   - calculation version

### Missing-source handling

- Do not block score creation because of a missing source.
- Mark score as incomplete/provisional until finalization.
- Expose missing sources in admin list/detail views and exports.

## Finalization policy

At **Thursday 00:01 Europe/London** for each active week:

1. Freeze scores as `final`.
2. Recalculate one final time with latest submissions before lock.
3. Create missing-submission alerts for teams with required sources missing.
4. Write audit events for finalization and alerts.

After finalization:

- Scores are read-only except through explicit admin override.
- Late submissions should be visible but should not silently mutate finalized score unless an override/reopen flow is used.

## Override policy

Admins may override a score if justified:

- Override form requires non-empty reason (min length suggested: 20 chars).
- Persist both old and new values.
- Record who overrode and when.
- Display override badge and reason in score detail/report exports.
- Emit audit log event with rationale metadata.

## Ownership split: app vs n8n

### In app (authoritative)

- Validation, persistence, scoring math, finalization state, overrides, alerts, audit logs.

### In n8n (orchestration)

- Scheduled trigger at Thursday 00:01 (or calls app endpoint that enforces this).
- AI narrative/report generation from app-provided data.
- Notification fan-out (email/slack/etc) for missing submissions and weekly packs.

Rule: n8n should not become the canonical scorer; it may trigger scoring/finalization endpoints but business logic remains in app code.

## Data to surface in admin UI

- Current weights in effect and history.
- Source completeness per team/week.
- Finalization status and timestamp.
- Missing-submission alert queue (`open`, `acknowledged`, `resolved`).
- Override history per score.

## Change control

Any change to weights, required sources, or finalization time requires:

1. Admin action in UI (or controlled migration/seed for bootstrapping),
2. Audit record,
3. Effective week metadata.

