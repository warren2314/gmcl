# Open Questions and Decision Register

**Planning baseline:** 26 July 2026
**Status:** Decision gates for approval and delivery

The evidence labels defined in [00-executive-summary.md](00-executive-summary.md) apply throughout this document.

## Decision status definitions

- **Locked recommendation:** Architecture/product decision in this planning pack; changing it requires an explicit ADR/risk review.
- **Verified constraint:** Current repository or official source establishes the boundary.
- **External dependency:** Another organisation/agreement/source must answer.
- **Open question:** Named GMCL owner must decide at the specified gate.

Priorities:

1. **Blocking** — no dependent design/implementation may proceed.
2. **Required before implementation** — discovery/prototypes may continue, but production code for that policy must not.
3. **Required before production** — implementation may use safe defaults/disabled flags; live data/use is blocked.
4. **Can be deferred** — does not affect current safe scope.

## Answers to the 34 mandated questions

| # | Question | Answer / decision | Status, owner and delivery gate |
|---:|---|---|---|
| 1 | How is an individual invited and verified as a club administrator? | An authorized inviter creates a hashed, single-use, 24-hour invitation bound to club/role/intended email after official-contact evidence is checked. The person signs in/enrols strong auth with the managed IdP, accepts terms/recovery, and activation is atomic and audited. Generic mailboxes receive notices only. | **Locked recommendation.** Identity Product Owner + CLO; invitation evidence/playbook **required before implementation** |
| 2 | Who approves a club's primary administrator? | The initial and transferred Club Primary Administrator is approved by a GMCL Club Liaison Officer or Super Administrator against verified official-contact evidence. High-risk transfer uses step-up, dual notification and separation controls. | **Locked recommendation.** GMCL Board names approver roster; **blocking** for onboarding |
| 3 | How are administrators removed when their role ends? | Revoke or let the effective-dated assignment expire; increment security version and immediately invalidate affected sessions. Preserve audit/history and notify relevant contacts. Reappointment creates a new assignment. | **Locked recommendation.** Club governance owner defines notification/recertification cadence; **required before implementation** |
| 4 | Can one person administer more than one club? | Yes. One User may have several ClubMemberships/RoleAssignments, but the user selects one acting context and queries intersect that scope. | **Locked recommendation.** Authorization tests are a **production gate** |
| 5 | Which authentication method and why? | Managed OIDC Authorization Code + PKCE; passkeys preferred, password plus TOTP fallback; email links only onboarding/controlled recovery. It provides phishing resistance and specialist lifecycle capability while the app retains domain authorization. | **Locked recommendation**, subject to provider procurement/security/DPA. GMCL procurement + DPO/CISO; provider selection is **blocking** |
| 6 | Which actions require step-up? | Role grant/revoke/primary transfer, recovery/session reset, sensitive exports/doc/photo access, sanction approval/publication/overturn, fixture publication, activated rule/source changes, integration/AI configuration and break-glass. Recommended recent-auth window: 10 minutes. | **Locked recommendation.** Security + domain owners validate final action catalogue **before implementation** |
| 7 | Which club data can change directly? | Club-owned contacts/preferences, unsubmitted drafts, club responses/evidence and constraint inputs within policy. Verified official contacts/external mappings may trigger review. | **Locked recommendation.** Data owners approve field matrix **before implementation** |
| 8 | Which official data needs correction/appeal? | Submitted reports, missed findings/exemptions, cards/sanctions/deductions, approved starred lists/exemptions/findings, registration decisions and published fixtures. Preserve originals; apply version/event with requester, reason, reviewer and effective date. | **Locked recommendation.** Domain owners define appeal eligibility/deadlines **before each module** |
| 9 | How are yellow/red cards aggregated? | Store/effect them at team level. Compute season-specific club totals from ledger entries, showing included statuses and drill-down. Correct an underlying entry, never a stand-alone club number. | **Locked recommendation**, aligned with current sanction design. Compliance owner signs projection tests **before portal pilot** |
| 10 | How are internal notes guaranteed never to reach clubs? | Separate tables, RLS, repositories, attachment links, API types, search indexes, exports, notification builders and AI adapters. Club projections/ETags/counts ignore them; dedicated negative tests cover every channel. | **Locked recommendation.** Security owner; zero leakage is a **production gate** |
| 11 | How are cases assigned/escalated? | Configurable category queues; append-only primary-owner history, watchers, priority, deadline and status. Deadline/risk rules notify owner/duty role; reassignment/escalation records actor/reason. Safeguarding routes separately. | **Recommendation.** Operations leads must set queue map/targets/cover **before implementation** |
| 12 | How does the system operate while email is official? | Under current Rule 1.5, send/record required official email in parallel with portal case/notification. Portal read/ack is supplementary, not a replacement; maintain bounce/outage/manual fallback. | **Verified constraint + locked recommendation.** Rules owner; any portal-primary transition requires formal Rule 1.5/policy amendment **before production claim** |
| 13 | How are starred lists versioned? | One club/season list with immutable submitted/approved/published versions linked to exact Rule 3.5 release; amendments fork drafts; predecessor remains effective until successor effective date. | **Locked recommendation.** Compliance/rules owners approve release tables **before implementation** |
| 14 | How are potential starred breaches detected? | A deterministic service combines scorecard, effective approved list, exemptions, player match and versioned rules. It creates an idempotent potential finding with exact inputs, never a sanction; humans review. | **Locked recommendation.** Compliance owner approves rule/test corpus and shadow thresholds **before production** |
| 15 | What authority does Hawk have? | Advisory explanation only. It cannot amend, decide, approve, sanction, notify, call writes or publish. | **Locked recommendation.** No-action invariant is a **production gate** |
| 16 | How are Hawk answers restricted to authorized data? | Server policy authorizes fixed field-limited tools against trusted rules and deterministic tenant read models. No arbitrary SQL; internal notes/messages/attachments/photos/safeguarding excluded; validate citations and audit response. | **Locked recommendation.** Security/DPO approval and cross-tenant/prompt tests **before production** |
| 17 | Who receives junior communications? | Verified current adult club roles such as Club Junior Secretary, Club Safeguarding Officer where appropriate, Primary Administrator or other approved adult contacts. No direct junior accounts/messages in v1. | **Locked recommendation.** Junior/Safeguarding owners approve exact recipient policy **before implementation** |
| 18 | How is safeguarding kept separate? | A separately authorized route/service/store with explicit safeguarding roles, neutral ordinary-case handoff, no general search/export/AI, minimal data, access audit, special retention and DPIA. | **Locked boundary; open detailed design.** Safeguarding Lead + DPO; **blocking** for any safeguarding feature |
| 19 | Can Play-Cricket provide active players and photos through authorized API? | Documented Players API returns member ID/name and defaults to active squad-role members. It does not document photo data/URLs or GMCL eligibility. Existing app reads fixtures/scorecards. Photo capability remains unconfirmed. | **Verified limited read + external dependency.** ECB/Play-Cricket answer; **blocking** for photo implementation |
| 20 | Can photos legally/contractually be cached or redisplayed? | Not established by public docs or repository. Written agreement must state purpose, recipients, caching, retention, controller roles and junior terms; DPIA required. | **External dependency.** ECB/Play-Cricket + DPO; **blocking** |
| 21 | What if Play-Cricket cannot provide photos? | Keep photo feature disabled and use approved manual match-day identity process. Do not scrape, copy without rights or treat missing photo as ineligibility. A separately governed GMCL photo scheme would require DPIA/policy. | **Locked fallback.** Competition/Safeguarding/DPO owners approve manual process **before any pilot** |
| 22 | How is match-day access restricted? | Exact appointed fixture/role and short time window; server authorization per roster row; rate limits; no bulk export; no-store/private cache; short-lived image access; audit; immediate revoke. | **Locked recommendation.** Match Officials + Security set window/roles **before implementation** |
| 23 | What does active player mean? | Use separate facts: source active squad role, current GMCL registration, selected fixture roster, not transferred/suspended and competition eligibility. Never one ambiguous flag; show authority/as-at. | **Locked model.** Registration/Competition owners approve purpose-specific predicate **before implementation** |
| 24 | What fixture data is required? | Versioned teams/entries, competitions/divisions, calendar/cup/reserve dates, venues/pitches/capacity, shared resources, availability/blackouts, fixed/locked fixtures, withdrawals/byes, travel matrix, club limits/preferences, historic schedules/changes/overrides and publication mappings. | **Recommendation.** Fixture Administrator/data owners complete inventory; **blocking** for prototype |
| 25 | What does tightening mean measurably? | GMCL must choose measures such as total/95th/max travel, geographic outliers, calendar gaps, conflicts, home/away runs, fairness and change ripple, with baseline and tolerances. No target is invented in this pack. | **Open question.** GMCL Board + Fixture Lead; **blocking** for objective prototype |
| 26 | Which fixture constraints are mandatory? | The candidate catalogue includes required opponents/counts, no simultaneous team, resource availability/capacity, dates, locks, withdrawals/byes and binding sharing/start rules, but each must be confirmed with owner/effective rule. | **Open question.** Fixture Lead + competition owners; signed hard catalogue **blocking** |
| 27 | How are manual fixture decisions preserved? | First-class immutable locks/overrides with before/after, reason, actor, approver and plan version; subsequent runs consume approved locks and report impact/conflict. | **Locked recommendation.** Migration must capture historical/manual decisions **before prototype replay** |
| 28 | How are fixtures approved/published? | Solver creates candidate only; independent validator proves hard constraints; authorized independent human approves with step-up; controlled adapter/export publishes exact immutable version and reconciles; rollback is authorized publication. | **Locked recommendation.** Publication mechanism/approver is **blocking before production** |
| 29 | Can GMCL submit/update Play-Cricket registration through API? | No public write endpoint or webhook was identified and the repository client is read-only. Treat writes as unavailable unless Play-Cricket confirms an official endpoint/agreement. Scraping/shared credentials/browser automation are prohibited. | **External dependency.** ECB/Play-Cricket; **blocking for Level 3**, not for guided handoff |
| 30 | How can registration feel coherent with an external step? | One GMCL application reference, dynamic checklist, named task owner, supported deep link/instructions, saved progress and external status/as-at reconciliation. Clearly label leaving/returning; do not claim the portal performed the external action. | **Locked recommendation.** UX/process owners validate **before pilot** |
| 31 | Which current registration parts can portal replace? | Candidate replacements: applicable intake/forms, checklists, secure evidence, club confirmations, more-information responses, GMCL queues/decisions/status. Play-Cricket steps and Rule 3.1 direct email remain. Actual forms/sheets must be inventoried before named retirement. | **Recommendation + unavailable evidence.** Registration Process Owner; inventory **blocking** |
| 32 | Which changes require rule amendment? | Portal-primary official communication (Rule 1.5); in-portal replacement of direct former-club email (Rule 3.1); any new official acknowledgement/receipt, automated decision/publication or registration/photo rule that conflicts with published releases. Resolve junior photo guidance conflict before automation. | **Verified constraints/open policy.** Rules Committee; amendment must be published/effective **before dependent production behavior** |
| 33 | How is existing data migrated/reconciled? | Map clubs/teams/seasons/external IDs; verify people/roles instead of email inference; preserve reports/sanctions IDs; import provenance-labelled starred versions; quarantine ambiguous players; count/hash/sample reconciliation; parallel run; no indiscriminate sensitive attachments. | **Locked approach.** Data Migration Lead + domain owners approve mappings/reconciliation **before each module** |
| 34 | How is rollout non-disruptive in-season? | Foundation first; read-only pilot clubs; server flags; legacy captain/official routes in parallel; scoped writes later; support/training/monitoring; reconciliation; rule-aware cutovers; disable new feature on rollback while preserving submitted history. | **Locked recommendation.** Programme/Operations owners set pilot/cutover/blackout policy **before production** |

