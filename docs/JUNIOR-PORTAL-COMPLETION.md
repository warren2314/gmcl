# Junior core completion — 27 October 2026

This completes the four gaps recorded after PR #90, with an isolated demonstration for Joe on Sunday 1 November. Production rollout remains separate from hosting the fictional demonstration.

## Contact directory and audiences

`Juniors → Contact directory` lists current adult roles for the selected season. Staff can search name, club, email or phone and combine club, role, age group and competition filters. Phone-only contacts and missing roles remain discoverable. The CSV uses the same filtered rows and existing spreadsheet escaping.

Contact selections open the message composer without queuing or sending. The composer supports either everyone matching the group filters or an explicit set of contacts, optionally narrowed to several selected clubs. Clubs, current roles, competition/team relationships, valid addresses and junior participation are rechecked server-side. Empty explicit selections cannot become all-club messages. Ordering and duplicate selections do not change audience identity; shared email addresses retain each club's attribution while receiving one transport attempt.

The immutable preview hash includes the normalised filters and current recipient details. Changed contact details, participation or inactive status require another preview. Existing dispatch/freshness controls remain authoritative.

## Linked club record

Each club profile now joins its own form assignments, competition entries, published fixtures, concession records and scoped communication history, alongside contacts, teams, badges, participation history and lead access. Staff get direct links to assignment review, competition management, concession review and filtered exports. Club and preview views retain their permission scope; unpublished fixtures remain staff work.

## Inactive and non-entry

Migration 0100 adds a junior-only club status, independent of the permanent shared club identity and season participation. Staff can mark inactive/reactivate with a recorded reason. The marker persists across junior seasons without deleting contacts, fixtures, submissions, memberships or history and without changing senior reporting. Inactive clubs are excluded from new audiences and readiness participation counts. Previously queued messages and already granted access are retained; this is not an access revocation or cancellation switch.

Season participation additionally supports an explicit Not entered this season value, retaining a dated source/reason rather than deleting history.

## Demonstration boundary

`cmd/junior-demo` is a separate executable. It requires the dedicated `gmcl_junior_demo` database/user, its own session secret, outer access password and expiry. It constructs only the junior HTTP surface and normal authentication routes; it never calls the production application constructor or starts production workers. SMTP is forcibly unset and outbound sending disabled. Only fictional `@demo.example.invalid` access messages can reach its in-memory simulated inbox.

The demo has its own PostgreSQL container, file volume and credentials. It loads no production .env or volume. The app image is built from explicitly selected runtime artifacts, never a working-directory snapshot containing credentials or backups. Existing production application/database containers remain unchanged when the additional hostname is proxied.

The seed retains demonstrations across restarts. An interrupted initial seed is refused rather than silently replayed over partial data. Operational recovery uses a fresh explicitly named demo volume while preserving the previous one.

## Verification

On 27 October the complete local `go test -race -p 1 -count=1 ./...` suite passed against a fresh fully migrated regression database, the isolated handler database and the populated demo rehearsal database. `go vet ./...` and diff/format checks passed. Hosted browser and remote CI receipts are recorded separately in the delivery handoff.

Database-backed regressions cover directory/filter/CSV agreement; explicit and multi-club audiences; shared-address attribution; empty and out-of-scope selections; stale previews; inactive status across seasons; preserved participation/contact history; staff versus club authority; linked-profile club isolation; and first-run links.

The populated-demo rehearsal uses its own fresh schema and exercises normal staff password login, simulated-email one-use club sign-in, real club submission and staff completion. It checks zero outbound transport attempts and refuses non-demo databases. CI provisions this rehearsal database alongside the existing test databases.

Mailbox integration, real invitation delivery, authoritative roster/competition inputs and club pilot acceptance remain operational rollout work. This does not implement Joe's later fines, club-to-club fixture negotiation or advanced automation ideas.
