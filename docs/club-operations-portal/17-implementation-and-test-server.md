# Club Portal Implementation and Test-Server Runbook

**Implementation branch:** `codex/club-operations-portal`

**Implementation baseline:** 26 July 2026

**Current delivery slice:** identity/tenancy foundation, feature-flagged read-only action centre, account-security activity and appointment inventory

This runbook records implemented behaviour and the controlled route to a test-server pilot. It does not remove or replace the existing captain-report or administrator services.

## Implemented in this slice

- Provider-neutral managed OpenID Connect client using Authorization Code flow, PKCE, discovery and verified ID tokens.
- Issuer, audience, signature, expiry, state, nonce and PKCE enforcement.
- Single-use approved onboarding invitations; the verified provider email must match the approved official-contact email.
- Separate users, external identities, club memberships and effective-dated role assignments.
- Multi-club acting-context selection with bearer-token rotation when the context changes.
- A named-user appointment inventory in `/portal/contexts` shows each currently effective club role, team/season/competition scope and start/end dates. The selected appointment is marked and cannot be redundantly reselected, avoiding an unnecessary token rotation and audit event.
- Opaque, hashed, server-side sessions with 30-minute idle and 12-hour absolute defaults.
- User-visible active-session inventory, immediate per-session revocation and all-device revocation through account security-version invalidation.
- User-visible account activity at `/portal/activity`, limited to the latest 100 events for the named user in the selected club context. The repository projects only action, outcome, acting role and timestamp; the page maps these through explicit presentation allowlists and never selects or renders audit metadata, targets, record IDs, IP addresses, user agents, hashes or another club's events.
- Provider-enforced step-up for sensitive account actions. The generic profile uses `prompt=login`, `max_age=0` and an approved step-up ACR. The Cognito profile uses forced login plus a signed `auth_time` no earlier than the stored step-up request; both require the returned identity to match the initiating named user.
- State-changing forms retain double-submit CSRF protection with 256-bit cryptographic tokens, fail-closed generation, constant-time comparison and `Secure`, `HttpOnly`, `SameSite=Lax` cookies. Existing server-rendered forms and the configured asynchronous client do not require JavaScript access to the cookie.
- Existing per-IP/per-path token-bucket limits now use a 10,000-entry bounded O(1) LRU with ten-minute idle expiry, preventing unbounded attacker-controlled memory growth while preserving every route's configured request allowance. Limited responses include a calculated `Retry-After`.
- Deny-by-default, application-owned permissions with club/team/season/competition containment.
- A restricted `gmcl_portal_runtime` PostgreSQL role, tenant transaction context, forced RLS on portal-private tables and a startup refusal if the effective role can bypass RLS.
- Append-only, versioned hash-chained audit events without raw authentication or invitation secrets. Version-2 hashes use PostgreSQL-compatible timestamp precision and can be independently recomputed by the operational preflight; legacy version-1 rows are reported separately rather than overstated as fully verified.
- A fail-closed `/bin/portal-preflight` command verifies required migrations, effective RLS, forced-RLS coverage, append-only and chain-shape constraints, every audit-chain position/link/hash (including stored IP and user-agent fields for version 2), session policy, generic ACR or verified Cognito assurance, HTTPS callback alignment, SMTP, internal-worker authentication and read-only OIDC discovery without printing secrets or enabling a club.
- `scripts/verify-portal-staging.sh` combines strict pilot preflight with external HTTPS checks for health, the preserved captain entry, administrator login, unauthenticated portal redirect, no-store behavior, baseline CSP/HSTS/framing/content-type headers, hardened CSRF cookie flags and rejection of an unsigned notification-worker request.
- Per-club feature flags and a Super Administrator pilot-control page at `/admin/portal`; disabling a club's `portal_access` atomically disables its module flags and revokes every active session currently scoped to that club.
- Super Administrator controls to revoke unused invitations and effective-dated appointments; appointment revocation immediately invalidates every session using that role and emits an audited outbox event.
- Super Administrator pilot reconciliation showing active-team mapping completeness, active captain-contact counts, portal memberships/appointments and the latest mapped fixture synchronization per club.
- Transactional account-security events are idempotently materialized into a separate notification queue for account activation and appointment revocation.
- An HMAC-protected worker delivers allowlisted, non-sensitive security templates through configured SMTP, with a same-connection advisory lock, ten-minute claim lease, exponential retry, five-attempt dead-letter threshold and stale-worker generation checks.
- `/admin/portal` shows unpublished, pending, retrying, sending, sent and dead-letter delivery health without displaying message bodies, tokens or recipient addresses.
- Read-only action-centre totals over existing Play-Cricket fixtures, submissions, exemptions and the team-level sanctions ledger.
- Tenant-scoped report-obligation history with fixture/match/submission source identifiers, deadlines, exemption reasons and derived due/submitted/late/missed status.
- Tenant-scoped sanction-ledger history showing only team deltas and public case fields; the repository never selects private summaries, reporter details or internal notes.
- Historical season and team filters that can only narrow the effective appointment; unavailable identifiers disclose no foreign metadata and create an audited denial event.
- A selected-team handoff to the existing captain magic-link journey; the portal remains read-only and does not duplicate or replace captain submission behavior.
- Explicit unavailable/stale and unreconciled-legacy states; missing source data is not rendered as zero/compliant.
- Sensitive onboarding email refuses the development body-logging fallback and requires SMTP.