## Blocking decisions

| ID | Decision/question | Decision owner | Evidence required | Blocks |
|---|---|---|---|---|
| B-01 | Select managed OIDC provider and approve security, UK GDPR DPA, subprocessor/data-location and exit terms | GMCL procurement, Security Lead, DPO | Capability assessment against [05-authentication-adr.md](05-authentication-adr.md), contract/DPA | Foundation build/live identity |
| B-02 | Name CLO/Super Admin primary-administrator approvers and official-contact verification evidence/process | GMCL Board/Operations | Authoritative contact sources, approval/appeal/support playbook | Club onboarding |
| B-03 | Approve final role grantors, scopes, expiry, delegation and separation of duties | GMCL Board and each functional lead | Signed role/permission matrix | Authorization policies |
| B-04 | Reconcile authoritative club/team/season/competition identifiers and resolve ambiguous mappings | Data Owner + competition leads | Source inventory, mapping exceptions, sign-off | Club read models and all later tenancy |
| B-05 | Approve message categories, queues, owners, targets, emergency email and retention | Operations + Rules/Records owners | Current mail/case process and Rule 1.5 interpretation | Secure messaging |
| B-06 | Approve rule release governance and machine decision tables for reports, sanctions, starred and registration | Rules Committee + domain leads | Exact sources, effective dates, historic examples | Deterministic automation |
| B-07 | Complete safeguarding/junior DPIA and separate route/retention/lawful-basis design | Safeguarding Lead + DPO | Data flow, processor, incident and rights analysis | Safeguarding and live junior module |
| B-08 | Resolve conflict between junior Rule 7.5.3.3 and `Photo Required` guidance | Rules Committee + Safeguarding/DPO | Published authoritative interpretation/effective date | Automated junior photo requirements |
| B-09 | Obtain written ECB/Play-Cricket player/photo/retention/controller terms | GMCL contract owner + ECB/Play-Cricket + DPO | Agreement, endpoint/export schema, limits | Player-photo/identity |
| B-10 | Inventory exact registration forms, sheets, steps, owners, exceptions, volumes and decisions | Registration Process Owner | Artefacts/interviews/field map | Registration redesign/form retirement |
| B-11 | Map fixture process/data/manual decisions/publication and approve hard constraints/measures | Fixture Lead + Board | Artefacts, questionnaire, historical corpus | Solver prototype |

