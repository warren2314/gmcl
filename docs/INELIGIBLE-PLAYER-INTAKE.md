# Ineligible-player Google Form intake

Staff who need click-by-click instructions should use the
[Ineligible-player quick guide](INELIGIBLE-PLAYER-QUICK-GUIDE.md). This document
continues with the technical setup, controls and reconciliation detail.

The private Google Form sync is a read-only staging path. It writes source
observations to the migration-0050 intake tables; it does not create a sanction
case, contact either club, or issue a sanction.

## Google access

1. Create a dedicated Google service account.
2. Share only the Form response spreadsheet and its Google Form upload folder
   with the service-account `client_email` as a **Viewer**. Do not make either
   resource public.
3. Store the downloaded service-account JSON outside the repository and mount
   it read-only into the app container.
4. Set the following deployment values:

```dotenv
INELIGIBLE_IMPORT_ENABLED=true
INELIGIBLE_BOOTSTRAP_IMPORT_ENABLED=false
INELIGIBLE_PRIVATE_GOOGLE_FORM_URL=https://docs.google.com/forms/d/e/.../viewform
INELIGIBLE_GOOGLE_SPREADSHEET_ID=18I8JoouigCOX-sja3lfHguSZtOF3EwJIFIZ2Cee2uR8
INELIGIBLE_GOOGLE_SHEET_GID=1964965743
GOOGLE_SERVICE_ACCOUNT_FILE=/run/secrets/gmcl-google-service-account.json
```

`GOOGLE_SERVICE_ACCOUNT_JSON` can be used instead of the file path, but never
set both. `INELIGIBLE_GOOGLE_SHEET_RANGE` defaults to
`'Form responses 1'!A:N`, and the HTTP timeout defaults to 45 seconds. The OAuth
token is requested with the Google Sheets and Drive read-only scopes. The Drive
client never lists or searches files; it requests only IDs parsed from the
literal `File Upload` cell.

Permitted upload bytes are retained under
`/app/data/ineligible-uploads/sha256/<prefix>/<sha256>` by default. This path is
inside the existing persistent app-data volume. The following limits can be
overridden without changing code:

```dotenv
INELIGIBLE_UPLOAD_DIR=/app/data/ineligible-uploads
INELIGIBLE_UPLOAD_MAX_FILES=10
INELIGIBLE_UPLOAD_MAX_FILE_BYTES=10485760
INELIGIBLE_UPLOAD_MAX_TOTAL_BYTES=26214400
INELIGIBLE_UPLOAD_ALLOWED_CONTENT_TYPES=application/pdf,application/msword,application/vnd.ms-excel,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet,application/vnd.openxmlformats-officedocument.wordprocessingml.document,image/gif,image/heic,image/jpeg,image/png,image/webp,message/rfc822,text/plain
```

Files are streamed through a byte limit, hashed with SHA-256, atomically moved
to content-addressed storage, and made read-only. Migration 0054 records the
original name, media type, size, Drive ID/source URL, hash, and storage key
against the exact intake revision. Existing content is re-hashed before reuse.

The separate `INELIGIBLE_IMPORT_ENABLED` switch can stop all intake while
leaving `SANCTIONS_EMAIL_DISABLED` unchanged. Ingesting rows never sends mail.
`INELIGIBLE_OUTBOUND_EMAIL_ENABLED` remains independent: changing either
switch never changes the other.

## Protected schema

The importer accepts the response sheet only when A1:N1 exactly equals, in
order:

1. `Timestamp`
2. `Email address`
3. `Name of defaulting player as shown on scorecard`
4. `Reason you believe the player is ineligible`
5. `Additional Info`
6. `Your Club`
7. `Your Name & Role at Club/League`
8. `Your Preferred tel no`
9. `Offending Club's Name`
10. `Team in question`
11. `Fixture Date`
12. `Additional Evidence`
13. `File Upload`
14. `Score`

A count, spelling, whitespace, or order change fails the run before any intake
rows are written. A deliberately revised form can be deployed atomically with
`INELIGIBLE_GOOGLE_SCHEMA_JSON`; the value must contain exactly 14 literal
`headers` and a `columns` object mapping the eight semantic fields shown in
`.env.example` to zero-based indexes.

