# Current admin workspace: first implementation slice

Date: 15 October 2026. Updated: 16 October 2026. Status: isolated local staging and release checks passed; candidate prepared for review; not released.

Review update, 16 October 2026: the project owner accepted the presented Home, case-list and case-workflow layouts and confirmed that the season is closed with no current real cases. Use synthetic cases to finish workflow validation now. Empty active queues must remain distinct from unavailable data, retained history and unfinished follow-up. Reconcile the first real next-season cases when they exist; this is a future operating check, not a reason to wait before testing.

Closed-season follow-up: synthetic verification now covers the actual signed-in Home, case list and case-detail pages. It exposed two local issues, both corrected before release: Home could select next season's future reporting instead of the last completed week, and investigator-only accounts could encounter an unavailable My work query. History and delivery scope labels now describe their filtered populations.

This slice follows the next-season assessment and prioritises Home, ineligible-player casework and case detail. Joe's junior portal requirements remain in the next-season plan and are not implemented by this change.

## Baseline and scope

- Started from commit `44e4c4c85` in a separate managed worktree, preserving the original checkout and its untracked migrations, corrections and planning files.
- Reconciled with upstream master at `0c40366aa03060e8fda693f09d45eb52d5ebe48b`, including upstream umpire corrections in migrations `0091`–`0093`. The complete migration set now contains 97 files. This workspace change introduces no migration of its own.
- Local branch: `codex/admin-ineligible-workspace`. Changes are available for review in the attached worktree.
- Keep the current Go/PostgreSQL application. No database migration or new infrastructure is required.
- No production records, permissions, recipients, scheduled jobs or sending rules were changed. Nothing has been pushed or deployed.

## Delivered behaviour

Home puts personal reply, decision and task work first, with assigned ineligible cases and training in separate disclosures. Assigned counts and the existing personal index use the same ineligible-only population. Open tasks remain visible on completed cases and explain future effective dates. League totals are separate and overlapping measures are not added together.

Home's reporting context chooses an active week, then the most recently completed week, then an upcoming week only if no completed period is available. Configuring next season therefore does not replace completed-season reporting with future zeros. Planning and submission policies and explicit archive exclusions retain their existing behaviour.

Weekly reporting distinguishes required, received, exempt and missing obligations. Empty rating samples and failed queries are explicit rather than displayed as zero. Season detail and recent submissions remain available.

The season-statistics link opens the actual disclosure. Rankings, legacy ledger and weekly-record drilldowns carry Home's reporting season; weekly details and their filter/back links retain that explicit season. Other pages keep their existing default selection policy.

The ineligible workspace separates Reports to review, Cases in progress and Case history. The clean workspace URL opens cases; existing report bookmarks still open reports. New unassigned reports remain discoverable. Case lists start from cases, so manual cases and split cases are not lost through representative report links.

Case and report results are paginated with an uncapped matching total. Their count and page queries run in one read-only repeatable-read transaction. Case export uses the same population and includes all matching pages, with spreadsheet-safe text cells. Search, fixture dates, ownership, training and return-to-results context are retained where applicable.

Case detail shows a current-record-derived next action and groups background, evidence, delivery, history and maintenance around it. Current owner review, designated ineligible approval and separate final issue remain distinct. Historical replies do not reopen completed work. Changed source evidence uses one domain-owned read predicate in detail and lists, and removes a stale approved case from the final-issue population.

Existing route permissions remain authoritative. The full ineligible workspace and its export remain triage-only; other staff retain existing personal routes. Contextual links do not offer case detail without existing investigation or approval access.

Home, personal assigned cases, reports/case lists and case detail use a capability-aware navbar. Approval-only case readers retain background, evidence metadata and locked decisions while investigator controls remain hidden. A direct case open has a permitted Home fallback when the viewer cannot open the full cases index. These presentation changes do not add grants.

## Verification