## Preserved behaviour

- Existing `/captain/*`, captain magic-link, report submission and missed-report calculations are unchanged.
- Existing `/admin/*` authentication and operational pages remain available in parallel.
- Existing sanctions cases, legacy ledger, starred-player, Hawk, Play-Cricket, n8n and email jobs are not rewritten.
- No portal write changes an official report, fixture, sanction or registration record.
- Email remains the official communication record during the pilot.

## External gates still in force

The following are not technical defaults and must be approved before the corresponding live use:

1. Managed identity-provider procurement, security review, DPA, subprocessor/data-location and exit terms.
2. Named CLO/Super Administrator approvers and the official-contact evidence process.
3. Signed role grantor/scope/expiry and separation-of-duties matrix.
4. Authoritative club/team/season/competition reconciliation and pilot-club sign-off.
5. Retention, lawful basis, support, incident, recovery and on-call decisions.

Use synthetic people and a provider sandbox on the test server until these gates are signed off.

## Test-server configuration

Register this exact redirect URI with the provider:

```text
https://TEST_HOST/portal/auth/callback
```

Set these values in the test server's protected `.env`; never commit their values:

```dotenv
APP_ENV=test
PUBLIC_BASE_URL=https://TEST_HOST

CLUB_PORTAL_ENABLED=true
CLUB_PORTAL_SESSION_IDLE_MINUTES=30
CLUB_PORTAL_SESSION_ABSOLUTE_HOURS=12
CLUB_PORTAL_STEP_UP_MINUTES=15

CLUB_PORTAL_OIDC_ENABLED=true
CLUB_PORTAL_OIDC_PROVIDER_PROFILE=cognito
CLUB_PORTAL_OIDC_ISSUER=https://cognito-idp.eu-west-2.amazonaws.com/eu-west-2_USER_POOL_ID
CLUB_PORTAL_OIDC_CLIENT_ID=COGNITO_APP_CLIENT_ID
CLUB_PORTAL_OIDC_CLIENT_SECRET=SECRET_VALUE
CLUB_PORTAL_OIDC_REDIRECT_URL=https://TEST_HOST/portal/auth/callback
CLUB_PORTAL_COGNITO_POLICY_VERIFIED=true
CLUB_PORTAL_OIDC_REQUIRED_ACR=
CLUB_PORTAL_OIDC_STEP_UP_ACR=

SMTP_HOST=CONFIGURED_TEST_SMTP
SMTP_FROM=CONFIGURED_TEST_SENDER
EMAIL_OVERRIDE=CONTROLLED_TEST_MAILBOX

N8N_HMAC_SECRET=AT_LEAST_32_RANDOM_BYTES
```

`CLUB_PORTAL_OIDC_ALLOW_INSECURE` must remain false on the test server. The Cognito profile fails startup if either ACR value is present or the policy-verification flag is false.

### Verify the Cognito User Pool

Create a separate pilot User Pool and confidential app client. Use administrator-created named accounts only. Configure managed login with passkeys and password, required MFA, TOTP, passkey user verification `required`, and `FactorConfiguration=MULTI_FACTOR_WITH_USER_VERIFICATION`. Configure Cognito email delivery to use the existing SES identity in the same Region.