## Required before implementation

| ID | Open question | Owner | Required decision |
|---|---|---|---|
| I-01 | Can ordinary Club Administrators invite peers, or only the Primary Administrator? | Club Governance Owner | Grant/delegation policy and notification |
| I-02 | What are exact session idle/absolute lifetimes and accessible fallback conditions? | Security + Operations | Risk/usability decision; recommended 30/60-minute idle, 12-hour absolute as starting point |
| I-03 | Which role/category may view each attachment/document type and export? | Domain owners + DPO | Field/object/export purpose matrix |
| I-04 | Which correction/appeal types, deadlines and independent approvers apply? | Reports, Compliance, Registration, Fixture leads | Versioned workflow policy |
| I-05 | Which dashboard actions/metrics and definitions are operationally useful? | Club Product Owner | Tested metric/action contract |
| I-06 | What recipient resolution happens when no current club role exists? | Operations | Official fallback and escalation without creating shared login |
| I-07 | What case priorities, targets and schedule/reminder rules apply? | Operations owners | Effective-dated configuration and cover |
| I-08 | What is the authoritative competition/division/team-season model including mid-season movement? | Competition/Data Owner | Identifiers, effective-date semantics |
| I-09 | Who reviews/approves/publishes starred lists and overrides? | Compliance + Rules owners | Separation, deadlines and publication audience |
| I-10 | What match-scorecard/player identity reconciliation confidence is sufficient for a potential finding? | Compliance/Data Owner | Human queue thresholds; no auto-confirmation |
| I-11 | Which registration routes require documents, categories, photos and deadlines in each competition/season? | Registration + Rules owners | Machine-readable decision table |
| I-12 | What fixture distance method, fairness measures and weights are approved? | Fixture Lead + Board | Versioned objective definitions |