- Full `go test ./...`, `go vet ./...` and `go build ./...` passed against the isolated implementation.
- Selected integration tests used a fresh, disposable localhost PostgreSQL database with session-local synthetic tables. They cover unique case populations, manual/split links, training, completed and withdrawn work, permissions, response deadlines, current proposal revisions, source-review parity, and count/list/export reconciliation.
- A 2,065-report fixture verifies totals beyond the old 2,000-row cap, stable pages and filtered history/training/deferred populations.
- Offline Chrome checks at 1440px and 390px cover production Home/list renderers and eight case workflow component states. No horizontal overflow, broken images or browser script errors were found; native disclosure controls opened.
- All 97 unchanged repository migrations ran through the actual migration runner in fresh disposable schemas, with the real pgvector extension and HNSW index. No migration statements or embedding types were substituted.
- Actual authenticated GET handlers passed against that migrated schema with invented records: owner replies and saved audience notices, current-revision owner review, designated same-proposer approval, separate final issue and locked notices, changed-source review, completed-case tasks/delivery, authentication/permission denials, CSRF denial and retained back-to-results context. GET and rejected POST requests queued no messages.
- Closed-season checks confirm zero active cases and personal action queues while retaining historical cases, delivery exceptions and future-effective supporting tasks. Unavailable reads remain distinct from zero; reporting retains completed-season context when next season is configured.
- Fifteen actual authenticated pages passed 30 offline desktop/mobile checks, including closed-season Home and next-action links revealing collapsed sections. No page overflow, broken images, missing next-action targets or script errors were found.

- Successful authenticated command tests now cover multipart manual reply recording, idempotent reply review, report linking, proposal effects, three saved audience notices, current versus stale owner-review versions, designated same-proposer approval, separate final issue, assigned follow-up completion and corrected-source reopening/remerge. They inspect the exact locked PDF bytes, queued bodies, effects, ledger and audit events. Missing CSRF, wrong owners, general approvers and duplicate issue remain rejected.
- Real Chrome browser testing uses actual bcrypt login, session/CSRF cookies and production handlers/styles on a loopback server. It has exercised Home disclosure navigation, filtered case opening and return after reply review, report detail, proposal with yellow card/fine/points effects, saving three notices, owner submission, designated approval, separate final issue and the actual follow-up task form. Approval-only/no-grants restrictions and 390px layouts passed.
- Release tooling targets Go 1.26.9 and `golang.org/x/text` 0.41.0 (with required `x/sync` 0.22.0). Configured `gosec` reports zero findings. `govulncheck` reports zero reachable vulnerabilities; advisories in unused paths of required modules are not represented as cleared module advisories. Production Linux/amd64 compilation passes. CI now explicitly creates the separate handler-test database with real pgvector so these migrated action tests execute.

Visual and live fixtures use fictional records. The owner chose isolated local staging. Its dedicated schema, synthetic accounts and `.invalid` recipients are task-local; no senders, importers, schedulers or external API clients run. Approval and final issue create expected synthetic queue records, which is not proof of email delivery. Action tests verify seven exact synthetic outcome recipients and zero delivery attempts.

Local browser login intentionally disables email 2FA only in the fixture. Hosted TLS, reverse-proxy behavior, email 2FA, provider transport, the final production container and live data are not certified by this harness. Local receipt/screenshots and tool logs are under `E:\GMCL\tmp\admin-ineligible-verification`; they are excluded from the candidate.

## Remaining before release

1. Record the immutable candidate and passing exact-commit CI, including the Linux production image build. Final Go 1.26.9 race, selected database/action race, vet and build checks pass after the weekly-source drilldown correction. Use [the release checklist](ADMIN-WORKSPACE-RELEASE-CHECKLIST.md) for evidence and release handoff.
2. The owner has accepted the layouts; successful synthetic transitions and closed-season empty/history/follow-up checks are covered. Obtain release approval for the concrete reviewed candidate. Production TLS/proxy/email-2FA and read-only authenticated smoke checks belong to the controlled release handoff.
3. Confirm source-import coverage and the operational freshness threshold before relying on live import health. No importer or transport was activated for this walkthrough. Reconcile authorised first real next-season cases when activity resumes.
4. Wider statistics work retains the separately named Executive report's existing upcoming-first default. Broader navigation cleanup remains outside the current workspace pages. These follow-ups are separate from the repaired Home drilldowns.

The wider next-season plan remains open: junior club/lead access, contacts, season participation, forms, communications, fixture planning/publication, results and concession review need their agreed foundations and later implementation phases.
