# GMCL Club Operations Portal - Executive Summary

**Planning baseline:** 26 July 2026
**Status:** Implementation-ready discovery and architecture recommendation
**Change boundary:** This pack plans future work only. It does not change the running application.

## Evidence labels

- **Verified fact** - supported by repository evidence or a current official source.
- **Assumption** - used to make the plan coherent but not yet confirmed by GMCL.
- **Recommendation** - the proposed product or technical decision.
- **External dependency** - requires another organisation, agreement, procurement, policy or source.
- **Open question** - recorded with an owner and decision gate in [16-open-questions-and-decisions.md](16-open-questions-and-decisions.md).

## Vision

Create one secure GMCL product in which named individuals can perform league, club and team duties with the minimum data and authority their current appointment requires. The portal should make operational work visible and accountable without allowing clubs to silently rewrite official GMCL records or weakening the existing captain-report and sanctions services.

The portal is an action centre rather than a collection of statistics. Every actionable item should identify its season, team or club scope, source, status, governing rule, effective date, deadline and permitted next action.

## Verified current state

- The application is a Go server-rendered modular monolith using `net/http`, chi and PostgreSQL. It exposes public, captain, administrator and HMAC-protected internal route groups (`internal/httpserver/router.go:43-157`).
- Captains use team/week-scoped magic links and a signed two-hour cookie; administrators use a separate password, emailed code and signed eight-hour cookie (`internal/auth/magic.go:35-179`, `internal/auth/admin.go:73-202`, `internal/httpserver/admin.go:364-393`, `internal/httpserver/captain.go:1320-1395`).
- The database distinguishes clubs from teams and stores captain reports at team level (`migrations/0001_core_schema.sql:22-92`).
- The current general role model is `admin` or `super_admin`, with a small permission table and a hard-coded configured super-administrator identity (`migrations/0021_admin_roles_and_email_events.sql:1-8`, `migrations/0026_admin_user_permissions.sql:1-13`, `internal/httpserver/admin.go:2541-2647`).
- Missed-report calculations derive one requirement per team/fixture and preserve approved exemptions (`internal/httpserver/dashboard_data.go:8-116`).
- Sanctions now have effective-dated policy, cases, revisions, append-only events, a team/club ledger and a notification outbox, while a linked legacy sanctions table remains during transition (`migrations/0038_sanctions_case_management.sql:7-424`).
- The application imports fixtures and scorecards through read-only Play-Cricket-style APIs and imports the published starred-player list from a Google Sheet (`internal/leagueapi/client.go:29-169`, `internal/starred/store.go:24-74`).
- Hawk AI ingests versioned rule releases and stores rule documents, chunks, citations and answer audits. Its authenticated record lookups are currently administrator-wide or captain-own-team (`migrations/0035_rules_assistant.sql:3-107`, `internal/httpserver/rules_assistant.go:121-227`).
- Deployment is Docker Compose on a DigitalOcean host behind Caddy, with PostgreSQL and n8n on the internal network. CI validates migrations, tests, vet, security tools and the production image before deployment (`docker-compose.yml:1-61`, `.github/workflows/ci.yml:1-121`).

## Current problems

1. There is no named portal identity that can hold several club, team, competition or seasonal appointments.
2. Stateless cookies cannot provide device lists, immediate session revocation or reliable stale-role invalidation.
3. Club authorization does not exist; adding club routes to the current administrator model would create a severe cross-club disclosure risk.
4. Generic cases, messages and internal notes do not exist. The sanctions case subsystem is useful precedent but is not a general club inbox.
5. Club, team, competition, division and season relationships are incomplete for fixture, registration and starred-player workflows.
6. Player identity and registration are not first-class domains.
7. Current evidence storage is local private disk with size and declared MIME checks, but no content-signature verification, malware scanning or object-storage lifecycle (`internal/httpserver/sanctions_cases.go:447-453`, `internal/httpserver/sanctions_cases.go:1177-1214`).
8. General audit logs are ordinary mutable rows and are deleted by the current 365-day default cleanup; the sanctions append-only model is stronger (`migrations/0002_auth_tokens_audit.sql:74-91`, `internal/httpserver/admin_security.go:113-165`).
9. Published GMCL procedures still make email primary and require direct-email steps for some processes.
10. Play-Cricket's public API documentation does not establish registration writes, photograph retrieval or photograph redistribution.

## Recommended authentication model

