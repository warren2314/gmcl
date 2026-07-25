# GMCL Maintenance Plan

## Current architecture

- Go 1.25 server application using `net/http`, chi routing, pgx, PostgreSQL 16,
  server-rendered HTML, Bootstrap 5, HTMX and Chart.js.
- Public captain, submission-status, sanctions and rules-assistant journeys share
  the same process as the authenticated administration portal and HMAC-protected
  internal automation endpoints.
- Authentication uses signed captain/admin cookies, CSRF protection, rate
  limiting, email 2FA, role checks and fine-grained sanctions/umpire permissions.
- Database migrations are append-only and currently run through `0047`.
- UI, SQL queries, calculations and HTML rendering are largely colocated in
  `internal/httpserver`. Several handler files exceed 1,000 lines; `admin.go`
  exceeds 2,600 lines.
- Styling is Bootstrap plus a shared `static/css/brand.css`. Navigation is built
  centrally in `layout.go`, but page composition and state handling are repeated.

## Main problems discovered

1. **Incorrect week display:** the dashboard resolves the scheduled database week
   and then subtracts `seasons.compliance_start_week`. With competition week 15
   and compliance tracking starting at week 14, it renders “Week 2”. A compliance
   reporting window must never renumber the competition schedule.
2. **No authoritative competition context:** current-week SQL is duplicated across
   dashboard, compliance, fixtures, reports, sanctions, umpire views, public
   status, captain-link generation and internal jobs. The copies use different
   active/past/upcoming priorities and rely on the database session's
   `CURRENT_DATE`, rather than an explicit Europe/London date.
3. **Misleading dashboard metrics:** “This Week” compares distinct submitting teams
   with every active team, while the compliance workspace correctly works in
   fixture-report units and accounts for byes, Friday exclusions, double headers,
   legacy submissions and exemptions. The headline compliance percentage uses
   `elapsed weeks × active teams`, so it does not reconcile with fixture demand.
4. **Navigation reflects implementation growth:** top-level links and large
   dropdowns mix operational workflows, reference data, specialist compliance,
   sanctions, reporting and system administration. Some menu items are rendered
   for users who cannot open their permission-protected route.
5. **Dashboard interaction is inconsistent:** most KPI cards animate on hover but
   are not links; only a small nested sanctions link is actionable. Charts lack
   visible scope/date context, accessible summaries and drill-down destinations.
6. **Presentation is inconsistent:** repeated inline styles, emoji icons with
   encoding damage, mixed container widths, dense desktop-only tables and ad-hoc
   cards make pages feel unrelated. Loading and error handling is frequently
   silent when a query fails.
7. **Local-run configuration is fragile:** the checked-in Compose app consumes a
   host-only `localhost` DSN, so the standard stack cannot connect from the app
   container. The persisted local database was also only migrated through `0007`
   until the isolated maintenance run applied the normal migrations.
8. **Maintainability/performance risk:** repeated SQL and large render handlers
   make data definitions hard to compare. Several pages issue many sequential
   queries and ignore scan/query errors. No dead code will be removed until route,
   permission, call-site and asset usage are all traced.

## Proposed information architecture

- **Overview:** operational dashboard with authoritative season/week context,
  attention queue, headline metrics, recent activity and quick actions.
- **Match Operations:** fixtures; weekly compliance; submissions; reminders;
  teams and captains; captain-form preview.
- **Performance:** club rankings; umpire rankings (permission-aware); pitch data.
- **Discipline:** case dashboard; decisions; follow-up tasks; imports; recipients;
  automation safety; legacy card ledger; public-register link.
- **Reports:** executive report; generated reports; missing-submission/card report
  where authorised.
- **Competition Administration (super admin):** Play-Cricket sync; weeks/season
  setup; starred players and replacements; legacy data imports.
- **System (super admin):** email/link health; security and privacy; GDPR; form
  settings; users and access.
- **Account:** password and sign-out.

Role and permission checks remain authoritative at the route layer and will also
govern discoverability in navigation and dashboard actions.

## Implementation phases

1. Introduce a shared, Europe/London-aware competition-week resolver that returns
   the real scheduled week number plus active/upcoming/completed status. Replace
   high-impact duplicated defaults without changing explicitly selected weeks.
2. Extract a fixture-aware dashboard summary service and reuse the established
   compliance definition for expected, submitted, exempt and missing reports.
   Add reconciliation tests and explicit scope labels.
3. Rebuild the dashboard around overview, attention, activity and quick-action
   sections. Make cards either full-card links with focus states or clearly
   non-interactive. Add chart descriptions, empty/error states and drill-downs.
4. Reorganise the shared admin navigation into workflow-based groups, preserving
   every route and hiding links the current user cannot use.
5. Expand shared design tokens and components in `brand.css`; remove damaged emoji
   glyphs from core UI, standardise spacing, headings, cards, tables, badges,
   focus states and responsive behaviour.
6. Incrementally extract oversized handler sections, repeated query/calculation
   helpers and render helpers. Remove only items proven unused by route and
   call-site inventory plus tests.
7. Verify desktop, tablet and mobile layouts; keyboard/focus behaviour; empty,
   loading, error and permission-denied states; console errors; links and routes.

## High-risk areas

- Captain magic-link week selection and token revocation.
- Compliance, exemptions, double headers, byes and sanction generation.
- Role and fine-grained permission visibility.
- Season boundaries, gaps between scheduled weeks and London DST/date rollover.
- Legacy submissions without a Play-Cricket match ID.
- Starred-player and sanction case-management workflows.
- Production data reconciliation cannot be inferred from the small local seed;
  unresolved production-only discrepancies must name the exact source records.

## Regression test inventory

- Admin password, 2FA, forced-password-change, session and logout flows.
- Captain magic-link request/redeem, delegate access, autosave and submit.
- Super-admin, admin and permission-specific route/navigation visibility.
- Active, upcoming and completed competition-week resolution at London date
  boundaries; displayed week must always equal `weeks.week_number`.
- Fixture-aware dashboard/compliance totals including bye, Friday, double-header,
  exemption and legacy-submission cases.
- Dashboard filters, cards, charts, accessible summaries and drill-down links.
- Submission search/detail/export/import and existing validation.
- Sanction case lifecycle, legacy cards, email approval and permissions.
- Reports, fixtures, reminders, rankings, teams/captains and starred-player flows.
- Mobile/tablet/desktop navigation, tables, forms and focus states.
- All public/internal routes, security headers, CSRF, HMAC and rate limiting.
- `go test ./...`, `go vet ./...`, production build, local health check, browser
  console, broken-link crawl and final route inventory comparison.