Run the read-only verifier from an approved workstation with least-privilege AWS credentials:

```powershell
.\scripts\verify-cognito-portal.ps1 `
  -UserPoolId eu-west-2_USER_POOL_ID `
  -ClientId COGNITO_APP_CLIENT_ID `
  -IssuerUrl https://cognito-idp.eu-west-2.amazonaws.com/eu-west-2_USER_POOL_ID `
  -CallbackUrl https://TEST_HOST/portal/auth/callback `
  -Region eu-west-2 `
  -Profile GMCL_AWS_PROFILE
```

Only copy `CLUB_PORTAL_COGNITO_POLICY_VERIFIED=true` into the deployment after this command passes. Re-run it after any User Pool, app-client, domain, callback, factor, MFA or email configuration change. It reads configuration and does not create users or change AWS resources.

Import the updated `n8n_workflow.json`, set the test n8n environment's `GMCL_BASE_URL=https://TEST_HOST`, bind the existing internal HMAC secret through the approved n8n credential mechanism, and leave the workflow inactive until the application deployment and controlled-mailbox checks pass. The portal-notification node derives its endpoint from `GMCL_BASE_URL` and runs every five minutes. Do not retain the repository placeholder as a live credential.

## Deployment sequence

1. Back up the test database.
2. Deploy the exact tested branch commit with `MIGRATE=1`.
3. Confirm migrations `0048_club_portal_foundation.sql`, `0049_portal_audit_hash_verification.sql` and `0050_portal_operations_workflows.sql` are recorded in `schema_migrations`.
4. Run `APP_DIR=/opt/gmcl scripts/verify-portal-staging.sh`. It must report `portal_preflight=ready`, HTTP 200 for health/legacy/admin, HTTP 303 for the unauthenticated portal and HTTP 401 for the unsigned worker request; any non-zero exit blocks pilot enablement.
5. Confirm the application starts with `CLUB_PORTAL_ENABLED=true`. Startup deliberately fails if the effective portal database role can bypass RLS.
6. Verify `/health`, legacy captain login and legacy administrator login before enabling a club.
7. Sign in as the named test Super Administrator and open `/admin/portal`.
8. Confirm the account-security notification panel reports SMTP configured and no unexplained dead-letter item.
9. Activate the test-host copy of the five-minute portal-notification n8n trigger and verify an empty cycle returns HTTP 200 without creating mail.
10. Enable `portal_access` for one synthetic/pilot club.
11. Enable `read_only_dashboard` for that club. A module cannot be enabled until portal access is enabled.
12. Record an official-contact evidence reference and send an invitation to a controlled synthetic/test mailbox.
13. Redeem the invitation through the managed identity provider, choose the acting club/role and open `/portal`.
14. Open `/portal/contexts`; confirm the current role, club-wide or narrower scope and effective dates match the approved appointment, and confirm the current appointment has no redundant switch action.
15. Verify exactly one account-activation security email reaches `EMAIL_OVERRIDE`, contains the test club and session-management URL, and contains no invitation token.
16. Reconcile the action-centre report and sanction totals with direct source queries for the pilot club.
17. Open `/portal/sessions`, revoke a second session, complete same-user strong step-up and exercise all-device revocation.
18. Open `/portal/activity`; reconcile sign-in and context-selection rows with the audit source, then confirm raw action keys, source identifiers, IP/device details and activity from a second synthetic club are absent.
19. Revoke a synthetic appointment and verify its sessions fail immediately and exactly one allowlisted revocation notification is sent without the administrative reason.
20. Exercise logout, expired session, club feature disable, SMTP outage, one retry and provider outage paths.

## Operations-module test journeys

Enable these flags for the controlled test club from `/admin/portal`, after `portal_access` and `read_only_dashboard`:

1. `secure_messaging`
2. `club_self_service`
3. `junior_administration`
4. `player_identity`
5. `registration`
6. `fixture_optimisation`

Use synthetic or explicitly approved non-sensitive content only.

### Secure communication