## Required before production

| ID | Open question | Owner | Required evidence |
|---|---|---|---|
| P-01 | Approved retention/lawful basis for every data class | DPO, Records Owner, domain owners | Signed schedule and privacy notices |
| P-02 | Approved RPO/RTO, backup scope and restore evidence | Platform/Operations | Successful production-like restore exercise |
| P-03 | Who monitors security/integration/queue alerts and responds out of hours? | Operations/Security | On-call, thresholds, runbooks and tabletop |
| P-04 | What pilot clubs/users represent real accessibility and workflow conditions? | Product/Club Liaison | Recruitment, consent/briefing, success/stop criteria |
| P-05 | What support route handles passkey/TOTP recovery without bypass? | Support/Security | Two-person recovery rehearsal and accessible playbook |
| P-06 | What processor retention/no-training/international-transfer terms apply to Hawk? | DPO/Procurement/Security | DPA/security review; sensitive sources remain excluded |
| P-07 | What false-positive/ambiguity threshold permits showing potential findings to clubs? | Compliance/Product | Shadow-mode measured results and error review |
| P-08 | Which message/case data can be searched and for how long? | Records/DPO/Operations | Index classification and deletion verification |
| P-09 | What is the approved manual fallback for provider/email/photo/fixture publication outages? | Domain Operations | Exercised incident and reconciliation playbook |
| P-10 | What exact external publication/import method is used for fixtures and how is rollback performed? | Fixture Lead + Play-Cricket contract owner | Supported format/API, permissions and replay test |
| P-11 | Does Rule 1.5 need amendment and on what transition date can portal become primary? | Rules Committee/Board | Published policy/rule and communications; until then email remains primary |
| P-12 | Is Rule 3.1 amended to accept verified portal clearance? | Rules Committee/Registration | Published amendment; until then direct email remains |

## Can be deferred

