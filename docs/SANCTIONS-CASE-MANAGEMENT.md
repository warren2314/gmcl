# Sanctions and case management

This subsystem replaces the sanctions Google Forms/spreadsheets with an
effective-dated, data-driven workflow. It deliberately starts in shadow mode.
No deterministic source can become automatic until it has three recorded clean
reconciliation cycles and a super-admin explicitly changes its mode.

## Entry points

- Public register: `https://sanctions.gmcl.co.uk/sanctions`
- Verified public report: `/sanctions/report`
- Secure party response: `/sanctions/case/respond?token=...`
- Captain own-team history: `/captain/discipline`
- Admin case queue: `/admin/cases`
- Safety modes and kill switch: `/admin/cases/automation`
- Role-based notice recipients: `/admin/cases/recipients`
- Immutable historical CSV staging: `/admin/cases/imports`
- HMAC-protected outbox worker: `POST /internal/process-sanction-outbox`

The root of `sanctions.gmcl.co.uk` redirects to the register. The same routes
also remain reachable through the main application host for operational
fallback.

## Data and immutability

Migration `0038_sanctions_case_management.sql` adds cases, parties, secure
tokens, evidence metadata, decision/effect revisions, the card ledger,
follow-up tasks, notification policies/outbox/attempts, automation settings,
configuration history, and import staging.

Events, evidence metadata, policy versions, decision/effect revisions, card
ledger entries, notice attempts, raw import rows, and configuration events have
database triggers rejecting `UPDATE` and `DELETE`. Corrections and reversals
are new revisions or compensating ledger entries. `sanction_cases` is a mutable
current-state projection and is never the sole audit record.

Evidence and original import exports are held in the persistent Docker volume
`sanction_data`, under `/app/data`. Configure these paths with:

- `SANCTIONS_EVIDENCE_DIR`
- `SANCTIONS_IMPORT_DIR`
- `SANCTIONS_BASE_URL`

Do not put either directory in the image or Git. Back up the volume together
with Postgres; evidence rows store SHA-256 checksums for verification.

## Card policy

All manual, bulk, and scheduled captain-report proposals call
`internal/sanctions.Calculate`. The policy version effective on the offence
date supplies the yellow threshold, per-match red maximum, and club Board
threshold. Approved card decisions append balance deltas to
`sanction_card_ledger_entries`; a third-yellow conversion uses a `-2` yellow
delta and `+1` red delta, preserving all three source decisions while leaving a
zero current yellow balance.

Legacy `sanctions` rows remain temporarily as a compatibility projection for
existing reports. New compatibility rows carry `case_id` and are excluded from
opening-balance queries, preventing double counting. The old bulk-delete route
is removed and its handler returns HTTP 410.

## Approval and automation

Ordinary admins need explicit `sanctions_*` permissions. Super-admins grant
these from the Admin Users page. The proposer and approver must differ. A
same-user emergency approval is restricted to a super-admin, requires a reason,
and is flagged in the immutable timeline.

The scheduled captain-report source behaves as follows:

- `shadow`: creates a triage finding only;
- `manual`: creates a proposed decision awaiting independent approval;
- `automatic`: proposes, system-approves and publishes only after three clean
  cycles, source enablement, and the global kill switch all permit it.

Judgement sources are not present in the automation settings and therefore
cannot use automatic approval.

Publication resolves the active versioned notification policy into immutable
outbox rows. Captains are resolved from the affected team. Executive,
discipline, finance, and Play-Cricket recipients come from the recipient
directory; finance is included for fines and Play-Cricket for points effects.
The n8n workflow processes pending messages every five minutes. Idempotency is
per case, decision revision, and recipient.

## Historical migration procedure

1. Export each Google Form/sheet or existing public table separately as CSV.
2. Upload each original export through `/admin/cases/imports`, recording its
   source name and URL. The original bytes and SHA-256 checksum are retained.
3. Reconcile every pending row to a club, team/player, season, rule snapshot,
   effect, and status. Mark duplicates and source defects explicitly; do not
   infer ambiguous mappings.
4. Apply reconciled rows as `historical_import` cases and immutable import
   ledger entries. Historical outcomes are preserved as recorded rather than
   recalculated under the current policy.
5. Compare per-team/per-season yellows, reds, points, active bans, fines, and
   carry-over balances against the source sheets. Record named sign-off and any
   remaining exception.
6. Run at least three weekly shadow/manual cycles before enabling automatic
   captain-report decisions. Then freeze the legacy Google assets as read-only
   archives and update the old website to link to the canonical register.

The repository provides lossless staging and provenance. Actual source exports
and ambiguous mapping decisions are operational inputs and are intentionally
not embedded in code.

## Deployment checklist

1. Back up Postgres and the `sanction_data` volume.
2. Deploy migration 0038 with `MIGRATE=1`.
3. Add the `sanctions.gmcl.co.uk` DNS record to the existing server and confirm
   Caddy has issued TLS for it.
4. Grant minimum sanctions permissions and configure role recipients.
5. Keep `_global`, `captain_report`, and `play_cricket` in shadow/manual mode.
6. Import and reconcile history, then compare the new public register with the
   existing live page before changing its link.
7. Import/activate the updated n8n workflow and verify the HMAC secret.
8. Test a non-production case through report, response, proposal, independent
   approval, publication, outbox delivery, captain lookup, appeal, and reversal.