1. As the club user, open `/portal/messages`, create a normal case and confirm the sender sees one saved club-visible message.
2. In a separate GMCL administrator session, open `/admin/portal/cases`, assign the case, set a deadline and add an internal note.
3. Return to the club session and confirm the assignment/deadline status is visible but the internal note, its author and its timestamp are absent.
4. Reply as GMCL, confirm the club can see and acknowledge it, and confirm the verified case creator receives the official email copy.
5. Simulate SMTP failure, confirm the saved message is marked as needing email attention, restore SMTP and use **Retry official email copy** without creating a second portal message.
6. Disable `secure_messaging`; confirm club case routes fail closed while existing records remain in the GMCL queue.

### Club profile and corrections

1. Open `/portal/club-profile` and submit a synthetic contact version with an evidence reference.
2. From `/admin/portal/cases`, verify or reject that version. Confirm the result appears in club history without overwriting the earlier version.
3. Submit a record-correction request. Confirm the source record is unchanged and a linked communication case exists.
4. Record a GMCL review decision and confirm it is visible to the originating club only.

### Starred-player review

1. Open `/portal/starred-players` and submit a synthetic player, season and published rule/list release.
2. Confirm the request is labelled as a potential issue requiring human review.
3. As GMCL, record a review status, rule release and decision note.
4. Confirm no starred list, sanction or source record changes automatically.

### Junior administration

1. Use only a synthetic verified-adult role; do not enter child contact, medical or safeguarding information.
2. Submit an ordinary administration/acknowledgement request and process it through the GMCL queue.
3. Confirm there is no child-recipient selector, player-photo field or safeguarding submission route.
4. Confirm a role without `junior_administration.manage` cannot submit.

### Player identity

1. Submit a synthetic ambiguity using an external member reference where available.
2. Confirm there is no image upload, biometric matching, roster import or automatic identity merge.
3. Record a human reconciliation decision and confirm the external system remains unchanged.

### Registration

1. Submit a synthetic guided handoff with the registration route, external reference and former-club direct-email status.
2. Confirm there is no Play-Cricket write, registration approval or webhook claim.
3. Record a GMCL review outcome and continue the published external/manual process in parallel.

### Fixture planning

1. Submit club-wide and team-specific hard/soft constraints for the selected season.
2. Attempt a team or season outside the acting scope and confirm the write is denied.
3. Confirm the inventory is tenant-scoped and no fixture has changed.
4. Confirm there is no solver execution or publication control; generated schedules remain outside this slice and can never publish automatically.

## Required test evidence

- Wrong issuer, audience, signature, nonce, state and PKCE are rejected generically; an unfamiliar rotated signing-key ID triggers a fresh JWKS retrieval before a valid token can be accepted.
- Invitation replay, expired invitation and mismatched/unverified email are rejected.
- A Club A identity receives no Club B membership, count, name, source freshness or detail.
- Team-scoped roles cannot access another team in the same club.
- Season/team query filters cannot broaden an appointment; rejected scope identifiers generate a `portal.scope.denied` audit event without confirming whether the identifier exists.
- Revoking the selected appointment denies the next request.
- Changing acting context invalidates the previous session token.
- The appointment inventory lists only the named user's currently effective roles for portal-enabled clubs, renders club-wide and narrower scopes with effective dates, visibly marks the selected appointment and offers switch forms only for other appointments.
- Revoking one owned session does not expose any token and denies that session's next request.
- All-device revocation is denied without recent step-up, rejects an identity switch, increments the account security version and denies every prior token.
- CSRF middleware accepts matching server-rendered form and explicit header tokens, rejects missing or mismatched tokens before the handler runs and does not rotate a valid request's cookie.
- Rate limits refill at the configured rate, isolate IP/path keys, expire idle buckets, evict the least-recently-used bucket at the fixed capacity and serialize concurrent requests without exceeding the allowance.
- Account activity includes the named user's allowlisted lifecycle events for the selected club, renders unknown actions and outcomes generically, and discloses no event from another club even when that event has the same actor user ID.
- Account activity HTML contains no raw audit action key, target or session identifier, metadata field, IP address or user-agent value; its table has a caption and scoped column headers.
- Dashboard counts reconcile to fixtures, submissions and exemptions for the selected club/team/season.
- Club sanction totals equal the displayed team-ledger rows.
- Unlinked legacy sanctions trigger a warning and are not silently double-counted.
- Missing fixture sync produces “Unavailable”; a sync older than 36 hours produces “Stale”.
- Historical season selection retains teams that are inactive today and preserves the source calculation contract effective for the selected records.
- Disabling `portal_access` removes all club acting contexts, switches off its module flags, immediately revokes sessions currently scoped to that club and records the revocation count in the feature-change audit event.
- Reprocessing an account activation or appointment-revocation outbox event creates no duplicate notification row.
- SMTP absence leaves materialized notifications queued and returns fail-closed 503 without logging their body; transient delivery failure records an error and a future retry without losing the source event.
- A stale notification claim can be recovered after ten minutes, a stale final-attempt claim becomes dead-lettered, and a poison or unsupported event stops retrying after five recorded failures.
- Security notification rendering uses an explicit field allowlist: invitation tokens, administrative revocation reasons and arbitrary payload fields never appear in email.
- The strict pilot preflight fails for a missing baseline or step-up ACR, missing/weak worker authentication, missing SMTP, non-HTTPS or mismatched callbacks, unavailable OIDC discovery, incomplete RLS/trigger/migration state, audit-chain gaps or any version-2 canonical hash mismatch.
- The staging verifier fails if the portal redirect leaves the internal login flow, browser security/no-store headers regress, the administrator login is unavailable, any required CSRF cookie flag is absent or an unsigned notification-worker request is accepted.
- Audit-chain head digests and positions are recorded as deployment evidence; version-1 legacy events are visibly counted as link-only verification.
- Legacy captain and administrator regression tests remain green with global and club flags both on and off.
- Keyboard-only use and a 320-pixel viewport remain operable for login, context choice and the action centre.
- Club-visible messages and internal GMCL notes are stored in different tables, loaded by different repository paths and never combined in a club response.
- Every new operations write revalidates the selected assignment, active membership, `portal_access`, module feature and application permission in the transaction.
- All seven operations tables have enabled and forced PostgreSQL row-level security; club reads are tenant-scoped and internal notes have no club read policy.
- Operational review updates remain human decisions. Starred findings do not amend a list or sanction; registration does not write to Play-Cricket; fixture constraints do not run or publish a schedule.
- An SMTP failure does not duplicate a portal message. Its email state is visible to GMCL and the same message can be retried after delivery is restored.

