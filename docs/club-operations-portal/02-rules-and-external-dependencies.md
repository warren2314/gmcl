# Rules and External Dependencies

**Official-source review date:** 26 July 2026
**Rule principle:** Every automated decision is linked to an immutable rule release and season. A change to the current rule set must not alter a historical decision.

## Source register

| Source | Version/date visible | Review result |
|---|---|---|
| [GMCL rules main menu](https://www.gtrmcrcricket.co.uk/pages/rules-main-menu) | Page dated 12 March 2026 | Current 2026 menu and amendment summary |
| [Rule 1.5 - Communications](https://www.gtrmcrcricket.co.uk/pages/rules-1-5) | Contact references updated 2025; no formal release identifier | Email remains primary and response-bearing communication is by email |
| [Rule 3.1 - Player registration](https://www.gtrmcrcricket.co.uk/pages/rules-3-1) | Updated 16 February 2026 | Play-Cricket plus selected GMCL form; direct former-club transfer email |
| [Rule 3.5 - Starred players](https://www.gtrmcrcricket.co.uk/pages/rules-3-5) | Reviewed/updated 15 April 2026 | List A/B, availability, review, thresholds and exemptions |
| [Junior rules](https://www.gtrmcrcricket.co.uk/pages/rules-junior) | Current page viewed; no formal release identifier | Team/player registration, junior photos and safeguarding-related match media |
| [Penalties menu](https://www.gtrmcrcricket.co.uk/pages/rules-pen-menu) | Current 2026 page; no formal release identifier | Card system is per team; club-level escalation is separate |
| [Transfers and new players](https://www.gtrmcrcricket.co.uk/pages/transfers) | Current 2026 page | User-facing registration summary and existing forms |
| [Starred players page](https://www.gtrmcrcricket.co.uk/pages/starred-players-latest) | Current page | Published list/process summary |
| [Photo required](https://www.gtrmcrcricket.co.uk/pages/photo-required) | Current page | Senior photo eligibility requirement and junior exception statement |
| [Safeguarding](https://www.gtrmcrcricket.co.uk/pages/safeguarding) | Current page | Official safeguarding contacts and external official route |
| [Play-Cricket API section](https://play-cricket.ecb.co.uk/hc/en-us/sections/360000978518-API-Experienced-Developers-Only) | Current help centre | Read-oriented public endpoint catalogue |
| [Players API PDF](https://play-cricket.ecb.co.uk/hc/en-us/article_attachments/360000847657) | Undated one-page specification | Member ID and name; active/historic role filters |
| [Play-Cricket API access](https://play-cricket.ecb.co.uk/hc/en-us/articles/115004270145-Do-You-Have-an-API-to-Access-Play-Cricket-Data) | Current help centre | Agreement required; low-traffic/overnight use |
| [League player photos](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000347558-League-Registered-Player-Photos-Competition-Sites) | Help article, workflow originating in 2018 | UI photo nomination and league approval |
| [Registered players](https://play-cricket.ecb.co.uk/hc/en-us/articles/360000961349-Day-To-Day-Registered-Players) | Current help centre | Admin UI status/category/DOB filters and downloads |
| [NCSC passkeys](https://www.ncsc.gov.uk/passkeys) | Current page | Prefer passkeys over passwords |
| [NCSC recommended MFA](https://www.ncsc.gov.uk/collection/mfa-for-your-corporate-online-services/recommended-types-of-mfa) | Version 2.0, reviewed 26 September 2024 | FIDO2 first; TOTP above message-based MFA |
| [ICO data sharing and children](https://ico.org.uk/for-organisations/uk-gdpr-guidance-and-resources/data-sharing/data-sharing-a-code-of-practice/data-sharing-and-children/) | Under review following Data (Use and Access) Act | Best interests, high privacy default and DPIA |
| [ICO storage limitation](https://ico.org.uk/for-organisations/uk-gdpr-guidance-and-resources/data-protection-principles/a-guide-to-the-data-protection-principles/storage-limitation/) | Under review following Data (Use and Access) Act | Retain personal data no longer than necessary |

**External dependency:** GMCL must introduce a formal rule-release identifier or approved snapshot process. Page update dates alone are not sufficient for long-lived automated decisions.

## Rules and dependencies matrix

| Rule/source | Requirement | Competition / role | Required data | Current support | Proposed support | Decision owner | Automation / approval | Amendment? |
|---|---|---|---|---|---|---|---|---|
| 1.5.1.1 | Email is the primary league communication tool | All; GMCL and club officials | Official contact addresses, delivery evidence | SMTP/SES email; no portal inbox | Portal cases plus email-primary parallel record | GMCL Board/Secretary | Notification automated; official meaning remains human/policy | **Yes** before portal-only official record |
| 1.5.2.1 | Communications requiring a response should be by email | All response workflows | Message, recipients, reply/acknowledgement | Direct email only | Portal response mirrored or summarized by email during transition | GMCL Board/Secretary | Portal tracking automated; response human | **Yes** to remove email requirement |
| 1.5.3.1 | Personal club contacts must not be published generally | Club-to-club | Role, division/competition need | Some contact data in captains/directories | Role-addressed portal messaging; no public directory | DPO and Club Liaison lead | Server policy; no human approval per message | No if portal follows purpose limitation |
| 1.5.5 | Urgency, subject, response time and agreement must be explicit | Club/league communications | Priority, subject, deadline, acknowledgement | Email convention | Required structured case fields and receipts | Secretary | Validation automated; agreement human | Clarification recommended |
| 3.1.1 | Every player must be a club member/registered category/loan | All GMCL competitions | Player, club membership, status | No player domain | Versioned registration requirements | Registration Officer | Validation automated; approval human | No |
| 3.1.2.1 | All players require Play-Cricket; selected players also GMCL form | Open-age | External member/registration status, GMCL application | Fixtures/scorecards only | Guided handoff and reconciliation | Registration Officer / ECB | Check automated where authorized; approval human | No |
| 3.1.2.1.2.1-.5 | GMCL form for new, returning, Cat 3, named pro and recent transfers | Open-age | History, category, pro status, transfer | External Google Form | Portal application state machine | Registration Officer | Requirement selection deterministic; decision human | Form/process policy update |
| 3.1.2.1.2.5.1.2 | Former club must email GMCL directly; forwards rejected | Transfers | Verified former-club official, debts, bans | External email | Verified portal clearance with identity and audit | GMCL Board/Registration Officer | Reminder automated; response human | **Yes** before replacement |
| 3.1.3 | Category-specific registration deadlines | Open-age | Category, competition, application/approval timestamps | Not supported | Versioned deadline rules and explanations | Registration Officer | Validation deterministic; override authorized/audited | No |
| 3.1.4 | Club Play-Cricket admin allocates relevant squads | Open-age | External squad state | Not supported | Guided external task and status evidence | Club PC Admin | Reminder/status import only | No |
| 3.5.1-.2 | Multi-team clubs maintain List A and sometimes List B | Senior/open-age | Team count, division, category, paid/pro status | Published CSV import and admin analysis | Club drafts, immutable submissions and GMCL decisions | Compliance Officer | Completeness checks automated; acceptance human | No |
| 3.5.2.2 | List A contains paid/pro, relevant Cat 3, then best Cat 1 to minimum five | Senior/open-age | Category, paid/pro status, performance evidence | Partial imported-list classification | Versioned rules-analysis finding | Compliance Officer | Potential issue automated; list judgment human | Clarification may be needed for data definition |
| 3.5.2.3-.4 | List B and total sizes depend on team count/division | Senior/open-age | Team count, first-XI division | Division overrides; 2026 logic | `TeamSeasonEntry` plus versioned requirements | Compliance Officer | Deterministic count check; acceptance human | No |
| 3.5.3 | Listed players must be available and not banned | Senior/open-age | Availability windows, bans, selection | Partial periods/exemptions | Effective-dated entry and availability evidence | Club then Compliance | Warning automated; final decision human | No |
| 3.5.4 | Lists submitted pre-season, accepted by GMCL and published | Senior/open-age | Submission, reviewer, rule version | Published external list only | Draft/submitted/reviewed/approved/published versions | Compliance Officer/Board | Never auto-approve/publish | No |
| 3.5.5 | Regular review; 30 June 50% threshold; 31 July final deadline | Senior/open-age | Appearances and personal league-game denominator | Appearance import and candidate review | Deterministic evidence with exact denominator | Compliance Officer | Candidate automated; human review | Data definition confirmation required |
| 3.5.6 | Conditional junior exemption | Senior/open-age juniors | Age reference date, county/academy/interleague appearances | Manual exemption records | Restricted evidence and effective dates | Compliance + Junior roles | Warning automated; approval/revocation human | Clarify source/refresh authority |
| 3.5.7 | Category 3 exemptions/restrictions | Saturday/Sunday | Category, list, team/competition, exemption | Partial exemption records | Versioned eligibility findings | Compliance Officer | Warning automated; decision human | Clarify wording at 3.5.7.1.3 |
| Penalties menu | Cards apply per team; three yellows convert; team red ordinal sets points | All affected teams | Append-only team ledger | Implemented in case/ledger model | Preserve source entries; derive club summaries | Compliance/Sanctions Officer | Calculation deterministic; approval/publish human | No |
| Penalties menu | Three club reds trigger Board intervention | Club aggregate | Team ledger grouped by club/season | Follow-up task support | Action-centre escalation linked to team records | Board/Compliance | Trigger automated; outcome human | No |
| Junior 7.5 | Junior entries, managers/coordinator and player registration | Junior | Adult contacts, team entries, player status | Not supported | Restricted junior module | Junior Administrator | Validation/reminders automated; approval human | No |
| Junior 7.5.3.3 vs Photo page | Junior rule says all players include a photo; photo page says juniors not in senior cricket should use a badge | Junior/senior crossover | Age, competition participation, approved image | Not supported | No automated rule until conflict resolved | Junior Board + DPO | Human-only pending clarification | **Published conflict requires decision** |
| Photo Required | Senior players need suitable Play-Cricket photo to be eligible | Senior | Approved photo/status | Not supported by API client | Match-day status only after agreement/DPIA | Registration/Compliance | Missing-photo warning; eligibility decision human | No, but process/API agreement required |
| Play-Cricket Players API | Default returns members with active squad roles; payload is member ID/name | Club | Site ID, token | No players client | Overnight minimal sync if agreement permits | ECB/Play-Cricket | Read sync only | External agreement |
| Play-Cricket API guidance | Low-traffic, non-real-time, minimize retrieval/retention | All integrations | Agreement, rate and cache policy | Season/scorecard reads | Scheduled deltas, cache and backoff | ECB + Technical Owner | Automated within contract | External agreement |
| Play-Cricket photo UI | Photos can be nominated and league-approved in Play-Cricket UI | Registered players | Photo and registration workflow | No integration | Prefer deep link; cache only with written permission | ECB + DPO | No automated copying without permission | External agreement |
| ICO children guidance | High privacy by default; compelling reason/best interests; DPIA | Junior data/photos | Purpose, lawful basis, recipients, controls | No junior module | Adult-contact-only v1, data minimization, DPIA | DPO/Safeguarding Officer | No broad automated disclosure | Policy/DPIA |
| ICO storage limitation | Retention must be purpose-based and reviewed | All personal data | Classification, decision/legal hold | Partial environment defaults | Approved per-class schedule and retention jobs | DPO/Records Owner | Automated deletion/anonymization after approval | Policy approval |

## Differences between published rules and the application

1. The application has no club portal or official communication record; Rule 1.5 remains email-based.
2. Registration and transfer requirements are not modeled. The application cannot verify or approve a GMCL registration.
3. The starred subsystem analyzes an externally published 2026 list but does not let clubs submit immutable versions for GMCL approval.
4. Sanction rules have an effective-dated policy model and team ledger, but the general audit and other rule domains do not yet use equivalent versioning.
5. The application has no junior team/player registration or photo rule implementation.
6. Play-Cricket scorecard reads cannot establish league registration or approved photograph status.
7. Fixture imports support reports and analysis, but fixture generation/publication rules are absent.

## Play-Cricket capability assessment

### Confirmed from public documentation and repository

- GMCL's current code can read match summaries and match details/scorecards when configured.
- The Players API can return member ID and name for club members with active squad roles; optional parameters broaden active roles or include historic squad roles.
- Play-Cricket has administrator UI workflows for registered-player status, category, DOB and photo approval.
- API keys require authorization/agreement and the service is intended for low-traffic or overnight use.

### Unconfirmed and therefore unavailable to the design

- League-registration status in the Players API.
- Player categories, DOB or photo fields in the Players API.
- Photograph URL/API, caching, redisplay or derivative rights.
- Registration nomination, approval or transfer write endpoints.
- Webhooks for players, registrations, photos, squads or fixtures.
- Contractual synchronization frequency, retention and junior-data terms for GMCL's key.

### Prohibited approach

No scraping, credential sharing, browser automation or storage of a club administrator's Play-Cricket credentials.

## Required external agreements and policy changes

| Dependency | Required evidence | Gate |
|---|---|---|
| Managed IdP | Contract, DPA, UK transfer position, security features, incident terms, exit/export plan | Before foundation implementation |
| Rule 1.5 | Approved wording and effective date for portal record, acknowledgement and email fallback | Before portal-primary communication |
| Rule 3.1 transfer | Approved portal clearance equivalence, identity standard and fallback | Before retiring direct former-club email |
| Play-Cricket | Current agreement, approved endpoints/data, rate, retention, photo and write position | Before player identity/registration integration |
| Junior data | DPIA, lawful basis, privacy notices, access and retention | Before junior pilot |
| Photos | DPIA, controller roles, transparency, source/approval, cache and deletion terms | Before any GMCL-hosted photo |
| Safeguarding | Approved minimal intake/routing and separately controlled retention | Before adding safeguarding category |
| Fixture process | Signed constraints, objectives, owners, source data and publication contract | Before solver prototype |

## Rule-versioning requirements

Each `RuleRelease` must record:

- immutable release ID and human version label;
- competition/season applicability and effective dates;
- source URL, retrieval timestamp and content hash;
- approval actor and activation date;
- superseded release;
- structured requirements used by deterministic rules;
- test corpus version;
- affected decisions.

Every `RuleDecision` and official workflow decision must persist the release ID, exact rule reference, material inputs, deterministic result, human reviewer, override reason and effective time. Activating a new release triggers future evaluations only; it never recomputes an approved historical decision silently.
