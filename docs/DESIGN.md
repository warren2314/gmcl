# Design and architecture

## Magic link behaviour

### Workflow context

- Email after match (Sat/Sun); weekly report Wednesday.
- Captains may request a link on Saturday or Sunday and use it over several days; a same-day expiry would be a support burden.

### Expiry rule (Europe/London)

- **If link requested on Saturday, Sunday, Monday, Tuesday or Wednesday**  
  Expires **that Wednesday 23:59:59** Europe/London.

- **If requested on Thursday or Friday**  
  Expires **next Wednesday 23:59:59** (keeps a consistent weekly window).

- **Hard cap**: No token lives longer than **7 days** (safety limit).

- **Floor**: If computed TTL is under 10 minutes (e.g. clock skew), use **10 minutes** so the link is still usable.

Implementation: `internal/timeutil.NextWednesdayExpiry(now, loc)`; London TZ loaded once at startup (`Server.LondonLoc`).

### Single-use and revocation

- Tokens are **single-use**: `used_at` set on redeem; `ConsumeMagicToken` checks and updates in one transaction.
- **Revocation on re-issue**: When a captain requests a new link, any **unused** tokens for that same (captain_id, season_id, week_id) are invalidated in the **same transaction** as inserting the new token. So only the latest link is valid (“latest link wins”).

### Throttling

- **3 per hour** per captain (per season/week): avoids abuse; same generic response when limit hit.
- **10 per week** (rolling 7 days) per captain: avoids “I kept clicking send” without revealing throttle to user.

Counts come from `magic_link_send_log`; limits are applied before creating a new token.

### Why this is acceptable for security

- Token is single-use and bound to captain + team/week.
- Per-captain throttling and optional “max one active token per captain/week” (enforced by revocation).
- Human “Continue” click before redeem reduces pre-click consumption.
- Tokens not exposed in logs.
- 3–5 day effective TTL fits the operational window without acting as a long-lived credential.

---

## Security

### CSRF

- Double-submit: token in cookie (readable by JS) and in form/header; server compares both.
- New token generated when none present; stored in context and set on cookie (no variable shadowing: same `token` used for context and cookie).
- Validated on POST/PUT/DELETE/PATCH for admin and captain routes.

### Internal HMAC (n8n → app)

- Signature over: `ts|nonce|method|path|Content-Type|body`.
- Body read with `MaxBytesReader(1MB)`.
- Nonce cache with TTL and size cap to prevent replay.
- Used for `/internal/send-reminders` and `/internal/generate-weekly-report`.

### Audit

- `audit_logs`: actor (admin_user_id, captain_id, or “n8n”), action, resource, metadata (e.g. request_id from `X-Request-ID`).
- Covers: magic_link requested/redeemed, draft_autosaved, submission_created, admin login/2FA, csv_captains_preview/apply, internal_send_reminders, internal_generate_weekly_report.

### Captain session

- Cookie Path=`/captain`; HMAC-signed; contains aud, jti, iat; 2h expiry. On logout (e.g. after submit), clear with same Path.

---

## Database

- **Migrations**: Applied in order (0001 → 0002 → 0003). Core entities: seasons, weeks, clubs, teams, umpires, captains; submissions and drafts; magic_link_tokens and magic_link_send_log; admin_users and admin_2fa_codes; csv_preview_tokens; ranking_config, report_config; audit_logs, ai_summaries.
- **Drafts**: Keyed by (season_id, week_id, team_id); fits “link valid until Wednesday” — captain can request again within the window and resume.

---

## Known gaps / future work

- **Admin**: “No active week” banner when no week has `CURRENT_DATE` between start_date and end_date.
- **Admin**: Lockout after N failed logins (e.g. 15 min); reset on successful 2FA; log lockouts to audit.
- **Admin CSV**: Bulk lookups for club/team validation (collect unique names, 1–2 queries instead of per-row).
- **Admin CSV**: Stricter rate limit (e.g. 10/min on `/admin/csv/*`).
- **Norm name**: Single normalisation (trim, collapse whitespace, casefold) for CSV vs DB comparison.
- **Captain logout**: Ensure cookie clear on submit uses Path=`/captain`.