## Validation recorded for this branch

On 26-27 July 2026, the implementation was validated from clean disposable PostgreSQL databases. The foundation record below used migrations through `0049`; the operations slice was subsequently validated through `0050`:

- `go vet ./...` passed.
- `go test ./...` passed on the Windows development host.
- `go test -race ./...` passed in the Linux builder image.
- A clean 54-migration database reported 12/12 portal-private tables with enabled and forced RLS, a live append-only trigger and validated audit-chain constraints. Schema preflight passed with the portal disabled; a fully configured pilot preflight passed against a local synthetic OIDC discovery endpoint, while uppercase `PILOT` with missing dependencies failed closed.
- After the race suite generated audit activity, preflight independently recomputed all 18 version-2 events across two chains and recorded both chain-head digests. A malformed chain-position insert was rejected by PostgreSQL, and a deliberate disposable-database metadata mutation was detected as a canonical hash mismatch with a non-zero preflight exit.
- The database integration suite passed tenant RLS isolation, append-only audit enforcement, signed OIDC ID-token verification, nonce/state/PKCE replay controls, invitation redemption, same-user step-up, context token rotation, individual/all-device session revocation, club kill-switch session revocation with an audited count, dashboard tenant reads, valid and foreign team filters, denied-scope auditing, captain-handoff club/team validation and immediate appointment revocation.
- The OIDC lifecycle passed live RSA signing-key rotation: onboarding verified with the initial key, the synthetic provider rotated to a new key and `kid`, and same-user step-up succeeded only after the cached verifier performed a second JWKS retrieval. The provider harness and complete suite remained clean under the race detector.
- Account-activity unit and database integration tests passed limit clamping, presentation allowlists, same-actor cross-club RLS isolation, semantic table rendering and negative disclosure checks for raw action keys, identifiers, metadata, IP addresses and user agents. The lifecycle fixtures were also made independent of pre-seeded seasons and concurrent package fixtures.
- After the account-activity race suite, schema preflight independently recomputed 21 version-2 audit events across four chains and reported ready.
- Appointment-inventory unit and database-backed handler tests passed current-role detection, club-wide and team/season/competition scope rendering, effective start/end timestamps, escaping of club and CSRF values and suppression of the current appointment's identifier and redundant switch form. The complete Linux race suite remained green, and post-race schema preflight recomputed 21 version-2 audit events across four chains.
- Shared CSRF tests passed token entropy/shape, request-context propagation, matching form/header submission, rejection and constant-time comparison. A production-stage `/admin/login` response emitted `Path=/; HttpOnly; Secure; SameSite=Lax`; complete host and Linux race suites remained green across administrator, captain, sanctions and portal routes.
- Bounded rate-limiter tests passed refill timing, IP/path isolation, idle expiry, LRU eviction, backward-clock recovery, calculated retry guidance and a 100-request concurrent burst under the race detector. In the production-stage container, requests 1–60 to `/admin/login` succeeded and request 61 returned HTTP 429 with `Retry-After: 1`.
- The generic pilot preflight requires an explicit provider-approved baseline ACR and step-up ACR. The Cognito profile instead requires the independently verified User Pool policy and enforces signed `token_use`/`auth_time` evidence in the callback.
- Cognito-focused tests passed for original and updated issuer shapes, confidential-client requirements, rejection of ACR configuration, omission of unsupported `acr_values`, forced-login step-up parameters, ID-token purpose, future/missing authentication times, stale step-up and fail-closed pilot preflight. The complete Go test and vet suites passed on 27 July 2026.
- The portal notification lifecycle passed idempotent event materialization, verified-identity recipient resolution, bounded retry delay, allowlisted activation/revocation templates, successful completion, queue-health aggregation, stale final-lease expiry and poison-event dead-lettering.
- A real authenticated browser journey at a 320 CSS-pixel viewport passed for the action centre, report history, sanction ledger and session security pages. The first keyboard tab reached the skip link; activating it focused `main`; the collapsed navigation remained keyboard-operable; each page exposed one `aria-current="page"` link; tables exposed captions and header scopes; cards stacked; and wide tables scrolled inside their responsive containers without document-level horizontal overflow.
- The production-stage image built successfully, contained no `.env`, started with the restricted runtime role, returned 200 for `/health` and the legacy entry page, and returned fail-closed 503 for `/portal/login` when OIDC was deliberately disabled.
- The production image contained `/bin/portal-preflight` and migration `0049`; Bash syntax, ShellCheck and `docker compose config --quiet` passed for the staging/setup workflow. The expanded verifier passed deterministic external-response mocks and a deliberate missing-`HttpOnly` response produced the expected non-zero, fail-closed result.
- Each disposable test database and application container was removed after validation. Shared PostgreSQL roles were preserved unless positively proven to be test-owned.
- `git diff --check` passed.
- Migration `0050_portal_operations_workflows.sql` applied successfully after the complete preceding migration chain in a disposable PostgreSQL 16/pgvector database. All seven new private tables reported both enabled and forced RLS, and the club-visible-message and internal-note immutability triggers were present. The disposable container was then removed.
- Focused portal and HTTP suites passed after adding the operations repositories, routes, permission matrix, feature controls and GMCL work queue. Run the complete suites again against the final commit before deployment.

