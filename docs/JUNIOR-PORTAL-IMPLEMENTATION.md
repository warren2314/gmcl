# Junior portal implementation and rollout

Updated 17 October 2026. Owner target: first usable release by **31 October 2026**. The owner has authorised junior implementation and instructed that mailbox integration be prepared separately. This supersedes the earlier assessment's planning-only status for Joe's work. Production enablement and deployment of this junior branch still require release approval.

The dashboard/ineligible release is PR #89, deployed from `b79f8aea0698b272ff9e54b495c31e117308e403`. Junior development starts from that release on `codex/junior-portal-core` in an isolated worktree. The original checkout and unrelated work are preserved.

## Structure and controls

The existing Go/PostgreSQL application is retained. `internal/junior` owns records, access, forms, communications, competitions, fixture publication, result observations and concession review. HTTP adapters live in separate `junior_*.go` files. Permanent `clubs.id` is shared; junior seasons, teams and fixtures have separate tables and cannot activate senior obligations.

Migrations 0094–0099 are additive. No applied migrations are rewritten. Deploying this branch starts no junior worker. `JUNIOR_PORTAL_ENABLED` and `JUNIOR_OUTBOUND_EMAIL_ENABLED` default false. Staff require `junior_manage` or the established effective super-admin policy. Super Admin alone can grant/revoke junior staff access; no real grants have been changed during development.

## Delivered workflows under verification

| Workflow | Behaviour | Evidence |
| --- | --- | --- |
| Junior Home | Explicit season, linked attention queues, readiness and record search | Migrated query and card/list reconciliation tests |
| Clubs | Permanent identity, Add New Club, participation history, multiple teams, adult multi-role contacts and private versioned badges | Two-season, badge and senior-isolation tests |
| Prior roster | CSV preview, explicit identity decisions, reviewed mapping and repeat-safe atomic apply | Invalid/ambiguous mapping, rollback and replay tests |
| Lead access | Club-bound one-use invites/login, eight-hour revocable sessions, atomic handover | Actual lifecycle, concurrent redemption/revocation and cross-club tests |
| Staff preview | Read-only View as Club retaining/auditing staff identity | Forged writes rejected by HTTP and service |
| Forms | Ten-question builder, seven field types, immutable versions, assignments, drafts, receipts, review/return and history | Actual publish/save/submit/review/republish/CSV journey; stale and closed-season tests |
| Communications | Audience preview, immutable content/recipient snapshots, group/individual messages, controlled retries and per-club history | Shared addresses deduplicated, privacy, disabled transport and retry tests |
| Fixtures | Age group/division, entries, dates, grounds/capacity, unavailability, draft generation/editing, checks and atomic publication | Pairings, constrained schedules, stale changes and cross-competition checks |
| Concessions | Explicit local fixture/result import, retained evidence, assisted candidates, Confirm/Dismiss/Request information and correction history | Unknown/rain/bye never auto-confirm; replay, correction and totals tests |

Message status distinguishes queued, missing recipients, failed and transport accepted. Acceptance by SMTP is not proof of mailbox delivery. Reminders exclude submitted/completed assignments and reuse a same-day message identity. Fixture publication is independent of delivery and missing contacts.

Dispatch also rechecks workflow freshness before claiming recipients: submitted/superseded form reminders, older fixture publications, outdated concession requests and closed-season workflow messages cannot be sent. An SMTP operation begins after the transaction commits; stable Message-ID and visible attempts support reconciliation, not an exactly-once delivery guarantee.

Results initially use reviewed CSV with explicit local fixture IDs and outcome codes. This is not a connected junior Play-Cricket feed. External authority, real result coverage/codes and senior ground bookings remain operational inputs. Missing external bookings are a recorded coverage gap, not a claim of complete clash coverage. Automatic fines and club-to-club negotiation remain later work.

## Configuration

The shared inbox is visibly disconnected; manual incoming correspondence records do not complete automatic mailbox integration. See [separate mailbox preparation](JUNIOR-MAILBOX-PREPARATION.md).

