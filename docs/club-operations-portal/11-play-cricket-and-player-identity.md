# Play-Cricket and Player Identity

**Planning baseline:** 26 July 2026
**Status:** Read integration confirmed; player-photo and registration-write outcomes externally blocked

The evidence labels defined in [00-executive-summary.md](00-executive-summary.md) apply throughout this document.

## Conclusion

### Confirmed possible

- The existing application reads fixture lists and scorecard details from the configured Play-Cricket-style API (`internal/leagueapi/client.go:29-169`, `internal/leagueapi/types.go:4-75`).
- The public Players API documentation describes a club-player read returning `member_id` and `name`, defaulting to active squad-role members, with options to include everyone or historic members.
- Play-Cricket provides user-interface workflows for player/league-registration photographs.

### Possible only subject to approval

- A low-frequency player reconciliation feed under the existing or amended API agreement.
- An approved ECB/Play-Cricket export.
- GMCL-hosted or cached photographs if written purpose, access, redistribution, retention and controller terms permit it.
- A time-bound match-day identity view after source rights, DPIA, roles and fallback are approved.

### Not supported by current documented public APIs

- Player photograph bytes or photograph URLs through the documented Players API.
- League-registration status, date of birth or registration category in that documented response.
- A public registration create/update API.
- A public registration webhook.

These statements mean “not identified in the current official public documentation reviewed”, not “technically impossible”. Written confirmation and the actual GMCL agreement are authoritative.

### Requires GMCL process change

- Definition of an active/eligible player for each purpose.
- Approval and correction ownership for photographs.
- A no-photo/manual identity route.
- Match-official appointment and access-window data.
- Privacy notices, retention, incident response and auditing.

### Requires ECB or Play-Cricket agreement

- Any additional player/registration/photo API or export.
- Photograph caching or redisplay.
- Permitted sync frequency, retention, junior restrictions and controller/processor allocations.
- Any registration write or webhook integration.

## Sources reviewed

Reviewed 25 July 2026:

