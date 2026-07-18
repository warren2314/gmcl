# Sanctions programme requirements audit

Audit source: `Description of Penalties – Current Process` and the agreed
`GMCL Sanctions and Case Management Programme` implementation plan.

Last reviewed: 19 July 2026.

This is a release-gate document. `Partial` and `Missing` items must not be
described as complete, and automatic approval must remain disabled until the
acceptance gates are evidenced.

## Current process requirements

| Requirement | Status | Evidence / remaining work |
|---|---|---|
| Captain reports can create card candidates | Implemented | Captain-report/non-submission candidates use the case service and the common card calculator. |
| Disciplinary, ineligible-player, grounds/facilities, forfeiture and Play-Cricket intake | Partial | Public/manual case intake supports each source. Grounds ratings create review cases only for explicit unfit criteria. Automated Play-Cricket and forfeiture findings are not yet end-to-end. |
| Three yellows convert to a red; remaining yellows carry | Implemented | `internal/sanctions/policy.go` and append-only ledger. |
| Red ordinal determines card-system points | Implemented | The calculator uses the team red ordinal. |
| Third club red creates Board-intervention work | Implemented | Approval creates a `board_intervention` follow-up task managed from the immutable follow-up queue. |
| Per-match maximum | Implemented | Enforced by the common calculator. |
| Direct, suspended and rescindable sanctions | Partial | Calculation and storage exist. Activation, expiry and remedy commands/screens are missing. |
| Flexible annual rule numbers and historical snapshots | Partial | Decisions link to effective-dated policy/rule snapshots. There is no sanctions policy-version administration screen or rule-snapshot validation gate. |
| Fines and player bans do not enter card totting | Implemented | `counts_for_totting` is false unless the effect is a card. |
| Notices to captain, Executive, Play-Cricket and finance where applicable | Partial | Publish creates immutable outbox records and conditional recipients. Template/routing rules are still partly hard-coded; delivery correlation, bounce handling and attachment hashes are incomplete. |
| Match-forfeit letterheaded document | Missing | No forfeiture notice/document generator is present. |

## Data, audit and correction requirements

| Requirement | Status | Evidence / remaining work |
|---|---|---|
| Stable case references | Implemented | `GMCL-YYYY-NNNNNN` references are used by cases, notices, public detail and captain lookup. |
| Append-only case events, decisions, effects and card ledger | Implemented | Database triggers reject update/delete and corrections append events/revisions. |
| Every human edit stores actor, time, reason, request ID and before/after | Implemented for case management; migration completed for legacy ledger | `0039_legacy_sanction_event_immutability.sql` closes the transitional legacy-edit gap. |
| No destructive clear-all | Implemented | The operation returns `410 Gone`; its obsolete button has been removed. |
| Immutable sent-email snapshots | Partial | Recipient, subject, body and attempts are retained. Provider message ID, delivery/bounce correlation and attachment hashes are incomplete. |
| Evidence retention leaves tombstones | Partial | Immutable metadata/tombstone tables exist, but there is no authorised redaction/expiry command or retention worker. |

## Workflow and access requirements

| Requirement | Status | Evidence / remaining work |
|---|---|---|
| Verified public reports, rate limits, consent and evidence validation | Partial | Verification, rate limits and validated uploads exist. Turnstile/stronger anti-abuse and explicit consent wording are not complete. |
| Time-limited isolated club response links | Implemented | Hashed, expiring, case-scoped links and immutable response events. |
| Two-person proposal and approval | Implemented | Separation of duties is enforced; emergency override is super-admin only and recorded. |
| Granular sanctions permissions | Implemented | Permission catalogue and route checks exist. |
| Follow-up work for points, finance, Board, suspension, appeal and expiry | Implemented | Tasks are created and managed through an attributed, append-only task-event queue. |
| Appeals | Partial | Secure appeal submission and overturn/reversal exist. A full appeal workflow with deadlines, review outcome and notices is incomplete. |

## Public register and captain assistant

| Requirement | Status | Evidence / remaining work |
|---|---|---|
| Canonical public register at `sanctions.gmcl.co.uk` | Implemented | The hostname root and `/sanctions` render the register; navigation exposes it. |
| Active default, archive, season/subject/type filters | Implemented | Ended bans are excluded from the default and remain in the archive. |
| Card balance, red count, points and threshold | Implemented | Public card rows show ledger balance and threshold; case detail shows public consequences. |
| No evidence, correspondence, contacts or internal notes exposed | Implemented | Public queries select approved public fields only. |
| Authenticated captain own-team lookup; public rules-only | Implemented but hidden pending A1 testing | Deterministic lookup is scoped to the captain session. The A1 interface remains disabled as requested. |
| Old public register redirects/links to canonical register | Missing outside this repository | The `gtrmcrcricket.co.uk` page still needs its CMS link/redirect changed after reconciliation. |

## Migration and rollout

| Requirement | Status | Evidence / remaining work |
|---|---|---|
| Fetch existing live individual bans, team cards and team/cup bans | Implemented | Admin can fetch the published sources without obtaining export files. |
| Immutable source snapshots, checksums and row provenance | Implemented | Batches and rows retain files, hashes, URLs and row data. |
| Preview, deterministic mapping and exception queue | Implemented | Exception corrections retain the source row and append an attributed before/after mapping event. |
| Preserve historical outcomes without silent recalculation | Implemented | Imports store source outcomes and use `import` ledger entries. |
| 100% reconciliation and named sign-off | Not yet achieved | Three staged batches still require human apply; unmatched rows require resolution and sign-off. |
| Shadow/manual/automatic per deterministic source and kill switch | Implemented | Automatic mode requires three clean cycles; judgement sources are not automatic. |
| Three clean weekly cycles and notice reconciliation | Not yet achieved | This is an operational launch gate, not a code-only assertion. |
| DNS/TLS/reversible cutover | Implemented, pending data approval | DNS points to the GMCL server and Caddy serves TLS. No legacy CMS redirect has been made. |

## Test and launch gaps

The policy unit suite covers third-yellow conversion, ordinal red points,
suspended behaviour, match maximum and rescindable yellows. Still required:

- database integration tests for immutable triggers and projection rebuilds;
- reversal/recalculation and suspended activation/expiry workflow tests;
- isolation and upload-security tests for public/captain case access;
- outbox retry, bounce, duplicate and partial-delivery tests;
- migration reconciliation fixtures and named sign-off records;
- three clean manual weekly cycles before any automatic approval.