Junior sending requires `JUNIOR_OUTBOUND_EMAIL_ENABLED=true`, `SMTP_HOST`, valid `JUNIOR_SMTP_FROM`, optional valid `JUNIOR_REPLY_TO` (defaults to junior From), and no `EMAIL_OVERRIDE`. Sign-in delivery additionally requires `JUNIOR_PORTAL_ENABLED=true` and canonical HTTPS `PUBLIC_BASE_URL` or `APP_BASE_URL`. Junior sender settings do not alter existing mail streams. Verification uses injected fake delivery only; real SMTP has not been exercised.

`JUNIOR_UPLOAD_DIR=/app/data/junior` uses the persisted `/app/data` Compose volume. Back up badge files alongside the database. A database-only restore cannot recover image bytes. Downloads always pass club/staff permission checks.

## Delivery target

1. **17–20 October:** join and verify modules, complete full regression/browser checks and prepare the review candidate. Keep rollout/email gates off.
2. **21–24 October:** Joe/admin walkthrough; obtain authoritative prior roster, first official form/deadline, competition entries/dates/grounds, fixture authority and representative result examples.
3. **25–28 October:** approved small-club pilot, transport/access checks, real official-action cycle, backup/restore and support ownership.
4. **29–31 October:** resolve pilot findings, obtain release approval and enable in stages. Keep the mailbox visibly pending until its separately prepared connector is accepted.

These are targets, not claims that operational inputs or acceptance have arrived. Portal development continues independently of provider decisions.

## First walkthrough and season setup

For Joe/League Admin, open **Juniors → Clubs and contacts**, configure the junior season and review the previous roster before applying it. Reuse existing club/contact/team identities wherever the source refers to the same record. Resolve ambiguous rows explicitly. Confirm participation, the Junior Lead and Safeguarding Officer contacts, and age-group/team coach assignments. Contact roles do not grant sign-in access.

Use **View as Club** to check the result. Configure and verify junior transport separately before sending an invitation. Super Admin grants Joe's staff account `junior_manage` through **Junior Home → Staff access**; it does not create another staff identity or grant sanctions powers.

Publish the first official form to selected participating clubs with its deadline and review requirement. A club lead opens **Official forms**, saves a draft, submits and retains the locked receipt. Staff review the submitted revision, complete it or return it with a reason. Prepare outstanding reminders, then inspect the immutable audience and transport status in **Messages**; preparing is not sending.

For fixtures, record age group/division, entries, playing/reserve dates, grounds/capacity and unavailable dates. Review the generated/manual draft and known clashes. Record external booking coverage before publishing. Clubs see official snapshots; corrections create another publication and retain earlier versions. Import result examples using local fixture IDs and explicit outcome codes, then review candidates with recorded reasons. No automatic fines are issued.

At rollover, retain the closed junior season and its submissions/publications/results. Create the next junior season, explicitly activate participation and review dated contacts/entries again while reusing permanent identities. Do not activate a legacy senior season or copy junior fixtures into senior reporting tables. Closed junior workflows remain readable; authorised season configuration controls reopening when a correction genuinely requires it.

## Release gates

Local verification, 17 October 2026: the frozen implementation passed `go test -race -p 1 -count=1 ./...` with both disposable databases configured, `go vet ./...`, application build and the repository's configured gosec rules (zero findings). Govulncheck returned zero reachable call findings; it still reports advisories in dependency modules that the application does not call. These are local checks, not remote CI or production acceptance. The loopback HTTPS browser rehearsal passed 35 desktop/phone checks and the actual club draft → submit → staff completion journey, with external browser requests blocked and no real email.

The earlier regression attempts exposed test-environment issues: an unmigrated legacy test database, cleanup lock pressure from overlapping full-schema tests, and a pre-existing legacy approval test retaining fixture recipients between runs. Final verification used a fresh named regression database and serialized package execution. No existing approval-routing production code was changed to make those runs pass.

- Full race-enabled regression suite against prepared disposable databases, vet, build and repository security checks; fresh migration tests with real pgvector.
- Desktop/phone browser journeys, matching attention counts/lists/exports and no club data leakage.
- Baseline upgrade and database plus badge-volume restore rehearsal. Rollback disables both junior flags first and retains additive junior tables.
- Verified canonical URL, dedicated sender/reply address and controlled test recipients before real invitations; no automatic sending scheduler in the initial release.
- Staff/club pilot acceptance and explicit merge/deploy approval for this change. PR #89 approval applies to the already deployed dashboard release.