- [Players API](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000467737-Players-API) and its official PDF attachment.
- [API access guidance](https://play-cricket.ecb.co.uk/hc/en-us/articles/115004270145-Do-You-Have-an-API-to-Access-Play-Cricket-Data).
- [League registered player photos — competition sites](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000347558-League-Registered-Player-Photos-Competition-Sites).
- [League registered player photos — club/county board sites](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000347598-League-Registered-Player-Photos-Club-County-Board-Sites).
- [League Registration Process post-GDPR](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000851717-The-League-Registration-Process-post-GDPR).
- [League Registration for Clubs/County Boards](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000852097-League-Registration-for-Clubs-County-Boards).
- [Day-to-Day Registered Players](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000961349-Day-To-Day-Registered-Players).

The full dated source register is in [02-rules-and-external-dependencies.md](02-rules-and-external-dependencies.md).

## Capability assessment

| Question | Public documentation result | Repository result | Planning classification |
|---|---|---|---|
| What does Players API return? | `member_id` and `name` in documented response | No current player client | Confirmed limited read |
| Active squad roles? | Default response described as active squad-role members | Not implemented | Confirmed source concept, not GMCL eligibility |
| League registration status? | Not documented in the response | No model/client | Not supported by current documented endpoint |
| Player photographs or URLs? | Not documented in Players API; UI approval docs exist | No photo client/store | Externally blocked |
| Date of birth? | Not documented in response | No player domain | Not supported by current documented endpoint |
| Registration category? | Not documented in response | Starred source may contain categories but is not registration authority | Not supported by current documented endpoint |
| Write API? | No public registration write endpoint identified | Client is read-only | Unconfirmed/unavailable |
| Webhook? | No public registration webhook identified | SES webhooks only; no Play-Cricket callback | Unconfirmed/unavailable |
| New agreement? | API guidance says access is agreement/key-gated and revocable | Existing key configuration exists, agreement scope unavailable | Must be confirmed in writing |
| Sync frequency? | Guidance is low-traffic, non-real-time and encourages caching/minimal calls | Current fixture/scorecard reads exist | Negotiate exact limits; overnight player reconciliation is the design assumption |
| Retention/controller terms? | Not established by endpoint documentation alone | Agreement/DPA not in repository | External legal/contract dependency |
| Junior restrictions? | Not established for an API/photo reuse design | No implementation | DPIA and written approval required |

## Existing integration

- **Verified fact:** Environment configuration supplies a base URL, site ID and API token; the client issues authenticated GETs for match lists and match detail (`internal/leagueapi/config.go:7-56`, `internal/leagueapi/client.go:29-169`).
- **Verified fact:** Fixture cache and season competition identifiers exist (`migrations/0006_league_fixtures_and_team_mapping.sql:1-33`, `migrations/0017_season_league_competition_ids.sql:1-5`).
- **Recommendation:** Reuse the client's timeout, error and fixture/scorecard provenance patterns, but create a separate player reconciliation adapter only after agreement scope is confirmed.
- **Recommendation:** Do not expose provider tokens to the browser, logs, Hawk or n8n payloads. Use secret management and least-privilege network access.

## “Active player” definition

There is no safe universal `active` boolean. Define purpose-specific predicates:

| Predicate | Proposed definition | Authority |
|---|---|---|
| `source_active_squad_member` | Appears in the latest successful Players API default response for a club | Play-Cricket observation with as-at time; not eligibility |
| `gmcl_registration_current` | Has a GMCL-approved internal registration effective for the date/competition and not superseded/withdrawn | GMCL Registration decision |
| `club_membership_current` | Club appointment/membership source says current | Source agreement to confirm |
| `selected_for_fixture` | Appears on the reconciled team/scorecard for the exact fixture | Fixture/scorecard source |
| `not_known_transferred_away` | No later effective transfer state in reconciled data | GMCL plus external observation |
| `not_suspended_for_fixture` | No effective approved suspension for the date/competition | GMCL sanctions ledger |
| `eligible_for_fixture` | Human-authoritative/deterministic result under exact rule release using complete inputs | GMCL; never inferred solely from Players API |

The match-day list should start from `selected_for_fixture` or an approved pre-match team list, then display the separate registration/photo/source states. It must not label Players API membership as eligibility.

Refresh behavior:

- show last successful source time and staleness threshold;
- keep source observations immutable/as-at for decisions;
- overnight or agreement-permitted synchronization, not per-page provider calls;
- urgent manual reconciliation route for late changes;
- never silently turn stale data into `not active`.

## Integration options

| Option | Technical/accuracy | Contract/data protection | Support/security/cost | Recommendation |
|---|---|---|---|---|
| 1. Authorized Play-Cricket API sync | Strong identifiers if a suitable endpoint exists; current Players response is minimal | Agreement scope, retention, junior/photo terms required | Maintain adapter, caching, rate limits and reconciliation | Use for permitted reads only after written confirmation |
| 2. Approved ECB data export | Can provide stable batch snapshot if officially supplied | Requires documented fields, controller roles and secure transfer | Batch validation/replay; manual delivery risk | Viable fallback/pilot subject to agreement |
| 3. Deep links to Play-Cricket screens | Low technical integration; source remains visible | Must use supported links and user's own account | Context switching and status reconciliation burden | Recommended near-term handoff |
| 4. Club-managed GMCL photo upload | Technically feasible with private scanning/storage | GMCL becomes responsible for purpose, notices, approval, corrections and retention | High support and duplicate/outdated-photo risk, especially juniors | Do not start without DPIA/policy |
| 5. GMCL approval of club photos | Adds quality/status control | Same rights/lawful-basis issues; approver accountability | Significant operations queue and appeals | Possible only as separately approved process |
| 6. Hybrid Play-Cricket IDs + GMCL photos | Good matching if IDs are stable and authorized | Requires both identifier and GMCL-hosting rights | Reconciliation complexity; strong access controls | Possible subject to agreements/DPIA |
| 7. Manual/CSV import | Fast prototype for non-photo identifiers | Official export and transfer terms still required | Staleness, human error, secure deletion and support | Controlled interim reconciliation only |

Scraping, shared administrator credentials and browser automation are prohibited.

## Recommended approach

### Stage 1: identity reconciliation without photos

1. Obtain and review the GMCL API agreement and data dictionary.
2. Import only permitted `member_id`/name observations at low frequency.
3. Map external references to internal player records through a restricted reconciliation queue.
4. Use scorecard reads for fixture participation.
5. Display source/as-at and ambiguity; do not infer eligibility.

### Stage 2: written photo feasibility

Ask ECB/Play-Cricket to confirm endpoint/export, purpose, competitions, controller roles, photo approval/status, junior coverage, caching/redisplay, retention, security, access logs, correction and termination. Complete DPIA and GMCL photo policy.

### Stage 3: match-day pilot

Only if authorized:

- a small senior competition/pilot;
- named appointed umpires/match officials or an explicitly approved opposing-captain role;
- minimal fields;
- exact fixture and short access window;
- no bulk export/download;
- mobile security and accessibility testing;
- manual fallback and incident support;
- measured missing/outdated/duplicate rates.

## Match-day identity view

Potential display, when each field is authorized:

- approved photograph;
- full player name;
- club/team and exact fixture;
- GMCL/reference identifier;
- separate registration and photo approval states;
- relevant confirmed/potential eligibility warning with rule citation;
- last synchronized time.

Access policy:

- user has a current appointment for the fixture or approved restricted role;
- window begins a configured period before and ends shortly after the fixture;
- step-up for photo access if risk assessment requires;
- server checks every player row belongs to fixture roster;
- rate limits per user/session/fixture;
- `Cache-Control: no-store, private`, CSP and anti-indexing;
- no bulk API, print/export, sequential identifier browsing or public URL;
- short-lived image authorization, watermark only if useful and accessible;
- audit each roster/photo view at proportionate granularity;
- revoke immediately on appointment/fixture change.

No facial recognition, embeddings, automated face comparison or identity score is proposed.

## Exceptions and fallback

| Condition | Required treatment |
|---|---|
| Missing photo | Show `No authorized photo available`, not a silhouette that implies verification; use approved manual identity route |
| Rejected/expired photo | Do not show it as current; state reason category only where permitted and route correction |
| Duplicate/same name | Display stable authorized reference; create reconciliation task; do not merge |
| Late team change | Require source refresh or authorized temporary roster event with audit |
| Transferred/withdrawn player | Preserve historic match evidence but remove future access/results according to effective state |
| Junior in senior cricket | Apply approved photo/rule policy and DPIA; no extra junior fields |
| Junior not in senior cricket | Resolve the current published guidance conflict; do not automate |
| Provider outage/stale feed | Show as-at/stale warning and manual process; do not deny eligibility automatically |
| No provider photo agreement | Keep the match-day photo feature disabled and use existing manual procedure |

## External questions for ECB/Play-Cricket

1. Which current endpoints/exports may GMCL use for members, league registration, categories, photos and approval status?
2. Does “active squad role” mean only current club role, and what inclusion/exclusion rules and refresh latency apply?
3. Is there a league-registration identifier/status and its effective history?
4. Are photograph bytes/URLs available; are they stable, authenticated and approval-aware?
5. May GMCL cache or redisplay photographs to appointed match officials/opposing captains? For how long and on what devices?
6. What rights, notices and controller/processor responsibilities apply, particularly to juniors?
7. Are dates of birth or registration categories available, and for what approved purpose?
8. Is any registration create/update API or webhook available under a separate agreement?
9. What request frequency, batch size, retention, security, audit and deletion requirements apply?
10. How are corrections, transfers, withdrawals, consent/objection and agreement termination communicated?

## Tests and production gates

- Contract tests against an approved sandbox/fixture, never production scraping.
- Rate-limit, retry, caching and idempotent import tests.
- Ambiguous/duplicate/same-name reconciliation scenarios.
- Source correction and historic as-at replay.
- Cross-club and fixture-window authorization.
- Photo harvesting, sequential ID, cache, referrer and signed-URL tests.
- Missing/outdated/rejected photo and provider outage fallbacks.
- DPIA, DPA/agreement, accessibility and incident exercise.

**Blocking:** The agreement, API fields, photo rights and DPIA must be approved before player-photo implementation. The read-only club MVP does not depend on them.