Every accepted source row is stored as an exact-header-keyed JSON object. Its
canonical SHA-256 determines whether a later observation is unchanged or a new
append-only revision. Stable response keys hash the spreadsheet/gid, timestamp,
reporter email, player, offending club/team, and fixture date. The source row
number is also retained as a conservative secondary anchor. If an identity key
changes at a previously observed row, a known key moves to another row, or two
intakes claim the same row, the importer never guesses: it appends an immutable
identity-exception revision, leaves the mapped identity projection unchanged,
and invalidates any linked case response window pending manual review. Exact
retries of an administrator-resolved exception remain idempotent. Invalid
dates, missing identity fields, collisions, unapproved file types, over-limit
files, invalid links, and inaccessible Drive files are likewise preserved as
`exception` intakes for manual triage. Successfully downloaded files on a
partially invalid row remain retained; no exception can create a case
automatically.

## Import selection and queue visibility

The importer reads cell values, not spreadsheet formatting. It cannot detect
whether a row is blue, so **Import and choose reports** always reads the full
configured Google response range. Staff then choose the reports to progress on
the selection screen; deleting, reordering or recolouring source rows is not a
selection mechanism.

The import summary and selection table measure different things:

- **Source rows read** is every data row returned by the configured Google
  range.
- **Added** and **changed** count database mutations. Both can be zero on a
  successful repeat import.
- **Need attention** counts row warnings and failures. A validation or evidence
  warning can still be retained as an exception intake; an unresolved source
  identity or incomplete manifest blocks selection.
- The chooser contains only Google reports in new, reviewing or exception
  state that are not already linked to a non-duplicate case. Linked, duplicate
  and ignored reports remain in **Report history** and are not offered
  again.
- A candidate is enabled only when the latest import observed its exact current
  revision. Older or changed open candidates can be shown disabled for
  reference.
- One spreadsheet row creates one intake. Multiple names in the Player cell are
  not split automatically into separate reports or cases.

Every applied row writes an immutable manifest entry for that sync run,
including rows whose intake content is unchanged. Each entry pins the source
row and hash to either the exact intake revision that was resolved or an
unresolved error. A selection can be saved only against the latest Google sync
run and the exact current revisions represented by that run. The server
recomputes the candidate fingerprint while holding the import and selection
locks; an incomplete or stale run, a manifest-count mismatch, an unresolved
row, or a changed candidate/revision fails closed and requires a fresh import
or selection page.

Saving creates an append-only work-list batch and one append-only visible or
deferred decision for every candidate. Checked reports become visible in the
default **Selected reports** queue; the server calculates the unchecked
complement and marks it deferred. A later batch supersedes the presentation
choice without editing or deleting its history. Reports with no work-list
decision, including submissions arriving after the saved selection, default to
visible and are labelled as not yet chosen so new work cannot silently
disappear.

Deferred is only a default-queue visibility choice. It does not delete an
intake, change its lifecycle state, resolve or ignore it, or create an
authorisation boundary. Authorised staff can inspect deferred reports through
**View hidden reports** or the **Hidden reports** filter. **Report history** uses every lifecycle state and every work-list visibility so linked,
duplicate and ignored reports can also be found.

The default evidence allowance is ten Drive files per report, with the existing
10 MB per-file and 25 MB total limits plus the approved content-type allowlist.
Invalid links, inaccessible files and over-limit evidence are retained as
reviewable exceptions rather than being silently discarded.

Importing or saving a selection creates no sanction case, correspondence,
outbox item, decision, effect or outcome. Staff must still open each visible
report and explicitly raise or link its case before following the investigation
and independent-approval process below.

## Scheduling and operations

`n8n_workflow.json` calls the HMAC-protected endpoint daily at 03:30 in the
`Europe/London` timezone:

```text
POST /internal/sync-ineligible-reports
X-Timestamp: <Unix seconds>
X-Nonce: <unique random UUID>
X-Signature: HMAC-SHA256-HEX(timestamp|nonce|POST|/internal/sync-ineligible-reports||)

<empty body; no Content-Type header>
```

The n8n Code node reads `N8N_HMAC_SECRET` from its environment and uses the
Node `crypto` built-in. The bundled compose service therefore permits only
that built-in through `NODE_FUNCTION_ALLOW_BUILTIN=crypto`. Bearer tokens are
not accepted by this endpoint, and a valid nonce cannot be replayed within the
five-minute signature window.