## Rollback

The migration is additive and must not be reversed on an in-season test server merely to roll back application code.

1. Set `CLUB_PORTAL_ENABLED=false` and restart the application. The `/portal` routes disappear.
2. Alternatively disable `portal_access` for the pilot club from `/admin/portal`; this also disables every module flag and immediately revokes sessions currently scoped to that club.
3. Keep the portal notification worker running until committed account-security events are drained. If SMTP is unsafe, deactivate only its n8n trigger and preserve the queue for controlled recovery; do not delete pending events or notifications.
4. Preserve portal users, invitations, sessions, notification state and audit history for investigation and reconciliation.
5. Redeploy the previous tested application commit if needed. Existing captain and administrator routes continue to use their original data and authentication.

## Deliberately not enabled or claimed

- Portal-primary official communication: email remains the official record.
- File uploads, malware scanning and evidence downloads.
- Child-recipient messaging, child data capture or any safeguarding route.
- Player photographs, biometric matching, bulk player imports or automatic merges.
- Play-Cricket registration writes or webhooks.
- Fixture solver execution or publication.
- Any Hawk authority to amend, approve, sanction or publish.

The testable workflow slices above do not close their governance, privacy, data-rights or operating-model gates. Those gates remain authoritative in [16-open-questions-and-decisions.md](16-open-questions-and-decisions.md).
