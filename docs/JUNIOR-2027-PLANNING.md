# Junior fixture planning for 2027

This feature branch adds U9 capacity, visible clash alerts, a daily ground calendar and grading attached to team entries. It is based on production release `615419044`. It has not been deployed to production.

## Grounds and U9 capacity

Under Junior administration → Grounds and clashes, record each physical venue once and confirm its address, postcode, source and permitted layouts. U9 now appears alongside U11/U13/U15/U18. Enter a layout such as **3 U9 games**, or a separately confirmed mixed layout such as **2 U9 + 1 U11**. Layout rows are alternatives: their capacities cannot be added together.

U9 competition constraints and individual fixtures now select the same saved grounds as other age groups. Existing unlinked U9 venue labels must be reviewed and linked/selected before publication when saved grounds exist. No capacities are inferred for existing grounds. Duration defines occupancy; include any changeover time in the duration. A game ending at 10:00 does not overlap a game starting at 10:00.

A geographical zone can use a separate competition/division (for example U9 Rochdale), with the agreed twelve Sundays entered under playing dates. Custom/manual fixtures continue to allow unequal repeat pairings. This release does not add an automatic twelve-week zone generator.

## Clash alerts and calendar

Junior Home shows a red blocking-warning alert. Grounds and clashes, competition drafts and the ground calendar show detailed warnings, with a list of every booking at each affected ground on that day. Use **Ground and division on this day** to see both the selected ground's bookings across competitions and the selected division's fixtures at every ground. Draft and current published reservations remain distinguishable. An unchanged match is counted once; moving its draft retains the official reservation until republished.

The **Ground calendar** lets staff choose a date, move to adjacent days, search by venue/address/postcode and select a division. Grounds without bookings are labelled **No recorded bookings**, rather than assumed available. All bookings on that date remain available below the selected ground/division context, including bookings with unlinked grounds.

Women’s, senior and other commitments can be entered in the calendar with their confirmed source. These reserve the whole ground during the entered interval, participate in generation/publication checks, and appear independently of junior season filters. Cancel a booking to release its slot; cancellation retains the record and audit trail. Correct a booking by cancelling it and recording its replacement. These records do not create junior teams, results or concessions. Existing senior fixtures are not automatically imported or matched by venue name.

## Entry grading

Every team row on a club's team-entry form has optional requested grade, geographical zone and grading notes (previous division, experience and playing strength). These are shown to staff before league selection, in approval previews and immutable receipts, and alongside the resulting competition entry with a link to the original form response. Staff still choose the division. Requested grade is not an automatic allocation rule.

Existing responses and receipts remain readable without grading fields. Resubmission through a new approved entry form can update the entry's grading and source link; earlier submission and approval receipts remain unchanged. Ordinary text fields retain their existing limits, while the structured team-entry field accepts up to ten teams with individual grading notes.

## Club access and rollout

1. Confirm the club's current adult Junior Lead and registered email; contact roles and portal access are managed separately.
2. Configure and test junior outbound email, then invite that lead through the existing club access controls.
3. The lead follows their invitation and completes the verification steps. On later visits they request a sign-in link using their invited email address. Links last 15 minutes, work once, and require the Continue step.
4. Pilot with a small number of clubs before wider invitations. The login page now explains link expiry, replacement and the absence of a shared password.

This branch does not enable production club sign-in or email, send invitations, connect the mailbox or change live records.

## Technical rollout

Apply additive migration `0103_junior_planning.sql` through the normal deployment pipeline. It adds grading/provenance columns to competition entries and a table for external ground bookings. U9 uses the existing ground-layout JSON without a data rewrite. Deploy the application and migration together; publication checks require the new table. Retain the established database and file-volume backup process.

External booking writes use the publication advisory lock and invalidate affected draft competition revisions, so a stale reviewed publication cannot bypass a newly recorded commitment. Staff authorization, club scope, CSRF and read-only preview restrictions remain enforced. No new email transport or worker is introduced.

## Validation — 15 November 2026

- `go test ./... -count=1` passed with the full unchanged migration chain plus 0103 in a loopback-only, disposable PostgreSQL 16 database using real pgvector.
- Race-enabled junior service and HTTP/PostgreSQL regressions passed, including U9 capacity, external booking creation/cancellation, stale publication review, draft/publication reservation deduplication, CSRF/permissions and entry grading provenance/receipts.
- `go vet ./...` passed. Focused checks passed again after the final warning-text corrections.
- Synthetic calendar and grading handler pages were inspected in Chrome at desktop width and 390px phone width. Actual create/cancel/approval requests were exercised by HTTP integration tests; browser review used saved synthetic responses, not production.
- No live database, club records, invitation delivery or production deployment was changed.