The checked-in workflow export is deliberately `active: false`. Import it,
verify the production base URL and secret, then activate the schedule after the
named backfill application is complete. Calls made too early are rejected by
the server-side rollout gate, but leaving the export inactive avoids noisy
pre-rollout failures.

The response contains `run_id`, `status`, `seen`, `new`, `changed`, and
`errors`. Concurrent runs return HTTP 409. Disabled imports return 503, and
upstream/authentication/schema failures return a non-2xx response after the
failed run is recorded. Administrative code should call
`ineligible.SyncFromEnv(ctx, pool, ineligible.Trigger{Type: "admin", AdminID:
&adminID})` so manual and scheduled runs share the same lock and idempotency
rules.

### Fail-closed rollout gate

Daily n8n polling is blocked until a tracker reconciliation has both a named
sign-off and a completed post-sign-off application. Initial staging is the only
exception: temporarily set `INELIGIBLE_BOOTSTRAP_IMPORT_ENABLED=true` and use
the authenticated administrator **Sync now** action. Bootstrap never authorises
the n8n trigger, native website submissions, case creation or email. Set it
back to `false` immediately after the protected sheet has been staged.

Once the application prerequisite exists, scheduled runs begin the clean-date
gate. It counts three **distinct adjacent Europe/London calendar dates**, not
requests. Repeating a successful request on one date cannot increment the
count. Any failed or partial scheduled attempt on a date makes that date failed
and resets the sequence. Manual Sync now runs never count.

The third clean date writes one immutable activation record containing the
named application, the three qualifying dates, activation time and an exact
30-day Google grace deadline. Before activation,
`/sanctions/report?type=ineligible_player` redirects to
`INELIGIBLE_PRIVATE_GOOGLE_FORM_URL`; the native option is removed and native
POSTs are blocked server-side. After activation, native intake is available and
scheduled Google polling continues during the grace period. At the deadline,
scheduled calls return a clear `retired` result without reading or writing
Google data. Authenticated manual sync remains available for exceptional
reconciliation while the independent import kill switch is enabled.

Administrators can inspect the prerequisite, clean-date count, activation,
grace deadline, import/bootstrap switches and outbound switch at
`/admin/ineligible/rollout`.

## Investigation, response and outcomes

Syncing, native intake and starred-player escalation only stage or link private
records. They never contact a club or issue a sanction. A starred finding uses
the idempotent **Create ineligible-player case** command, retains the stable
list/match/player/scorecard provenance, requires an unambiguous Play-Cricket
fixture-side mapping and assigns the configured Hussan account.

### Staff route: ask the club for its explanation

Use this order on the case page:

1. Check the public summary, private source information and scorecard evidence.
2. Run HawkAI. It ranks published rules using the case wording and any rule
   already recorded; for example, a starred-player finding should rank the
   starred-player rule above a generic dispensation rule. Open the cited source
   and save the rule only after checking it.
3. In **Contact the club for its explanation**, review and save the initial
   email. It must ask what happened, why the player appeared, whether the club
   believes the player was eligible and what permission, exemption or evidence
   supports its position. It must also say that no decision has been made.
4. Review and save the reminder. This prepares it but does not send it.
5. Check the displayed verified official mailbox, then select **Send initial
   email to club**. This is the action that queues the first email. Opening the
   case, running HawkAI and saving either draft do not contact the club.
6. Wait for the secure portal response or record an email, telephone or meeting
   response manually. Review the response before proposing any decision.

The initial send button is unavailable until outbound email is enabled and the
offending club has a verified official mailbox. Only the first email is queued
when staff select it. The saved reminder is queued for day five after confirmed
delivery and is cancelled if the club responds first.

The initial allegation and response request go only to the verified official
mailbox for the offending club. Reporter name, role, email, telephone number
and reporting-club identity are screened from offending-club correspondence.
Raw evidence cannot be marked shareable. An administrator must upload a
separate redacted derivative, attest that the reporter and reporting-club
identity have been removed, and then explicitly share that derivative. The
source-to-derivative relationship and both SHA-256 values are immutable; the
portal verifies the stored derivative checksum again before download.

Creating a response request queues only the initial notice. Its secure token
has no usable expiry and the five/seven-day clocks do not begin until that
notice has been accepted by the mail provider. Successful delivery activates
the token and queues one reminder for London-calendar day five. The link
expires on day seven, reopens the investigation and records the overdue event;
expiry never creates an adverse finding automatically. Portal replies and
externally received email, telephone or meeting responses share the same
immutable case timeline and unreviewed-response queue.