| ID | Question | Owner | Current safe decision |
|---|---|---|---|
| D-01 | Should optional Google/Microsoft federation be shown for suitable users? | Identity Product Owner | Managed IdP may support it later; never sole method or authorization source |
| D-02 | Should ordinary users receive optional digest notifications? | Product/Operations | Mandatory notices remain; add preferences after core delivery is reliable |
| D-03 | Should club users get limited exports beyond on-screen history? | Data owners/DPO | Denied by default; add purpose-specific exports later |
| D-04 | Should Hawk ever process message/document content? | DPO/Security/domain owner | No; would require new use case, DPIA and provider controls |
| D-05 | Is a GMCL-hosted photo scheme needed if Play-Cricket cannot supply photos? | Board/DPO/Safeguarding | Use manual fallback; treat any scheme as separate project |
| D-06 | Should a MIP/hybrid fixture solver be benchmarked? | Fixture technical lead | CP-SAT prototype first; benchmark only if evidence warrants |
| D-07 | Should juniors ever have direct accounts? | Board/Safeguarding/DPO | Out of scope; separate age-appropriate design project |
| D-08 | Should legacy administrator/captain auth be retired entirely? | Product/Security/Operations | Preserve through safe adoption; decide only after measured migration and fallback |

## Confirmed architectural decisions

1. Modular Go monolith and PostgreSQL remain; no broad rewrite.
2. Captain-report functionality is preserved throughout rollout.
3. Named accounts only; memberships/roles are separate and multi-club capable.
4. Managed OIDC, passkeys preferred, password plus TOTP fallback.
5. Application-owned season-aware authorization; server-side deny by default.
6. Immediate session revocation and sensitive-action step-up.
7. RLS on new club-private tables as defence in depth.
8. Team-level card/sanction ledger and derived club totals.
9. Internal notes structurally separate from club-visible messages.
10. Email remains official under Rule 1.5.
11. Direct former-club email remains under Rule 3.1.
12. Starred lists/rules are immutable, effective-dated and season/release versioned.
13. Potential findings are deterministic and human-reviewed.
14. Hawk is advisory, cited, tenant-scoped and has no action authority.
15. Junior v1 communicates with verified adults; safeguarding is separate/DPIA-gated.
16. Play-Cricket fixture/scorecard reads and limited Players API fields are the only confirmed data capabilities in scope.
17. Registration begins with guided handoff/reconciliation, not unsupported writes.
18. Fixture CP-SAT work begins only after discovery and never auto-publishes.

## Rules and policies that may require amendment

| Rule/policy | Current constraint | Amendment/clarification needed for |
|---|---|---|
| GMCL Rule 1.5 | Email is primary official communication | Portal-primary receipt/record, response clocks, outages and fallback |
| GMCL Rule 3.1 | Direct email from responsible former-club officer; forwarded email not accepted | Verified in-portal transfer clearance |
| GMCL Rule 3.5 governance | Current release applies but must not become permanent code | Machine decision table, effective releases and any workflow/publication change |
| Junior Rule 7.5.3.3 vs Photo Required guidance | Published sources conflict on junior photographs | Any automated requirement/eligibility outcome |
| Case/acknowledgement policy | Portal acknowledgements have no defined official consequence | Mandatory acknowledgement/receipt/escalation effect |
| AI policy | No published authority model identified | Production Hawk provider/data use and user wording; architecture keeps advisory only |
| Fixture approval/publication policy | Current process unavailable | Formal candidate approval, publication, rollback and objective governance |
| Registration process/forms | Exact operational artefacts unavailable | Portal replacement/retirement and any changed evidence/deadline |

## Unavailable repository, credentials and source information

The plan did not have:

- the current managed identity provider because none is selected;
- production database contents or production audit/retention statistics;
- the GMCL–Play-Cricket/ECB API agreement, key scope, limits or data-processing terms;
- any private player/photo/registration API specification;
- current registration Google Forms, response sheets, scripts or retention/process owners;
- current fixture spreadsheets/scripts, constraint logs, manual overrides, publication method or full historical corpus;
- processor contracts/DPAs for IdP, object storage, scanning or an external AI provider;
- finalized GMCL role grantors, case targets, retention schedule, RPO/RTO or incident/on-call roster;
- a definitive published resolution of the junior photograph inconsistency.

No credentials, tokens, personal data or secret values were printed or copied into this planning pack.

## Approval checklist before development begins

GMCL should explicitly approve:

- the locked architecture decisions above;
- provider procurement and DPA path;
- role owners/grantors and first-primary verification;
- authoritative club/team/season data reconciliation;
- MVP scope as Epics 1–3;
- Rule 1.5 parallel-email interpretation;
- internal-note structural separation;
- privacy/retention/DPIA work owners;
- rule release/decision-table governance;
- the decision to keep player identity externally blocked, registration at guided handoff and fixtures at discovery until their evidence gates pass.