Use a managed OpenID Connect identity provider, subject to procurement, security review and a UK GDPR data-processing agreement.

- Passkeys/WebAuthn are the preferred method.
- Password plus TOTP is the supported fallback.
- Email links are restricted to invitations and controlled recovery; they are not the routine sign-in method.
- GMCL staff and Club Primary Administrators must enrol a passkey or, where unavailable, password plus TOTP.
- Sensitive operations require recent step-up authentication.
- PostgreSQL remains authoritative for memberships, appointments and permissions.
- The application maintains revocable server-side sessions and device history.
- A generic club email may receive notices but cannot be a shared login.

The provider capability and recovery requirements are decision-complete in [05-authentication-adr.md](05-authentication-adr.md); commercial provider selection remains a blocking GMCL procurement decision.

## Recommended MVP

The MVP is the smallest release that establishes a safe tenancy boundary and gives clubs immediate operational value:

1. Named identities, invitations, club memberships, scoped role assignments and revocable sessions.
2. Verified Club Primary Administrator onboarding and controlled administrator changes.
3. Read-only club action centre for teams, report requirements, submissions, exemptions, team-level cards, sanctions and deadlines.
4. Source-record drill-down, audit timeline and correction-request workflow for official data.
5. Secure cases and club-visible messages with assignment, acknowledgement, restricted attachments and separately stored internal notes.
6. Portal notifications while email remains the official communication channel.
7. Feature flags, pilot clubs, support playbooks, accessibility and authorization tests.

Not in the MVP: club-authored starred lists, junior data, player photographs, end-to-end player registration or fixture optimisation.

## Recommended delivery order

| Order | Programme | Relative size | Release condition |
|---|---|---:|---|
| 1 | Identity and tenancy foundation | XL | IdP/DPA approved; authorization and revocation tests pass |
| 2 | Read-only club portal and action centre | L | Club/team data reconciled; pilot clubs approve totals |
| 3 | Secure communication and corrections | L | Internal-note isolation and email parallel-running gates pass |
| 4 | Club self-service and starred players | L | Versioned rules and human-review policy approved |
| 5 | Junior administration | M | DPIA, adult-contact directory and safeguarding boundary approved |
| 6 | Player identity | XL, externally blocked | ECB/Play-Cricket agreement and photo DPIA approved |
| 7 | Registration redesign | XL | Rule changes, process owners and external handoff agreed |
| 8 | Fixture optimisation | XL standalone programme | Current process and constraint catalogue signed off |

No calendar estimate should be attached until GMCL identifies delivery capacity and named product, engineering, security, data-protection and operational owners.

## Major blockers and external approvals

- Managed identity-provider procurement, security review and DPA.
- GMCL approval of who can appoint or remove Club Primary Administrators.
- Rule or policy amendment before portal records replace email under Rule 1.5.
- Rule amendment before a portal response replaces the direct former-club email required by Rule 3.1.2.1.2.5.1.2.
- Written Play-Cricket/ECB confirmation of GMCL's existing agreement, permitted data, rate, retention, photographs, redistribution, writes and webhooks.
- Approved lawful bases, retention schedule and DPIAs for junior data, photos, registration documents and any safeguarding metadata.
- Access to current Google Forms, spreadsheets, fixture workbooks, historic fixture decisions and production reconciliation extracts.

## Highest technical risks

1. Cross-club leakage caused by missing or inconsistent tenant scope.
2. Stale or compromised sessions retaining privileged access.
3. Incorrect reconciliation between legacy sanctions, case revisions and team-level ledger totals.
4. Sensitive attachments or player photographs being exposed, retained or processed unsafely.
5. Hawk AI or external integrations crossing authorization or data-controller boundaries.

## Highest operational and rules risks

1. Treating the portal as official communication before Rule 1.5 and operating procedures change.
2. Replacing a mandatory direct transfer email without a Rule 3.1 amendment.
3. Assuming Play-Cricket supports undocumented data or write operations.
4. Expanding junior or safeguarding data access beyond demonstrable need.
5. Automating an incomplete or disputed fixture process.

## Production-readiness principles

- Pilot and feature-flag every externally visible phase.
- Preserve current captain links, forms, submissions and sanctions during migration.
- Use additive, backwards-compatible migrations with rehearsed rollback and reconciliation.
- Require authorization, privacy, accessibility and operational sign-off for each phase.
- Never automatically sanction, approve a registration or publish a fixture from an AI or optimiser result.