Decision composition remains editable through the proposed subject, findings,
rule determination, atomic effect bundle and appeal instructions. From those
fields the service renders deterministic audience-safe email and PDF drafts;
the rendered wording itself is read-only so it cannot contradict the decision
or acquire private case text. A different authorised administrator approves
and locks the exact email/PDF bytes and checksums before publication.

Outcome delivery is deliberately separate by audience:

- the offending club receives the complete findings, rule, sanctions,
  effective dates and appeal instructions;
- every mapped reporting club receives the confirmed findings, rule and final
  outcome or no-action result at its official club mailbox, without the
  offending-club response, private evidence, internal notes or reporter
  details;
- a same-club report receives one combined notice;
- a `GMCL Official` intake has no reporting-club notice and is covered by the
  configured league-official recipients;
- Executive and discipline recipients always receive the official outcome,
  with Play-Cricket added for league-table points and finance for fines.

No-action decisions use the same independent approval and notifications, then
close unpublished. Only a separate `points_adjustment` effect creates Denver's
two-day Play-Cricket task; card-system calculations stay in the card ledger.

If a linked source revision changes after approval, the case cannot silently
reuse its decision or PDF. Before any outcome may have been delivered and
before follow-up work starts, an authorised administrator can use the audited
reopen command. It preserves the old approved snapshots, appends compensating
effect/card records, cancels untouched tasks, revokes unsent outbox items and
requires the new source revision to be merged before a fresh proposal and
independent approval. Delivery uncertainty, publication or started external
work blocks this shortcut and requires the normal correction process.

## 2026 tracker backfill

Administrators with `sanctions_import` can open
`/admin/ineligible/backfill` and upload the supplied 2026 `.xlsx` tracker. The
backfill requires the exact `Form responses 1` sheet and exact A:Z headers. It
retains the source content-addressed under `INELIGIBLE_BACKFILL_DIR` (default
`data/ineligible-backfills`) and records the workbook and row SHA-256 values.

Each row is reconciled conservatively against a staged `google_form` intake by
timestamp, player, offending club, team and fixture date. Exact matches are
distinguished from matches requiring only Unicode/case/whitespace
normalisation; multiple candidates remain exceptions. Columns O:Z are stored
verbatim as immutable manual history.

Every row needs an append-only administrator review confirming its intake
disposition and historical open/closed state. Any non-empty free-text points or
cards cell must be manually classified and explained before the run can receive
a named sign-off. Upload, review and sign-off never create or change a case,
effect, card ledger entry, league-points task, correspondence revision or
outbox row, so staging historical data cannot resend an old outcome.

After named sign-off, the run page exposes a separate application preview. It
is fail-closed unless every non-excluded `accept_match` review still equals the
review revision pinned by the sign-off and its intake links to exactly one
unpublished `ineligible_player` case. Missing links, multiple cases, later
review edits, prior equivalent applications, published history, or conflicting
open/closed reviews block the whole run. The preflight also rejects any target
case that already has a decision revision/effect, correspondence, outbox
message, pending response request or active response token. Open rows may only
restore `submitted`, `triage` or `investigating` cases; closed rows may also
preserve/restore `closed`. Protected workflow states including
`response_pending`, `decision_proposed`, `approved` and `published` are never
mutated, and any approval timestamp blocks application regardless of status.

The confirmed apply command executes once under a serializable transaction. It
appends the verbatim O:Z history, manual effect interpretation and provenance as
a private immutable case event, then restores reviewed open cases to
`investigating` and reviewed closed cases to `closed` with
`public_status='unpublished'`. Unmatched and excluded rows remain in
reconciliation. Closed rows also create a readable immutable historical outcome
snapshot containing the exact signed-off tracker history and its manual-effect
review status. These snapshots are deliberately non-operative: they do not
create a decision, effect, points/card ledger entry, follow-up task or message.
They appear on the admin case page so an old finding remains readable without
being recalculated or resent.

Immutable application and per-row records preserve before and after state and
make retries idempotent. Counts of decisions, effects, card ledger entries,
legacy sanctions, follow-up tasks, correspondence and outbox messages are
compared before and after; any change rolls back the complete application.
