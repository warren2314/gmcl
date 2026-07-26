# Club Portal Implementation and Test-Server Runbook

**Implementation branch:** `codex/club-operations-portal`

**Implementation baseline:** 26 July 2026

**Current delivery slice:** identity/tenancy foundation and feature-flagged read-only action centre

This runbook records implemented behaviour and the controlled route to a test-server pilot. It does not remove or replace the existing captain-report or administrator services.

## Implemented in this slice

- Provider-neutral managed OpenID Connect client using Authorization Code flow, PKCE, discovery and verified ID tokens.
- Issuer, audience, signature, expiry, state, nonce and PKCE enforcement.
- Single-use approved onboarding invitations; the verified provider email must match the approved official-contact email.
- Separate users, external identities, club memberships and effective-dated role assignments.
- Multi-club acting-context selection with bearer-token rotation when the context changes.
- Opaque, hashed, server-side sessions with 30-minute idle and 12-hour absolute defaults.
- User-visible active-session inventory, immediate per-session revocation and all-device revocation through account security-version invalidation.
- Provider-enforced step-up for sensitive account actions using `prompt=login`, `max_age=0` and the approved step-up ACR; the returned identity must match the initiating named user.
- Deny-by-default, application-owned permissions with club/team/season/competition containment.
- A restricted `gmcl_portal_runtime` PostgreSQL role, tenant transaction context, forced RLS on portal-private tables and a startup refusal if the effective role can bypass RLS.
- Append-only, hash-chained audit events without raw authentication or invitation secrets.
- Per-club feature flags and a Super Administrator pilot-control page at `/admin/portal`.
- Super Administrator controls to revoke unused invitations and effective-dated appointments; appointment revocation immediately invalidates every session using that role and emits an audited outbox event.
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
CLUB_PORTAL_OIDC_ISSUER=https://PROVIDER_ISSUER
CLUB_PORTAL_OIDC_CLIENT_ID=SECRET_VALUE
CLUB_PORTAL_OIDC_CLIENT_SECRET=SECRET_VALUE
CLUB_PORTAL_OIDC_REDIRECT_URL=https://TEST_HOST/portal/auth/callback
CLUB_PORTAL_OIDC_REQUIRED_ACR=PROVIDER_APPROVED_ACR
CLUB_PORTAL_OIDC_STEP_UP_ACR=PROVIDER_APPROVED_STEP_UP_ACR

SMTP_HOST=CONFIGURED_TEST_SMTP
SMTP_FROM=CONFIGURED_TEST_SENDER
EMAIL_OVERRIDE=CONTROLLED_TEST_MAILBOX
```

`CLUB_PORTAL_OIDC_ALLOW_INSECURE` must remain false on the test server. Configure passkeys as the provider's preferred authenticator and password plus TOTP as the accessible fallback.

## Deployment sequence

1. Back up the test database.
2. Deploy the exact tested branch commit with `MIGRATE=1`.
3. Confirm migration `0048_club_portal_foundation.sql` is recorded in `schema_migrations`.
4. Confirm the application starts with `CLUB_PORTAL_ENABLED=true`. Startup deliberately fails if the effective portal database role can bypass RLS.
5. Verify `/health`, legacy captain login and legacy administrator login before enabling a club.
6. Sign in as the named test Super Administrator and open `/admin/portal`.
7. Enable `portal_access` for one synthetic/pilot club.
8. Enable `read_only_dashboard` for that club. A module cannot be enabled until portal access is enabled.
9. Record an official-contact evidence reference and send an invitation to a controlled synthetic/test mailbox.
10. Redeem the invitation through the managed identity provider, choose the acting club/role and open `/portal`.
11. Reconcile the action-centre report and sanction totals with direct source queries for the pilot club.
12. Open `/portal/sessions`, revoke a second session, complete same-user strong step-up and exercise all-device revocation.
13. Exercise logout, expired session, role revocation, feature disable and provider outage paths.

## Required test evidence

- Wrong issuer, audience, signature, nonce, state and PKCE are rejected generically.
- Invitation replay, expired invitation and mismatched/unverified email are rejected.
- A Club A identity receives no Club B membership, count, name, source freshness or detail.
- Team-scoped roles cannot access another team in the same club.
- Season/team query filters cannot broaden an appointment; rejected scope identifiers generate a `portal.scope.denied` audit event without confirming whether the identifier exists.
- Revoking the selected appointment denies the next request.
- Changing acting context invalidates the previous session token.
- Revoking one owned session does not expose any token and denies that session's next request.
- All-device revocation is denied without recent step-up, rejects an identity switch, increments the account security version and denies every prior token.
- Dashboard counts reconcile to fixtures, submissions and exemptions for the selected club/team/season.
- Club sanction totals equal the displayed team-ledger rows.
- Unlinked legacy sanctions trigger a warning and are not silently double-counted.
- Missing fixture sync produces “Unavailable”; a sync older than 36 hours produces “Stale”.
- Historical season selection retains teams that are inactive today and preserves the source calculation contract effective for the selected records.
- Disabling `portal_access` removes all club acting contexts and switches off its module flags.
- Legacy captain and administrator regression tests remain green with global and club flags both on and off.
- Keyboard-only use and a 320-pixel viewport remain operable for login, context choice and the action centre.

## Validation recorded for this branch

On 26 July 2026, the implementation was validated from a clean disposable PostgreSQL database using all migrations through `0048`:

- `go vet ./...` passed.
- `go test ./...` passed on the Windows development host.
- `go test -race ./...` passed in the Linux builder image.
- The database integration suite passed tenant RLS isolation, append-only audit enforcement, signed OIDC ID-token verification, nonce/state/PKCE replay controls, invitation redemption, same-user step-up, context token rotation, individual/all-device session revocation, dashboard tenant reads, valid and foreign team filters, denied-scope auditing, captain-handoff club/team validation and immediate appointment revocation.
- The production-stage image built successfully, contained no `.env`, started with the restricted runtime role, returned 200 for `/health` and the legacy entry page, and returned fail-closed 503 for `/portal/login` when OIDC was deliberately disabled.
- Each disposable test database, container and temporary database role was removed after validation.
- `git diff --check` passed.

## Rollback

The migration is additive and must not be reversed on an in-season test server merely to roll back application code.

1. Set `CLUB_PORTAL_ENABLED=false` and restart the application. The `/portal` routes disappear.
2. Alternatively disable `portal_access` for the pilot club from `/admin/portal`; this also disables every module flag for that club.
3. Preserve portal users, invitations, sessions and audit history for investigation and reconciliation.
4. Redeploy the previous tested application commit if needed. Existing captain and administrator routes continue to use their original data and authentication.

## Deliberately not enabled yet

- Portal-primary official communication
- Secure cases/messages and internal notes
- Club contact self-service and correction requests
- Junior or safeguarding workflows
- Player identity/photos
- Registration writes
- Fixture solver or publication
- Any Hawk authority to amend, approve, sanction or publish

These remain subsequent roadmap slices and retain the delivery gates in [16-open-questions-and-decisions.md](16-open-questions-and-decisions.md).
